// Package research places the email domains the global data cannot:
// the long tail, where the domain's own homepage usually says what it is
// ("free disposable email, no sign-up", "secure mailbox from €1 a month").
//
// Every part of it runs on the workspace's own accounts, because Outflare
// never supplies its API keys for a connector or for AI:
//
//   - the workspace's scraping connector (Geonode) reads the homepage,
//   - the workspace's own AI model reads the words,
//   - what it costs is booked against the workspace's own monthly limits.
//
// So a verdict belongs to the workspace that paid for it. It is stored
// against that workspace, shown only to it, and never written back into
// the global classifier data, which stays Outflare's reviewed providers
// plus the public lists.
package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/classify"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/llm"
	"github.com/outflareai/outflare/engine/internal/track"
)

// JobResearch researches one domain for one workspace.
const JobResearch = "classify.research"

const (
	// A verdict is kept this long before the domain is researched again:
	// a throwaway provider's site changes, but not weekly.
	freshFor = 90 * 24 * time.Hour
	// What one domain is assumed to cost before it runs, so a workspace at
	// its limit never starts a call it cannot pay for: a homepage and a
	// short answer, rounded well up.
	estimateCents = 2
	// The model's answer is short; the question carries the page.
	maxAnswerTokens = 400
	// A domain's research gives up after this long, page and model
	// together.
	timeout = 2 * time.Minute
)

// Events. Research is a product action, so it says what it did and why it
// could not, with codes and never with page text.
const (
	EventResearched = "domain.researched"
	EventFailed     = "domain.research_failed"
)

// Choice is a workspace's AI model, opened with the workspace's own key
// and allowed to spend.
type Choice struct {
	Provider      llm.Provider
	IntegrationID string
	Model         llm.Model
	Price         llm.Price
	// Task is the tool the model was opened for, as the AI usage log
	// names it.
	Task string
}

// Models opens the workspace's own AI model and books what it spends.
// The engine's API server implements it, so the monthly AI limit is
// checked in exactly one place.
type Models interface {
	// ForTask opens the model the workspace chose for a task, refusing
	// when the workspace has no AI connected or the month's AI limit would
	// not cover about cents more.
	ForTask(ctx context.Context, ws, task string, cents int64) (Choice, error)
	// Spent books what a call cost on the workspace's own provider bill.
	Spent(ctx context.Context, ws string, c Choice, u llm.Usage, ref, note string) error
}

// Pages reads a domain's homepage for a workspace.
type Pages interface {
	// Homepage returns the page and what the fetch cost on the provider's
	// plan. A page that simply could not be read comes back with Fetched
	// false, and the error says why.
	Homepage(ctx context.Context, ws, domain string) (classify.Page, float64, error)
}

// Service researches domains in the background.
type Service struct {
	Pool  *pgxpool.Pool
	Store *connectors.Store
	// Pages reads homepages; nil uses the workspace's own connected page
	// reader (Geonode).
	Pages Pages
	// Models is the workspace's AI model; nil turns research off.
	Models Models
	Log    *slog.Logger
	// Now is the clock, for tests.
	Now func() time.Time
}

// Register handles research jobs.
func (s *Service) Register(q *jobs.Queue) { q.Handle(JobResearch, s.job) }

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Settings are a workspace's research choices, read from its AI settings.
type Settings struct {
	On bool
	// LimitCents is the most research may cost in a calendar month, or nil
	// for no limit of its own. The workspace's overall AI limit still
	// applies, and research stops at whichever comes first.
	LimitCents *int64
}

// Settings reads a workspace's research settings.
func (s *Service) Settings(ctx context.Context, q jobs.Querier, ws string) (Settings, error) {
	var out Settings
	err := q.QueryRow(ctx, `SELECT domain_research, domain_research_limit_cents
		FROM workspace_ai_settings WHERE workspace_id = $1`, ws).Scan(&out.On, &out.LimitCents)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, nil
	}
	return out, err
}

// Row is a stored verdict.
type Row struct {
	Domain       string
	Verdict      classify.Verdict
	Model        string
	ResearchedAt time.Time
	CostCents    int64
	HomepageURL  string
}

// Of returns the workspace's research into a domain, if it has any that is
// still fresh.
func (s *Service) Of(ctx context.Context, q jobs.Querier, ws, domain string) (Row, bool, error) {
	var (
		r     Row
		sub   *string
		sigs  []byte
		attrs []byte
	)
	err := q.QueryRow(ctx, `SELECT domain, category, subcategory, signals, attributes, note, model, cost_cents, homepage_url, researched_at
		FROM domain_research WHERE workspace_id = $1 AND domain = $2 AND researched_at > $3`,
		ws, domain, s.now().Add(-freshFor)).
		Scan(&r.Domain, &r.Verdict.Category, &sub, &sigs, &attrs, &r.Verdict.Note, &r.Model, &r.CostCents, &r.HomepageURL, &r.ResearchedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Row{}, false, nil
	}
	if err != nil {
		return Row{}, false, err
	}
	if sub != nil {
		r.Verdict.Subcategory = *sub
	}
	_ = json.Unmarshal(sigs, &r.Verdict.Signals)
	readAttrs(attrs, &r.Verdict)
	return r, true, nil
}

