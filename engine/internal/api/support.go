package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/support"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The support inbox: conversations with customers who write in, answered
// by the team. See package support for intake and sending.

func (s *Server) routesSupport(mux *http.ServeMux) {
	read, manage := auth.ScopeInboxRead, auth.ScopeInboxManage
	base := "/v1/workspaces/{ws}/support"
	mux.HandleFunc("GET "+base+"/conversations", s.inBrand(read, s.listSupportConversations))
	mux.HandleFunc("GET "+base+"/conversations/{id}", s.inBrand(read, s.getSupportConversation))
	mux.HandleFunc("PATCH "+base+"/conversations/{id}", s.inBrand(manage, s.updateSupportConversation))
	mux.HandleFunc("POST "+base+"/conversations/{id}/replies", s.inBrand(manage, s.replySupportConversation))
	mux.HandleFunc("POST "+base+"/conversations/{id}/notes", s.inBrand(manage, s.noteSupportConversation))
	mux.HandleFunc("POST "+base+"/conversations/{id}/suggest-reply", s.inBrand(manage, s.suggestSupportReply))
	mux.HandleFunc("GET "+base+"/stats", s.inBrand(read, s.supportStats))
	mux.HandleFunc("GET "+base+"/mailboxes", s.inWorkspace(auth.ScopeMailboxesRead, s.listSupportMailboxes))
	mux.HandleFunc("PUT "+base+"/mailboxes/{id}", s.inWorkspace(auth.ScopeMailboxesManage, s.addSupportMailbox))
	mux.HandleFunc("DELETE "+base+"/mailboxes/{id}", s.inWorkspace(auth.ScopeMailboxesManage, s.removeSupportMailbox))
	mux.HandleFunc("POST /v1/workspaces/{ws}/test/support-email", s.inWorkspace(manage, s.simulateSupportEmail))
}

