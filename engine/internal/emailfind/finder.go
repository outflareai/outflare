package emailfind

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/mail"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/mailbox"
	"github.com/outflareai/outflare/engine/internal/outcomes"
	"github.com/outflareai/outflare/engine/internal/pipeline"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/usagelimits"
	"github.com/outflareai/outflare/engine/internal/verify"
)

// The finder tier: the workspace's own connected email finders (connectors
// with the data.find_email capability, such as Treg, Prospeo or Findymail).
// It runs after the free tiers and before the workspace's verifier, on the
// workspace's own connections only, and is billed to that workspace's
// account with the provider.
const (
	// finderConfidence is the confidence of an address a connected finder
	// returned without saying it checked the mailbox. It sits above
	// highConfidence: a provider that looked the person up is a stronger
	// signal than any shape we would guess ourselves, so with no verifier
	// connected the address is found, not risky. It stays below what a
	// checked address gets, because nobody has confirmed the mailbox.
	finderConfidence = 80
	// finderVerifiedConfidence is for an address the provider says it
	// checked: just under first-party history (95), which is our own proof.
	finderVerifiedConfidence = 90

	// finderCallTimeout bounds one call to one connection. Some finders
	// hold a request for a minute or more and may bill an answer we gave
	// up on, and Treg asks up to four providers one after another for one
	// person (at most 150 seconds in all, 75 for each), so it is longer
	// than the slowest of them. Each call gets the whole of it: when one
	// limit covered a person's calls to every provider, a slow first
	// provider left the next too little time to answer, and that timeout
	// was retried and billed.
	finderCallTimeout = 160 * time.Second
	// finderItemTimeout bounds everything one person can take at the
	// finders, whatever the number of providers: two calls at their
	// slowest.
	finderItemTimeout = 6 * time.Minute
	// finderBudget is how long one Resolve keeps starting finder calls.
	// People not reached by then are answered pending and asked on the
	// next call, so one batch takes at most finderBudget plus
	// finderItemTimeout at this tier (seven minutes), which a pipeline
	// stage's renewed lease covers.
	finderBudget = 60 * time.Second
	// finderWait is how long a pending person waits when no limiter said.
	finderWait = 30 * time.Second
	// finderConcurrency is how many people of one batch are asked about at
	// once. A batch is at most MaxBatch (50) people and a slow finder takes
	// half a minute or more for one, so a low number left most of a batch
	// waiting for the next call. Each connection's own pace (finderRate)
	// is what keeps a provider from being asked too fast.
	finderConcurrency = 16
	// finderCacheMaxAge is how long a provider's answer is reused: the
	// list builder's cache, which this tier shares (lists.cacheMaxAge).
	finderCacheMaxAge = 90 * 24 * time.Hour
)

// finderRate is how fast one connection is asked, unless its connector
// names its own pace (connectors.FindPacer: Blitz allows 600 a minute). A
// provider that answers with a rate limit halves the pace for five minutes,
// down to an eighth (pipeline.Limits), so the default stays low enough
// that an eighth of it is slow enough for a gateway such as Treg.
var finderRate = pipeline.Rate{PerMinute: 120, Burst: MaxBatch}

// finderPace is how fast one connection to c is asked: the connector's own
// pace when it names one, else finderRate.
func finderPace(c connectors.Connector) pipeline.Rate {
	rate := finderRate
	if p, ok := c.(connectors.FindPacer); ok && p.FindsPerMinute() > 0 {
		rate.PerMinute = p.FindsPerMinute()
	}
	return rate
}

// finderConn is one of the workspace's finder connections, opened on first
// use in a Resolve.
type finderConn struct {
	id   string
	once sync.Once
	o    *connectors.Opened
	err  error
}

