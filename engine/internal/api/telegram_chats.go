package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/tgsession"
)

// A brand's chats synced from company Telegram accounts (migration 0492).
// Everyone who can read the brand's customers sees them, except chats
// marked private: those only the account's holder and people who manage
// the team directory see.

func (s *Server) routesTelegramChats(mux *http.ServeMux) {
	base := "/v1/brands/{brand}/telegram-chats"
	mux.HandleFunc("GET "+base, s.onBrand(auth.ScopeCustomersRead, s.listTelegramChats))
	mux.HandleFunc("GET "+base+"/{id}", s.onBrand(auth.ScopeCustomersRead, s.getTelegramChat))
	mux.HandleFunc("PATCH "+base+"/{id}", s.onBrand(auth.ScopeCustomersManage, s.updateTelegramChat))
	mux.HandleFunc("GET "+base+"/{id}/messages", s.onBrand(auth.ScopeCustomersRead, s.listTelegramMessages))
	mux.HandleFunc("POST "+base+"/{id}/match", s.onBrand(auth.ScopeCustomersManage, s.matchTelegramChat))
	mux.HandleFunc("POST "+base+"/{id}/summary", s.onBrand(auth.ScopeCustomersManage, s.summarizeTelegramChat))
}

var telegramChatKinds = []string{"unsorted", "customer", "internal", "personal"}

var telegramMatchStatuses = []string{"pending", "suggested", "no_match", "confirmed"}

type telegramRecordRef struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
}

type telegramChatView struct {
	ID            string     `json:"id"`
	AccountID     string     `json:"account_id"`
	AccountLabel  string     `json:"account_label"`
	PeerKind      string     `json:"peer_kind"`
	PeerID        string     `json:"peer_id"`
	Title         string     `json:"title"`
	Username      *string    `json:"username"`
	Phone         *string    `json:"phone"`
	Members       *int       `json:"members"`
	RecordID      *string    `json:"record_id"`
	Kind          string     `json:"kind"`
	Private       bool       `json:"private"`
	MessageCount  int        `json:"message_count"`
	LastMessageAt *time.Time `json:"last_message_at"`
	HistoryDone   bool       `json:"history_done"`
	CreatedAt     time.Time  `json:"created_at"`

	Record          *telegramRecordRef `json:"record"`
	MatchStatus     string             `json:"match_status"`
	SuggestedRecord *telegramRecordRef `json:"suggested_record"`
	MatchReason     *string            `json:"match_reason"`
	MatchConfidence *float64           `json:"match_confidence"`
	MatchNote       *string            `json:"match_note"`
	SuggestedKind   *string            `json:"suggested_kind"`
	MatchedAt       *time.Time         `json:"matched_at"`
	// Whether the account is held by the caller: their own queue.
	Mine bool `json:"mine"`
	// Since when the other side has waited for an answer.
	WaitingSince *time.Time           `json:"waiting_since"`
	Summary      *telegramChatSummary `json:"summary"`
}

type telegramChatSummary struct {
	Text      string    `json:"text"`
	OpenItems []string  `json:"open_items"`
	WaitingOn string    `json:"waiting_on"`
	At        time.Time `json:"at"`
	// Whether messages came in after it was written.
	Stale bool `json:"stale"`
}

type telegramMessageView struct {
	ID        string     `json:"id"`
	MessageID int        `json:"message_id"`
	Outgoing  bool       `json:"outgoing"`
	SenderID  *string    `json:"sender_id"`
	FromName  *string    `json:"from_name"`
	Text      string     `json:"text"`
	Media     *string    `json:"media"`
	FileName  *string    `json:"file_name"`
	ReplyTo   *int       `json:"reply_to"`
	Service   bool       `json:"service"`
	SentAt    time.Time  `json:"sent_at"`
	EditedAt  *time.Time `json:"edited_at"`
}

func (s *Server) telegramOpener() *tgsession.Service { return &tgsession.Service{Box: s.box} }

