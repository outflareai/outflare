package emailfind

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/data"
	"github.com/outflareai/outflare/engine/internal/pipeline"
	"github.com/outflareai/outflare/engine/internal/track"
)

// stepFinder is a connected finder that can be asked one step at a time
// (connectors.StepFinder). It records each step it was asked and answers
// with step.
type stepFinder struct {
	*fakeFinder
	only  bool // reads a LinkedIn profile and nothing else
	steps []connectors.FindStep

	smu   sync.Mutex
	asks  []string
	reply func(key, step string, q connectors.PersonQuery) (connectors.StepAnswer, error)
}

func (f *stepFinder) ProfileOnly() bool                { return f.only }
func (f *stepFinder) FindSteps() []connectors.FindStep { return f.steps }
func (f *stepFinder) FindStep(_ context.Context, _ *connectors.Client, cr connectors.Creds, step string, q connectors.PersonQuery) (connectors.StepAnswer, error) {
	f.smu.Lock()
	f.asks = append(f.asks, step+" "+q.LinkedInURL)
	f.smu.Unlock()
	return f.reply(cr["api_key"], step, q)
}

func (f *stepFinder) asked() []string {
	f.smu.Lock()
	defer f.smu.Unlock()
	return append([]string(nil), f.asks...)
}

func offers(addresses ...string) func(string, string, connectors.PersonQuery) (connectors.StepAnswer, error) {
	return func(string, string, connectors.PersonQuery) (connectors.StepAnswer, error) {
		return connectors.StepAnswer{Addresses: addresses, Credits: 1}, nil
	}
}

func refuses(status int) func(string, string, connectors.PersonQuery) (connectors.StepAnswer, error) {
	return func(string, string, connectors.PersonQuery) (connectors.StepAnswer, error) {
		return connectors.StepAnswer{}, connectors.HTTPError("Fake finder", status, "no")
	}
}

const stepProfile = "https://www.linkedin.com/in/ana-lee-example"

var stepPerson = FindItem{Ref: "r1", FirstName: "Ana", LastName: "Lee", Domain: "acme.example", LinkedInURL: stepProfile + "/"}

// stepEnv is a workspace with two finders that share the "blitz" step: one
// that reads only a profile (asked first), and a gateway with more steps.
func stepEnv(t *testing.T) (e *finderEnv, direct, gateway *stepFinder, directID, gatewayID string) {
	t.Helper()
	direct = &stepFinder{fakeFinder: &fakeFinder{id: "directfinder", answer: miss}, only: true,
		steps: []connectors.FindStep{{ID: "blitz", NeedsProfile: true}}, reply: offers()}
	gateway = &stepFinder{fakeFinder: &fakeFinder{id: "gatewayfinder", answer: miss},
		steps: []connectors.FindStep{{ID: "blitz", NeedsProfile: true}, {ID: "filtrat"}}, reply: offers()}
	e = newFinderEnv(t, direct, gateway)
	// The gateway was connected first: the order is still the direct one's.
	gatewayID = e.connect("gatewayfinder", "test_not_a_real_key_g")
	directID = e.connect("directfinder", "test_not_a_real_key_d")
	return e, direct, gateway, directID, gatewayID
}

func (e *finderEnv) step(step string, it FindItem) StepResult {
	e.t.Helper()
	res, err := e.s.AskFinderStep(context.Background(), e.ws, step, it)
	if err != nil {
		e.t.Fatal(err)
	}
	if res.Step != step || res.Addresses == nil {
		e.t.Fatalf("result = %+v", res)
	}
	return res
}

