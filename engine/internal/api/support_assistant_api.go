package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/support/assistant"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The support assistant's settings, its runs on a conversation, and its
// report. See support_assistant.go for how it decides.

type assistantSettingView struct {
	ID            string               `json:"id"`
	BrandID       *string              `json:"brand_id"`
	ChatMode      string               `json:"chat_mode"`
	EmailMode     string               `json:"email_mode"`
	Name          string               `json:"name"`
	FindOnWebsite bool                 `json:"find_on_website"`
	SlackID       *string              `json:"slack_integration_id"`
	SlackChannels []connectors.Channel `json:"slack_channels"`
	UpdatedAt     time.Time            `json:"updated_at"`
}

const assistantSettingSelect = `SELECT id::text, brand_id::text, chat_mode, email_mode, name, find_on_website, slack_integration_id::text, slack_channels,
	updated_at FROM support_assistant_settings`

// autoAvailable says whether the assistant may send on its own in a
// workspace: only when an extension decides so (SetSupportAuto). The
// engine on its own only drafts.
func (s *Server) autoAvailable(ctx context.Context, ws string) bool {
	return s.supportAuto != nil && s.supportAuto(ctx, ws)
}

// SetSupportAuto lets an extension decide which workspaces the support
// assistant may answer customers in without a person (Auto mode).
func (s *Server) SetSupportAuto(fn func(ctx context.Context, workspaceID string) bool) {
	s.supportAuto = fn
}

func (s *Server) listAssistantSettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID}
	rows, err := s.pool.Query(r.Context(), assistantSettingSelect+` WHERE workspace_id = $1`+sharedBrandSQL(brand, "brand_id", &args)+
		` ORDER BY brand_id IS NOT NULL, created_at`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[assistantSettingView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "auto_available": s.autoAvailable(r.Context(), a.WorkspaceID)})
}

