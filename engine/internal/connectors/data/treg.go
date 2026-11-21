package data

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Treg: https://treg.to/docs
//
// Treg is a gateway: one token and one prepaid balance in front of many
// providers. Every call names one provider's route (/call/<route>), so
// which provider answers is decided here and never by Treg's own router
// (treg.people.email.find). That router asked its providers one after
// another inside a single call, which often ran out of time, and it
// stopped at the first answer, including a former employer's address.
//
// Finding a work email asks the routes in tregChain in order and stops at
// the first address (EnrichPerson). Checking an address asks Filtrat, then
// BounceBan when Filtrat does not pass it (Verify). Treg reports what each
// call cost, in millionths of a dollar, in the X-Treg-Cost-Micro header.
type Treg struct{}

// tregWait is longer than the 60 seconds Treg itself holds a slow provider
// before it answers 202, so that answer is seen instead of being mistaken
// for a timeout. Some lookups get no answer even then: that comes back as
// a timeout (connectors.IsTimeout), which a caller must not take for a
// service that is down. A variable so tests don't wait.
var tregWait = 75 * time.Second

// tregChainBudget is how long one person's chain may take in all. A route
// gets what is left of it, at most tregWait, and no route is started with
// less than tregRouteMin left: two routes that both ran out of time say
// Treg is slow right now, not that the next one will answer. The email
// finder's own limit for one call (emailfind.finderCallTimeout) is longer
// than this. Variables so tests don't wait.
var (
	tregChainBudget = 150 * time.Second
	tregRouteMin    = 20 * time.Second
)

// Treg answering that it has no room for a call is saturation: a body with
// treg_saturated, or HTTP 429 that is not the account's quota (share_busy:
// the provider's shared capacity is in use; money_admission_timeout: Treg
// could not admit the call in its own time; a plain rate limit). Nothing was
// charged and the same call succeeds a moment later, so it is asked again in
// place after tregBackoff, as long as the call began less than
// tregRetryWithin ago and the caller has time for another answer. A shorter
// pause Treg names is waited instead; one longer than tregBusyMaxPause is
// not waited for here. Still saturated after that is a busy error
// (connectors.Busyf), which hands the same call to the workspace's other
// Treg connection (across, and the email finder's own rotation).
// tregBusyDefault is the pause the error names when Treg names none.
// Variables so tests don't wait.
var (
	tregBackoff      = []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}
	tregRetryWithin  = 60 * time.Second
	tregBusyDefault  = 2 * time.Second
	tregBusyMaxPause = 8 * time.Second
)

// tregTurn takes turns between a workspace's Treg connections (across).
var tregTurn atomic.Uint32

// tregRoute is one provider's email finder behind Treg.
type tregRoute struct {
	id string
	// step names the route for a caller that asks one at a time
	// (connectors.StepFinder).
	step string
	// maxCost caps what one call may cost, in dollars, a little above the
	// provider's list price: Treg refuses a call that would cost more.
	maxCost string
	// needsProfile: the provider looks a person up by their LinkedIn
	// profile, so the route is skipped when the query has none.
	needsProfile bool
	body         func(first, last, domain, profile string) map[string]any
	// read takes the address out of the provider's answer, and whether
	// the provider says it checked the mailbox. The answer is data from
	// outside: only the fields named here are read, and anything that is
	// not the text expected is no address.
	read func(o map[string]json.RawMessage, domain string) (email string, verified bool)
	// all, when set, takes every address the provider offered out of its
	// answer, whatever company it is at, for a caller that judges them
	// itself (FindStep). Without it the step's addresses are read's one.
	all func(o map[string]json.RawMessage) []string
}

// tregBlitzEmails is every address in Blitz's answer: the one it puts
// first, then the rest of its list. None when Blitz says it found none.
func tregBlitzEmails(o map[string]json.RawMessage) []string {
	var found *bool
	if json.Unmarshal(o["found"], &found) == nil && found != nil && !*found {
		return nil
	}
	emails := []string{tregText(o["email"])}
	var all []struct {
		Email json.RawMessage `json:"email"`
	}
	if json.Unmarshal(o["all_emails"], &all) == nil {
		for _, a := range all {
			emails = append(emails, tregText(a.Email))
		}
	}
	return tregAddresses(emails)
}