// finderGroup is the workspace's connections to one finder. They are the
// same provider, so one answer stands for all of them: calls are spread
// across them, and the next is asked only when one cannot be.
type finderGroup struct {
	connector string
	conns     []*finderConn
	// profile: the connector looks a person up by their LinkedIn profile
	// (connectors.ProfileFinder). profileOnly: and by nothing else.
	profile, profileOnly bool
}

// finderAnswer is what the finder tier found out about one person.
type finderAnswer struct {
	email    string // the address found, lower case; empty for a miss
	verified bool   // the provider says it checked the mailbox
	provider string // the connector that found it
	cost     Cost   // what the lookups cost; no Units when nothing was charged
	// code is empty when a finder answered (a hit or a miss); otherwise
	// pending, retry or stop, with wait or stopCode.
	code     Code
	stopCode string
	wait     time.Duration
}

// finders lists the workspace's connected email finders, grouped by
// connector in the order they were first connected. Only this workspace's
// own connections, and only those that serve the brand the work is for
// when the context names one.
func (s *Service) finders(ctx context.Context, ws string) ([]*finderGroup, error) {
	if s.Store == nil || s.Store.Registry == nil {
		return nil, nil
	}
	list, err := s.Store.List(ctx, s.Pool, ws, "")
	if err != nil {
		return nil, err
	}
	list = connectors.ForBrand(list, outcomes.BrandFrom(ctx))
	sort.SliceStable(list, func(i, j int) bool {
		if !list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].CreatedAt.Before(list[j].CreatedAt)
		}
		return list[i].ID < list[j].ID
	})
	var groups []*finderGroup
	byConnector := map[string]*finderGroup{}
	for _, i := range list {
		if i.Status != connectors.StatusConnected {
			continue
		}
		if d, ok := s.Store.Registry.Definition(i.Connector); !ok || !d.Has(connectors.CapFindEmail) {
			continue
		}
		c, ok := s.Store.Registry.Get(i.Connector)
		if !ok {
			continue
		}
		if _, ok := c.(connectors.PersonEnricher); !ok {
			continue
		}
		g := byConnector[i.Connector]
		if g == nil {
			g = &finderGroup{connector: i.Connector}
			if pf, ok := c.(connectors.ProfileFinder); ok {
				g.profile, g.profileOnly = true, pf.ProfileOnly()
			}
			byConnector[i.Connector] = g
			groups = append(groups, g)
		}
		g.conns = append(g.conns, &finderConn{id: i.ID})
	}
	return groups, nil
}

// wantsFinder says whether the free tiers left a person for the finder:
// nothing proven, and either no address, one known to be undeliverable, or
// a guess below highConfidence.
func wantsFinder(it FindItem, r ItemResult) bool {
	if strings.TrimSpace(it.FirstName) == "" || strings.TrimSpace(it.LastName) == "" || finderDomain(it.Domain) == "" {
		return false // a finder needs a name and a company domain
	}
	switch r.Code {
	case CodeVerified, CodeStop:
		return false
	case CodeNotFound, CodeUndeliverable:
		return true
	}
	return r.Email == "" || r.Confidence < highConfidence
}

func finderDomain(d string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(d)), "www.")
}

// askFinders asks the workspace's connected finders about each person the
// free tiers did not settle. The answers line up with res; nil means the
// finder tier does not apply to that person. With no finder connected it
// returns nil and nothing changes.
func (s *Service) askFinders(ctx context.Context, ws string, items []FindItem, res []ItemResult) ([]*finderAnswer, error) {
	var need []int
	for i := range res {
		if i < len(items) && wantsFinder(items[i], res[i]) {
			need = append(need, i)
		}
	}
	if len(need) == 0 {
		return nil, nil
	}
	groups, err := s.finders(ctx, ws)
	if err != nil || len(groups) == 0 {
		return nil, err
	}
	// A test workspace never spends a provider's credits (as in verify).
	if test, err := mailbox.IsTestWorkspace(ctx, s.Pool, ws); err != nil || test {
		return nil, err
	}
	out := make([]*finderAnswer, len(res))
	deadline := time.Now().Add(finderBudget)
	sem := make(chan struct{}, finderConcurrency)
	var wg sync.WaitGroup
	for _, i := range need {
		sem <- struct{}{}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			<-sem
			out[i] = &finderAnswer{code: CodePending, wait: finderWait}
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			a := s.askFinder(ctx, ws, items[i], groups)
			out[i] = &a
		}(i)
	}
	wg.Wait()
	return out, nil
}