type supportPerson struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type supportConversationView struct {
	ID       string  `json:"id"`
	BrandID  *string `json:"brand_id"`
	RecordID *string `json:"record_id"`
	Contact  struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"contact"`
	MailboxID      *string        `json:"mailbox_id"`
	MailboxEmail   *string        `json:"mailbox_email"`
	Channel        string         `json:"channel"`
	Subject        string         `json:"subject"`
	Status         string         `json:"status"`
	Assignee       *supportPerson `json:"assignee"`
	Team           *supportPerson `json:"team"`
	Tags           []string       `json:"tags"`
	Unread         bool           `json:"unread"`
	LastFrom       string         `json:"last_from"`
	Preview        string         `json:"preview"`
	Messages       int            `json:"messages"`
	FirstMessageAt time.Time      `json:"first_message_at"`
	FirstReplyAt   *time.Time     `json:"first_reply_at"`
	ClosedAt       *time.Time     `json:"closed_at"`
	LastMessageAt  time.Time      `json:"last_message_at"`
	CreatedAt      time.Time      `json:"created_at"`
}

const supportConversationSelect = `
	SELECT c.id::text, c.brand_id::text, c.record_id::text, COALESCE(NULLIF(r.name, ''), ''), c.contact_email,
		c.mailbox_id::text, m.email, c.channel, c.subject, c.status,
		c.assignee_id::text, COALESCE(NULLIF(u.name, ''), u.email), c.team_id::text, t.name,
		c.tags, c.unread, c.last_from,
		COALESCE((SELECT left(regexp_replace(sm.body, '\s+', ' ', 'g'), 160) FROM support_messages sm
			WHERE sm.conversation_id = c.id AND sm.kind <> 'note' ORDER BY sm.created_at DESC LIMIT 1), ''),
		(SELECT count(*) FROM support_messages sm WHERE sm.conversation_id = c.id AND sm.kind <> 'note')::int,
		c.first_message_at, c.first_reply_at, c.closed_at, c.last_message_at, c.created_at
	FROM support_conversations c
	LEFT JOIN records r ON r.id = c.record_id
	LEFT JOIN mailboxes m ON m.id = c.mailbox_id
	LEFT JOIN users u ON u.id = c.assignee_id
	LEFT JOIN teams t ON t.id = c.team_id`

func scanSupportConversation(row pgx.CollectableRow) (supportConversationView, error) {
	var v supportConversationView
	var assigneeID, assigneeName, teamID, teamName *string
	err := row.Scan(&v.ID, &v.BrandID, &v.RecordID, &v.Contact.Name, &v.Contact.Email, &v.MailboxID, &v.MailboxEmail,
		&v.Channel, &v.Subject, &v.Status, &assigneeID, &assigneeName, &teamID, &teamName, &v.Tags, &v.Unread, &v.LastFrom,
		&v.Preview, &v.Messages, &v.FirstMessageAt, &v.FirstReplyAt, &v.ClosedAt, &v.LastMessageAt, &v.CreatedAt)
	if assigneeID != nil {
		v.Assignee = &supportPerson{ID: *assigneeID, Name: deref(assigneeName)}
	}
	if teamID != nil {
		v.Team = &supportPerson{ID: *teamID, Name: deref(teamName)}
	}
	if v.Tags == nil {
		v.Tags = []string{}
	}
	return v, err
}

// supportBrandFilter keeps a brand-limited caller to its brands'
// conversations (and ones with no brand).
func supportBrandFilter(a *access, args *[]any) string {
	if a.Brands == nil {
		return ""
	}
	*args = append(*args, a.Brands)
	return ` AND (c.brand_id IS NULL OR c.brand_id = ANY($` + itoa(len(*args)) + `::uuid[]))`
}

func (s *Server) listSupportConversations(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	qs := r.URL.Query()
	limit, ok := intParam(w, r, "limit", 50, 1, 200)
	if !ok {
		return
	}
	offset, ok := offsetParam(w, r)
	if !ok {
		return
	}
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID}
	where := ` WHERE c.workspace_id = $1` + supportBrandSQL(brand, &args)
	if st := qs.Get("status"); st != "" && st != "all" {
		if !slices.Contains(support.Statuses, st) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "status is open, pending, closed or all")
			return
		}
		args = append(args, st)
		where += ` AND c.status = $` + itoa(len(args))
	}
	switch as := qs.Get("assignee"); {
	case as == "":
	case as == "none":
		where += ` AND c.assignee_id IS NULL`
	case as == "me":
		if a.UserID == "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "assignee=me needs a person signed in; pass their user id")
			return
		}
		args = append(args, a.UserID)
		where += ` AND c.assignee_id = $` + itoa(len(args))
	case validUUID(as):
		args = append(args, as)
		where += ` AND c.assignee_id = $` + itoa(len(args))
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "assignee is me, none or a user id")
		return
	}
	if t := qs.Get("team_id"); t != "" {
		if !validUUID(t) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "team_id is a team's id")
			return
		}
		args = append(args, t)
		where += ` AND c.team_id = $` + itoa(len(args))
	}
	if tag := strings.TrimSpace(qs.Get("tag")); tag != "" {
		args = append(args, tag)
		where += ` AND $` + itoa(len(args)) + ` = ANY(c.tags)`
	}
	if rec := qs.Get("record_id"); rec != "" {
		if !validUUID(rec) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "record_id is a customer record's id")
			return
		}
		args = append(args, rec)
		where += ` AND c.record_id = $` + itoa(len(args))
	}
	if q := strings.TrimSpace(qs.Get("q")); q != "" {
		args = append(args, "%"+likeEscape(strings.ToLower(q))+"%")
		n := itoa(len(args))
		where += ` AND (lower(c.subject) LIKE $` + n + ` OR c.contact_email LIKE $` + n + ` OR lower(COALESCE(r.name, '')) LIKE $` + n +
			` OR EXISTS (SELECT 1 FROM support_messages sm WHERE sm.conversation_id = c.id AND lower(sm.body) LIKE $` + n + `))`
	}
	ctx := r.Context()
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM support_conversations c LEFT JOIN records r ON r.id = c.record_id`+where, args...).Scan(&total); err != nil {
		s.internal(w, r, err)
		return
	}
	// Waiting longest first among open ones reads like a queue; the rest
	// newest first.
	order := ` ORDER BY c.last_message_at DESC, c.id`
	if qs.Get("status") == support.StatusOpen && qs.Get("sort") == "oldest" {
		order = ` ORDER BY c.last_message_at, c.id`
	}
	args = append(args, limit, offset)
	rows, err := s.pool.Query(ctx, supportConversationSelect+where+order+` LIMIT $`+itoa(len(args)-1)+` OFFSET $`+itoa(len(args)), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, scanSupportConversation)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	counts, err := s.supportCounts(ctx, a, brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *int
	if offset+len(list) < total {
		n := offset + len(list)
		next = &n
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "total": total, "next_offset": next, "counts": counts})
}