// The steps are those of the workspace's own connected finders, in the
// order to ask them: the connector that reads only a profile first.
func TestFinderStepsOrder(t *testing.T) {
	e := newFinderEnv(t, data.Blitz{}, data.Treg{}, &fakeFinder{id: "fakefinder", answer: miss})
	if got, err := e.s.FinderSteps(context.Background(), e.ws); err != nil || len(got) != 0 {
		t.Fatalf("nothing connected: %+v %v", got, err)
	}
	// A finder that can't be asked by step offers none.
	e.connect("fakefinder", "test_not_a_real_key_f")
	e.connect("treg", "trg_test_not_a_real_token")
	got, err := e.s.FinderSteps(context.Background(), e.ws)
	want := []FinderStep{
		{ID: "blitz", NeedsProfile: true, Connectors: []string{"treg"}},
		{ID: "filtrat", Connectors: []string{"treg"}},
		{ID: "kitt", Connectors: []string{"treg"}},
		{ID: "anymailfinder", Connectors: []string{"treg"}},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Treg alone: %+v %v", got, err)
	}
	// Blitz, connected after Treg, is still asked before it.
	blitz := e.connect("blitz", "test_not_a_real_key_b")
	e.connect("treg", "trg_test_not_a_real_token_2")
	got, err = e.s.FinderSteps(context.Background(), e.ws)
	want[0].Connectors = []string{"blitz", "treg"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Blitz and Treg: %+v %v", got, err)
	}
	// A connection that is not connected does not count.
	if _, err := e.pool.Exec(context.Background(), `UPDATE integrations SET status = 'error' WHERE id = $1`, blitz); err != nil {
		t.Fatal(err)
	}
	want[0].Connectors = []string{"treg"}
	if got, err = e.s.FinderSteps(context.Background(), e.ws); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Blitz broken: %+v %v", got, err)
	}
}