// Queue asks for a domain to be researched, unless the workspace has it
// already or research is off. It reports whether a job is now waiting.
// The job runs in the caller's transaction, so it is enqueued exactly when
// the request that caused it succeeds.
func (s *Service) Queue(ctx context.Context, q jobs.Querier, ws, domain string) (bool, error) {
	if s == nil || s.Models == nil {
		return false, nil
	}
	set, err := s.Settings(ctx, q, ws)
	if err != nil || !set.On {
		return false, err
	}
	// One job per domain per day: asking about the same domain all morning
	// must not pay for it twice.
	key := fmt.Sprintf("%s:%s:%s", ws, domain, s.now().Format(time.DateOnly))
	_, err = jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobResearch, WorkspaceID: &ws, IdempotencyKey: key,
		Payload: map[string]string{"domain": domain}, MaxAttempts: 3})
	return err == nil, err
}

type payload struct {
	Domain string `json:"domain"`
}

func (s *Service) job(ctx context.Context, j jobs.Job) error {
	var p payload
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		return err
	}
	if j.WorkspaceID == nil {
		return errors.New("research needs a workspace")
	}
	return s.Do(ctx, *j.WorkspaceID, p.Domain)
}

// Do researches one domain for one workspace: its own page reader fetches
// the homepage, its own model reads it, and the verdict is stored against
// that workspace. An error means the job should be retried.
func (s *Service) Do(ctx context.Context, ws, domain string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ev := track.Event{WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"task": classify.ResearchTask}}
	fail := func(code string, err error) error {
		track.Fail(ctx, s.Log, track.Event{Name: EventFailed, WorkspaceID: ws, ActorType: "system", Surface: "job",
			Props: ev.Props}, code, err)
		return err
	}

	set, err := s.Settings(ctx, s.Pool, ws)
	if err != nil {
		return err
	}
	if !set.On {
		// Turned off between the request and the job: nothing to do, and
		// nothing to charge for.
		return nil
	}
	if err := s.withinLimit(ctx, ws, set); err != nil {
		return fail("research_budget_reached", err)
	}

	// The homepage, through the workspace's own scraping connection.
	page, units, err := s.pages().Homepage(ctx, ws, domain)
	if err != nil {
		if errors.Is(err, errNoFetcher) {
			return fail("connector_needed", err)
		}
		// A site that cannot be read is not a failure of ours: the model
		// decides on the evidence that is left.
		s.Log.Info("research homepage unread", "workspace_id", ws, "error", err)
	}

	res, err := (&classify.Classifier{}).Classify(ctx, classify.Input{Domain: domain})
	if err != nil {
		return fail("invalid_request", err)
	}
	choice, err := s.Models.ForTask(ctx, ws, classify.ResearchTask, estimateCents)
	if err != nil {
		return fail("ai_model_needed", err)
	}
	v, usage, err := s.ask(ctx, choice, res, page)
	if usage.InputTokens > 0 || usage.OutputTokens > 0 {
		// A call that answered badly still cost the workspace money.
		if err := s.Models.Spent(ctx, ws, choice, usage, spendRef(domain, s.now()), "Email domain research"); err != nil {
			s.Log.Error("recording research spend", "workspace_id", ws, "error", err)
		}
	}
	if err != nil {
		return fail("ai_failed", err)
	}

	cost := choice.Price.Cents(usage)
	if err := s.save(ctx, ws, domain, v, choice.Model.ID(), page, cost, units); err != nil {
		return err
	}
	ev.Name = EventResearched
	ev.Props["category"] = v.Category
	ev.Props["signals"] = len(v.Signals)
	ev.Props["homepage_read"] = page.Fetched
	ev.Props["cost_cents"] = cost
	ev.Props["model"] = choice.Model.ID()
	track.Emit(ctx, ev)
	return nil
}

// spendRef names one research call's spend. Spend entries are unique per
// ref, so every call, a retry included, gets its own.
func spendRef(domain string, at time.Time) string {
	return "domain_research:" + domain + ":" + at.UTC().Format(time.RFC3339Nano)
}

// withinLimit refuses when this month's research already reached the
// workspace's research limit. The overall AI limit is checked by Models.
func (s *Service) withinLimit(ctx context.Context, ws string, set Settings) error {
	if set.LimitCents == nil {
		return nil
	}
	var spent int64
	err := s.Pool.QueryRow(ctx, `SELECT COALESCE(sum(cost_cents), 0) FROM domain_research
		WHERE workspace_id = $1 AND researched_at >= date_trunc('month', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`, ws).
		Scan(&spent)
	if err != nil {
		return err
	}
	if spent+estimateCents > *set.LimitCents {
		return fmt.Errorf("domain research reached this month's limit of $%.2f (spent $%.2f)",
			float64(*set.LimitCents)/100, float64(spent)/100)
	}
	return nil
}

