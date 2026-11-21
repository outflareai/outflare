package outflare

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/classify"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/guardsync"
	"github.com/outflareai/outflare/engine/internal/llm"
	"github.com/outflareai/outflare/engine/internal/outcomes"
	"github.com/outflareai/outflare/engine/internal/research"
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

// HandleBrand adds a brand route under the extension's own prefix, such
// as "POST /v1/brands/{brand}/cloud/lead-finder/runs". The engine finds the
// brand's workspace and checks the caller as HandleWorkspace does, answers
// 404 for a brand the caller can't see, and sets Caller.Brand. Keep what
// the route reads and writes to that brand.
//
// It panics at start-up for a path outside /v1/brands/{brand}/ or an
// unknown scope.
func (h *Host) HandleBrand(pattern, scope string, fn func(w http.ResponseWriter, r *http.Request, c *Caller)) {
	path := pattern
	if _, p, ok := strings.Cut(pattern, " "); ok {
		path = strings.TrimSpace(p)
	}
	if !strings.HasPrefix(path, "/v1/brands/{brand}/") {
		panic("outflare: brand route " + pattern + " must be under /v1/brands/{brand}/")
	}
	if !auth.ValidScope(scope) {
		panic("outflare: brand route " + pattern + " has unknown scope " + scope)
	}
	h.workspaceRoutes = append(h.workspaceRoutes, api.WorkspaceRoute{Pattern: pattern, Scope: scope, Handler: fn, Brand: true})
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

// Missions an extension starts for the caller (Caller.StartMission): a
// loop is an agent inside a mission, so it passes the API's gate, limits
// and audit log, and waits for a person's approval like any mission.
type MissionDraft = api.MissionDraft

// ErrMissionInvalid and ErrMissionDenied sort Caller.StartMission's errors
// (errors.Is): a 400 and a 403, with a message to show.
var (
	ErrMissionInvalid = api.ErrMissionInvalid
	ErrMissionDenied  = api.ErrMissionDenied
	// ErrMissionRetired is what StartMission returns now: missions are
	// retired, and a loop starts an agent run (StartAgentRun) instead.
	ErrMissionRetired = api.ErrMissionRetired
)

// Agent runs an extension starts for the caller (Caller.StartAgentRun): a
// loop's brief carried out by Outflare's agent on the action layer, as a
// playbook run is. It replaces MissionDraft for new loops.
type AgentRunDraft = api.AgentRunDraft

// ErrAgentRunInvalid and ErrAgentRunDenied sort Caller.StartAgentRun's
// errors into 400 and 403.
var (
	ErrAgentRunInvalid = api.ErrAgentRunInvalid
	ErrAgentRunDenied  = api.ErrAgentRunDenied
)

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

// RecordField is a field an extension keeps on people or companies, such
// as what a tool learned about them. Type is text, number, url or
// timestamp.
type RecordField = customers.Field

// ErrBadRecordField is a RecordField the catalog can't hold.
var ErrBadRecordField = customers.ErrBadField

// SetRecordFields writes values (by field key) onto a record in tx, as
// source (such as "enrichment"), making each field in the record's brand
// the first time. It never overwrites a value a source ahead of it in the
// field's precedence set (a person's own edit, first-party data), keeps
// each change in the record's history and records record.updated. It is
// for background jobs: workspaceID and recordID must come from rows the
// job already holds for that workspace. It reports whether anything
// changed.
func SetRecordFields(ctx context.Context, tx pgx.Tx, workspaceID, recordID, source string, fields []RecordField, values map[string]any) (bool, error) {
	return customers.SetFields(ctx, tx, workspaceID, recordID, source, fields, values)
}

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

// ConnectorBusy says whether err is a connected service that had no room
// for the call right now (a rate limit answer, or its capacity in use) and
// charged nothing, and how long it said to wait (zero when it didn't). It
// is never the account's quota: nothing needs fixing, and the same call
// goes through a little later. Its ConnectorErrorKind is unavailable.
func ConnectorBusy(err error) (time.Duration, bool) {
	return connectors.IsBusy(err)
}

// BusyError is the error of a connected service with no room for a call
// right now (ConnectorBusy), asking for retryAfter: for an extension's own
// provider client, and for fakes in tests.
func BusyError(retryAfter time.Duration, msg string) error {
	return connectors.Throttledf(retryAfter, "%s", msg)
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

// OpenAll opens every connection of the workspace with a capability that
// serves brand (nil for shared work), for a feature that can use any of
// them: where Open takes only the oldest, a provider that is out of
// credits must not hide another that works. The connections whose
// connector id is in prefer come first, in prefer's order, then the rest;
// within each, the oldest first. A connection that can't be opened (a
// sign-in that expired) is left out and logged; when none can, the first
// one's error is returned. With none connected it is ErrNotConnected.
//
// It is for background jobs, as Open is: workspaceID and brand must come
// from a row the job already holds for that workspace, never from input.
func (cs *Connections) OpenAll(ctx context.Context, workspaceID string, brand *string, capability string, prefer ...string) ([]*Connection, error) {
	if cs.h.store == nil {
		return nil, ErrNotConnected
	}
	b := ""
	if brand != nil {
		b = *brand
	}
	list, err := cs.h.store.ListCapable(ctx, cs.h.Pool, workspaceID, b, capability)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotConnected
	}
	// A stable sort keeps the oldest first within each rank.
	rank := func(connector string) int {
		for i, p := range prefer {
			if p == connector {
				return i
			}
		}
		return len(prefer)
	}
	sort.SliceStable(list, func(i, j int) bool { return rank(list[i].Connector) < rank(list[j].Connector) })
	var out []*Connection
	var first error
	for _, i := range list {
		o, err := cs.h.store.Open(ctx, cs.h.Pool, workspaceID, i.ID)
		if err != nil {
			if first == nil {
				first = err
			}
			cs.h.logger().WarnContext(ctx, "a connection could not be opened and is left out", "workspace_id", workspaceID,
				"integration_id", i.ID, "connector", i.Connector, "capability", capability, "err", err)
			continue
		}
		out = append(out, &Connection{ID: o.ID, WorkspaceID: o.WorkspaceID, Connector: o.Connector.Definition().ID, o: o, h: cs.h})
	}
	if len(out) == 0 {
		return nil, first
	}
	return out, nil
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

// DNS: the workspace's own DNS host (Cloudflare), connected by the
// workspace. An extension adds records through it without seeing its
// credentials.
const CapDNSWrite = connectors.CapDNSWrite

type (
	DNSRecord = connectors.DNSRecord
	DNSResult = connectors.DNSResult
)

// EnsureDNSRecords adds records on the connection's DNS host. It only adds:
// a record already there is left as it is, and none is changed or removed.
func (c *Connection) EnsureDNSRecords(ctx context.Context, records []DNSRecord) (DNSResult, error) {
	d, ok := c.o.Connector.(connectors.DNSHost)
	if !ok {
		return DNSResult{}, ErrNotConnected
	}
	return d.EnsureRecords(ctx, c.o.Client, c.o.Creds, records)
}

// People search: the workspace's own people-data account (Apollo, PDL)
// finds people at companies. Outflare keeps no contact database of its own.
const CapSearchPeople = connectors.CapSearchPeople

type (
	PeopleFilter = connectors.PeopleFilter
	FoundPerson  = connectors.Person
	PeoplePage   = connectors.SearchPage
)

// SearchPeople returns the first page (up to size people) of a people
// search on the connection, and books the credits it cost against ref.
func (c *Connection) SearchPeople(ctx context.Context, f PeopleFilter, size int, ref string) (PeoplePage, error) {
	s, ok := c.o.Connector.(connectors.PeopleSearcher)
	if !ok {
		return PeoplePage{}, ErrNotConnected
	}
	if err := usagelimits.Check(ctx, c.h.Pool, c.WorkspaceID, c.ID, "credit", 0); err != nil {
		return PeoplePage{}, err
	}
	page, err := s.SearchPeople(ctx, c.o.Client, c.o.Creds, f, "", size)
	if page.Credits > 0 {
		id, credits := c.ID, page.Credits
		e := outcomes.Entry{WorkspaceID: c.WorkspaceID, Category: outcomes.CategoryData, Provider: c.Connector,
			IntegrationID: &id, OccurredAt: time.Now(), Source: outcomes.SourceAuto, Note: "People search", Units: &credits, UnitName: "credit"}
		if ref != "" {
			e.Ref = &ref
		}
		if _, _, serr := outcomes.Record(ctx, c.h.Pool, e); serr != nil && err == nil {
			err = serr
		}
	}
	return page, err
}

// A company's LinkedIn page: the workspace's own provider (Blitz) finds it
// from the company's domain, and can then search that page for people
// without looking it up again.
const CapCompanyPage = connectors.CapCompanyPage

// CompanyLinkedInPage is a company's page on LinkedIn, as
// Connection.CompanyPage found it.
type CompanyLinkedInPage = connectors.CompanyLinkedInPage

// CompanyPage finds the LinkedIn page of the company at domain on the
// connection and books the credits it cost against ref. Found is false
// when the provider knows no page, which is an answer and not an error.
// As everywhere here, a ref is booked once in a workspace: give each call
// that can cost a ref of its own (a run id with the step and the company),
// or its charge is taken for a repeat of the first and left out.
func (c *Connection) CompanyPage(ctx context.Context, domain, ref string) (CompanyLinkedInPage, error) {
	l, ok := c.o.Connector.(connectors.CompanyLocator)
	if !ok {
		return CompanyLinkedInPage{}, ErrNotConnected
	}
	if err := usagelimits.Check(ctx, c.h.Pool, c.WorkspaceID, c.ID, "credit", 0); err != nil {
		return CompanyLinkedInPage{}, err
	}
	page, err := l.CompanyPage(ctx, c.o.Client, c.o.Creds, domain)
	if serr := c.credits(ctx, page.Credits, ref, "Company page lookup"); serr != nil && err == nil {
		err = serr
	}
	return page, err
}

// SearchPeopleAt is SearchPeople at one company whose LinkedIn page is
// already known (from CompanyPage, on this connection or another): a
// connector that can search the page does only that, and the page is not
// looked up or paid for again. Otherwise, or when page.Found is false, it
// is SearchPeople with f's company set to page.Domain. Only f's job titles
// and seniorities matter for a page search. The credits are booked against
// ref.
func (c *Connection) SearchPeopleAt(ctx context.Context, page CompanyLinkedInPage, f PeopleFilter, size int, ref string) (PeoplePage, error) {
	s, ok := c.o.Connector.(connectors.CompanyPageSearcher)
	if !ok || !page.Found {
		f.CompanyDomains = []string{page.Domain}
		return c.SearchPeople(ctx, f, size, ref)
	}
	if err := usagelimits.Check(ctx, c.h.Pool, c.WorkspaceID, c.ID, "credit", 0); err != nil {
		return PeoplePage{}, err
	}
	found, err := s.SearchCompanyPage(ctx, c.o.Client, c.o.Creds, page, f, size)
	if serr := c.credits(ctx, found.Credits, ref, "People search"); serr != nil && err == nil {
		err = serr
	}
	return found, err
}

// logger is the host's log, or the default one before it has any.
func (h *Host) logger() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.Default()
}

// credits books provider credits a call cost in the workspace's spend
// ledger, against ref. A ref is booked once in a workspace: a second
// charge with a ref already in the ledger changes nothing, so a retried
// job never counts twice, and each call that costs needs a ref of its own.
func (c *Connection) credits(ctx context.Context, credits float64, ref, note string) error {
	if credits <= 0 {
		return nil
	}
	id := c.ID
	e := outcomes.Entry{WorkspaceID: c.WorkspaceID, Category: outcomes.CategoryData, Provider: c.Connector,
		IntegrationID: &id, OccurredAt: time.Now(), Source: outcomes.SourceAuto, Note: note, Units: &credits, UnitName: "credit"}
	if ref != "" {
		e.Ref = &ref
	}
	_, _, err := outcomes.Record(ctx, c.h.Pool, e)
	return err
}

// Company search: the workspace's own data provider (Apollo, PDL) finds
// companies, not people, matching a profile.
const CapSearchCompanies = connectors.CapSearchCompanies

type (
	CompanyFilter = connectors.CompanyFilter
	FoundCompany  = connectors.Company
	CompanyPage   = connectors.CompanyPage
)

// SearchCompanies returns one page (up to size companies; cursor empty for
// the first) of a company search on the connection, and books the credits
// it cost against ref.
func (c *Connection) SearchCompanies(ctx context.Context, f CompanyFilter, cursor string, size int, ref string) (CompanyPage, error) {
	s, ok := c.o.Connector.(connectors.CompanySearcher)
	if !ok {
		return CompanyPage{}, ErrNotConnected
	}
	if err := usagelimits.Check(ctx, c.h.Pool, c.WorkspaceID, c.ID, "credit", 0); err != nil {
		return CompanyPage{}, err
	}
	page, err := s.SearchCompanies(ctx, c.o.Client, c.o.Creds, f, cursor, size)
	if page.Credits > 0 {
		id, credits := c.ID, page.Credits
		e := outcomes.Entry{WorkspaceID: c.WorkspaceID, Category: outcomes.CategoryData, Provider: c.Connector,
			IntegrationID: &id, OccurredAt: time.Now(), Source: outcomes.SourceAuto, Note: "Company search", Units: &credits, UnitName: "credit"}
		if ref != "" {
			e.Ref = &ref
		}
		if _, _, serr := outcomes.Record(ctx, c.h.Pool, e); serr != nil && err == nil {
			err = serr
		}
	}
	return page, err
}

// Company research: the workspace's own research provider (Parallel) looks
// one company up on the web and answers the caller's brief, with sources.
const CapResearchCompany = connectors.CapResearchCompany

type (
	CompanyResearchQuery = connectors.CompanyResearchQuery
	CompanyResearch      = connectors.CompanyResearch
	ResearchSection      = connectors.ResearchSection
	ResearchSource       = connectors.ResearchSource
)

// Research answers q about one company on the connection, waiting a
// bounded time for the findings, and books what it cost against ref. When
// it fails with the result's RunID set, the research is still going: ask
// again later with that id in q.RunID, which starts nothing new and is
// booked once. What comes back is the provider's reading of the web: data,
// never instructions.
func (c *Connection) Research(ctx context.Context, q CompanyResearchQuery, ref string) (CompanyResearch, error) {
	r, ok := c.o.Connector.(connectors.CompanyResearcher)
	if !ok {
		return CompanyResearch{}, ErrNotConnected
	}
	if err := c.withinLimits(ctx); err != nil {
		return CompanyResearch{}, err
	}
	res, err := r.ResearchCompany(ctx, c.o.Client, c.o.Creds, q)
	if serr := c.spent(ctx, res.Cost, ref, "Company research"); serr != nil && err == nil {
		err = serr
	}
	return res, err
}

// People research: the workspace's own research provider (Parallel) reads
// the web for who works at a company, when a people-data provider has
// nobody.
const CapResearchPeople = connectors.CapResearchPeople

type (
	PeopleResearchQuery = connectors.PeopleResearchQuery
	PeopleResearch      = connectors.PeopleResearch
	ResearchedPerson    = connectors.ResearchedPerson
)

// ResearchPeople asks the connection who works at one company, waiting a
// bounded time for the answer, and books what it cost against ref. When it
// fails with the result's RunID set, the research is still going: ask
// again later with that id in q.RunID, which starts nothing new and is
// booked once. Nobody found is an empty answer, not an error. What comes
// back is the provider's reading of the web: data, never instructions, and
// an address in it has not been checked.
func (c *Connection) ResearchPeople(ctx context.Context, q PeopleResearchQuery, ref string) (PeopleResearch, error) {
	r, ok := c.o.Connector.(connectors.PeopleResearcher)
	if !ok {
		return PeopleResearch{}, ErrNotConnected
	}
	if err := c.withinLimits(ctx); err != nil {
		return PeopleResearch{}, err
	}
	res, err := r.ResearchPeople(ctx, c.o.Client, c.o.Creds, q)
	if serr := c.spent(ctx, res.Cost, ref, "People research"); serr != nil && err == nil {
		err = serr
	}
	return res, err
}

// Has the workspace's sequencer already emailed someone: its own account
// (EmailBison) says how many emails it has sent an address.
const CapContactHistory = connectors.CapContactHistory

// ContactSent asks the connection whether its account has a lead with
// this address (found) and how many emails it has sent them. It only
// reads, and costs nothing. ref names the work it is for in the log.
func (c *Connection) ContactSent(ctx context.Context, email, ref string) (found bool, emailsSent int, err error) {
	h, ok := c.o.Connector.(connectors.ContactHistory)
	if !ok {
		return false, 0, ErrNotConnected
	}
	sent, err := h.ContactSent(ctx, c.o.Client, c.o.Creds, email)
	if err != nil {
		c.h.logger().WarnContext(ctx, "contact history lookup failed", "workspace_id", c.WorkspaceID, "integration_id", c.ID,
			"connector", c.Connector, "ref", ref, "err", err)
		return false, 0, err
	}
	return sent.Found, sent.EmailsSent, nil
}

// May an address be contacted: the workspace's own outside sources (its
// product's account lookup, a shared outreach blocklist) say whether
// someone is already a customer or is blocked.
const CapContactGuard = connectors.CapContactGuard

type (
	GuardedContact = connectors.GuardedContact
	ContactVerdict = connectors.ContactVerdict
)

// What a guard says about a contact (ContactVerdict.Status), and why it
// blocked one (ContactVerdict.Reason).
const (
	GuardClear       = connectors.GuardClear
	GuardBlocked     = connectors.GuardBlocked
	GuardUnknown     = connectors.GuardUnknown
	GuardCustomer    = connectors.GuardCustomer
	GuardBlocklisted = connectors.GuardBlocklisted
)

// ContactGuard is one of the workspace's connections that says who may be
// contacted.
type ContactGuard struct {
	// ID is the connection and Connector its connector's id.
	ID, WorkspaceID, Connector string

	conn *Connection
	err  error
	h    *Host
}

// ContactGuards returns every guard connection of the workspace that
// serves brand (nil for shared work): the connectors named in prefer
// first, in that order, then the rest, the oldest first within each.
// Empty, with no error, when the workspace has connected none; the
// feature then has no outside source to ask.
//
// Unlike OpenAll it leaves no connection out. A guard whose key was
// refused at its last check, or that can't be opened, is still returned
// and its Check fails: a guard that can't answer clears nobody, so a
// broken one must hold people, never let them through.
//
// It is for background jobs, as Open is: workspaceID and brand must come
// from a row the job already holds for that workspace, never from input.
func (cs *Connections) ContactGuards(ctx context.Context, workspaceID string, brand *string, prefer ...string) ([]*ContactGuard, error) {
	if cs.h.store == nil {
		return nil, nil
	}
	b := ""
	if brand != nil {
		b = *brand
	}
	list, err := cs.h.store.ListGuards(ctx, cs.h.Pool, workspaceID, b, connectors.CapContactGuard)
	if err != nil {
		return nil, err
	}
	rank := func(connector string) int {
		for i, p := range prefer {
			if p == connector {
				return i
			}
		}
		return len(prefer)
	}
	sort.SliceStable(list, func(i, j int) bool { return rank(list[i].Connector) < rank(list[j].Connector) })
	out := make([]*ContactGuard, 0, len(list))
	for _, i := range list {
		g := &ContactGuard{ID: i.ID, WorkspaceID: workspaceID, Connector: i.Connector, h: cs.h}
		o, err := cs.h.store.Open(ctx, cs.h.Pool, workspaceID, i.ID)
		if err != nil {
			g.err = err
			cs.h.logger().WarnContext(ctx, "a contact guard could not be opened: it clears nobody", "workspace_id", workspaceID,
				"integration_id", i.ID, "connector", i.Connector, "err", err)
		} else {
			g.conn = &Connection{ID: o.ID, WorkspaceID: o.WorkspaceID, Connector: i.Connector, o: o, h: cs.h}
		}
		out = append(out, g)
	}
	return out, nil
}

// Check asks the guard about contacts and returns one verdict for each,
// in the same order, whatever happens. It only reads, and costs nothing.
// Only GuardClear lets a person through. When the guard could not be
// asked, or a call to it failed, the contacts concerned are GuardUnknown
// and the failure is returned beside the verdicts: GuardFailure reads its
// code and how long the service asked to be left alone. A refused key
// marks the connection so the Connectors page shows it. ref names the
// work it is for in the log.
func (g *ContactGuard) Check(ctx context.Context, contacts []GuardedContact, ref string) ([]ContactVerdict, error) {
	unknown := func(detail string) []ContactVerdict {
		out := make([]ContactVerdict, len(contacts))
		for i, ct := range contacts {
			out[i] = ContactVerdict{Email: ct.Email, Status: GuardUnknown, Detail: detail}
		}
		return out
	}
	if g.conn == nil {
		err := g.err
		if err == nil {
			err = ErrNotConnected
		}
		return unknown("connection_unavailable"), err
	}
	cg, ok := g.conn.o.Connector.(connectors.ContactGuard)
	if !ok {
		return unknown("connection_unavailable"), ErrNotConnected
	}
	out, err := cg.CheckContacts(ctx, g.conn.o.Client, g.conn.o.Creds, contacts)
	if len(out) != len(contacts) {
		// A guard that breaks its own promise clears nobody.
		if err == nil {
			err = fmt.Errorf("outflare: the %s guard answered for %d of %d contacts", g.Connector, len(out), len(contacts))
		}
		out = unknown("internal")
	}
	if err != nil {
		code, _ := connectors.GuardFailure(err)
		g.conn.h.logger().WarnContext(ctx, "a contact guard failed: the people asked about are held", "workspace_id", g.WorkspaceID,
			"integration_id", g.ID, "connector", g.Connector, "ref", ref, "code", code, "contacts", len(contacts), "err", err)
		if connectors.KindOf(err) == connectors.ErrAuth && g.conn.h.store != nil {
			_ = g.conn.h.store.Checked(ctx, g.conn.h.Pool, g.WorkspaceID, g.ID, nil, err)
		}
	}
	return out, err
}

// CompanyFromList answers for a company, by its domain, from the copy of
// the guard's whole list the workspace keeps in its opt-out list (the
// guard's list sync), with no call to the guard: GuardBlocked with the
// guard's reason when the domain, or a domain it is under, is on it, else
// GuardClear. fresh is false when the copy can't stand in for the guard:
// the guard's list can't be read, its sync is off, has not read the whole
// list yet or has not succeeded within twice its interval, or the domain
// is one the copy says nothing about (a mailbox provider's). The caller
// then asks the guard itself (Check). It only reads, and costs nothing.
func (g *ContactGuard) CompanyFromList(ctx context.Context, domain string) (v ContactVerdict, fresh bool, err error) {
	v = ContactVerdict{Status: GuardUnknown}
	if g.h == nil || g.h.store == nil {
		return v, false, nil
	}
	info, ok := guardsync.Info(g.h.store.Registry, g.Connector)
	if !ok {
		return v, false, nil
	}
	fresh, err = guardsync.Fresh(ctx, g.h.Pool, g.h.store.Registry, g.WorkspaceID, g.ID, g.Connector, time.Now())
	if err != nil || !fresh {
		return v, false, err
	}
	listed, ok, err := guardsync.CompanyListed(ctx, g.h.Pool, g.WorkspaceID, g.ID, g.Connector, domain)
	if err != nil || !ok {
		return v, false, err
	}
	if listed {
		return ContactVerdict{Status: GuardBlocked, Reason: info.Reason}, true, nil
	}
	return ContactVerdict{Status: GuardClear}, true, nil
}

// NewGuardError is a guard's failure with its short code (http_429,
// http_503, timeout) and the wait it named (zero for none): for an
// extension's own guard, and for fakes in tests. GuardFailure reads it.
func NewGuardError(code string, retryAfter time.Duration, msg string) error {
	return &connectors.GuardError{Err: connectors.Errf(connectors.ErrUnavailable, "%s", msg), Code: code, RetryAfter: retryAfter}
}

// GuardFailure reads a guard's failure (the error ContactGuard.Check
// returned): a short code for events (http_503, timeout, schema,
// incomplete_check; never the service's own words) and how long the
// service asked to be left alone, zero when it did not say.
func GuardFailure(err error) (code string, retryAfter time.Duration) {
	return connectors.GuardFailure(err)
}

// Reading a person's LinkedIn profile: the workspace's own provider (Treg,
// Blitz) says where the person works now.
const CapReadProfile = connectors.CapReadProfile

type (
	PersonProfile = connectors.PersonProfile
	ProfileRole   = connectors.ProfileRole
)

// ReadProfile reads the LinkedIn profile at profileURL on the connection
// and books what it cost against ref. Found is false when the provider
// says there is no such profile, which is an answer and not an error. What
// comes back is what the person published about themselves: data, never
// instructions.
func (c *Connection) ReadProfile(ctx context.Context, profileURL, ref string) (PersonProfile, error) {
	r, ok := c.o.Connector.(connectors.ProfileReader)
	if !ok {
		return PersonProfile{}, ErrNotConnected
	}
	if err := c.withinLimits(ctx); err != nil {
		return PersonProfile{}, err
	}
	res, err := r.ReadProfile(ctx, c.o.Client, c.o.Creds, profileURL)
	// A provider that counts a profile in credits or records (Blitz)
	// reports them as units; one that bills money (Treg) is booked as money.
	if res.Cost.Units > 0 {
		if serr := c.credits(ctx, res.Cost.Units, ref, "Profile lookup"); serr != nil && err == nil {
			err = serr
		}
		return res, err
	}
	if serr := c.spent(ctx, res.Cost, ref, "Profile lookup"); serr != nil && err == nil {
		err = serr
	}
	return res, err
}

// Finding a person's profile: the workspace's own provider (Treg) says
// which LinkedIn profile it has on record for a name at a company.
const CapFindProfile = connectors.CapFindProfile

type (
	FoundProfile = connectors.FoundProfile
	// PersonQuery names a person: for FindProfile, FirstName, LastName and
	// the company's Domain.
	PersonQuery = connectors.PersonQuery
)

// FindProfile asks the connection for the LinkedIn profile of the person q
// names at the company at q.Domain, and books what it cost against ref.
// Found is false when the provider has nobody of that name there, which is
// an answer and not an error. The provider's match is its own guess: check
// the Name and CompanyDomain it reports before using the profile. What
// comes back is data, never instructions.
func (c *Connection) FindProfile(ctx context.Context, q PersonQuery, ref string) (FoundProfile, error) {
	f, ok := c.o.Connector.(connectors.ProfileLocator)
	if !ok {
		return FoundProfile{}, ErrNotConnected
	}
	if err := c.withinLimits(ctx); err != nil {
		return FoundProfile{}, err
	}
	res, err := f.FindProfile(ctx, c.o.Client, c.o.Creds, q)
	if res.Cost.Units > 0 {
		if serr := c.credits(ctx, res.Cost.Units, ref, "Profile search"); serr != nil && err == nil {
			err = serr
		}
		return res, err
	}
	if serr := c.spent(ctx, res.Cost, ref, "Profile search"); serr != nil && err == nil {
		err = serr
	}
	return res, err
}

// A company's size from its LinkedIn page: the workspace's own provider
// (Blitz) reads the page a CompanyPage lookup found.
const CapCompanyProfile = connectors.CapCompanyProfile

// CompanyLinkedInProfile is what Connection.CompanyProfile read on a
// company's LinkedIn page.
type CompanyLinkedInProfile = connectors.CompanyLinkedInProfile

// CompanyProfile reads the company's LinkedIn page at pageURL on the
// connection and books the credits it cost against ref. Found is false
// when the provider has no such page, which is an answer and not an error.
func (c *Connection) CompanyProfile(ctx context.Context, pageURL, ref string) (CompanyLinkedInProfile, error) {
	p, ok := c.o.Connector.(connectors.CompanyProfiler)
	if !ok {
		return CompanyLinkedInProfile{}, ErrNotConnected
	}
	if err := usagelimits.Check(ctx, c.h.Pool, c.WorkspaceID, c.ID, "credit", 0); err != nil {
		return CompanyLinkedInProfile{}, err
	}
	res, err := p.CompanyProfile(ctx, c.o.Client, c.o.Creds, pageURL)
	if serr := c.credits(ctx, res.Credits, ref, "Company size lookup"); serr != nil && err == nil {
		err = serr
	}
	return res, err
}

// RoleAddress reports whether an address is a role or shared inbox, such
// as info@, contact@ or sales@, rather than one person's.
func RoleAddress(email string) bool {
	local, _, ok := strings.Cut(strings.TrimSpace(email), "@")
	return ok && classify.RoleLocal(local)
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
	// AIImage is a picture in a message or a tool result; AIToolResult
	// answers a tool call the model made.
	AIImage      = llm.Image
	AIToolResult = llm.ToolResult
)

// AISupportsImages reports whether a provider reads images.
func AISupportsImages(provider string) bool { return llm.SupportsImages(provider) }

// AICheckImage reports why an image can't be sent to a model, or nil.
func AICheckImage(img AIImage) error { return llm.CheckImage(img) }

// AICapabilities is what a provider's chat API does through Outflare.
type AICapabilities = llm.Capabilities

// AICapabilitiesOf reports what provider's API does with model, so a
// feature offers only what will work (tools, images, streaming, search).
func AICapabilitiesOf(provider, model string) AICapabilities {
	return llm.CapabilitiesOf(provider, model)
}

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

// AIConnection is one of the workspace's connected AI model connections.
// DefaultModel is the provider's own default; Searches says whether that
// model can search the web before it answers (AISearchesWeb).
type AIConnection struct {
	ID, Provider, ProviderName, Name, DefaultModel string
	Searches                                       bool
}

// AISearchesWeb reports whether a provider's model can search the web
// before it answers, returning the pages it used as AIResponse.Citations:
// Claude's web search, OpenRouter's web plugin (any model) or OpenAI's
// search models.
func AISearchesWeb(provider, model string) bool { return llm.SupportsWebSearch(provider, model) }

// Connections lists the workspace's connected AI model connections, for a
// feature that calls several of them rather than the one the workspace
// chose. workspaceID must come from a row the caller holds for that
// workspace.
func (a *AI) Connections(ctx context.Context, workspaceID string) ([]AIConnection, error) {
	if a.h.server == nil {
		return nil, errors.New("the engine has not started")
	}
	conns, err := a.h.server.AIConnectionsOf(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]AIConnection, len(conns))
	for i, c := range conns {
		out[i] = AIConnection{ID: c.ID, Provider: c.Provider.ID, ProviderName: c.Provider.Name, Name: c.Name,
			DefaultModel: c.Provider.DefaultModel, Searches: llm.SupportsWebSearch(c.Provider.ID, c.Provider.DefaultModel)}
	}
	return out, nil
}

