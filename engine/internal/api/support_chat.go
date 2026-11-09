package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/support"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Live chat on the business's website. The embed code loads a small
// script that opens the chat in an iframe served from here, so the page
// can't read the conversation and the chat can't read the page. A browser
// may show it only on the widget's sites (frame-ancestors). The visitor is
// known by a random token the iframe keeps; only its hash is stored. A
// site that signs a signed-in user's email with the widget's identity
// secret has that email trusted; any other email is what the visitor typed.

//go:embed support_chat.html
var supportChatHTML string

//go:embed support_chat_loader.js
var supportChatLoader string

const (
	chatKeyPrefix   = "ofw_"
	chatTokenPrefix = "ofv_"
	// The longest message a visitor can send.
	chatMaxBody = 5000
	// How long a token stays: a visitor coming back within it sees their
	// conversation.
	chatSecretPurpose = "support-chat-identity"
)

var chatKeyRe = regexp.MustCompile(`^ofw_[A-Za-z0-9]{24}$`)

type supportChatSettings struct {
	Color        string `json:"color"`
	Title        string `json:"title"`
	Greeting     string `json:"greeting"`
	RequireEmail bool   `json:"require_email"`
	// Search the help centre before writing.
	ShowArticles bool `json:"show_articles"`
}

func chatSettingsFrom(raw []byte) supportChatSettings {
	st := supportChatSettings{Color: "#111827", Title: "Chat with us", Greeting: "Hi! Ask us anything, we usually reply within a few hours.", ShowArticles: true}
	_ = json.Unmarshal(raw, &st)
	return st
}

