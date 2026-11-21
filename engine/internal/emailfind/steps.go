package emailfind

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/mailbox"
)

// Asking the finders one step at a time. Resolve asks whole providers and
// stops at the first address any of them returns. A caller that wants to
// judge and verify an address before paying for the next lookup asks one
// step instead (AskFinderStep): one lookup at one source, with every
// address that source offered, unfiltered. The steps are those of the
// workspace's own connected finders (connectors.StepFinder), and each call
// goes through the same usage limits, pacing, cache, spend ledger and
// events as Resolve's.

// stepPaceWait is the longest our own pacing may ask a caller to wait for
// a step's first connector before the next connector that offers the step
// is asked instead.
const stepPaceWait = 5 * time.Second

// FinderStep is one step the workspace's connected finders offer.
type FinderStep struct {
	// ID names the step, such as "blitz" or "filtrat".
	ID string `json:"id"`
	// NeedsProfile: the step looks a person up by their LinkedIn profile
	// and by nothing else. The others need a first name, a last name and
	// a company domain.
	NeedsProfile bool `json:"needs_profile"`
	// Connectors are the connected connectors that offer the step, in the
	// order AskFinderStep asks them.
	Connectors []string `json:"connectors"`
}

// StepResult is what one step said about one person.
type StepResult struct {
	Step string `json:"step"`
	// Provider and Connection name the connector and the connection that
	// answered; Connection is empty for an answer from the cache.
	Provider   string `json:"provider,omitempty"`
	Connection string `json:"connection,omitempty"`
	// Addresses are every plain address the provider offered for the
	// person, best first, in lower case. They are not filtered by company
	// and nobody has checked them: the caller judges each. Empty with an
	// empty Code is a miss, which is final for the step.
	Addresses []string `json:"addresses"`
	// Verified: the provider says it checked the first address's mailbox.
	Verified bool `json:"verified,omitempty"`
	// Asked: the step has an answer for this person, from a provider now
	// or from the cache. False when no provider was or could be asked:
	// the step needs something the item lacks (a profile, or a name and a
	// domain), the workspace is a test workspace, or Code says why.
	Asked bool `json:"asked"`
	// Cached: the answer was remembered from an earlier call and cost
	// nothing.
	Cached bool `json:"cached,omitempty"`
	// Code is empty for an answer (a hit or a miss). Otherwise pending
	// (our pacing, or a busy provider, says wait RetryAfter), retry (a
	// transient failure of every connector) or stop (every connector
	// refused; StopCode says what to fix: connector_quota,
	// connector_needed, usage_limit_reached).
	Code       Code          `json:"code,omitempty"`
	StopCode   string        `json:"stop_code,omitempty"`
	RetryAfter time.Duration `json:"retry_after,omitempty"`
	// Busy, with Code pending: the provider behind the step had no room
	// for the call and charged nothing. Nothing is known about the person
	// from this step yet. The caller may ask its other steps meanwhile,
	// and asks this one again after RetryAfter: it is never a miss.
	Busy bool `json:"busy,omitempty"`
	// Cost is what this call was charged, as the spend ledger books it.
	Cost Cost `json:"cost"`
}

// stepGroup is a step with the workspace's connections that offer it.
type stepGroup struct {
	FinderStep
	groups []*finderGroup
}

// finderSteps lists the steps the workspace's connected finders offer.
// The connectors are walked in the order Resolve asks them for a person
// with a profile (finderOrder: those that read only a profile first), and
// a step takes the place of its first appearance. A step several
// connectors offer lists them in that same order.
func (s *Service) finderSteps(ctx context.Context, ws string) ([]*stepGroup, error) {
	groups, err := s.finders(ctx, ws)
	if err != nil {
		return nil, err
	}
	var out []*stepGroup
	byID := map[string]*stepGroup{}
	for _, g := range finderOrder(groups, true) {
		c, ok := s.Store.Registry.Get(g.connector)
		if !ok {
			continue
		}
		sf, ok := c.(connectors.StepFinder)
		if !ok {
			continue
		}
		for _, st := range sf.FindSteps() {
			sg := byID[st.ID]
			if sg == nil {
				sg = &stepGroup{FinderStep: FinderStep{ID: st.ID, NeedsProfile: st.NeedsProfile}}
				byID[st.ID] = sg
				out = append(out, sg)
			}
			sg.Connectors = append(sg.Connectors, g.connector)
			sg.groups = append(sg.groups, g)
		}
	}
	return out, nil
}