// ChatOn is Chat on one of the workspace's AI connections and a model of
// the caller's choosing (empty: the workspace's model on that connection,
// else the provider's default), under task's and the workspace's monthly
// AI limits. A model whose price Outflare doesn't know is refused, except
// on a router that reports what each call cost. The cents include any web
// searches the model ran.
func (a *AI) ChatOn(ctx context.Context, workspaceID, task, integrationID, model string, req AIRequest, maxCents int64, ref string) (AIResponse, int64, error) {
	if a.h.server == nil {
		return AIResponse{}, 0, errors.New("the engine has not started")
	}
	c, err := a.h.server.AIModelOn(ctx, workspaceID, task, integrationID, model, maxCents)
	if err != nil {
		return AIResponse{}, 0, err
	}
	return chatOn(ctx, a.h.server.AIModels(), workspaceID, task, c, req, ref)
}

// chatOn makes one call on an opened model, books it and returns what it
// cost in cents: the cost the provider reported when it reports one (a
// router such as OpenRouter, whose models have no price of Outflare's),
// else the price, with cached input at its own rate (llm.Price.Cents).
func chatOn(ctx context.Context, m research.Models, workspaceID, task string, c research.Choice, req AIRequest, ref string) (AIResponse, int64, error) {
	resp, err := c.Model.Chat(ctx, req)
	cents := c.Price.Cents(resp.Usage)
	if serr := m.Spent(ctx, workspaceID, c, resp.Usage, ref, task); serr != nil && err == nil {
		err = serr
	}
	return resp, cents, err
}

// AIModel is a chat model one of the workspace's AI connections can use.
// Priced says whether Outflare can keep it to the workspace's spend limits
// (a known list price, or a router that reports each call's cost), which
// ChatOn needs.
type AIModel struct {
	ID, Name string
	Priced   bool
}

// Models lists the chat models one of the workspace's AI connections can
// use, asked of the provider live with the workspace's own key (it costs
// nothing). workspaceID must come from a row the caller holds for that
// workspace.
func (a *AI) Models(ctx context.Context, workspaceID, integrationID string) ([]AIModel, error) {
	if a.h.server == nil {
		return nil, errors.New("the engine has not started")
	}
	p, list, err := a.h.server.AIModelsOn(ctx, workspaceID, integrationID)
	if err != nil {
		return nil, err
	}
	out := make([]AIModel, len(list))
	for i, m := range list {
		_, priced := llm.KnownPrice(p.ID, m.ID)
		out[i] = AIModel{ID: m.ID, Name: m.Name, Priced: priced}
	}
	return out, nil
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
	return chatOn(ctx, m, workspaceID, task, c, req, ref)
}
