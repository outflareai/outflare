package data

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// The pinned routes, in the order the chain asks them.
const (
	tregBlitzPath   = "/call/blitzapi.people.email.find"
	tregFiltratPath = "/call/filtrat-finder.people.email.find"
	tregKittPath    = "/call/trykitt.people.email.find"
	tregAnyPath     = "/call/anymailfinder.people.email.find"
)

var tregKey = c.Creds{"api_key": "trg_test_not_a_real_token"}

type tregHandler = func(http.ResponseWriter, map[string]any, *http.Request)

func tregReply(status int, costMicro, body string) tregHandler {
	return func(w http.ResponseWriter, _ map[string]any, _ *http.Request) {
		if costMicro != "" {
			w.Header().Set("X-Treg-Cost-Micro", costMicro)
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

// tregSlow answers after the caller has given up.
func tregSlow(body string) tregHandler {
	return func(w http.ResponseWriter, _ map[string]any, _ *http.Request) {
		time.Sleep(400 * time.Millisecond)
		io.WriteString(w, body)
	}
}

// tregChainFake serves the chain's routes and records the order they were
// called in. The router route is not served, so calling it fails the test.
func tregChainFake(t *testing.T, routes map[string]tregHandler) (*fake, *c.Client, func() []string) {
	f, cl := newFake(t)
	var mu sync.Mutex
	var order []string
	for path, h := range routes {
		f.routes[path] = func(w http.ResponseWriter, body map[string]any, r *http.Request) {
			mu.Lock()
			order = append(order, strings.TrimPrefix(path, "/call/"))
			mu.Unlock()
			h(w, body, r)
		}
	}
	return f, cl, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), order...)
	}
}

func tregShortWait(t *testing.T) {
	old, oldMin := tregWait, tregRouteMin
	tregWait, tregRouteMin = 50*time.Millisecond, time.Millisecond
	t.Cleanup(func() { tregWait, tregRouteMin = old, oldMin })
}

var (
	tregMissFiltrat = tregReply(200, "0", `{"status":"not_found","confidence":0}`)
	tregMissKitt    = tregReply(200, "0", `{"email":"no-results-found"}`)
	tregMissAny     = tregReply(200, "0", `{"email":null,"email_status":"not_found","valid_email":null}`)
	tregMissBlitz   = tregReply(200, "0", `{"found":false,"email":null}`)
)

