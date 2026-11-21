package parallel

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

var peopleQuery = c.PeopleResearchQuery{Domain: "https://www.Northpeak.example/team", Name: " Northpeak  Labs "}

const peopleDone = `{
	"run": {"run_id": "trun_test1", "status": "completed"},
	"output": {"type": "json", "content": {"people": [
		{"name": "  Ana   Diaz ", "title": "CEO", "linkedin_url": "https://www.linkedin.com/in/ana-diaz-example/?trk=x",
		 "work_email": "Ana.Diaz@Northpeak.example", "email_source": "https://northpeak.example/team", "person_source": "https://northpeak.example/about"},
		{"name": "Ben Ito", "title": "CTO", "linkedin_url": "https://www.linkedin.com/company/northpeak-example",
		 "work_email": "Ben Ito <ben@northpeak.example>", "email_source": "javascript:alert(1)", "person_source": "not a url"},
		{"name": "ana diaz", "title": "Chief Executive", "linkedin_url": "", "work_email": "", "email_source": "", "person_source": ""},
		{"name": "", "title": "Nobody", "linkedin_url": "", "work_email": "", "email_source": "", "person_source": ""},
		{"name": 7, "title": "A number"},
		"not a person",
		{"name": "Cy Roe", "title": {"odd": true}, "linkedin_url": null, "work_email": "cy@", "email_source": null, "person_source": "http://news.example/cy"}
	]}}
}`

func TestResearchPeople(t *testing.T) {
	f, cl := newFake(t)
	f.create = queued
	// Still running the first time it is asked, done the second.
	f.result = func(w http.ResponseWriter, n int) {
		if n == 1 {
			answer(http.StatusRequestTimeout, stillRunning)(w)
			return
		}
		answer(http.StatusOK, peopleDone)(w)
	}
	res, err := quick.ResearchPeople(context.Background(), cl, creds, peopleQuery)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"POST /v1/tasks/runs", "GET /v1/tasks/runs/trun_test1/result", "GET /v1/tasks/runs/trun_test1/result"}; !reflect.DeepEqual(f.calls, want) {
		t.Errorf("calls: %v", f.calls)
	}
	// The default effort is standard: the base processor.
	if f.created["processor"] != "base" || len(f.created) != 3 {
		t.Errorf("request: %v", f.created)
	}
	wantInput := strings.Join([]string{
		"Find up to 8 named people who currently work at Northpeak Labs (website domain northpeak.example).",
		"Prefer founders, the CEO, CTO and other executives, then heads and directors of data, engineering or product.",
		"For each person give their full name, current title, personal LinkedIn profile URL (linkedin.com/in/...) and the URL of a page showing they work there now.",
		"Give a work email only when it is published in a source, belongs to that person, and ends in @northpeak.example. Never guess or construct an address. Never give shared inboxes such as info@, sales@ or support@.",
		"Use an empty string for anything you cannot find. Do not include people who have left the company.",
	}, "\n")
	if f.created["input"] != wantInput {
		t.Errorf("input:\n%s\nwant:\n%s", f.created["input"], wantInput)
	}
	str := func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
	wantSpec := map[string]any{"output_schema": map[string]any{"type": "json", "json_schema": map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"people"},
		"properties": map[string]any{"people": map[string]any{
			"type": "array", "description": "Up to 8 people who currently work at the company, decision makers first.",
			"items": map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []any{"name", "title", "linkedin_url", "work_email", "email_source", "person_source"},
				"properties": map[string]any{
					"name":          str("Full name of the person."),
					"title":         str("Their current job title at the company."),
					"linkedin_url":  str("Their personal LinkedIn profile URL (linkedin.com/in/...), or empty string."),
					"work_email":    str("Their own work email on the company's domain, only if published in a source. Never guessed. Empty string if none."),
					"email_source":  str("URL of the page where the work email is published, or empty string."),
					"person_source": str("URL of the page showing they currently work at the company."),
				},
			},
		}},
	}}}
	if !reflect.DeepEqual(f.created["task_spec"], wantSpec) {
		t.Errorf("task_spec: %v", f.created["task_spec"])
	}
	if res.RunID != "trun_test1" || res.Cost != (c.SEOCost{USD: 0.01, Estimated: true}) {
		t.Errorf("run %q cost %+v", res.RunID, res.Cost)
	}
	// Each person once, by name; only a person's profile, a plain address
	// and web pages are kept.
	want := []c.ResearchedPerson{
		{Name: "Ana Diaz", Title: "CEO", LinkedInURL: "https://www.linkedin.com/in/ana-diaz-example",
			Email: "ana.diaz@northpeak.example", EmailSource: "https://northpeak.example/team", Source: "https://northpeak.example/about"},
		{Name: "Ben Ito", Title: "CTO"},
		{Name: "Cy Roe", Source: "http://news.example/cy"},
	}
	if !reflect.DeepEqual(res.People, want) {
		t.Errorf("people: %+v", res.People)
	}
	if _, ok := c.Connector(Connector{}).(c.PeopleResearcher); !ok || !(Connector{}).Definition().Has(c.CapResearchPeople) {
		t.Error("Parallel does not offer people research")
	}
}