type supportChatWidgetView struct {
	ID             string              `json:"id"`
	BrandID        *string             `json:"brand_id"`
	Key            string              `json:"key"`
	Enabled        bool                `json:"enabled"`
	AllowedOrigins []string            `json:"allowed_origins"`
	Settings       supportChatSettings `json:"settings"`
	// Whether the site can vouch for signed-in users (an identity secret
	// was made).
	IdentityVerification bool      `json:"identity_verification"`
	EmbedCode            string    `json:"embed_code"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

const supportChatWidgetSelect = `SELECT id::text, brand_id::text, key, enabled, allowed_origins, settings, identity_secret IS NOT NULL, created_at, updated_at
	FROM support_chat_widgets`

func (s *Server) scanChatWidget(row pgx.Row) (supportChatWidgetView, error) {
	var v supportChatWidgetView
	var raw []byte
	err := row.Scan(&v.ID, &v.BrandID, &v.Key, &v.Enabled, &v.AllowedOrigins, &raw, &v.IdentityVerification, &v.CreatedAt, &v.UpdatedAt)
	v.Settings = chatSettingsFrom(raw)
	if v.AllowedOrigins == nil {
		v.AllowedOrigins = []string{}
	}
	v.EmbedCode = `<script src="` + strings.TrimRight(s.publicURL, "/") + `/chat/` + v.Key + `/widget.js" async></script>`
	return v, err
}

func (s *Server) routesSupportChat(mux *http.ServeMux) {
	read, manage := auth.ScopeInboxRead, auth.ScopeInboxManage
	base := "/v1/workspaces/{ws}/support/chat-widgets"
	brandBase := "/v1/brands/{brand}/support/chat-widgets"
	mux.HandleFunc("GET "+base, s.inBrand(read, s.listChatWidgets))
	mux.HandleFunc("GET "+brandBase, s.onBrand(read, s.listChatWidgets))
	mux.HandleFunc("POST "+base, s.inBrand(manage, s.createChatWidget))
	mux.HandleFunc("POST "+brandBase, s.onBrand(manage, s.createChatWidget))
	mux.HandleFunc("PATCH "+base+"/{id}", s.inBrand(manage, s.updateChatWidget))
	mux.HandleFunc("PATCH "+brandBase+"/{id}", s.onBrand(manage, s.updateChatWidget))
	mux.HandleFunc("DELETE "+base+"/{id}", s.inBrand(manage, s.deleteChatWidget))
	mux.HandleFunc("DELETE "+brandBase+"/{id}", s.onBrand(manage, s.deleteChatWidget))
	mux.HandleFunc("POST "+base+"/{id}/identity-secret", s.inBrand(manage, s.rotateChatSecret))
	mux.HandleFunc("POST "+brandBase+"/{id}/identity-secret", s.onBrand(manage, s.rotateChatSecret))

	// What the website loads: no account, the widget's key in the path.
	mux.HandleFunc("GET /chat/{key}/widget.js", s.chatLoader)
	mux.HandleFunc("GET /chat/{key}/frame", s.chatFrame)
	mux.HandleFunc("POST /chat/{key}/session", s.chatSession)
	mux.HandleFunc("GET /chat/{key}/messages", s.chatMessages)
	mux.HandleFunc("POST /chat/{key}/messages", s.chatSend)
	mux.HandleFunc("GET /chat/{key}/articles", s.chatArticles)
	mux.HandleFunc("POST /chat/{key}/messages/{id}/feedback", s.chatFeedback)
}

func (s *Server) listChatWidgets(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID}
	rows, err := s.pool.Query(r.Context(), supportChatWidgetSelect+` WHERE workspace_id = $1`+brand.sql("brand_id", &args)+` ORDER BY created_at`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (supportChatWidgetView, error) { return s.scanChatWidget(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

type chatWidgetInput struct {
	BrandID        optional[string]     `json:"brand_id"`
	Enabled        *bool                `json:"enabled"`
	AllowedOrigins *[]string            `json:"allowed_origins"`
	Settings       *supportChatSettings `json:"settings"`
}

var hexColorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// cleanChatSettings checks what a person sets: text the visitor reads is
// plain text, kept short.
func cleanChatSettings(st supportChatSettings) (supportChatSettings, error) {
	if !hexColorRe.MatchString(st.Color) {
		return st, errInvalid("settings.color is a colour such as #0f766e")
	}
	st.Title, st.Greeting = strings.TrimSpace(st.Title), strings.TrimSpace(st.Greeting)
	if st.Title == "" || utf8.RuneCountInString(st.Title) > 60 {
		return st, errInvalid("settings.title is 1 to 60 characters")
	}
	if utf8.RuneCountInString(st.Greeting) > 300 {
		return st, errInvalid("settings.greeting is at most 300 characters")
	}
	return st, nil
}

func (s *Server) createChatWidget(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req chatWidgetInput
	if !decode(w, r, &req) {
		return
	}
	s.saveChatWidget(w, r, a, "", req)
}

func (s *Server) updateChatWidget(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req chatWidgetInput
	if !decode(w, r, &req) {
		return
	}
	if !validUUID(r.PathValue("id")) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such chat widget")
		return
	}
	s.saveChatWidget(w, r, a, r.PathValue("id"), req)
}

func (s *Server) saveChatWidget(w http.ResponseWriter, r *http.Request, a *access, id string, req chatWidgetInput) {
	ctx := r.Context()
	cur := supportChatWidgetView{Enabled: true, AllowedOrigins: []string{}, Settings: chatSettingsFrom(nil)}
	if id != "" {
		v, err := s.scanChatWidget(s.pool.QueryRow(ctx, supportChatWidgetSelect+` WHERE id = $1 AND workspace_id = $2`, id, a.WorkspaceID))
		if errors.Is(err, pgx.ErrNoRows) || err == nil && !a.sees(v.BrandID) {
			writeError(w, r, http.StatusNotFound, "not_found", "no such chat widget")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		cur = v
	}
	brand := cur.BrandID
	if req.BrandID.Set {
		brand = nil
		if req.BrandID.Value != nil && *req.BrandID.Value != "" {
			b, err := s.brandIDFor(ctx, a.WorkspaceID, *req.BrandID.Value)
			if errors.Is(err, errNotFound) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "brand_id is not a brand in this workspace")
				return
			}
			if err != nil {
				s.internal(w, r, err)
				return
			}
			brand = &b
		}
	}
	// Brands share no widget: one with no brand named is the route's, else
	// the workspace's first.
	if brand == nil {
		b, err := s.rowBrand(ctx, a)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if b != "" {
			brand = &b
		}
	}
	if !a.writes(brand) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "brand_id needs to be one of your brands")
		return
	}
	if req.Enabled != nil {
		cur.Enabled = *req.Enabled
	}
	if req.AllowedOrigins != nil {
		o, err := cleanOrigins(*req.AllowedOrigins)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		cur.AllowedOrigins = o
	}
	if len(cur.AllowedOrigins) == 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "allowed_origins needs your site, such as example.com: the chat shows only there")
		return
	}
	if req.Settings != nil {
		st, err := cleanChatSettings(*req.Settings)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		cur.Settings = st
	}
	raw, _ := json.Marshal(cur.Settings)
	action := "support.chat_widget_updated"
	var v supportChatWidgetView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if id == "" {
			action = "support.chat_widget_created"
			if err := tx.QueryRow(ctx, `INSERT INTO support_chat_widgets (workspace_id, brand_id, key, enabled, allowed_origins, settings)
				VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`, a.WorkspaceID, brand, newChatKey(), cur.Enabled, cur.AllowedOrigins, raw).Scan(&id); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `UPDATE support_chat_widgets SET brand_id = $3, enabled = $4, allowed_origins = $5, settings = $6, updated_at = now()
			WHERE id = $1 AND workspace_id = $2`, id, a.WorkspaceID, brand, cur.Enabled, cur.AllowedOrigins, raw); err != nil {
			return err
		}
		var err error
		if v, err = s.scanChatWidget(tx.QueryRow(ctx, supportChatWidgetSelect+` WHERE id = $1`, id)); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "support_chat_widget", id, map[string]any{
			"enabled": cur.Enabled, "origins": len(cur.AllowedOrigins), "brand": brand != nil, "require_email": cur.Settings.RequireEmail})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	status := http.StatusOK
	if action == "support.chat_widget_created" {
		status = http.StatusCreated
	}
	writeJSON(w, status, v)
}

// chatWidgetWritable reports whether the caller may change a widget: one of
// the workspace's, of a brand they work on.
func (s *Server) chatWidgetWritable(ctx context.Context, tx pgx.Tx, a *access, id string) (bool, error) {
	var brand *string
	err := tx.QueryRow(ctx, `SELECT brand_id::text FROM support_chat_widgets WHERE id = $1 AND workspace_id = $2`, id, a.WorkspaceID).Scan(&brand)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil && a.writes(brand), err
}

// cmpErr is err, or fallback when err is nil.
func cmpErr(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

func newChatKey() string {
	return chatKeyPrefix + randomAlnum(24)
}

// randomAlnum is n random letters and digits.
func randomAlnum(n int) string {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}

func (s *Server) deleteChatWidget(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such chat widget")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if ok, err := s.chatWidgetWritable(ctx, tx, a, id); err != nil || !ok {
			return cmpErr(err, errNotFound)
		}
		tag, err := tx.Exec(ctx, `DELETE FROM support_chat_widgets WHERE id = $1 AND workspace_id = $2`, id, a.WorkspaceID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "support.chat_widget_deleted", "support_chat_widget", id, nil)
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such chat widget")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// rotateChatSecret makes a new identity secret and shows it once. The site
// signs a signed-in user's email with it (HMAC-SHA256, hex) so the chat
// trusts that email; the old secret stops working.
func (s *Server) rotateChatSecret(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such chat widget")
		return
	}
	secret := "ofc_" + randomAlnum(40)
	sealed := base64.RawURLEncoding.EncodeToString(s.box.Seal([]byte(secret), []byte(chatSecretPurpose+":"+id)))
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if ok, err := s.chatWidgetWritable(ctx, tx, a, id); err != nil || !ok {
			return cmpErr(err, errNotFound)
		}
		tag, err := tx.Exec(ctx, `UPDATE support_chat_widgets SET identity_secret = $3, updated_at = now() WHERE id = $1 AND workspace_id = $2`,
			id, a.WorkspaceID, sealed)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "support.chat_secret_rotated", "support_chat_widget", id, nil)
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such chat widget")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secret": secret})
}

// ---- The website's side ----

// chatWidget is a live widget, by its public key.
type chatWidget struct {
	id, ws   string
	brand    *string
	origins  []string
	settings supportChatSettings
	secret   *string
}

// chatWidgetFrom finds the widget a public request names. Unknown, off or
// malformed: 404, the same for each, so keys can't be probed.
func (s *Server) chatWidgetFrom(w http.ResponseWriter, r *http.Request, route string) (*chatWidget, bool) {
	if !s.chatIPLimit.allow(w, r, networkKey(clientIP(r))) {
		metrics.SupportChatRequest(route, "limited")
		return nil, false
	}
	key := r.PathValue("key")
	cw := &chatWidget{}
	var raw []byte
	err := errNotFound
	if chatKeyRe.MatchString(key) {
		err = s.pool.QueryRow(r.Context(), `SELECT w.id::text, w.workspace_id::text, w.brand_id::text, w.allowed_origins, w.settings, w.identity_secret
			FROM support_chat_widgets w WHERE w.key = $1 AND w.enabled`, key).Scan(&cw.id, &cw.ws, &cw.brand, &cw.origins, &raw, &cw.secret)
	}
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, errNotFound) {
		metrics.SupportChatRequest(route, "not_found")
		writeError(w, r, http.StatusNotFound, "not_found", "no such chat")
		return nil, false
	}
	if err != nil {
		metrics.SupportChatRequest(route, "error")
		s.internal(w, r, err)
		return nil, false
	}
	cw.settings = chatSettingsFrom(raw)
	return cw, true
}

// frameAncestors is the CSP list of sites that may show the chat: each
// whole domain also takes its bare name.
func frameAncestors(origins []string) string {
	var out []string
	for _, o := range origins {
		if host, ok := strings.CutPrefix(o, "https://*."); ok {
			out = append(out, "https://"+host)
		}
		out = append(out, o)
	}
	return strings.Join(out, " ")
}

func (s *Server) chatLoader(w http.ResponseWriter, r *http.Request) {
	cw, ok := s.chatWidgetFrom(w, r, "loader")
	if !ok {
		return
	}
	cfg, _ := json.Marshal(map[string]any{
		"frame": strings.TrimRight(s.publicURL, "/") + "/chat/" + r.PathValue("key") + "/frame",
		"color": cw.settings.Color, "title": cw.settings.Title,
	})
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(strings.Replace(supportChatLoader, "/*CONFIG*/{}", string(cfg), 1)))
	metrics.SupportChatRequest("loader", "ok")
}

func (s *Server) chatFrame(w http.ResponseWriter, r *http.Request) {
	cw, ok := s.chatWidgetFrom(w, r, "frame")
	if !ok {
		return
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	nonce := base64.RawStdEncoding.EncodeToString(b)
	cfg, _ := json.Marshal(map[string]any{"key": r.PathValue("key"), "settings": cw.settings, "identity": cw.secret != nil})
	page := strings.NewReplacer("{{NONCE}}", nonce, "/*CONFIG*/{}", string(cfg)).Replace(supportChatHTML)
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+nonce+"'; style-src 'nonce-"+nonce+"'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors "+frameAncestors(cw.origins))
	_, _ = w.Write([]byte(page))
	metrics.SupportChatRequest("frame", "ok")
}

// chatToken reads the visitor's token, and its hash.
func chatToken(r *http.Request) (string, []byte) {
	t := strings.TrimSpace(r.Header.Get("X-Chat-Token"))
	if !strings.HasPrefix(t, chatTokenPrefix) || len(t) != len(chatTokenPrefix)+40 {
		return "", nil
	}
	h := sha256.Sum256([]byte(t))
	return t, h[:]
}

type chatMessageView struct {
	ID        string    `json:"id"`
	From      string    `json:"from"` // visitor or team
	Name      string    `json:"name"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	// Assistant: the support assistant sent it, so the visitor can say
	// whether it helped; Feedback is what they said (null for nothing yet).
	Assistant bool    `json:"assistant"`
	Feedback  *string `json:"feedback"`
}