// FinderSteps lists the steps the workspace's connected email finders can
// be asked one at a time, in the order to ask them. Only the workspace's
// own connections, and only those that serve the brand the work is for
// when ctx names one (outcomes.WithBrand). Empty when no connected finder
// can be asked by step.
func (s *Service) FinderSteps(ctx context.Context, ws string) ([]FinderStep, error) {
	steps, err := s.finderSteps(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := make([]FinderStep, len(steps))
	for i, st := range steps {
		out[i] = st.FinderStep
	}
	return out, nil
}

// stepCache is a step's answer in the cache of provider answers
// (enrichment_cache.person).
type stepCache struct {
	Addresses []string `json:"step_addresses"`
	Verified  bool     `json:"verified,omitempty"`
}

// stepKey names a step's answer about a person in the cache: apart from
// what the whole provider said (Resolve), which was filtered by company.
func stepKey(step, key string) string { return "step:" + step + "|" + key }

// stepCached reads a step's remembered answer from one connector.
func (s *Service) stepCached(ctx context.Context, ws, connector, key string) (stepCache, bool) {
	var person []byte
	err := s.Pool.QueryRow(ctx, `SELECT person FROM enrichment_cache
		WHERE workspace_id = $1 AND provider = $2 AND lookup_key = $3 AND fetched_at > $4`,
		ws, connector, key, time.Now().Add(-finderCacheMaxAge)).Scan(&person)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) && s.Log != nil {
			s.Log.WarnContext(ctx, "email finder cache read", "workspace_id", ws, "err", err)
		}
		return stepCache{}, false
	}
	var c stepCache
	_ = json.Unmarshal(person, &c)
	c.Addresses = plainAddresses(c.Addresses)
	return c, true
}

