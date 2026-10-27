package api

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/inbox"
	"github.com/outflareai/outflare/engine/internal/mailbox"
	"github.com/outflareai/outflare/engine/internal/sending"
)

func (s *Server) routesInbox(mux *http.ServeMux) {
	read, manage := auth.ScopeInboxRead, auth.ScopeInboxManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/inbox", s.inBrand(read, s.listInbox))
	mux.HandleFunc("GET /v1/workspaces/{ws}/inbox/{id}", s.inBrand(read, s.getInbound))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/inbox/{id}", s.inBrand(manage, s.updateInbound))
	mux.HandleFunc("POST /v1/workspaces/{ws}/inbox/{id}/reply", s.inBrand(manage, s.replyInbound))
	mux.HandleFunc("POST /v1/workspaces/{ws}/mailboxes/{id}/sync", s.inWorkspace(auth.ScopeMailboxesManage, s.syncMailbox))
}

type inboundView struct {
	ID           string    `json:"id"`
	MailboxID    string    `json:"mailbox_id"`
	MailboxEmail string    `json:"mailbox_email"`
	From         string    `json:"from"`
	FromName     string    `json:"from_name"`
	To           string    `json:"to"`
	Subject      string    `json:"subject"`
	Body         string    `json:"body"`
	Kind         string    `json:"kind"`
	OptOut       bool      `json:"opt_out"`
	Read         bool      `json:"read"`
	LeadID       *string   `json:"lead_id"`
	LeadName     *string   `json:"lead_name"`
	Company      *string   `json:"company"`
	SequenceID   *string   `json:"sequence_id"`
	SequenceName *string   `json:"sequence_name"`
	ReceivedAt   time.Time `json:"received_at"`
	// Triage.
	Category       *string    `json:"category"`
	CategorySource *string    `json:"category_source"`
	AssignedTo     *string    `json:"assigned_to"`
	AssigneeName   *string    `json:"assignee_name"`
	SnoozedUntil   *time.Time `json:"snoozed_until"`
	Done           bool       `json:"done"`
	Labels         []string   `json:"labels"`
	// Unified inbox and labelling.
	Source             string         `json:"source"`
	IntegrationID      *string        `json:"integration_id"`
	CategoryConfidence *float32       `json:"category_confidence"`
	CategoryReason     *string        `json:"category_reason"`
	Details            map[string]any `json:"details"`
	NeedsReview        bool           `json:"needs_review"`
	ClassifiedAt       *time.Time     `json:"classified_at"`
	ProjectID          *string        `json:"project_id"`
	BrandID            *string        `json:"brand_id"`
	Group              *string        `json:"group"`
}

// withGroup fills in the group from the category.
func (v *inboundView) withGroup() {
	v.Group = nil
	if v.Category != nil {
		if g := inbox.GroupOf(*v.Category); g != "" {
			v.Group = &g
		}
	}
}

const inboundSelect = `
	SELECT i.id, i.mailbox_id, m.email, i.from_email, i.from_name, i.to_email, i.subject, i.body, i.kind, i.opt_out, i.read,
	       i.lead_id, NULLIF(trim(l.first_name || ' ' || l.last_name), ''), NULLIF(l.company, ''), i.sequence_id, q.name, i.received_at,
	       i.category, i.category_source, i.assigned_to, u.name, i.snoozed_until, i.done, i.labels,
	       i.source, i.integration_id, i.category_confidence, i.category_reason, i.details, i.needs_review, i.classified_at,
	       COALESCE(q.project_id, m.project_id), COALESCE(q.brand_id, m.brand_id), NULL::text
	FROM inbound_messages i
	JOIN mailboxes m ON m.id = i.mailbox_id
	LEFT JOIN leads l ON l.id = i.lead_id
	LEFT JOIN sequences q ON q.id = i.sequence_id
	LEFT JOIN users u ON u.id = i.assigned_to`