func nonZero[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

// telegramChatSQL selects chats the caller may see ($1 workspace, $2
// brand, $3 the caller's user id or ”, $4 whether they manage the
// directory).
const telegramChatSQL = `SELECT c.id::text, c.account_id::text, a.label, c.peer_kind, c.peer_id, c.details_sealed, c.record_id::text, c.kind,
		c.private, c.message_count, c.last_message_at, c.history_done, c.created_at,
		rr.record_type, rr.name, c.match_status, c.suggested_record_id::text, sr.record_type, sr.name, c.match_reason, c.match_confidence,
		c.match_note_sealed, c.suggested_kind, c.matched_at,
		EXISTS(SELECT 1 FROM staff st WHERE st.id = a.holder_id AND st.user_id::text = NULLIF($3, '')) AS mine,
		c.waiting_since, c.summary_sealed, c.summary_at, c.summary_through, c.last_message_id
	FROM telegram_chats c JOIN staff_accounts a ON a.id = c.account_id AND a.workspace_id = c.workspace_id
	LEFT JOIN records rr ON rr.id = c.record_id
	LEFT JOIN records sr ON sr.id = c.suggested_record_id
	WHERE c.workspace_id = $1 AND c.brand_id = $2
	  AND (NOT c.private OR $4 OR EXISTS(SELECT 1 FROM staff st WHERE st.id = a.holder_id AND st.user_id::text = NULLIF($3, '')))`

func (s *Server) scanTelegramChat(row pgx.Row) (telegramChatView, error) {
	var v telegramChatView
	var peer int64
	var label string
	var sealed, note, summary []byte
	var summaryAt *time.Time
	var through *int
	var last int
	var recType, recName, sugID, sugType, sugName *string
	var conf *float32
	if err := row.Scan(&v.ID, &v.AccountID, &label, &v.PeerKind, &peer, &sealed, &v.RecordID, &v.Kind, &v.Private,
		&v.MessageCount, &v.LastMessageAt, &v.HistoryDone, &v.CreatedAt,
		&recType, &recName, &v.MatchStatus, &sugID, &sugType, &sugName, &v.MatchReason, &conf, &note, &v.SuggestedKind, &v.MatchedAt, &v.Mine,
		&v.WaitingSince, &summary, &summaryAt, &through, &last); err != nil {
		return v, err
	}
	ref := func(id, typ, name *string) *telegramRecordRef {
		if id == nil || typ == nil {
			return nil
		}
		r := &telegramRecordRef{ID: *id, Type: *typ}
		if name != nil {
			r.Name = *name
		}
		return r
	}
	v.Record, v.SuggestedRecord = ref(v.RecordID, recType, recName), ref(sugID, sugType, sugName)
	if conf != nil {
		c := float64(*conf)
		v.MatchConfidence = &c
	}
	if len(note) > 0 {
		if plain, err := s.box.Open(note, []byte(v.ID)); err == nil {
			n := string(plain)
			v.MatchNote = &n
		}
	}
	if len(summary) > 0 && summaryAt != nil {
		if sum, err := s.telegramOpener().OpenSummary(v.ID, summary); err == nil {
			v.Summary = &telegramChatSummary{Text: sum.Summary, OpenItems: sum.OpenItems, WaitingOn: sum.WaitingOn, At: *summaryAt,
				Stale: through != nil && *through < last}
			if v.Summary.OpenItems == nil {
				v.Summary.OpenItems = []string{}
			}
		}
	}
	v.AccountLabel = label
	v.PeerID = strconv.FormatInt(peer, 10)
	d, err := s.telegramOpener().OpenChat(v.ID, sealed)
	if err != nil {
		return v, err
	}
	v.Title, v.Username, v.Phone, v.Members = d.Title, nonZero(d.Username), nonZero(d.Phone), nonZero(d.Members)
	return v, nil
}

func (a *access) telegramArgs() []any {
	return []any{a.WorkspaceID, a.Brand, a.UserID, a.has(auth.ScopeMembersManage)}
}

func (s *Server) listTelegramChats(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit is 1 to 200")
			return
		}
		limit = n
	}
	offset, ok := pageOffsetParam(w, r)
	if !ok {
		return
	}
	sql := telegramChatSQL
	args := a.telegramArgs()
	if v := q.Get("account_id"); v != "" {
		if !validUUID(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "account_id is a company account id")
			return
		}
		args = append(args, v)
		sql += ` AND c.account_id = $` + strconv.Itoa(len(args))
	}
	if v := q.Get("kind"); v != "" {
		if !slices.Contains(telegramChatKinds, v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is unsorted, customer, internal or personal")
			return
		}
		args = append(args, v)
		sql += ` AND c.kind = $` + strconv.Itoa(len(args))
	}
	if v := q.Get("match_status"); v != "" {
		list := strings.Split(v, ",")
		for _, st := range list {
			if !slices.Contains(telegramMatchStatuses, st) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "match_status is pending, suggested, no_match or confirmed, or several joined by commas")
				return
			}
		}
		args = append(args, list)
		sql += ` AND c.match_status = ANY($` + strconv.Itoa(len(args)) + `)`
	}
	if q.Get("mine") == "true" {
		sql += ` AND EXISTS(SELECT 1 FROM staff st WHERE st.id = a.holder_id AND st.user_id::text = NULLIF($3, ''))`
	} else if v := q.Get("mine"); v != "" && v != "false" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "mine is true or false")
		return
	}
	if q.Get("waiting") == "true" {
		sql += ` AND c.waiting_since IS NOT NULL AND c.kind IN ('unsorted', 'customer')`
	} else if v := q.Get("waiting"); v != "" && v != "false" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "waiting is true or false")
		return
	}
	if v := q.Get("record_id"); v != "" {
		if !validUUID(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "record_id is a record id")
			return
		}
		args = append(args, v)
		sql += ` AND c.record_id = $` + strconv.Itoa(len(args))
	}
	args = append(args, limit+1, offset)
	sql += ` ORDER BY c.last_message_at DESC NULLS LAST, c.id LIMIT $` + strconv.Itoa(len(args)-1) + ` OFFSET $` + strconv.Itoa(len(args))
	rows, err := s.pool.Query(r.Context(), sql, args...)
	if s.allocError(w, r, err) {
		return
	}
	out := []telegramChatView{}
	defer rows.Close()
	for rows.Next() {
		v, err := s.scanTelegramChat(rows)
		if s.allocError(w, r, err) {
			return
		}
		out = append(out, v)
	}
	if s.allocError(w, r, rows.Err()) {
		return
	}
	var next any
	if len(out) > limit {
		out = out[:limit]
		next = strconv.Itoa(offset + limit)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "next_cursor": nextCursor(pageOffset, next)})
}

