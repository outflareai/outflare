package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/alerts"
	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/leads"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/policy"
	"github.com/outflareai/outflare/engine/internal/sending"
	"github.com/outflareai/outflare/engine/internal/stories"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Quote approval requests: a person (or a token or agent with seo.manage,
// since asking grants nothing) asks a quote's expert to approve its exact
// words, by email or by a link to paste anywhere. The expert decides on a
// public page (story_quote_approval_page.go). A request is bound to the
// words and the expert, works once and expires after 14 days; this table
// keeps only the link's hash. Changing the words, withdrawing or approving
// the quote, or asking again voids it; removing the expert or the story
// deletes it.
//
// The email is ordinary outreach: the expert becomes a lead with the link
// in a custom field and is enrolled in one of the brand's own Outflare
// sequences, which the sequence engine sends like any other, from the
// mailboxes allocated to that sequence, after its send policy checks
// (suppressions, verification, the sending window, mailbox and domain
// limits). The engine's OnStep hook tells us when it went or why it
// didn't.

// quoteApprovalTTL is how long an approval link works.
const quoteApprovalTTL = 14 * 24 * time.Hour

// quoteApprovalLinkVar is the custom field the sequence's step puts the
// link in: {{custom.approval_link}}.
const quoteApprovalLinkVar = "approval_link"

func (s *Server) routesQuoteApprovals(mux *http.ServeMux) {
	read, manage := auth.ScopeSEORead, auth.ScopeSEOManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/stories/{story}/quotes/{quote}/approval-requests", s.inBrand(read, s.listQuoteApprovals))
	mux.HandleFunc("POST /v1/workspaces/{ws}/stories/{story}/quotes/{quote}/approval-requests", s.inBrand(manage, s.requestQuoteApproval))
	mux.HandleFunc("GET /quote-approval/{token}", s.quoteApprovalPage)
	mux.HandleFunc("POST /quote-approval/{token}", s.quoteApprovalDecide)
}

type quoteApprovalView struct {
	ID       string `json:"id"`
	QuoteID  string `json:"quote_id"`
	ExpertID string `json:"expert_id"`
	// Body is the words the expert was asked about.
	Body     string `json:"body"`
	Delivery string `json:"delivery"`
	// Status: sending, open, failed, approved, changes_suggested,
	// declined, void, or expired (open past expires_at).
	Status string `json:"status"`
	// Reason says why it is void or failed, or, while sending, why the
	// sequence is holding the email (outside_sending_window,
	// no_mailbox_allocated, mailbox_daily_limit...).
	Reason        string     `json:"reason"`
	SequenceID    *string    `json:"sequence_id"`
	MailboxID     *string    `json:"mailbox_id"`
	SuggestedBody string     `json:"suggested_body"`
	ExpertNote    string     `json:"expert_note"`
	ExpiresAt     time.Time  `json:"expires_at"`
	SentAt        *time.Time `json:"sent_at"`
	OpenedAt      *time.Time `json:"opened_at"`
	DecidedAt     *time.Time `json:"decided_at"`
	CreatedAt     time.Time  `json:"created_at"`
	// URL is the link, only in the answer that made a link request.
	URL string `json:"url,omitempty"`
}

const quoteApprovalCols = `a.id::text, a.quote_id::text, a.expert_id::text, a.body, a.delivery,
	CASE WHEN a.status IN ('sending', 'open') AND a.expires_at <= now() THEN 'expired' ELSE a.status END,
	CASE WHEN a.status = 'sending' THEN COALESCE(trim(split_part(e.status_reason, ':', 1)), '') ELSE a.reason END,
	a.sequence_id::text, a.mailbox_id::text, a.suggested_body, a.expert_note, a.expires_at, a.sent_at, a.opened_at, a.decided_at, a.created_at`

const quoteApprovalFrom = ` FROM story_quote_approvals a LEFT JOIN enrollments e ON e.id = a.enrollment_id`

func scanQuoteApproval(row pgx.Row) (quoteApprovalView, error) {
	var v quoteApprovalView
	err := row.Scan(&v.ID, &v.QuoteID, &v.ExpertID, &v.Body, &v.Delivery, &v.Status, &v.Reason, &v.SequenceID, &v.MailboxID,
		&v.SuggestedBody, &v.ExpertNote, &v.ExpiresAt, &v.SentAt, &v.OpenedAt, &v.DecidedAt, &v.CreatedAt)
	return v, err
}

// latestQuoteApprovals is each quote's latest request, for one story.
func (s *Server) latestQuoteApprovals(ctx context.Context, ws, story string) (map[string]quoteApprovalView, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT ON (a.quote_id) `+quoteApprovalCols+quoteApprovalFrom+`
		WHERE a.workspace_id = $1 AND a.story_id = $2 ORDER BY a.quote_id, a.created_at DESC, a.id`, ws, story)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (quoteApprovalView, error) { return scanQuoteApproval(row) })
	if err != nil {
		return nil, err
	}
	out := make(map[string]quoteApprovalView, len(list))
	for _, v := range list {
		out[v.QuoteID] = v
	}
	return out, nil
}

// quoteApprovalVoidReason says why a change to a quote voids its open
// requests, or "" when it doesn't.
func quoteApprovalVoidReason(oldBody, newBody, status string) string {
	switch {
	case oldBody != newBody:
		return "words_changed"
	case status == "withdrawn":
		return "withdrawn"
	case status == "approved":
		return "approved_by_person"
	}
	return ""
}

// voidQuoteApprovals voids a quote's open requests, in tx, and says how
// many there were. Their links stop working at once, an email still
// waiting in its sequence is stopped, and the link leaves the expert's
// lead.
func voidQuoteApprovals(ctx context.Context, tx pgx.Tx, ws, quote, reason string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `
		WITH v AS (
			UPDATE story_quote_approvals SET status = 'void', reason = $3, token_hash = NULL, updated_at = now()
			WHERE workspace_id = $1 AND quote_id = $2 AND status IN ('sending', 'open')
			RETURNING enrollment_id, lead_id
		), stopped AS (
			UPDATE enrollments SET status = 'stopped', status_reason = 'quote_approval_void', next_send_at = NULL, updated_at = now()
			WHERE workspace_id = $1 AND id IN (SELECT enrollment_id FROM v) AND status = 'active'
		), cleared AS (
			UPDATE leads SET custom = custom - $4::text, updated_at = now() WHERE workspace_id = $1 AND id IN (SELECT lead_id FROM v)
		)
		SELECT count(*) FROM v`, ws, quote, reason, quoteApprovalLinkVar).Scan(&n)
	return n, err
}

func (s *Server) quoteApprovalURL(secret string) string {
	return strings.TrimRight(s.publicURL, "/") + "/quote-approval/" + secret
}

// quoteFor finds the story's quote named in the path, or answers 404.
func quoteFor(w http.ResponseWriter, r *http.Request, v *storyView) (storyQuoteView, bool) {
	id := r.PathValue("quote")
	i := slices.IndexFunc(*v.Quotes, func(q storyQuoteView) bool { return q.ID == id })
	if !validUUID(id) || i < 0 {
		writeError(w, r, http.StatusNotFound, "not_found", "quote not found")
		return storyQuoteView{}, false
	}
	return (*v.Quotes)[i], true
}

func (s *Server) listQuoteApprovals(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.storyFor(w, r, a)
	if !ok {
		return
	}
	q, ok := quoteFor(w, r, v)
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT `+quoteApprovalCols+quoteApprovalFrom+`
		WHERE a.workspace_id = $1 AND a.story_id = $2 AND a.quote_id = $3 ORDER BY a.created_at DESC, a.id LIMIT 100`, a.WorkspaceID, v.ID, q.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (quoteApprovalView, error) { return scanQuoteApproval(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []quoteApprovalView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// approvalSequence checks the sequence an approval email goes through, in
// tx: one of the workspace's own (not a sequencer's campaign), active,
// the story's brand or shared, one step that carries
// {{custom.approval_link}}, without click tracking (so the link reaches
// the expert as it is), and with at least one mailbox allocated to it.
func approvalSequence(ctx context.Context, tx pgx.Tx, ws, id, brand string) error {
	var status string
	var seqBrand *string
	var linked, track, link, mailbox bool
	var steps int
	err := tx.QueryRow(ctx, `SELECT s.status, s.brand_id::text, s.track_clicks, EXISTS (SELECT 1 FROM campaign_links l WHERE l.sequence_id = s.id),
			(SELECT count(*) FROM sequence_steps t WHERE t.sequence_id = s.id),
			EXISTS (SELECT 1 FROM sequence_steps t WHERE t.sequence_id = s.id AND t.body ~* '\{\{\s*(custom\.)?approval_link\s*\}\}'),
			EXISTS (SELECT 1 FROM mailbox_assignments ma JOIN mailboxes m ON m.id = ma.mailbox_id
				WHERE ma.sequence_id = s.id AND ma.state = 'active' AND ma.link_id IS NULL AND m.status = 'active')
		FROM sequences s WHERE s.id = $1 AND s.workspace_id = $2`, id, ws).Scan(&status, &seqBrand, &track, &linked, &steps, &link, &mailbox)
	bad := func(msg string) error { return errPitch{http.StatusBadRequest, "invalid_request", msg} }
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return bad("sequence_id: sequence not found")
	case err != nil:
		return err
	case linked:
		return bad("the sequence runs in a connected sequencer; ask through one of Outflare's own sequences")
	case seqBrand != nil && *seqBrand != brand:
		return bad("the sequence belongs to another brand")
	case steps != 1:
		return bad("an approval email is one email: the sequence needs exactly 1 step")
	case !link:
		return bad("the sequence's step must carry the link: put {{custom.approval_link}} in its body")
	case track:
		return bad("turn off click tracking on the sequence, so the approval link reaches the expert as it is")
	case status != "active":
		return errPitch{http.StatusConflict, "conflict", "the sequence is " + status + "; start it, or copy the link and send it yourself"}
	case !mailbox:
		return errPitch{http.StatusConflict, "blocked", policy.NoMailbox + ": no mailbox sends for this sequence; allocate the brand's mailboxes to it under Outreach, or copy the link and send it yourself"}
	}
	return nil
}

// enrollForApproval makes the expert a lead carrying the link and
// enrolls them in the sequence, in tx, and returns the lead and
// enrollment. An enrollment the expert already finished in that sequence
// (an earlier ask) starts over as a new first email; one still waiting
// to send is refused, so one link never replaces another in flight.
func enrollForApproval(ctx context.Context, tx pgx.Tx, ws, brand, sequence, email, name string, custom map[string]string) (lead, enrollment string, err error) {
	first, last := firstLast(name)
	// The expert is a lead of the story's brand, like a pitched journalist.
	res, err := leads.ImportIn(ctx, tx, ws, brand, "digital_pr", nil, []leads.Lead{{Email: email, FirstName: first, LastName: last, Custom: custom}})
	if err != nil {
		return "", "", err
	}
	if res.OtherBrand > 0 {
		return "", "", errPitch{http.StatusConflict, "conflict", "the expert's address is a lead of another brand; until brands are fully separated an address is a lead in one brand at a time, so copy the link instead"}
	}
	if err := tx.QueryRow(ctx, `SELECT id::text FROM leads WHERE workspace_id = $1 AND brand_id = $2 AND lower(email) = lower($3)`, ws, brand, email).Scan(&lead); err != nil {
		return "", "", err
	}
	var status string
	err = tx.QueryRow(ctx, `SELECT id::text, status FROM enrollments WHERE workspace_id = $1 AND sequence_id = $2 AND lead_id = $3 FOR UPDATE`,
		ws, sequence, lead).Scan(&enrollment, &status)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		err = tx.QueryRow(ctx, `INSERT INTO enrollments (workspace_id, sequence_id, lead_id, next_send_at) VALUES ($1, $2, $3, now()) RETURNING id::text`,
			ws, sequence, lead).Scan(&enrollment)
		return lead, enrollment, err
	case err != nil:
		return "", "", err
	case status == "active":
		return "", "", errPitch{http.StatusConflict, "conflict", "the expert is already waiting for an email from this sequence; wait until it goes, or copy the link"}
	}
	// Start over: the earlier email stays in the history, apart from this
	// enrollment, so the new one is a first email again from a mailbox
	// allocated to the sequence now.
	if _, err := tx.Exec(ctx, `UPDATE messages SET enrollment_id = NULL WHERE enrollment_id = $1`, enrollment); err != nil {
		return "", "", err
	}
	_, err = tx.Exec(ctx, `UPDATE enrollments SET status = 'active', status_reason = NULL, next_step = 0, next_send_at = now(), mailbox_id = NULL,
		thread_subject = NULL, last_message_id = NULL, references_ids = '', handoff = false, updated_at = now() WHERE id = $1`, enrollment)
	return lead, enrollment, err
}

func (s *Server) requestQuoteApproval(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Delivery   string `json:"delivery"`
		SequenceID string `json:"sequence_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	switch {
	case req.Delivery != "email" && req.Delivery != "link":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "delivery is email (through one of the brand's outreach sequences) or link (you copy the link and send it)")
		return
	case req.Delivery == "email" && !validUUID(req.SequenceID):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "sequence_id: the outreach sequence that emails the expert, with {{custom.approval_link}} in its one step")
		return
	case req.Delivery == "link" && req.SequenceID != "":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "sequence_id is only for delivery email")
		return
	}
	v, ok := s.storyWriter(w, r, a)
	if !ok {
		return
	}
	q, ok := quoteFor(w, r, v)
	if !ok {
		return
	}
	if v.Status == "archived" {
		writeError(w, r, http.StatusConflict, "conflict", "the story is archived; move it back to draft first")
		return
	}
	if q.Status != "draft" {
		writeError(w, r, http.StatusConflict, "conflict", "only a draft quote can go to its expert; this one is "+q.Status)
		return
	}
	ctx := r.Context()
	expert, err := s.loadPRExpert(ctx, a.WorkspaceID, q.ExpertID)
	if err != nil || expert == nil {
		s.internal(w, r, errors.Join(err, errNotFound))
		return
	}
	if req.Delivery == "email" && expert.Email == nil {
		writeError(w, r, http.StatusConflict, "conflict", "the expert's record has no email address: add one to their record, or copy the link and send it yourself")
		return
	}
	// A copied link is answered once and never kept, so a retry with the
	// same Idempotency-Key runs again: a new link, voiding this one.
	if req.Delivery == "link" {
		noIdempotentReplay(r)
	}
	secret, hash := auth.NewToken(auth.PrefixQuoteApproval)
	status := "open"
	if req.Delivery == "email" {
		status = "sending"
	}
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// One request at a time: asking again replaces the last one. Asking
		// again is also how a person takes the expert's suggested words.
		replaced, err := voidQuoteApprovals(ctx, tx, a.WorkspaceID, q.ID, "replaced")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE story_quotes SET suggested_at = NULL WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, q.ID); err != nil {
			return err
		}
		expires := time.Now().Add(quoteApprovalTTL)
		var seq, lead, enrollment *string
		if req.Delivery == "email" {
			if err := approvalSequence(ctx, tx, a.WorkspaceID, req.SequenceID, v.BrandID); err != nil {
				return err
			}
			var busy bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM story_quote_approvals a JOIN leads l ON l.id = a.lead_id
				WHERE a.workspace_id = $1 AND a.status = 'sending' AND lower(l.email) = lower($2))`, a.WorkspaceID, *expert.Email).Scan(&busy); err != nil {
				return err
			}
			if busy {
				return errPitch{http.StatusConflict, "conflict", "another quote's email to this expert hasn't gone yet; wait until it does, or copy the link"}
			}
			brandName := ""
			if err := tx.QueryRow(ctx, `SELECT name FROM brands WHERE id = $1 AND workspace_id = $2`, v.BrandID, a.WorkspaceID).Scan(&brandName); err != nil {
				return err
			}
			l, en, err := enrollForApproval(ctx, tx, a.WorkspaceID, v.BrandID, req.SequenceID, *expert.Email, expert.Name, map[string]string{
				quoteApprovalLinkVar: s.quoteApprovalURL(secret), "quote": q.Body, "story_title": v.Title, "brand_name": brandName,
				"approval_expires_on": expires.UTC().Format("2 January 2006"),
			})
			if err != nil {
				return err
			}
			seq, lead, enrollment = &req.SequenceID, &l, &en
		}
		if err := tx.QueryRow(ctx, `INSERT INTO story_quote_approvals (workspace_id, brand_id, story_id, quote_id, expert_id, body, delivery,
				token_hash, status, expires_at, requested_by, sequence_id, lead_id, enrollment_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) RETURNING id::text`,
			a.WorkspaceID, v.BrandID, v.ID, q.ID, q.ExpertID, q.Body, req.Delivery, hash, status, expires, actorRef(a),
			seq, lead, enrollment).Scan(&id); err != nil {
			return err
		}
		data := map[string]any{"brand_id": v.BrandID, "story_id": v.ID, "quote_id": q.ID, "expert_id": q.ExpertID,
			"request_id": id, "delivery": req.Delivery, "replaced": replaced}
		if seq != nil {
			data["sequence_id"], data["enrollment_id"] = *seq, *enrollment
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "story_quote.approval_requested", "story", v.ID, data, data)
	})
	if err != nil {
		s.storyPitchError(w, r, err)
		return
	}
	out, err := scanQuoteApproval(s.pool.QueryRow(ctx, `SELECT `+quoteApprovalCols+quoteApprovalFrom+` WHERE a.workspace_id = $1 AND a.id = $2`, a.WorkspaceID, id))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if req.Delivery == "link" {
		out.URL = s.quoteApprovalURL(secret)
	}
	writeJSON(w, http.StatusCreated, out)
}

