package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

func (s *Server) routesSales(mux *http.ServeMux) {
	b := "/v1/brands/{brand}/sales"
	for _, rt := range []struct {
		method, path, scope string
		handler             http.HandlerFunc
	}{
		{"GET", "/reporting/team", auth.ScopeCustomersRead, s.getSalesTeamReporting},
		{"GET", "/reporting", auth.ScopeCustomersRead, s.getSalesReporting},
		{"GET", "/reporting/leads", auth.ScopeCustomersRead, s.getSalesReporting},
		{"GET", "/reporting/lanes", auth.ScopeCustomersRead, s.getSalesReporting},
		{"POST", "/queue-seen", auth.ScopeCustomersManage, s.markSalesQueueSeen},
		{"GET", "/me", auth.ScopeCustomersRead, s.getSalesMe}, {"PUT", "/me", auth.ScopeCustomersManage, s.putSalesMe},
		{"GET", "/team", auth.ScopeCustomersRead, s.listSalesTeam}, {"PUT", "/team/{user}", auth.ScopeMembersManage, s.putSalesRole},
		{"GET", "/leads", auth.ScopeCustomersRead, s.listSalesLeads}, {"GET", "/leads/{id}", auth.ScopeCustomersRead, s.getSalesLead},
		{"GET", "/leads/{id}/slack-search", auth.ScopeCustomersRead, s.searchSalesLeadSlack},
		{"POST", "/leads/{id}/actions", auth.ScopeCustomersManage, s.actSalesLead},
		{"PUT", "/leads/{id}/draft", auth.ScopeCustomersManage, s.putSalesDraft}, {"POST", "/leads/{id}/send", auth.ScopeCustomersManage, s.sendSalesReply},
		{"POST", "/leads/{id}/draft-assist", auth.ScopeCustomersManage, s.requestSalesDraftAssist},
		{"GET", "/leads/{id}/draft-assist/{request}", auth.ScopeCustomersRead, s.getSalesDraftAssist},
		{"GET", "/templates", auth.ScopeCustomersRead, s.listSalesTemplates}, {"POST", "/templates", auth.ScopeCustomersManage, s.putSalesTemplate}, {"PUT", "/templates/{id}", auth.ScopeCustomersManage, s.putSalesTemplate}, {"PUT", "/templates/{id}/favorite", auth.ScopeCustomersManage, s.favoriteSalesTemplate},
		{"GET", "/feeds", auth.ScopeCustomersRead, s.getSalesFeeds}, {"PUT", "/feeds", auth.ScopeMembersManage, s.putSalesFeeds},
		{"GET", "/feeds/channels", auth.ScopeMembersManage, s.listSalesFeedChannels}, {"POST", "/feeds/test", auth.ScopeMembersManage, s.testSalesFeedChannel},
		{"PUT", "/feeds/alerts", auth.ScopeMembersManage, s.putSalesAlerts},
		{"GET", "/feeds/{id}/fields", auth.ScopeCustomersRead, s.listSalesFeedFields}, {"PUT", "/feeds/{id}/layout", auth.ScopeCustomersManage, s.putSalesFeedLayout},
		{"GET", "/feed-items", auth.ScopeCustomersRead, s.listSalesFeedItems}, {"POST", "/feed-items/{id}/actions", auth.ScopeCustomersManage, s.actSalesFeedItem},
		{"POST", "/bridge", auth.ScopeCustomersManage, s.importSalesLead},
		{"POST", "/mail/authorize", auth.ScopeCustomersManage, s.authorizeSalesGoogle}, {"POST", "/mail/workspace", auth.ScopeCustomersManage, s.connectSalesWorkspace}, {"DELETE", "/mail/connection", auth.ScopeCustomersRead, s.disconnectSalesGoogle}, {"POST", "/mail/sync", auth.ScopeCustomersManage, s.queueSalesSync},
	} {
		mux.HandleFunc(rt.method+" "+b+rt.path, s.onBrand(rt.scope, rt.handler))
	}
	mux.HandleFunc("GET "+salesLegacyCallback, s.requireUser(s.finishSalesGoogle))
}

type salesMember struct {
	UserID       string     `json:"user_id"`
	Name         string     `json:"name"`
	Role         string     `json:"role"`
	Introduction string     `json:"introduction"`
	Connected    bool       `json:"connected"`
	MailVia      string     `json:"mail_via"`
	MailStatus   string     `json:"mail_status"`
	LastSynced   *time.Time `json:"last_synced_at"`
	OpenLeads    int        `json:"open_leads"`
	// WorkspaceAvailable says the person may use the company's Google
	// Workspace connection instead of signing in (only on /sales/me).
	WorkspaceAvailable *bool `json:"workspace_available,omitempty"`
}