// turn returns the workspace's next place in the rotation of its finder
// connections.
func (s *Service) turn(ws string) int {
	s.finderMu.Lock()
	defer s.finderMu.Unlock()
	if s.finderTurn == nil {
		s.finderTurn = map[string]int{}
	}
	n := s.finderTurn[ws]
	s.finderTurn[ws] = n + 1
	return n
}

// What one call on one connection came to.
const (
	finderAnswered = iota // the provider answered: a hit or a miss
	finderBusy            // our own rate limit or breaker says wait
	finderRefused         // refused key, quota or usage limit: try the next connection
	finderBroken          // a transient failure
	// finderLimited: the provider has no room for the call right now and
	// charged nothing (connectors.IsBusy). The account is fine and nothing
	// is known about the person: ask again after the wait.
	finderLimited
)

// finderOrder is the order the providers are asked in for one person.
// Without a LinkedIn profile it is the order they were connected in, less
// the providers that can only read a profile. With one, the providers that
// read nothing else go first (Blitz), then the others that can use a
// profile (Treg), then the rest, each set in the order it was connected:
// a lookup by profile names the very person, where a name and a domain
// only describe them, and it is the cheapest and fastest answer there is.
func finderOrder(groups []*finderGroup, hasProfile bool) []*finderGroup {
	out := make([]*finderGroup, 0, len(groups))
	if !hasProfile {
		for _, g := range groups {
			if !g.profileOnly {
				out = append(out, g)
			}
		}
		return out
	}
	for _, want := range []func(*finderGroup) bool{
		func(g *finderGroup) bool { return g.profileOnly },
		func(g *finderGroup) bool { return g.profile && !g.profileOnly },
		func(g *finderGroup) bool { return !g.profile },
	} {
		for _, g := range groups {
			if want(g) {
				out = append(out, g)
			}
		}
	}
	return out
}

// finderProfileKey names what a provider said about a person's LinkedIn
// profile at one company in the cache of provider answers. It is not the
// key of the name alone (PersonQuery.CacheKey), so a provider that had no
// address for the name is asked again once the profile is known. The
// domain is part of it because the answer is: an address at that company.
func finderProfileKey(profile, domain string) string {
	return "linkedin:" + strings.ToLower(profile) + "|" + domain
}