func (s *Server) listInbox(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be 1 to 200")
			return
		}
		limit = n
	}
	where := ` WHERE i.workspace_id = $1`
	args := []any{a.WorkspaceID}
	arg := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }
	// By default the inbox shows mail about leads: replies and auto-replies.
	switch v := q.Get("kind"); v {
	case "":
		where += ` AND i.kind IN ('reply', 'auto_reply')`
	case "all":
	case "reply", "auto_reply", "bounce", "other":
		where += ` AND i.kind = ` + arg(v)
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind must be reply, auto_reply, bounce, other or all")
		return
	}
	for _, f := range []string{"mailbox_id", "lead_id", "sequence_id"} {
		if v := q.Get(f); v != "" {
			if !validUUID(v) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", f+" must be a UUID")
				return
			}
			where += ` AND i.` + f + ` = ` + arg(v)
		}
	}
	if q.Get("unread") == "true" {
		where += ` AND NOT i.read`
	}
	// Triage: open is what still needs someone (not done, not snoozed).
	switch q.Get("status") {
	case "", "all":
	case "open":
		where += ` AND NOT i.done AND (i.snoozed_until IS NULL OR i.snoozed_until <= now())`
	case "snoozed":
		where += ` AND NOT i.done AND i.snoozed_until > now()`
	case "done":
		where += ` AND i.done`
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status must be open, snoozed, done or all")
		return
	}
	// category takes one label, several separated by commas, or none for
	// replies nobody has labelled; group takes hot, later, closed or
	// automatic (also comma separated).
	if v := q.Get("category"); v != "" {
		var cats []string
		none := false
		for _, c := range strings.Split(v, ",") {
			switch c = strings.TrimSpace(c); {
			case c == "none":
				none = true
			case slices.Contains(inbox.Categories, c):
				cats = append(cats, c)
			default:
				writeError(w, r, http.StatusBadRequest, "invalid_request", "category must be none or one of "+strings.Join(inbox.Categories, ", "))
				return
			}
		}
		switch {
		case none && len(cats) > 0:
			where += ` AND (i.category IS NULL OR i.category = ANY(` + arg(cats) + `))`
		case none:
			where += ` AND i.category IS NULL`
		default:
			where += ` AND i.category = ANY(` + arg(cats) + `)`
		}
	}
	if v := q.Get("group"); v != "" {
		var cats []string
		for _, g := range strings.Split(v, ",") {
			cs, ok := inbox.Groups[strings.TrimSpace(g)]
			if !ok {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "group must be one or more of "+strings.Join(inbox.GroupNames, ", "))
				return
			}
			cats = append(cats, cs...)
		}
		where += ` AND i.category = ANY(` + arg(cats) + `)`
	}
	if v := strings.TrimSpace(strings.ToLower(q.Get("source"))); v != "" {
		where += ` AND i.source = ANY(` + arg(strings.Split(v, ",")) + `)`
	}
	switch q.Get("needs_review") {
	case "":
	case "true":
		where += ` AND i.needs_review`
	case "false":
		where += ` AND NOT i.needs_review`
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "needs_review must be true or false")
		return
	}
	if v := q.Get("project_id"); v != "" {
		if !validUUID(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "project_id must be a UUID")
			return
		}
		where += ` AND COALESCE(q.project_id, m.project_id) = ` + arg(v)
	}
	if v := q.Get("min_confidence"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 || f > 1 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "min_confidence must be a number from 0 to 1")
			return
		}
		where += ` AND i.category_confidence >= ` + arg(f)
	}
	switch v := q.Get("assigned_to"); {
	case v == "":
	case v == "none":
		where += ` AND i.assigned_to IS NULL`
	case v == "me":
		if a.UserID == "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "assigned_to=me needs a signed-in person; API tokens pass a user id")
			return
		}
		where += ` AND i.assigned_to = ` + arg(a.UserID)
	case validUUID(v):
		where += ` AND i.assigned_to = ` + arg(v)
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "assigned_to must be me, none or a user id")
		return
	}
	// tag (or label, its older name) takes one tag, or several separated by
	// commas for replies with any of them.
	tag := q.Get("tag")
	if tag == "" {
		tag = q.Get("label")
	}
	if v := strings.TrimSpace(strings.ToLower(tag)); v != "" {
		var tags []string
		for _, t := range strings.Split(v, ",") {
			if t = strings.TrimSpace(t); t != "" {
				tags = append(tags, t)
			}
		}
		where += ` AND i.labels && ` + arg(tags)
	}
	if v := strings.TrimSpace(q.Get("q")); v != "" {
		p := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.ToLower(v)) + "%"
		n := arg(p)
		where += ` AND (lower(i.from_email) LIKE ` + n + ` OR lower(i.subject) LIKE ` + n + ` OR lower(i.from_name) LIKE ` + n +
			` OR lower(i.body) LIKE ` + n + `)`
	}
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	where += brand.sql("m.brand_id", &args)
	if v := q.Get("before"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "before must be an RFC 3339 time")
			return
		}
		where += ` AND i.received_at < ` + arg(t)
	}
	rows, err := s.pool.Query(r.Context(), inboundSelect+where+` ORDER BY i.received_at DESC LIMIT `+arg(limit+1), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[inboundView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for i := range list {
		list[i].withGroup()
	}
	var next *string
	if len(list) > limit {
		list = list[:limit]
		t := list[limit-1].ReceivedAt.Format(time.RFC3339Nano)
		next = &t
	}
	var unread int
	uargs := []any{a.WorkspaceID}
	if err := s.pool.QueryRow(r.Context(), `SELECT count(*) FROM inbound_messages i JOIN mailboxes m ON m.id = i.mailbox_id
		WHERE i.workspace_id = $1 AND NOT i.read AND NOT i.done AND i.kind = 'reply'`+brand.sql("m.brand_id", &uargs), uargs...).Scan(&unread); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next, "unread_replies": unread})
}