// tregChain is the order the finders are asked in, cheapest first. Blitz
// reads a LinkedIn profile, so it can return an address at a company the
// person has left: only one at the company asked about counts.
var tregChain = []tregRoute{
	{
		id: "blitzapi.people.email.find", step: "blitz", maxCost: "0.01", needsProfile: true,
		body: func(_, _, _, profile string) map[string]any {
			return map[string]any{"person_linkedin_url": profile}
		},
		read: func(o map[string]json.RawMessage, domain string) (string, bool) {
			for _, e := range tregBlitzEmails(o) {
				if tregAtDomain(e, domain) {
					return e, false
				}
			}
			return "", false
		},
		all: tregBlitzEmails,
	},
	{
		id: "filtrat-finder.people.email.find", step: "filtrat", maxCost: "0.01",
		body: func(first, last, domain, _ string) map[string]any {
			return map[string]any{"first_name": first, "last_name": last, "domain": domain, "resolve_catchall": true}
		},
		read: func(o map[string]json.RawMessage, _ string) (string, bool) {
			if tregText(o["status"]) != "found" {
				return "", false
			}
			return tregAddress(tregText(o["found_email"])), false
		},
	},
	{
		id: "trykitt.people.email.find", step: "kitt", maxCost: "0.01",
		body: func(first, last, domain, _ string) map[string]any {
			return map[string]any{"fullName": first + " " + last, "domain": domain, "realtime": true}
		},
		// A miss is the text "no-results-found" in the email field, which
		// is no address.
		read: func(o map[string]json.RawMessage, _ string) (string, bool) {
			return tregAddress(tregText(o["email"])), tregText(o["validity"]) == "valid"
		},
	},
	{
		id: "anymailfinder.people.email.find", step: "anymailfinder", maxCost: "0.03",
		body: func(first, last, domain, _ string) map[string]any {
			return map[string]any{"domain": domain, "full_name": first + " " + last}
		},
		// valid_email is set only for an address Anymailfinder checked.
		read: func(o map[string]json.RawMessage, _ string) (string, bool) {
			if e := tregAddress(tregText(o["valid_email"])); e != "" {
				return e, true
			}
			return tregAddress(tregText(o["email"])), tregText(o["email_status"]) == "valid"
		},
	},
}

func (Treg) Definition() c.Definition {
	return c.Definition{
		ID: "treg", Name: "Treg", Category: c.CategoryData,
		Summary:     "Find and verify work emails through the providers behind your Treg account.",
		Description: "Fills in work emails for people found elsewhere: it asks Blitz (with a LinkedIn profile), Filtrat, Kitt and Anymailfinder in that order and stops at the first address. It can also be your verification service: Filtrat checks each address, and BounceBan gives a second opinion on the ones Filtrat does not pass. It can read a person's LinkedIn profile too (through Fetchin), to check where they work now, and find a person's profile from their name and company (through Crawl4AI). Every call is billed to your own Treg balance.",
		Website:     "https://treg.to", DocsURL: "https://treg.to/docs",
		KeyURL: "https://treg.to/app", Auth: c.AuthAPIKey,
		Fields:       apiKeyField("Sign in at treg.to/app and copy a team token (it starts with trg_live_)."),
		Capabilities: []string{c.CapEnrichPerson, c.CapFindEmail, c.CapVerify, c.CapReadProfile, c.CapFindProfile},
		Pricing:      "Pay per call from your Treg balance: at most $0.06 to find one person's email, verifying and reading a profile are billed per call",
	}
}

// ProfileOnly: Treg's first route reads a LinkedIn profile and keeps only
// an address at the company asked about; the rest need none
// (connectors.ProfileFinder).
func (Treg) ProfileOnly() bool { return false }

func (Treg) base(cl *c.Client) string { return cl.Base("treg", "https://treg.to") }

func (t Treg) req(r *http.Request, cr c.Creds) *http.Request {
	r.Header.Set("X-Treg-Token", cr["api_key"])
	return r
}

// tregAnswer is what a Treg call came back with besides its body.
type tregAnswer struct {
	// pending: Treg is still waiting for a provider. The task may yet
	// finish and be billed, so it must not be asked again.
	pending bool
	// billed says whether Treg reported a cost, and cost what it was, in
	// millionths of a dollar.
	billed bool
	cost   float64
	// limited: the error is Treg or the provider behind the route asking
	// for fewer calls (HTTP 429), which is about that route right now,
	// unlike an empty balance. The error is a busy one (connectors.IsBusy).
	limited bool
	// For an answer that is not a 2xx: its status, the provider Treg says
	// answered (X-Treg-Provider) and the body, so a caller can tell a
	// provider's own "no such thing" from a refused request.
	status   int
	provider string
	body     []byte
}

// micros is what Treg said the call cost, in millionths of a dollar: 0
// when it said nothing.
func (a tregAnswer) micros() float64 {
	if a.billed && a.cost > 0 {
		return a.cost
	}
	return 0
}