// chatThread is what the visitor sees of their latest conversation: their
// messages and the team's replies after a time, never notes or anything of
// another visitor's. status is "" before they have written.
func (s *Server) chatThread(ctx context.Context, cw *chatWidget, token []byte, after time.Time) (string, []chatMessageView, error) {
	_, status, list, err := s.chatThreadOf(ctx, cw, token, after)
	return status, list, err
}

// chatThreadOf is chatThread with the conversation's id ("" before any).
func (s *Server) chatThreadOf(ctx context.Context, cw *chatWidget, token []byte, after time.Time) (string, string, []chatMessageView, error) {
	var conv, status string
	err := s.pool.QueryRow(ctx, `SELECT id::text, status FROM support_conversations
		WHERE workspace_id = $1 AND chat_widget_id = $2 AND chat_token_hash = $3 ORDER BY last_message_at DESC LIMIT 1`,
		cw.ws, cw.id, token).Scan(&conv, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", []chatMessageView{}, nil
	}
	if err != nil {
		return "", "", nil, err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE support_conversations SET visitor_seen_at = now() WHERE id = $1`, conv); err != nil {
		return "", "", nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT m.id::text, CASE m.kind WHEN 'in' THEN 'visitor' ELSE 'team' END,
			CASE WHEN m.kind = 'out' THEN COALESCE(NULLIF(split_part(u.name, ' ', 1), ''), '') ELSE '' END, m.body, m.created_at,
			r.id IS NOT NULL, r.feedback
		FROM support_messages m LEFT JOIN users u ON u.id = m.author_id
		LEFT JOIN support_assistant_runs r ON r.sent_message_id = m.id
		WHERE m.conversation_id = $1 AND m.kind IN ('in', 'out') AND m.created_at > $2
		ORDER BY m.created_at LIMIT 200`, conv, after)
	if err != nil {
		return "", "", nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[chatMessageView])
	return conv, status, list, err
}

