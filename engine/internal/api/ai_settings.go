package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/aivis"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/classify"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/models"
	"github.com/outflareai/outflare/engine/internal/inbox"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/llm"
	"github.com/outflareai/outflare/engine/internal/missions"
	"github.com/outflareai/outflare/engine/internal/outcomes"
)

// The AI model a workspace uses to plan missions and run Outflare's own
// agent: which connection (Claude, OpenAI, Gemini, Mistral, xAI or a router), which of its
// models, what that costs, and the most AI may spend in a month. The key
// stays sealed on the integration; settings only point at it.

func (s *Server) routesAISettings(mux *http.ServeMux) {
	read, manage := auth.ScopeIntegrationsRead, auth.ScopeIntegrationsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/ai-settings", s.inWorkspace(read, s.getAISettings))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/ai-settings", s.inWorkspace(manage, s.putAISettings))
	mux.HandleFunc("GET /v1/workspaces/{ws}/integrations/{id}/models", s.inWorkspace(manage, s.listIntegrationModels))
}

// The tasks the AI model does. Each can use a model of its own, so routine
// work doesn't pay for the strongest model.
const (
	taskPlan    = "plan"     // drafting a mission from a goal
	taskTurn    = "turn"     // an agent turn that starts something or answers a person
	taskCheckIn = "check_in" // a scheduled check-in with nothing new from people
	// Labelling a reply to outreach (interested, not now, out of office...).
	taskReplyLabelling = inbox.LabelTask
	// Reading an unknown email domain's homepage to classify it.
	taskDomainResearch = classify.ResearchTask
	// Reading AI models' answers about a brand, such as checking them
	// against the brand's facts. Sampling itself asks the models chosen
	// under AI visibility.
	taskAIVisibility = aivis.Task
	// Drafting a reply to a post a social watch found.
	taskSocialReplies = socialReplyTask
	// Checking an email address the free checks could not settle.
	taskEmailVerification = "email_verification"
	// Asking the models chosen under AI visibility about a brand. It uses
	// those models, not a task model, so it is a tool in the usage log and
	// can have a limit, but has no model of its own here.
	taskVisibilitySampling = "ai_visibility_sampling"
)

type aiTask struct {
	ID, Name, Description string
}

var aiTasks = []aiTask{
	{taskPlan, "Planning", "Drafts a mission from your goal."},
	{taskTurn, "Agent turns", "Outflare's agent starting a mission, answering you and acting on your decisions."},
	{taskCheckIn, "Routine check-ins", "Outflare's agent looking in on a running mission when nothing new came from you."},
	{taskReplyLabelling, "Reply labelling", "Labelling replies to your outreach: interested, not now, out of office and so on."},
	{taskDomainResearch, "Domain research", "Reading an unknown email domain's homepage to tell what kind of company it is."},
	{taskAIVisibility, "AI visibility", "Reading what AI models answer about your brands, such as checking the answers against your brand's facts."},
	{taskSocialReplies, "Social replies", "Drafting replies to posts your social watches found, such as Reddit threads, for a person to approve."},
	{taskEmailVerification, "Email verification", "Checking email addresses the free checks could not settle."},
}

// taskTier says which of the provider's cheaper models a task defaults to.
func taskTier(task string, p llm.Provider) string {
	switch task {
	case taskCheckIn, taskAIVisibility, taskSocialReplies:
		return p.LightModel
	case taskReplyLabelling, taskDomainResearch, taskEmailVerification:
		if p.CheapModel != "" {
			return p.CheapModel
		}
		return p.LightModel
	}
	return ""
}

func validTask(id string) bool {
	return slices.ContainsFunc(aiTasks, func(t aiTask) bool { return t.ID == id })
}

// taskModel is the model a workspace chose for one task, on the provider
// that was in use when it chose.
type taskModel struct {
	Provider    string   `json:"provider"`
	Model       string   `json:"model"`
	InputCents  *float64 `json:"input_cents_per_million,omitempty"`
	OutputCents *float64 `json:"output_cents_per_million,omitempty"`
}

