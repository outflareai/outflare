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
		{"POST", "/queue-seen", auth.ScopeCustomersManage, s.markSalesQueueSeen},
		{"GET", "/me", auth.ScopeCustomersRead, s.getSalesMe}, {"PUT", "/me", auth.ScopeCustomersManage, s.putSalesMe},
		{"GET", "/team", auth.ScopeCustomersRead, s.listSalesTeam}, {"PUT", "/team/{user}", auth.ScopeMembersManage, s.putSalesRole},
		{"GET", "/leads", auth.ScopeCustomersRead, s.listSalesLeads}, {"GET", "/leads/{id}", auth.ScopeCustomersRead, s.getSalesLead},
		{"POST", "/leads/{id}/actions", auth.ScopeCustomersManage, s.actSalesLead},
		{"PUT", "/leads/{id}/draft", auth.ScopeCustomersManage, s.putSalesDraft}, {"POST", "/leads/{id}/send", auth.ScopeCustomersManage, s.sendSalesReply},
		{"GET", "/templates", auth.ScopeCustomersRead, s.listSalesTemplates}, {"POST", "/templates", auth.ScopeCustomersManage, s.putSalesTemplate}, {"PUT", "/templates/{id}", auth.ScopeCustomersManage, s.putSalesTemplate},
		{"POST", "/bridge", auth.ScopeCustomersManage, s.importSalesLead},
		{"POST", "/mail/authorize", auth.ScopeCustomersManage, s.authorizeSalesGoogle}, {"DELETE", "/mail/connection", auth.ScopeCustomersRead, s.disconnectSalesGoogle}, {"POST", "/mail/sync", auth.ScopeCustomersManage, s.queueSalesSync},
	} {
		mux.HandleFunc(rt.method+" "+b+rt.path, s.onBrand(rt.scope, rt.handler))
	}
	mux.HandleFunc("GET /v1/sales/google/callback", s.requireUser(s.finishSalesGoogle))
}

type salesMember struct {
	UserID       string     `json:"user_id"`
	Name         string     `json:"name"`
	Role         string     `json:"role"`
	Introduction string     `json:"introduction"`
	Connected    bool       `json:"connected"`
	MailStatus   string     `json:"mail_status"`
	LastSynced   *time.Time `json:"last_synced_at"`
	OpenLeads    int        `json:"open_leads"`
}

func salesMemberAt(ctx context.Context, q mailQuery, a *access, user string) (salesMember, error) {
	var v salesMember
	err := q.QueryRow(ctx, `SELECT u.id::text,u.name,COALESCE(sm.role,'none'),COALESCE(sm.introduction,''),COALESCE(c.status='connected',false),COALESCE(c.error_code,c.status,'not_connected'),c.last_synced_at,
 (SELECT count(*) FROM sales_leads l WHERE l.workspace_id=$1 AND l.brand_id=$2 AND l.owner_id=u.id AND l.state='active')
 FROM memberships m JOIN users u ON u.id=m.user_id LEFT JOIN sales_members sm ON sm.workspace_id=m.workspace_id AND sm.brand_id=$2 AND sm.user_id=u.id
 LEFT JOIN sales_mail_connections c ON c.workspace_id=m.workspace_id AND c.brand_id=$2 AND c.owner_id=u.id
 WHERE m.workspace_id=$1 AND u.id=$3 AND u.disabled_at IS NULL AND (m.brand_ids IS NULL OR cardinality(m.brand_ids)=0 OR $2::uuid=ANY(m.brand_ids))`, a.WorkspaceID, a.Brand, user).Scan(&v.UserID, &v.Name, &v.Role, &v.Introduction, &v.Connected, &v.MailStatus, &v.LastSynced, &v.OpenLeads)
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
	if ok {
		writeJSON(w, 200, m)
	}
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
	ID            string               `json:"id"`
	RecordID      string               `json:"record_id"`
	Source        string               `json:"source"`
	SourceID      string               `json:"source_id"`
	IntegrationID *string              `json:"-"`
	State         string               `json:"state"`
	OwnerID       *string              `json:"owner_id"`
	SDRID         *string              `json:"sdr_id"`
	AEID          *string              `json:"ae_id"`
	Name          string               `json:"name"`
	Email         string               `json:"email"`
	Company       string               `json:"company"`
	JobTitle      string               `json:"job_title"`
	Website       string               `json:"website"`
	LinkedInURL   string               `json:"linkedin_url"`
	LatestMessage *salesMessagePreview `json:"latest_message,omitempty"`
	LatestReply   *salesMessagePreview `json:"latest_reply,omitempty"`
	LastActivity  *salesLeadActivity   `json:"last_activity,omitempty"`
	Version       int                  `json:"version"`
	Note          string               `json:"note"`
	Amount        *int64               `json:"amount_minor"`
	Currency      string               `json:"currency"`
	Unread        bool                 `json:"unread"`
	ReplyState    string               `json:"reply_state"`
	CreatedAt     time.Time            `json:"created_at"`
	UpdatedAt     time.Time            `json:"updated_at"`
	IntroducedAt  *time.Time           `json:"introduced_at"`
	IntroducedBy  *string              `json:"introduced_by"`
	ConfirmedAt   *time.Time           `json:"confirmed_at"`
	ConfirmedBy   *string              `json:"confirmed_by"`
}