func TestTregChain(t *testing.T) {
	const profile = "https://www.linkedin.com/in/ana-lee-example"
	// A 429 that names a long pause is not asked again in place.
	limited := tregReply(429, "", `{"detail":{"error":"rate_limited","retry_after_s":30}}`)
	down := tregReply(502, "", `<html>bad gateway</html>`)
	for _, tc := range []struct {
		name    string
		profile string
		routes  map[string]tregHandler
		order   string
		email   string
		status  string
		credits float64
		// kind is the error's kind when the lookup fails; timeout says
		// it is the kind a caller takes for "no address".
		kind    string
		timeout bool
		busy    bool
	}{
		{name: "the first route with an address ends the chain",
			routes: map[string]tregHandler{
				tregFiltratPath: tregReply(200, "3000", `{"status":"found","found_email":" Ana.Lee@Acme-io.example "}`),
			},
			order: "filtrat-finder", email: "ana.lee@acme-io.example", status: c.EmailGuessed, credits: 1},
		{name: "a miss moves on, and only billed calls count",
			routes: map[string]tregHandler{
				tregFiltratPath: tregMissFiltrat,
				tregKittPath:    tregReply(200, "5000", `{"email":"ana@acme-io.example","validity":"valid-risky"}`),
			},
			order: "filtrat-finder trykitt", email: "ana@acme-io.example", status: c.EmailGuessed, credits: 1},
		{name: "Kitt says it checked the mailbox",
			routes: map[string]tregHandler{
				tregFiltratPath: tregMissFiltrat,
				tregKittPath:    tregReply(200, "5000", `{"email":"ana@acme-io.example","validity":"valid"}`),
			},
			order: "filtrat-finder trykitt", email: "ana@acme-io.example", status: c.EmailVerified, credits: 1},
		{name: "every billed route is summed, and Anymailfinder's valid address is verified",
			routes: map[string]tregHandler{
				tregFiltratPath: tregReply(200, "3000", `{"status":"not_found"}`),
				tregKittPath:    tregReply(200, "5000", `{"email":"no-results-found"}`),
				tregAnyPath:     tregReply(200, "20000", `{"email":"ana.lee@acme-io.example","valid_email":"ana.lee@acme-io.example","email_status":"valid"}`),
			},
			order: "filtrat-finder trykitt anymailfinder", email: "ana.lee@acme-io.example", status: c.EmailVerified, credits: 3},
		{name: "Anymailfinder's unchecked address is a guess",
			routes: map[string]tregHandler{
				tregFiltratPath: tregMissFiltrat, tregKittPath: tregMissKitt,
				tregAnyPath: tregReply(200, "20000", `{"email":"ana.lee@acme-io.example","valid_email":null,"email_status":"risky"}`),
			},
			order: "filtrat-finder trykitt anymailfinder", email: "ana.lee@acme-io.example", status: c.EmailGuessed, credits: 1},
		{name: "no cost header: a found address counts as one billed call",
			routes: map[string]tregHandler{
				tregFiltratPath: tregReply(200, "", `{"status":"found","found_email":"ana@acme-io.example"}`),
			},
			order: "filtrat-finder", email: "ana@acme-io.example", status: c.EmailGuessed, credits: 1},
		{name: "every route misses",
			routes: map[string]tregHandler{tregFiltratPath: tregMissFiltrat, tregKittPath: tregMissKitt, tregAnyPath: tregMissAny},
			order:  "filtrat-finder trykitt anymailfinder"},
		{name: "answers that are not the shape expected are misses, never a broken service",
			routes: map[string]tregHandler{
				tregFiltratPath: tregReply(200, "0", `{"status":"found","found_email":{"address":"ana@acme-io.example"}}`),
				tregKittPath:    tregReply(200, "0", `false`),
				tregAnyPath:     tregReply(200, "0", `{"email":["ana@acme-io.example"],"valid_email":"not an address"}`),
			},
			order: "filtrat-finder trykitt anymailfinder"},
		{name: "a route with no answer in time has no address, and the next is asked",
			routes: map[string]tregHandler{
				tregFiltratPath: tregSlow(`{"status":"found","found_email":"late@acme-io.example"}`),
				tregKittPath:    tregReply(200, "5000", `{"email":"ana@acme-io.example"}`),
			},
			order: "filtrat-finder trykitt", email: "ana@acme-io.example", status: c.EmailGuessed, credits: 1},
		{name: "a timeout and misses are a miss",
			routes: map[string]tregHandler{tregFiltratPath: tregMissFiltrat, tregKittPath: tregSlow(`{}`), tregAnyPath: tregMissAny},
			order:  "filtrat-finder trykitt anymailfinder"},
		{name: "a task still running at Treg is a billed miss, and the next is asked",
			routes: map[string]tregHandler{
				tregFiltratPath: tregReply(202, "", `{"_treg":{"outcome":"pending"}}`),
				tregKittPath:    tregMissKitt, tregAnyPath: tregMissAny,
			},
			order: "filtrat-finder trykitt anymailfinder", credits: 1},
		{name: "a failing route does not stop the chain",
			routes: map[string]tregHandler{
				tregFiltratPath: down, tregKittPath: limited,
				tregAnyPath: tregReply(200, "20000", `{"valid_email":"ana@acme-io.example"}`),
			},
			order: "filtrat-finder trykitt anymailfinder", email: "ana@acme-io.example", status: c.EmailVerified, credits: 1},
		{name: "every route out of time is a timeout",
			routes: map[string]tregHandler{tregFiltratPath: tregSlow(`{}`), tregKittPath: tregSlow(`{}`), tregAnyPath: tregSlow(`{}`)},
			order:  "filtrat-finder trykitt anymailfinder", kind: c.ErrUnavailable, timeout: true},
		{name: "every route failing is a service that is down, whatever timed out",
			routes: map[string]tregHandler{tregFiltratPath: tregSlow(`{}`), tregKittPath: down, tregAnyPath: tregSlow(`{}`)},
			order:  "filtrat-finder trykitt anymailfinder", kind: c.ErrUnavailable},
		{name: "every route failing with a busy one among them is busy, never the account's quota",
			routes: map[string]tregHandler{tregFiltratPath: limited, tregKittPath: down, tregAnyPath: down},
			order:  "filtrat-finder trykitt anymailfinder", kind: c.ErrUnavailable, busy: true},
		{name: "every route refusing the request is a refusal",
			routes: map[string]tregHandler{
				tregFiltratPath: tregReply(422, "", `{"detail":"first_name is required"}`),
				tregKittPath:    tregReply(404, "", `{"detail":"no such route"}`),
				tregAnyPath:     tregReply(422, "", `{"detail":"full_name is required"}`),
			},
			order: "filtrat-finder trykitt anymailfinder", kind: c.ErrInvalid},
		{name: "a refused token ends the chain",
			routes: map[string]tregHandler{tregFiltratPath: tregReply(401, "", `{"detail":"invalid token"}`)},
			order:  "filtrat-finder", kind: c.ErrAuth},
		{name: "an empty balance ends the chain",
			routes: map[string]tregHandler{tregFiltratPath: tregMissFiltrat, tregKittPath: tregReply(402, "", `{"detail":"insufficient balance"}`)},
			order:  "filtrat-finder trykitt", kind: c.ErrQuota},
		{name: "a used-up trial ends the chain",
			routes: map[string]tregHandler{tregFiltratPath: tregReply(403, "", `{"detail":{"error":"trial_allowance_reached"}}`)},
			order:  "filtrat-finder", kind: c.ErrQuota},
		{name: "an answer that is not JSON is a failing route",
			routes: map[string]tregHandler{tregFiltratPath: tregReply(200, "", `<html>hello</html>`), tregKittPath: tregMissKitt, tregAnyPath: tregMissAny},
			order:  "filtrat-finder trykitt anymailfinder"},
		{name: "with a LinkedIn profile, Blitz is asked first",
			profile: profile,
			routes: map[string]tregHandler{
				tregBlitzPath: tregReply(200, "500", `{"found":true,"email":"ana@acme-io.example","all_emails":[{"email":"ana@acme-io.example"}]}`),
			},
			order: "blitzapi", email: "ana@acme-io.example", status: c.EmailGuessed, credits: 1},
		{name: "Blitz's address at this company wins over its primary one elsewhere",
			profile: profile,
			routes: map[string]tregHandler{
				tregBlitzPath: tregReply(200, "500", `{"found":true,"email":"ana@northpeak.example","all_emails":[{"email":"ana@northpeak.example"},{"email":"Ana.Lee@mail.acme-io.example"}]}`),
			},
			order: "blitzapi", email: "ana.lee@mail.acme-io.example", status: c.EmailGuessed, credits: 1},
		{name: "Blitz's address at another company is no address",
			profile: profile,
			routes: map[string]tregHandler{
				tregBlitzPath:   tregReply(200, "500", `{"found":true,"email":"ana@northpeak.example","all_emails":[{"email":"ana@notacme-io.example"}]}`),
				tregFiltratPath: tregReply(200, "3000", `{"status":"found","found_email":"ana.lee@acme-io.example"}`),
			},
			order: "blitzapi filtrat-finder", email: "ana.lee@acme-io.example", status: c.EmailGuessed, credits: 2},
		{name: "Blitz is not asked about a company page",
			profile: "https://www.linkedin.com/company/acme-io-example",
			routes:  map[string]tregHandler{tregBlitzPath: tregMissBlitz, tregFiltratPath: tregMissFiltrat, tregKittPath: tregMissKitt, tregAnyPath: tregMissAny},
			order:   "filtrat-finder trykitt anymailfinder"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tregShortWait(t)
			_, cl, order := tregChainFake(t, tc.routes)
			q := c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "https://www.acme-io.example/", LinkedInURL: tc.profile}
			got, err := Treg{}.EnrichPerson(context.Background(), cl, tregKey, q)
			if o := strings.ReplaceAll(strings.Join(order(), " "), ".people.email.find", ""); o != tc.order {
				t.Errorf("routes called: %q, want %q", o, tc.order)
			}
			if tc.kind != "" {
				if err == nil || c.KindOf(err) != tc.kind || c.IsTimeout(err) != tc.timeout || isBusy(err) != tc.busy || got.Found || got.Credits != 0 {
					t.Fatalf("got %+v %v (timeout %v), want a %s error", got, err, c.IsTimeout(err), tc.kind)
				}
				return
			}
			if err != nil || got.Credits != tc.credits || got.Found != (tc.email != "") || got.Person.Email != tc.email || got.Person.EmailStatus != tc.status {
				t.Fatalf("got %+v %v, want %q (%s) for %v credits", got, err, tc.email, tc.status, tc.credits)
			}
			if tc.email != "" {
				if p := got.Person; !p.HasEmail || p.FirstName != "Ana" || p.LastName != "Lee" || p.Company.Domain != "acme-io.example" || p.LinkedInURL != tc.profile {
					t.Fatalf("person: %+v", p)
				}
			}
		})
	}
}