// supportBrandSQL keeps a list to the picked brand: its conversations and
// those of no brand (a mailbox or address shared by every brand), which
// are every brand's to answer.
func supportBrandSQL(b brandSel, args *[]any) string {
	if b.id == "" || b.id == "none" {
		return b.sql("c.brand_id", args)
	}
	*args = append(*args, b.id)
	out := ` AND (c.brand_id IS NULL OR c.brand_id = $` + itoa(len(*args)) + `)`
	if b.in != nil {
		*args = append(*args, b.in)
		out += ` AND (c.brand_id IS NULL OR c.brand_id = ANY($` + itoa(len(*args)) + `::uuid[]))`
	}
	return out
}

// supportCounts are the inbox's folder counts.
func (s *Server) supportCounts(ctx context.Context, a *access, brand brandSel) (map[string]int, error) {
	args := []any{a.WorkspaceID, a.UserID}
	var open, pending, closed, mine, unassigned int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE c.status = 'open'), count(*) FILTER (WHERE c.status = 'pending'),
			count(*) FILTER (WHERE c.status = 'closed'),
			count(*) FILTER (WHERE c.status <> 'closed' AND c.assignee_id::text = $2),
			count(*) FILTER (WHERE c.status <> 'closed' AND c.assignee_id IS NULL)
		FROM support_conversations c WHERE c.workspace_id = $1`+supportBrandSQL(brand, &args), args...).
		Scan(&open, &pending, &closed, &mine, &unassigned)
	return map[string]int{"open": open, "pending": pending, "closed": closed, "mine": mine, "unassigned": unassigned}, err
}

type supportMessageView struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	Author    *supportPerson `json:"author"`
	FromEmail string         `json:"from_email"`
	FromName  string         `json:"from_name"`
	ToEmail   string         `json:"to_email"`
	Subject   string         `json:"subject"`
	Body      string         `json:"body"`
	AutoReply bool           `json:"auto_reply"`
	Status    string         `json:"status"`
	Error     *string        `json:"error"`
	CreatedAt time.Time      `json:"created_at"`
	SentAt    *time.Time     `json:"sent_at"`
}

// supportConversation loads one conversation the caller can see.
func (s *Server) supportConversation(ctx context.Context, a *access, id string) (*supportConversationView, error) {
	if !validUUID(id) {
		return nil, errNotFound
	}
	rows, err := s.pool.Query(ctx, supportConversationSelect+` WHERE c.id = $1 AND c.workspace_id = $2`, id, a.WorkspaceID)
	if err != nil {
		return nil, err
	}
	v, err := pgx.CollectExactlyOneRow(rows, scanSupportConversation)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !a.sees(v.BrandID) {
		return nil, errNotFound
	}
	return &v, err
}

func (s *Server) getSupportConversation(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	c, err := s.supportConversation(ctx, a, r.PathValue("id"))
	if s.supportError(w, r, err) {
		return
	}
	rows, err := s.pool.Query(ctx, `
		SELECT m.id::text, m.kind, m.author_id::text, COALESCE(NULLIF(u.name, ''), u.email), m.from_email, m.from_name, m.to_email,
			m.subject, m.body, m.auto_reply, m.status, m.error, m.created_at, m.sent_at
		FROM support_messages m LEFT JOIN users u ON u.id = m.author_id
		WHERE m.conversation_id = $1 AND m.workspace_id = $2 ORDER BY m.created_at, m.id`, c.ID, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	msgs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (supportMessageView, error) {
		var m supportMessageView
		var authorID, authorName *string
		err := row.Scan(&m.ID, &m.Kind, &authorID, &authorName, &m.FromEmail, &m.FromName, &m.ToEmail, &m.Subject, &m.Body,
			&m.AutoReply, &m.Status, &m.Error, &m.CreatedAt, &m.SentAt)
		if authorID != nil {
			m.Author = &supportPerson{ID: *authorID, Name: deref(authorName)}
		}
		return m, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// Opening it marks it read.
	if c.Unread && a.UserID != "" {
		if _, err := s.pool.Exec(ctx, `UPDATE support_conversations SET unread = false WHERE id = $1`, c.ID); err != nil {
			s.internal(w, r, err)
			return
		}
		c.Unread = false
	}
	// Their earlier conversations, for context.
	other := []supportConversationView{}
	if c.RecordID != nil {
		args := []any{a.WorkspaceID, *c.RecordID, c.ID}
		rows, err := s.pool.Query(ctx, supportConversationSelect+` WHERE c.workspace_id = $1 AND c.record_id = $2 AND c.id <> $3`+
			supportBrandFilter(a, &args)+` ORDER BY c.last_message_at DESC LIMIT 10`, args...)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if other, err = pgx.CollectRows(rows, scanSupportConversation); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversation": c, "messages": msgs, "other_conversations": other})
}

// supportWrites refuses a brand-limited caller a conversation shared by
// every brand (no brand), as with other shared records.
func (s *Server) supportWrites(w http.ResponseWriter, r *http.Request, a *access, c *supportConversationView) bool {
	if a.writes(c.BrandID) {
		return true
	}
	writeError(w, r, http.StatusForbidden, "forbidden", "this conversation belongs to no brand, so only people with every brand can change it")
	return false
}

// supportError writes the error for a failed support call; false if none.
func (s *Server) supportError(w http.ResponseWriter, r *http.Request, err error) bool {
	var inv errInvalid
	var blocked support.ErrBlocked
	switch {
	case err == nil:
		return false
	case errors.Is(err, errNotFound), errors.Is(err, support.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "conversation not found")
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
	case errors.As(err, &blocked):
		writeError(w, r, http.StatusConflict, "blocked", blocked.Error())
	default:
		s.internal(w, r, err)
	}
	return true
}

func (s *Server) updateSupportConversation(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Status     *string          `json:"status"`
		AssigneeID optional[string] `json:"assignee_id"`
		TeamID     optional[string] `json:"team_id"`
		Tags       *[]string        `json:"tags"`
		Unread     *bool            `json:"unread"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Status != nil && !slices.Contains(support.Statuses, *req.Status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is open, pending or closed")
		return
	}
	var tags []string
	if req.Tags != nil {
		if len(*req.Tags) > 20 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "at most 20 tags")
			return
		}
		for _, t := range *req.Tags {
			t = strings.ToLower(strings.TrimSpace(t))
			if t == "" || utf8.RuneCountInString(t) > 40 {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "a tag is 1 to 40 characters")
				return
			}
			if !slices.Contains(tags, t) {
				tags = append(tags, t)
			}
		}
		if tags == nil {
			tags = []string{}
		}
	}
	ctx := r.Context()
	c, err := s.supportConversation(ctx, a, r.PathValue("id"))
	if s.supportError(w, r, err) {
		return
	}
	if !s.supportWrites(w, r, a, c) {
		return
	}
	data := map[string]any{}
	action := "support.conversation_updated"
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if req.AssigneeID.Set && req.AssigneeID.Value != nil {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM memberships WHERE workspace_id = $1 AND user_id::text = $2)`,
				a.WorkspaceID, *req.AssigneeID.Value).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return errInvalid("assignee_id is not a member of this workspace")
			}
		}
		if req.TeamID.Set && req.TeamID.Value != nil {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM teams WHERE workspace_id = $1 AND id::text = $2)`,
				a.WorkspaceID, *req.TeamID.Value).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return errInvalid("team not found")
			}
		}
		_, err := tx.Exec(ctx, `
			UPDATE support_conversations SET
				status = COALESCE($3, status),
				closed_at = CASE WHEN $3 = 'closed' AND status <> 'closed' THEN now() ELSE closed_at END,
				assignee_id = CASE WHEN $4 THEN $5::uuid ELSE assignee_id END,
				team_id = CASE WHEN $6 THEN $7::uuid ELSE team_id END,
				tags = COALESCE($8, tags),
				unread = COALESCE($9, unread),
				updated_at = now()
			WHERE id = $1 AND workspace_id = $2`,
			c.ID, a.WorkspaceID, req.Status, req.AssigneeID.Set, req.AssigneeID.Value, req.TeamID.Set, req.TeamID.Value, tags, req.Unread)
		if err != nil {
			return err
		}
		if req.Status != nil && *req.Status != c.Status {
			data["status"] = *req.Status
			switch *req.Status {
			case support.StatusClosed:
				action = "support.conversation_closed"
			case support.StatusOpen:
				action = "support.conversation_reopened"
			}
		}
		if req.AssigneeID.Set {
			data["assignee_id"] = req.AssigneeID.Value
			if req.Status == nil || *req.Status == c.Status {
				action = "support.conversation_assigned"
			}
		}
		if req.TeamID.Set {
			data["team_id"] = req.TeamID.Value
		}
		if req.Tags != nil {
			data["tags"] = len(tags)
		}
		if req.Unread != nil && len(data) == 0 {
			// Marking read or unread is a view preference, not a change.
			return nil
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "support_conversation", c.ID, data)
	})
	if s.supportError(w, r, err) {
		return
	}
	c, err = s.supportConversation(ctx, a, c.ID)
	if s.supportError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func supportBody(w http.ResponseWriter, r *http.Request, body string) bool {
	if strings.TrimSpace(body) == "" || len(body) > 20000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "body must be 1 to 20000 characters")
		return false
	}
	return true
}