// chatTyping says whether the support assistant is writing a reply that
// will reach the visitor on its own (Auto on chat), so the chat can show
// it is typing.
func (s *Server) chatTyping(ctx context.Context, cw *chatWidget, conv string) (bool, error) {
	if conv == "" || s.supportAuto == nil {
		return false, nil
	}
	var brand *string
	if err := s.pool.QueryRow(ctx, `SELECT brand_id::text FROM support_conversations WHERE id = $1`, conv).Scan(&brand); err != nil {
		return false, err
	}
	if !s.autoAvailable(ctx, cw.ws, brand) {
		return false, nil
	}
	mode, err := support.AssistantMode(ctx, s.pool, cw.ws, brand, support.ChannelChat)
	if err != nil || mode != "auto" {
		return false, err
	}
	var busy bool
	err = s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM jobs WHERE workspace_id = $1 AND kind = $2 AND status IN ('queued', 'running')
		AND payload->>'conversation_id' = $3)`, cw.ws, support.JobAssistant, conv).Scan(&busy)
	return busy, err
}

// verifyIdentity checks the site's signature on a signed-in user's email.
func (s *Server) verifyIdentity(cw *chatWidget, email, sig string) bool {
	if cw.secret == nil || email == "" || sig == "" {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(*cw.secret)
	if err != nil {
		return false
	}
	secret, err := s.box.Open(raw, []byte(chatSecretPurpose+":"+cw.id))
	if err != nil {
		return false
	}
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(strings.ToLower(strings.TrimSpace(email))))
	want := hex.EncodeToString(m.Sum(nil))
	return hmac.Equal([]byte(want), []byte(strings.ToLower(sig)))
}

type chatIdentity struct {
	Email     string `json:"email"`
	Name      string `json:"name"`
	Signature string `json:"signature"`
}

func cleanChatEmail(e string) (string, bool) {
	e = strings.ToLower(strings.TrimSpace(e))
	if e == "" {
		return "", true
	}
	a, err := mail.ParseAddress(e)
	if err != nil || a.Address != e || len(e) > 254 {
		return "", false
	}
	return e, true
}

// chatSession starts or resumes the visitor's chat: a new token when they
// have none, and their conversation so far.
func (s *Server) chatSession(w http.ResponseWriter, r *http.Request) {
	cw, ok := s.chatWidgetFrom(w, r, "session")
	if !ok {
		return
	}
	var req struct {
		Identity *chatIdentity `json:"identity"`
	}
	if !decodeLarge(w, r, &req, 4<<10) {
		metrics.SupportChatRequest("session", "invalid")
		return
	}
	token, hash := chatToken(r)
	if token == "" {
		if !s.chatStartLimit.hit(networkKey(clientIP(r))) {
			metrics.SupportChatRequest("session", "limited")
			writeError(w, r, http.StatusTooManyRequests, "rate_limited", "too many new chats from this network; try again later")
			return
		}
		token = chatTokenPrefix + randomAlnum(40)
		sum := sha256.Sum256([]byte(token))
		hash = sum[:]
	}
	ctx := r.Context()
	verified := false
	if req.Identity != nil {
		email, ok := cleanChatEmail(req.Identity.Email)
		verified = ok && s.verifyIdentity(cw, email, req.Identity.Signature)
		if verified {
			err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
				conv, err := support.ChatConversation(ctx, tx, cw.ws, cw.id, hash)
				if err != nil || conv == "" {
					return err
				}
				return support.SetChatContact(ctx, tx, cw.ws, conv, email, truncate(req.Identity.Name, 200), true)
			})
			if err != nil {
				metrics.SupportChatRequest("session", "error")
				s.internal(w, r, err)
				return
			}
		}
	}
	status, list, err := s.chatThread(ctx, cw, hash, time.Time{})
	if err != nil {
		metrics.SupportChatRequest("session", "error")
		s.internal(w, r, err)
		return
	}
	metrics.SupportChatRequest("session", "ok")
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "verified": verified, "status": nilIfEmpty(status), "messages": list})
}

func (s *Server) chatMessages(w http.ResponseWriter, r *http.Request) {
	cw, ok := s.chatWidgetFrom(w, r, "poll")
	if !ok {
		return
	}
	_, hash := chatToken(r)
	if hash == nil {
		metrics.SupportChatRequest("poll", "bad_token")
		writeError(w, r, http.StatusUnauthorized, "unauthenticated", "start a chat first")
		return
	}
	var after time.Time
	if a := r.URL.Query().Get("after"); a != "" {
		t, err := time.Parse(time.RFC3339Nano, a)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "after is a time from the last message")
			return
		}
		after = t
	}
	conv, status, list, err := s.chatThreadOf(r.Context(), cw, hash, after)
	var typing bool
	if err == nil {
		typing, err = s.chatTyping(r.Context(), cw, conv)
	}
	if err != nil {
		metrics.SupportChatRequest("poll", "error")
		s.internal(w, r, err)
		return
	}
	metrics.SupportChatRequest("poll", "ok")
	writeJSON(w, http.StatusOK, map[string]any{"status": nilIfEmpty(status), "messages": list, "typing": typing})
}

func (s *Server) chatSend(w http.ResponseWriter, r *http.Request) {
	cw, ok := s.chatWidgetFrom(w, r, "send")
	if !ok {
		return
	}
	token, hash := chatToken(r)
	if hash == nil {
		metrics.SupportChatRequest("send", "bad_token")
		writeError(w, r, http.StatusUnauthorized, "unauthenticated", "start a chat first")
		return
	}
	var req struct {
		Body     string        `json:"body"`
		Email    string        `json:"email"`
		Name     string        `json:"name"`
		Identity *chatIdentity `json:"identity"`
	}
	if !decodeLarge(w, r, &req, 32<<10) {
		metrics.SupportChatRequest("send", "invalid")
		return
	}
	body := strings.TrimSpace(req.Body)
	if body == "" || utf8.RuneCountInString(body) > chatMaxBody {
		metrics.SupportChatRequest("send", "invalid")
		writeError(w, r, http.StatusBadRequest, "invalid_request", "write a message of up to 5,000 characters")
		return
	}
	email, ok := cleanChatEmail(req.Email)
	if !ok {
		metrics.SupportChatRequest("send", "invalid")
		writeError(w, r, http.StatusBadRequest, "invalid_request", "that email address doesn't look right")
		return
	}
	name := truncate(strings.TrimSpace(req.Name), 200)
	verified := false
	if req.Identity != nil {
		if e, ok := cleanChatEmail(req.Identity.Email); ok && s.verifyIdentity(cw, e, req.Identity.Signature) {
			email, verified = e, true
			if n := strings.TrimSpace(req.Identity.Name); n != "" {
				name = truncate(n, 200)
			}
		}
	}
	if !s.chatSendLimit.hit(token) || !s.chatNetSendLimit.hit(networkKey(clientIP(r))) {
		metrics.SupportChatRequest("send", "limited")
		writeError(w, r, http.StatusTooManyRequests, "rate_limited", "you're sending messages very fast; wait a moment")
		return
	}
	ctx := r.Context()
	if cw.settings.RequireEmail && email == "" {
		var known bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM support_conversations WHERE workspace_id = $1 AND chat_widget_id = $2
			AND chat_token_hash = $3 AND contact_email <> '')`, cw.ws, cw.id, hash).Scan(&known); err != nil {
			s.internal(w, r, err)
			return
		}
		if !known {
			metrics.SupportChatRequest("send", "invalid")
			writeError(w, r, http.StatusBadRequest, "email_required", "leave your email so we can get back to you")
			return
		}
	}
	var got support.Received
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		got, err = support.IntakeChat(ctx, tx, support.ChatMessage{WorkspaceID: cw.ws, WidgetID: cw.id, BrandID: cw.brand, TokenHash: hash,
			Email: email, Name: name, Verified: verified, Body: body})
		return err
	})
	if err != nil {
		metrics.SupportChatRequest("send", "error")
		track.Fail(ctx, s.log, track.Event{Name: "support.chat_failed", WorkspaceID: cw.ws, ActorType: "system", Surface: "system", RequestID: RequestID(ctx)}, "internal", err)
		s.internal(w, r, err)
		return
	}
	metrics.SupportChatRequest("send", "ok")
	writeJSON(w, http.StatusCreated, map[string]any{"id": got.MessageID, "started": got.Started})
}