const salesLeadSelect = `SELECT (to_jsonb(l)-'source_sealed'-'details_sealed') || jsonb_build_object(
 'name',r.name,
 'email',COALESCE((SELECT i.value FROM record_identities i WHERE i.workspace_id=l.workspace_id AND i.brand_id=l.brand_id AND i.record_id=r.id AND i.namespace='email' AND i.valid_to IS NULL ORDER BY i.verified DESC,i.valid_from DESC LIMIT 1),''),
 'company',COALESCE(c.name,''),
 'job_title',COALESCE(r.attributes->>'job_title',''),
 'website',COALESCE(NULLIF(c.attributes->>'website',''),r.attributes->>'website',''),
 'linkedin_url',COALESCE(r.attributes->>'linkedin_url',''),
 'unread',l.updated_at>COALESCE((SELECT seen_at FROM sales_seen v WHERE v.workspace_id=l.workspace_id AND v.brand_id=l.brand_id AND v.lead_id=l.id AND v.user_id=NULLIF($3,'')::uuid),'-infinity'::timestamptz)),
 l.details_sealed,l.integration_id::text
 FROM sales_leads l JOIN records r ON r.id=l.record_id AND r.workspace_id=l.workspace_id AND r.brand_id=l.brand_id
 LEFT JOIN records c ON c.workspace_id=l.workspace_id AND c.brand_id=l.brand_id AND c.id::text=r.attributes->'companies'->>0 AND c.deleted_at IS NULL AND c.merged_into IS NULL
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
		writeError(w, r, 403, "forbidden", "All leads is for Sales managers")
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
	if state := r.URL.Query().Get("state"); state != "" {
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
	var unseen, unread bool
	err = s.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM sales_leads l WHERE l.workspace_id=$1 AND l.brand_id=$2 AND l.state='new' AND l.created_at>COALESCE((SELECT queue_seen_at FROM sales_members WHERE workspace_id=$1 AND brand_id=$2 AND user_id=$3),'-infinity'::timestamptz)),EXISTS(SELECT 1 FROM sales_leads l WHERE l.workspace_id=$1 AND l.brand_id=$2 AND l.owner_id=$3 AND l.state IN ('active','customer') AND l.updated_at>COALESCE((SELECT seen_at FROM sales_seen WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=l.id AND user_id=$3),'-infinity'::timestamptz))`, a.WorkspaceID, a.Brand, a.UserID).Scan(&unseen, &unread)
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, map[string]any{"data": list, "next_cursor": next, "new_unseen": unseen, "unread": unread})
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
	writeJSON(w, 200, map[string]any{"lead": l, "messages": messages, "draft": draft, "events": ev})
}

type salesAction struct {
	Action     string `json:"action"`
	Version    int    `json:"version"`
	AssigneeID string `json:"assignee_id"`
	Body       string `json:"body"`
	Note       string `json:"note"`
	Amount     *int64 `json:"amount_minor"`
	Currency   string `json:"currency"`
	Reason     string `json:"reason"`
	MessageID  string `json:"message_id"`
}

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
		if !salesVisible(l, a, m) {
			return errNotFound
		}
		if in.Action == "read" {
			_, err = tx.Exec(r.Context(), `INSERT INTO sales_seen(workspace_id,brand_id,lead_id,user_id)VALUES($1,$2,$3,$4)ON CONFLICT(workspace_id,brand_id,lead_id,user_id)DO UPDATE SET seen_at=now()`, a.WorkspaceID, a.Brand, l.ID, a.UserID)
			if err != nil {
				return err
			}
			l.Unread = false
			return err
		}
		if l.Version != in.Version {
			return recordConflict{"record_changed", "Lead changed; reload before saving."}
		}
		before := salesState(l)
		if len(in.Body) > 20000 || len(in.Note) > 20000 || len(in.Reason) > 1000 {
			return errInvalid("text is too long")
		}
		if in.Action != "take" && deref(l.OwnerID) != a.UserID && !(in.Action == "details" && salesVisible(l, a, m)) && !(in.Action == "archive" && l.State == "new") && !(in.Action == "reopen" && l.OwnerID == nil) {
			return errDenied("only the lead owner can perform this action")
		}
		switch in.Action {
		case "retry_send":
			if !validUUID(in.MessageID) || !m.Connected || !slices.Contains([]string{"active", "customer"}, l.State) {
				return errInvalid("connect your email and reopen this lead before retrying")
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
			if l.State != "new" || !slices.Contains([]string{"sdr", "ae"}, m.Role) {
				return errInvalid("only an SDR or account manager can take a new lead")
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
				return errInvalid("choose an account manager for an active lead")
			}
			target, e := salesMemberAt(r.Context(), tx, a, in.AssigneeID)
			if e != nil {
				return e
			}
			if target.Role != "ae" || !target.Connected {
				return errInvalid("the account manager must connect their own email first")
			}
			// Retain the introducing SDR even if their role changed after they
			// took the lead as an account manager.
			l.SDRID = &a.UserID
			if err = s.salesQueueIntroduction(r.Context(), tx, a, l, target, "handoff", in.Body); err != nil {
				return err
			}
			// Ownership changes after the provider accepts the introduction, keeping failures actionable.
		case "archive":
			if l.State == "archived" {
				return errInvalid("lead is already archived")
			}
			l.State = "archived"
		case "reopen":
			if !slices.Contains([]string{"archived", "customer"}, l.State) {
				return errInvalid("only archived leads and confirmed customers can be reopened")
			}
			l.State = "active"
			if l.OwnerID == nil {
				l.State = "new"
			}
			l.ConfirmedAt = nil
			l.ConfirmedBy = nil
		case "confirm_customer":
			if m.Role != "ae" || l.State != "active" {
				return errInvalid("the owning account manager can confirm an active lead")
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
