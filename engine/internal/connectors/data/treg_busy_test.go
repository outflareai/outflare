package data

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// tregShareBusy is Treg's answer when a provider's shared capacity is in
// use: HTTP 429, nothing charged, and when to ask again.
const tregShareBusy = `{"detail":{"error":"share_busy","provider":"filtrat-finder","active":0,"retry_after_s":2,` +
	`"message":"filtrat-finder is fully used and your team already has 0 calls in progress on it; not charged, try again in 2 s"}}`

// tregBusyFor answers share_busy the first n times it is asked, then with
// then. n < 0 is always busy. calls counts every request.
func tregBusyFor(n int, calls *atomic.Int32, then tregHandler) tregHandler {
	return func(w http.ResponseWriter, body map[string]any, r *http.Request) {
		if k := int(calls.Add(1)); n < 0 || k <= n {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, tregShareBusy)
			return
		}
		then(w, body, r)
	}
}

func tregQuickBusy(t *testing.T) {
	old := tregBackoff
	tregBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { tregBackoff = old })
}

func isBusy(err error) bool {
	_, ok := c.IsBusy(err)
	return ok
}

// A provider whose shared capacity is in use is asked again in place after
// a backoff: a moment later it answers.
func TestTregShareBusyIsAskedAgainInPlace(t *testing.T) {
	tregQuickBusy(t)
	tregShortWait(t)
	ctx := context.Background()
	q := c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "acme-io.example"}
	found := tregReply(200, "3000", `{"status":"found","found_email":"ana.lee@acme-io.example"}`)

	var calls atomic.Int32
	f, cl := newFake(t)
	f.routes[tregFiltratPath] = tregBusyFor(2, &calls, found)
	got, err := Treg{}.FindStep(ctx, cl, tregKey, "filtrat", q)
	if err != nil || !reflect.DeepEqual(got.Addresses, []string{"ana.lee@acme-io.example"}) || got.Credits != 1 || calls.Load() != 3 {
		t.Fatalf("busy twice, then found: %+v %v after %d calls", got, err, calls.Load())
	}

	// Still busy after the retries: an error that says busy and when to
	// ask again. Never a refusal of the account, and never a miss.
	calls.Store(0)
	f.routes[tregFiltratPath] = tregBusyFor(-1, &calls, found)
	got, err = Treg{}.FindStep(ctx, cl, tregKey, "filtrat", q)
	wait, busy := c.IsBusy(err)
	if !busy || c.IsThrottled(err) || wait != 2*time.Second || c.KindOf(err) == c.ErrQuota || c.KindOf(err) == c.ErrAuth || len(got.Addresses) != 0 {
		t.Fatalf("always busy: %+v %v (busy %v, wait %s, kind %s)", got, err, busy, wait, c.KindOf(err))
	}
	if n := int(calls.Load()); n != 1+len(tregBackoff) {
		t.Fatalf("always busy: %d calls, want %d", n, 1+len(tregBackoff))
	}
}

// The chain goes on past a busy route. When another route has the address
// that is the answer; when none has, the person is not a miss: the busy
// route may have had it.
func TestTregChainWithABusyRoute(t *testing.T) {
	tregQuickBusy(t)
	tregShortWait(t)
	ctx := context.Background()
	q := c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "acme-io.example"}
	var calls atomic.Int32
	always := tregBusyFor(-1, &calls, nil)

	_, cl, order := tregChainFake(t, map[string]tregHandler{
		tregFiltratPath: always,
		tregKittPath:    tregMissKitt,
		tregAnyPath:     tregReply(200, "20000", `{"email":"ana.lee@acme-io.example","email_status":"valid"}`),
	})
	got, err := Treg{}.EnrichPerson(ctx, cl, tregKey, q)
	if err != nil || got.Person.Email != "ana.lee@acme-io.example" || got.Person.EmailStatus != c.EmailVerified {
		t.Fatalf("busy, miss, found: %+v %v (%v)", got, err, order())
	}

	_, cl, _ = tregChainFake(t, map[string]tregHandler{tregFiltratPath: always, tregKittPath: tregMissKitt, tregAnyPath: tregMissAny})
	got, err = Treg{}.EnrichPerson(ctx, cl, tregKey, q)
	if !isBusy(err) || got.Found {
		t.Fatalf("busy and misses must not be a miss: %+v %v", got, err)
	}
}