func (s *Server) putAssistantSetting(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		ChatMode      string   `json:"chat_mode"`
		EmailMode     string   `json:"email_mode"`
		Name          string   `json:"name"`
		FindOnWebsite *bool    `json:"find_on_website"`
		SlackID       *string  `json:"slack_integration_id"`
		SlackChannels []string `json:"slack_channels"`
	}
	if !decode(w, r, &req) {
		return
	}
	website := req.FindOnWebsite == nil || *req.FindOnWebsite
	for _, m := range []*string{&req.ChatMode, &req.EmailMode} {
		if *m == "" {
			*m = "off"
		}
		if *m != "off" && *m != "draft" && *m != "auto" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "a mode is off, draft or auto")
			return
		}
		if *m == "auto" && !s.autoAvailable(r.Context(), a.WorkspaceID) {
			writeError(w, r, http.StatusForbidden, "feature_locked", "sending without a person is part of Outflare Cloud; draft mode drafts every answer for your team to send")
			return
		}
	}
	req.Name = strings.TrimSpace(req.Name)
	if utf8.RuneCountInString(req.Name) > 60 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "the name is at most 60 characters")
		return
	}
	brand, err := s.slaScope(r, a)
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such brand")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	ctx := r.Context()
	// Slack: the channels must be ones the connection can see; the app
	// joins the public ones so it can read them.
	channels := []connectors.Channel{}
	if req.SlackID != nil && *req.SlackID == "" {
		req.SlackID = nil
	}
	if req.SlackID == nil && len(req.SlackChannels) > 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "slack_channels need slack_integration_id")
		return
	}
	if req.SlackID != nil {
		if len(req.SlackChannels) == 0 || len(req.SlackChannels) > 20 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "pick 1 to 20 slack_channels for the assistant to read")
			return
		}
		o, ok := s.openMessageReader(w, r, a, *req.SlackID)
		if !ok {
			return
		}
		list, err := o.reader.Channels(ctx, o.Client, o.Creds)
		_ = s.integrations.Checked(ctx, s.pool, a.WorkspaceID, o.ID, nil, err)
		if err != nil {
			s.connectorFailed(w, r, err)
			return
		}
		for _, id := range req.SlackChannels {
			i := slices.IndexFunc(list, func(c connectors.Channel) bool { return c.ID == id })
			if i < 0 {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "Slack has no channel "+id+" this app can see; invite the app to a private channel first")
				return
			}
			if !slices.ContainsFunc(channels, func(c connectors.Channel) bool { return c.ID == id }) {
				channels = append(channels, list[i])
			}
		}
		for _, ch := range channels {
			if ch.Private {
				continue
			}
			if err := o.reader.Join(ctx, o.Client, o.Creds, ch.ID); err != nil {
				s.connectorFailed(w, r, err)
				return
			}
		}
	}
	chJSON, _ := json.Marshal(channels)
	var v assistantSettingView
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO support_assistant_settings (workspace_id, brand_id, chat_mode, email_mode, name, find_on_website, slack_integration_id, slack_channels)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (workspace_id, COALESCE(brand_id, '00000000-0000-0000-0000-000000000000'::uuid)) DO UPDATE SET
				chat_mode = EXCLUDED.chat_mode, email_mode = EXCLUDED.email_mode, name = EXCLUDED.name, find_on_website = EXCLUDED.find_on_website,
				slack_integration_id = EXCLUDED.slack_integration_id, slack_channels = EXCLUDED.slack_channels, updated_at = now()
			RETURNING id::text`, a.WorkspaceID, brand, req.ChatMode, req.EmailMode, req.Name, website, req.SlackID, chJSON).Scan(&id); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, assistantSettingSelect+` WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if v, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[assistantSettingView]); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "support.assistant_updated", "support_assistant_setting", id, map[string]any{
			"brand": brand != nil, "chat_mode": req.ChatMode, "email_mode": req.EmailMode, "named": req.Name != "", "find_on_website": website,
			"slack_channels": len(channels)})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) deleteAssistantSetting(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, err := s.slaScope(r, a)
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such brand")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	ctx := r.Context()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `DELETE FROM support_assistant_settings WHERE workspace_id = $1 AND brand_id IS NOT DISTINCT FROM $2::uuid
			RETURNING id::text`, a.WorkspaceID, brand).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "support.assistant_removed", "support_assistant_setting", id, map[string]any{"brand": brand != nil})
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "no assistant settings for that scope")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// getConversationAssistant returns the assistant's latest run on a
// conversation, or null.
func (s *Server) getConversationAssistant(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	c, err := s.supportConversation(ctx, a, r.PathValue("id"))
	if s.supportError(w, r, err) {
		return
	}
	v, err := s.scanAssistantRun(ctx, s.pool.QueryRow(ctx, assistantRunSelect+` WHERE r.conversation_id = $1 AND r.workspace_id = $2
		ORDER BY r.created_at DESC LIMIT 1`, c.ID, a.WorkspaceID))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"run": nil})
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": v})
}

// runConversationAssistant asks the assistant now, whatever the setting:
// "Draft with AI" in the conversation.
func (s *Server) runConversationAssistant(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	c, err := s.supportConversation(ctx, a, r.PathValue("id"))
	if s.supportError(w, r, err) {
		return
	}
	if !s.supportWrites(w, r, a, c) {
		return
	}
	actor := nilIfEmpty(a.ActorID)
	v, err := s.runAssistant(ctx, a.WorkspaceID, c.ID, "", "person", actor, "draft")
	var inv errInvalid
	if errors.As(err, &inv) {
		writeError(w, r, http.StatusConflict, "nothing_to_answer", string(inv))
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v.Reason == "no_ai" {
		code, _ := v.Checks["ai_problem"].(string)
		msg, _ := v.Checks["ai_message"].(string)
		writeError(w, r, http.StatusConflict, code, msg)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// discardAssistantRun marks a draft as not used.
func (s *Server) discardAssistantRun(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "run not found")
		return
	}
	var conv string
	err := s.pool.QueryRow(ctx, `SELECT conversation_id::text FROM support_assistant_runs WHERE id = $1 AND workspace_id = $2`, id, a.WorkspaceID).Scan(&conv)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "run not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	c, err := s.supportConversation(ctx, a, conv)
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "run not found")
		return
	}
	if s.supportError(w, r, err) || !s.supportWrites(w, r, a, c) {
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE support_assistant_runs SET outcome = 'discarded', decided_by = $2, decided_at = now()
			WHERE id = $1 AND outcome = 'drafted'`, id, nilIfEmpty(a.ActorID))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errAlreadyDecided
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "support.assistant_discarded", "support_conversation", c.ID, map[string]any{"run_id": id})
	})
	if errors.Is(err, errAlreadyDecided) {
		writeError(w, r, http.StatusConflict, "already_decided", "this draft was already sent or discarded")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v, err := s.assistantRun(ctx, a.WorkspaceID, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

var errAlreadyDecided = errors.New("already decided")

// useAssistantRun marks the draft a reply came from as sent (as drafted)
// or edited, in the reply's transaction.
func useAssistantRun(ctx context.Context, tx pgx.Tx, ws, conv, run, actor, body string) error {
	var reply string
	err := tx.QueryRow(ctx, `SELECT reply FROM support_assistant_runs WHERE id = $1 AND workspace_id = $2 AND conversation_id = $3
		AND outcome = 'drafted' FOR UPDATE`, run, ws, conv).Scan(&reply)
	if errors.Is(err, pgx.ErrNoRows) {
		return errInvalid("assistant_run_id is not a draft waiting on this conversation")
	}
	if err != nil {
		return err
	}
	outcome := "edited"
	if strings.TrimSpace(reply) == strings.TrimSpace(body) {
		outcome = "sent"
	}
	_, err = tx.Exec(ctx, `UPDATE support_assistant_runs SET outcome = $2, decided_by = $3, decided_at = now() WHERE id = $1`, run, outcome, nilIfEmpty(actor))
	return err
}

// assistantStats is the assistant's report: how often it answered, asked,
// handed off, and how much of what it drafted the team sent as it was.
func (s *Server) assistantStats(w http.ResponseWriter, r *http.Request) {
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
	rows, err := s.pool.Query(r.Context(), `
		SELECT r.action, r.outcome, r.reason, count(*)::int FROM support_assistant_runs r
		WHERE r.workspace_id = $1 AND r.created_at > now() - make_interval(days => $2)`+sharedBrandSQL(brand, "r.brand_id", &args)+`
		GROUP BY 1, 2, 3`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	actions := map[string]int{"answer": 0, "clarify": 0, "handoff": 0, "skipped": 0}
	outcomes := map[string]int{"drafted": 0, "sent": 0, "edited": 0, "discarded": 0, "failed": 0}
	reasons := map[string]int{}
	var action, outcome, reason string
	var n, total int
	if _, err := pgx.ForEachRow(rows, []any{&action, &outcome, &reason, &n}, func() error {
		total += n
		actions[action] += n
		if action == "answer" || action == "clarify" {
			outcomes[outcome] += n
		}
		if action == "handoff" && reason != "" {
			reasons[reason] += n
		}
		return nil
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": days, "runs": total, "actions": actions, "drafts": outcomes, "handoff_reasons": reasons})
}

type openedReader struct {
	*connectors.Opened
	reader connectors.MessageReader
}

// openMessageReader opens one of the workspace's connections that can read
// chosen channels (Slack (knowledge)), or answers why not.
func (s *Server) openMessageReader(w http.ResponseWriter, r *http.Request, a *access, id string) (openedReader, bool) {
	const notOne = "slack_integration_id is not a Slack (knowledge) connection in this workspace"
	if !validUUID(id) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", notOne)
		return openedReader{}, false
	}
	o, err := s.integrations.Open(r.Context(), s.pool, a.WorkspaceID, id)
	if errors.Is(err, connectors.ErrNotFound) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", notOne)
		return openedReader{}, false
	}
	if err != nil {
		s.connectorFailed(w, r, err)
		return openedReader{}, false
	}
	reader, ok := o.Connector.(connectors.MessageReader)
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "that connection can't read channels; connect Slack (knowledge) under Connectors")
		return openedReader{}, false
	}
	return openedReader{o, reader}, true
}

// listAssistantSlackChannels lists the channels a Slack (knowledge)
// connection can read, to pick from.
func (s *Server) listAssistantSlackChannels(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	o, ok := s.openMessageReader(w, r, a, r.URL.Query().Get("integration_id"))
	if !ok {
		return
	}
	list, err := o.reader.Channels(r.Context(), o.Client, o.Creds)
	_ = s.integrations.Checked(r.Context(), s.pool, a.WorkspaceID, o.ID, nil, err)
	if err != nil {
		s.connectorFailed(w, r, err)
		return
	}
	if list == nil {
		list = []connectors.Channel{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// testAssistant asks the assistant a made-up question, as a customer would,
// without a conversation: what it would answer, ask or hand off, with its
// checks. Nothing is stored or sent.
func (s *Server) testAssistant(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Question string  `json:"question"`
		Channel  string  `json:"channel"`
		Brand    *string `json:"brand"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Question = strings.TrimSpace(req.Question)
	if req.Question == "" || utf8.RuneCountInString(req.Question) > 2000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "question is 1 to 2000 characters")
		return
	}
	if req.Channel == "" {
		req.Channel = "email"
	}
	if req.Channel != "email" && req.Channel != "chat" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "channel is email or chat")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	var brand *string
	if req.Brand != nil && *req.Brand != "" {
		id, err := s.brandIDFor(ctx, a.WorkspaceID, *req.Brand)
		if errors.Is(err, errNotFound) || err == nil && !a.sees(&id) {
			writeError(w, r, http.StatusNotFound, "not_found", "no such brand")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		brand = &id
	} else if len(a.Brands) == 1 {
		brand = &a.Brands[0]
	}
	start := time.Now()
	out := map[string]any{"cited": []assistantCit{}, "checks": map[string]any{}}
	if reason, handoff := assistant.Stop(assistant.Before{Body: req.Question}); reason != "" {
		action := "skipped"
		if handoff {
			action = "handoff"
		}
		out["action"], out["reason"], out["reply"] = action, reason, ""
	} else {
		name, business, err := s.assistantName(ctx, s.pool, a.WorkspaceID, brand)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		res, err := s.assistantAnswer(ctx, assistantInput{WS: a.WorkspaceID, Brand: brand, Channel: assistant.Channel(req.Channel),
			Name: name, Business: business, Thread: []assistantTurn{{From: "customer", Text: req.Question}}})
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if res.aiProblem != nil {
			writeError(w, r, http.StatusConflict, res.aiProblem.Code, res.aiProblem.Message)
			return
		}
		centre, err := s.liveHelpCentre(ctx, a.WorkspaceID, brand)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		cited := []assistantCit{}
		for _, p := range res.Cited {
			c := assistantCit{ID: p.ItemID, Title: p.Title}
			if p.Visibility == "public" {
				c.URL = helpArticleURL(centre, p.ItemID, p.Title)
			}
			cited = append(cited, c)
		}
		out["action"], out["reason"], out["reply"], out["cited"], out["checks"] = res.Action, res.Reason, res.Reply, cited, res.Checks
	}
	out["duration_ms"] = time.Since(start).Milliseconds()
	track.Emit(ctx, track.Event{Name: "support.assistant_tested", WorkspaceID: a.WorkspaceID, UserID: a.personID(), ActorID: a.ActorID,
		ActorType: a.ActorType, RequestID: RequestID(ctx),
		Props: map[string]any{"action": out["action"], "reason": out["reason"], "channel": req.Channel, "duration_ms": out["duration_ms"]}})
	writeJSON(w, http.StatusOK, out)
}
