package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"errors"
	"html/template"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/alerts"
	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/stories"
)

// The public quote approval page: the expert opens their link (from the
// email, Slack or anywhere) and approves the quote's exact words, suggests
// other words or declines, with no account. Server-rendered, noindex, no
// scripts at all and no third-party anything; forms post back to the same
// address with a double-submit token (a cookie that matches a field), and
// cross-site posts are refused. The link's secret is looked up by its
// hash; a link that is unknown, used, expired or void gets the same plain
// page. A decision records exactly what a person's approval would, as the
// expert, through the link.

//go:embed story_quote_approval.html
var quoteApprovalHTML string

var quoteApprovalTemplate = template.Must(template.New("quote_approval").Parse(quoteApprovalHTML))

// quoteApprovalCookie carries the page's form token.
const quoteApprovalCookie = "outflare_quote_approval"

// quoteApprovalPageView is what the page shows.
type quoteApprovalPageView struct {
	// State: ask, done, gone or limited.
	State                                   string
	Brand, Expert, Headline, Quote, Context string
	Embargo, Expires                        string
	Action, CSRF, Message                   string
	Suggested, Note                         string
	OpenSuggest                             bool
	// Done is the decision made: approved, changes_suggested or declined.
	Done string
}

// quoteApprovalRequest is a request as its link opens it.
type quoteApprovalRequest struct {
	ID, WorkspaceID, BrandID, StoryID, QuoteID, ExpertID, RecordID string
	Status, Body                                                   string
	ExpiresAt                                                      time.Time
	Expert, Brand, Headline, Release                               string
	Embargo                                                        *time.Time
	QuoteStatus, QuoteBody, QuoteExpert                            string
	RequestedBy                                                    *string
	OpenedAt                                                       *time.Time
}

// live reports whether the link still decides: waiting for the expert,
// not expired, and the quote still says the words asked about, by the
// same expert, as a draft.
func (q *quoteApprovalRequest) live(now time.Time) bool {
	return (q.Status == "open" || q.Status == "sending") && q.ExpiresAt.After(now) &&
		q.QuoteStatus == "draft" && q.QuoteBody == q.Body && q.QuoteExpert == q.ExpertID
}

