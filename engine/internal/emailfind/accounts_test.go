package emailfind

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/data"
	"github.com/outflareai/outflare/engine/internal/track"
)

// tregNoAdmission is Treg's answer when it could not admit a call in its
// own time: HTTP 429 after about 20 seconds, nothing charged. The real
// answer names no pause; the one here does so the test does not wait out
// the 2, 4 and 8 second backoff.
const tregNoAdmission = `{"detail":{"error":"money_admission_timeout","retry_after_s":0.01,` +
	`"message":"could not admit this call in time; not charged"}}`

// Two Treg accounts of one workspace, as fake tokens.
const (
	tregTokenA = "trg_test_not_a_real_token_a"
	tregTokenB = "trg_test_not_a_real_token_b"
)

// twoTregs is a Treg with two accounts. An account in full answers every
// route with money_admission_timeout; one in pending answers 202; the
// others answer with the route's body. It records which account each call
// to a route came from.
type twoTregs struct {
	mu      sync.Mutex
	full    map[string]bool
	pending map[string]bool
	routes  map[string]string
	calls   map[string][]string // route -> tokens, in order
}

func (f *twoTregs) asked(route, token string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, t := range f.calls[route] {
		if t == token {
			n++
		}
	}
	return n
}

// twoTregEnv is a workspace with two Treg connections of its own, A
// connected first and chosen as its verifier.
func twoTregEnv(t *testing.T, more ...connectors.Connector) (e *finderEnv, f *twoTregs, a, b string) {
	t.Helper()
	f = &twoTregs{full: map[string]bool{}, pending: map[string]bool{}, routes: map[string]string{}, calls: map[string][]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, token := strings.TrimPrefix(r.URL.Path, "/call/"), r.Header.Get("X-Treg-Token")
		f.mu.Lock()
		f.calls[route] = append(f.calls[route], token)
		full, pending := f.full[token], f.pending[token]
		body, ok := f.routes[route]
		f.mu.Unlock()
		switch {
		case full:
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, tregNoAdmission)
		case pending:
			w.WriteHeader(http.StatusAccepted)
			io.WriteString(w, `{"_treg":{"outcome":"pending"}}`)
		case !ok:
			t.Errorf("unexpected call to %s", route)
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.Header().Set("X-Treg-Cost-Micro", "3000")
			io.WriteString(w, body)
		}
	}))
	t.Cleanup(srv.Close)
	e = newFinderEnv(t, append([]connectors.Connector{data.Treg{}}, more...)...)
	e.store.Client = &connectors.Client{BaseURLs: map[string]string{"treg": srv.URL}}
	a = e.connect("treg", tregTokenA)
	b = e.connect("treg", tregTokenB)
	if _, err := e.pool.Exec(context.Background(),
		`INSERT INTO verification_settings (workspace_id, integration_id) VALUES ($1, $2)`, e.ws, a); err != nil {
		t.Fatal(err)
	}
	return e, f, a, b
}

func verifyOne(email string) VerifyRequest {
	var req VerifyRequest
	req.Items = append(req.Items, struct {
		Ref   string `json:"ref"`
		Email string `json:"email"`
	}{Ref: "r1", Email: email})
	return req
}

