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
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/data"
	"github.com/outflareai/outflare/engine/internal/track"
)

// tregShareBusy is Treg's answer when a provider's shared capacity is in
// use: HTTP 429, nothing charged, and when to ask again.
const tregShareBusy = `{"detail":{"error":"share_busy","provider":"filtrat-finder","active":0,"retry_after_s":0.01,` +
	`"message":"filtrat-finder is fully used and your team already has 0 calls in progress on it; not charged, try again in 2 s"}}`

// fakeTreg is a Treg whose routes answer with what the test sets: "busy"
// for share_busy, else a 200 with that body. It counts the calls per route.
type fakeTreg struct {
	mu     sync.Mutex
	routes map[string]string
	calls  map[string]int
}

func (f *fakeTreg) set(route, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[route] = body
}

func (f *fakeTreg) asked(route string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[route]
}

// tregEnv is a workspace whose own Treg connection is its email finder
// and its chosen verifier, with Treg served by a fake.
func tregEnv(t *testing.T) (*finderEnv, *fakeTreg, string) {
	t.Helper()
	f := &fakeTreg{routes: map[string]string{}, calls: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := strings.TrimPrefix(r.URL.Path, "/call/")
		f.mu.Lock()
		f.calls[route]++
		body, ok := f.routes[route]
		f.mu.Unlock()
		switch {
		case !ok:
			t.Errorf("unexpected call to %s", route)
			w.WriteHeader(http.StatusInternalServerError)
		case body == "busy":
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, tregShareBusy)
		case body == "limited":
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"detail":{"error":"rate_limited","retry_after_s":0.01}}`)
		default:
			w.Header().Set("X-Treg-Cost-Micro", "3000")
			io.WriteString(w, body)
		}
	}))
	t.Cleanup(srv.Close)
	e := newFinderEnv(t, data.Treg{})
	e.store.Client = &connectors.Client{BaseURLs: map[string]string{"treg": srv.URL}}
	id := e.connect("treg", "trg_test_not_a_real_token")
	if _, err := e.pool.Exec(context.Background(),
		`INSERT INTO verification_settings (workspace_id, integration_id) VALUES ($1, $2)`, e.ws, id); err != nil {
		t.Fatal(err)
	}
	return e, f, id
}

const (
	routeFiltratFind   = "filtrat-finder.people.email.find"
	routeKittFind      = "trykitt.people.email.find"
	routeAnyFind       = "anymailfinder.people.email.find"
	routeFiltratVerify = "filtrat.people.email.verify.resolve_catchall"
	routeBounceBan     = "bounceban.people.email.verify"
)

// A provider whose shared capacity is in use (Treg: HTTP 429 share_busy,
// nothing charged) is not the account's quota: the step is pending and
// busy, so a caller goes on to its next step and asks this one again. It
// never stops the caller, is never remembered as a miss and never marks
// the connection as broken.
func TestAskFinderStepBusyProviderIsNotARefusal(t *testing.T) {
	e, treg, id := tregEnv(t)
	treg.set(routeFiltratFind, "busy")
	treg.set(routeKittFind, `{"email":"ana.lee@acme.example","validity":"valid-risky"}`)
	treg.set(routeAnyFind, `{"email":"ana.lee@acme.example","email_status":"valid"}`)
	ana := FindItem{Ref: "r1", FirstName: "Ana", LastName: "Lee", Domain: "acme.example"}
	events := track.Capture(t)

	got := e.step("filtrat", ana)
	if got.Code != CodePending || !got.Busy || got.StopCode != "" || got.Asked || got.RetryAfter < time.Second || got.Cost.Units != 0 {
		t.Fatalf("a busy step: %+v", got)
	}
	// Asked again in place first, then given up for now.
	if n := treg.asked(routeFiltratFind); n != 4 {
		t.Fatalf("the busy route was called %d times, want 4", n)
	}
	var busy int
	for _, ev := range events() {
		if ev.Name == "email.finder_failed" {
			busy++
			if ev.Props["kind"] != "busy" || ev.Props["throttled"] != false || ev.Props["step"] != "filtrat" || ev.Props["integration_id"] != id ||
				ev.ErrorCode != "connector_unavailable" || ev.Outcome != track.Failure {
				t.Fatalf("event = %+v", ev)
			}
		}
	}
	if busy != 1 {
		t.Fatalf("%d email.finder_failed events, want 1", busy)
	}
	var status string
	var remembered int
	if err := e.pool.QueryRow(context.Background(), `SELECT status FROM integrations WHERE id = $1`, id).Scan(&status); err != nil || status != connectors.StatusConnected {
		t.Fatalf("connection status = %q, %v", status, err)
	}
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM enrichment_cache WHERE workspace_id = $1`, e.ws).Scan(&remembered); err != nil || remembered != 0 {
		t.Fatalf("%d answers remembered, %v", remembered, err)
	}
	var slow bool
	if err := e.pool.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM pipeline_limits WHERE key LIKE '%' || $1 || '%' AND slow_until > now())`, id).Scan(&slow); err != nil || slow {
		t.Fatalf("a provider everyone fills must not slow the connection: %v, %v", slow, err)
	}

	// The next steps answer at once: a provider everyone else fills does
	// not slow the connection, whose pace every step shares.
	for _, step := range []string{"kitt", "anymailfinder"} {
		res := e.step(step, ana)
		if res.Code != "" || !res.Asked || !reflect.DeepEqual(res.Addresses, []string{"ana.lee@acme.example"}) || res.Verified != (step == "anymailfinder") {
			t.Fatalf("%s while another step is busy: %+v", step, res)
		}
	}

	// Once the provider has room, the busy step answers.
	treg.set(routeFiltratFind, `{"status":"found","found_email":"ana.lee@acme.example"}`)
	if got = e.step("filtrat", ana); got.Code != "" || !got.Asked || !reflect.DeepEqual(got.Addresses, []string{"ana.lee@acme.example"}) {
		t.Fatalf("the step once it has room: %+v", got)
	}

	// A rate limit is busy too, and never a refusal, but it is about this
	// account's own calls: the connection is asked more slowly for a while.
	treg.set(routeFiltratFind, "limited")
	ben := FindItem{Ref: "r2", FirstName: "Ben", LastName: "Ito", Domain: "acme.example"}
	if got = e.step("filtrat", ben); got.Code != CodePending || !got.Busy || got.StopCode != "" {
		t.Fatalf("a rate limited step: %+v", got)
	}
	var slowed bool
	if err := e.pool.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM pipeline_limits WHERE key LIKE '%' || $1 || '%' AND slow_until > now())`, id).Scan(&slowed); err != nil || !slowed {
		t.Fatalf("a rate limit must slow the connection: %v, %v", slowed, err)
	}
}