// loadQuoteApproval finds the request a link's secret opens, or nil.
// lock holds its row for a decision, in tx.
func loadQuoteApproval(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, secret string, lock bool) (*quoteApprovalRequest, error) {
	if !strings.HasPrefix(secret, auth.PrefixQuoteApproval) || len(secret) > 100 {
		return nil, nil
	}
	forUpdate := ""
	if lock {
		forUpdate = " FOR UPDATE OF a"
	}
	var v quoteApprovalRequest
	err := q.QueryRow(ctx, `
		SELECT a.id::text, a.workspace_id::text, a.brand_id::text, a.story_id::text, a.quote_id::text, a.expert_id::text, x.record_id::text,
			a.status, a.body, a.expires_at, r.name, b.name, st.title, st.release, st.embargo_until,
			sq.status, sq.body, sq.expert_id::text, a.requested_by::text, a.opened_at
		FROM story_quote_approvals a
		JOIN story_quotes sq ON sq.id = a.quote_id AND sq.workspace_id = a.workspace_id AND sq.story_id = a.story_id
		JOIN stories st ON st.id = a.story_id AND st.workspace_id = a.workspace_id AND st.brand_id = a.brand_id
		JOIN pr_experts x ON x.id = a.expert_id AND x.workspace_id = a.workspace_id AND x.brand_id = a.brand_id
		JOIN records r ON r.id = x.record_id
		JOIN brands b ON b.id = a.brand_id AND b.workspace_id = a.workspace_id
		WHERE a.token_hash = $1`+forUpdate, auth.HashToken(secret)).Scan(&v.ID, &v.WorkspaceID, &v.BrandID, &v.StoryID, &v.QuoteID,
		&v.ExpertID, &v.RecordID, &v.Status, &v.Body, &v.ExpiresAt, &v.Expert, &v.Brand, &v.Headline, &v.Release, &v.Embargo,
		&v.QuoteStatus, &v.QuoteBody, &v.QuoteExpert, &v.RequestedBy, &v.OpenedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &v, err
}

// writeQuoteApprovalPage renders the page with headers that keep it out
// of search engines, caches, frames and other sites' referrers.
func (s *Server) writeQuoteApprovalPage(w http.ResponseWriter, r *http.Request, status int, v quoteApprovalPageView) {
	var buf bytes.Buffer
	if err := quoteApprovalTemplate.Execute(&buf, v); err != nil {
		s.internal(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// askView is the page asking the expert to decide, with a fresh form
// token in a cookie.
func (s *Server) askView(w http.ResponseWriter, r *http.Request, q *quoteApprovalRequest) quoteApprovalPageView {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	csrf := base64.RawURLEncoding.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{Name: quoteApprovalCookie, Value: csrf, Path: "/quote-approval/", HttpOnly: true,
		Secure: s.secureCookies, SameSite: http.SameSiteStrictMode, MaxAge: 6 * 3600})
	v := quoteApprovalPageView{State: "ask", Brand: q.Brand, Expert: q.Expert, Headline: q.Headline, Quote: q.Body,
		Context: stories.InContext(q.Release, q.Body), Expires: q.ExpiresAt.UTC().Format("2 January 2006"),
		Action: r.URL.Path, CSRF: csrf, Suggested: q.Body}
	if v.Expert == "" {
		v.Expert = "Hello"
	}
	if q.Embargo != nil {
		v.Embargo = q.Embargo.UTC().Format("2 January 2006, 15:04 MST")
	}
	return v
}

func (s *Server) quoteApprovalPage(w http.ResponseWriter, r *http.Request) {
	if !s.quoteApprovalLimit.hit(networkKey(clientIP(r))) {
		metrics.QuoteApprovalPage("view", "limited")
		s.writeQuoteApprovalPage(w, r, http.StatusTooManyRequests, quoteApprovalPageView{State: "limited"})
		return
	}
	ctx := r.Context()
	q, err := loadQuoteApproval(ctx, s.pool, r.PathValue("token"), false)
	if err != nil {
		metrics.QuoteApprovalPage("view", "error")
		s.internal(w, r, err)
		return
	}
	if q == nil || !q.live(time.Now()) {
		metrics.QuoteApprovalPage("view", "gone")
		s.writeQuoteApprovalPage(w, r, http.StatusNotFound, quoteApprovalPageView{State: "gone"})
		return
	}
	// The first time a person (not a link preview) opens it.
	if q.OpenedAt == nil && !helpBot(r) {
		err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `UPDATE story_quote_approvals SET opened_at = now(), updated_at = now() WHERE id = $1 AND opened_at IS NULL`, q.ID)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			data := map[string]any{"brand_id": q.BrandID, "story_id": q.StoryID, "quote_id": q.QuoteID, "expert_id": q.ExpertID, "request_id": q.ID}
			return audit.Record(ctx, tx, audit.Event{WorkspaceID: q.WorkspaceID, ActorType: "expert_link", Action: "story_quote.approval_opened",
				TargetType: "story", TargetID: q.StoryID, RequestID: RequestID(ctx), BrandID: q.BrandID, Data: data, Props: data})
		})
		if err != nil {
			metrics.QuoteApprovalPage("view", "error")
			s.internal(w, r, err)
			return
		}
	}
	metrics.QuoteApprovalPage("view", "ok")
	s.writeQuoteApprovalPage(w, r, http.StatusOK, s.askView(w, r, q))
}

// quoteApprovalDecisions maps the form's choice to the request's status.
var quoteApprovalDecisions = map[string]string{"approve": "approved", "suggest": "changes_suggested", "decline": "declined"}