// The first connector that offers the step answers: every address it
// offered comes back, unfiltered, and the other connector is not asked.
func TestAskFinderStepFirstConnectorAnswers(t *testing.T) {
	e, direct, gateway, directID, gatewayID := stepEnv(t)
	direct.reply = func(string, string, connectors.PersonQuery) (connectors.StepAnswer, error) {
		return connectors.StepAnswer{Addresses: []string{"Ana@Globex.example", "ana.lee@acme.example", "not an address", "ana@globex.example"}, Credits: 1}, nil
	}
	events := track.Capture(t)
	got := e.step("blitz", stepPerson)
	want := StepResult{Step: "blitz", Provider: "directfinder", Connection: directID, Asked: true,
		Addresses: []string{"ana@globex.example", "ana.lee@acme.example"}, Cost: Cost{Provider: "directfinder", Connection: directID, Units: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if a := direct.asked(); len(a) != 1 || a[0] != "blitz "+stepProfile || len(gateway.asked()) != 0 {
		t.Fatalf("asked %v and %v", a, gateway.asked())
	}
	// Whole providers were not asked: a step is one lookup.
	if len(direct.calls()) != 0 || len(gateway.calls()) != 0 {
		t.Fatalf("EnrichPerson was called: %v %v", direct.calls(), gateway.calls())
	}
	if e.booked(directID) != 1 || e.booked(gatewayID) != 0 {
		t.Fatalf("booked %v and %v", e.booked(directID), e.booked(gatewayID))
	}
	var note string
	if err := e.pool.QueryRow(context.Background(), `SELECT note FROM spend_entries WHERE workspace_id = $1 AND integration_id = $2`, e.ws, directID).Scan(&note); err != nil || note != "Email finder lookup" {
		t.Fatalf("note %q %v", note, err)
	}
	var asked int
	for _, ev := range events() {
		if ev.Name == "email.finder_asked" {
			asked++
			if ev.Props["step"] != "blitz" || ev.Props["connector"] != "directfinder" || ev.Props["integration_id"] != directID ||
				ev.Props["found"] != true || ev.Props["by_profile"] != true || ev.WorkspaceID != e.ws {
				t.Fatalf("event = %+v", ev)
			}
		}
	}
	if asked != 1 {
		t.Fatalf("%d email.finder_asked events, want 1", asked)
	}

	// Asked again, the answer is remembered and nothing is paid twice.
	got = e.step("blitz", stepPerson)
	want.Connection, want.Cached, want.Cost = "", true, Cost{Provider: "directfinder"}
	if !reflect.DeepEqual(got, want) || len(direct.asked()) != 1 || len(gateway.asked()) != 0 || e.booked(directID) != 1 {
		t.Fatalf("again: %+v after %v, booked %v", got, direct.asked(), e.booked(directID))
	}
	// A step by name is asked, and remembered, apart from it.
	gateway.reply = func(_, step string, q connectors.PersonQuery) (connectors.StepAnswer, error) {
		if step != "filtrat" || q.LinkedInURL != "" || q.FirstName != "Ana" || q.LastName != "Lee" || q.Domain != "acme.example" {
			t.Errorf("filtrat asked with %q %+v", step, q)
		}
		return connectors.StepAnswer{Addresses: []string{"ana@acme.example"}, Verified: true, Credits: 1}, nil
	}
	got = e.step("filtrat", stepPerson)
	if got.Provider != "gatewayfinder" || got.Connection != gatewayID || !got.Verified || !got.Asked || got.Cached ||
		!reflect.DeepEqual(got.Addresses, []string{"ana@acme.example"}) || e.booked(gatewayID) != 1 {
		t.Fatalf("filtrat: %+v", got)
	}
	if got = e.step("filtrat", stepPerson); !got.Cached || !got.Verified || len(gateway.asked()) != 1 {
		t.Fatalf("filtrat again: %+v after %v", got, gateway.asked())
	}
}

// A miss is the step's answer: the next connector that offers the same
// step is not asked, now or later.
func TestAskFinderStepMissIsFinal(t *testing.T) {
	e, direct, gateway, directID, _ := stepEnv(t)
	direct.reply = func(string, string, connectors.PersonQuery) (connectors.StepAnswer, error) {
		return connectors.StepAnswer{}, nil
	}
	gateway.reply = offers("ana@acme.example")
	got := e.step("blitz", stepPerson)
	if !got.Asked || got.Cached || got.Code != "" || len(got.Addresses) != 0 || got.Provider != "directfinder" || got.Cost.Units != 0 {
		t.Fatalf("miss: %+v", got)
	}
	if got = e.step("blitz", stepPerson); !got.Cached || len(got.Addresses) != 0 {
		t.Fatalf("miss again: %+v", got)
	}
	if len(direct.asked()) != 1 || len(gateway.asked()) != 0 || e.booked(directID) != 0 {
		t.Fatalf("asked %v and %v", direct.asked(), gateway.asked())
	}
}

// Only a connector that could not answer hands the step to the next one.
func TestAskFinderStepFallsOverWhenTheFirstCannotAnswer(t *testing.T) {
	for name, status := range map[string]int{"a refused key": 401, "out of credits": 402, "unreachable": 503} {
		t.Run(name, func(t *testing.T) {
			e, direct, gateway, directID, gatewayID := stepEnv(t)
			direct.reply = refuses(status)
			gateway.reply = offers("ana@acme.example")
			events := track.Capture(t)
			got := e.step("blitz", stepPerson)
			if got.Code != "" || !got.Asked || got.Provider != "gatewayfinder" || got.Connection != gatewayID ||
				!reflect.DeepEqual(got.Addresses, []string{"ana@acme.example"}) || got.Cost.Units != 1 {
				t.Fatalf("got %+v", got)
			}
			if len(direct.asked()) != 1 || len(gateway.asked()) != 1 || e.booked(directID) != 0 || e.booked(gatewayID) != 1 {
				t.Fatalf("asked %v and %v", direct.asked(), gateway.asked())
			}
			var failed int
			for _, ev := range events() {
				if ev.Name == "email.finder_failed" {
					failed++
					if ev.Props["step"] != "blitz" || ev.Props["integration_id"] != directID || ev.ErrorCode == "" {
						t.Fatalf("event = %+v", ev)
					}
				}
			}
			if failed != 1 {
				t.Fatalf("%d email.finder_failed events, want 1", failed)
			}
		})
	}
	// A usage limit on the first connection does the same.
	e, direct, gateway, directID, _ := stepEnv(t)
	gateway.reply = offers("ana@acme.example")
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO connection_limits (workspace_id, integration_id, unit, daily_limit, hard_stop)
		VALUES ($1, $2, 'credit', 0.5, true)`, e.ws, directID); err != nil {
		t.Fatal(err)
	}
	if got := e.step("blitz", stepPerson); got.Provider != "gatewayfinder" || len(direct.asked()) != 0 || len(gateway.asked()) != 1 {
		t.Fatalf("usage limit: %+v after %v", got, direct.asked())
	}
}

// When every connector that offers the step refuses, the result says what
// to fix; a transient failure of every one asks again later.
func TestAskFinderStepWhenNoConnectorAnswers(t *testing.T) {
	e, direct, gateway, directID, _ := stepEnv(t)
	direct.reply, gateway.reply = refuses(401), refuses(402)
	got := e.step("blitz", stepPerson)
	if got.Code != CodeStop || got.StopCode != "connector_quota" || got.Asked || len(got.Addresses) != 0 || got.Cost.Units != 0 {
		t.Fatalf("every connector refused: %+v", got)
	}
	// The refused key shows on the connection, which is left out from now on.
	var status string
	if err := e.pool.QueryRow(context.Background(), `SELECT status FROM integrations WHERE id = $1`, directID).Scan(&status); err != nil || status != connectors.StatusError {
		t.Fatalf("status = %q, %v", status, err)
	}
	if steps, err := e.s.FinderSteps(context.Background(), e.ws); err != nil || len(steps) != 2 || !reflect.DeepEqual(steps[0].Connectors, []string{"gatewayfinder"}) {
		t.Fatalf("steps: %+v %v", steps, err)
	}
	// Nothing was remembered, so it is asked again once that is fixed.
	var remembered int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM enrichment_cache WHERE workspace_id = $1`, e.ws).Scan(&remembered); err != nil || remembered != 0 {
		t.Fatalf("%d answers remembered, %v", remembered, err)
	}

	e, direct, gateway, _, _ = stepEnv(t)
	direct.reply, gateway.reply = refuses(503), refuses(502)
	if got = e.step("blitz", stepPerson); got.Code != CodeRetry || got.Asked || len(direct.asked()) != 1 || len(gateway.asked()) != 1 {
		t.Fatalf("every connector unreachable: %+v", got)
	}
	// One refused and one unreachable: the unreachable one may answer later.
	direct.reply = refuses(401)
	if got = e.step("blitz", stepPerson); got.Code != CodeRetry {
		t.Fatalf("refused and unreachable: %+v", got)
	}

	// A step no connected finder offers.
	if got = e.step("kitt", stepPerson); got.Code != CodeStop || got.StopCode != "connector_needed" || got.Asked {
		t.Fatalf("an unknown step: %+v", got)
	}
}

// A lookup the provider is still working on may still be billed: it is
// the provider's own answer, a miss, and is never asked again. A lookup
// that got no answer in time is no answer at all: it is a miss for this
// ask (the next step is tried) but is never remembered, so asking again
// later calls the provider again instead of reading "no email" for 90 days.
func TestAskFinderStepPendingIsRememberedAndTimeoutIsNot(t *testing.T) {
	e, direct, gateway, directID, _ := stepEnv(t)
	direct.reply = func(string, string, connectors.PersonQuery) (connectors.StepAnswer, error) {
		return connectors.StepAnswer{Pending: true, Credits: 1, Addresses: []string{"ana@acme.example"}}, nil
	}
	got := e.step("blitz", stepPerson)
	if got.Code != "" || !got.Asked || len(got.Addresses) != 0 || got.Cost.Units != 1 || e.booked(directID) != 1 {
		t.Fatalf("pending: %+v, booked %v", got, e.booked(directID))
	}
	if got = e.step("blitz", stepPerson); !got.Cached || len(got.Addresses) != 0 || len(direct.asked()) != 1 || len(gateway.asked()) != 0 || e.booked(directID) != 1 {
		t.Fatalf("pending again: %+v after %v", got, direct.asked())
	}

	e, direct, gateway, _, _ = stepEnv(t)
	direct.reply = func(string, string, connectors.PersonQuery) (connectors.StepAnswer, error) {
		return connectors.StepAnswer{}, connectors.TimedOutf("Fake finder did not answer in time")
	}
	gateway.reply = offers("ana@acme.example")
	if got = e.step("blitz", stepPerson); got.Code != "" || !got.Asked || len(got.Addresses) != 0 || got.Provider != "directfinder" {
		t.Fatalf("timed out: %+v", got)
	}
	if got = e.step("blitz", stepPerson); got.Cached || len(direct.asked()) != 2 || len(gateway.asked()) != 0 {
		t.Fatalf("timed out again: a timeout was remembered as a miss: %+v after %v and %v", got, direct.asked(), gateway.asked())
	}
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM enrichment_cache WHERE workspace_id = $1`, e.ws).Scan(&n); err != nil || n != 0 {
		t.Fatalf("enrichment_cache rows after timeouts = %d, %v", n, err)
	}
}

// A step that does not apply to the person calls nobody, and a test
// workspace never spends a provider's credits.
func TestAskFinderStepWithoutACall(t *testing.T) {
	e, direct, gateway, _, _ := stepEnv(t)
	direct.reply, gateway.reply = offers("ana@acme.example"), offers("ana@acme.example")
	for name, tc := range map[string]struct {
		step string
		it   FindItem
	}{
		"a profile step without a profile":     {"blitz", FindItem{FirstName: "Ana", LastName: "Lee", Domain: "acme.example"}},
		"a profile step with a company page":   {"blitz", FindItem{FirstName: "Ana", LastName: "Lee", Domain: "acme.example", LinkedInURL: "https://www.linkedin.com/company/acme-example"}},
		"a name step without a last name":      {"filtrat", FindItem{FirstName: "Ana", Domain: "acme.example", LinkedInURL: stepProfile}},
		"a name step without a company domain": {"filtrat", FindItem{FirstName: "Ana", LastName: "Lee", LinkedInURL: stepProfile}},
	} {
		if got := e.step(tc.step, tc.it); got.Asked || got.Code != "" || len(got.Addresses) != 0 || got.Provider != "" {
			t.Errorf("%s: %+v", name, got)
		}
	}
	if _, err := e.pool.Exec(context.Background(), `UPDATE workspaces SET test = true WHERE id = $1`, e.ws); err != nil {
		t.Fatal(err)
	}
	if got := e.step("blitz", stepPerson); got.Asked || got.Code != "" || len(got.Addresses) != 0 {
		t.Fatalf("a test workspace: %+v", got)
	}
	if len(direct.asked()) != 0 || len(gateway.asked()) != 0 {
		t.Fatalf("asked %v and %v", direct.asked(), gateway.asked())
	}
}

// Our own pacing: a long wait for the first connector hands the step to
// the next; with no other connector the caller is told when to ask again.
func TestAskFinderStepPacing(t *testing.T) {
	old := finderRate
	finderRate = pipeline.Rate{PerMinute: 1, Burst: 1}
	t.Cleanup(func() { finderRate = old })
	e, direct, gateway, _, gatewayID := stepEnv(t)
	direct.reply, gateway.reply = offers("ana@acme.example"), offers("ben@acme.example")
	if got := e.step("blitz", stepPerson); got.Provider != "directfinder" {
		t.Fatalf("first: %+v", got)
	}
	ben := FindItem{FirstName: "Ben", LastName: "Ito", Domain: "acme.example", LinkedInURL: "https://www.linkedin.com/in/ben-ito-example"}
	if got := e.step("blitz", ben); got.Provider != "gatewayfinder" || got.Connection != gatewayID || got.Code != "" {
		t.Fatalf("the first connector must wait: %+v", got)
	}
	cy := FindItem{FirstName: "Cy", LastName: "Roe", Domain: "acme.example", LinkedInURL: "https://www.linkedin.com/in/cy-roe-example"}
	got := e.step("blitz", cy)
	if got.Code != CodePending || got.RetryAfter <= stepPaceWait || got.RetryAfter > 2*time.Minute || got.Asked {
		t.Fatalf("both must wait: %+v", got)
	}
	if len(direct.asked()) != 1 || len(gateway.asked()) != 1 {
		t.Fatalf("asked %v and %v", direct.asked(), gateway.asked())
	}
}

// A step whose provider said what the call cost carries that cost on the
// result and books it as money in the spend ledger, beside the credit.
func TestAskFinderStepBooksTheReportedCost(t *testing.T) {
	e, direct, _, directID, _ := stepEnv(t)
	direct.reply = func(string, string, connectors.PersonQuery) (connectors.StepAnswer, error) {
		return connectors.StepAnswer{Addresses: []string{"ana.lee@acme.example"}, Credits: 1, CostMicros: 4500}, nil
	}
	got := e.step("blitz", stepPerson)
	if got.Cost.Units != 1 || got.Cost.Micros != 4500 {
		t.Fatalf("cost = %+v, want one credit costing 4500 micros", got.Cost)
	}
	var units float64
	var cents, micros *int64
	if err := e.pool.QueryRow(context.Background(), `SELECT units::float8, amount_cents, amount_micros FROM spend_entries WHERE workspace_id = $1 AND integration_id = $2`,
		e.ws, directID).Scan(&units, &cents, &micros); err != nil {
		t.Fatal(err)
	}
	if units != 1 || micros == nil || *micros != 4500 || cents == nil || *cents != 1 {
		t.Fatalf("booked %v units, %v cents, %v micros", units, cents, micros)
	}
}