// ErrNoFetcher means the workspace has no connector that can read a page.
var errNoFetcher = errors.New("no connector that can read a page is connected")

func (s *Service) pages() Pages {
	if s.Pages != nil {
		return s.Pages
	}
	return connectorPages{s}
}

// connectorPages reads homepages through the workspace's own connected
// page reader. Outflare never fetches the page itself: the request goes
// out on the workspace's account, as its own research.
type connectorPages struct{ s *Service }

func (c connectorPages) Homepage(ctx context.Context, ws, domain string) (classify.Page, float64, error) {
	s := c.s
	if s.Store == nil {
		return classify.Page{}, 0, errNoFetcher
	}
	list, err := s.Store.List(ctx, s.Pool, ws, "")
	if err != nil {
		return classify.Page{}, 0, err
	}
	for _, i := range list {
		if i.Status != connectors.StatusConnected {
			continue
		}
		o, err := s.Store.Open(ctx, s.Pool, ws, i.ID)
		if err != nil {
			continue
		}
		f, ok := o.Connector.(connectors.PageFetcher)
		if !ok {
			continue
		}
		p, err := f.FetchPage(ctx, o.Client, o.Creds, "https://"+domain+"/", false)
		if err != nil {
			return classify.Page{}, p.Cost.Units, err
		}
		if p.Status < 200 || p.Status >= 300 || strings.TrimSpace(p.HTML) == "" {
			return classify.Page{URL: p.URL, Status: p.Status}, p.Cost.Units, nil
		}
		return classify.Page{URL: p.URL, Status: p.Status, HTML: p.HTML, Fetched: true}, p.Cost.Units, nil
	}
	return classify.Page{}, 0, errNoFetcher
}

// ask puts the evidence and the page to the workspace's model.
func (s *Service) ask(ctx context.Context, c Choice, res classify.Result, p classify.Page) (classify.Verdict, llm.Usage, error) {
	resp, err := c.Model.Chat(ctx, llm.Request{
		System:     classify.ResearchSystem(),
		Messages:   []llm.Message{{Role: "user", Text: classify.ResearchPrompt(res, p)}},
		JSONSchema: classify.VerdictSchema(),
		MaxTokens:  maxAnswerTokens,
	})
	if err != nil {
		return classify.Verdict{}, resp.Usage, err
	}
	if resp.Stop == llm.StopRefusal {
		return classify.Verdict{}, resp.Usage, errors.New("the model refused to answer")
	}
	v, err := classify.ParseVerdict([]byte(resp.Message.Text))
	return v, resp.Usage, err
}

func (s *Service) save(ctx context.Context, ws, domain string, v classify.Verdict, model string,
	p classify.Page, cents int64, units float64) error {
	var sub *string
	if v.Subcategory != "" {
		sub = &v.Subcategory
	}
	sigs, _ := json.Marshal(v.Signals)
	attrs, _ := json.Marshal(attrsOf(v))
	var status *int
	if p.Status > 0 {
		status = &p.Status
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO domain_research
		(workspace_id, domain, category, subcategory, confidence_status, signals, attributes, note,
		 homepage_url, homepage_status, model, cost_cents, page_units, researched_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (workspace_id, domain) DO UPDATE SET
			category = excluded.category, subcategory = excluded.subcategory,
			confidence_status = excluded.confidence_status, signals = excluded.signals,
			attributes = excluded.attributes, note = excluded.note, homepage_url = excluded.homepage_url,
			homepage_status = excluded.homepage_status, model = excluded.model,
			cost_cents = excluded.cost_cents, page_units = excluded.page_units,
			researched_at = excluded.researched_at`,
		ws, domain, v.Category, sub, classify.Inferred, sigs, attrs, v.Note,
		p.URL, status, model, cents, units, s.now())
	return err
}

// attrsOf is the verdict's attributes as name -> true/false/unknown.
func attrsOf(v classify.Verdict) map[string]string {
	return map[string]string{
		"is_disposable":       v.IsDisposable.String(),
		"is_public_inbox":     v.IsPublicInbox.String(),
		"is_free_provider":    v.IsFreeProvider.String(),
		"is_forwarder":        v.IsForwarder.String(),
		"is_alias_service":    v.IsAliasService.String(),
		"is_privacy_provider": v.IsPrivacyProvider.String(),
	}
}

func readAttrs(raw []byte, v *classify.Verdict) {
	var m map[string]string
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	for name, into := range map[string]*classify.Tri{
		"is_disposable": &v.IsDisposable, "is_public_inbox": &v.IsPublicInbox,
		"is_free_provider": &v.IsFreeProvider, "is_forwarder": &v.IsForwarder,
		"is_alias_service": &v.IsAliasService, "is_privacy_provider": &v.IsPrivacyProvider,
	} {
		switch m[name] {
		case "true":
			*into = classify.TriTrue
		case "false":
			*into = classify.TriFalse
		}
	}
}