// chatArticles searches the help centre from the widget, so a visitor can
// find the answer before writing.
func (s *Server) chatArticles(w http.ResponseWriter, r *http.Request) {
	cw, ok := s.chatWidgetFrom(w, r, "articles")
	if !ok {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	list := []supportArticle{}
	if q == "" || !cw.settings.ShowArticles {
		writeJSON(w, http.StatusOK, map[string]any{"data": list})
		return
	}
	if utf8.RuneCountInString(q) > 200 {
		q = string([]rune(q)[:200])
	}
	ctx := r.Context()
	centre, err := s.liveHelpCentre(ctx, cw.ws, cw.brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if centre == nil {
		writeJSON(w, http.StatusOK, map[string]any{"data": list})
		return
	}
	// The widget's own reader: no member, no brands of a member's.
	a := &access{WorkspaceID: cw.ws}
	passages, _, err := s.retrieveKnowledge(ctx, a, knowledgeQuery{q: q, audience: audienceCustomers, brand: cw.brand, limit: 16})
	if err != nil {
		metrics.SupportChatRequest("articles", "error")
		s.internal(w, r, err)
		return
	}
	seen := map[string]bool{}
	for _, p := range passages {
		if p.Visibility != "public" || seen[p.ItemID] || len(list) == 5 {
			continue
		}
		seen[p.ItemID] = true
		list = append(list, supportArticle{ID: p.ItemID, Kind: p.Kind, Title: p.Title, Excerpt: helpExcerpt(p.Text), URL: helpArticleURL(centre, p.ItemID, p.Title)})
	}
	metrics.SupportChatRequest("articles", "ok")
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// chatFeedback records whether the support assistant's answer helped the
// visitor. "No" hands the conversation to the team: it reopens, the
// assistant stops answering in it, and the visitor is told once that a
// person will reply.
func (s *Server) chatFeedback(w http.ResponseWriter, r *http.Request) {
	cw, ok := s.chatWidgetFrom(w, r, "feedback")
	if !ok {
		return
	}
	_, hash := chatToken(r)
	if hash == nil {
		metrics.SupportChatRequest("feedback", "bad_token")
		writeError(w, r, http.StatusUnauthorized, "unauthenticated", "start a chat first")
		return
	}
	var req struct {
		Helpful *bool `json:"helpful"`
	}
	if !decodeLarge(w, r, &req, 1<<10) {
		metrics.SupportChatRequest("feedback", "invalid")
		return
	}
	id := r.PathValue("id")
	if req.Helpful == nil || !validUUID(id) {
		metrics.SupportChatRequest("feedback", "invalid")
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send helpful: true or false for one of the assistant's messages")
		return
	}
	ctx := r.Context()
	verdict := "helpful"
	if !*req.Helpful {
		verdict = "not_helpful"
	}
	var run, conv, channel string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Only a message in the visitor's own conversation, rated once.
		err := tx.QueryRow(ctx, `
			UPDATE support_assistant_runs r SET feedback = $4, feedback_at = now()
			FROM support_conversations c
			WHERE r.sent_message_id = $1 AND c.id = r.conversation_id AND c.workspace_id = $2 AND c.chat_widget_id = $5 AND c.chat_token_hash = $3
			  AND r.feedback IS NULL
			RETURNING r.id::text, r.conversation_id::text, r.channel`, id, cw.ws, hash, verdict, cw.id).Scan(&run, &conv, &channel)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if verdict == "not_helpful" {
			var brand *string
			if err := tx.QueryRow(ctx, `SELECT brand_id::text FROM support_conversations WHERE id = $1`, conv).Scan(&brand); err != nil {
				return err
			}
			res := assistantResult{Action: "handoff", Reason: "not_helpful"}
			if _, err := s.assistantSend(ctx, tx, cw.ws, brand, conv, channel, run, res); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE support_conversations SET status = 'open', updated_at = now() WHERE id = $1 AND status <> 'open'`, conv); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: cw.ws, ActorType: "system", Action: "support.assistant_rated",
			TargetType: "support_conversation", TargetID: conv,
			Data:  map[string]any{"run_id": run, "feedback": verdict, "channel": channel},
			Props: map[string]any{"feedback": verdict, "channel": channel}})
	})
	if errors.Is(err, errNotFound) {
		metrics.SupportChatRequest("feedback", "not_found")
		writeError(w, r, http.StatusNotFound, "not_found", "no answer from the assistant to rate here, or it was rated already")
		return
	}
	if err != nil {
		metrics.SupportChatRequest("feedback", "error")
		s.internal(w, r, err)
		return
	}
	metrics.SupportChatRequest("feedback", "ok")
	writeJSON(w, http.StatusOK, map[string]any{"feedback": verdict})
}