// do sends the request build returns on one account, waits up to wait for
// each answer and decodes a 2xx JSON body into v. build is called once per
// attempt: only a saturated answer is asked again (tregBackoff).
//
// HTTP 429 is never the account's quota here (an empty balance is 402): it
// is a busy error, so no caller takes it for a refusal, a miss or a verdict.
// share_busy and money_admission_timeout are Treg's own capacity, used up by
// everyone's calls (connectors.Busyf); any other 429 asks this account for
// fewer calls (connectors.Throttledf), which slows the connection's pace.
func (t Treg) do(ctx context.Context, cl *c.Client, cr c.Creds, wait time.Duration, build func() *http.Request, v any) (tregAnswer, error) {
	started := time.Now()
	attempt := 0
	// again waits before the same call is made once more, and says whether
	// to make it. named is the pause Treg asked for, when it named one.
	again := func(named *time.Duration) bool {
		if attempt >= len(tregBackoff) {
			return false
		}
		pause := tregBackoff[attempt]
		if named != nil {
			if *named > tregBusyMaxPause {
				return false
			}
			pause = min(pause, *named)
		}
		if time.Since(started)+pause > tregRetryWithin {
			return false
		}
		if d, ok := ctx.Deadline(); ok && time.Until(d) < pause+tregRouteMin {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(pause):
		}
		attempt++
		return true
	}
	for {
		req := t.req(build(), cr).WithContext(ctx)
		req.Header.Set("Accept", "application/json")
		resp, err := cl.DoWithin(req, wait)
		if err != nil {
			if tregTimedOut(err) {
				return tregAnswer{}, c.TimedOutf("Treg did not answer this lookup in time. It may still finish and bill it.")
			}
			return tregAnswer{}, c.Errf(c.ErrUnavailable, "could not reach Treg: %v", err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil {
			if tregTimedOut(err) {
				return tregAnswer{}, c.TimedOutf("Treg did not answer this lookup in time. It may still finish and bill it.")
			}
			return tregAnswer{}, c.Errf(c.ErrUnavailable, "reading the Treg response: %v", err)
		}
		// Treg's own notes sit beside the provider's answer. The body is
		// data: only these fields are read, and a body that is not an
		// object leaves them empty.
		var env struct {
			Saturated bool `json:"treg_saturated"`
			Treg      struct {
				Outcome string `json:"outcome"`
			} `json:"_treg"`
			Detail json.RawMessage `json:"detail"`
		}
		_ = json.Unmarshal(body, &env)

		if env.Saturated {
			if again(nil) {
				continue
			}
			return tregAnswer{limited: true}, c.Busyf(tregBusyDefault, "Treg is at capacity right now and charged nothing. It is asked again shortly.")
		}
		var a tregAnswer
		if micro, err := strconv.ParseFloat(strings.TrimSpace(resp.Header.Get("X-Treg-Cost-Micro")), 64); err == nil && micro >= 0 {
			a.billed, a.cost = true, micro
		}
		if resp.StatusCode == http.StatusAccepted || env.Treg.Outcome == "pending" {
			a.pending = true
			return a, nil
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			// The detail is data from outside: an object with these two
			// fields, or anything else, which leaves them empty.
			var detail struct {
				Error      string   `json:"error"`
				RetryAfter *float64 `json:"retry_after_s"`
			}
			_ = json.Unmarshal(env.Detail, &detail)
			if detail.Error == "trial_allowance_reached" {
				return a, c.Errf(c.ErrQuota, "Treg says the account's trial allowance is used up. Add balance at treg.to.")
			}
			a.status, a.provider, a.body = resp.StatusCode, resp.Header.Get("X-Treg-Provider"), body
			if resp.StatusCode == http.StatusTooManyRequests {
				var named *time.Duration
				pause := tregBusyDefault
				if s := detail.RetryAfter; s != nil && *s >= 0 && *s <= 3600 {
					pause = time.Duration(*s * float64(time.Second))
					named = &pause
				}
				if again(named) {
					continue
				}
				a.limited = true
				switch detail.Error {
				case "share_busy", "money_admission_timeout":
					// Everyone's calls fill Treg or the provider behind
					// it: our own pace has nothing to do with it.
					return a, c.Busyf(pause, "Treg says it has no room for this call right now and charged nothing. It is asked again shortly.")
				}
				return a, c.Throttledf(pause, "Treg asked for fewer calls right now and charged nothing. It is asked again shortly.")
			}
			return a, c.HTTPError("Treg", resp.StatusCode, string(body))
		}
		if v != nil {
			if err := json.Unmarshal(body, v); err != nil {
				return a, c.Errf(c.ErrUnavailable, "Treg returned something that is not the JSON it documents")
			}
		}
		return a, nil
	}
}

// across makes one call on the workspace's Treg accounts. They take turns,
// and an account that is refused, out of balance or still saturated after
// its own retries (do) hands the same call to the next one at once. An
// answer, a lookup Treg is still working on and one that got no answer in
// time are final: the first account may still finish and bill it. account
// is the index of the account the answer or the error came from. When no
// account could take the call, a busy one's error is returned before a
// refusal: the call succeeds a moment later.
func (t Treg) across(ctx context.Context, cl *c.Client, accounts []c.Creds, wait time.Duration, build func() *http.Request, v any) (a tregAnswer, account int, err error) {
	start := 0
	if len(accounts) > 1 {
		start = int(tregTurn.Add(1)-1) % len(accounts)
	}
	var busy error
	var busyAnswer tregAnswer
	busyAt := 0
	for i := range accounts {
		account = (start + i) % len(accounts)
		a, err = t.do(ctx, cl, accounts[account], wait, build, v)
		if err == nil {
			return a, account, nil
		}
		_, isBusy := c.IsBusy(err)
		if k := c.KindOf(err); !isBusy && k != c.ErrAuth && k != c.ErrQuota {
			return a, account, err
		}
		if isBusy && busy == nil {
			busy, busyAnswer, busyAt = err, a, account
		}
		if d, ok := ctx.Deadline(); ok && time.Until(d) < tregRouteMin {
			break
		}
	}
	if busy != nil {
		return busyAnswer, busyAt, busy
	}
	return a, account, err
}

// Test checks the token and reads the balance it spends from.
func (t Treg) Test(ctx context.Context, cl *c.Client, cr c.Creds) (c.Account, error) {
	var me struct {
		OrgID json.Number `json:"org_id"`
	}
	get := func(path string) func() *http.Request {
		return func() *http.Request { return getReq(t.base(cl)+path, nil) }
	}
	if _, err := t.do(ctx, cl, cr, tregWait, get("/auth/me"), &me); err != nil {
		return c.Account{}, err
	}
	org, err := me.OrgID.Int64()
	if err != nil {
		return c.Account{}, c.Errf(c.ErrInvalid, "Treg accepted the token but it belongs to no team. Use a team token.")
	}
	acct := c.Account{ID: strconv.FormatInt(org, 10), Name: "Treg"}
	// The balance is a nicety: a token that may not read it still finds
	// emails.
	var bal struct {
		Micro *float64 `json:"balance_micro"`
	}
	if _, err := t.do(ctx, cl, cr, tregWait, get("/orgs/"+acct.ID+"/balance"), &bal); err == nil && bal.Micro != nil {
		acct.Details = map[string]string{"balance_usd": strconv.FormatFloat(*bal.Micro/1e6, 'f', 2, 64)}
	}
	return acct, nil
}

// EnrichPerson finds a work email from a name and company domain: it asks
// the routes in tregChain in order and stops at the first address. Credits
// counts the billed calls, one for each route Treg charged for: Treg bills
// dollars, not credits, and a lookup is the unit the other finders report.
//
// A route that fails or gives no answer in time has no address, and the
// next is asked. Only when no route answered at all is the lookup an
// error: the kind that says most about what to do (busy, then
// unreachable, then no answer in time). A refused token or an empty
// balance ends the chain at once, since every route spends the same one.
//
// A route that was busy (HTTP 429, nothing charged) is different from one
// with no address: when no other route has the address either, the lookup
// is the busy error, never a miss, so the person is asked about again.
func (t Treg) EnrichPerson(ctx context.Context, cl *c.Client, cr c.Creds, q c.PersonQuery) (c.Enriched, error) {
	first, last, domain, ok := nameDomain(q)
	if !ok {
		return c.Enriched{}, c.Errf(c.ErrInvalid, "Treg needs a first name, last name and company domain")
	}
	profile := c.LinkedInProfile(q.LinkedInURL)
	deadline := time.Now().Add(tregChainBudget)
	credits, micros := 0.0, 0.0
	answered := false
	var failed, busy error
	for _, route := range tregChain {
		if route.needsProfile && profile == "" {
			continue
		}
		// The caller stopped waiting: not every route was asked, so this
		// is no answer about the person, whatever the routes so far said.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return c.Enriched{}, c.TimedOutf("Treg did not answer this lookup in time. It may still finish and bill it.")
		} else if ctx.Err() != nil {
			return c.Enriched{}, c.Errf(c.ErrUnavailable, "could not reach Treg: %v", ctx.Err())
		}
		left := time.Until(deadline)
		if left < tregRouteMin {
			break
		}
		a, fields, err := t.askRoute(ctx, cl, cr, route, min(tregWait, left), first, last, domain, profile)
		if err != nil {
			if k := c.KindOf(err); k == c.ErrAuth || (k == c.ErrQuota && !a.limited) {
				return c.Enriched{}, err
			}
			if tregWorse(err, failed) {
				failed = err
			}
			if _, ok := c.IsBusy(err); ok && busy == nil {
				busy = err
			}
			continue
		}
		answered = true
		micros += a.micros()
		if a.pending {
			// Still running at Treg after its own wait. It may finish
			// and bill, and asking again would bill twice, so this
			// route has no address and counts as a billed call.
			credits++
			continue
		}
		email, verified := route.read(fields, domain)
		if (a.billed && a.cost > 0) || (!a.billed && email != "") {
			credits++
		}
		if email == "" {
			continue
		}
		status := c.EmailGuessed
		if verified {
			status = c.EmailVerified
		}
		p := c.Person{FirstName: first, LastName: last, Email: email, EmailStatus: status, HasEmail: true,
			LinkedInURL: q.LinkedInURL, Company: c.Company{Domain: domain}}
		return c.Enriched{Person: p, Found: true, Credits: credits, CostMicros: micros}, nil
	}
	if busy != nil {
		// A route that was busy may have had the address: this is not a
		// miss, whatever the others said. The person is asked about again.
		return c.Enriched{}, busy
	}
	if !answered && failed != nil {
		return c.Enriched{}, failed
	}
	return c.Enriched{Credits: credits, CostMicros: micros}, nil
}

// askRoute makes one call to one finder route and returns the fields of
// the provider's answer. A provider with no address may answer with
// anything: a body that is not an object has no fields.
func (t Treg) askRoute(ctx context.Context, cl *c.Client, cr c.Creds, route tregRoute, wait time.Duration, first, last, domain, profile string) (tregAnswer, map[string]json.RawMessage, error) {
	var o json.RawMessage
	a, err := t.do(ctx, cl, cr, wait, func() *http.Request {
		r := postReq(t.base(cl)+"/call/"+route.id, route.body(first, last, domain, profile))
		r.Header.Set("X-Treg-Route-Max-Cost", route.maxCost)
		return r
	}, &o)
	if err != nil || a.pending {
		return a, nil, err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(o, &fields)
	return a, fields, nil
}

// FindSteps lists the routes of tregChain as steps, in the chain's order
// (connectors.StepFinder).
func (Treg) FindSteps() []c.FindStep {
	steps := make([]c.FindStep, len(tregChain))
	for i, r := range tregChain {
		steps[i] = c.FindStep{ID: r.step, NeedsProfile: r.needsProfile}
	}
	return steps
}

// FindStep asks one route of tregChain about a person, with the same
// request EnrichPerson sends it, and returns what the provider offered
// without judging it: for Blitz every address on the profile, whatever the
// company, for the others the one address they found. The caller decides
// whether to ask the next step.
//
// A lookup Treg is still waiting on is Pending and counts as a billed
// call: it may finish and bill, so it must not be asked again. Errors are
// the route's own (a refused token, an empty balance, a rate limit, no
// answer in time).
func (t Treg) FindStep(ctx context.Context, cl *c.Client, cr c.Creds, step string, q c.PersonQuery) (c.StepAnswer, error) {
	var route *tregRoute
	for i := range tregChain {
		if tregChain[i].step == step {
			route = &tregChain[i]
		}
	}
	if route == nil {
		return c.StepAnswer{}, c.Errf(c.ErrInvalid, "Treg has no email finder step %q", step)
	}
	profile := c.LinkedInProfile(q.LinkedInURL)
	first, last, domain, named := nameDomain(q)
	switch {
	case route.needsProfile && profile == "":
		return c.StepAnswer{}, c.Errf(c.ErrInvalid, "this step needs the person's LinkedIn profile address")
	case !route.needsProfile && !named:
		return c.StepAnswer{}, c.Errf(c.ErrInvalid, "Treg needs a first name, last name and company domain")
	}
	a, fields, err := t.askRoute(ctx, cl, cr, *route, tregWait, first, last, domain, profile)
	if err != nil {
		return c.StepAnswer{}, err
	}
	if a.pending {
		return c.StepAnswer{Pending: true, Credits: 1, CostMicros: a.micros()}, nil
	}
	out := c.StepAnswer{CostMicros: a.micros()}
	if route.all != nil {
		out.Addresses = route.all(fields)
	} else if email, verified := route.read(fields, domain); email != "" {
		out.Addresses, out.Verified = []string{email}, verified
	}
	if (a.billed && a.cost > 0) || (!a.billed && len(out.Addresses) > 0) {
		out.Credits = 1
	}
	return out, nil
}

// tregProfile is the route that reads a LinkedIn profile (Fetchin).
const tregProfile = "fetchinio.linkedin.user.profile"

// tregProfileWait is how long one profile lookup may take. A variable so
// tests don't wait.
var tregProfileWait = 30 * time.Second

// tregNoProfile matches a provider's words for a profile that isn't there.
var tregNoProfile = regexp.MustCompile(`(?i)not[\s_-]*found|no such (profile|member|user)|does not exist|profile (is )?unavailable`)

// tregProfileMissing says whether a refused profile lookup is the
// provider's own answer that the profile doesn't exist: a 404 with Treg's
// PROFILE_NOT_FOUND code, or a 404 from the profile provider whose detail
// says not found. Any other 404 (a route that moved, a gateway page) is a
// failed request, never a missing person.
func tregProfileMissing(a tregAnswer) bool {
	if a.status != http.StatusNotFound {
		return false
	}
	var o struct {
		Code   json.RawMessage `json:"code"`
		Detail json.RawMessage `json:"detail"`
	}
	_ = json.Unmarshal(a.body, &o)
	if tregText(o.Code) == "PROFILE_NOT_FOUND" {
		return true
	}
	return strings.Contains(strings.ToLower(a.provider), "fetchin") && tregNoProfile.Match(o.Detail)
}

// ReadProfile reads a person's LinkedIn profile and returns the jobs it
// lists as current (connectors.ProfileReader). A profile the provider says
// doesn't exist is Found false, not an error. A lookup Treg is still
// waiting on is a timeout: it may finish and bill, and says nothing about
// the person. The profile is data from outside: only the fields named
// here are read.
func (t Treg) ReadProfile(ctx context.Context, cl *c.Client, cr c.Creds, profileURL string) (c.PersonProfile, error) {
	profile := c.LinkedInProfile(profileURL)
	if profile == "" {
		return c.PersonProfile{}, c.Errf(c.ErrInvalid, "Treg needs a person's LinkedIn profile address, like https://www.linkedin.com/in/someone")
	}
	slug := profile[strings.LastIndex(profile, "/in/")+len("/in/"):]
	if s, err := url.PathUnescape(slug); err == nil {
		slug = s
	}
	var o struct {
		Title       json.RawMessage `json:"title"`
		Location    json.RawMessage `json:"geoLocationName"`
		Country     json.RawMessage `json:"geoCountryName"`
		Experiences []struct {
			Current  json.RawMessage `json:"current"`
			Ongoing  json.RawMessage `json:"ongoing"`
			Name     json.RawMessage `json:"name"`
			JobTitle json.RawMessage `json:"jobTitle"`
			Company  struct {
				Name json.RawMessage `json:"name"`
				URL  json.RawMessage `json:"url"`
			} `json:"company"`
		} `json:"experiences"`
	}
	a, err := t.do(ctx, cl, cr, tregProfileWait, func() *http.Request {
		return getReq(t.base(cl)+"/call/"+tregProfile, url.Values{"profileUrlOrUrn": {slug}})
	}, &o)
	out := c.PersonProfile{Current: []c.ProfileRole{}}
	if a.billed && a.cost > 0 {
		out.Cost = c.SEOCost{USD: a.cost / 1e6}
	}
	if err != nil {
		if tregProfileMissing(a) {
			return out, nil
		}
		return out, err
	}
	if a.pending {
		return out, c.TimedOutf("Treg did not answer this profile lookup in time. It may still finish and bill it.")
	}
	isTrue := func(v json.RawMessage) bool { return strings.TrimSpace(string(v)) == "true" }
	out.Found = true
	out.Headline, out.Location, out.Country = tregClip(tregText(o.Title), 300), tregClip(tregText(o.Location), 200), tregClip(tregText(o.Country), 200)
	for _, e := range o.Experiences {
		if !isTrue(e.Current) && !isTrue(e.Ongoing) {
			continue
		}
		role := c.ProfileRole{Company: tregClip(tregText(e.Company.Name), 200), Title: tregClip(tregText(e.JobTitle), 200)}
		if role.Company == "" {
			role.Company = tregClip(tregText(e.Name), 200)
		}
		if u := tregText(e.Company.URL); strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
			role.CompanyURL = tregClip(u, 300)
		}
		if len(out.Current) < 20 {
			out.Current = append(out.Current, role)
		}
	}
	return out, nil
}

// tregFindProfile is the route that looks a person up by name and company
// domain (Crawl4AI's person lookup): about a tenth of a cent for a person
// it has, nothing for one it has not (404).
const tregFindProfile = "crawl4ai.people.enrich"

// tregNoPerson matches a provider's words for nobody of that name.
var tregNoPerson = regexp.MustCompile(`(?i)not[\s_-]*found|no such (person|people|profile|member|user|contact)|does not exist|no (matching )?(person|people|match)`)

// tregFindMissing says whether a refused lookup by name is the provider's
// own answer that it has nobody of that name: a 404 from Crawl4AI, or a
// JSON 404 whose detail, error or message says so ({"error":"no such
// person"} comes without the provider's name). Any other refusal, and a
// 404 that isn't JSON (a route that moved, a gateway page), is a failed
// request.
func tregFindMissing(a tregAnswer) bool {
	if a.status != http.StatusNotFound {
		return false
	}
	if strings.Contains(strings.ToLower(a.provider), "crawl4ai") {
		return true
	}
	var o struct {
		Detail  json.RawMessage `json:"detail"`
		Error   json.RawMessage `json:"error"`
		Message json.RawMessage `json:"message"`
	}
	if json.Unmarshal(a.body, &o) != nil {
		return false
	}
	return tregNoPerson.Match(o.Detail) || tregNoPerson.Match(o.Error) || tregNoPerson.Match(o.Message)
}

// FindProfile asks for the LinkedIn profile of the person named in q at
// the company at q.Domain (connectors.ProfileLocator), and returns it with
// the name, title and company the provider has for it, for the caller to
// check against what it asked. Nobody on record, or nobody with a profile
// address, is Found false. A lookup Treg is still waiting on is a timeout.
// The answer is data from outside: only the fields named here are read.
func (t Treg) FindProfile(ctx context.Context, cl *c.Client, cr c.Creds, q c.PersonQuery) (c.FoundProfile, error) {
	first, last, domain, named := nameDomain(c.PersonQuery{FirstName: strings.TrimSpace(q.FirstName), LastName: strings.TrimSpace(q.LastName), Domain: q.Domain})
	if !named {
		return c.FoundProfile{}, c.Errf(c.ErrInvalid, "Treg needs a first name, last name and company domain to find a profile")
	}
	var o struct {
		Data *struct {
			Name        json.RawMessage `json:"name"`
			First       json.RawMessage `json:"first_name"`
			Last        json.RawMessage `json:"last_name"`
			Title       json.RawMessage `json:"title"`
			LinkedInURL json.RawMessage `json:"linkedin_url"`
			Company     struct {
				Name   json.RawMessage `json:"name"`
				Domain json.RawMessage `json:"domain"`
			} `json:"company"`
		} `json:"data"`
	}
	a, err := t.do(ctx, cl, cr, tregProfileWait, func() *http.Request {
		r := getReq(t.base(cl)+"/call/"+tregFindProfile, url.Values{"name": {first + " " + last}, "domain": {domain}})
		r.Header.Set("X-Treg-Route-Max-Cost", "0.01")
		return r
	}, &o)
	var out c.FoundProfile
	if a.billed && a.cost > 0 {
		out.Cost = c.SEOCost{USD: a.cost / 1e6}
	}
	if err != nil {
		if tregFindMissing(a) {
			return out, nil
		}
		return out, err
	}
	if a.pending {
		return out, c.TimedOutf("Treg did not answer this profile lookup in time. It may still finish and bill it.")
	}
	if o.Data == nil {
		return out, nil
	}
	profile := c.LinkedInProfile(tregText(o.Data.LinkedInURL))
	if profile == "" {
		return out, nil
	}
	name := tregText(o.Data.Name)
	if name == "" {
		name = strings.TrimSpace(tregText(o.Data.First) + " " + tregText(o.Data.Last))
	}
	out.Found, out.LinkedInURL = true, profile
	out.Name, out.Title = tregClip(name, 200), tregClip(tregText(o.Data.Title), 200)
	out.Company, out.CompanyDomain = tregClip(tregText(o.Data.Company.Name), 200), domainOf(tregText(o.Data.Company.Domain))
	return out, nil
}

// tregClip keeps text from outside to n characters.
func tregClip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// tregWorse says whether err tells a caller more than have about why no
// route answered: a busy provider, then a service that can't be reached,
// then no answer in time, then a request the provider refused.
func tregWorse(err, have error) bool {
	rank := func(err error) int {
		switch {
		case err == nil:
			return 0
		case c.KindOf(err) == c.ErrQuota:
			return 4
		}
		if _, busy := c.IsBusy(err); busy {
			return 4
		}
		switch {
		case c.IsTimeout(err):
			return 2
		case c.KindOf(err) == c.ErrUnavailable:
			return 3
		}
		return 1
	}
	return rank(err) > rank(have)
}

// tregText is a JSON value when it is text, else "".
func tregText(v json.RawMessage) string {
	var s string
	if json.Unmarshal(v, &s) != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

// tregAddress is s in lower case when it looks like one email address,
// else "".
func tregAddress(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	at := strings.LastIndex(s, "@")
	if at < 1 || len(s) > 320 || strings.ContainsAny(s, " \t\r\n<>,;") || !strings.Contains(s[at+1:], ".") {
		return ""
	}
	return s
}

// tregAddresses keeps the plain addresses in list, in lower case, each
// once, in the order given.
func tregAddresses(list []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, e := range list {
		if e = tregAddress(e); e != "" && !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out
}

// tregAtDomain says whether email is at domain or one of its subdomains.
func tregAtDomain(email, domain string) bool {
	host := email[strings.LastIndex(email, "@")+1:]
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// The routes Verify asks.
const (
	tregFiltratVerify   = "filtrat.people.email.verify.resolve_catchall"
	tregBounceBan       = "bounceban.people.email.verify"
	tregBounceBanStatus = "bounceban.people.email.verify.status"
)

// The time Verify has for one address. Each call waits tregWait, past
// Treg's own hold, so a slow answer is seen. Filtrat gets tregFiltratBudget
// in all, across its retries and the workspace's accounts: when it has no
// answer by then the workspace's other verifier is asked, then BounceBan
// with what is left of tregVerifyBudget. A BounceBan check still running is
// asked about every tregVerifyPoll. The verification service gives a Treg
// address this long (VerifyWait). Variables so tests don't wait.
var (
	tregVerifyBudget  = 240 * time.Second
	tregFiltratBudget = 100 * time.Second
	tregVerifyPoll    = 4 * time.Second
)

// VerifyWait is how long one address may take (connectors.SlowVerifier).
func (Treg) VerifyWait() time.Duration { return tregVerifyBudget + 5*time.Second }

// Verify checks one address. Filtrat checks it first and resolves accept-all
// domains itself: an address it passes is deliverable. Anything else it
// says (invalid, accept-all, unknown, or no answer at all) gets BounceBan's
// deep check as a second opinion, and BounceBan's verdict stands: Filtrat
// turns down some good addresses, BounceBan wins those back.
//
// A refused token or an empty balance is the error it is, so the
// verification service falls back to the built-in checks. A BounceBan check
// that fails or is still running when the time is up is an error too,
// never a verdict about the address, and the address is checked again later.
func (t Treg) Verify(ctx context.Context, cl *c.Client, cr c.Creds, email string) (c.Verification, error) {
	return t.VerifyWith(ctx, cl, cr, email, nil)
}

// VerifyWith is Verify with another verifier to ask when Filtrat is
// unavailable (connectors.FallbackVerifier): its call failed or Treg is
// still waiting for it. A real answer from Filtrat, whatever it is, never
// asks fallback, and a refused token or an empty balance is still the
// error it is.
//
// What fallback says then decides: undeliverable is undeliverable;
// deliverable on a domain it confirmed does not accept every address
// (Verification.NotCatchAll) is deliverable, and risky there is risky;
// deliverable or risky on any other domain gets BounceBan's deep check,
// whose verdict stands. Most verifiers only flag a domain that accepts
// everything and say nothing otherwise, so only one that says outright
// that the domain does not counts as confirmed. Unknown, or a fallback
// that fails, goes on to BounceBan exactly as Verify does.
func (t Treg) VerifyWith(ctx context.Context, cl *c.Client, cr c.Creds, email string, fallback func(ctx context.Context, email string) (c.Verification, error)) (c.Verification, error) {
	v, _, err := t.VerifyAcross(ctx, cl, []c.Creds{cr}, email, fallback)
	return v, err
}

// VerifyAcross is VerifyWith on several of the workspace's own Treg
// accounts (connectors.AccountsVerifier): each call takes its turn on one
// of them, and an account that is refused, out of balance or saturated
// hands the same call to the next (across). account is the index of the
// account whose answer decided, the first when the fallback's did.
func (t Treg) VerifyAcross(ctx context.Context, cl *c.Client, accounts []c.Creds, email string, fallback func(ctx context.Context, email string) (c.Verification, error)) (c.Verification, int, error) {
	if len(accounts) == 0 {
		return c.Verification{}, 0, c.Errf(c.ErrAuth, "no Treg account to verify with")
	}
	ctx, cancel := context.WithTimeout(ctx, tregVerifyBudget)
	defer cancel()
	left := func(most time.Duration) time.Duration {
		if d, ok := ctx.Deadline(); ok {
			most = min(most, time.Until(d))
		}
		return max(most, time.Millisecond)
	}
	running := func() error {
		return c.TimedOutf("BounceBan is still checking this address through Treg. It is checked again later.")
	}

	var f struct {
		Status     json.RawMessage `json:"status"`
		Confidence *float64        `json:"confidence"`
		Domain     struct {
			CatchAll *bool `json:"catch_all"`
		} `json:"domain_intelligence"`
	}
	fctx, fcancel := context.WithTimeout(ctx, tregFiltratBudget)
	a, at, err := t.across(fctx, cl, accounts, left(tregWait), func() *http.Request {
		r := postReq(t.base(cl)+"/call/"+tregFiltratVerify, map[string]any{"email": email, "resolve_catchall": true})
		r.Header.Set("X-Treg-Route-Max-Cost", "0.01")
		return r
	}, &f)
	fcancel()
	// micros is what the calls of this check cost, as Treg said.
	micros := a.micros()
	if k := c.KindOf(err); err != nil && (k == c.ErrAuth || k == c.ErrQuota) {
		return c.Verification{}, at, err
	}
	filtrat := "no answer"
	if err == nil && !a.pending {
		if s := tregWord(tregText(f.Status)); s != "" {
			filtrat = s
		}
	}
	catchAll := err == nil && f.Domain.CatchAll != nil && *f.Domain.CatchAll
	v := c.Verification{Email: email}
	if filtrat == "verified" {
		v.Status, v.Reason, v.ProviderStatus = c.Deliverable, "Filtrat: verified", "filtrat:verified"
		if catchAll {
			v.Reason += " on an accept-all domain"
		}
		if f.Confidence != nil {
			v.Score = tregScore(*f.Confidence)
		}
		v.CostMicros = micros
		return v, at, nil
	}

	// first is what the check before BounceBan said, for the reason shown
	// beside the result: Filtrat's word, or the fallback's when it was
	// asked and BounceBan still decides.
	first, firstStatus := "Filtrat: "+filtrat, "filtrat:"+filtrat
	if (err != nil || a.pending) && fallback != nil {
		if fb, ferr := fallback(ctx, email); ferr == nil {
			// The fallback's words are data from outside, shown as they
			// are and kept short.
			why := tregClip(strings.TrimSpace(fb.Reason), 80)
			if why == "" {
				why = fb.Status
			}
			confirmed := fb.NotCatchAll && !fb.CatchAll
			settled := fb.Status == c.Undeliverable || (confirmed && (fb.Status == c.Deliverable || fb.Status == c.Risky))
			if settled {
				fb.Email = email
				fb.Reason = why + " (Filtrat: no answer)"
				fb.ProviderStatus = "filtrat:no answer/fallback:" + fb.Status
				fb.CostMicros += micros
				return fb, 0, nil
			}
			if fb.Status == c.Deliverable || fb.Status == c.Risky {
				first, firstStatus = why, "filtrat:no answer/fallback:"+fb.Status
				catchAll = catchAll || fb.CatchAll
			}
		}
	}

	var b struct {
		ID     json.RawMessage `json:"id"`
		Status json.RawMessage `json:"status"`
		Result json.RawMessage `json:"result"`
		Score  *float64        `json:"score"`
	}
	// on is the account BounceBan's check runs on: the one that took it is
	// the one asked how it went.
	on := accounts
	ask := func(route string, q url.Values) error {
		b.ID, b.Status, b.Result, b.Score = nil, nil, nil, nil
		a, i, err := t.across(ctx, cl, on, left(tregWait), func() *http.Request {
			return getReq(t.base(cl)+"/call/"+route, q)
		}, &b)
		micros += a.micros()
		if len(on) > 1 {
			at, on = i, on[i:i+1]
		}
		if err == nil && a.pending {
			return running()
		}
		return err
	}
	at = 0
	if err := ask(tregBounceBan, url.Values{"email": {email}, "mode": {"deepverify"}}); err != nil {
		return c.Verification{}, at, err
	}
	for {
		if s := tregText(b.Status); s != "verifying" && s != "queue" {
			break
		}
		id := tregText(b.ID)
		if id == "" {
			id = strings.TrimSpace(string(b.ID)) // a number
		}
		if id == "" || id == "null" || left(time.Hour) <= tregVerifyPoll {
			return c.Verification{}, at, running()
		}
		select {
		case <-ctx.Done():
			return c.Verification{}, at, running()
		case <-time.After(tregVerifyPoll):
		}
		if err := ask(tregBounceBanStatus, url.Values{"id": {id}}); err != nil {
			return c.Verification{}, at, err
		}
	}
	result := tregText(b.Result)
	switch result {
	case "deliverable":
		v.Status = c.Deliverable
	case "undeliverable":
		v.Status = c.Undeliverable
	case "risky":
		v.Status = c.Risky
	default:
		v.Status, result = c.Unknown, "unknown"
	}
	// A deliverable address on an accept-all domain has been confirmed, so
	// it is not flagged as one the domain merely accepts.
	v.CatchAll = catchAll && v.Status != c.Deliverable
	v.Reason = first + ", BounceBan: " + result
	v.ProviderStatus = firstStatus + "/bounceban:" + result
	if b.Score != nil {
		v.Score = tregScore(*b.Score)
	}
	v.CostMicros = micros
	return v, at, nil
}

// tregWord keeps a provider's status word short: it is data from outside,
// shown beside the result.
func tregWord(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// tregScore is a provider's 0 to 100 score, or nil when it is not one.
func tregScore(v float64) *int {
	n := int(v + 0.5)
	if v < 0 || n > 100 {
		return nil
	}
	return &n
}

// tregTimedOut says whether a transport error is the call running out of
// time, ours or the caller's.
func tregTimedOut(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}