func TestResearchPeopleEffortNameAndExclusions(t *testing.T) {
	for effort, want := range map[string]struct {
		processor string
		usd       float64
	}{c.ResearchLight: {"lite", 0.005}, c.ResearchStandard: {"base", 0.01}, c.ResearchDeep: {"core", 0.025}} {
		f, cl := newFake(t)
		f.create = queued
		f.result = func(w http.ResponseWriter, _ int) { answer(http.StatusOK, `{"output":{"content":{"people":[]}}}`)(w) }
		// No name: the domain names the company. The people to leave out
		// are the last line.
		q := c.PeopleResearchQuery{Domain: "northpeak.example", Effort: effort, Exclude: []string{" Ana  Diaz ", "", "Ben Ito", "ana diaz"}}
		res, err := quick.ResearchPeople(context.Background(), cl, creds, q)
		if err != nil || f.created["processor"] != want.processor || res.Cost.USD != want.usd {
			t.Errorf("%s: processor %v, cost %+v, %v", effort, f.created["processor"], res.Cost, err)
		}
		lines := strings.Split(f.created["input"].(string), "\n")
		if len(lines) != 6 || lines[0] != "Find up to 8 named people who currently work at northpeak.example (website domain northpeak.example)." ||
			lines[5] != "Do not return these people, they were already considered: Ana Diaz; Ben Ito." {
			t.Errorf("%s: input %q", effort, lines)
		}
	}
}

// The answer is text from the web in several shapes; nobody in it is an
// answer, not an error, and is still paid for.
// The roles the caller wants most come before the people to leave out;
// without roles the task is as before.
func TestResearchPeopleRoles(t *testing.T) {
	f, cl := newFake(t)
	f.create = queued
	f.result = func(w http.ResponseWriter, _ int) { answer(http.StatusOK, `{"output":{"content":{"people":[]}}}`)(w) }
	q := c.PeopleResearchQuery{Domain: "northpeak.example", Roles: []string{" Head of  IT ", "CTO", "head of it", ""}, Exclude: []string{"Ana Diaz"}}
	if _, err := quick.ResearchPeople(context.Background(), cl, creds, q); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(f.created["input"].(string), "\n")
	if len(lines) != 7 || lines[5] != "Look first for people in these roles, most wanted first: Head of IT; CTO." ||
		lines[6] != "Do not return these people, they were already considered: Ana Diaz." {
		t.Errorf("input %q", lines)
	}
}

func TestResearchPeopleAnswers(t *testing.T) {
	ana := `{"name":"Ana Diaz","title":"CEO","linkedin_url":"","work_email":"","email_source":"","person_source":""}`
	many := make([]string, 12)
	for i := range many {
		many[i] = `{"name":"Person ` + string(rune('A'+i)) + `","title":"Engineer"}`
	}
	long := strings.Repeat("n", 300)
	for name, tc := range map[string]struct {
		content string
		names   []string
	}{
		"an object":            {`{"people":[` + ana + `]}`, []string{"Ana Diaz"}},
		"a bare list":          {`[` + ana + `]`, []string{"Ana Diaz"}},
		"a string":             {`"{\"people\":[{\"name\":\"Ana Diaz\"}]}"`, []string{"Ana Diaz"}},
		"a string with a list": {`"[{\"name\":\"Ana Diaz\"}]"`, []string{"Ana Diaz"}},
		"nobody":               {`{"people":[]}`, []string{}},
		"no list":              {`{"people":null}`, []string{}},
		"words":                {`"I could not find anyone."`, []string{}},
		"something else":       {`42`, []string{}},
		"at most eight":        {`{"people":[` + strings.Join(many, ",") + `]}`, []string{"Person A", "Person B", "Person C", "Person D", "Person E", "Person F", "Person G", "Person H"}},
		"a long name is cut":   {`[{"name":"` + long + `"}]`, []string{long[:200]}},
	} {
		f, cl := newFake(t)
		f.create = queued
		f.result = func(w http.ResponseWriter, _ int) { answer(http.StatusOK, `{"output":{"content":`+tc.content+`}}`)(w) }
		res, err := quick.ResearchPeople(context.Background(), cl, creds, peopleQuery)
		got := []string{}
		for _, p := range res.People {
			got = append(got, p.Name)
		}
		if err != nil || !reflect.DeepEqual(got, tc.names) || res.People == nil || res.Cost.USD != 0.01 || res.RunID != "trun_test1" {
			t.Errorf("%s: %v cost %+v run %q, %v", name, got, res.Cost, res.RunID, err)
		}
	}
}

