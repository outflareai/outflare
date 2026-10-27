package outflare

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/llm"
	"github.com/outflareai/outflare/engine/internal/outcomes"
	"github.com/outflareai/outflare/engine/internal/usagelimits"
)

// Workspace features for extensions: routes behind the workspace check,
// the workspace's own connections and AI model, and its company records.
// Everything goes through the workspace's own keys (bring your own keys):
// an extension never brings one of its own.

// Caller is who is making a workspace request (HandleWorkspace).
type Caller = api.Caller

// HandleWorkspace adds a workspace route under the extension's own prefix,
// such as "POST /v1/workspaces/{ws}/cloud/lead-finder/runs". The engine checks the
// caller's sign-in or token, that they can see the workspace (404 if not),
// that they have scope, missions and idempotency, as for its own routes.
// Brand-limited callers are let in: keep them to their brands with
// Caller.Sees and Caller.Writes.
//
// It panics at start-up for a path outside /v1/workspaces/{ws}/ or an
// unknown scope, so a typo can't leave a route ungated.
func (h *Host) HandleWorkspace(pattern, scope string, fn func(w http.ResponseWriter, r *http.Request, c *Caller)) {
	path := pattern
	if _, p, ok := strings.Cut(pattern, " "); ok {
		path = strings.TrimSpace(p)
	}
	if !strings.HasPrefix(path, "/v1/workspaces/{ws}/") {
		panic("outflare: workspace route " + pattern + " must be under /v1/workspaces/{ws}/")
	}
	if !auth.ValidScope(scope) {
		panic("outflare: workspace route " + pattern + " has unknown scope " + scope)
	}
	h.workspaceRoutes = append(h.workspaceRoutes, api.WorkspaceRoute{Pattern: pattern, Scope: scope, Handler: fn})
}

// WriteError writes the engine's error body: {"error": {"code", "message",
// "request_id"}}. Codes come from the engine's catalog where one fits.
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	api.WriteError(w, r, status, code, message)
}

// WriteJSON writes v as JSON with a status.
func WriteJSON(w http.ResponseWriter, status int, v any) { api.WriteJSON(w, status, v) }

// RecordSystem writes an audit event done by Outflare itself (a job, a
// schedule) in tx. It is also the product event of the same name.
func RecordSystem(ctx context.Context, tx pgx.Tx, workspaceID, action, targetType, targetID string, data map[string]any) error {
	return audit.Record(ctx, tx, audit.Event{WorkspaceID: workspaceID, Action: action, TargetType: targetType, TargetID: targetID,
		ActorType: "system", Data: data})
}

// Company records.
type Company = customers.Company

// EnsureCompanies returns the company record for each domain, adding
// prospect companies (of brand, nil for shared) for the ones the workspace
// doesn't have. via says what found them, such as "lead_finder". It is for
// background jobs: workspaceID and brand must come from a row the job
// already holds for that workspace. Request handlers use
// EnsureCompaniesFor.
func EnsureCompanies(ctx context.Context, tx pgx.Tx, workspaceID string, brand *string, via string, companies []Company) (map[string]string, error) {
	return customers.EnsureCompanies(ctx, tx, workspaceID, brand, via, companies)
}

// EnsureCompaniesFor is EnsureCompanies in the caller's workspace, for a
// brand the caller may change (ErrNotYours otherwise).
func EnsureCompaniesFor(ctx context.Context, tx pgx.Tx, c *Caller, brand *string, via string, companies []Company) (map[string]string, error) {
	if !c.Writes(brand) {
		return nil, ErrNotYours
	}
	return customers.EnsureCompanies(ctx, tx, c.WorkspaceID, brand, via, companies)
}

// CompanyDomain is a website or domain as a company identity, or "".
func CompanyDomain(s string) string { return customers.CompanyDomain(s) }

// Connections: the workspace's own connected accounts.
type (
	WebQuery    = connectors.WebQuery
	WebResults  = connectors.WebResults
	WebResult   = connectors.WebResult
	FetchedPage = connectors.FetchedPage
	Cost        = connectors.SEOCost
)

// Capabilities Connections.Open finds.
const (
	CapWebSearch = connectors.CapWebSearch
	CapPageFetch = connectors.CapCrawlFetch
)

