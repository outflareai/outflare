package reviewrequests

import (
	"github.com/outflareai/outflare/engine/internal/emails"
	"strings"
	"testing"
)

func TestInvitationContentEscapesCustomerData(t *testing.T) {
	c := Config{Subject: "Your experience", Body: "Hi {{person.name|there}}, <script>alert(1)</script>"}
	h, text := Content(c, []Destination{{Platform: "trustpilot", URL: "https://www.trustpilot.com/review/northpeak.example?a=1&b=2"}})
	out, err := emails.Render(c.Subject, h, text, nil, map[string]any{"person": map[string]any{"name": "<img src=x onerror=alert(1)>"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.HTML, "<script>") || strings.Contains(out.HTML, "<img") || !strings.Contains(out.HTML, "a=1&amp;b=2") {
		t.Fatalf("unsafe HTML: %s", out.HTML)
	}
}
func TestPlatformInvitationRestrictions(t *testing.T) {
	c := Config{Name: "Feedback", Mode: "workflow", Subject: "Your experience", Body: "Please share an honest review.", Platforms: []string{"google_reviews"}}
	if err := c.Check(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"yelp", "zocdoc", "opentable", "booking", "expedia"} {
		c.Platforms = []string{id}
		if c.Check() == nil {
			t.Errorf("%s allowed external invitations", id)
		}
	}
	c.Platforms = []string{"google_reviews"}
	c.Body = "{{{person.name}}}"
	if c.Check() == nil {
		t.Fatal("unescaped customer HTML allowed")
	}
	s := Defaults()
	s.Destinations = []Destination{{Platform: "google_reviews", URL: "javascript:alert(1)"}}
	if s.Check() == nil {
		t.Fatal("non-HTTPS destination allowed")
	}
	s.Destinations = []Destination{{Platform: "google_reviews", URL: "https://example.com/{{person.name}}"}}
	if s.Check() == nil {
		t.Fatal("dynamic destination URL breaks attribution")
	}
}