func TestResearchPeopleBadQueriesSendNothing(t *testing.T) {
	f, cl := newFake(t)
	for name, q := range map[string]c.PeopleResearchQuery{
		"no domain":      {Name: "Northpeak"},
		"not a domain":   {Domain: "northpeak"},
		"a bad effort":   {Domain: "northpeak.example", Effort: "extreme"},
		"a long name":    {Domain: "northpeak.example", Name: strings.Repeat("n", 201)},
		"a long exclude": {Domain: "northpeak.example", Exclude: []string{strings.Repeat("n", 201)}},
		"a long role":    {Domain: "northpeak.example", Roles: []string{strings.Repeat("r", 101)}},
	} {
		if _, err := quick.ResearchPeople(context.Background(), cl, creds, q); c.KindOf(err) != c.ErrInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("calls: %v", f.calls)
	}
}

// The run, the wait and picking a run up again are the company research's.
func TestResearchPeopleWaitsAndIsPickedUp(t *testing.T) {
	f, cl := newFake(t)
	f.create = queued
	f.result = func(w http.ResponseWriter, _ int) { answer(http.StatusRequestTimeout, stillRunning)(w) }
	res, err := quick.ResearchPeople(context.Background(), cl, creds, peopleQuery)
	if !c.IsTimeout(err) || res.RunID != "trun_test1" || res.Cost != (c.SEOCost{}) || len(res.People) != 0 {
		t.Fatalf("still running: %+v %v", res, err)
	}
	// Asking again with the id starts nothing new, and is paid for once.
	f.calls = nil
	f.result = func(w http.ResponseWriter, _ int) {
		answer(http.StatusOK, `{"output":{"content":[{"name":"Ana Diaz"}]}}`)(w)
	}
	q := peopleQuery
	q.RunID = res.RunID
	res, err = quick.ResearchPeople(context.Background(), cl, creds, q)
	if err != nil || len(res.People) != 1 || res.Cost.USD != 0.01 || len(f.calls) != 1 || f.calls[0] != "GET /v1/tasks/runs/trun_test1/result" {
		t.Fatalf("picked up: %+v %v after %v", res, err, f.calls)
	}

	// A run that failed is over: no id to ask about again.
	f.result = func(w http.ResponseWriter, _ int) {
		answer(http.StatusNotFound, `{"type":"error","error":{"message":"Run failed"}}`)(w)
	}
	f.run = answer(http.StatusOK, `{"run_id":"trun_test1","status":"failed"}`)
	res, err = quick.ResearchPeople(context.Background(), cl, creds, q)
	if c.KindOf(err) != c.ErrUnavailable || c.IsTimeout(err) || res.RunID != "" {
		t.Fatalf("a failed run: %+v %v", res, err)
	}

	// Starting can fail: a refused key, a processor the account can't use.
	if _, err = quick.ResearchPeople(context.Background(), cl, c.Creds{"api_key": "test-another-fake-key"}, peopleQuery); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("a refused key: %v", err)
	}
	f.create = answer(http.StatusForbidden, `{"type":"error","error":{"message":"processor not available"}}`)
	if res, err = quick.ResearchPeople(context.Background(), cl, creds, peopleQuery); c.KindOf(err) != c.ErrInvalid || res.RunID != "" {
		t.Fatalf("a processor it can't use: %+v %v", res, err)
	}
	f.create = answer(http.StatusPaymentRequired, `{"type":"error","error":{"message":"Insufficient credit"}}`)
	if _, err = quick.ResearchPeople(context.Background(), cl, creds, peopleQuery); c.KindOf(err) != c.ErrQuota {
		t.Fatalf("no balance: %v", err)
	}
}