func (s *Server) quoteApprovalDecide(w http.ResponseWriter, r *http.Request) {
	if !s.quoteApprovalLimit.hit(networkKey(clientIP(r))) {
		metrics.QuoteApprovalPage("decide", "limited")
		s.writeQuoteApprovalPage(w, r, http.StatusTooManyRequests, quoteApprovalPageView{State: "limited"})
		return
	}
	ctx := r.Context()
	secret := r.PathValue("token")
	q, err := loadQuoteApproval(ctx, s.pool, secret, false)
	if err != nil {
		metrics.QuoteApprovalPage("decide", "error")
		s.internal(w, r, err)
		return
	}
	now := time.Now()
	if q == nil || !q.live(now) {
		metrics.QuoteApprovalPage("decide", "gone")
		s.writeQuoteApprovalPage(w, r, http.StatusNotFound, quoteApprovalPageView{State: "gone"})
		return
	}
	// Another site can't post for the expert: browsers mark it, and the
	// form's token must match the cookie the page set.
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	parseErr := r.ParseForm()
	cookie, _ := r.Cookie(quoteApprovalCookie)
	form := r.PostForm.Get("csrf")
	if parseErr != nil || r.Header.Get("Sec-Fetch-Site") == "cross-site" || cookie == nil || form == "" ||
		subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(form)) != 1 {
		metrics.QuoteApprovalPage("decide", "invalid")
		v := s.askView(w, r, q)
		v.Message = "That didn't go through, so nothing was saved. Please choose again."
		s.writeQuoteApprovalPage(w, r, http.StatusForbidden, v)
		return
	}
	decision, ok := quoteApprovalDecisions[r.PostForm.Get("decision")]
	words := strings.TrimSpace(strings.ReplaceAll(r.PostForm.Get("body"), "\r\n", "\n"))
	note := strings.TrimSpace(r.PostForm.Get("note"))
	again := func(msg string) {
		metrics.QuoteApprovalPage("decide", "invalid")
		v := s.askView(w, r, q)
		v.Message, v.Note = msg, note
		if decision == "changes_suggested" {
			v.OpenSuggest, v.Suggested = true, words
		}
		s.writeQuoteApprovalPage(w, r, http.StatusBadRequest, v)
	}
	switch {
	case !ok:
		again("Choose one: approve, suggest a change or decline.")
		return
	case utf8.RuneCountInString(note) > 1000:
		again("Keep the note to 1,000 characters.")
		return
	case decision == "changes_suggested" && (words == "" || utf8.RuneCountInString(words) > 2000):
		again("Write the words you'd like, up to 2,000 characters.")
		return
	case decision == "changes_suggested" && stories.SameWords(words, q.Body):
		again("These are the words already there. To agree to them, press Approve these words.")
		return
	}
	gone := false
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Again under the lock: a person may have changed the quote, or the
		// expert pressed twice.
		q, err := loadQuoteApproval(ctx, tx, secret, true)
		if err != nil {
			return err
		}
		if q == nil || !q.live(time.Now()) {
			gone = true
			return nil
		}
		return s.decideQuote(ctx, tx, q, decision, words, note)
	})
	if err != nil {
		metrics.QuoteApprovalPage("decide", "error")
		s.internal(w, r, err)
		return
	}
	if gone {
		metrics.QuoteApprovalPage("decide", "gone")
		s.writeQuoteApprovalPage(w, r, http.StatusNotFound, quoteApprovalPageView{State: "gone"})
		return
	}
	metrics.QuoteApprovalPage("decide", "ok")
	metrics.QuoteApprovalDecided(decision)
	http.SetCookie(w, &http.Cookie{Name: quoteApprovalCookie, Path: "/quote-approval/", MaxAge: -1, HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteStrictMode})
	s.writeQuoteApprovalPage(w, r, http.StatusOK, quoteApprovalPageView{State: "done", Done: decision, Brand: q.Brand})
}