// Each route gets its provider's own field names, the workspace's token
// and a price ceiling.
func TestTregChainRequests(t *testing.T) {
	const profile = "https://www.linkedin.com/in/ana-lee-example"
	ceilings := map[string]string{tregBlitzPath: "0.01", tregFiltratPath: "0.01", tregKittPath: "0.01", tregAnyPath: "0.03"}
	check := func(path string, miss tregHandler) tregHandler {
		return func(w http.ResponseWriter, body map[string]any, r *http.Request) {
			if r.Method != http.MethodPost || r.Header.Get("X-Treg-Token") != "trg_test_not_a_real_token" || r.Header.Get("X-Treg-Route-Max-Cost") != ceilings[path] {
				t.Errorf("%s: %s %v", path, r.Method, r.Header)
			}
			miss(w, body, r)
		}
	}
	f, cl, _ := tregChainFake(t, map[string]tregHandler{
		tregBlitzPath: check(tregBlitzPath, tregMissBlitz), tregFiltratPath: check(tregFiltratPath, tregMissFiltrat),
		tregKittPath: check(tregKittPath, tregMissKitt), tregAnyPath: check(tregAnyPath, tregMissAny),
	})
	q := c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "www.acme-io.example", LinkedInURL: profile}
	if got, err := (Treg{}).EnrichPerson(context.Background(), cl, tregKey, q); err != nil || got.Found || got.Credits != 0 {
		t.Fatalf("all miss: %+v %v", got, err)
	}
	for path, want := range map[string]map[string]any{
		tregBlitzPath:   {"person_linkedin_url": profile},
		tregFiltratPath: {"first_name": "Ana", "last_name": "Lee", "domain": "acme-io.example", "resolve_catchall": true},
		tregKittPath:    {"fullName": "Ana Lee", "domain": "acme-io.example", "realtime": true},
		tregAnyPath:     {"domain": "acme-io.example", "full_name": "Ana Lee"},
	} {
		if got := f.bodies[path]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s body: %v, want %v", path, got, want)
		}
	}

	if _, err := (Treg{}).EnrichPerson(context.Background(), cl, tregKey, c.PersonQuery{FirstName: "Ana", LastName: "Lee"}); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("needs a domain: %v", err)
	}
}

