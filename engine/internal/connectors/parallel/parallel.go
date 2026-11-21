// Package parallel connects Parallel (parallel.ai): its Task API researches
// a company on the web and answers a brief with the pages each finding came
// from. It implements connectors.CompanyResearcher, and
// connectors.PeopleResearcher (people.go: who works at a company), on the
// workspace's own Parallel API key.
//
// The Task API is asynchronous: POST /v1/tasks/runs starts a run, and
// GET /v1/tasks/runs/{id}/result holds the request open until the run ends
// (408 while it is still going). Request shapes follow
// https://docs.parallel.ai/api-reference. The base URL can be pointed at a
// fake in tests through connectors.Client.BaseURLs.
package parallel

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const (
	// ID is the connector's id.
	ID   = "parallel"
	name = "Parallel"
	api  = "https://api.parallel.ai"
)

// Connector is Parallel's Task API.
type Connector struct {
	// Tests shorten the waiting; zero keeps the defaults below.
	wait, hold, pause time.Duration
}

// tier is how one effort runs on Parallel: the processor, its list price
// per run (docs.parallel.ai/getting-started/pricing; only completed runs
// are charged) and how long Outflare waits for it, a little over the
// processor's usual latency.
type tier struct {
	processor string
	usd       float64
	wait      time.Duration
}

var tiers = map[string]tier{
	c.ResearchLight:    {"lite", 0.005, 2 * time.Minute},
	c.ResearchStandard: {"base", 0.01, 3 * time.Minute},
	c.ResearchDeep:     {"core", 0.025, 6 * time.Minute},
}

const (
	// holdFor is how long one result request is held open; pauseFor the
	// rest before asking again when Parallel answers "still running"
	// sooner than that.
	holdFor  = 60 * time.Second
	pauseFor = 2 * time.Second
	// grace is how much longer than the hold Outflare waits for the HTTP
	// answer, so Parallel's own 408 arrives.
	grace = 10 * time.Second
)

// Limits on what is kept of an answer: it is text from the web.
const (
	maxSections = 30
	maxSources  = 10
	maxText     = 20000
	maxTitle    = 300
	maxExcerpt  = 1000
)

func (Connector) Definition() c.Definition {
	return c.Definition{
		ID: ID, Name: name, Category: c.CategoryData,
		Summary: "Research a company on the web with your own Parallel account: findings with the pages they came from.",
		Description: "Looks one company up on the web through Parallel's Task API and answers a research brief about it, each finding with its sources. " +
			"It can also find the people who work at a company, each with the page that shows it. " +
			"Every run is paid from your own Parallel balance, on the processor the effort asks for (lite, base or core).",
		Website: "https://parallel.ai", DocsURL: "https://docs.parallel.ai", KeyURL: "https://platform.parallel.ai",
		Auth:         c.AuthAPIKey,
		Fields:       []c.Field{{Name: "api_key", Label: "API key", Secret: true, Help: "Parallel platform → API Keys."}},
		Capabilities: []string{c.CapResearchCompany, c.CapResearchPeople},
		Pricing:      "Per completed run on your Parallel balance, by processor",
	}
}

func (Connector) request(cl *c.Client, creds c.Creds, method, path string, body any) (*http.Request, error) {
	var rd *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, c.Errf(c.ErrInvalid, "could not build the %s request", name)
		}
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, cl.Base(ID, api)+path, rd)
	if err != nil {
		return nil, c.Errf(c.ErrInvalid, "bad %s address: %v", name, err)
	}
	req.Header.Set("x-api-key", creds["api_key"])
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// noRun is a well-formed run id no account has.
const noRun = "trun_00000000000000000000000000000000"

// Test asks for a run that doesn't exist, which spends nothing: Parallel
// answers 401 to a key it doesn't know and 404 to one it does. (Its balance
// endpoint takes an OAuth login, not the API key.)
func (p Connector) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	req, err := p.request(cl, creds, http.MethodGet, "/v1/tasks/runs/"+noRun, nil)
	if err != nil {
		return c.Account{}, err
	}
	if status, err := c.DoJSONRateLimited(ctx, cl, name, req, nil, 0); err != nil && status != http.StatusNotFound {
		return c.Account{}, err
	}
	return c.Account{Name: name}, nil
}

func (Connector) EstimateCompanyResearch(q c.CompanyResearchQuery) c.SEOCost {
	t, ok := tiers[q.Effort]
	if !ok {
		t = tiers[c.ResearchStandard]
	}
	return c.SEOCost{USD: t.usd, Estimated: true}
}