func (s *Server) replySupportConversation(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Body   string `json:"body"`
		Status string `json:"status"`
	}
	if !decode(w, r, &req) || !supportBody(w, r, req.Body) {
		return
	}
	if req.Status != "" && req.Status != support.StatusPending && req.Status != support.StatusClosed && req.Status != support.StatusOpen {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status after the reply is pending (the default: waiting on the customer), closed or open")
		return
	}
	ctx := r.Context()
	c, err := s.supportConversation(ctx, a, r.PathValue("id"))
	if s.supportError(w, r, err) {
		return
	}
	if !s.supportWrites(w, r, a, c) {
		return
	}
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		id, err = support.QueueReply(ctx, tx, support.Reply{WorkspaceID: a.WorkspaceID, ConversationID: c.ID,
			AuthorID: a.UserID, Body: req.Body, Status: req.Status})
		if err != nil {
			return err
		}
		data := map[string]any{"message_id": id}
		if req.Status != "" {
			data["status"] = req.Status
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "support.replied", "support_conversation", c.ID, data)
	})
	if err != nil {
		metrics.SupportMessage("out", "failure")
	}
	if s.supportError(w, r, err) {
		return
	}
	metrics.SupportMessage("out", "success")
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "status": "sending"})
}

func (s *Server) noteSupportConversation(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Body string `json:"body"`
	}
	if !decode(w, r, &req) || !supportBody(w, r, req.Body) {
		return
	}
	ctx := r.Context()
	c, err := s.supportConversation(ctx, a, r.PathValue("id"))
	if s.supportError(w, r, err) {
		return
	}
	if !s.supportWrites(w, r, a, c) {
		return
	}
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO support_messages (workspace_id, conversation_id, kind, record_id, author_id, body, status)
			VALUES ($1, $2, 'note', $3, $4, $5, 'saved') RETURNING id::text`,
			a.WorkspaceID, c.ID, c.RecordID, nilIfEmpty(a.UserID), req.Body).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE support_conversations SET updated_at = now() WHERE id = $1`, c.ID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "support.note_added", "support_conversation", c.ID, map[string]any{"message_id": id})
	})
	if s.supportError(w, r, err) {
		return
	}
	metrics.SupportMessage("note", "success")
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// suggestSupportReply drafts an answer to the customer's latest message
// from what customers may be told, with the workspace's own AI. Nothing is
// sent: the person edits and sends it.
func (s *Server) suggestSupportReply(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), knowledgeAskTimeout)
	defer cancel()
	c, err := s.supportConversation(ctx, a, r.PathValue("id"))
	if s.supportError(w, r, err) {
		return
	}
	var question string
	err = s.pool.QueryRow(ctx, `SELECT body FROM support_messages WHERE conversation_id = $1 AND kind = 'in' AND NOT auto_reply
		ORDER BY created_at DESC LIMIT 1`, c.ID).Scan(&question)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusConflict, "nothing_to_answer", "the customer hasn't written anything to answer yet")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	question = strings.TrimSpace(question)
	if c.Subject != "" {
		question = c.Subject + "\n\n" + question
	}
	// The question is at most 1000 characters: the start of their email.
	if utf8.RuneCountInString(question) > 1000 {
		question = string([]rune(question)[:1000])
	}
	kq := knowledgeQuery{q: question, audience: audienceCustomers, brand: c.BrandID, limit: 8}
	if kq.brand == nil && len(a.Brands) == 1 {
		kq.brand = &a.Brands[0]
	}
	start := time.Now()
	ev := track.Event{Name: "support.reply_suggested", WorkspaceID: a.WorkspaceID, UserID: a.personID(), ActorID: a.ActorID, ActorType: a.ActorType,
		RequestID: RequestID(ctx), Props: map[string]any{}}
	out, err := s.answerKnowledge(ctx, a, kq, "support")
	ev.Props["duration_ms"] = time.Since(start).Milliseconds()
	if err != nil {
		metrics.KnowledgeAsked("error", time.Since(start))
		track.Fail(ctx, s.log, ev, "internal", err)
		s.internal(w, r, err)
		return
	}
	outcome := "answered"
	switch {
	case out.AI.Problem != nil:
		outcome = "no_ai"
		ev.Props["ai_problem"] = out.AI.Problem.Code
	case !out.Answered:
		outcome = "unanswered"
	}
	ev.Props["answered"], ev.Props["cited"] = out.Answered, len(out.Citations)
	metrics.KnowledgeAsked(outcome, time.Since(start))
	track.Emit(ctx, ev)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) supportStats(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	days, ok := intParam(w, r, "days", 30, 1, 365)
	if !ok {
		return
	}
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, days}
	var started, replied, closed int
	var firstReply, resolution *float64
	err := s.pool.QueryRow(r.Context(), `
		SELECT count(*), count(c.first_reply_at), count(c.closed_at),
			percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM c.first_reply_at - c.first_message_at)) FILTER (WHERE c.first_reply_at IS NOT NULL),
			percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM c.closed_at - c.first_message_at)) FILTER (WHERE c.closed_at IS NOT NULL)
		FROM support_conversations c
		WHERE c.workspace_id = $1 AND c.first_message_at > now() - make_interval(days => $2)`+supportBrandSQL(brand, &args), args...).
		Scan(&started, &replied, &closed, &firstReply, &resolution)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	counts, err := s.supportCounts(r.Context(), a, brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": days, "conversations": started, "replied": replied, "closed": closed,
		"median_first_reply_seconds": firstReply, "median_resolution_seconds": resolution, "counts": counts})
}