// askFinder asks the finders about one person, provider by provider
// (finderOrder): a remembered answer first, then one of the provider's
// connections, taking turns. A miss moves on to the next provider, a hit
// ends it.
//
// A provider that can read a LinkedIn profile gets the person's, and its
// answer is remembered under the profile (finderProfileKey). An address it
// found earlier from the name alone still stands and is not paid for
// again; a miss from the name alone does not, since the profile may find
// what the name did not. Every other provider is asked, and remembered, by
// name and domain exactly as before.
func (s *Service) askFinder(ctx context.Context, ws string, it FindItem, groups []*finderGroup) finderAnswer {
	byName := connectors.PersonQuery{FirstName: strings.TrimSpace(it.FirstName), LastName: strings.TrimSpace(it.LastName),
		Domain: finderDomain(it.Domain), Company: strings.TrimSpace(it.Company)}
	nameKey := byName.CacheKey()
	profile := connectors.LinkedInProfile(it.LinkedInURL)
	cctx, cancel := context.WithTimeout(ctx, finderItemTimeout)
	defer cancel()

	var a finderAnswer
	turn := s.turn(ws)
	answered, busy := false, false
	stop := ""
	wait := time.Duration(0)
	for _, g := range finderOrder(groups, profile != "") {
		q, key := byName, nameKey
		byProfile := profile != "" && g.profile
		if byProfile {
			q.LinkedInURL, key = profile, finderProfileKey(profile, byName.Domain)
		}
		e, ok := s.finderCached(ctx, ws, g.connector, key)
		if !ok && byProfile {
			// Only an address counts from the lookup by name alone.
			if e, ok = s.finderCached(ctx, ws, g.connector, nameKey); ok && finderEmail(e) == "" {
				ok = false
			}
		}
		if ok {
			answered = true
			if email := finderEmail(e); email != "" {
				a.email, a.verified, a.provider = email, e.Person.EmailStatus == connectors.EmailVerified, g.connector
				if a.cost.Provider == "" {
					a.cost.Provider = g.connector // remembered: nothing charged
				}
				return a
			}
			continue
		}
	conns:
		for k := range g.conns {
			c := g.conns[(turn+k)%len(g.conns)]
			e, how, code, w := s.callFinder(ctx, cctx, ws, g.connector, c, q, key)
			switch how {
			case finderAnswered:
				answered = true
				if units := int(math.Ceil(e.Credits - 1e-9)); units > 0 {
					a.cost = Cost{Provider: g.connector, Connection: c.id, Units: a.cost.Units + units, Micros: a.cost.Micros + micros(e.CostMicros)}
				}
				if email := finderEmail(e); email != "" {
					a.email, a.verified, a.provider = email, e.Person.EmailStatus == connectors.EmailVerified, g.connector
					if a.cost.Provider == "" {
						a.cost.Provider = g.connector
					}
					return a
				}
				break conns // the provider has no address: its other connections won't either
			case finderBusy, finderLimited:
				// Our own pace, or the provider has no room right now:
				// either way it is asked again, never taken for a miss.
				if !busy || w < wait {
					wait = w
				}
				busy = true
			case finderRefused:
				stop = worseStop(stop, code)
			default:
				a.code = CodeRetry
				return a
			}
		}
	}
	switch {
	case busy:
		// A connected finder could not be asked yet: ask again rather
		// than settle for a guess.
		a.code, a.wait = CodePending, max(wait, time.Second)
	case answered:
		// Every finder that answered had no address: a miss.
	case stop != "":
		a.code, a.stopCode = CodeStop, stop
	}
	return a
}

// worseStop keeps the stop code that best says what to fix when several
// connections refused: out of quota, then a usage limit, then a refused key.
func worseStop(have, code string) string {
	rank := map[string]int{"": 0, "connector_needed": 1, "usage_limit_reached": 2, "connector_quota": 3}
	if rank[code] > rank[have] {
		return code
	}
	return have
}

// finderEmail returns the address in a provider's answer, or "" when it has
// none. The answer is data from outside: only a plain address is accepted.
func finderEmail(e connectors.Enriched) string {
	email := strings.ToLower(strings.TrimSpace(e.Person.Email))
	if !e.Found || email == "" || len(email) > 320 {
		return ""
	}
	if a, err := mail.ParseAddress(email); err != nil || strings.ToLower(a.Address) != email {
		return ""
	}
	return email
}

// finderCached reads a provider's remembered answer for a person, so a
// retry of the same item, or a list that already looked them up, is never
// paid for twice.
func (s *Service) finderCached(ctx context.Context, ws, connector, key string) (connectors.Enriched, bool) {
	if key == "" {
		return connectors.Enriched{}, false
	}
	var found bool
	var person []byte
	err := s.Pool.QueryRow(ctx, `SELECT found, person FROM enrichment_cache
		WHERE workspace_id = $1 AND provider = $2 AND lookup_key = $3 AND fetched_at > $4`,
		ws, connector, key, time.Now().Add(-finderCacheMaxAge)).Scan(&found, &person)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) && s.Log != nil {
			s.Log.WarnContext(ctx, "email finder cache read", "workspace_id", ws, "err", err)
		}
		return connectors.Enriched{}, false
	}
	var p connectors.Person
	_ = json.Unmarshal(person, &p)
	return connectors.Enriched{Person: p, Found: found}, true
}