// decideQuote records the expert's decision, in tx: the request is used,
// the quote approved exactly as a person's approval would be (by the
// expert, through the link) or given the expert's words as a draft, and
// the team gets an alert.
func (s *Server) decideQuote(ctx context.Context, tx pgx.Tx, q *quoteApprovalRequest, decision, words, note string) error {
	suggested := ""
	if decision == "changes_suggested" {
		suggested = words
	}
	if _, err := tx.Exec(ctx, `UPDATE story_quote_approvals SET status = $2, suggested_body = $3, expert_note = $4, decided_at = now(),
			token_hash = NULL, updated_at = now() WHERE id = $1`, q.ID, decision, suggested, note); err != nil {
		return err
	}
	// The link has done its job: it leaves the expert's lead, and an email
	// still waiting in its sequence (asked again meanwhile) stays unsent.
	if _, err := tx.Exec(ctx, `
		WITH r AS (SELECT lead_id, enrollment_id FROM story_quote_approvals WHERE id = $1),
		stopped AS (UPDATE enrollments SET status = 'stopped', status_reason = 'quote_approval_decided', next_send_at = NULL, updated_at = now()
			WHERE id = (SELECT enrollment_id FROM r) AND status = 'active')
		UPDATE leads SET custom = custom - $2::text, updated_at = now() WHERE id = (SELECT lead_id FROM r)`, q.ID, quoteApprovalLinkVar); err != nil {
		return err
	}
	data := map[string]any{"brand_id": q.BrandID, "story_id": q.StoryID, "quote_id": q.QuoteID, "expert_id": q.ExpertID,
		"record_id": q.RecordID, "request_id": q.ID, "via": "link"}
	action := "story_quote.declined"
	switch decision {
	case "approved":
		action = "story_quote.approved"
		data["status"], data["was"], data["body_changed"] = "approved", "draft", false
		if _, err := tx.Exec(ctx, `UPDATE story_quotes SET status = 'approved', approval_note = $3, approved_by = NULL, approved_at = now(),
				approved_via = 'expert_link', suggested_at = NULL, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, q.WorkspaceID, q.QuoteID,
			"Approved by the expert through an approval link on "+time.Now().UTC().Format("2 Jan 2006")); err != nil {
			return err
		}
	case "changes_suggested":
		action = "story_quote.change_suggested"
		data["body_changed"] = true
		// The quote stays a draft, now with the expert's words, for a
		// person to review and ask about again.
		if _, err := tx.Exec(ctx, `UPDATE story_quotes SET body = $3, status = 'draft', approval_note = '', approved_by = NULL, approved_at = NULL,
				approved_via = '', suggested_at = now(), updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, q.WorkspaceID, q.QuoteID, words); err != nil {
			return err
		}
		if err := storyChanged(ctx, tx, q.WorkspaceID, q.StoryID); err != nil {
			return err
		}
	}
	props := map[string]any{"brand_id": q.BrandID, "story_id": q.StoryID, "quote_id": q.QuoteID, "request_id": q.ID, "via": "link",
		"has_note": note != ""}
	if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: q.WorkspaceID, ActorType: "expert_link", Action: action,
		TargetType: "story", TargetID: q.StoryID, RequestID: RequestID(ctx), BrandID: q.BrandID, Data: data, Props: props}); err != nil {
		return err
	}
	// Tell whoever asked, else the owners and admins.
	var owner *string
	if q.RequestedBy != nil {
		if err := tx.QueryRow(ctx, `SELECT id::text FROM users WHERE actor_id = $1`, *q.RequestedBy).Scan(&owner); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	_, err := alerts.Raise(ctx, tx, alerts.Alert{WorkspaceID: q.WorkspaceID, BrandID: &q.BrandID, Kind: stories.AlertQuoteDecided,
		DedupeKey: q.ID, TargetType: "story_quote", TargetID: q.QuoteID, RecordID: &q.RecordID, OwnerID: owner,
		Data: map[string]any{"decision": decision, "story_id": q.StoryID, "request_id": q.ID}, RequestID: RequestID(ctx)})
	return err
}