// threadEntry is one message in a conversation, in either direction.
type threadEntry struct {
	ID        string    `json:"id"`
	Direction string    `json:"direction"` // in or out
	From      string    `json:"from"`
	To        string    `json:"to"`
	Subject   string    `json:"subject"`
	Body      string    `json:"body"`
	Status    string    `json:"status"`
	At        time.Time `json:"at"`
}

// inboundBrand checks a brand-limited caller may see the message {id}, by
// its mailbox's brand, and change it when write is set. Anyone else passes.
func (s *Server) inboundBrand(w http.ResponseWriter, r *http.Request, a *access, write bool) bool {
	if a.Brands == nil {
		return true
	}
	id := r.PathValue("id")
	var brand *string
	err := pgx.ErrNoRows
	if validUUID(id) {
		err = s.pool.QueryRow(r.Context(), `SELECT m.brand_id::text FROM inbound_messages i JOIN mailboxes m ON m.id = i.mailbox_id
			WHERE i.id = $1 AND i.workspace_id = $2`, id, a.WorkspaceID).Scan(&brand)
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(nil, brand)):
		writeError(w, r, http.StatusNotFound, "not_found", "message not found")
	case err != nil:
		s.internal(w, r, err)
	case write && !a.writes(nil, brand):
		writeError(w, r, http.StatusForbidden, "forbidden", "this came to a mailbox every brand shares; only someone with access to every brand can change it")
	default:
		return true
	}
	return false
}

func (s *Server) findInbound(w http.ResponseWriter, r *http.Request, ws string) (*inboundView, bool) {
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "message not found")
		return nil, false
	}
	if !s.inboundBrand(w, r, accessFrom(r.Context()), false) {
		return nil, false
	}
	rows, err := s.pool.Query(r.Context(), inboundSelect+` WHERE i.id = $1 AND i.workspace_id = $2`, id, ws)
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	v, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[inboundView])
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "message not found")
		return nil, false
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	v.withGroup()
	return &v, true
}