// The chain has one time limit for a person: with too little of it left,
// the next route is not started.
func TestTregChainStopsWhenItsTimeIsUp(t *testing.T) {
	tregShortWait(t)
	oldBudget := tregChainBudget
	tregChainBudget, tregRouteMin = 80*time.Millisecond, 40*time.Millisecond
	t.Cleanup(func() { tregChainBudget = oldBudget })
	_, cl, order := tregChainFake(t, map[string]tregHandler{
		tregFiltratPath: tregSlow(`{}`), tregKittPath: tregReply(200, "5000", `{"email":"ana@acme-io.example"}`), tregAnyPath: tregMissAny,
	})
	q := c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "acme-io.example"}
	_, err := Treg{}.EnrichPerson(context.Background(), cl, tregKey, q)
	if !c.IsTimeout(err) || len(order()) != 1 {
		t.Fatalf("got %v after %v", err, order())
	}

	// A cancelled run asks nobody else.
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Treg{}).EnrichPerson(gone, cl, tregKey, q); c.KindOf(err) != c.ErrUnavailable || c.IsTimeout(err) || len(order()) != 1 {
		t.Fatalf("cancelled: %v after %v", err, order())
	}
	// One whose time ran out is a timeout, even after a route answered.
	late, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, cl, order = tregChainFake(t, map[string]tregHandler{
		tregFiltratPath: func(w http.ResponseWriter, b map[string]any, r *http.Request) {
			time.Sleep(30 * time.Millisecond)
			tregMissFiltrat(w, b, r)
		},
		tregKittPath: tregMissKitt, tregAnyPath: tregMissAny,
	})
	tregWait = time.Second
	if got, err := (Treg{}).EnrichPerson(late, cl, tregKey, q); !c.IsTimeout(err) || got.Credits != 0 || len(order()) != 1 {
		t.Fatalf("out of time: %+v %v after %v", got, err, order())
	}
}