func (s *Service) finderEvent(ws, connector, id string, props map[string]any) track.Event {
	p := map[string]any{"connector": connector, "integration_id": id}
	for k, v := range props {
		p[k] = v
	}
	return track.Event{Name: "email.finder_asked", WorkspaceID: ws, ActorType: "system", Surface: "job", Props: p}
}

// finderReply is what one call on one connection came back with, for the
// event, the spend ledger and the cache of provider answers.
type finderReply struct {
	hit      bool    // an address came back
	verified bool    // the provider says it checked the mailbox
	credits  float64 // what the provider charged
	micros   float64 // what the provider said it cost, in millionths of a dollar
	found    bool    // the cache's found column
	person   any     // the cache's person column
}

// finderAsk is one call to make on one connection: the whole provider
// (callFinder) or one of its steps (callStep).
type finderAsk struct {
	// step names the step asked, or "" for the whole provider. It is a
	// property of the events.
	step string
	// key is where the answer is remembered; "" remembers nothing.
	key       string
	byProfile bool
	// can says whether the connector can make the call at all.
	can func(connectors.Connector) bool
	// call asks the provider.
	call func(ctx context.Context, o *connectors.Opened) (finderReply, error)
	// timedOut, when set, is the answer for this ask when the provider
	// gave none in time. It is never remembered (callConn).
	timedOut *finderReply
}

// callFinder asks one connection about a person. ctx is the caller's;
// cctx carries the person's time limit and is only for the provider call,
// which also gets its own (finderCallTimeout).
// It respects the connection's usage limits and its rate limit and circuit
// breaker, remembers the answer, and books what it cost in the spend
// ledger. code is the stop code when the connection refused.
func (s *Service) callFinder(ctx, cctx context.Context, ws, connector string, c *finderConn, q connectors.PersonQuery, key string) (e connectors.Enriched, how int, code string, wait time.Duration) {
	_, how, code, wait = s.callConn(ctx, cctx, ws, connector, c, finderAsk{
		key: key, byProfile: q.LinkedInURL != "",
		can: func(c connectors.Connector) bool { _, ok := c.(connectors.PersonEnricher); return ok },
		call: func(callCtx context.Context, o *connectors.Opened) (finderReply, error) {
			var err error
			e, err = o.Connector.(connectors.PersonEnricher).EnrichPerson(callCtx, o.Client, o.Creds, q)
			if err != nil {
				e = connectors.Enriched{}
				return finderReply{}, err
			}
			return finderReply{hit: finderEmail(e) != "", verified: e.Person.EmailStatus == connectors.EmailVerified,
				credits: e.Credits, micros: e.CostMicros, found: e.Found, person: e.Person}, nil
		},
	})
	return e, how, code, wait
}