// defaultAILimitCents is the monthly AI limit of a workspace that never
// set one: US$50.
const defaultAILimitCents = 5000

type aiSettingsRow struct {
	IntegrationID *string
	Model         string
	InputCents    *float64
	OutputCents   *float64
	MonthlyLimit  *int64
	Tasks         map[string]taskModel
	// TaskLimits caps a tool's AI spend each calendar month, in cents, on
	// top of MonthlyLimit. A tool left out has no limit of its own.
	TaskLimits map[string]int64
	// Research reads an unknown email domain's homepage with the
	// workspace's own page reader and its own model; off until asked for.
	Research      bool
	ResearchLimit *int64
	UpdatedAt     *time.Time
}

func (s *Server) loadAISettings(ctx context.Context, q connectors.Querier, ws string) (aiSettingsRow, error) {
	return loadAISettings(ctx, q, ws)
}

// rowQuerier reads one row: a pool, a connection or a transaction.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func loadAISettings(ctx context.Context, q rowQuerier, ws string) (aiSettingsRow, error) {
	row := aiSettingsRow{MonthlyLimit: ptr(int64(defaultAILimitCents))}
	var tasks, limits []byte
	err := q.QueryRow(ctx, `SELECT integration_id::text, model, input_cents_per_m::float8, output_cents_per_m::float8, monthly_limit_cents,
			domain_research, domain_research_limit_cents, task_models, task_limits, updated_at
		FROM workspace_ai_settings WHERE workspace_id = $1`, ws).
		Scan(&row.IntegrationID, &row.Model, &row.InputCents, &row.OutputCents, &row.MonthlyLimit,
			&row.Research, &row.ResearchLimit, &tasks, &limits, &row.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return aiSettingsRow{MonthlyLimit: ptr(int64(defaultAILimitCents))}, nil
	}
	if err == nil {
		err = json.Unmarshal(tasks, &row.Tasks)
	}
	if err == nil {
		err = json.Unmarshal(limits, &row.TaskLimits)
	}
	return row, err
}

