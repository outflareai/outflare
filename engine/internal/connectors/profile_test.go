package connectors

import "testing"

func TestLinkedInProfile(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.linkedin.com/in/ana-lee-example":            "https://www.linkedin.com/in/ana-lee-example",
		" http://WWW.LinkedIn.com/in/Ana-Lee-Example/?trk=x#top": "https://www.linkedin.com/in/Ana-Lee-Example",
		"linkedin.com/in/ana-lee-example/":                       "https://linkedin.com/in/ana-lee-example",
		"https://uk.linkedin.com/in/ana-lee-example":             "https://uk.linkedin.com/in/ana-lee-example",
		"https://www.linkedin.com/company/acme-example":          "",
		"https://www.linkedin.com/in/":                           "",
		"https://www.linkedin.com/in/ana/details/experience":     "",
		"https://notlinkedin.com/in/ana-lee-example":             "",
		"https://linkedin.com.evil.example/in/ana-lee-example":   "",
		"https://evil.example/?u=https://www.linkedin.com/in/a":  "",
		"ana lee": "",
		"":        "",
	} {
		if got := LinkedInProfile(in); got != want {
			t.Errorf("LinkedInProfile(%q) = %q, want %q", in, got, want)
		}
	}
}

// The key of a query without a LinkedIn URL is what it always was, so
// answers remembered before still answer.
func TestPersonQueryCacheKey(t *testing.T) {
	for want, q := range map[string]PersonQuery{
		"name:ana|lee|acme.example|":     {FirstName: "Ana", LastName: "Lee", Domain: "acme.example"},
		"name:ana|lee|acme.example|acme": {FirstName: "Ana", LastName: "Lee", Domain: "acme.example", Company: "Acme"},
		"email:ana@acme.example":         {Email: "Ana@acme.example", FirstName: "Ana", LastName: "Lee", Domain: "acme.example"},
		"id:p1":                          {ProviderID: "p1", Email: "ana@acme.example"},
		"linkedin:https://www.linkedin.com/in/ana-lee-example": {LinkedInURL: "https://www.linkedin.com/in/Ana-Lee-Example/", FirstName: "Ana", LastName: "Lee", Domain: "acme.example"},
		"": {FirstName: "Ana"},
	} {
		if got := q.CacheKey(); got != want {
			t.Errorf("CacheKey(%+v) = %q, want %q", q, got, want)
		}
	}
}