func TestTregSaturatedIsRetriedThenGivesUp(t *testing.T) {
	old := tregBackoff
	tregBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { tregBackoff = old })
	f, cl := newFake(t)
	ctx := context.Background()
	q := c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "acme-io.example"}

	calls := 0
	f.routes[tregFiltratPath] = func(w http.ResponseWriter, _ map[string]any, _ *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(503)
			io.WriteString(w, `{"treg_saturated":true}`)
			return
		}
		w.Header().Set("X-Treg-Cost-Micro", "3000")
		io.WriteString(w, `{"status":"found","found_email":"ana@acme-io.example"}`)
	}
	if got, err := (Treg{}).EnrichPerson(ctx, cl, tregKey, q); err != nil || !got.Found || calls != 3 {
		t.Fatalf("saturated then found: %+v %v after %d calls", got, err, calls)
	}

	calls = 0
	always := func(w http.ResponseWriter, _ map[string]any, _ *http.Request) {
		calls++
		io.WriteString(w, `{"treg_saturated":true}`)
	}
	f.routes[tregFiltratPath], f.routes[tregKittPath], f.routes[tregAnyPath] = always, always, always
	// Still saturated after the three retries: busy, so the caller hands the
	// call to another account and asks again, never a miss.
	if _, err := (Treg{}).EnrichPerson(ctx, cl, tregKey, q); !isBusy(err) || c.KindOf(err) != c.ErrUnavailable || c.IsTimeout(err) || calls != 12 {
		t.Fatalf("always saturated: %v after %d calls", err, calls)
	}
}

const (
	tregVerifyPath = "/call/filtrat.people.email.verify.resolve_catchall"
	tregBBPath     = "/call/bounceban.people.email.verify"
	tregBBStatus   = "/call/bounceban.people.email.verify.status"
)