func (s *Server) telegramChat(ctx context.Context, a *access, id string) (telegramChatView, error) {
	if !validUUID(id) {
		return telegramChatView{}, errNotFound
	}
	v, err := s.scanTelegramChat(s.pool.QueryRow(ctx, telegramChatSQL+` AND c.id = $5`, append(a.telegramArgs(), id)...))
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	return v, err
}

func (s *Server) getTelegramChat(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := s.telegramChat(r.Context(), a, r.PathValue("id"))
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) updateTelegramChat(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Kind    *string `json:"kind"`
		Private *bool   `json:"private"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Kind != nil && !slices.Contains(telegramChatKinds, *req.Kind) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is unsorted, customer, internal or personal")
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	before, err := s.telegramChat(ctx, a, id)
	if s.allocError(w, r, err) {
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE telegram_chats SET kind = COALESCE($4, kind), private = COALESCE($5, private), updated_at = now()
			WHERE workspace_id = $1 AND brand_id = $2 AND id = $3`, a.WorkspaceID, a.Brand, id, req.Kind, req.Private); err != nil {
			return err
		}
		removed := int64(0)
		if req.Kind != nil && (*req.Kind == "personal" || *req.Kind == "internal") && *req.Kind != before.Kind {
			// Not company business: what was copied is removed, and
			// nothing more is.
			tag, err := tx.Exec(ctx, `DELETE FROM telegram_messages WHERE workspace_id = $1 AND brand_id = $2 AND chat_id = $3`, a.WorkspaceID, a.Brand, id)
			if err != nil {
				return err
			}
			removed = tag.RowsAffected()
			if _, err := tx.Exec(ctx, `UPDATE telegram_chats SET message_count = 0 WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, id); err != nil {
				return err
			}
		}
		props := map[string]any{"removed_messages": removed}
		if req.Kind != nil {
			props["kind"] = *req.Kind
		}
		if req.Private != nil {
			props["private"] = *req.Private
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "telegram_chat.updated", "telegram_chat", id,
			map[string]any{"brand_id": a.Brand, "kind": req.Kind, "private": req.Private}, props)
	})
	if s.allocError(w, r, err) {
		return
	}
	v, err := s.telegramChat(ctx, a, id)
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) listTelegramMessages(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	chat, err := s.telegramChat(ctx, a, r.PathValue("id"))
	if s.allocError(w, r, err) {
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit is 1 to 500")
			return
		}
		limit = n
	}
	offset, ok := pageOffsetParam(w, r)
	if !ok {
		return
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, message_id, outgoing, sender_id, content_sealed, sent_at, edited_at
		FROM telegram_messages WHERE workspace_id = $1 AND brand_id = $2 AND chat_id = $3
		ORDER BY message_id DESC LIMIT $4 OFFSET $5`, a.WorkspaceID, a.Brand, chat.ID, limit+1, offset)
	if s.allocError(w, r, err) {
		return
	}
	defer rows.Close()
	open := s.telegramOpener()
	out := []telegramMessageView{}
	for rows.Next() {
		var v telegramMessageView
		var sender *int64
		var sealed []byte
		if err := rows.Scan(&v.ID, &v.MessageID, &v.Outgoing, &sender, &sealed, &v.SentAt, &v.EditedAt); s.allocError(w, r, err) {
			return
		}
		c, err := open.OpenMessage(v.ID, sealed)
		if s.allocError(w, r, err) {
			return
		}
		if sender != nil {
			id := strconv.FormatInt(*sender, 10)
			v.SenderID = &id
		}
		v.Text, v.FromName, v.Media, v.FileName, v.ReplyTo, v.Service = c.Text, nonZero(c.FromName), nonZero(c.Media), nonZero(c.FileName), nonZero(c.ReplyTo), c.Service
		out = append(out, v)
	}
	if s.allocError(w, r, rows.Err()) {
		return
	}
	var next any
	if len(out) > limit {
		out = out[:limit]
		next = strconv.Itoa(offset + limit)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "next_cursor": nextCursor(pageOffset, next)})
}