func salesMemberAt(ctx context.Context, q mailQuery, a *access, user string) (salesMember, error) {
	var v salesMember
	err := q.QueryRow(ctx, `SELECT u.id::text,u.name,COALESCE(sm.role,'none'),COALESCE(sm.introduction,''),COALESCE(c.status='connected',false),CASE WHEN c.workspace_integration_id IS NOT NULL THEN 'workspace' WHEN c.id IS NOT NULL THEN 'personal' ELSE 'none' END,COALESCE(c.error_code,c.status,'not_connected'),c.last_synced_at,
 (SELECT count(*) FROM sales_leads l WHERE l.workspace_id=$1 AND l.brand_id=$2 AND l.owner_id=u.id AND l.state='active')
 FROM memberships m JOIN users u ON u.id=m.user_id LEFT JOIN sales_members sm ON sm.workspace_id=m.workspace_id AND sm.brand_id=$2 AND sm.user_id=u.id
 LEFT JOIN sales_mail_connections c ON c.workspace_id=m.workspace_id AND c.brand_id=$2 AND c.owner_id=u.id
 WHERE m.workspace_id=$1 AND u.id=$3 AND u.disabled_at IS NULL AND (m.brand_ids IS NULL OR cardinality(m.brand_ids)=0 OR $2::uuid=ANY(m.brand_ids))`, a.WorkspaceID, a.Brand, user).Scan(&v.UserID, &v.Name, &v.Role, &v.Introduction, &v.Connected, &v.MailVia, &v.MailStatus, &v.LastSynced, &v.OpenLeads)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	return v, err
}
func (s *Server) salesPerson(w http.ResponseWriter, r *http.Request) (salesMember, bool) {
	if !s.crmMailPerson(w, r) {
		return salesMember{}, false
	}
	a := accessFrom(r.Context())
	m, err := salesMemberAt(r.Context(), s.pool, a, a.UserID)
	if s.allocError(w, r, err) {
		return m, false
	}
	return m, true
}
func (s *Server) getSalesMe(w http.ResponseWriter, r *http.Request) {
	m, ok := s.salesPerson(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	_, available, err := s.salesWorkspaceFor(r.Context(), s.pool, a.WorkspaceID, a.Brand, a.UserID)
	if s.allocError(w, r, err) {
		return
	}
	m.WorkspaceAvailable = &available
	writeJSON(w, 200, m)
}
func (s *Server) putSalesMe(w http.ResponseWriter, r *http.Request) {
	m, ok := s.salesPerson(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	var in struct {
		Introduction string `json:"introduction"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Introduction) > 1000 {
		writeError(w, r, 400, "invalid_request", "introduction is too long")
		return
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `INSERT INTO sales_members(workspace_id,brand_id,user_id,role,introduction)VALUES($1,$2,$3,$4,$5) ON CONFLICT(workspace_id,brand_id,user_id) DO UPDATE SET introduction=$5`, a.WorkspaceID, a.Brand, a.UserID, m.Role, in.Introduction)
		if err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "sales.introduction_updated", "sales_member", a.UserID, map[string]any{"brand_id": a.Brand})
	})
	if !s.allocError(w, r, err) {
		m.Introduction = in.Introduction
		writeJSON(w, 200, m)
	}
}
func (s *Server) listSalesTeam(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.salesPerson(w, r); !ok {
		return
	}
	a := accessFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), `SELECT user_id::text FROM memberships WHERE workspace_id=$1 AND (brand_ids IS NULL OR cardinality(brand_ids)=0 OR $2::uuid=ANY(brand_ids)) ORDER BY user_id`, a.WorkspaceID, a.Brand)
	if s.allocError(w, r, err) {
		return
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if s.allocError(w, r, err) {
		return
	}
	out := []salesMember{}
	for _, id := range ids {
		m, e := salesMemberAt(r.Context(), s.pool, a, id)
		if errors.Is(e, errNotFound) {
			continue
		}
		if s.allocError(w, r, e) {
			return
		}
		out = append(out, m)
	}
	writeJSON(w, 200, map[string]any{"data": out})
}
func (s *Server) putSalesRole(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.salesPerson(w, r); !ok {
		return
	}
	a := accessFrom(r.Context())
	user := r.PathValue("user")
	var in struct {
		Role string `json:"role"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !validUUID(user) || !slices.Contains([]string{"sdr", "ae", "manager", "none"}, in.Role) {
		writeError(w, r, 400, "invalid_request", "choose a member and Sales role")
		return
	}
	var m salesMember
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		if _, err = tx.Exec(r.Context(), `SELECT 1 FROM memberships WHERE workspace_id=$1 AND user_id=$2 FOR UPDATE`, a.WorkspaceID, user); err != nil {
			return err
		}
		m, err = salesMemberAt(r.Context(), tx, a, user)
		if err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO sales_members(workspace_id,brand_id,user_id,role)VALUES($1,$2,$3,$4) ON CONFLICT(workspace_id,brand_id,user_id)DO UPDATE SET role=$4`, a.WorkspaceID, a.Brand, user, in.Role)
		if err != nil {
			return err
		}
		if in.Role != "sdr" && in.Role != "ae" {
			if _, err = tx.Exec(r.Context(), `DELETE FROM sales_mail_connections WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3`, a.WorkspaceID, a.Brand, user); err != nil {
				return err
			}
			if _, err = tx.Exec(r.Context(), `DELETE FROM sales_oauth_flows WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3`, a.WorkspaceID, a.Brand, user); err != nil {
				return err
			}
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "sales.role_updated", "sales_member", user, map[string]any{"brand_id": a.Brand, "role": in.Role})
	})
	if !s.allocError(w, r, err) {
		m.Role = in.Role
		writeJSON(w, 200, m)
	}
}

type salesLead struct {
	Country        string               `json:"country"`
	City           string               `json:"city"`
	CompanyCountry string               `json:"company_country"`
	ViewedBy       []salesLeadView      `json:"viewed_by,omitempty"`
	FeedbackCount  int                  `json:"feedback_count"`
	Attribution    *salesAttribution    `json:"attribution,omitempty"`
	ID             string               `json:"id"`
	RecordID       string               `json:"record_id"`
	Source         string               `json:"source"`
	SourceID       string               `json:"source_id"`
	IntegrationID  *string              `json:"-"`
	State          string               `json:"state"`
	OwnerID        *string              `json:"owner_id"`
	SDRID          *string              `json:"sdr_id"`
	AEID           *string              `json:"ae_id"`
	Name           string               `json:"name"`
	Email          string               `json:"email"`
	Company        string               `json:"company"`
	CompanyID      *string              `json:"company_id"`
	JobTitle       string               `json:"job_title"`
	Website        string               `json:"website"`
	LinkedInURL    string               `json:"linkedin_url"`
	LatestMessage  *salesMessagePreview `json:"latest_message,omitempty"`
	LatestReply    *salesMessagePreview `json:"latest_reply,omitempty"`
	LastActivity   *salesLeadActivity   `json:"last_activity,omitempty"`
	Version        int                  `json:"version"`
	Note           string               `json:"note"`
	Amount         *int64               `json:"amount_minor"`
	Currency       string               `json:"currency"`
	Unread         bool                 `json:"unread"`
	ReplyState     string               `json:"reply_state"`
	CreatedAt      time.Time            `json:"created_at"`
	UpdatedAt      time.Time            `json:"updated_at"`
	IntroducedAt   *time.Time           `json:"introduced_at"`
	IntroducedBy   *string              `json:"introduced_by"`
	ConfirmedAt    *time.Time           `json:"confirmed_at"`
	ConfirmedBy    *string              `json:"confirmed_by"`
	// The Slack channel whose post started the conversation (sales_feeds).
	FeedKind        *string `json:"feed_kind"`
	FeedChannelID   *string `json:"feed_channel_id"`
	FeedChannelName string  `json:"feed_channel_name"`
	// The app that posted it ("Fillout"); null when a person did.
	FeedSource *string `json:"feed_source"`
	// The person's lead source category: inbound or outbound, from their
	// Source; null when their Source has none.
	SourceCategory *string `json:"source_category"`
	// A rep's mark on how good the conversation was: good, poor_fit or
	// junk; null until someone marks it.
	Quality *string `json:"quality"`
	// The newest post on it, for the queue row: filled by listSalesLeads and
	// getSalesLead (salesLatestPosts), null elsewhere and without posts.
	LatestPost *salesPostPreview `json:"latest_post"`
}

const salesLeadSelect = `SELECT (to_jsonb(l)-'source_sealed'-'details_sealed') || jsonb_build_object(
 'name',r.name,
 'email',COALESCE((SELECT i.value FROM record_identities i WHERE i.workspace_id=l.workspace_id AND i.brand_id=l.brand_id AND i.record_id=r.id AND i.namespace='email' AND i.valid_to IS NULL ORDER BY i.preferred DESC,i.verified DESC,i.valid_from DESC LIMIT 1),''),
 'company',COALESCE(c.name,''),
 'company_id',c.id,
 'job_title',COALESCE(r.attributes->>'job_title',''),
 'country',COALESCE(r.attributes->>'country',''),
 'city',COALESCE(r.attributes->>'city',''),
 'company_country',COALESCE(c.attributes->>'country',''),
 'website',COALESCE(NULLIF(c.attributes->>'website',''),r.attributes->>'website',''),
 'linkedin_url',COALESCE(r.attributes->>'linkedin_url',''),
 'feed_channel_name',COALESCE(fc.channel_name,''),
 'source_category',r.attributes->>'source_category',
 'unread',l.updated_at>COALESCE((SELECT seen_at FROM sales_seen v WHERE v.workspace_id=l.workspace_id AND v.brand_id=l.brand_id AND v.lead_id=l.id AND v.user_id=NULLIF($3,'')::uuid),'-infinity'::timestamptz)),
 l.details_sealed,l.integration_id::text
 FROM sales_leads l JOIN records r ON r.id=l.record_id AND r.workspace_id=l.workspace_id AND r.brand_id=l.brand_id
 LEFT JOIN records c ON c.workspace_id=l.workspace_id AND c.brand_id=l.brand_id AND c.id::text=r.attributes->'companies'->>0 AND c.deleted_at IS NULL AND c.merged_into IS NULL
 LEFT JOIN sales_feed_channels fc ON fc.id=l.feed_channel_id AND fc.workspace_id=l.workspace_id
 WHERE l.workspace_id=$1 AND l.brand_id=$2 AND r.deleted_at IS NULL AND r.merged_into IS NULL`

func (s *Server) scanSalesLead(row pgx.Row) (salesLead, error) {
	var v salesLead
	var raw, detail []byte
	err := row.Scan(&raw, &detail, &v.IntegrationID)
	if err != nil {
		return v, err
	}
	if err = json.Unmarshal(raw, &v); err != nil {
		return v, err
	}
	if len(detail) > 0 {
		err = s.salesOpen(detail, v.ID, &v.Note)
	}
	return v, err
}
func (s *Server) salesSeal(id string, v any) []byte {
	raw, _ := json.Marshal(v)
	return s.box.Seal(raw, []byte(id))
}
func (s *Server) salesOpen(cipher []byte, id string, v any) error {
	raw, err := s.box.Open(cipher, []byte(id))
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}
func salesManager(a *access, m salesMember) bool {
	return m.Role == "manager" || auth.Grants(a.Scopes, auth.ScopeMembersManage)
}
func salesVisible(l salesLead, a *access, m salesMember) bool {
	return salesManager(a, m) || m.Role != "none" && (l.State == "new" || deref(l.OwnerID) == a.UserID || deref(l.SDRID) == a.UserID || deref(l.AEID) == a.UserID)
}
func (s *Server) salesLoad(ctx context.Context, q mailQuery, a *access, id string, lock bool) (salesLead, error) {
	if !validUUID(id) {
		return salesLead{}, errNotFound
	}
	tail := " AND l.id=$4"
	if lock {
		tail += " FOR UPDATE OF l"
	}
	l, e := s.scanSalesLead(q.QueryRow(ctx, salesLeadSelect+tail, a.WorkspaceID, a.Brand, a.UserID, id))
	if errors.Is(e, pgx.ErrNoRows) {
		e = errNotFound
	}
	return l, e
}
func (s *Server) listSalesLeads(w http.ResponseWriter, r *http.Request) {
	m, ok := s.salesPerson(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	limit, err := limitParam(r, 50, 100)
	if s.allocError(w, r, err) {
		return
	}
	queue := r.URL.Query().Get("queue")
	if queue == "" {
		queue = "new"
	}
	if queue == "all" && !salesManager(a, m) {
		writeError(w, r, 403, "forbidden", "All conversations is for Sales managers")
		return
	}
	args := []any{a.WorkspaceID, a.Brand, a.UserID}
	where := ""
	switch queue {
	case "new":
		where = " AND l.state='new'"
	case "mine":
		where = " AND l.owner_id=$3::uuid"
	case "inbox":
		where = " AND l.owner_id=$3::uuid AND l.state IN ('active','customer') AND l.reply_state='reply_received'"
	case "handoffs":
		where = " AND l.sdr_id=$3::uuid AND l.owner_id<>$3::uuid"
	case "all":
	default:
		writeError(w, r, 400, "invalid_request", "unknown queue")
		return
	}
	if m.Role == "none" && !salesManager(a, m) {
		where += " AND false"
	}
	switch r.URL.Query().Get("assignment") {
	case "", "all":
	case "unassigned":
		where += " AND l.owner_id IS NULL"
	case "mine":
		where += " AND l.owner_id=$3::uuid"
	default:
		writeError(w, r, 400, "invalid_request", "unknown assignment filter")
		return
	}
	if state := r.URL.Query().Get("state"); state == "open" {
		where += " AND l.state IN ('new','active')"
	} else if state != "" {
		args = append(args, state)
		where += fmt.Sprintf(" AND l.state=$%d", len(args))
	}
	if person := r.URL.Query().Get("person"); person != "" {
		if !validUUID(person) {
			writeError(w, r, 400, "invalid_request", "invalid Sales teammate")
			return
		}
		args = append(args, person)
		where += fmt.Sprintf(" AND $%d::uuid IN (l.owner_id,l.sdr_id,l.ae_id)", len(args))
	}
	if kind := r.URL.Query().Get("kind"); kind != "" {
		if !slices.Contains([]string{"new_person", "handover", "signal", "context"}, kind) {
			writeError(w, r, 400, "invalid_request", "kind is new_person, handover, signal or context")
			return
		}
		args = append(args, kind)
		where += fmt.Sprintf(" AND l.feed_kind=$%d", len(args))
	}
	// source and feed_channel may each be given several times: any of them.
	sources := slices.DeleteFunc(slices.Clone(r.URL.Query()["source"]), func(v string) bool { return v == "" })
	if len(sources) > 12 || slices.ContainsFunc(sources, func(v string) bool { return utf8.RuneCountInString(v) > 60 }) {
		writeError(w, r, 400, "invalid_request", "source is up to 12 apps' names, each at most 60 characters")
		return
	}
	arrived := r.URL.Query().Get("arrived")
	if !slices.Contains([]string{"", "channel", "reply"}, arrived) {
		writeError(w, r, 400, "invalid_request", "arrived is channel or reply")
		return
	}
	switch {
	case len(sources) > 0 && arrived == "reply":
		// Email replies together with some apps' posts: either.
		args = append(args, sources)
		where += fmt.Sprintf(" AND (l.feed_kind IS NULL OR l.feed_source=ANY($%d::text[]))", len(args))
	case len(sources) > 0:
		args = append(args, sources)
		where += fmt.Sprintf(" AND l.feed_source=ANY($%d::text[])", len(args))
	case arrived == "reply":
		where += " AND l.feed_kind IS NULL"
	}
	if arrived == "channel" {
		where += " AND l.feed_kind IS NOT NULL"
	}
	channels := slices.DeleteFunc(slices.Clone(r.URL.Query()["feed_channel"]), func(v string) bool { return v == "" })
	if len(channels) > feedMaxChannels || slices.ContainsFunc(channels, func(v string) bool { return !validUUID(v) }) {
		writeError(w, r, 400, "invalid_request", "feed_channel must be Sales channel ids")
		return
	}
	if len(channels) > 0 {
		args = append(args, channels)
		where += fmt.Sprintf(" AND l.feed_channel_id=ANY($%d::uuid[])", len(args))
	}
	switch category := r.URL.Query().Get("source_category"); category {
	case "":
	case "inbound", "outbound":
		args = append(args, category)
		where += fmt.Sprintf(" AND r.attributes->>'source_category'=$%d", len(args))
	default:
		writeError(w, r, 400, "invalid_request", "source_category is inbound or outbound")
		return
	}
	switch quality := r.URL.Query().Get("quality"); {
	case quality == "":
	case quality == "unrated":
		where += " AND l.quality IS NULL"
	case slices.Contains(salesQualities, quality):
		args = append(args, quality)
		where += fmt.Sprintf(" AND l.quality=$%d", len(args))
	default:
		writeError(w, r, 400, "invalid_request", "quality is good, poor_fit, junk or unrated")
		return
	}
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		if !validUUID(cursor) {
			writeError(w, r, 400, "invalid_request", "invalid cursor")
			return
		}
		args = append(args, cursor)
		where += fmt.Sprintf(" AND (l.updated_at,l.id)<(SELECT updated_at,id FROM sales_leads WHERE workspace_id=$1 AND brand_id=$2 AND id=$%d)", len(args))
	}
	args = append(args, limit+1)
	rows, err := s.pool.Query(r.Context(), salesLeadSelect+where+fmt.Sprintf(" ORDER BY l.updated_at DESC,l.id DESC LIMIT $%d", len(args)), args...)
	if s.allocError(w, r, err) {
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (salesLead, error) { return s.scanSalesLead(row) })
	if s.allocError(w, r, err) {
		return
	}
	if list == nil {
		list = []salesLead{}
	}
	var next *string
	if len(list) > limit {
		list = list[:limit]
		next = &list[len(list)-1].ID
	}
	if s.allocError(w, r, s.salesQueuePreviews(r.Context(), a, list, salesManager(a, m))) {
		return
	}
	s.salesLatestPosts(r.Context(), a, list)
	if s.allocError(w, r, s.salesTeamSignals(r.Context(), a, list)) {
		return
	}
	var unseen, unread bool
	var waiting int
	err = s.pool.QueryRow(r.Context(), `SELECT (SELECT count(*) FROM (SELECT 1 FROM sales_leads l JOIN records r ON r.id=l.record_id AND r.workspace_id=l.workspace_id AND r.brand_id=l.brand_id WHERE l.workspace_id=$1 AND l.brand_id=$2 AND l.state='new' AND l.owner_id IS NULL AND r.deleted_at IS NULL AND r.merged_into IS NULL LIMIT 1000) n),EXISTS(SELECT 1 FROM sales_leads l WHERE l.workspace_id=$1 AND l.brand_id=$2 AND l.state='new' AND l.created_at>COALESCE((SELECT queue_seen_at FROM sales_members WHERE workspace_id=$1 AND brand_id=$2 AND user_id=$3),'-infinity'::timestamptz)),EXISTS(SELECT 1 FROM sales_leads l WHERE l.workspace_id=$1 AND l.brand_id=$2 AND l.owner_id=$3 AND l.state IN ('new','active','customer') AND l.updated_at>COALESCE((SELECT seen_at FROM sales_seen WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=l.id AND user_id=$3),'-infinity'::timestamptz))`, a.WorkspaceID, a.Brand, a.UserID).Scan(&waiting, &unseen, &unread)
	if s.allocError(w, r, err) {
		return
	}
	s.enrichSalesLeads(r.Context(), a, list)
	if m.Role == "none" && !salesManager(a, m) {
		waiting = 0
	}
	// Who the new conversations are assigned to, so the team sees how they
	// are shared out: any Sales teammate can see every new conversation.
	owners := []salesOwnerCount{}
	if queue == "new" && salesDesk(a, m) {
		rows, err := s.pool.Query(r.Context(), `SELECT l.owner_id::text,count(*) FROM sales_leads l JOIN records r ON r.id=l.record_id AND r.workspace_id=l.workspace_id AND r.brand_id=l.brand_id
			WHERE l.workspace_id=$1 AND l.brand_id=$2 AND l.state='new' AND l.owner_id IS NOT NULL AND r.deleted_at IS NULL AND r.merged_into IS NULL
			GROUP BY 1 ORDER BY 2 DESC,1 LIMIT 100`, a.WorkspaceID, a.Brand)
		if s.allocError(w, r, err) {
			return
		}
		owners, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (salesOwnerCount, error) {
			var v salesOwnerCount
			return v, row.Scan(&v.OwnerID, &v.Count)
		})
		if s.allocError(w, r, err) {
			return
		}
		if owners == nil {
			owners = []salesOwnerCount{}
		}
	}
	// How many new conversations arrived since the caller last looked at
	// one (opened it, pressed Seen, or opened one of its links).
	var lastSeen *time.Time
	sinceSeen := 0
	if queue == "new" && salesDesk(a, m) {
		if err = s.pool.QueryRow(r.Context(), `WITH mine AS (SELECT max(seen_at) AS at FROM sales_seen WHERE workspace_id=$1 AND brand_id=$2 AND user_id=$3)
			SELECT (SELECT at FROM mine),(SELECT count(*) FROM (SELECT 1 FROM sales_leads l JOIN records r ON r.id=l.record_id AND r.workspace_id=l.workspace_id AND r.brand_id=l.brand_id
				WHERE l.workspace_id=$1 AND l.brand_id=$2 AND l.state='new' AND r.deleted_at IS NULL AND r.merged_into IS NULL
					AND l.created_at>COALESCE((SELECT at FROM mine),'-infinity'::timestamptz) LIMIT 1000) n)`, a.WorkspaceID, a.Brand, a.UserID).Scan(&lastSeen, &sinceSeen); s.allocError(w, r, err) {
			return
		}
	}
	writeJSON(w, 200, map[string]any{"data": list, "next_cursor": next, "new_unseen": unseen, "unread": unread, "new_count": waiting, "new_owners": owners,
		"new_since_seen": sinceSeen, "last_seen_at": lastSeen})
}

// salesOwnerCount is how many new conversations one teammate is assigned.
type salesOwnerCount struct {
	OwnerID string `json:"owner_id"`
	Count   int    `json:"count"`
}

func salesHash(v string) string { d := sha256.Sum256([]byte(v)); return hex.EncodeToString(d[:]) }
func (s *Server) salesEvent(ctx context.Context, tx pgx.Tx, a *access, l salesLead, role, action string, before, after any) error {
	id := newID()
	_, err := tx.Exec(ctx, `INSERT INTO sales_events(id,workspace_id,brand_id,record_id,lead_id,actor_id,actor_role,action,content_sealed)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, a.WorkspaceID, a.Brand, l.RecordID, l.ID, nullIfEmpty(a.UserID), role, action, s.salesSeal(id, map[string]any{"before": before, "after": after, "request_id": RequestID(ctx)}))
	if err != nil {
		return err
	}
	// The record's own history links to protected evidence, never duplicates mail text.
	if err = attributeChanged(quietly(ctx), tx, a, l.RecordID, "sales_workflow", nil, map[string]any{"event_id": id, "action": action}, false, "manual"); err != nil {
		return err
	}
	// The conversation's Slack message, when the brand posts alerts, follows.
	if err = s.enqueueSalesAlert(ctx, tx, a, l.ID); err != nil {
		return err
	}
	return record(ctx, tx, &a.principal, a.WorkspaceID, "sales."+action, "sales_lead", l.ID, map[string]any{"brand_id": a.Brand, "record_id": l.RecordID, "event_id": id})
}
func (s *Server) getSalesLead(w http.ResponseWriter, r *http.Request) {
	m, ok := s.salesPerson(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	l, err := s.salesLoad(r.Context(), s.pool, a, r.PathValue("id"), false)
	if err == nil && !salesVisible(l, a, m) {
		err = errNotFound
	}
	if s.allocError(w, r, err) {
		return
	}
	messages, err := s.salesMessages(r.Context(), s.pool, a, l.ID)
	if s.allocError(w, r, err) {
		return
	}
	draft, err := s.salesDraft(r.Context(), s.pool, a, l.ID)
	if s.allocError(w, r, err) {
		return
	}
	ev := []json.RawMessage{}
	if salesManager(a, m) {
		rows, e := s.pool.Query(r.Context(), `SELECT jsonb_build_object('id',id,'action',action,'actor_id',actor_id,'actor_role',actor_role,'occurred_at',occurred_at) FROM sales_events WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=$3 ORDER BY occurred_at,id`, a.WorkspaceID, a.Brand, l.ID)
		if s.allocError(w, r, e) {
			return
		}
		ev, err = pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
		if s.allocError(w, r, err) {
			return
		}
		if ev == nil {
			ev = []json.RawMessage{}
		}
	}
	visible := []salesLead{l}
	s.salesLatestPosts(r.Context(), a, visible)
	if s.allocError(w, r, s.salesTeamSignals(r.Context(), a, visible)) {
		return
	}
	feedback, err := s.salesFeedback(r.Context(), a, l.ID)
	if s.allocError(w, r, err) {
		return
	}
	feedItems, err := s.leadFeedItems(r.Context(), a, l)
	if s.allocError(w, r, err) {
		return
	}
	s.enrichSalesLeads(r.Context(), a, visible)
	writeJSON(w, 200, map[string]any{"lead": visible[0], "messages": messages, "draft": draft, "events": ev, "feedback": feedback, "feed_items": feedItems})
}

type salesAction struct {
	FeedbackCategory string `json:"feedback_category"`
	Action           string `json:"action"`
	Version          int    `json:"version"`
	AssigneeID       string `json:"assignee_id"`
	Body             string `json:"body"`
	Note             string `json:"note"`
	Amount           *int64 `json:"amount_minor"`
	Currency         string `json:"currency"`
	Reason           string `json:"reason"`
	MessageID        string `json:"message_id"`
	Quality          string `json:"quality"`
}

// salesQualities are the marks a rep can put on a conversation.
var salesQualities = []string{"good", "poor_fit", "junk"}

// Workflow evidence references the canonical record instead of copying its
// current person/company identity into every state transition.
func salesState(l salesLead) map[string]any {
	return map[string]any{"state": l.State, "owner_id": l.OwnerID, "sdr_id": l.SDRID, "ae_id": l.AEID, "note": l.Note, "amount_minor": l.Amount, "currency": l.Currency, "confirmed_at": l.ConfirmedAt, "confirmed_by": l.ConfirmedBy, "version": l.Version}
}

func (s *Server) actSalesLead(w http.ResponseWriter, r *http.Request) {
	m, ok := s.salesPerson(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	var in salesAction
	if !decode(w, r, &in) {
		return
	}
	var l salesLead
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		l, err = s.salesLoad(r.Context(), tx, a, r.PathValue("id"), true)
		if err != nil {
			return err
		}
		visible := salesVisible(l, a, m)
		// An unassigned conversation leaves a rep's visible queue when archived. Let
		// its latest archiver undo that action without granting access to other
		// archived conversations. The version check below still prevents a stale
		// Undo from overwriting someone else's work.
		if !visible && in.Action == "reopen" && l.State == "archived" && l.OwnerID == nil && (m.Role == "sdr" || m.Role == "ae") {
			err = tx.QueryRow(r.Context(), `SELECT COALESCE((SELECT actor_id=$4::uuid FROM sales_events
			 WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=$3 AND action='lead_archived'
			 ORDER BY occurred_at DESC,id DESC LIMIT 1),false)`, a.WorkspaceID, a.Brand, l.ID, a.UserID).Scan(&visible)
			if err != nil {
				return err
			}
		}
		if !visible {
			return errNotFound
		}
		if in.Action == "read" {
			_, err = tx.Exec(r.Context(), `INSERT INTO sales_seen(workspace_id,brand_id,lead_id,user_id)VALUES($1,$2,$3,$4)ON CONFLICT(workspace_id,brand_id,lead_id,user_id)DO UPDATE SET seen_at=now()`, a.WorkspaceID, a.Brand, l.ID, a.UserID)
			if err != nil {
				return err
			}
			l.Unread = false
			// Reading is evidence, but not a change to the customer's attributes.
			id := newID()
			if _, err = tx.Exec(r.Context(), `INSERT INTO sales_events(id,workspace_id,brand_id,record_id,lead_id,actor_id,actor_role,action,content_sealed)VALUES($1,$2,$3,$4,$5,$6,$7,'lead_seen',$8)`, id, a.WorkspaceID, a.Brand, l.RecordID, l.ID, a.UserID, m.Role, s.salesSeal(id, map[string]any{})); err != nil {
				return err
			}
			if err = s.enqueueSalesAlert(r.Context(), tx, a, l.ID); err != nil {
				return err
			}
			return record(r.Context(), tx, &a.principal, a.WorkspaceID, "sales.lead_seen", "sales_lead", l.ID, map[string]any{"brand_id": a.Brand, "event_id": id})
		}
		if in.Action == "rate" {
			// A mark is a quick tap in the queue, on whatever version is on
			// screen: it changes nothing a teammate could be editing, so it
			// needs no version and does not move the conversation up the list.
			if !salesDesk(a, m) {
				return errDenied("a Sales role is required")
			}
			if in.Quality != "" && !slices.Contains(salesQualities, in.Quality) {
				return errInvalid("quality is good, poor_fit or junk, or empty to clear the mark")
			}
			was := deref(l.Quality)
			if was == in.Quality {
				return nil
			}
			if _, err = tx.Exec(r.Context(), `UPDATE sales_leads SET quality=NULLIF($4,'') WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, l.ID, in.Quality); err != nil {
				return err
			}
			l.Quality = nil
			if in.Quality != "" {
				l.Quality = &in.Quality
			}
			return s.salesEvent(r.Context(), tx, a, l, m.Role, "quality_set", map[string]any{"quality": was}, map[string]any{"quality": in.Quality})
		}
		if l.Version != in.Version {
			return recordConflict{"record_changed", "Conversation changed; reload before saving."}
		}
		before := salesState(l)
		if len(in.Body) > 20000 || len(in.Note) > 20000 || len(in.Reason) > 1000 {
			return errInvalid("text is too long")
		}
		if in.Action == "feedback" {
			if !salesManager(a, m) && m.Role != "sdr" && m.Role != "ae" {
				return errDenied("a Sales role is required")
			}
			if !slices.Contains([]string{"wrong_contact", "poor_fit", "reply_classification", "missing_information", "question", "other"}, in.FeedbackCategory) || strings.TrimSpace(in.Body) == "" || len(in.Body) > 4000 {
				return errInvalid("choose a feedback category and add a comment under 4000 characters")
			}
			return s.salesEvent(r.Context(), tx, a, l, m.Role, "feedback_added", nil, map[string]any{"feedback": map[string]string{"category": in.FeedbackCategory, "body": strings.TrimSpace(in.Body)}})
		}
		if in.Action == "assign" || in.Action == "release" {
			// New conversations can be assigned and released by any Sales teammate.
			// Once taken, only its owner or a manager can pass it to someone else,
			// and it is never released.
			active := l.State == "active" && in.Action == "assign"
			if active {
				if deref(l.OwnerID) != a.UserID && !salesManager(a, m) {
					return errDenied("only the conversation owner or a manager can reassign it")
				}
			} else if l.State != "new" || (!salesManager(a, m) && m.Role != "sdr" && m.Role != "ae") {
				return errDenied("only new conversations can be assigned")
			}
			before := salesState(l)
			previous := deref(l.OwnerID)
			if !active {
				l.OwnerID, l.SDRID, l.AEID = nil, nil, nil
			}
			if in.Action == "assign" {
				if !validUUID(in.AssigneeID) {
					return errInvalid("choose a Sales teammate")
				}
				target, e := salesMemberAt(r.Context(), tx, a, in.AssigneeID)
				if e != nil {
					return e
				}
				if target.Role != "sdr" && target.Role != "ae" {
					return errInvalid("choose an SDR or account manager")
				}
				if active {
					// Already theirs: nothing changes, so no version bump and no event.
					if target.UserID == previous {
						l, err = s.salesLoad(r.Context(), tx, a, l.ID, false)
						return err
					}
					// An email still going out was written for the current owner (a
					// hand-off names its account manager). Passing the conversation on
					// now would leave it to complete for the wrong person. A message
					// that definitely failed sent nothing, so it does not block.
					var sending bool
					if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM sales_messages WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=$3 AND status IN ('queued','sending','uncertain'))`, a.WorkspaceID, a.Brand, l.ID).Scan(&sending); err != nil {
						return err
					}
					if sending {
						return recordConflict{"conflict", "An email for this conversation is still being sent. Try again once it has gone."}
					}
				}
				l.OwnerID = &target.UserID
				// The target's own role slot is set; on an active conversation the
				// other slot stays, as in a hand-off, so the introducing SDR keeps it.
				if target.Role == "sdr" {
					l.SDRID = &target.UserID
				} else {
					l.AEID = &target.UserID
				}
			}
			_, err = tx.Exec(r.Context(), `UPDATE sales_leads SET owner_id=$4,sdr_id=$5,ae_id=$6,version=version+1,updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, l.ID, l.OwnerID, l.SDRID, l.AEID)
			if err != nil {
				return err
			}
			action := "lead_assigned"
			if in.Action == "release" {
				action = "assignment_released"
			}
			// The person's Owner follows, in their own history as the caller.
			if in.Action == "assign" {
				err = salesSetOwner(r.Context(), tx, a, l.RecordID, *l.OwnerID)
			} else if previous != "" {
				err = salesClearOwner(r.Context(), tx, a, l.RecordID, previous)
			}
			if err != nil {
				return err
			}
			if err = s.salesEvent(r.Context(), tx, a, l, m.Role, action, before, salesState(l)); err != nil {
				return err
			}
			l, err = s.salesLoad(r.Context(), tx, a, l.ID, false)
			return err
		}
		if in.Action != "take" && deref(l.OwnerID) != a.UserID && !(in.Action == "details" && salesVisible(l, a, m)) && !(in.Action == "archive" && l.State == "new" && (l.OwnerID == nil || salesManager(a, m))) && !(in.Action == "reopen" && l.OwnerID == nil) {
			return errDenied("only the conversation owner can perform this action")
		}
		switch in.Action {
		case "retry_send":
			if !validUUID(in.MessageID) || !m.Connected || !slices.Contains([]string{"active", "customer"}, l.State) {
				return errInvalid("connect your email and reopen this conversation before retrying")
			}
			var pending bool
			if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM sales_messages WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=$3 AND status IN ('queued','sending','uncertain'))`, a.WorkspaceID, a.Brand, l.ID).Scan(&pending); err != nil {
				return err
			}
			if pending {
				return recordConflict{"conflict", "A send is pending or needs reconciliation. Resolve it before retrying."}
			}
			tag, e := tx.Exec(r.Context(), `UPDATE sales_messages SET status='queued' WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=$3 AND id=$4 AND sender_id=$5 AND status='failed'`, a.WorkspaceID, a.Brand, l.ID, in.MessageID, a.UserID)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return errInvalid("only a definitely failed send can be retried")
			}
			_, err = jobs.Enqueue(r.Context(), tx, jobs.NewJob{Kind: jobSalesSend, WorkspaceID: &a.WorkspaceID, Payload: map[string]string{"id": in.MessageID, "brand_id": a.Brand}, MaxAttempts: 5, IdempotencyKey: in.MessageID + ":retry:" + newID()})
			if err != nil {
				return err
			}
		case "take":
			if l.OwnerID != nil && *l.OwnerID != a.UserID {
				return errDenied("only the assigned person can send the introduction")
			}
			if l.State != "new" || !slices.Contains([]string{"sdr", "ae"}, m.Role) {
				return errInvalid("only an SDR or account manager can take a new conversation")
			}
			if !m.Connected {
				return errInvalid("connect your own email in Sales → My email")
			}
			l.OwnerID = &a.UserID
			l.State = "active"
			if m.Role == "sdr" {
				l.SDRID = &a.UserID
			} else {
				l.AEID = &a.UserID
			}
			if err = s.salesQueueIntroduction(r.Context(), tx, a, l, m, "introduction", in.Body); err != nil {
				return err
			}
		case "handoff":
			if m.Role != "sdr" || l.State != "active" || !validUUID(in.AssigneeID) {
				return errInvalid("choose an account manager for an active conversation")
			}
			target, e := salesMemberAt(r.Context(), tx, a, in.AssigneeID)
			if e != nil {
				return e
			}
			if target.Role != "ae" || !target.Connected {
				return errInvalid("the account manager must connect their own email first")
			}
			// Retain the introducing SDR even if their role changed after they
			// took the conversation as an account manager.
			l.SDRID = &a.UserID
			if err = s.salesQueueIntroduction(r.Context(), tx, a, l, target, "handoff", in.Body); err != nil {
				return err
			}
			// Ownership changes after the provider accepts the introduction, keeping failures actionable.
		case "archive":
			if l.State == "archived" {
				return errInvalid("conversation is already archived")
			}
			l.State = "archived"
		case "reopen":
			if !slices.Contains([]string{"archived", "customer"}, l.State) {
				return errInvalid("only archived conversations and confirmed customers can be reopened")
			}
			l.State = "active"
			var started bool
			if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM sales_events WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=$3 AND action='lead_taken')`, a.WorkspaceID, a.Brand, l.ID).Scan(&started); err != nil {
				return err
			}
			if l.OwnerID == nil || !started {
				l.State = "new"
			}
			l.ConfirmedAt = nil
			l.ConfirmedBy = nil
		case "confirm_customer":
			if m.Role != "ae" || l.State != "active" {
				return errInvalid("the owning account manager can confirm an active conversation")
			}
			now := time.Now()
			l.State = "customer"
			l.ConfirmedAt = &now
			l.ConfirmedBy = &a.UserID
			if in.Amount != nil {
				l.Amount = in.Amount
			}
		case "details":
			l.Note = in.Note
			l.Amount = in.Amount
			if in.Currency != "" {
				l.Currency = in.Currency
			}
		default:
			return errInvalid("unknown Sales action")
		}
		if l.Amount != nil && *l.Amount < 0 {
			return errInvalid("amount must be nonnegative")
		}
		if len(l.Currency) != 3 || strings.Trim(l.Currency, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
			return errInvalid("currency must be a three-letter code")
		}
		_, err = tx.Exec(r.Context(), `UPDATE sales_leads SET state=$4,owner_id=$5,sdr_id=$6,ae_id=$7,details_sealed=$8,amount_minor=$9,currency=$10,confirmed_at=$11,confirmed_by=$12,version=version+1,updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, l.ID, l.State, l.OwnerID, l.SDRID, l.AEID, s.salesSeal(l.ID, l.Note), l.Amount, l.Currency, l.ConfirmedAt, l.ConfirmedBy)
		if err != nil {
			return err
		}
		// The CRM follows the conversation: its owner is the person's Owner,
		// and a confirmed customer's deal is won.
		switch in.Action {
		case "take":
			err = salesSetOwner(r.Context(), tx, a, l.RecordID, a.UserID)
		case "confirm_customer":
			err = salesWinDeal(r.Context(), tx, a, l.RecordID)
		}
		if err != nil {
			return err
		}
		action := map[string]string{"take": "lead_taken", "handoff": "handoff_queued", "archive": "lead_archived", "reopen": "lead_reopened", "confirm_customer": "customer_confirmed", "details": "details_updated", "retry_send": "send_retry_queued"}[in.Action]
		if err = s.salesEvent(r.Context(), tx, a, l, m.Role, action, before, map[string]any{"lead": salesState(l), "reason": in.Reason, "message_id": in.MessageID}); err != nil {
			return err
		}
		l, err = s.salesLoad(r.Context(), tx, a, l.ID, false)
		return err
	})
	if !s.recordError(w, r, err) {
		writeJSON(w, 200, l)
	}
}

const jobSalesSend = "sales.send"
const jobSalesSync = "sales.sync"

func (s *Server) enqueueSales(ctx context.Context, q jobs.Querier, a *access, kind, id string, at time.Time) error {
	key := id
	if kind == jobSalesSync {
		key += fmt.Sprintf(":%d", at.Unix()/60)
	}
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: kind, WorkspaceID: &a.WorkspaceID, Payload: map[string]string{"id": id, "brand_id": a.Brand}, RunAt: at, MaxAttempts: 5, IdempotencyKey: key})
	return err
}

func (s *Server) markSalesQueueSeen(w http.ResponseWriter, r *http.Request) {
	m, ok := s.salesPerson(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	_, err := s.pool.Exec(r.Context(), `INSERT INTO sales_members(workspace_id,brand_id,user_id,role,queue_seen_at)VALUES($1,$2,$3,$4,now())ON CONFLICT(workspace_id,brand_id,user_id)DO UPDATE SET queue_seen_at=now()`, a.WorkspaceID, a.Brand, a.UserID, m.Role)
	if !s.allocError(w, r, err) {
		writeJSON(w, 200, map[string]bool{"seen": true})
	}
}