func TestTregVerify(t *testing.T) {
	oldPoll := tregVerifyPoll
	tregVerifyPoll = time.Millisecond
	t.Cleanup(func() { tregVerifyPoll = oldPoll })
	const email = "ana@acme-io.example"
	filtrat := func(status string, catchAll bool) tregHandler {
		return tregReply(200, "1250", `{"status":"`+status+`","confidence":97.6,"domain_intelligence":{"catch_all":`+strconv.FormatBool(catchAll)+`}}`)
	}
	bb := func(result string) tregHandler {
		return tregReply(200, "4000", `{"id":"bb_1","status":"success","result":"`+result+`","score":88}`)
	}
	score := func(n int) *int { return &n }
	for _, tc := range []struct {
		name    string
		filtrat tregHandler
		bb      tregHandler // nil: BounceBan must not be asked
		status  tregHandler // the status route, asked while BounceBan is still checking
		want    c.Verification
		kind    string
		timeout bool
	}{
		{name: "Filtrat's pass is final", filtrat: filtrat("verified", false),
			want: c.Verification{Status: c.Deliverable, Reason: "Filtrat: verified", ProviderStatus: "filtrat:verified", Score: score(98)}},
		{name: "Filtrat's pass on an accept-all domain is final too", filtrat: filtrat("verified", true),
			want: c.Verification{Status: c.Deliverable, Reason: "Filtrat: verified on an accept-all domain", ProviderStatus: "filtrat:verified", Score: score(98)}},
		{name: "accept-all, BounceBan confirms it", filtrat: filtrat("catch_all", true), bb: bb("deliverable"),
			want: c.Verification{Status: c.Deliverable, Reason: "Filtrat: catch_all, BounceBan: deliverable", ProviderStatus: "filtrat:catch_all/bounceban:deliverable", Score: score(88)}},
		{name: "accept-all, BounceBan can't tell", filtrat: filtrat("catch_all", true), bb: bb("risky"),
			want: c.Verification{Status: c.Risky, CatchAll: true, Reason: "Filtrat: catch_all, BounceBan: risky", ProviderStatus: "filtrat:catch_all/bounceban:risky", Score: score(88)}},
		{name: "unknown at both", filtrat: filtrat("unknown", false), bb: bb("unknown"),
			want: c.Verification{Status: c.Unknown, Reason: "Filtrat: unknown, BounceBan: unknown", ProviderStatus: "filtrat:unknown/bounceban:unknown", Score: score(88)}},
		{name: "Filtrat turns it down, BounceBan agrees", filtrat: filtrat("invalid", false), bb: bb("undeliverable"),
			want: c.Verification{Status: c.Undeliverable, Reason: "Filtrat: invalid, BounceBan: undeliverable", ProviderStatus: "filtrat:invalid/bounceban:undeliverable", Score: score(88)}},
		{name: "Filtrat turns it down, BounceBan wins it back", filtrat: filtrat("invalid", false), bb: bb("deliverable"),
			want: c.Verification{Status: c.Deliverable, Reason: "Filtrat: invalid, BounceBan: deliverable", ProviderStatus: "filtrat:invalid/bounceban:deliverable", Score: score(88)}},
		{name: "a BounceBan verdict that is none of the four is unknown", filtrat: filtrat("unknown", true),
			bb:   tregReply(200, "", `{"status":"success","result":{"odd":true},"score":-1}`),
			want: c.Verification{Status: c.Unknown, CatchAll: true, Reason: "Filtrat: unknown, BounceBan: unknown", ProviderStatus: "filtrat:unknown/bounceban:unknown"}},
		{name: "Filtrat down: BounceBan decides alone", filtrat: tregReply(502, "", `bad gateway`), bb: bb("deliverable"),
			want: c.Verification{Status: c.Deliverable, Reason: "Filtrat: no answer, BounceBan: deliverable", ProviderStatus: "filtrat:no answer/bounceban:deliverable", Score: score(88)}},
		{name: "Filtrat still running: BounceBan decides alone", filtrat: tregReply(202, "", `{"_treg":{"outcome":"pending"}}`), bb: bb("undeliverable"),
			want: c.Verification{Status: c.Undeliverable, Reason: "Filtrat: no answer, BounceBan: undeliverable", ProviderStatus: "filtrat:no answer/bounceban:undeliverable", Score: score(88)}},
		{name: "BounceBan still checking, then done", filtrat: filtrat("catch_all", true),
			bb:     tregReply(200, "4000", `{"id":7,"status":"verifying"}`),
			status: tregReply(200, "0", `{"id":7,"status":"success","result":"deliverable","score":91}`),
			want:   c.Verification{Status: c.Deliverable, Reason: "Filtrat: catch_all, BounceBan: deliverable", ProviderStatus: "filtrat:catch_all/bounceban:deliverable", Score: score(91)}},
		{name: "BounceBan still checking with nothing to ask about", filtrat: filtrat("catch_all", true),
			bb: tregReply(200, "", `{"status":"queue"}`), kind: c.ErrUnavailable, timeout: true},
		{name: "BounceBan still running at Treg", filtrat: filtrat("catch_all", true),
			bb: tregReply(202, "", `{"_treg":{"outcome":"pending"}}`), kind: c.ErrUnavailable, timeout: true},
		{name: "BounceBan down is never a verdict", filtrat: filtrat("invalid", false),
			bb: tregReply(502, "", `bad gateway`), kind: c.ErrUnavailable},
		{name: "BounceBan refusing the request is never a verdict", filtrat: filtrat("invalid", false),
			bb: tregReply(422, "", `{"detail":"email is required"}`), kind: c.ErrInvalid},
		{name: "a refused token", filtrat: tregReply(401, "", `{"detail":"invalid token"}`), kind: c.ErrAuth},
		{name: "an empty balance at Filtrat", filtrat: tregReply(402, "", `{"detail":"insufficient balance"}`), kind: c.ErrQuota},
		{name: "an empty balance at BounceBan", filtrat: filtrat("catch_all", true),
			bb: tregReply(402, "", `{"detail":"insufficient balance"}`), kind: c.ErrQuota},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, cl := newFake(t)
			f.routes[tregVerifyPath] = func(w http.ResponseWriter, body map[string]any, r *http.Request) {
				if r.Method != http.MethodPost || r.Header.Get("X-Treg-Token") != "trg_test_not_a_real_token" || r.Header.Get("X-Treg-Route-Max-Cost") != "0.01" ||
					!reflect.DeepEqual(body, map[string]any{"email": email, "resolve_catchall": true}) {
					t.Errorf("Filtrat request: %s %v %v", r.Method, r.Header, body)
				}
				tc.filtrat(w, body, r)
			}
			if tc.bb != nil {
				f.routes[tregBBPath] = func(w http.ResponseWriter, body map[string]any, r *http.Request) {
					if q := r.URL.Query(); r.Method != http.MethodGet || q.Get("email") != email || q.Get("mode") != "deepverify" || r.Header.Get("X-Treg-Token") == "" {
						t.Errorf("BounceBan request: %s %s", r.Method, r.URL)
					}
					tc.bb(w, body, r)
				}
			}
			if tc.status != nil {
				f.routes[tregBBStatus] = func(w http.ResponseWriter, body map[string]any, r *http.Request) {
					if r.Method != http.MethodGet || r.URL.Query().Get("id") != "7" {
						t.Errorf("status request: %s %s", r.Method, r.URL)
					}
					tc.status(w, body, r)
				}
			}
			got, err := Treg{}.Verify(context.Background(), cl, tregKey, email)
			if tc.kind != "" {
				if err == nil || c.KindOf(err) != tc.kind || c.IsTimeout(err) != tc.timeout {
					t.Fatalf("got %+v %v (timeout %v), want a %s error", got, err, c.IsTimeout(err), tc.kind)
				}
				return
			}
			tc.want.Email = email
			got.CostMicros = 0 // TestTregReportsWhatItCost
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v %v, want %+v", got, err, tc.want)
			}
		})
	}
}