// outputSchema is the answer Parallel is asked for: a summary and a list
// of findings. Parallel gives each top-level field, and each list item,
// its own citations.
var outputSchema = map[string]any{
	"type": "json",
	"json_schema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary": map[string]any{"type": "string", "description": "A short answer to the research brief, in plain sentences."},
			"findings": map[string]any{
				"type":        "array",
				"description": "The findings that answer the research brief, one per topic, most relevant first.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title": map[string]any{"type": "string", "description": "A short heading for the finding."},
						"text":  map[string]any{"type": "string", "description": "What was found, in plain sentences."},
					},
					"required":             []string{"title", "text"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"summary", "findings"},
		"additionalProperties": false,
	},
}

// ResearchCompany starts a run (or picks up q.RunID) and waits for its
// result, for as long as the effort's processor usually takes. A run that
// outlasts the wait comes back as a timeout with its id; Parallel finishes
// and charges it either way, so the caller asks again with the id rather
// than starting another.
func (p Connector) ResearchCompany(ctx context.Context, cl *c.Client, creds c.Creds, q c.CompanyResearchQuery) (c.CompanyResearch, error) {
	q, err := q.Normal()
	if err != nil {
		return c.CompanyResearch{}, err
	}
	company := q.Domain
	if q.Name != "" {
		company = q.Name + " (" + q.Domain + ")"
	}
	t := tiers[q.Effort]
	res, runID, err := p.run(ctx, cl, creds, t, q.RunID, q.Domain,
		"Company to research: "+company+"\n\nResearch brief:\n"+q.Brief, outputSchema)
	if err != nil {
		return c.CompanyResearch{RunID: runID}, err
	}
	sections := res.sections()
	cost := c.SEOCost{USD: t.usd, Estimated: true}
	if len(sections) == 0 {
		// The run is done and paid for; asking again gives the same.
		return c.CompanyResearch{Cost: cost}, c.Errf(c.ErrUnavailable, "%s finished the research but returned nothing Outflare can read", name)
	}
	return c.CompanyResearch{Sections: sections, RunID: runID, Cost: cost}, nil
}

// run starts a run with input and schema on t's processor (or picks up
// runID) and waits for its result, for as long as the processor usually
// takes. With an error, the id it returns is the run to ask about again:
// set while the run is still going or its answer could not be read yet,
// empty when nothing started or the run has ended for good.
func (p Connector) run(ctx context.Context, cl *c.Client, creds c.Creds, t tier, runID, domain, input string, schema map[string]any) (result, string, error) {
	wait, hold, pause := t.wait, holdFor, pauseFor
	if p.wait > 0 {
		wait, hold, pause = p.wait, p.hold, p.pause
	}
	deadline := time.Now().Add(wait)

	if runID == "" {
		var err error
		if runID, err = p.start(ctx, cl, creds, t.processor, input, schema); err != nil {
			return result{}, "", err
		}
	}
	path := "/v1/tasks/runs/" + url.PathEscape(runID)
	for {
		left := time.Until(deadline)
		if left <= 0 || ctx.Err() != nil {
			return result{}, runID, c.TimedOutf("%s is still researching %s. Ask again with the run id to read the result.", name, domain)
		}
		h := min(hold, left)
		req, err := p.request(cl, creds, http.MethodGet, path+"/result?timeout="+strconv.Itoa(max(1, int(h.Seconds()))), nil)
		if err != nil {
			return result{}, runID, err
		}
		var res result
		status, err := c.DoJSONRateLimited(ctx, cl, name, req, &res, h+grace)
		switch {
		case err == nil:
			return res, runID, nil
		case status == http.StatusRequestTimeout:
			// Still running: rest, then ask again until the wait runs out.
			select {
			case <-ctx.Done():
			case <-time.After(min(pause, left)):
			}
		case status == http.StatusNotFound:
			return result{}, "", p.ended(ctx, cl, creds, path)
		case c.IsTimeout(err):
			// No answer within the hold; the loop checks the wait.
		default:
			return result{}, runID, err
		}
	}
}

// start creates a run and returns its id.
func (p Connector) start(ctx context.Context, cl *c.Client, creds c.Creds, processor, input string, schema map[string]any) (string, error) {
	req, err := p.request(cl, creds, http.MethodPost, "/v1/tasks/runs", map[string]any{
		"processor": processor,
		"input":     input,
		"task_spec": map[string]any{"output_schema": schema},
	})
	if err != nil {
		return "", err
	}
	var run struct {
		RunID string `json:"run_id"`
	}
	status, err := c.DoJSONRateLimited(ctx, cl, name, req, &run, 0)
	if status == http.StatusForbidden {
		// Parallel's 403 here is a processor the account can't use, not a
		// refused key (that is 401), so the connection isn't broken.
		return "", c.Errf(c.ErrInvalid, "%s refused the request (HTTP 403): this account can't use the %s processor.", name, processor)
	}
	if err != nil {
		return "", err
	}
	if run.RunID == "" {
		return "", c.Errf(c.ErrUnavailable, "%s started the research without a run id", name)
	}
	return run.RunID, nil
}