// A busy checker is never a verdict. The built-in checks do not stand in
// for it (they would make a good address "risky"), nothing is stored, and
// the address has no answer yet, so the caller asks again.
func TestVerifyEmailsBusyVerifierIsNotAVerdict(t *testing.T) {
	e, treg, id := tregEnv(t)
	const email = "ana.lee@acme.example"
	var req VerifyRequest
	req.Items = append(req.Items, struct {
		Ref   string `json:"ref"`
		Email string `json:"email"`
	}{Ref: "r1", Email: email})
	stored := func() int {
		var n int
		if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM email_verifications WHERE workspace_id = $1 AND email = $2`, e.ws, email).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	treg.set(routeFiltratVerify, "busy")
	treg.set(routeBounceBan, "busy")
	res, err := e.s.VerifyEmails(context.Background(), e.ws, req)
	if err != nil || len(res.Items) != 0 || stored() != 0 {
		t.Fatalf("every checker busy: %+v %v, %d stored", res, err, stored())
	}
	var status string
	if err := e.pool.QueryRow(context.Background(), `SELECT status FROM integrations WHERE id = $1`, id).Scan(&status); err != nil || status != connectors.StatusConnected {
		t.Fatalf("connection status = %q, %v", status, err)
	}

	// Filtrat busy alone: BounceBan decides, on the workspace's verifier.
	treg.set(routeBounceBan, `{"id":"bb_1","status":"success","result":"deliverable","score":88}`)
	res, err = e.s.VerifyEmails(context.Background(), e.ws, req)
	if err != nil || len(res.Items) != 1 || res.Items[0].Code != CodeVerified || res.Items[0].Cost.Provider != "treg" || res.Items[0].Cost.Units != 1 {
		t.Fatalf("Filtrat busy, BounceBan passes: %+v %v", res, err)
	}
}