// A busy checker is never a verdict about the address and never an empty
// balance: Filtrat busy hands the address to BounceBan, and BounceBan busy
// is an error that says busy, so the address is checked again later.
func TestTregVerifyWithABusyChecker(t *testing.T) {
	tregQuickBusy(t)
	ctx := context.Background()
	const email = "ana@acme-io.example"
	var calls atomic.Int32
	always := tregBusyFor(-1, &calls, nil)

	f, cl := newFake(t)
	f.routes[tregVerifyPath] = always
	f.routes[tregBBPath] = tregReply(200, "4000", `{"id":"bb_1","status":"success","result":"deliverable","score":88}`)
	got, err := Treg{}.Verify(ctx, cl, tregKey, email)
	if err != nil || got.Status != c.Deliverable || got.ProviderStatus != "filtrat:no answer/bounceban:deliverable" {
		t.Fatalf("Filtrat busy: %+v %v", got, err)
	}

	// The workspace's other verifier is asked when Filtrat is busy.
	asked := 0
	got, err = Treg{}.VerifyWith(ctx, cl, tregKey, email, func(context.Context, string) (c.Verification, error) {
		asked++
		return c.Verification{Status: c.Deliverable, NotCatchAll: true, Reason: "deliverable"}, nil
	})
	if err != nil || asked != 1 || got.Status != c.Deliverable {
		t.Fatalf("Filtrat busy with a second verifier: %+v %v, asked %d", got, err, asked)
	}

	f.routes[tregVerifyPath] = tregReply(200, "1250", `{"status":"catch_all","domain_intelligence":{"catch_all":true}}`)
	f.routes[tregBBPath] = always
	got, err = Treg{}.Verify(ctx, cl, tregKey, email)
	if !isBusy(err) || c.KindOf(err) == c.ErrQuota || got.Status != "" {
		t.Fatalf("BounceBan busy: %+v %v", got, err)
	}
}

// Any other 429 is this account asked for fewer calls: busy as well (never
// the account's quota), and throttled, so a caller slows its pace. One that
// names a long pause is not asked again in place.
func TestTregRateLimitIsThrottled(t *testing.T) {
	tregQuickBusy(t)
	tregShortWait(t)
	_, cl, order := tregChainFake(t, map[string]tregHandler{tregKittPath: tregReply(429, "", `{"detail":{"error":"rate_limited","retry_after_s":30}}`)})
	_, err := Treg{}.FindStep(context.Background(), cl, tregKey, "kitt", c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "acme-io.example"})
	wait, busy := c.IsBusy(err)
	if !busy || !c.IsThrottled(err) || wait != 30*time.Second || c.KindOf(err) != c.ErrUnavailable || len(order()) != 1 {
		t.Fatalf("a rate limit: %v (busy %v, wait %s) after %v", err, busy, wait, order())
	}
}

// tregNoAdmission is Treg's answer when it could not admit a call in its
// own time: HTTP 429 after about 20 seconds, nothing charged, no pause
// named and no provider.
const tregNoAdmission = `{"detail":{"error":"money_admission_timeout","message":"could not admit this call in time; not charged"}}`

// tregFull answers money_admission_timeout the first n times, then with
// then. n < 0 is always full.
func tregFull(n int, calls *atomic.Int32, then tregHandler) tregHandler {
	return func(w http.ResponseWriter, body map[string]any, r *http.Request) {
		if k := int(calls.Add(1)); n < 0 || k <= n {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, tregNoAdmission)
			return
		}
		then(w, body, r)
	}
}

// Treg not admitting a call is saturation, like share_busy: asked again
// after each backoff (2, 4 and 8 seconds), then a busy error. It is Treg's
// capacity, never this account's pace or quota.
func TestTregAdmissionTimeoutIsSaturation(t *testing.T) {
	if want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}; !reflect.DeepEqual(tregBackoff, want) {
		t.Fatalf("backoff = %v, want %v", tregBackoff, want)
	}
	tregQuickBusy(t)
	tregShortWait(t)
	ctx := context.Background()
	q := c.PersonQuery{FirstName: "Ana", LastName: "Lee", Domain: "acme-io.example"}
	found := tregReply(200, "3000", `{"status":"found","found_email":"ana.lee@acme-io.example"}`)

	var calls atomic.Int32
	f, cl := newFake(t)
	f.routes[tregFiltratPath] = tregFull(3, &calls, found)
	got, err := Treg{}.FindStep(ctx, cl, tregKey, "filtrat", q)
	if err != nil || !reflect.DeepEqual(got.Addresses, []string{"ana.lee@acme-io.example"}) || calls.Load() != 4 {
		t.Fatalf("full three times, then found: %+v %v after %d calls", got, err, calls.Load())
	}

	calls.Store(0)
	f.routes[tregFiltratPath] = tregFull(-1, &calls, found)
	got, err = Treg{}.FindStep(ctx, cl, tregKey, "filtrat", q)
	if !isBusy(err) || c.IsThrottled(err) || c.KindOf(err) == c.ErrQuota || len(got.Addresses) != 0 || calls.Load() != 4 {
		t.Fatalf("always full: %+v %v after %d calls", got, err, calls.Load())
	}

	// A call that began too long ago is not asked again: the caller hands
	// it to another account instead.
	oldWithin := tregRetryWithin
	tregRetryWithin = 0
	t.Cleanup(func() { tregRetryWithin = oldWithin })
	calls.Store(0)
	if _, err = (Treg{}).FindStep(ctx, cl, tregKey, "filtrat", q); !isBusy(err) || calls.Load() != 1 {
		t.Fatalf("out of time to ask again: %v after %d calls", err, calls.Load())
	}
}