// callConn makes one call on one connection (finderAsk), for callFinder
// and callStep.
func (s *Service) callConn(ctx, cctx context.Context, ws, connector string, c *finderConn, ask finderAsk) (r finderReply, how int, code string, wait time.Duration) {
	event := func(props map[string]any) track.Event {
		ev := s.finderEvent(ws, connector, c.id, props)
		if ask.step != "" {
			ev.Props["step"] = ask.step
		}
		return ev
	}
	fail := func(code string, err error, outcome string) {
		ev := event(map[string]any{"kind": strings.TrimPrefix(code, "connector_")})
		ev.Name, ev.Outcome = "email.finder_failed", outcome
		track.Fail(ctx, s.Log, ev, code, err)
	}
	remember := func(r finderReply) {
		if ask.key == "" {
			return
		}
		b, _ := json.Marshal(r.person)
		if _, err := s.Pool.Exec(ctx, `
			INSERT INTO enrichment_cache (workspace_id, provider, lookup_key, found, person) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (workspace_id, provider, lookup_key) DO UPDATE SET found = EXCLUDED.found, person = EXCLUDED.person, fetched_at = now()`,
			ws, connector, ask.key, r.found, b); err != nil {
			fail("internal", err, track.Error)
		}
	}
	c.once.Do(func() { c.o, c.err = s.Store.Open(ctx, s.Pool, ws, c.id) })
	if c.err != nil {
		switch {
		case errors.Is(c.err, connectors.ErrNotFound):
			return r, finderRefused, "", 0 // disconnected meanwhile: as if it were not there
		case connectors.KindOf(c.err) == connectors.ErrAuth:
			fail("connector_auth", c.err, track.Failure)
			return r, finderRefused, stopCode(connectors.ErrAuth), 0
		}
		fail("connector_unavailable", c.err, track.Error)
		return r, finderBroken, "", 0
	}
	if !ask.can(c.o.Connector) {
		return r, finderRefused, "", 0
	}
	if err := usagelimits.Check(ctx, s.Pool, ws, c.id, "credit", 1); err != nil {
		var le *usagelimits.Error
		if errors.As(err, &le) {
			fail(le.Code(), err, track.Failure)
			return r, finderRefused, le.Code(), 0
		}
		fail("internal", err, track.Error)
		return r, finderBroken, "", 0
	}
	limits := pipeline.Limits{Q: s.Pool}
	account := pipeline.Key("integration", c.id+":find_email")
	w, err := limits.Take(ctx, ws, account, finderPace(c.o.Connector))
	if err != nil {
		fail("internal", err, track.Error)
		return r, finderBroken, "", 0
	}
	if w > 0 {
		return r, finderBusy, "", w
	}
	report := func(outcome string) {
		if err := limits.Report(ctx, account, outcome); err != nil && s.Log != nil {
			s.Log.WarnContext(ctx, "email finder limit report", "workspace_id", ws, "err", err)
		}
	}
	started := time.Now()
	callCtx, cancel := context.WithTimeout(cctx, finderCallTimeout)
	r, err = ask.call(callCtx, c.o)
	cancel()
	if err != nil {
		if again, ok := connectors.IsBusy(err); ok {
			// The provider has no room for the call right now and charged
			// nothing. That is not the account's quota (nothing to fix,
			// so never a stop) and not a broken service (so it never
			// opens the breaker): the person is asked about again.
			//
			// Only a provider that asked this account for fewer calls
			// slows the connection's pace. One whose capacity everyone
			// else fills does not: the pace is shared by every step of
			// the connection, and slowing it would hold back the very
			// steps a caller goes on to while this one is busy.
			throttled := connectors.IsThrottled(err)
			if throttled {
				report(pipeline.RateLimited)
			}
			ev := event(map[string]any{"kind": "busy", "throttled": throttled, "retry_after_ms": again.Milliseconds()})
			ev.Name, ev.Outcome = "email.finder_failed", track.Failure
			track.Fail(ctx, s.Log, ev, "connector_unavailable", err)
			return finderReply{}, finderLimited, "", max(again, time.Second)
		}
		switch kind := connectors.KindOf(err); kind {
		case connectors.ErrAuth:
			// Shows on the Connectors page; the connection is left out
			// until it is reconnected.
			_ = s.Store.Checked(ctx, s.Pool, ws, c.id, nil, err)
			fail("connector_auth", err, track.Failure)
			return finderReply{}, finderRefused, stopCode(kind), 0
		case connectors.ErrQuota:
			report(pipeline.RateLimited)
			fail("connector_quota", err, track.Failure)
			return finderReply{}, finderRefused, stopCode(kind), 0
		case connectors.ErrInvalid:
			// The provider cannot look this person up: its answer is
			// that it has no address. Nothing was charged.
			fail("connector_invalid", err, track.Failure)
			return finderReply{}, finderAnswered, "", 0
		default:
			if connectors.IsTimeout(err) {
				// The provider got no answer in its own time for this
				// person. That is this lookup, not the service: retrying
				// it now would run just as long and may be billed again
				// (it retried five times and opened the breaker), so for
				// this ask it counts as no address from this provider.
				// It is never remembered: a timeout says nothing about
				// the person, and remembering it would read as "no email"
				// for 90 days. Only the provider's own answer is kept.
				fail("connector_unavailable", err, track.Failure)
				if ask.timedOut != nil {
					return *ask.timedOut, finderAnswered, "", 0
				}
				return finderReply{}, finderAnswered, "", 0
			}
			report(pipeline.Failed)
			fail("connector_unavailable", err, track.Error)
			return finderReply{}, finderBroken, "", 0
		}
	}
	report(pipeline.Succeeded)

	// Remember the answer before anything else, so a retry never pays
	// again; then book what it cost.
	remember(r)
	if r.credits > 0 {
		units, id := r.credits, c.id
		e := outcomes.Entry{
			WorkspaceID: ws, Category: outcomes.CategoryData, Provider: connector,
			IntegrationID: &id, Units: &units, UnitName: "credit", OccurredAt: time.Now(),
			Source: outcomes.SourceAuto, Note: "Email finder lookup",
		}
		if m := micros(r.micros); m > 0 {
			// The provider said what the lookup cost: the money is known.
			e.AmountMicros = &m
		}
		if _, _, err := outcomes.Record(ctx, s.Pool, e); err != nil {
			fail("internal", err, track.Error)
		}
	}
	track.Emit(ctx, event(map[string]any{
		"found": r.hit, "verified": r.verified,
		"by_profile": ask.byProfile, "credits": r.credits, "duration_ms": time.Since(started).Milliseconds()}))
	return r, finderAnswered, "", 0
}

