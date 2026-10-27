package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/emails"
)

// Status: what is stopping the workspace and what to do about it, from one
// call. It gathers what other endpoints each show part of (mailbox errors,
// domain health, warm-up, broken connections, held mission writes,
// allocation shortages, plans awaiting approval, lifecycle email, social
// drafts and customer data) into a list of blockers,
// each naming the tool and page that resolve it and who has to act. Agents
// read it at the start of a conversation; the dashboard shows it on the
// overview.

func (s *Server) routesStatus(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/status", s.inWorkspace(auth.ScopeWorkspaceRead, s.status))
}

// statusFix says how to resolve a blocker: the MCP tool an agent calls
// first, the dashboard page a person opens, and who must act. agent means
// an agent with the right scopes can resolve it; person means it needs a
// person (a sign-in, a payment, an approval).
type statusFix struct {
	Tool string `json:"tool"`
	Page string `json:"page"`
	Who  string `json:"who"`
}

type statusItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Why this item is blocked, when known.
	Reason string `json:"reason,omitempty"`
}

type statusBlocker struct {
	Code     string       `json:"code"`
	Severity string       `json:"severity"`
	Area     string       `json:"area"`
	Title    string       `json:"title"`
	Detail   string       `json:"detail"`
	Count    int          `json:"count"`
	Items    []statusItem `json:"items"`
	Fix      statusFix    `json:"fix"`
}

type statusStep struct {
	Step  string `json:"step"`
	Title string `json:"title"`
	Done  bool   `json:"done"`
}

type statusView struct {
	Summary string `json:"summary"`
	// Whether at least one mailbox can send cold email today.
	CanSend  bool            `json:"can_send"`
	Setup    []statusStep    `json:"setup"`
	Blockers []statusBlocker `json:"blockers"`
	// When this was worked out.
	CheckedAt time.Time `json:"checked_at"`
}

var severityRank = map[string]int{"critical": 0, "warning": 1, "info": 2}

// statusItems runs a query returning (id, name, reason) rows, at most five,
// and the total count in a fourth column.
func (s *Server) statusItems(ctx context.Context, sql string, args ...any) ([]statusItem, int, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, err
	}
	total := 0
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (statusItem, error) {
		var it statusItem
		err := row.Scan(&it.ID, &it.Name, &it.Reason, &total)
		return it, err
	})
	if items == nil {
		items = []statusItem{}
	}
	return items, total, err
}