// A BounceBan check that outlasts the time one address has is checked again
// later, never given a verdict.
func TestTregVerifyRunsOutOfTime(t *testing.T) {
	oldPoll, oldBudget := tregVerifyPoll, tregVerifyBudget
	tregVerifyPoll, tregVerifyBudget = 10*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { tregVerifyPoll, tregVerifyBudget = oldPoll, oldBudget })
	f, cl := newFake(t)
	f.routes[tregVerifyPath] = tregReply(200, "1250", `{"status":"catch_all","domain_intelligence":{"catch_all":true}}`)
	f.routes[tregBBPath] = tregReply(200, "4000", `{"id":"bb_1","status":"verifying"}`)
	polls := 0
	f.routes[tregBBStatus] = func(w http.ResponseWriter, _ map[string]any, _ *http.Request) {
		polls++
		io.WriteString(w, `{"id":"bb_1","status":"verifying"}`)
	}
	_, err := Treg{}.Verify(context.Background(), cl, tregKey, "ana@acme-io.example")
	if !c.IsTimeout(err) || polls == 0 || polls > 12 {
		t.Fatalf("got %v after %d polls", err, polls)
	}
}

func TestTregCanVerify(t *testing.T) {
	var conn c.Connector = Treg{}
	if _, ok := conn.(c.Verifier); !ok {
		t.Fatal("Treg does not implement connectors.Verifier")
	}
	d := Treg{}.Definition()
	for _, capability := range []string{c.CapFindEmail, c.CapEnrichPerson, c.CapVerify} {
		if !d.Has(capability) {
			t.Errorf("Treg lacks %s", capability)
		}
	}
}