// Connector error kinds, from ConnectorErrorKind.
const (
	ConnectorAuth        = connectors.ErrAuth
	ConnectorQuota       = connectors.ErrQuota
	ConnectorUnavailable = connectors.ErrUnavailable
	ConnectorInvalid     = connectors.ErrInvalid
)

// ConnectorErrorKind sorts a connector error: auth, quota, unavailable,
// invalid, or "" for an error that isn't a connector's.
func ConnectorErrorKind(err error) string {
	var e *connectors.Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return ""
}

// ErrorCode is the engine's catalog code for an error from Connections or
// AI (connector_needed, connector_auth, connector_quota,
// connector_unavailable, connector_invalid, ai_model_needed,
// ai_budget_reached, usage_limit_reached), or "" when it has none.
func ErrorCode(err error) string {
	var coded interface{ Code() string }
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrNotConnected):
		return "connector_needed"
	case errors.Is(err, llm.ErrNoKey):
		return "ai_model_needed"
	case errors.As(err, &coded):
		return coded.Code()
	}
	if k := ConnectorErrorKind(err); k != "" {
		return "connector_" + k
	}
	return ""
}

// ErrNotConnected means the workspace has no connected account that can
// do what was asked. The feature stays off and says what to connect.
var ErrNotConnected = connectors.ErrNoCapable

// Connections opens the workspace's connected accounts. An extension never
// sees their credentials: a Connection only searches and reads pages, and
// books what each call cost in the workspace's spend ledger.
type Connections struct{ h *Host }

// Connections is the workspace connections API.
func (h *Host) Connections() *Connections { return &Connections{h} }

// Connection is one of the workspace's connected accounts.
type Connection struct {
	ID, WorkspaceID, Connector string

	o *connectors.Opened
	h *Host
}

// String and LogValue name the connection without anything secret, should
// it end up in a log.
func (c *Connection) String() string       { return "connection " + c.ID + " (" + c.Connector + ")" }
func (c *Connection) LogValue() slog.Value { return slog.StringValue(c.String()) }

// ErrNotYours means the caller may not spend on connections for that brand.
var ErrNotYours = errors.New("you can only use connections for your own brands")

// OpenFor returns the oldest connection with a capability (CapWebSearch,
// CapPageFetch) in the caller's workspace that serves brand (nil for
// shared work), or ErrNotConnected. A brand-limited caller only spends
// for its own brands (ErrNotYours).
func (cs *Connections) OpenFor(ctx context.Context, c *Caller, brand *string, capability string) (*Connection, error) {
	if !c.Writes(brand) {
		return nil, ErrNotYours
	}
	return cs.Open(ctx, c.WorkspaceID, brand, capability)
}

// Open is OpenFor for background jobs, which have no caller. workspaceID
// and brand must come from a row the job already holds for that workspace,
// never from input.
func (cs *Connections) Open(ctx context.Context, workspaceID string, brand *string, capability string) (*Connection, error) {
	if cs.h.store == nil {
		return nil, ErrNotConnected
	}
	b := ""
	if brand != nil {
		b = *brand
	}
	o, err := cs.h.store.OpenCapable(ctx, cs.h.Pool, workspaceID, b, capability)
	if err != nil {
		return nil, err
	}
	return &Connection{ID: o.ID, WorkspaceID: o.WorkspaceID, Connector: o.Connector.Definition().ID, o: o, h: cs.h}, nil
}

// Search runs a web search on the connection and books what it cost
// against ref (such as a run id).
func (c *Connection) Search(ctx context.Context, q WebQuery, ref string) (WebResults, error) {
	s, ok := c.o.Connector.(connectors.WebSearcher)
	if !ok {
		return WebResults{}, ErrNotConnected
	}
	if err := c.withinLimits(ctx); err != nil {
		return WebResults{}, err
	}
	res, err := s.WebSearch(ctx, c.o.Client, c.o.Creds, q)
	if serr := c.spent(ctx, res.Cost, ref, "Web search"); serr != nil && err == nil {
		err = serr
	}
	return res, err
}