func countOf(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := s.workspaceStatus(r.Context(), a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// workspaceStatus works out the workspace's blockers and set-up steps.
func (s *Server) workspaceStatus(ctx context.Context, ws string) (statusView, error) {
	v := statusView{Blockers: []statusBlocker{}, CheckedAt: time.Now().UTC()}
	page := func(p string) string { return s.appPath(ctx, ws, p) }
	add := func(b statusBlocker) {
		if b.Items == nil {
			b.Items = []statusItem{}
		}
		v.Blockers = append(v.Blockers, b)
	}
	// Each query is small and indexed by workspace; a workspace's status
	// is read a few times a conversation, not per request.
	var c struct {
		mailboxes, ready, warming, leads, sequences, activeSequences, unread, held, draftPlans, shortages, fixErrors int
		firstReady                                                                                                   *time.Time
	}
	err := s.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM mailboxes WHERE workspace_id = $1 AND lifecycle_stage <> 'retired'),
			(SELECT count(*) FROM mailboxes WHERE workspace_id = $1 AND status = 'active' AND lifecycle_stage = 'ready'),
			(SELECT count(*) FROM mailboxes WHERE workspace_id = $1 AND status IN ('active', 'verifying') AND lifecycle_stage = 'warming'),
			(SELECT min(stage_until) FROM mailboxes WHERE workspace_id = $1 AND status IN ('active', 'verifying') AND lifecycle_stage = 'warming'),
			(SELECT count(*) FROM leads WHERE workspace_id = $1),
			(SELECT count(*) FROM sequences WHERE workspace_id = $1 AND status <> 'archived'),
			(SELECT count(*) FROM sequences WHERE workspace_id = $1 AND status = 'active'),
			(SELECT count(*) FROM inbound_messages WHERE workspace_id = $1 AND kind = 'reply' AND NOT read AND NOT done),
			(SELECT count(*) FROM mission_actions WHERE workspace_id = $1 AND status = 'pending'),
			(SELECT count(*) FROM infra_plans WHERE workspace_id = $1 AND status = 'draft'),
			(SELECT count(*) FROM allocation_status st JOIN sequences q ON q.id = st.sequence_id
			 WHERE st.workspace_id = $1 AND q.status = 'active' AND st.needed_daily > st.active_daily),
			(SELECT count(*) FROM fixes WHERE workspace_id = $1 AND status = 'open' AND severity = 'error')`, ws,
	).Scan(&c.mailboxes, &c.ready, &c.warming, &c.firstReady, &c.leads, &c.sequences, &c.activeSequences,
		&c.unread, &c.held, &c.draftPlans, &c.shortages, &c.fixErrors)
	if err != nil {
		return v, err
	}

	// Mailboxes.
	errored, nErrored, err := s.statusItems(ctx, `
		SELECT id::text, email, COALESCE(status_reason, ''), count(*) OVER ()::int FROM mailboxes
		WHERE workspace_id = $1 AND status = 'error' ORDER BY updated_at DESC LIMIT 5`, ws)
	if err != nil {
		return v, err
	}
	switch {
	case c.mailboxes == 0:
		add(statusBlocker{Code: "no_mailboxes", Severity: "critical", Area: "mailboxes", Title: "No mailboxes connected",
			Detail: "Nothing can send until a mailbox is connected. Connect Google or Microsoft mailboxes, upload SMTP/IMAP ones, or buy them from a supplier such as Cheap Inboxes.",
			Fix:    statusFix{Tool: "request_connection", Page: page("mailboxes"), Who: "person"}})
	case c.ready == 0 && c.warming > 0:
		d := countOf(c.warming, "mailbox is", "mailboxes are") + " still warming up, so none can send cold email yet."
		if c.firstReady != nil {
			d += " The first should be ready by " + c.firstReady.UTC().Format("2 Jan 2006") + "."
		}
		add(statusBlocker{Code: "none_ready", Severity: "warning", Area: "mailboxes", Title: "No mailbox is ready to send", Detail: d, Count: c.warming,
			Fix: statusFix{Tool: "get_infrastructure", Page: page("infrastructure"), Who: "agent"}})
	}
	if nErrored > 0 {
		add(statusBlocker{Code: "mailbox_error", Severity: "critical", Area: "mailboxes", Title: countOf(nErrored, "mailbox can't sign in", "mailboxes can't sign in"),
			Detail: "They send and receive nothing until reconnected. get_mailbox shows each one's error.", Count: nErrored, Items: errored,
			Fix: statusFix{Tool: "get_mailbox", Page: page("mailboxes"), Who: "person"}})
	}

	// Domains and sending servers.
	domains, err := s.queryDomains(ctx, ws, nil)
	if err != nil {
		return v, err
	}
	var critical []statusItem
	healthy := false
	for _, d := range domains {
		if d.Health == "critical" {
			reason := ""
			for _, is := range d.Issues {
				if is.Severity == "critical" {
					reason = is.Title
					break
				}
			}
			critical = append(critical, statusItem{ID: d.Domain, Name: d.Domain, Reason: reason})
		}
		if d.Status == "active" && (d.Health == "healthy" || d.Health == "warning") {
			healthy = true
		}
	}
	if len(critical) > 0 {
		n := len(critical)
		add(statusBlocker{Code: "domain_critical", Severity: "critical", Area: "domains", Title: countOf(n, "domain has", "domains have") + " critical problems",
			Detail: "Mail from these domains is likely to bounce or land in spam. get_domain shows the exact DNS record to fix each.", Count: n, Items: critical[:min(n, 5)],
			Fix: statusFix{Tool: "get_domain", Page: page("domains"), Who: "person"}})
	}
	servers, nServers, err := s.statusItems(ctx, `
		SELECT id::text, COALESCE(NULLIF(name, ''), host), COALESCE(status_reason, ''), count(*) OVER ()::int FROM sending_servers
		WHERE workspace_id = $1 AND status = 'paused' ORDER BY updated_at DESC LIMIT 5`, ws)
	if err != nil {
		return v, err
	}
	if nServers > 0 {
		add(statusBlocker{Code: "server_paused", Severity: "critical", Area: "servers", Title: countOf(nServers, "sending server is", "sending servers are") + " paused",
			Detail: "Their mailboxes send nothing, usually because an IP is on a blocklist. get_sending_server shows which list and how to get off it.", Count: nServers, Items: servers,
			Fix: statusFix{Tool: "get_sending_server", Page: page("servers"), Who: "person"}})
	}

	// Connections.
	broken, nBroken, err := s.statusItems(ctx, `
		SELECT id::text, COALESCE(NULLIF(name, ''), connector), COALESCE(last_error, ''), count(*) OVER ()::int FROM integrations
		WHERE workspace_id = $1 AND status = 'error' ORDER BY updated_at DESC LIMIT 5`, ws)
	if err != nil {
		return v, err
	}
	if nBroken > 0 {
		add(statusBlocker{Code: "connection_broken", Severity: "warning", Area: "connectors", Title: countOf(nBroken, "connection has", "connections have") + " stopped working",
			Detail: "The service refused its key or sign-in. Call request_connection for the same connector and give the person the link to reconnect; never ask for keys in chat.", Count: nBroken, Items: broken,
			Fix: statusFix{Tool: "request_connection", Page: page("connectors"), Who: "person"}})
	}
	waiting, nWaiting, err := s.statusItems(ctx, `
		SELECT id::text, connector, '', count(*) OVER ()::int FROM connect_requests
		WHERE workspace_id = $1 AND status = 'pending' AND expires_at > now() ORDER BY created_at DESC LIMIT 5`, ws)
	if err != nil {
		return v, err
	}
	if nWaiting > 0 {
		add(statusBlocker{Code: "connection_requested", Severity: "info", Area: "connectors", Title: countOf(nWaiting, "connect link is", "connect links are") + " waiting for a person",
			Detail: "Someone was asked to connect these and hasn't yet. get_connection_request shows whether each is done; remind the person with the same link.", Count: nWaiting, Items: waiting,
			Fix: statusFix{Tool: "get_connection_request", Page: page("connectors"), Who: "person"}})
	}

	// Missions.
	if c.held > 0 {
		add(statusBlocker{Code: "approval_waiting", Severity: "warning", Area: "missions", Title: countOf(c.held, "mission change waits", "mission changes wait") + " for a person",
			Detail: "An agent's writes are held until a person approves them on the Missions page. An agent can never approve its own.", Count: c.held,
			Fix: statusFix{Tool: "list_pending_approvals", Page: page("missions"), Who: "person"}})
	}
	paused, nPaused, err := s.statusItems(ctx, `
		SELECT id::text, name, status_reason, count(*) OVER ()::int FROM missions
		WHERE workspace_id = $1 AND status = 'paused' ORDER BY updated_at DESC LIMIT 5`, ws)
	if err != nil {
		return v, err
	}
	if nPaused > 0 {
		add(statusBlocker{Code: "mission_paused", Severity: "warning", Area: "missions", Title: countOf(nPaused, "mission is", "missions are") + " paused",
			Detail: "A paused mission only reads. Each item says why; a person resumes it or raises the limit that stopped it.", Count: nPaused, Items: paused,
			Fix: statusFix{Tool: "get_mission", Page: page("missions"), Who: "person"}})
	}

	// Sending.
	if c.shortages > 0 {
		add(statusBlocker{Code: "allocation_short", Severity: "warning", Area: "sequences", Title: countOf(c.shortages, "active sequence is", "active sequences are") + " short of mailboxes",
			Detail: "They send less than they are set to. get_allocation_attention says why and what would free up mailboxes.", Count: c.shortages,
			Fix: statusFix{Tool: "get_allocation_attention", Page: page("allocation"), Who: "agent"}})
	}
	if c.draftPlans > 0 {
		add(statusBlocker{Code: "infra_plan_waiting", Severity: "info", Area: "infrastructure", Title: countOf(c.draftPlans, "infrastructure plan waits", "infrastructure plans wait") + " for approval",
			Detail: "Approving a plan buys domains and mailboxes with the person's money, so only a person with infrastructure.approve does it.", Count: c.draftPlans,
			Fix: statusFix{Tool: "list_infra_plans", Page: page("infrastructure"), Who: "person"}})
	}
	// The fixes list: only errors nobody has taken yet, so Home shows the
	// list once and the Fixes page has the rest.
	if c.fixErrors > 0 {
		add(statusBlocker{Code: "fixes_waiting", Severity: "warning", Area: "fixes", Title: countOf(c.fixErrors, "error on the fixes list needs", "errors on the fixes list need") + " someone",
			Detail: "list_fixes with status open lists them, worst first; get_fix_prompt gives a coding agent what it needs to fix one, and verify_fix checks it.", Count: c.fixErrors,
			Fix: statusFix{Tool: "list_fixes", Page: page("fixes"), Who: "agent"}})
	}
	// Lifecycle email: sending domains the provider would not verify, and
	// broadcasts that stopped this week.
	failedDomains, nFailedDomains, err := s.statusItems(ctx, `
		SELECT id::text, domain, COALESCE(status_reason, ''), count(*) OVER ()::int FROM email_domains
		WHERE workspace_id = $1 AND status = 'failed' ORDER BY updated_at DESC LIMIT 5`, ws)
	if err != nil {
		return v, err
	}
	if nFailedDomains > 0 {
		add(statusBlocker{Code: "email_domain_failed", Severity: "warning", Area: "lifecycle", Title: countOf(nFailedDomains, "email sending domain isn't", "email sending domains aren't") + " verified",
			Detail: "Lifecycle email from these domains can't send until the email provider verifies them. list_email_domains shows the DNS records to add; check_email_domain checks again.", Count: nFailedDomains, Items: failedDomains,
			Fix: statusFix{Tool: "list_email_domains", Page: page("providers"), Who: "person"}})
	}
	failedSends, nFailedSends, err := s.statusItems(ctx, `
		SELECT id::text, name, COALESCE(status_reason, ''), count(*) OVER ()::int FROM email_broadcasts
		WHERE workspace_id = $1 AND status = 'failed' AND updated_at > now() - interval '7 days' ORDER BY updated_at DESC LIMIT 5`, ws)
	if err != nil {
		return v, err
	}
	if nFailedSends > 0 {
		add(statusBlocker{Code: "email_broadcast_failed", Severity: "warning", Area: "lifecycle", Title: countOf(nFailedSends, "broadcast", "broadcasts") + " stopped sending this week",
			Detail: "get_email_broadcast shows why each stopped and how many people it reached first.", Count: nFailedSends, Items: failedSends,
			Fix: statusFix{Tool: "get_email_broadcast", Page: page("broadcasts"), Who: "agent"}})
	}

	// Scheduled broadcasts going to people who look like they never asked
	// for it: a warning, not a stop, saying what to do instead.
	rows, err := s.pool.Query(ctx, `SELECT id::text, name, brand_id::text, audience_kind, record_ids::text[] FROM email_broadcasts
		WHERE workspace_id = $1 AND status = 'scheduled' ORDER BY send_at NULLS LAST, created_at LIMIT 20`, ws)
	if err != nil {
		return v, err
	}
	type scheduled struct {
		id, name, kind string
		brand          *string
		ids            []string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (scheduled, error) {
		var b scheduled
		return b, r.Scan(&b.id, &b.name, &b.brand, &b.kind, &b.ids)
	})
	if err != nil {
		return v, err
	}
	var risky []statusItem
	if len(list) > 0 {
		rule, err := customers.MarketingRule(ctx, s.pool, ws)
		if err != nil {
			return v, err
		}
		for _, b := range list {
			bases, err := emails.AudienceBases(ctx, s.pool, ws, b.brand, b.kind, b.ids)
			if err != nil {
				return v, err
			}
			if w := audienceWarnings(bases, rule); len(w) > 0 {
				risky = append(risky, statusItem{ID: b.id, Name: b.name, Reason: w[0].Message})
			}
		}
	}
	if len(risky) > 0 {
		add(statusBlocker{Code: "email_broadcast_audience_warning", Severity: "warning", Area: "lifecycle",
			Title:  countOf(len(risky), "scheduled broadcast goes", "scheduled broadcasts go") + " to people who may not have asked for it",
			Detail: "Third-party leads are left out of marketing email; people of unknown origin are not. get_email_broadcast lists the warnings and what to do.",
			Count:  len(risky), Items: risky[:min(len(risky), 5)],
			Fix: statusFix{Tool: "get_email_broadcast", Page: page("broadcasts"), Who: "person"}})
	}

	// Social replies drafted for a person to approve before they post.
	drafts, nDrafts, err := s.statusItems(ctx, `
		SELECT id::text, channel, '', count(*) OVER ()::int FROM social_actions
		WHERE workspace_id = $1 AND status = 'draft' ORDER BY created_at DESC LIMIT 5`, ws)
	if err != nil {
		return v, err
	}
	if nDrafts > 0 {
		add(statusBlocker{Code: "social_drafts_waiting", Severity: "info", Area: "social", Title: countOf(nDrafts, "social reply waits", "social replies wait") + " for approval",
			Detail: "Nothing posts until a person approves it. list_social_replies with status draft lists them.", Count: nDrafts, Items: drafts,
			Fix: statusFix{Tool: "list_social_replies", Page: page("social"), Who: "person"}})
	}

	// Customer data: possible duplicates to decide, new event names held
	// until approved, and CRM imports that failed.
	var dupes, quarantined int
	if err := s.pool.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM record_merge_candidates WHERE workspace_id = $1 AND status = 'open'),
			(SELECT count(*) FROM event_definitions WHERE workspace_id = $1 AND status = 'quarantined')`, ws).Scan(&dupes, &quarantined); err != nil {
		return v, err
	}
	if dupes > 0 {
		add(statusBlocker{Code: "duplicates_waiting", Severity: "info", Area: "customers", Title: countOf(dupes, "possible duplicate person waits", "possible duplicate people wait") + " for a decision",
			Detail: "list_record_duplicates shows each pair side by side; merge, link or dismiss each with merge_records, link_record_duplicate or dismiss_record_duplicate.", Count: dupes,
			Fix: statusFix{Tool: "list_record_duplicates", Page: page("ai"), Who: "agent"}})
	}
	if quarantined > 0 {
		add(statusBlocker{Code: "events_quarantined", Severity: "info", Area: "customers", Title: countOf(quarantined, "new product event name is", "new product event names are") + " held for review",
			Detail: "Events with a new name are stored but kept out of journeys and reports until approved. get_catalog lists them; update_event_definition approves or deprecates each.", Count: quarantined,
			Fix: statusFix{Tool: "update_event_definition", Page: page("ai"), Who: "agent"}})
	}
	imports, nImports, err := s.statusItems(ctx, `
		SELECT id::text, connector, COALESCE(error, ''), count(*) OVER ()::int FROM crm_imports
		WHERE workspace_id = $1 AND status = 'failed' AND finished_at > now() - interval '7 days'
		  AND NOT EXISTS (SELECT 1 FROM crm_imports o WHERE o.integration_id = crm_imports.integration_id AND o.created_at > crm_imports.created_at)
		ORDER BY created_at DESC LIMIT 5`, ws)
	if err != nil {
		return v, err
	}
	if nImports > 0 {
		add(statusBlocker{Code: "crm_import_failed", Severity: "warning", Area: "connectors", Title: countOf(nImports, "CRM import", "CRM imports") + " failed",
			Detail: "get_crm_import says where it stopped; running it again with start_crm_import picks up the same records without duplicating them.", Count: nImports, Items: imports,
			Fix: statusFix{Tool: "get_crm_import", Page: page("connectors"), Who: "agent"}})
	}
	if c.unread > 0 {
		add(statusBlocker{Code: "unread_replies", Severity: "info", Area: "inbox", Title: countOf(c.unread, "reply is", "replies are") + " unread",
			Detail: "list_inbox with unread true lists them; triage and answer interested ones first.", Count: c.unread,
			Fix: statusFix{Tool: "list_inbox", Page: page("inbox"), Who: "agent"}})
	}

	// Analytics: sites that count for no brand (no website, in a workspace
	// without brands), and brands no analytics tool reads.
	unmapped, nUnmapped, err := s.statusItems(ctx, `
		WITH b AS (SELECT EXISTS (SELECT 1 FROM brands WHERE workspace_id = $1 AND status = 'active') AS any)
		SELECT s.integration_id::text, COALESCE(NULLIF(s.site_name, ''), s.site), i.name, count(*) OVER ()::int
		FROM analytics_syncs s JOIN integrations i ON i.id = s.integration_id, b
		WHERE s.workspace_id = $1 AND s.site <> '' AND i.status <> 'error'
		  AND CASE WHEN b.any THEN analytics_site_brand($1, s.integration_id, s.site, s.host) IS NULL
		        AND NOT EXISTS (SELECT 1 FROM analytics_site_brands o WHERE o.integration_id = s.integration_id AND o.site = s.site)
		      ELSE s.host = '' END
		ORDER BY i.name, 2 LIMIT 5`, ws)
	if err != nil {
		return v, err
	}
	if nUnmapped > 0 {
		add(statusBlocker{Code: "analytics_site_unmapped", Severity: "info", Area: "analytics", Title: countOf(nUnmapped, "analytics site has", "analytics sites have") + " no brand yet",
			Detail: "Their numbers count for no brand and don't line up with the brand's other tools. update_analytics_site with the brand's website as domain, or set_analytics_site_brand, maps each; a site that is a test copy can be switched off with sync false.", Count: nUnmapped, Items: unmapped,
			Fix: statusFix{Tool: "update_analytics_site", Page: page("analytics"), Who: "person"}})
	}
	unread, nUnread, err := s.statusItems(ctx, `
		SELECT b.id::text, b.name, b.website, count(*) OVER ()::int FROM brands b
		WHERE b.workspace_id = $1 AND b.status = 'active' AND b.website <> ''
		  AND EXISTS (SELECT 1 FROM analytics_syncs a WHERE a.workspace_id = $1 AND a.site <> '')
		  AND NOT EXISTS (SELECT 1 FROM analytics_syncs s WHERE s.workspace_id = $1 AND s.site <> ''
		      AND analytics_site_brand($1, s.integration_id, s.site, s.host) = b.id)
		ORDER BY b.created_at LIMIT 5`, ws)
	if err != nil {
		return v, err
	}
	if nUnread > 0 {
		add(statusBlocker{Code: "brand_without_analytics", Severity: "info", Area: "analytics", Title: countOf(nUnread, "brand has", "brands have") + " no website analytics",
			Detail: "No connected analytics tool reads these brands' websites. If one already tracks them, map its site to the brand on the Analytics page; otherwise connect the tool their website uses with request_connection.", Count: nUnread, Items: unread,
			Fix: statusFix{Tool: "request_connection", Page: page("analytics"), Who: "person"}})
	}

	// Install health: what the workspace put on its sites or wired to
	// Outflare that went quiet, fell behind or broke. Failing connections
	// are connection_broken above.
	for _, sev := range []string{"critical", "warning"} {
		items, n, err := s.statusItems(ctx, `
			SELECT id::text, title, kind, count(*) OVER ()::int FROM install_issues
			WHERE workspace_id = $1 AND resolved_at IS NULL AND severity = $2 AND kind <> 'connection_broken'
			ORDER BY opened_at LIMIT 5`, ws, sev)
		if err != nil {
			return v, err
		}
		if n == 0 {
			continue
		}
		b := statusBlocker{Code: "install_broken", Severity: sev, Area: "setup", Title: countOf(n, "install has stopped working", "installs have stopped working"),
			Detail: "Such as a website script that sends no visits or a site whose DNS proof is missing. get_install_health says what each one is and how to fix it.",
			Count:  n, Items: items, Fix: statusFix{Tool: "get_install_health", Page: page("setup"), Who: "person"}}
		if sev == "warning" {
			b.Code, b.Title = "install_outdated", countOf(n, "install needs attention", "installs need attention")
			b.Detail = "Such as an old copy of the website script, an outdated SDK or MCP server, or calls to a deprecated API route. get_install_health says what to update."
		}
		add(b)
	}

	// Company context.
	cv, err := s.loadContext(ctx, ws)
	if err != nil {
		return v, err
	}
	if cv.AskForMissing {
		add(statusBlocker{Code: "context_missing", Severity: "info", Area: "context", Title: "Outflare doesn't know the company yet",
			Detail: "Missing: " + strings.Join(cv.Missing, ", ") + ". Ask the person for these in one message (or offer to read the website), then save them with update_context so no one asks again.",
			Count:  len(cv.Missing), Fix: statusFix{Tool: "update_context", Page: page("company"), Who: "agent"}})
	}

	slices.SortStableFunc(v.Blockers, func(x, y statusBlocker) int { return severityRank[x.Severity] - severityRank[y.Severity] })
	v.CanSend = c.ready > 0 && healthy
	v.Setup = []statusStep{
		{"context", "Tell Outflare about the company", len(cv.Missing) == 0},
		{"mailboxes", "Connect a mailbox", c.mailboxes > 0},
		{"ready", "Have a mailbox ready to send on a healthy domain", v.CanSend},
		{"leads", "Add leads", c.leads > 0},
		{"sequence", "Write a sequence", c.sequences > 0},
		{"live", "Start a sequence", c.activeSequences > 0},
	}
	v.Summary = statusSummary(v)
	return v, nil
}

// statusSummary is one sentence an agent can repeat.
func statusSummary(v statusView) string {
	var critical, warnings int
	for _, b := range v.Blockers {
		switch b.Severity {
		case "critical":
			critical++
		case "warning":
			warnings++
		}
	}
	var next string
	for _, st := range v.Setup {
		if !st.Done {
			next = st.Title
			break
		}
	}
	switch {
	case critical > 0:
		return fmt.Sprintf("%s stopping sending: %s.", countOf(critical, "problem is", "problems are"), strings.ToLower(v.Blockers[0].Title[:1])+v.Blockers[0].Title[1:])
	case warnings > 0:
		return fmt.Sprintf("Sending can run; %s attention: %s.", countOf(warnings, "thing needs", "things need"), strings.ToLower(v.Blockers[0].Title[:1])+v.Blockers[0].Title[1:])
	case next != "":
		return "Nothing is broken. Next step: " + strings.ToLower(next[:1]) + next[1:] + "."
	}
	return "Everything is running and nothing needs attention."
}