// addCost adds what the finder tier charged to a result's cost. A finder
// that charged names the cost; the units are the total.
func addCost(base, finder Cost) Cost {
	if finder.Units == 0 {
		return base
	}
	finder.Units += base.Units
	finder.Cents += base.Cents
	finder.Micros += base.Micros
	return finder
}

// resolveOne takes one native result up the tiers, with the finder's
// answer for the person when the finder tier applied.
func (s *Service) resolveOne(ctx context.Context, ws string, r ItemResult, hasVerifier bool, fa *finderAnswer) ItemResult {
	if fa == nil {
		return s.waterfall(ctx, ws, r, hasVerifier)
	}
	if fa.code != "" {
		// No answer yet (wait or retry), or every connection refused
		// (stop): the caller asks again or pauses, and still books what
		// was charged.
		r.Code, r.StopCode, r.RetryAfter = fa.code, fa.stopCode, fa.wait
		r.Cost = addCost(r.Cost, fa.cost)
		return r
	}
	if fa.email != "" {
		if out, ok := s.fromFinder(ctx, ws, r, hasVerifier, fa); ok {
			return out
		}
	}
	// A miss, or an address already known to be bad: today's tiers.
	out := s.waterfall(ctx, ws, r, hasVerifier)
	out.Cost = addCost(out.Cost, fa.cost)
	return out
}