// ---- Support mailboxes ----

type supportMailboxView struct {
	ID           string     `json:"id"`
	Email        string     `json:"email"`
	DisplayName  string     `json:"display_name"`
	BrandID      *string    `json:"brand_id"`
	Status       string     `json:"status"`
	LastSyncedAt *time.Time `json:"last_synced_at"`
	SyncError    *string    `json:"sync_error"`
}

func (s *Server) listSupportMailboxes(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), `
		SELECT id::text, email, display_name, brand_id::text, status, last_synced_at, sync_error FROM mailboxes
		WHERE workspace_id = $1 AND purpose = 'support' ORDER BY email`, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[supportMailboxView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// addSupportMailbox has a connected mailbox answer support: its mail goes
// to the support inbox from now on and sequences stop sending from it.
func (s *Server) addSupportMailbox(w http.ResponseWriter, r *http.Request) {
	s.setMailboxPurpose(w, r, support.PurposeSupport)
}

func (s *Server) removeSupportMailbox(w http.ResponseWriter, r *http.Request) {
	s.setMailboxPurpose(w, r, support.PurposeOutreach)
}

func (s *Server) setMailboxPurpose(w http.ResponseWriter, r *http.Request, purpose string) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "mailbox not found")
		return
	}
	ctx := r.Context()
	var view supportMailboxView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var old string
		var login bool
		err := tx.QueryRow(ctx, `SELECT purpose, has_login FROM mailboxes WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, id, a.WorkspaceID).Scan(&old, &login)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if purpose == support.PurposeSupport && old != purpose {
			if !login {
				return supportConflict("Outflare can't read this mailbox: it only knows it from a sequencer. Connect it with its login first.")
			}
			var sequences int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM mailbox_assignments WHERE mailbox_id = $1 AND state = 'active'`, id).Scan(&sequences); err != nil {
				return err
			}
			if sequences > 0 {
				return supportConflict("this mailbox sends for a sequence or campaign: take it off them first, so customers' mail isn't mixed with replies to cold email")
			}
		}
		if old != purpose {
			if _, err := tx.Exec(ctx, `UPDATE mailboxes SET purpose = $2, updated_at = now() WHERE id = $1`, id, purpose); err != nil {
				return err
			}
			action := "support.mailbox_added"
			if purpose == support.PurposeOutreach {
				action = "support.mailbox_removed"
			}
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, action, "mailbox", id, nil); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx, `SELECT id::text, email, display_name, brand_id::text, status, last_synced_at, sync_error FROM mailboxes WHERE id = $1`, id).
			Scan(&view.ID, &view.Email, &view.DisplayName, &view.BrandID, &view.Status, &view.LastSyncedAt, &view.SyncError)
	})
	var conflict supportConflict
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "mailbox not found")
	case errors.As(err, &conflict):
		writeError(w, r, http.StatusConflict, "mailbox_in_use", string(conflict))
	case err != nil:
		s.internal(w, r, err)
	case purpose == support.PurposeOutreach:
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusOK, view)
	}
}