func TestTregConnectionCheck(t *testing.T) {
	f, cl := newFake(t)
	ctx := context.Background()
	f.routes["/auth/me"] = func(w http.ResponseWriter, _ map[string]any, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("X-Treg-Token") != "trg_test_not_a_real_token" {
			t.Errorf("request: %s %v", r.Method, r.Header)
		}
		io.WriteString(w, `{"org_id":42}`)
	}
	f.routes["/orgs/42/balance"] = reply(`{"balance_micro":12340000}`)
	acct, err := Treg{}.Test(ctx, cl, tregKey)
	if err != nil || acct.ID != "42" || acct.Name != "Treg" || acct.Details["balance_usd"] != "12.34" || acct.Credits != nil {
		t.Fatalf("account: %+v %v", acct, err)
	}

	// The balance is optional.
	f.routes["/orgs/42/balance"] = tregReply(403, "", `{"detail":"not allowed"}`)
	if acct, err = (Treg{}).Test(ctx, cl, tregKey); err != nil || acct.ID != "42" || acct.Details != nil {
		t.Fatalf("no balance: %+v %v", acct, err)
	}

	f.routes["/auth/me"] = tregReply(401, "", `{"detail":"invalid token"}`)
	if _, err = (Treg{}).Test(ctx, cl, tregKey); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("bad token: %v", err)
	}
	f.routes["/auth/me"] = reply(`{"org_id":null}`)
	if _, err = (Treg{}).Test(ctx, cl, tregKey); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("no team: %v", err)
	}
	f.routes["/auth/me"] = reply(`not json`)
	if _, err = (Treg{}).Test(ctx, cl, tregKey); c.KindOf(err) != c.ErrUnavailable {
		t.Fatalf("malformed: %v", err)
	}
}

// What each call cost, from Treg's X-Treg-Cost-Micro header, comes back
// with the answer, summed over every call a lookup or a check made, so the
// spend ledger books money and not only calls.
func TestTregReportsWhatItCost(t *testing.T) {
	tregShortWait(t)
	q := c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "acme-io.example"}

	_, cl, _ := tregChainFake(t, map[string]tregHandler{
		tregFiltratPath: tregReply(200, "3000", `{"status":"not_found"}`),
		tregKittPath:    tregReply(200, "5000", `{"email":"ana@acme-io.example","validity":"valid"}`),
	})
	e, err := Treg{}.EnrichPerson(context.Background(), cl, tregKey, q)
	if err != nil || e.Credits != 2 || e.CostMicros != 8000 {
		t.Fatalf("lookup: %+v %v, want 2 credits costing 8000 micros", e, err)
	}

	_, cl, _ = tregChainFake(t, map[string]tregHandler{
		tregAnyPath: tregReply(200, "20000", `{"email":"ana.lee@acme-io.example","valid_email":"ana.lee@acme-io.example","email_status":"valid"}`),
	})
	st, err := Treg{}.FindStep(context.Background(), cl, tregKey, "anymailfinder", q)
	if err != nil || st.Credits != 1 || st.CostMicros != 20000 {
		t.Fatalf("step: %+v %v, want 1 credit costing 20000 micros", st, err)
	}

	_, cl, _ = tregChainFake(t, map[string]tregHandler{tregFiltratPath: tregReply(200, "", `{"status":"found","found_email":"ana@acme-io.example"}`)})
	if e, err := (Treg{}).EnrichPerson(context.Background(), cl, tregKey, q); err != nil || e.Credits != 1 || e.CostMicros != 0 {
		t.Fatalf("no cost header: %+v %v, want a credit with no cost", e, err)
	}

	oldPoll := tregVerifyPoll
	tregVerifyPoll = time.Millisecond
	t.Cleanup(func() { tregVerifyPoll = oldPoll })
	f, cl := newFake(t)
	f.routes[tregVerifyPath] = tregReply(200, "1250", `{"status":"catch_all","domain_intelligence":{"catch_all":true}}`)
	f.routes[tregBBPath] = tregReply(200, "4000", `{"id":7,"status":"verifying"}`)
	f.routes[tregBBStatus] = tregReply(200, "0", `{"id":7,"status":"success","result":"deliverable","score":91}`)
	v, err := Treg{}.Verify(context.Background(), cl, tregKey, "ana@acme-io.example")
	if err != nil || v.Status != c.Deliverable || v.CostMicros != 5250 {
		t.Fatalf("check: %+v %v, want deliverable costing 5250 micros", v, err)
	}
}