// tregCalls is the accounts a route was called by, in order. The fake
// server writes it from its own goroutines while the test reads it, and a
// call the client stopped waiting for can still be writing.
type tregCalls struct {
	mu     sync.Mutex
	tokens []string
}

func (k *tregCalls) add(token string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.tokens = append(k.tokens, token)
}

func (k *tregCalls) list() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.tokens...)
}

func (k *tregCalls) reset() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.tokens = nil
}

// tregByToken answers a route by the account that called it.
func tregByToken(by map[string]tregHandler, calls *tregCalls) tregHandler {
	return func(w http.ResponseWriter, body map[string]any, r *http.Request) {
		token := r.Header.Get("X-Treg-Token")
		calls.add(token)
		by[token](w, body, r)
	}
}

// Two accounts of one workspace take turns for a check. One that cannot
// admit the call hands the same call to the other at once; a lookup still
// running, or one that got no answer in time, is never handed over, since
// the first account may still finish and bill it.
func TestTregVerifyAcrossAccounts(t *testing.T) {
	tregQuickBusy(t)
	ctx := context.Background()
	const email = "ana@acme-io.example"
	full := tregReply(429, "", tregNoAdmission)
	a, b := c.Creds{"api_key": "trg_test_not_a_real_token_a"}, c.Creds{"api_key": "trg_test_not_a_real_token_b"}
	accounts := []c.Creds{a, b}

	f, cl := newFake(t)
	var filtrat, bounceban, status tregCalls
	f.routes[tregVerifyPath] = tregByToken(map[string]tregHandler{a["api_key"]: full,
		b["api_key"]: tregReply(200, "1250", `{"status":"verified","confidence":97}`)}, &filtrat)
	// Whichever account's turn it is, the healthy one decides.
	for i := 0; i < 2; i++ {
		got, at, err := Treg{}.VerifyAcross(ctx, cl, accounts, email, nil)
		if err != nil || got.Status != c.Deliverable || at != 1 {
			t.Fatalf("round %d, one account full: %+v on account %d, %v", i, got, at, err)
		}
	}
	// The full account was asked four times on its turn, and not at all
	// when the healthy one went first.
	n := 0
	for _, token := range filtrat.list() {
		if token == a["api_key"] {
			n++
		}
	}
	if n != 4 || len(filtrat.list()) != 6 {
		t.Fatalf("Filtrat calls: %v", filtrat.list())
	}

	// BounceBan's check is asked about on the account that took it.
	f.routes[tregVerifyPath] = tregReply(200, "1250", `{"status":"catch_all","domain_intelligence":{"catch_all":true}}`)
	f.routes[tregBBPath] = tregByToken(map[string]tregHandler{a["api_key"]: full,
		b["api_key"]: tregReply(200, "4000", `{"id":"bb_1","status":"verifying"}`)}, &bounceban)
	f.routes[tregBBStatus] = tregByToken(map[string]tregHandler{a["api_key"]: full,
		b["api_key"]: tregReply(200, "0", `{"id":"bb_1","status":"success","result":"deliverable","score":90}`)}, &status)
	oldPoll := tregVerifyPoll
	tregVerifyPoll = time.Millisecond
	t.Cleanup(func() { tregVerifyPoll = oldPoll })
	for i := 0; i < 2; i++ {
		got, at, err := Treg{}.VerifyAcross(ctx, cl, accounts, email, nil)
		if err != nil || got.Status != c.Deliverable || at != 1 {
			t.Fatalf("round %d, BounceBan on the other account: %+v on account %d, %v", i, got, at, err)
		}
	}
	if !reflect.DeepEqual(status.list(), []string{b["api_key"], b["api_key"]}) {
		t.Fatalf("status asked on %v", status.list())
	}

	// Every account full: busy, never a verdict and never an empty balance.
	f.routes[tregVerifyPath], f.routes[tregBBPath] = full, full
	if got, _, err := (Treg{}).VerifyAcross(ctx, cl, accounts, email, nil); !isBusy(err) || c.KindOf(err) == c.ErrQuota || got.Status != "" {
		t.Fatalf("every account full: %+v %v", got, err)
	}

	// Still running at Treg (202): not handed over. BounceBan decides.
	filtrat.reset()
	pending := tregReply(202, "", `{"_treg":{"outcome":"pending"}}`)
	f.routes[tregVerifyPath] = tregByToken(map[string]tregHandler{a["api_key"]: pending, b["api_key"]: pending}, &filtrat)
	f.routes[tregBBPath] = tregReply(200, "4000", `{"id":"bb_1","status":"success","result":"deliverable","score":88}`)
	if got, _, err := (Treg{}).VerifyAcross(ctx, cl, accounts, email, nil); err != nil || got.ProviderStatus != "filtrat:no answer/bounceban:deliverable" || len(filtrat.list()) != 1 {
		t.Fatalf("Filtrat pending: %+v %v after %v", got, err, filtrat.list())
	}

	// No answer in time: not handed over either.
	tregShortWait(t)
	filtrat.reset()
	slow := tregSlow(`{"status":"verified"}`)
	f.routes[tregVerifyPath] = tregByToken(map[string]tregHandler{a["api_key"]: slow, b["api_key"]: slow}, &filtrat)
	if got, _, err := (Treg{}).VerifyAcross(ctx, cl, accounts, email, nil); err != nil || got.ProviderStatus != "filtrat:no answer/bounceban:deliverable" || len(filtrat.list()) != 1 {
		t.Fatalf("Filtrat slow: %+v %v after %v", got, err, filtrat.list())
	}

	// A refused or empty account hands over too; when every account
	// refuses, that is the error.
	f.routes[tregVerifyPath] = tregByToken(map[string]tregHandler{a["api_key"]: tregReply(402, "", `{"detail":"insufficient balance"}`),
		b["api_key"]: tregReply(200, "1250", `{"status":"verified"}`)}, &filtrat)
	for i := 0; i < 2; i++ {
		if got, at, err := (Treg{}).VerifyAcross(ctx, cl, accounts, email, nil); err != nil || got.Status != c.Deliverable || at != 1 {
			t.Fatalf("round %d, one account empty: %+v on account %d, %v", i, got, at, err)
		}
	}
	f.routes[tregVerifyPath] = tregReply(402, "", `{"detail":"insufficient balance"}`)
	if _, _, err := (Treg{}).VerifyAcross(ctx, cl, accounts, email, nil); c.KindOf(err) != c.ErrQuota {
		t.Fatalf("every account empty: %v", err)
	}
}