// A Treg account that cannot admit a call (HTTP 429
// money_admission_timeout) is asked again in place, then hands the same
// step to the workspace's other Treg connection, which answers. It is
// Treg's capacity, not this account's pace: the connection is not slowed.
func TestAskFinderStepHandsOverToTheOtherAccount(t *testing.T) {
	e, treg, a, b := twoTregEnv(t)
	treg.full[tregTokenA] = true
	treg.routes[routeFiltratFind] = `{"status":"found","found_email":"ana.lee@acme.example"}`
	events := track.Capture(t)

	// Whichever account's turn it is, the healthy one answers.
	for i, who := range []FindItem{
		{Ref: "r1", FirstName: "Ana", LastName: "Lee", Domain: "acme.example"},
		{Ref: "r2", FirstName: "Ben", LastName: "Ito", Domain: "acme.example"},
		{Ref: "r3", FirstName: "Cy", LastName: "Roe", Domain: "acme.example"},
	} {
		got := e.step("filtrat", who)
		if got.Code != "" || !got.Asked || got.Connection != b || !reflect.DeepEqual(got.Addresses, []string{"ana.lee@acme.example"}) {
			t.Fatalf("step %d with one account full: %+v", i, got)
		}
	}
	if n := treg.asked(routeFiltratFind, tregTokenB); n != 3 {
		t.Fatalf("the healthy account answered %d calls, want 3", n)
	}
	// The full account was asked again in place each time its turn came:
	// once, then after each backoff.
	if n := treg.asked(routeFiltratFind, tregTokenA); n == 0 || n%4 != 0 {
		t.Fatalf("the full account got %d calls, want 4 for each turn", n)
	}
	var slowed bool
	if err := e.pool.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM pipeline_limits WHERE key LIKE '%' || $1 || '%' AND slow_until > now())`, a).Scan(&slowed); err != nil || slowed {
		t.Fatalf("Treg's own capacity must not slow the connection: %v, %v", slowed, err)
	}
	for _, ev := range events() {
		if ev.Name == "email.finder_failed" && (ev.Props["kind"] != "busy" || ev.Props["throttled"] != false || ev.Props["integration_id"] != a) {
			t.Fatalf("event = %+v", ev)
		}
	}

	// Every account full: the step is busy, never a miss and never a stop.
	treg.full[tregTokenB] = true
	got := e.step("filtrat", FindItem{Ref: "r4", FirstName: "Dee", LastName: "Ono", Domain: "acme.example"})
	if got.Code != CodePending || !got.Busy || got.Asked || got.StopCode != "" {
		t.Fatalf("every account full: %+v", got)
	}
}

// A lookup Treg is still working on (202 pending) is final for the step:
// it may yet finish and be billed, so the other account is never asked the
// same thing, and asking the step again costs nothing.
func TestAskFinderStepPendingIsFinalOnEveryAccount(t *testing.T) {
	e, treg, _, _ := twoTregEnv(t)
	treg.pending[tregTokenA], treg.pending[tregTokenB] = true, true
	ana := FindItem{Ref: "r1", FirstName: "Ana", LastName: "Lee", Domain: "acme.example"}

	got := e.step("filtrat", ana)
	if got.Code != "" || !got.Asked || len(got.Addresses) != 0 || got.Cached {
		t.Fatalf("a pending lookup: %+v", got)
	}
	if got = e.step("filtrat", ana); got.Code != "" || !got.Asked || !got.Cached {
		t.Fatalf("asked again: %+v", got)
	}
	if n := treg.asked(routeFiltratFind, tregTokenA) + treg.asked(routeFiltratFind, tregTokenB); n != 1 {
		t.Fatalf("a pending lookup was sent %d times, want 1", n)
	}
}

// The workspace's verifier is one Treg account. When it cannot admit a
// check, the same check goes to the workspace's other Treg connection, and
// is booked there.
func TestVerifyEmailsHandsOverToTheOtherAccount(t *testing.T) {
	e, treg, a, b := twoTregEnv(t)
	treg.full[tregTokenA] = true
	treg.routes[routeFiltratVerify] = `{"status":"verified","confidence":97}`
	events := track.Capture(t)

	const email = "ana.lee@acme.example"
	res, err := e.s.VerifyEmails(context.Background(), e.ws, verifyOne(email))
	if err != nil || len(res.Items) != 1 || res.Items[0].Code != CodeVerified || res.Items[0].Cost.Provider != "treg" || res.Items[0].Cost.Connection != b {
		t.Fatalf("the chosen account full: %+v %v", res, err)
	}
	if treg.asked(routeFiltratVerify, tregTokenB) != 1 || treg.asked(routeBounceBan, tregTokenA)+treg.asked(routeBounceBan, tregTokenB) != 0 {
		t.Fatalf("calls: %v", treg.calls)
	}
	if e.booked(a) != 0 || e.booked(b) != 1 {
		t.Fatalf("booked %v on the full account and %v on the other, want 0 and 1", e.booked(a), e.booked(b))
	}
	handed := 0
	for _, ev := range events() {
		if ev.Name == "email.verifier_handed_over" {
			handed++
			if ev.Props["integration_id"] != b || ev.Props["from_integration_id"] != a || ev.Props["connector"] != "treg" || ev.Props["addresses"] != 1 {
				t.Fatalf("event = %+v", ev)
			}
		}
	}
	if handed != 1 {
		t.Fatalf("%d email.verifier_handed_over events, want 1", handed)
	}
}

// With every Treg account full, the workspace's other verifier is the one
// asked instead: a connection to another service, never the second Treg
// account standing in as a second opinion.
func TestVerifyEmailsEveryAccountFullAsksTheOtherVerifier(t *testing.T) {
	other := &fakeVerifier{status: connectors.Undeliverable}
	e, treg, _, _ := twoTregEnv(t, other)
	e.connect("fakeverifier", "test_not_a_real_key_v")
	treg.full[tregTokenA], treg.full[tregTokenB] = true, true

	const email = "ana.lee@acme.example"
	res, err := e.s.VerifyEmails(context.Background(), e.ws, verifyOne(email))
	if err != nil || len(res.Items) != 1 || res.Items[0].Code != CodeUndeliverable {
		t.Fatalf("every account full, the other verifier rules it out: %+v %v", res, err)
	}
	// Filtrat was asked on both accounts before the other verifier.
	if treg.asked(routeFiltratVerify, tregTokenA) == 0 || treg.asked(routeFiltratVerify, tregTokenB) == 0 {
		t.Fatalf("calls: %v", treg.calls)
	}

	// No other verifier's verdict to go on: busy, never a verdict.
	other.status = connectors.Deliverable
	res, err = e.s.VerifyEmails(context.Background(), e.ws, verifyOne("ben.ito@acme.example"))
	if err != nil || len(res.Items) != 0 {
		t.Fatalf("every account full and no verdict: %+v %v", res, err)
	}
}