// ended explains a run whose result is gone (404): it failed, was
// cancelled, or Parallel no longer knows it. Either way it is over, so
// the error carries no run id and the caller starts afresh. Parallel's own
// words about the failure may quote the web, so only the status is kept.
func (p Connector) ended(ctx context.Context, cl *c.Client, creds c.Creds, path string) error {
	req, err := p.request(cl, creds, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	var run struct {
		Status string `json:"status"`
	}
	status, err := c.DoJSONRateLimited(ctx, cl, name, req, &run, 0)
	if status == http.StatusNotFound {
		return c.Errf(c.ErrInvalid, "%s doesn't know this research run any more. Start the research again.", name)
	}
	if err != nil {
		return err
	}
	switch run.Status {
	case "failed", "cancelled", "cancelling":
		return c.Errf(c.ErrUnavailable, "%s could not finish the research (run %s). Try again later.", name, run.Status)
	}
	return c.Errf(c.ErrUnavailable, "%s has no result for the research. Try again later.", name)
}

// result is a finished run's answer. Content is the JSON asked for with
// outputSchema, sometimes sent as a string; basis holds each field's
// citations, list items as "findings.0".
type result struct {
	Output struct {
		Content json.RawMessage `json:"content"`
		Basis   []struct {
			Field     string     `json:"field"`
			Citations []citation `json:"citations"`
		} `json:"basis"`
	} `json:"output"`
}

type citation struct {
	Title    string   `json:"title"`
	URL      string   `json:"url"`
	Excerpts []string `json:"excerpts"`
}

type findings struct {
	Summary  string `json:"summary"`
	Findings []struct {
		Title string `json:"title"`
		Text  string `json:"text"`
	} `json:"findings"`
}

// sections maps the answer onto sections, or none when it isn't one.
func (r result) sections() []c.ResearchSection {
	raw := bytes.TrimSpace(r.Output.Content)
	var plain string
	if len(raw) > 0 && raw[0] == '"' {
		if json.Unmarshal(raw, &plain) != nil {
			return nil
		}
		raw = []byte(strings.TrimSpace(plain))
	}
	var f findings
	if json.Unmarshal(raw, &f) != nil {
		// A plain-text answer is one section with every source.
		if plain = clip(plain, maxText); plain == "" {
			return nil
		}
		return []c.ResearchSection{{Title: "Findings", Text: plain, Sources: r.sources(func(string) bool { return true })}}
	}
	var out []c.ResearchSection
	if s := clip(f.Summary, maxText); s != "" {
		out = append(out, c.ResearchSection{Title: "Summary", Text: s, Sources: r.sources(func(field string) bool { return field == "summary" })})
	}
	for i, item := range f.Findings {
		if len(out) == maxSections {
			break
		}
		text := clip(item.Text, maxText)
		if text == "" {
			continue
		}
		title := clip(item.Title, maxTitle)
		if title == "" {
			title = "Finding"
		}
		own := "findings." + strconv.Itoa(i)
		src := r.sources(func(field string) bool { return field == own || strings.HasPrefix(field, own+".") })
		if src == nil {
			src = r.sources(func(field string) bool { return field == "findings" })
		}
		out = append(out, c.ResearchSection{Title: title, Text: text, Sources: src})
	}
	return out
}

// sources are the citations of the fields want picks: http(s) pages only,
// each once, the first few.
func (r result) sources(want func(field string) bool) []c.ResearchSource {
	var out []c.ResearchSource
	seen := map[string]bool{}
	for _, b := range r.Output.Basis {
		if !want(b.Field) {
			continue
		}
		for _, ci := range b.Citations {
			u, err := url.Parse(strings.TrimSpace(ci.URL))
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || seen[u.String()] {
				continue
			}
			if len(out) == maxSources {
				return out
			}
			seen[u.String()] = true
			s := c.ResearchSource{Title: clip(ci.Title, maxTitle), URL: u.String()}
			for _, e := range ci.Excerpts {
				if s.Excerpt = clip(e, maxExcerpt); s.Excerpt != "" {
					break
				}
			}
			out = append(out, s)
		}
	}
	return out
}

// clip trims s and cuts it to at most n characters.
func clip(s string, n int) string {
	s = strings.TrimSpace(strings.ToValidUTF8(s, ""))
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:n])) + "…"
}
