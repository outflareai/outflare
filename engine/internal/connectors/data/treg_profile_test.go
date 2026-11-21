package data

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const tregProfilePath = "/call/fetchinio.linkedin.user.profile"

// Treg reads a LinkedIn profile through Fetchin and returns the jobs the
// profile lists as current, with what the call cost.
func TestTregReadProfile(t *testing.T) {
	f, cl := newFake(t)
	ctx := context.Background()
	var conn c.Connector = Treg{}
	if _, ok := conn.(c.ProfileReader); !ok || !(Treg{}).Definition().Has(c.CapReadProfile) {
		t.Fatal("Treg does not read profiles")
	}
	f.routes[tregProfilePath] = func(w http.ResponseWriter, _ map[string]any, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("X-Treg-Token") != "trg_test_not_a_real_token" || r.URL.Query().Get("profileUrlOrUrn") != "ana-diaz-example" {
			t.Errorf("request: %s %s", r.Method, r.URL)
		}
		w.Header().Set("X-Treg-Cost-Micro", "4000")
		io.WriteString(w, `{"title":"CTO at Acme","geoLocationName":"Lisbon","geoCountryName":"Portugal","experiences":[
			{"current":true,"jobTitle":"CTO","company":{"name":"Acme","url":"https://www.linkedin.com/company/acme-example"}},
			{"ongoing":true,"name":"Side Project","jobTitle":"Advisor","company":{"url":"javascript:alert(1)"}},
			{"current":false,"jobTitle":"Engineer","company":{"name":"Old Co"}},
			{"current":"true","jobTitle":"Not a boolean","company":{"name":"Ignored"}}]}`)
	}
	p, err := Treg{}.ReadProfile(ctx, cl, tregKey, " https://www.linkedin.com/in/ana-diaz-example/?trk=x ")
	if err != nil || !p.Found || p.Headline != "CTO at Acme" || p.Location != "Lisbon" || p.Country != "Portugal" || p.Cost.USD != 0.004 {
		t.Fatalf("profile: %+v %v", p, err)
	}
	want := []c.ProfileRole{{Company: "Acme", CompanyURL: "https://www.linkedin.com/company/acme-example", Title: "CTO"}, {Company: "Side Project", Title: "Advisor"}}
	if len(p.Current) != 2 || p.Current[0] != want[0] || p.Current[1] != want[1] {
		t.Fatalf("current roles: %+v", p.Current)
	}

	// Not a person's profile: nothing is sent.
	f.routes[tregProfilePath] = func(http.ResponseWriter, map[string]any, *http.Request) {
		t.Error("asked about something that is not a profile")
	}
	if _, err := (Treg{}).ReadProfile(ctx, cl, tregKey, "https://www.linkedin.com/company/acme-example"); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("a company page: %v", err)
	}

	// A profile that doesn't exist is an answer: Treg's own code, or the
	// provider's 404 that says so.
	f.routes[tregProfilePath] = tregReply(404, "0", `{"error":"LinkedIn profile not found","code":"PROFILE_NOT_FOUND"}`)
	if p, err = (Treg{}).ReadProfile(ctx, cl, tregKey, "https://linkedin.com/in/nobody-example"); err != nil || p.Found || len(p.Current) != 0 {
		t.Fatalf("missing profile by code: %+v %v", p, err)
	}
	f.routes[tregProfilePath] = func(w http.ResponseWriter, _ map[string]any, _ *http.Request) {
		w.Header().Set("X-Treg-Provider", "fetchinio")
		w.WriteHeader(404)
		io.WriteString(w, `{"detail":{"message":"Profile not found"}}`)
	}
	if p, err = (Treg{}).ReadProfile(ctx, cl, tregKey, "https://linkedin.com/in/nobody-example"); err != nil || p.Found {
		t.Fatalf("missing profile by the provider's answer: %+v %v", p, err)
	}
	// Any other 404 is a failed request (a route that moved), never a
	// missing person.
	f.routes[tregProfilePath] = tregReply(404, "", `{"detail":"Not Found"}`)
	if _, err = (Treg{}).ReadProfile(ctx, cl, tregKey, "https://linkedin.com/in/ana-diaz-example"); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("a 404 that is not the provider's: %v", err)
	}
	f.routes[tregProfilePath] = tregReply(429, "", `{"detail":{"error":"rate_limited","retry_after_s":30}}`)
	if _, err = (Treg{}).ReadProfile(ctx, cl, tregKey, "https://linkedin.com/in/ana-diaz-example"); !isBusy(err) {
		t.Fatalf("rate limited: %v", err)
	}
	f.routes[tregProfilePath] = tregReply(401, "", `{"detail":"invalid token"}`)
	if _, err = (Treg{}).ReadProfile(ctx, cl, tregKey, "https://linkedin.com/in/ana-diaz-example"); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("refused token: %v", err)
	}
	// Still running at Treg: no answer about the person.
	f.routes[tregProfilePath] = tregReply(202, "", `{"_treg":{"outcome":"pending"}}`)
	if p, err = (Treg{}).ReadProfile(ctx, cl, tregKey, "https://linkedin.com/in/ana-diaz-example"); !c.IsTimeout(err) || p.Found {
		t.Fatalf("pending: %+v %v", p, err)
	}
	// No answer in time.
	old := tregProfileWait
	tregProfileWait = 50 * time.Millisecond
	t.Cleanup(func() { tregProfileWait = old })
	f.routes[tregProfilePath] = tregSlow(`{}`)
	if _, err = (Treg{}).ReadProfile(ctx, cl, tregKey, "https://linkedin.com/in/ana-diaz-example"); !c.IsTimeout(err) {
		t.Fatalf("slow: %v", err)
	}
}