// getInbound returns a received message with its conversation: everything
// sent to and received from the lead through that mailbox, oldest first.
func (s *Server) getInbound(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.findInbound(w, r, a.WorkspaceID)
	if !ok {
		return
	}
	var rows pgx.Rows
	var err error
	if v.LeadID != nil {
		rows, err = s.pool.Query(r.Context(), `
			SELECT id, 'out', from_email, to_email, subject, body, status, COALESCE(sent_at, created_at) FROM messages
			WHERE workspace_id = $1 AND lead_id = $2 AND mailbox_id = $3 AND status IN ('sent', 'unknown', 'failed')
			UNION ALL
			SELECT id, 'in', from_email, to_email, subject, body, kind, received_at FROM inbound_messages
			WHERE workspace_id = $1 AND lead_id = $2 AND mailbox_id = $3
			ORDER BY 8 LIMIT 200`, a.WorkspaceID, *v.LeadID, v.MailboxID)
	} else {
		rows, err = s.pool.Query(r.Context(), `
			SELECT id, 'in', from_email, to_email, subject, body, kind, received_at FROM inbound_messages WHERE id = $1
			UNION ALL
			SELECT id, 'out', from_email, to_email, subject, body, status, COALESCE(sent_at, created_at) FROM messages
			WHERE reply_to_inbound_id = $1
			ORDER BY 8`, v.ID)
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	thread, err := pgx.CollectRows(rows, pgx.RowToStructByPos[threadEntry])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": v, "thread": thread})
}

func (s *Server) updateInbound(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !s.inboundBrand(w, r, a, true) {
		return
	}
	var req struct {
		Read         *bool               `json:"read"`
		Category     optional[string]    `json:"category"`
		AssignedTo   optional[string]    `json:"assigned_to"`
		SnoozedUntil optional[time.Time] `json:"snoozed_until"`
		Done         *bool               `json:"done"`
		Labels       *[]string           `json:"labels"`
		NeedsReview  *bool               `json:"needs_review"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Read == nil && !req.Category.Set && !req.AssignedTo.Set && !req.SnoozedUntil.Set && req.Done == nil && req.Labels == nil && req.NeedsReview == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "nothing to change: send read, category, assigned_to, snoozed_until, done, labels or needs_review")
		return
	}
	if req.Category.Value != nil && !slices.Contains(inbox.Categories, *req.Category.Value) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "category must be null or one of "+strings.Join(inbox.Categories, ", "))
		return
	}
	if req.AssignedTo.Value != nil && !validUUID(*req.AssignedTo.Value) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "assigned_to must be a user id or null")
		return
	}
	if req.SnoozedUntil.Value != nil && !req.SnoozedUntil.Value.After(time.Now()) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "snoozed_until must be in the future, or null to wake it now")
		return
	}
	var labels []string
	if req.Labels != nil {
		labels = []string{}
		for _, l := range *req.Labels {
			l = strings.ToLower(strings.TrimSpace(l))
			if l == "" || len(l) > 40 {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "each label must be 1 to 40 characters")
				return
			}
			if !slices.Contains(labels, l) {
				labels = append(labels, l)
			}
		}
		if len(labels) > 20 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "at most 20 labels")
			return
		}
	}
	v, ok := s.findInbound(w, r, a.WorkspaceID)
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if req.AssignedTo.Value != nil {
			var member bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM memberships WHERE workspace_id = $1 AND user_id = $2)`, a.WorkspaceID, *req.AssignedTo.Value).Scan(&member); err != nil {
				return err
			}
			if !member {
				return errInvalid("assigned_to is not a member of this workspace")
			}
		}
		set := []string{}
		args := []any{v.ID}
		arg := func(x any) string { args = append(args, x); return "$" + strconv.Itoa(len(args)) }
		changed := map[string]any{}
		if req.Read != nil {
			set = append(set, "read = "+arg(*req.Read))
		}
		if req.Category.Set {
			// A person or agent deciding is sure, and settles the review.
			c := arg(req.Category.Value)
			set = append(set, "category = "+c, "category_source = CASE WHEN "+c+"::text IS NULL THEN NULL ELSE "+arg(categorySource(a.ActorType))+" END",
				"category_confidence = CASE WHEN "+c+"::text IS NULL THEN NULL ELSE 1 END", "category_reason = NULL",
				"classified_at = CASE WHEN "+c+"::text IS NULL THEN NULL ELSE now() END")
			if req.NeedsReview == nil {
				set = append(set, "needs_review = false")
			}
			changed["category"] = req.Category.Value
		}
		if req.NeedsReview != nil {
			set = append(set, "needs_review = "+arg(*req.NeedsReview))
			changed["needs_review"] = *req.NeedsReview
		}
		if req.AssignedTo.Set {
			set = append(set, "assigned_to = "+arg(req.AssignedTo.Value))
			changed["assigned_to"] = req.AssignedTo.Value
		}
		if req.SnoozedUntil.Set {
			set = append(set, "snoozed_until = "+arg(req.SnoozedUntil.Value))
			changed["snoozed_until"] = req.SnoozedUntil.Value
		}
		if req.Done != nil {
			set = append(set, "done = "+arg(*req.Done))
			changed["done"] = *req.Done
		}
		if req.Labels != nil {
			set = append(set, "labels = "+arg(labels))
			changed["labels"] = labels
			// A new tag joins the workspace's list, so it can be picked
			// and coloured later.
			if _, err := tx.Exec(ctx, `INSERT INTO inbox_tags (workspace_id, name) SELECT $1, unnest($2::text[]) ON CONFLICT DO NOTHING`,
				a.WorkspaceID, labels); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE inbound_messages SET `+strings.Join(set, ", ")+` WHERE id = $1`, args...); err != nil {
			return err
		}
		// Marking read is too routine to audit; triage changes are not.
		if len(changed) == 0 {
			return nil
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "inbox.updated", "inbound_message", v.ID, changed); err != nil {
			return err
		}
		if !req.Category.Set || equalPtr(req.Category.Value, v.Category) {
			// Confirming a label that waited for review runs its rules.
			if v.NeedsReview && req.NeedsReview != nil && !*req.NeedsReview && v.Category != nil {
				return inbox.QueueRules(ctx, tx, a.WorkspaceID, v.ID, *v.Category)
			}
			return nil
		}
		// Saying an opt-out wasn't one undoes what it did.
		if inbox.IsOptOut(v.Category) && !inbox.IsOptOut(req.Category.Value) {
			lifted, err := inbox.UndoOptOut(ctx, tx, a.WorkspaceID, v.ID, v.LeadID, v.From)
			if err != nil {
				return err
			}
			if lifted {
				if err := record(ctx, tx, &a.principal, a.WorkspaceID, "suppression.removed", "inbound_message", v.ID,
					map[string]any{"kind": "email", "value": strings.ToLower(v.From), "reason": "unsubscribed", "via": "relabelled"}); err != nil {
					return err
				}
			}
		}
		return inbox.Classified(ctx, tx, inbox.Classification{WorkspaceID: a.WorkspaceID, InboundID: v.ID, LeadID: v.LeadID,
			SequenceID: v.SequenceID, MailboxID: v.MailboxID, Source: v.Source, Category: req.Category.Value, Previous: v.Category,
			By: categorySource(a.ActorType), Confidence: 1, NeedsReview: req.NeedsReview != nil && *req.NeedsReview,
			ActorID: a.ActorID, ActorType: a.ActorType, RequestID: RequestID(ctx)})
	})
	var inv errInvalid
	switch {
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	if v, ok = s.findInbound(w, r, a.WorkspaceID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

func (s *Server) replyInbound(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !s.inboundBrand(w, r, a, true) {
		return
	}
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "message not found")
		return
	}
	var req struct {
		Body string `json:"body"`
	}
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Body) == "" || len(req.Body) > 20000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "body must be 1 to 20000 characters")
		return
	}
	msgID, err := s.send.Reply(r.Context(), a.WorkspaceID, id, req.Body)
	var blocked sending.ErrBlocked
	var ce *mailbox.ConnError
	switch {
	case errors.Is(err, sending.ErrInboundNotFound), errors.Is(err, mailbox.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "message not found")
		return
	case errors.As(err, &blocked):
		writeError(w, r, http.StatusConflict, "blocked", blocked.Error())
		return
	case errors.As(err, &ce):
		writeError(w, r, http.StatusBadGateway, "send_failed", ce.Error())
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	ctx := r.Context()
	if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return record(ctx, tx, &a.principal, a.WorkspaceID, "inbox.replied", "inbound_message", id, map[string]any{"message_id": msgID})
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message_id": msgID, "status": "sent"})
}

func (s *Server) syncMailbox(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "mailbox not found")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM mailboxes WHERE id = $1 AND workspace_id = $2`, id, a.WorkspaceID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if status != "active" {
			return errInvalid("only an active mailbox can be synced")
		}
		return s.inbox.QueueNow(ctx, tx, a.WorkspaceID, id)
	})
	var inv errInvalid
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "mailbox not found")
	case errors.As(err, &inv):
		writeError(w, r, http.StatusConflict, "not_active", string(inv))
	case err != nil:
		s.internal(w, r, err)
	default:
		writeJSON(w, http.StatusAccepted, map[string]bool{"queued": true})
	}
}

// categorySource is how a label set through the API is marked: a person,
// an API token, or an agent running a mission.
func categorySource(actorType string) string {
	switch actorType {
	case "human", "agent":
		return actorType
	}
	return "api_token"
}

func equalPtr(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
