package cheapinboxes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/infra"
)

// fakeOrders is just enough of Cheap Inboxes' ordering API.
type fakeOrders struct {
	mu        sync.Mutex
	checkouts int
	idemKey   string
	cart      []map[string]any
	quoted    []map[string]any
	cancelled []string
	busy      bool // answer checkout with 429
}

func (f *fakeOrders) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/v1/discovery/domains/search":
		kw := body["keyword"].(string)
		tld := body["tlds"].([]any)[0].(string)
		avail := kw != "taken"
		io.WriteString(w, `{"exact":[{"domain":"`+kw+`.`+tld+`","available":`+map[bool]string{true: "true", false: "false"}[avail]+`,"status":"`+map[bool]string{true: "available", false: "registered"}[avail]+`","price":9.99,"currency":"USD"}],"suggestions":[]}`)
	case r.URL.Path == "/v1/orders/quote":
		for _, d := range body["cart"].(map[string]any)["domains"].([]any) {
			f.quoted = append(f.quoted, d.(map[string]any))
		}
		io.WriteString(w, `{"quote":{"totals":{"total_today_cents":2049,"mailbox_monthly_cents":750}}}`)
	case r.URL.Path == "/v1/orders/checkout":
		f.checkouts++
		if f.busy {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"code":"RATE_LIMIT_EXCEEDED","message":"slow down"}}`)
			return
		}
		f.idemKey = r.Header.Get("Idempotency-Key")
		for _, d := range body["domains"].([]any) {
			f.cart = append(f.cart, d.(map[string]any))
		}
		io.WriteString(w, `{"order_id":"ord_1","status":"provisioning"}`)
	case r.URL.Path == "/v1/orders/ord_1":
		io.WriteString(w, `{"order":{"id":"ord_1","status":"provisioning"},
			"domains":[{"id":"d1","domain":"acme-com.example","status":"active"},{"id":"d2","domain":"acme-io.example","status":"pending_ns_verification"}],
			"mailboxes":[{"id":"m1","full_email":"Ana@acme-com.example","status":"active"},{"id":"m2","full_email":"ben@acme-io.example","status":"error","provisioning_error":"workspace limit"},{"id":"m3","full_email":"cy@acme-io.example","status":"provisioning"}]}`)
	case r.URL.Path == "/v1/orders/ord_2":
		io.WriteString(w, `{"order":{"id":"ord_2","status":"completed"},"domains":[{"domain":"acme-com.example","status":"active"}],"mailboxes":[{"full_email":"ana@acme-com.example","status":"active"}]}`)
	case r.URL.Path == "/v1/mailboxes":
		io.WriteString(w, `{"mailboxes":[{"id":"m1","full_email":"ana@acme-com.example","status":"active","provider":"google"},
			{"id":"m9","full_email":"old@acme-com.example","status":"active","provider":"google","cancellation_status":"scheduled"}],
			"pagination":{"total":2,"limit":200,"offset":0,"has_more":false}}`)
	case strings.HasSuffix(r.URL.Path, "/cancel"):
		f.cancelled = append(f.cancelled, strings.Split(r.URL.Path, "/")[3])
		io.WriteString(w, `{"success":true}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func testSupplier(t *testing.T) (Supplier, *fakeOrders) {
	f := &fakeOrders{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cl := &c.Client{BaseURLs: map[string]string{ID: srv.URL}}
	return Supplier{Open: func(_ context.Context, ws, id string) (*c.Opened, error) {
		if id != "int1" {
			return nil, c.ErrNotFound
		}
		return &c.Opened{Integration: c.Integration{ID: id, WorkspaceID: ws, Connector: ID},
			Connector: Connector{}, Creds: c.Creds{"api_key": "ci_live_test"}, Client: cl}, nil
	}}, f
}

func TestSupplierQuote(t *testing.T) {
	s, f := testSupplier(t)
	acct := infra.Account{WorkspaceID: "ws", IntegrationID: "int1"}
	q, err := s.Quote(context.Background(), acct, infra.QuoteRequest{Domains: []string{"acme-com.example", "taken.com"}, Provider: "google", MailboxesPerDomain: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Domains) != 2 || !q.Domains[0].Available || q.Domains[0].PriceCents == nil || *q.Domains[0].PriceCents != 999 ||
		q.Domains[1].Available || q.Domains[1].Reason != "registered" {
		t.Fatalf("domains: %+v", q.Domains)
	}
	if q.OnceCents == nil || *q.OnceCents != 2049 || q.MonthlyCents == nil || *q.MonthlyCents != 750 || q.Currency != "USD" {
		t.Fatalf("totals: %+v", q)
	}
	// Only the free domain is priced, with its mailboxes.
	if len(f.quoted) != 1 || f.quoted[0]["domain"] != "acme-com.example" || f.quoted[0]["mailboxes"].(map[string]any)["count"] != 3.0 {
		t.Fatalf("quoted: %v", f.quoted)
	}
	if _, err := s.Quote(context.Background(), infra.Account{WorkspaceID: "ws", IntegrationID: "nope"}, infra.QuoteRequest{}); err == nil {
		t.Fatal("unknown account quoted")
	}
}

func TestSupplierOrder(t *testing.T) {
	s, f := testSupplier(t)
	acct := infra.Account{WorkspaceID: "ws", IntegrationID: "int1"}
	ref, err := s.Order(context.Background(), acct, infra.OrderRequest{Provider: "google", IdempotencyKey: "plan-1", Domains: []infra.OrderDomain{
		{Domain: "acme-com.example", Mailboxes: []infra.OrderMailbox{{Email: "Ana@acme-com.example", FirstName: "Ana", LastName: "Silva"}}},
		{Domain: "owned-io.example", Owned: true, Mailboxes: []infra.OrderMailbox{{Email: "ben@owned-io.example", FirstName: "Ben", LastName: "Ode"}}},
	}})
	if err != nil || ref != "ord_1" {
		t.Fatalf("%q %v", ref, err)
	}
	if f.idemKey != "plan-1" || f.checkouts != 1 || len(f.cart) != 2 {
		t.Fatalf("checkout: key %q, %d calls, cart %v", f.idemKey, f.checkouts, f.cart)
	}
	acc := f.cart[0]["mailboxes"].(map[string]any)["accounts"].([]any)[0].(map[string]any)
	if acc["email_prefix"] != "ana" || acc["first_name"] != "Ana" || f.cart[0]["isExisting"] != nil || f.cart[1]["isExisting"] != true {
		t.Fatalf("cart: %v", f.cart)
	}

	// A mailbox on another domain is refused before anything is bought.
	f.checkouts = 0
	_, err = s.Order(context.Background(), acct, infra.OrderRequest{Provider: "google", Domains: []infra.OrderDomain{
		{Domain: "acme-com.example", Mailboxes: []infra.OrderMailbox{{Email: "ana@other-com.example"}}}}})
	if c.KindOf(err) != c.ErrInvalid || f.checkouts != 0 {
		t.Fatalf("wrong domain: %v, %d checkouts", err, f.checkouts)
	}

	// Checkout is never retried here, even when rate limited.
	f.busy = true
	if _, err := s.Order(context.Background(), acct, infra.OrderRequest{Provider: "google", IdempotencyKey: "plan-2",
		Domains: []infra.OrderDomain{{Domain: "acme-com.example"}}}); err == nil || f.checkouts != 1 {
		t.Fatalf("rate limited checkout: %v after %d calls", err, f.checkouts)
	}
}

func TestSupplierOrderStatus(t *testing.T) {
	s, _ := testSupplier(t)
	acct := infra.Account{WorkspaceID: "ws", IntegrationID: "int1"}
	st, err := s.OrderStatus(context.Background(), acct, "ord_1")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"acme-com.example": infra.OrderDone, "acme-io.example": infra.OrderPending,
		"ana@acme-com.example": infra.OrderDone, "ben@acme-io.example": infra.OrderFailed, "cy@acme-io.example": infra.OrderPending}
	for _, it := range append(st.Domains, st.Mailboxes...) {
		if want[it.Name] != it.Status {
			t.Errorf("%s: %s, want %s (%s)", it.Name, it.Status, want[it.Name], it.Detail)
		}
		if it.Status != infra.OrderDone && it.Detail == "" {
			t.Errorf("%s has no detail", it.Name)
		}
	}
	if st.Status != infra.OrderPending || !strings.Contains(st.Mailboxes[1].Detail, "workspace limit") {
		t.Fatalf("%+v", st)
	}
	st, err = s.OrderStatus(context.Background(), acct, "ord_2")
	if err != nil || st.Status != infra.OrderDone {
		t.Fatalf("completed order: %+v %v", st, err)
	}
}

func TestSupplierCancel(t *testing.T) {
	s, f := testSupplier(t)
	acct := infra.Account{WorkspaceID: "ws", IntegrationID: "int1"}
	for _, email := range []string{"ANA@acme-com.example", "old@acme-com.example", "gone@acme-com.example"} {
		if err := s.Cancel(context.Background(), acct, email); err != nil {
			t.Fatalf("%s: %v", email, err)
		}
	}
	// Only the active one is cancelled; one already ending, or not there, is left alone.
	if len(f.cancelled) != 1 || f.cancelled[0] != "m1" {
		t.Fatalf("cancelled %v", f.cancelled)
	}
}