// fromFinder takes the address a finder returned down the remaining tiers:
// first-party history, the free built-in check, then the workspace's own
// verifier if it has one. ok is false when the address is already known to
// be undeliverable, which is a miss.
func (s *Service) fromFinder(ctx context.Context, ws string, native ItemResult, hasVerifier bool, fa *finderAnswer) (ItemResult, bool) {
	source := "provider:" + fa.provider
	prev, err := verify.Get(ctx, s.Pool, ws, fa.email)
	if err != nil {
		prev = nil
	}
	known := prev != nil && prev.Status == connectors.Undeliverable
	if native.Code == CodeUndeliverable && native.Email == fa.email {
		known = true
	}
	if known {
		_ = logCheck(ctx, s.Pool, ws, fa.email, source, connectors.Undeliverable, nil,
			map[string]any{"finder": true, "known": true})
		return ItemResult{}, false
	}
	r := ItemResult{Ref: native.Ref, RecordID: native.RecordID, Email: fa.email, Confidence: finderConfidence,
		Code: CodeFound, Evidence: source, Cost: addCost(Cost{Provider: fa.provider}, fa.cost)}
	if fa.verified {
		r.Code, r.Confidence = CodeVerified, finderVerifiedConfidence
		_ = logCheck(ctx, s.Pool, ws, r.Email, source, connectors.Deliverable, intPtr(r.Confidence),
			map[string]any{"finder": true, "verified": true})
		return r, true
	}
	_ = logCheck(ctx, s.Pool, ws, r.Email, source, "", intPtr(r.Confidence),
		map[string]any{"finder": true, "verified": false})
	if prev != nil && prev.Status == connectors.Deliverable {
		r.Code, r.Confidence, r.Evidence = CodeVerified, 95, EvidenceHistory
		_ = logCheck(ctx, s.Pool, ws, r.Email, "history", connectors.Deliverable, intPtr(r.Confidence), nil)
		return r, true
	}

	// Free built-in check, as for a guess: it can only rule an address out.
	if bv, err := s.Verify.Builtin.Verify(ctx, nil, nil, r.Email); err == nil {
		_ = logCheck(ctx, s.Pool, ws, r.Email, "builtin", bv.Status, nil,
			map[string]any{"reason": bv.Reason, "catch_all": bv.CatchAll, "role": bv.Role})
		if bv.Status == connectors.Undeliverable {
			r.Code, r.Evidence = CodeUndeliverable, "builtin"
			return r, true
		}
	}

	// No verifier: a provider's answer is found, not a risky guess.
	if !hasVerifier {
		return r, true
	}
	res, err := s.Verify.Verify(ctx, ws, []string{r.Email}, false)
	if err != nil {
		if k := connectors.KindOf(err); k == connectors.ErrAuth || k == connectors.ErrQuota {
			r.Code, r.StopCode = CodeStop, stopCode(k)
			return r, true
		}
		r.Code, r.RetryAfter = CodeRetry, verifyRetryAfter(err)
		return r, true
	}
	var v verify.Result
	switch {
	case len(res) > 0:
		v = res[0]
	case prev != nil:
		v = *prev // checked recently enough: the stored verdict, at no cost
	default:
		return r, true
	}
	if v.IntegrationID == nil {
		// The free checks answered (a stored result of theirs, or the
		// verifier refused or reached its limit): they can rule the
		// address out or confirm it, never make a provider's answer a guess.
		switch v.Status {
		case connectors.Undeliverable:
			r.Code, r.Evidence = CodeUndeliverable, "provider:"+v.Provider
		case connectors.Deliverable:
			r.Code = CodeVerified
		}
		return r, true
	}
	r.Code = verifyCode(v.Status)
	r.Evidence = "provider:" + v.Provider
	if v.Score != nil {
		r.Confidence = *v.Score
	}
	if len(res) > 0 {
		// One credit an address, on top of what the finder charged. The
		// finder names the cost when it charged, else the verifier does.
		if r.Cost.Units == 0 {
			r.Cost = Cost{Provider: v.Provider, Connection: *v.IntegrationID}
		}
		r.Cost.Units++
		r.Cost.Micros += micros(v.CostMicros)
		_ = logCheck(ctx, s.Pool, ws, r.Email, "provider:"+v.Provider, v.Status, intPtr(r.Confidence),
			map[string]any{"reason": v.Reason, "catch_all": v.CatchAll, "provider_status": v.ProviderStatus})
	}
	return r, true
}