// quoteApprovalStepped follows an approval email through the sequence
// engine (sending.Engine.OnStep), in the engine's transaction: sent, the
// request is open; blocked, failed or bounced, it failed and the person
// who asked is alerted.
func (s *Server) quoteApprovalStepped(ctx context.Context, tx pgx.Tx, ev sending.StepEvent) error {
	if ev.Step != 0 {
		return nil
	}
	var id, brand, story, quote, expert, record string
	var requestedBy *string
	err := tx.QueryRow(ctx, `SELECT a.id::text, a.brand_id::text, a.story_id::text, a.quote_id::text, a.expert_id::text, x.record_id::text, a.requested_by::text
		FROM story_quote_approvals a JOIN pr_experts x ON x.id = a.expert_id
		WHERE a.workspace_id = $1 AND a.enrollment_id = $2 AND a.status = 'sending' FOR UPDATE OF a`, ev.WorkspaceID, ev.EnrollmentID).
		Scan(&id, &brand, &story, &quote, &expert, &record, &requestedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // not an approval email, or no longer waiting for it
	}
	if err != nil {
		return err
	}
	var mb, msg *string
	if ev.MailboxID != "" {
		mb = &ev.MailboxID
	}
	if ev.MessageID != "" {
		msg = &ev.MessageID
	}
	data := map[string]any{"brand_id": brand, "story_id": story, "quote_id": quote, "expert_id": expert, "request_id": id,
		"enrollment_id": ev.EnrollmentID, "via": "email"}
	if ev.Outcome == "sent" {
		if _, err := tx.Exec(ctx, `UPDATE story_quote_approvals SET status = 'open', reason = '', sent_at = now(), mailbox_id = $2, message_id = $3,
			updated_at = now() WHERE id = $1`, id, mb, msg); err != nil {
			return err
		}
		metrics.QuoteApprovalEmail("sent")
		data["mailbox_id"] = ev.MailboxID
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ev.WorkspaceID, ActorType: "system", Action: "story_quote.approval_sent",
			TargetType: "story", TargetID: story, BrandID: brand, Data: data, Props: data})
	}
	reason := cmp.Or(ev.Reason, ev.Outcome)
	if _, err := tx.Exec(ctx, `UPDATE story_quote_approvals SET status = 'failed', reason = left($2, 100), mailbox_id = $3, message_id = $4,
		token_hash = NULL, updated_at = now() WHERE id = $1`, id, reason, mb, msg); err != nil {
		return err
	}
	metrics.QuoteApprovalEmail(ev.Outcome)
	// A refusal by the send policy is a failure; a mailbox that couldn't
	// deliver is an error.
	code, outcome := "send_failed", track.Error
	if ev.Outcome == "blocked" {
		code, outcome = "blocked", track.Failure
	}
	props := map[string]any{"brand_id": brand, "story_id": story, "quote_id": quote, "request_id": id, "reason": reason, "outcome_kind": ev.Outcome}
	track.Fail(ctx, s.log, track.Event{Name: "story_quote.approval_send_failed", ActorType: "system", Surface: "job",
		WorkspaceID: ev.WorkspaceID, Outcome: outcome, Props: props}, code, fmt.Errorf("approval email %s: %s", ev.Outcome, reason))
	// Tell whoever asked, else the owners and admins.
	owner, err := userOfActor(ctx, tx, requestedBy)
	if err != nil {
		return err
	}
	_, err = alerts.Raise(ctx, tx, alerts.Alert{WorkspaceID: ev.WorkspaceID, BrandID: &brand, Kind: stories.AlertQuoteEmailFailed,
		DedupeKey: id, TargetType: "story_quote", TargetID: quote, RecordID: &record, OwnerID: owner,
		Data: map[string]any{"reason": reason, "story_id": story, "request_id": id}})
	return err
}

// userOfActor is the user behind an actor, or nil (an API token, a
// removed member).
func userOfActor(ctx context.Context, tx pgx.Tx, actor *string) (*string, error) {
	if actor == nil {
		return nil, nil
	}
	var user *string
	err := tx.QueryRow(ctx, `SELECT id::text FROM users WHERE actor_id = $1`, *actor).Scan(&user)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return user, err
}