// taskLimit is the tool's own monthly limit, or nil when it has none.
func (row aiSettingsRow) taskLimit(task string) *int64 {
	if v, ok := row.TaskLimits[task]; ok {
		return &v
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

// errAI is a reason the workspace's AI model can't be used yet, worded for
// the person who can fix it.
type errAI struct {
	status int
	code   string
	msg    string
}

func (e errAI) Error() string { return e.msg }

// Code is the error code from the catalog (ai_model_needed, ai_budget_reached).
func (e errAI) Code() string { return e.code }

// aiChoice is the model a workspace uses, resolved and ready to call.
type aiChoice struct {
	Provider      llm.Provider
	IntegrationID string
	Task          string
	Model         llm.Model
	Price         llm.Price
	PriceSource   string // list (Outflare knows it) or workspace
	MonthlyLimit  *int64
	TaskLimit     *int64 // the tool's own monthly limit, on top of MonthlyLimit
}

// aiConnections lists the workspace's connected AI model integrations, in
// the selector's provider order.
func (s *Server) aiConnections(ctx context.Context, ws string) ([]connectors.Integration, error) {
	if s.integrations == nil {
		return nil, nil
	}
	list, err := s.integrations.List(ctx, s.pool, ws, "")
	if err != nil {
		return nil, err
	}
	out := []connectors.Integration{}
	for _, p := range llm.Providers {
		for _, i := range list {
			if i.Connector == p.ID && i.Status == connectors.StatusConnected {
				out = append(out, i)
			}
		}
	}
	return out, nil
}

// pickAI decides which connection and model settings point at, without
// opening the key.
func pickAI(row aiSettingsRow, conns []connectors.Integration) (connectors.Integration, llm.Provider, string, llm.Price, string, error) {
	if len(conns) == 0 {
		return connectors.Integration{}, llm.Provider{}, "", llm.Price{}, "", errAI{http.StatusConflict, "connector_needed",
			"Connect an AI model with your own API key first: Claude, OpenAI, Gemini, Mistral, xAI, OpenRouter, Vercel AI Gateway or Requesty (connector ids claude, openai, gemini, mistral, xai, openrouter, vercel_ai_gateway, requesty)."}
	}
	conn, model := conns[0], ""
	if row.IntegrationID != nil {
		i := slices.IndexFunc(conns, func(c connectors.Integration) bool { return c.ID == *row.IntegrationID })
		if i < 0 {
			return connectors.Integration{}, llm.Provider{}, "", llm.Price{}, "", errAI{http.StatusConflict, "ai_model_needed",
				"The AI model connection chosen under Settings → AI model is no longer connected. Reconnect it or choose another."}
		}
		conn, model = conns[i], row.Model
	}
	p, _ := llm.ProviderFor(conn.Connector)
	if model == "" {
		model = p.DefaultModel
	}
	if model == "" {
		return conn, p, "", llm.Price{}, "", errAI{http.StatusConflict, "ai_model_needed",
			"Choose which " + p.Name + " model to use under Settings → AI model."}
	}
	if price, ok := llm.KnownPrice(p.ID, model); ok {
		return conn, p, model, price, "list", nil
	}
	if row.IntegrationID != nil && row.InputCents != nil && row.OutputCents != nil {
		return conn, p, model, llm.Price{Input: *row.InputCents, Output: *row.OutputCents}, "workspace", nil
	}
	return conn, p, model, llm.Price{}, "", errAI{http.StatusConflict, "ai_model_needed",
		"Enter what " + model + " costs under Settings → AI model, so Outflare can keep to your spend limits."}
}

// forTask is the model and price for task, given the main model pickAI
// chose: the workspace's choice for the task, else the main model, except
// that routine and simple tasks use the provider's cheaper model (taskTier)
// when it costs less.
// custom says the workspace chose it.
func forTask(row aiSettingsRow, p llm.Provider, model string, price llm.Price, source, task string) (string, llm.Price, string, bool) {
	if t, ok := row.Tasks[task]; ok && t.Provider == p.ID && t.Model != "" {
		if known, ok := llm.KnownPrice(p.ID, t.Model); ok {
			return t.Model, known, "list", true
		}
		if t.InputCents != nil && t.OutputCents != nil {
			return t.Model, llm.Price{Input: *t.InputCents, Output: *t.OutputCents}, "workspace", true
		}
	}
	if tier := taskTier(task, p); tier != "" && tier != model {
		if cheaper, ok := llm.KnownPrice(p.ID, tier); ok && cheaper.Input+cheaper.Output < price.Input+price.Output {
			return tier, cheaper, "list", false
		}
	}
	return model, price, source, false
}

// aiModel opens the workspace's AI model for task.
func (s *Server) aiModel(ctx context.Context, ws, task string) (aiChoice, error) {
	row, err := s.loadAISettings(ctx, s.pool, ws)
	if err != nil {
		return aiChoice{}, err
	}
	conns, err := s.aiConnections(ctx, ws)
	if err != nil {
		return aiChoice{}, err
	}
	conn, p, model, price, source, err := pickAI(row, conns)
	if err != nil {
		return aiChoice{}, err
	}
	model, price, source, _ = forTask(row, p, model, price, source, task)
	o, err := s.integrations.Open(ctx, s.pool, ws, conn.ID)
	if err != nil {
		return aiChoice{}, err
	}
	m, err := llm.New(models.Config(p, o.Client, o.Creds["api_key"], model))
	if err != nil {
		return aiChoice{}, err
	}
	return aiChoice{Provider: p, IntegrationID: conn.ID, Task: task, Model: m, Price: price, PriceSource: source,
		MonthlyLimit: row.MonthlyLimit, TaskLimit: row.taskLimit(task)}, nil
}

// aiMonthSpend is what AI calls cost the workspace this calendar month (UTC).
func (s *Server) aiMonthSpend(ctx context.Context, q connectors.Querier, ws string) (int64, error) {
	return aiMonthSpend(ctx, q, ws)
}

func aiMonthSpend(ctx context.Context, q rowQuerier, ws string) (int64, error) {
	var cents int64
	err := q.QueryRow(ctx, `SELECT COALESCE(sum(amount_cents), 0) FROM spend_entries
		WHERE workspace_id = $1 AND source = 'auto' AND category = 'software' AND provider = ANY($2)
		  AND occurred_at >= date_trunc('month', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`, ws, llm.Vendors()).Scan(&cents)
	return cents, err
}

// checkAIBudget refuses a call that would take the month's AI spend past
// the workspace limit, counting estimate cents for the call itself.
func (s *Server) checkAIBudget(ctx context.Context, ws string, c aiChoice, estimate int64) error {
	if c.TaskLimit != nil {
		spent, err := aiTaskMonthSpend(ctx, s.pool, ws, c.Task)
		if err != nil {
			return err
		}
		if spent+estimate > *c.TaskLimit {
			return errAI{http.StatusConflict, "ai_budget_reached", fmt.Sprintf(
				"AI calls for %s reached their monthly limit of $%.2f (spent $%.2f). An admin can raise it under Settings → AI usage.",
				aiToolName(c.Task), float64(*c.TaskLimit)/100, float64(spent)/100)}
		}
	}
	if c.MonthlyLimit == nil {
		return nil
	}
	spent, err := s.aiMonthSpend(ctx, s.pool, ws)
	if err != nil {
		return err
	}
	if spent+estimate > *c.MonthlyLimit {
		return errAI{http.StatusConflict, "ai_budget_reached", fmt.Sprintf(
			"AI calls in this workspace reached the monthly limit of $%.2f (spent $%.2f). An admin can raise it under Settings → AI model.",
			float64(*c.MonthlyLimit)/100, float64(spent)/100)}
	}
	return nil
}

// recordAISpend books what a model call cost, on the workspace's own bill
// with the provider.
//
// Every call also goes in the AI usage log (ai_calls) with its tool, model
// and tokens, even one too cheap to reach a cent, and a call that takes
// the month past 80% or all of a limit warns the workspace's admins.
func recordAISpend(ctx context.Context, q jobs.Querier, c aiChoice, ws string, missionID, projectID *string, actorID *string, u llm.Usage, ref, note string) error {
	cents := c.Price.Cents(u)
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheRead == 0 && u.CacheWrite == 0 && cents <= 0 {
		return nil
	}
	fresh, err := recordAICall(ctx, q, c, ws, missionID, projectID, actorID, u, cents, ref)
	if err != nil || cents <= 0 {
		return err
	}
	if missionID != nil {
		ctx = missions.WithID(ctx, *missionID)
	}
	if _, _, err := outcomes.Record(ctx, q, outcomes.Entry{
		WorkspaceID: ws, ProjectID: projectID, MissionID: missionID,
		Category: outcomes.CategorySoftware, Provider: c.Provider.Vendor, IntegrationID: &c.IntegrationID,
		AmountCents: &cents, Currency: "USD", OccurredAt: time.Now(), Source: "auto", Ref: &ref,
		Note: note + " (" + u.Model + ")", ActorID: actorID,
	}); err != nil {
		return err
	}
	if !fresh {
		return nil
	}
	return warnAIBudget(ctx, q, ws, aiCallTask(c.Task))
}

// aiError writes err as a response and reports whether there was one.
func (s *Server) aiError(w http.ResponseWriter, r *http.Request, err error) bool {
	var e errAI
	switch {
	case err == nil:
		return false
	case errors.As(err, &e):
		writeError(w, r, e.status, e.code, e.msg)
	default:
		s.internal(w, r, err)
	}
	return true
}

// ---- The settings resource ----

type aiProviderView struct {
	Connector    string               `json:"connector"`
	Name         string               `json:"name"`
	DefaultModel *string              `json:"default_model"`
	LightModel   *string              `json:"light_model"`
	CheapModel   *string              `json:"cheap_model"`
	KeyURL       string               `json:"key_url"`
	PricingURL   string               `json:"pricing_url"`
	Connections  []aiConnectionView   `json:"connections"`
	KnownPrices  map[string]llm.Price `json:"known_prices"`
}

type aiConnectionView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type aiActiveView struct {
	Provider      string    `json:"provider"`
	ProviderName  string    `json:"provider_name"`
	IntegrationID string    `json:"integration_id"`
	Model         string    `json:"model"`
	Price         llm.Price `json:"price"`
	PriceSource   string    `json:"price_source"`
}

type aiTaskView struct {
	Task        string     `json:"task"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Model       *string    `json:"model"`
	Price       *llm.Price `json:"price"`
	PriceSource *string    `json:"price_source"`
	Custom      bool       `json:"custom"`
}

type aiProblemView struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (s *Server) aiSettingsView(ctx context.Context, ws string) (map[string]any, error) {
	row, err := s.loadAISettings(ctx, s.pool, ws)
	if err != nil {
		return nil, err
	}
	conns, err := s.aiConnections(ctx, ws)
	if err != nil {
		return nil, err
	}
	spent, err := s.aiMonthSpend(ctx, s.pool, ws)
	if err != nil {
		return nil, err
	}
	researchSpent, err := s.researchMonthSpend(ctx, ws)
	if err != nil {
		return nil, err
	}
	providers := []aiProviderView{}
	for _, p := range llm.Providers {
		v := aiProviderView{Connector: p.ID, Name: p.Name, KeyURL: p.KeyURL, PricingURL: p.PricingURL, Connections: []aiConnectionView{}, KnownPrices: map[string]llm.Price{}}
		if p.DefaultModel != "" {
			v.DefaultModel = &p.DefaultModel
		}
		if p.LightModel != "" {
			v.LightModel = &p.LightModel
		}
		if p.CheapModel != "" {
			v.CheapModel = &p.CheapModel
		}
		for _, c := range conns {
			if c.Connector == p.ID {
				v.Connections = append(v.Connections, aiConnectionView{ID: c.ID, Name: c.Name})
			}
		}
		for _, id := range knownModels(p) {
			v.KnownPrices[id], _ = llm.KnownPrice(p.ID, id)
		}
		providers = append(providers, v)
	}
	out := map[string]any{
		"integration_id":                         row.IntegrationID,
		"model":                                  row.Model,
		"input_cents_per_million":                row.InputCents,
		"output_cents_per_million":               row.OutputCents,
		"monthly_limit_cents":                    row.MonthlyLimit,
		"task_models":                            row.taskModels(),
		"tasks":                                  nil,
		"spent_this_month_cents":                 spent,
		"domain_research":                        row.Research,
		"domain_research_limit_cents":            row.ResearchLimit,
		"domain_research_spent_this_month_cents": researchSpent,
		"active":                                 nil,
		"problem":                                nil,
		"providers":                              providers,
		"updated_at":                             row.UpdatedAt,
	}
	conn, p, model, price, source, err := pickAI(row, conns)
	var e errAI
	switch {
	case errors.As(err, &e):
		out["problem"] = aiProblemView{Code: e.code, Message: e.msg}
	case err != nil:
		return nil, err
	}
	tasks := []aiTaskView{}
	for _, t := range aiTasks {
		v := aiTaskView{Task: t.ID, Name: t.Name, Description: t.Description}
		if model != "" {
			m, pr, src, custom := forTask(row, p, model, price, source, t.ID)
			v.Model, v.Price, v.PriceSource, v.Custom = &m, &pr, &src, custom
		}
		tasks = append(tasks, v)
	}
	out["tasks"] = tasks
	if model != "" {
		out["active"] = aiActiveView{Provider: p.ID, ProviderName: p.Name, IntegrationID: conn.ID, Model: model, Price: price, PriceSource: source}
	}
	return out, nil
}

// taskModels is the stored per-task choices, never null.
func (row aiSettingsRow) taskModels() map[string]taskModel {
	if row.Tasks == nil {
		return map[string]taskModel{}
	}
	return row.Tasks
}

func knownModels(p llm.Provider) []string {
	if p.ID != llm.Claude.ID {
		return nil
	}
	return []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5", "claude-fable-5-1"}
}

func (s *Server) getAISettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := s.aiSettingsView(r.Context(), a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

var modelID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@\-]{0,199}$`)

func (s *Server) putAISettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		IntegrationID *string  `json:"integration_id"`
		Model         string   `json:"model"`
		InputCents    *float64 `json:"input_cents_per_million"`
		OutputCents   *float64 `json:"output_cents_per_million"`
		MonthlyLimit  *int64   `json:"monthly_limit_cents"`
		// Omitted: keep the current choices. Given: replaces them; a task
		// left out, null or with no model uses its default.
		TaskModels *map[string]*taskModel `json:"task_models"`
		// Omitted leaves research as it is. A limit of 0 stops it.
		Research      *bool  `json:"domain_research"`
		ResearchLimit *int64 `json:"domain_research_limit_cents"`
	}
	if !decode(w, r, &req) {
		return
	}
	bad := func(msg string) { writeError(w, r, http.StatusBadRequest, "invalid_request", msg) }
	price := func(v *float64) bool { return v == nil || (*v > 0 && *v <= 1e7 && !math.IsNaN(*v)) }
	switch {
	case req.Model != "" && !modelID.MatchString(req.Model):
		bad("model is a model id as the provider names it, such as claude-opus-5-5")
		return
	case !price(req.InputCents) || !price(req.OutputCents):
		bad("input_cents_per_million and output_cents_per_million are US cents per million tokens, above 0")
		return
	case req.MonthlyLimit != nil && (*req.MonthlyLimit < 0 || *req.MonthlyLimit > 10_000_000):
		bad("monthly_limit_cents is between 0 and 10000000 (US$100,000), or null for no limit")
		return
	case req.ResearchLimit != nil && (*req.ResearchLimit < 0 || *req.ResearchLimit > 10_000_000):
		bad("domain_research_limit_cents is between 0 and 10000000 (US$100,000); 0 stops research")
		return
	case req.IntegrationID == nil && req.Model != "":
		bad("model needs integration_id: choose the connection the model belongs to")
		return
	}
	provider := ""
	var conns []connectors.Integration
	if req.IntegrationID != nil || req.TaskModels != nil {
		var err error
		if conns, err = s.aiConnections(r.Context(), a.WorkspaceID); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	if req.IntegrationID != nil {
		if !validUUID(*req.IntegrationID) {
			bad("integration_id is not a connected AI model in this workspace")
			return
		}
		i := slices.IndexFunc(conns, func(c connectors.Integration) bool { return c.ID == *req.IntegrationID })
		if i < 0 {
			bad("integration_id is not a connected AI model in this workspace (Claude, OpenAI, Gemini, Mistral, xAI, OpenRouter, Vercel AI Gateway or Requesty)")
			return
		}
		p, _ := llm.ProviderFor(conns[i].Connector)
		provider = p.ID
		model := req.Model
		if model == "" {
			model = p.DefaultModel
		}
		if model == "" {
			bad("model is required for " + p.Name + ": pick one from GET /integrations/{id}/models")
			return
		}
		if _, ok := llm.KnownPrice(p.ID, model); !ok && (req.InputCents == nil || req.OutputCents == nil) {
			bad("input_cents_per_million and output_cents_per_million are required for " + model + ": Outflare doesn't know its price, and needs it to keep to spend limits. See " + p.PricingURL)
			return
		}
	}
	// Task models belong to the provider the settings now point at: the
	// chosen connection's, else the first connected one's.
	var tasks []byte
	chosen := []string{}
	if req.TaskModels != nil {
		taskProvider := provider
		if taskProvider == "" && len(conns) > 0 {
			taskProvider = conns[0].Connector
		}
		p, _ := llm.ProviderFor(taskProvider)
		out := map[string]taskModel{}
		for id, t := range *req.TaskModels {
			switch {
			case !validTask(id):
				bad("task_models keys are plan, turn, check_in, reply_labelling, classify_domain, ai_visibility, social_replies or email_verification")
				return
			case t == nil || t.Model == "":
				continue
			case taskProvider == "":
				bad("connect an AI model before choosing models for tasks")
				return
			case !modelID.MatchString(t.Model):
				bad("task_models." + id + ".model is a model id as the provider names it, such as claude-sonnet-5-5")
				return
			case !price(t.InputCents) || !price(t.OutputCents):
				bad("task_models." + id + " prices are US cents per million tokens, above 0")
				return
			}
			entry := taskModel{Provider: p.ID, Model: t.Model}
			if _, ok := llm.KnownPrice(p.ID, t.Model); !ok {
				if t.InputCents == nil || t.OutputCents == nil {
					bad("task_models." + id + " needs input_cents_per_million and output_cents_per_million: Outflare doesn't know what " + t.Model + " costs. See " + p.PricingURL)
					return
				}
				entry.InputCents, entry.OutputCents = t.InputCents, t.OutputCents
			}
			out[id] = entry
			chosen = append(chosen, id)
		}
		slices.Sort(chosen)
		var err error
		if tasks, err = json.Marshal(out); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO workspace_ai_settings (workspace_id, integration_id, model, input_cents_per_m, output_cents_per_m, monthly_limit_cents,
				domain_research, domain_research_limit_cents, task_models, updated_at, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, COALESCE($8, false), COALESCE($9, 20000), COALESCE($10::jsonb, '{}'), now(), $7)
			ON CONFLICT (workspace_id) DO UPDATE SET integration_id = $2, model = $3, input_cents_per_m = $4, output_cents_per_m = $5,
				monthly_limit_cents = $6,
				domain_research = COALESCE($8, workspace_ai_settings.domain_research),
				domain_research_limit_cents = COALESCE($9, workspace_ai_settings.domain_research_limit_cents),
				task_models = COALESCE($10::jsonb, workspace_ai_settings.task_models),
				updated_at = now(), updated_by = $7`,
			a.WorkspaceID, req.IntegrationID, req.Model, req.InputCents, req.OutputCents, req.MonthlyLimit, a.ActorID,
			req.Research, req.ResearchLimit, tasks); err != nil {
			return err
		}
		data := map[string]any{"provider": provider, "model": req.Model, "monthly_limit_cents": req.MonthlyLimit,
			"domain_research": req.Research, "domain_research_limit_cents": req.ResearchLimit}
		if req.TaskModels != nil {
			data["task_models"] = chosen
		}

		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "ai_settings.updated", "workspace", a.WorkspaceID, data)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.getAISettings(w, r)
}

func (s *Server) listIntegrationModels(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "integration not found")
		return
	}
	o, err := s.integrations.Open(r.Context(), s.pool, a.WorkspaceID, id)
	if errors.Is(err, connectors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "integration not found")
		return
	}
	if err != nil {
		connectorError(w, r, err)
		return
	}
	if !models.IsModel(o.Integration.Connector) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "this integration is not an AI model; models are listed for AI model connections such as claude, openai or openrouter")
		return
	}
	list, err := models.Models(r.Context(), o)
	if err != nil {
		if errors.Is(err, llm.ErrAuth) {
			err = connectors.Errf(connectors.ErrAuth, "the provider did not accept the API key; reconnect it")
		} else {
			err = connectors.Errf(connectors.ErrUnavailable, "%s", err.Error())
		}
		connectorError(w, r, err)
		return
	}
	p, _ := llm.ProviderFor(o.Integration.Connector)
	type modelView struct {
		ID    string     `json:"id"`
		Name  string     `json:"name"`
		Price *llm.Price `json:"price"`
	}
	data := make([]modelView, 0, len(list))
	for _, m := range list {
		v := modelView{ID: m.ID, Name: m.Name}
		if price, ok := llm.KnownPrice(p.ID, m.ID); ok {
			v.Price = &price
		}
		data = append(data, v)
	}
	var def *string
	if p.DefaultModel != "" {
		def = &p.DefaultModel
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "default_model": def})
}