// plainAddresses keeps the plain addresses in list, in lower case, each
// once, in the order given. A provider's answer is data from outside.
func plainAddresses(list []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, e := range list {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" || len(e) > 320 || seen[e] {
			continue
		}
		if a, err := mail.ParseAddress(e); err != nil || strings.ToLower(a.Address) != e {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return out
}

// AskFinderStep asks one step (FinderSteps) about one person and returns
// every address it offered. It asks the step's first connector, whose
// connections take turns: one that could not take the call (a refused key,
// no credits left, a usage limit, busy, or waiting for its own turn) hands
// the same call to the connector's next connection at once. Only when
// that connector could not answer (refused, unreachable, busy, or our own
// pacing asking for a longer wait) does it ask the next connector that
// offers the same step. An answer, hit
// or miss, is final for the step: no other connector is asked.
//
// A provider with no room for the call right now (connectors.IsBusy) is
// pending with Busy set, whatever else refused: it is not the account's
// quota, so it never stops the caller, and not an answer, so nothing is
// remembered.
//
// An answer is remembered for 90 days per connector, so asking again costs
// nothing (Cached). A lookup the provider said it was still working on is
// its answer, remembered as a miss: it may still be billed, so it is never
// asked twice. A lookup that got no answer in time is a miss for this ask
// only and is never remembered: a timeout is not the provider saying it
// has no address.
//
// Only the workspace's own connections are used, and only those that serve
// the brand the work is for when ctx names one. A test workspace never
// calls a provider.
func (s *Service) AskFinderStep(ctx context.Context, ws, step string, it FindItem) (StepResult, error) {
	res := StepResult{Step: step, Addresses: []string{}}
	if s.Store == nil || s.Store.Registry == nil {
		res.Code, res.StopCode = CodeStop, "connector_needed"
		return res, nil
	}
	steps, err := s.finderSteps(ctx, ws)
	if err != nil {
		return res, err
	}
	var sg *stepGroup
	for _, st := range steps {
		if st.ID == step {
			sg = st
		}
	}
	if sg == nil {
		// No connected finder offers the step: the caller says what to
		// connect.
		res.Code, res.StopCode = CodeStop, "connector_needed"
		return res, nil
	}
	q := connectors.PersonQuery{FirstName: strings.TrimSpace(it.FirstName), LastName: strings.TrimSpace(it.LastName),
		Domain: finderDomain(it.Domain), Company: strings.TrimSpace(it.Company)}
	key := q.CacheKey()
	if sg.NeedsProfile {
		profile := connectors.LinkedInProfile(it.LinkedInURL)
		if profile == "" {
			return res, nil
		}
		q.LinkedInURL, key = profile, finderProfileKey(profile, q.Domain)
	} else if q.FirstName == "" || q.LastName == "" || q.Domain == "" {
		return res, nil
	}
	key = stepKey(step, key)
	// A test workspace never spends a provider's credits (as in Resolve).
	if test, err := mailbox.IsTestWorkspace(ctx, s.Pool, ws); err != nil || test {
		return res, err
	}

	// A remembered answer from any connector that offers the step stands.
	for _, g := range sg.groups {
		if c, ok := s.stepCached(ctx, ws, g.connector, key); ok {
			res.Provider, res.Addresses, res.Verified = g.connector, c.Addresses, c.Verified && len(c.Addresses) > 0
			res.Asked, res.Cached, res.Cost = true, true, Cost{Provider: g.connector}
			return res, nil
		}
	}

	cctx, cancel := context.WithTimeout(ctx, finderItemTimeout)
	defer cancel()
	turn := s.turn(ws)
	busy, broken, limited := false, false, false
	stop := ""
	wait, again := time.Duration(0), time.Duration(0)
	for _, g := range sg.groups {
		// soon is the shortest wait for a turn on one of this connector's
		// connections, when it is a short one.
		soon := time.Duration(-1)
		for k := range g.conns {
			c := g.conns[(turn+k)%len(g.conns)]
			a, how, code, w := s.callStep(ctx, cctx, ws, g.connector, c, step, q, key)
			switch how {
			case finderAnswered:
				res.Provider, res.Connection, res.Asked = g.connector, c.id, true
				res.Addresses, res.Verified = a.Addresses, a.Verified && len(a.Addresses) > 0
				res.Cost = Cost{Provider: g.connector, Connection: c.id, Units: int(math.Ceil(a.Credits - 1e-9)), Micros: micros(a.CostMicros)}
				return res, nil
			case finderBusy:
				if w <= stepPaceWait {
					// A short wait for this connection's turn: the
					// connector's other connection may be free now.
					if soon < 0 || w < soon {
						soon = w
					}
					continue
				}
				if !busy || w < wait {
					wait = w
				}
				busy = true
			case finderLimited:
				// The provider is busy on this connection: another
				// account may have room.
				if !limited || w < again {
					again = w
				}
				limited = true
			case finderRefused:
				stop = worseStop(stop, code)
			default:
				broken = true
			}
		}
		if soon >= 0 {
			// A short wait for the connector whose turn it is: the caller
			// asks again rather than another provider.
			res.Code, res.RetryAfter = CodePending, max(soon, time.Second)
			return res, nil
		}
	}
	switch {
	case limited:
		res.Code, res.Busy, res.RetryAfter = CodePending, true, max(again, time.Second)
	case busy:
		res.Code, res.RetryAfter = CodePending, max(wait, time.Second)
	case broken:
		res.Code = CodeRetry
	default:
		// Every connection refused, or was disconnected meanwhile.
		res.Code, res.StopCode = CodeStop, worseStop("connector_needed", stop)
	}
	return res, nil
}

// callStep asks one connection for one step, through callConn. A pending
// answer is remembered as a miss; one that did not come in time is a miss
// for this ask and is not remembered.
func (s *Service) callStep(ctx, cctx context.Context, ws, connector string, c *finderConn, step string, q connectors.PersonQuery, key string) (connectors.StepAnswer, int, string, time.Duration) {
	var a connectors.StepAnswer
	_, how, code, wait := s.callConn(ctx, cctx, ws, connector, c, finderAsk{
		step: step, key: key, byProfile: q.LinkedInURL != "",
		can: func(c connectors.Connector) bool { _, ok := c.(connectors.StepFinder); return ok },
		call: func(callCtx context.Context, o *connectors.Opened) (finderReply, error) {
			got, err := o.Connector.(connectors.StepFinder).FindStep(callCtx, o.Client, o.Creds, step, q)
			if err != nil {
				return finderReply{}, err
			}
			got.Addresses = plainAddresses(got.Addresses)
			if got.Pending {
				got.Addresses, got.Verified = []string{}, false
			}
			a = got
			return finderReply{hit: len(got.Addresses) > 0, verified: got.Verified && len(got.Addresses) > 0, credits: got.Credits, micros: got.CostMicros,
				found: len(got.Addresses) > 0, person: stepCache{Addresses: got.Addresses, Verified: got.Verified}}, nil
		},
		timedOut: &finderReply{person: stepCache{Addresses: []string{}}},
	})
	if a.Addresses == nil {
		a.Addresses = []string{}
	}
	return a, how, code, wait
}