// A call waits past Treg's own 60 second hold, so a slow answer or its 202
// is seen, and one address has time for Filtrat on every account before
// BounceBan.
func TestTregWaits(t *testing.T) {
	if tregWait < 75*time.Second {
		t.Fatalf("a call waits %s, want at least 75s", tregWait)
	}
	if tregFiltratBudget < tregWait || tregVerifyBudget < tregFiltratBudget+tregWait {
		t.Fatalf("Filtrat has %s and a whole check %s, with calls of %s", tregFiltratBudget, tregVerifyBudget, tregWait)
	}
	if got := (Treg{}).VerifyWait(); got <= tregVerifyBudget {
		t.Fatalf("the verification service gives %s, less than the check's %s", got, tregVerifyBudget)
	}
	// A slow Filtrat answer is waited for, not cut off after 15 seconds.
	old, oldWait := tregFiltratBudget, tregWait
	tregFiltratBudget, tregWait = 2*time.Second, time.Second
	t.Cleanup(func() { tregFiltratBudget, tregWait = old, oldWait })
	f, cl := newFake(t)
	f.routes[tregVerifyPath] = tregSlow(`{"status":"verified"}`)
	if got, err := (Treg{}).Verify(context.Background(), cl, tregKey, "ana@acme-io.example"); err != nil || got.ProviderStatus != "filtrat:verified" {
		t.Fatalf("a slow Filtrat answer: %+v %v", got, err)
	}
}