// supportConflict is a 409 with its message.
type supportConflict string

func (e supportConflict) Error() string { return string(e) }

// simulateSupportEmail delivers a made-up customer email to a support
// mailbox in a test workspace, as if they had written in.
func (s *Server) simulateSupportEmail(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !testOnly(w, r, a, "Simulating support email") {
		return
	}
	var req struct {
		MailboxID string `json:"mailbox_id"`
		From      string `json:"from"`
		Name      string `json:"name"`
		Subject   string `json:"subject"`
		Body      string `json:"body"`
		InReplyTo string `json:"in_reply_to"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.From = strings.TrimSpace(req.From)
	if !strings.Contains(req.From, "@") || strings.ContainsAny(req.From, " \r\n<>") || len(req.From) > 254 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "from is the customer's email address")
		return
	}
	if strings.ContainsAny(req.Name+req.Subject+req.InReplyTo, "\r\n") || !supportBody(w, r, req.Body) {
		if strings.TrimSpace(req.Body) != "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "name, subject and in_reply_to are one line each")
		}
		return
	}
	ctx := r.Context()
	if !validUUID(req.MailboxID) {
		writeError(w, r, http.StatusNotFound, "not_found", "no support mailbox with that id")
		return
	}
	var to, purpose string
	err := s.pool.QueryRow(ctx, `SELECT email, purpose FROM mailboxes WHERE id = $1 AND workspace_id = $2`, req.MailboxID, a.WorkspaceID).Scan(&to, &purpose)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && purpose != support.PurposeSupport {
		writeError(w, r, http.StatusNotFound, "not_found", "no support mailbox with that id")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var b strings.Builder
	from := req.From
	if req.Name != "" {
		from = `"` + strings.ReplaceAll(req.Name, `"`, "") + `" <` + req.From + ">"
	}
	b.WriteString("From: " + from + "\r\nTo: " + to + "\r\nSubject: " + req.Subject + "\r\n")
	b.WriteString("Message-ID: <" + hex.EncodeToString(randomBytes()) + "@support.test>\r\n")
	if req.InReplyTo != "" {
		b.WriteString("In-Reply-To: <" + strings.Trim(req.InReplyTo, "<>") + ">\r\n")
	}
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + req.Body)
	id, err := s.inbox.Deliver(ctx, a.WorkspaceID, req.MailboxID, []byte(b.String()))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var conv string
	if err := s.pool.QueryRow(ctx, `SELECT conversation_id::text FROM support_messages WHERE id = $1`, id).Scan(&conv); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"message_id": id, "conversation_id": conv})
}

func randomBytes() []byte {
	b := make([]byte, 16)
	rand.Read(b) //nolint:errcheck // never fails
	return b
}