// matchTelegramChat links a chat to the customer record it is with, or
// (record_id null) says none of the suggestions is right. The record gets
// the chat's Telegram id as an identity, so the next chat with them, on any
// account, matches by itself.
func (s *Server) matchTelegramChat(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		RecordID *string `json:"record_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	chat, err := s.telegramChat(ctx, a, id)
	if s.allocError(w, r, err) {
		return
	}
	if chat.Kind == "internal" || chat.Kind == "personal" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "this chat is "+chat.Kind+": mark it customer or unsorted first")
		return
	}
	peerID, _ := strconv.ParseInt(chat.PeerID, 10, 64)
	ns, value := tgsession.IdentityFor(chat.PeerKind, peerID)
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var old, suggested *string
		if err := tx.QueryRow(ctx, `SELECT record_id::text, suggested_record_id::text FROM telegram_chats WHERE workspace_id = $1 AND brand_id = $2 AND id = $3 FOR UPDATE`,
			a.WorkspaceID, a.Brand, id).Scan(&old, &suggested); err != nil {
			return err
		}
		if old != nil && (req.RecordID == nil || *req.RecordID != *old) {
			// It was someone else: their record no longer claims the chat.
			var closed string
			err := tx.QueryRow(ctx, `UPDATE record_identities SET valid_to = now(), closed_reason = 'changed'
				WHERE record_id = $1 AND namespace = $2 AND value = $3 AND valid_to IS NULL RETURNING id::text`, *old, ns, value).Scan(&closed)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err == nil {
				if err := touch(ctx, tx, *old); err != nil {
					return err
				}
				if err := record(ctx, tx, &a.principal, a.WorkspaceID, "identity.closed", "identity", closed, map[string]any{"record_id": *old, "namespace": ns}); err != nil {
					return err
				}
			}
		}
		if req.RecordID == nil {
			if _, err := tx.Exec(ctx, `UPDATE telegram_chats SET match_status = 'no_match', record_id = NULL, suggested_record_id = NULL, match_reason = NULL,
				match_confidence = NULL, match_note_sealed = NULL, matched_at = now(), matched_by = $3, updated_at = now() WHERE workspace_id = $1 AND id = $2`,
				a.WorkspaceID, id, nullIfEmpty(a.ActorID)); err != nil {
				return err
			}
			return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "telegram_chat.unmatched", "telegram_chat", id,
				map[string]any{"brand_id": a.Brand}, map[string]any{"had_record": old != nil})
		}
		rec, err := lockRecord(ctx, tx, a, *req.RecordID)
		if err != nil {
			return err
		}
		if rec.Type != "person" && rec.Type != "account" {
			return errInvalid("record_id must be a person or a company")
		}
		ids := []identityInput{{Namespace: ns, Value: value, Source: "telegram", Verified: ptr(true)}}
		if chat.Username != nil {
			ids = append(ids, identityInput{Namespace: tgsession.IdentityUsername, Value: *chat.Username, Source: "telegram", Verified: ptr(false)})
		}
		if chat.Phone != nil && chat.PeerKind == "user" && rec.Type == "person" {
			ids = append(ids, identityInput{Namespace: "phone", Value: *chat.Phone, Source: "telegram", Verified: ptr(false)})
		}
		for _, in := range ids {
			if err := in.normalize(); err != nil {
				continue // a phone Telegram showed in a form we can't store
			}
			if _, _, err := putIdentity(ctx, tx, a, &rec, in); err != nil {
				return err
			}
		}
		if err := touch(ctx, tx, rec.ID); err != nil {
			return err
		}
		fromSuggestion := suggested != nil && *suggested == rec.ID
		if _, err := tx.Exec(ctx, `UPDATE telegram_chats SET match_status = 'confirmed', record_id = $3, suggested_record_id = NULL,
			match_reason = CASE WHEN $4 THEN match_reason END, match_confidence = CASE WHEN $4 THEN match_confidence END,
			match_note_sealed = CASE WHEN $4 THEN match_note_sealed END, suggested_kind = NULL,
			kind = CASE WHEN kind = 'unsorted' THEN 'customer' ELSE kind END, matched_at = now(), matched_by = $5, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, id, rec.ID, fromSuggestion, nullIfEmpty(a.ActorID)); err != nil {
			return err
		}
		// The same person or group on another account matches again, now
		// that their record has the Telegram id.
		rows, err := tx.Query(ctx, `UPDATE telegram_chats SET match_status = 'pending', updated_at = now()
			WHERE workspace_id = $1 AND brand_id = $2 AND id <> $3 AND peer_kind = $4 AND peer_id = $5 AND match_status IN ('suggested', 'no_match')
			RETURNING account_id::text`, a.WorkspaceID, a.Brand, id, chat.PeerKind, peerID)
		if err != nil {
			return err
		}
		others, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, acc := range others {
			if err := tgsession.EnqueueMatch(ctx, tx, a.WorkspaceID, acc, time.Now()); err != nil {
				return err
			}
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "telegram_chat.matched", "telegram_chat", id,
			map[string]any{"brand_id": a.Brand, "record_id": rec.ID, "from_suggestion": fromSuggestion},
			map[string]any{"from_suggestion": fromSuggestion, "record_type": rec.Type, "rematched": len(others)})
	})
	if s.recordError(w, r, err) {
		return
	}
	v, err := s.telegramChat(ctx, a, id)
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// summarizeTelegramChat asks the workspace's AI for a fresh summary of a
// chat. It is written in a job; the chat's summary shows it once done.
func (s *Server) summarizeTelegramChat(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	id := r.PathValue("id")
	chat, err := s.telegramChat(ctx, a, id)
	if s.allocError(w, r, err) {
		return
	}
	if chat.Kind == "internal" || chat.Kind == "personal" || chat.MessageCount == 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "this chat has no messages kept to summarise")
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var last int
		if err := tx.QueryRow(ctx, `SELECT last_message_id FROM telegram_chats WHERE workspace_id = $1 AND brand_id = $2 AND id = $3`, a.WorkspaceID, a.Brand, id).Scan(&last); err != nil {
			return err
		}
		if err := tgsession.EnqueueSummary(ctx, tx, a.WorkspaceID, id, last); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "telegram_chat.summary_requested", "telegram_chat", id, map[string]any{"brand_id": a.Brand})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}