// Fetch reads one page through the connection's own network, rendering
// JavaScript when asked, and books what it cost against ref.
func (c *Connection) Fetch(ctx context.Context, url string, renderJS bool, ref string) (FetchedPage, error) {
	f, ok := c.o.Connector.(connectors.PageFetcher)
	if !ok {
		return FetchedPage{}, ErrNotConnected
	}
	if err := c.withinLimits(ctx); err != nil {
		return FetchedPage{}, err
	}
	p, err := f.FetchPage(ctx, c.o.Client, c.o.Creds, url, renderJS)
	if serr := c.spent(ctx, p.Cost, ref, "Page read"); serr != nil && err == nil {
		err = serr
	}
	return p, err
}

// withinLimits refuses a call once the connection reached a usage limit
// that stops calls; ErrorCode gives usage_limit_reached.
func (c *Connection) withinLimits(ctx context.Context) error {
	return usagelimits.Check(ctx, c.h.Pool, c.WorkspaceID, c.ID, "", 0)
}

// spent books a call in the workspace's spend ledger: provider units
// (Geonode tokens) when the provider said, otherwise money.
func (c *Connection) spent(ctx context.Context, cost Cost, ref, note string) error {
	if cost.USD <= 0 && cost.Units <= 0 {
		return nil
	}
	id := c.ID
	e := outcomes.Entry{WorkspaceID: c.WorkspaceID, Category: outcomes.CategoryData, Provider: c.Connector,
		IntegrationID: &id, OccurredAt: time.Now(), Source: outcomes.SourceAuto, Note: note}
	if ref != "" {
		e.Ref = &ref
	}
	if cost.Units > 0 {
		u := cost.Units
		e.Units, e.UnitName = &u, "token"
	} else {
		cents := int64(math.Ceil(cost.USD * 100))
		e.AmountCents, e.Currency = &cents, "USD"
	}
	if cost.Estimated {
		e.Note += ", estimated from the price list"
	}
	_, _, err := outcomes.Record(ctx, c.h.Pool, e)
	return err
}

// The workspace's AI model.
type (
	AIRequest  = llm.Request
	AIMessage  = llm.Message
	AIUsage    = llm.Usage
	AIResponse = llm.Response
)

// ErrNoAIKey means the workspace has not connected an AI provider.
var ErrNoAIKey = llm.ErrNoKey

// AIStopRefusal is AIResponse.Stop when the model refused.
const AIStopRefusal = llm.StopRefusal

// AI calls the workspace's own AI model and books what it spends, under
// the workspace's monthly AI limit. The model and its key stay inside the
// engine.
type AI struct{ h *Host }

// AI is the workspace AI API. It works once the engine has started.
func (h *Host) AI() *AI { return &AI{h} }

// ChatFor sends req to the model the caller's workspace chose for task (a
// model task name, so it can pick a cheaper one), refusing when it has no
// AI connected or the month's AI limit would not cover about maxCents. It
// books the call against ref and returns the answer and what it cost in
// cents.
func (a *AI) ChatFor(ctx context.Context, c *Caller, task string, req AIRequest, maxCents int64, ref string) (AIResponse, int64, error) {
	return a.Chat(ctx, c.WorkspaceID, task, req, maxCents, ref)
}

// Chat is ChatFor for background jobs, which have no caller. workspaceID
// must come from a row the job already holds for that workspace, never
// from input.
func (a *AI) Chat(ctx context.Context, workspaceID, task string, req AIRequest, maxCents int64, ref string) (AIResponse, int64, error) {
	if a.h.server == nil {
		return AIResponse{}, 0, errors.New("the engine has not started")
	}
	m := a.h.server.AIModels()
	c, err := m.ForTask(ctx, workspaceID, task, maxCents)
	if err != nil {
		return AIResponse{}, 0, err
	}
	resp, err := c.Model.Chat(ctx, req)
	cents := int64(math.Ceil((float64(resp.Usage.InputTokens)*c.Price.Input + float64(resp.Usage.OutputTokens)*c.Price.Output) / 1e6))
	if serr := m.Spent(ctx, workspaceID, c, resp.Usage, ref, task); serr != nil && err == nil {
		err = serr
	}
	return resp, cents, err
}
