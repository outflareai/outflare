package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/workspace"
	"github.com/outflareai/outflare/engine/internal/crmmail"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/policy"
)

// Sending as a member (ADR 15): one way for every module to send an email
// from a person's own mailbox. The recipients pass the outbound policy
// check when it is asked for, the send runs in a job, and the sent email
// is kept on the record like captured mail. The job looks for the email's
// Message-ID in the person's Sent mail before sending again, so a retry
// never sends it twice. Mailboxes read through the company's Google
// Workspace connection send with its gmail.send delegation; a person's
// own Google sign-in gets sending with the shared sign-in (ADR 15, step 3).

const jobCRMMailSend = "crm_mail.send"

const gmailSendAPI = "https://gmail.googleapis.com/gmail/v1"

// mailSendContent is what is sent, sealed in crm_mail_sends.
type mailSendContent struct {
	From       string   `json:"from"`
	To         []string `json:"to"`
	Cc         []string `json:"cc"`
	Subject    string   `json:"subject"`
	Body       string   `json:"body"`
	InReplyTo  string   `json:"in_reply_to,omitempty"`
	References []string `json:"references,omitempty"`
}

type mailSendView struct {
	ID               string     `json:"id"`
	RecordID         *string    `json:"record_id"`
	Status           string     `json:"status"`
	ErrorCode        *string    `json:"error_code"`
	CRMMailMessageID *string    `json:"crm_mail_message_id"`
	CreatedAt        time.Time  `json:"created_at"`
	SentAt           *time.Time `json:"sent_at"`
}

func (s *Server) routesCRMMailSend(mux *http.ServeMux) {
	base := "/v1/brands/{brand}/crm/mail/sends"
	mux.HandleFunc("POST "+base, s.onBrand(auth.ScopeCustomersManage, s.sendCRMMail))
	mux.HandleFunc("GET "+base+"/{send}", s.onBrand(auth.ScopeCustomersRead, s.getCRMMailSend))
}

func mailSendSeal(id string) []byte { return []byte("crm_mail_send:" + id) }

func (s *Server) sendCRMMail(w http.ResponseWriter, r *http.Request) {
	if !s.crmMailPerson(w, r) {
		return
	}
	a := accessFrom(r.Context())
	ctx := r.Context()
	if s.box == nil {
		writeError(w, r, 503, "unavailable", "personal mail encryption is not configured")
		return
	}
	var in struct {
		RecordID  string   `json:"record_id"`
		To        []string `json:"to"`
		Cc        []string `json:"cc"`
		Subject   string   `json:"subject"`
		Body      string   `json:"body"`
		InReplyTo string   `json:"in_reply_to"`
	}
	if !decode(w, r, &in) {
		return
	}
	clean := func(list []string) ([]string, bool) {
		out := []string{}
		for _, v := range list {
			addr, err := mail.ParseAddress(strings.TrimSpace(v))
			if err != nil {
				return nil, false
			}
			e := strings.ToLower(addr.Address)
			if !strings.Contains(e, "@") {
				return nil, false
			}
			if !containsString(out, e) {
				out = append(out, e)
			}
		}
		return out, true
	}
	to, ok1 := clean(in.To)
	cc, ok2 := clean(in.Cc)
	in.Subject = strings.TrimSpace(in.Subject)
	switch {
	case !ok1 || !ok2:
		writeError(w, r, 400, "invalid_request", "to and cc must be email addresses")
		return
	case len(to) == 0 || len(to)+len(cc) > 20:
		writeError(w, r, 400, "invalid_request", "send to between 1 and 20 people (to and cc together)")
		return
	case in.Subject == "" || len(in.Subject) > 500 || strings.ContainsAny(in.Subject, "\r\n"):
		writeError(w, r, 400, "invalid_request", "subject is required, one line of at most 500 characters")
		return
	case strings.TrimSpace(in.Body) == "" || len(in.Body) > 200<<10:
		writeError(w, r, 400, "invalid_request", "body is required, at most 200 KB")
		return
	}
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.mailRecord(ctx, tx, a, in.RecordID); err != nil {
			return err
		}
		if ok, err := eligibleCRMMail(ctx, tx, a.WorkspaceID, a.Brand, a.UserID); err != nil {
			return err
		} else if !ok {
			return errDenied("your administrator must enable you for CRM email before you can send from Outflare")
		}
		var connected bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM crm_google_connections WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3 AND status='connected')`,
			a.WorkspaceID, a.Brand, a.UserID).Scan(&connected); err != nil {
			return err
		}
		if !connected {
			return errInvalid("connect your mailbox first: your admin reads it through Google Workspace, or you connect Google in CRM email settings")
		}
		// Every outbound email passes the policy check.
		decisions, err := policy.CheckRecipientsIn(ctx, tx, a.WorkspaceID, a.Brand, append(append([]string{}, to...), cc...))
		if err != nil {
			return err
		}
		for _, d := range decisions {
			if !d.Allowed {
				return recordConflict{code: "blocked", msg: d.Email + " can't be emailed: " + strings.Join(d.Reasons, ", ")}
			}
		}
		var from string
		if err := tx.QueryRow(ctx, `SELECT lower(email) FROM users WHERE id=$1`, a.UserID).Scan(&from); err != nil {
			return err
		}
		content := mailSendContent{From: from, To: to, Cc: cc, Subject: in.Subject, Body: in.Body}
		if in.InReplyTo != "" {
			// Reply in the thread of an email the sender may see.
			m, err := s.visibleCRMMail(ctx, a, in.InReplyTo)
			if err != nil {
				return err
			}
			if m.MessageID != "" {
				content.InReplyTo = m.MessageID
				content.References = append(append([]string{}, m.References...), m.MessageID)
			}
		}
		id = newID()
		_, domain, _ := strings.Cut(from, "@")
		messageID := newID() + "@" + domain
		raw, _ := json.Marshal(content)
		if _, err := tx.Exec(ctx, `INSERT INTO crm_mail_sends (id, workspace_id, brand_id, owner_id, record_id, content_sealed, message_id) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			id, a.WorkspaceID, a.Brand, a.UserID, in.RecordID, s.box.Seal(raw, mailSendSeal(id)), messageID); err != nil {
			return err
		}
		if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobCRMMailSend, WorkspaceID: &a.WorkspaceID, Payload: map[string]string{"send_id": id},
			IdempotencyKey: "crm_mail.send:" + id, MaxAttempts: 4}); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "crm_mail.send_queued", "crm_mail_send", id,
			map[string]any{"brand_id": a.Brand, "record_id": in.RecordID, "recipients": len(to) + len(cc), "reply": content.InReplyTo != ""})
	})
	if s.recordError(w, r, err) {
		return
	}
	v, err := s.loadMailSend(ctx, a, id)
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, v)
}

func (s *Server) getCRMMailSend(w http.ResponseWriter, r *http.Request) {
	if !s.crmMailPerson(w, r) {
		return
	}
	a := accessFrom(r.Context())
	v, err := s.loadMailSend(r.Context(), a, r.PathValue("send"))
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, 200, v)
}

// loadMailSend is one of the person's own sends.
func (s *Server) loadMailSend(ctx context.Context, a *access, id string) (mailSendView, error) {
	var v mailSendView
	if !validUUID(id) {
		return v, errNotFound
	}
	err := s.pool.QueryRow(ctx, `SELECT id::text, record_id::text, status, error_code, crm_mail_message_id::text, created_at, sent_at FROM crm_mail_sends
		WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3 AND id=$4`, a.WorkspaceID, a.Brand, a.UserID, id).
		Scan(&v.ID, &v.RecordID, &v.Status, &v.ErrorCode, &v.CRMMailMessageID, &v.CreatedAt, &v.SentAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	return v, err
}

// visibleCRMMail is a captured email the person may see, opened.
func (s *Server) visibleCRMMail(ctx context.Context, a *access, id string) (crmmail.Message, error) {
	var m crmmail.Message
	if !validUUID(id) {
		return m, errInvalid("in_reply_to must be an email's id")
	}
	var owner, rec, privacy string
	var sealed []byte
	err := s.pool.QueryRow(ctx, `SELECT owner_id::text, record_id::text, content_sealed, privacy FROM crm_mail_messages WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`,
		a.WorkspaceID, a.Brand, id).Scan(&owner, &rec, &sealed, &privacy)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, errInvalid("in_reply_to must be an email you can see")
	}
	if err != nil {
		return m, err
	}
	if owner != a.UserID {
		cfg, err := mailConfig(ctx, s.pool, "crm_mail_sharing", a.WorkspaceID, a.Brand, owner, rec)
		if err != nil {
			return m, err
		}
		raw, _ := json.Marshal(cfg)
		var share crmmail.Sharing
		if err := json.Unmarshal(raw, &share); err != nil {
			return m, err
		}
		chosen, _ := cfg["version"].(int)
		if crmmail.Visibility(owner, a.UserID, share) != "full" || (chosen == 0 && privacy != "work") {
			return m, errInvalid("in_reply_to must be an email you can see")
		}
	}
	plain, err := s.box.Open(sealed, mailAAD(id))
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(plain, &m)
}

func (s *Server) crmMailSendJob(ctx context.Context, job jobs.Job) error {
	var payload struct {
		ID string `json:"send_id"`
	}
	if json.Unmarshal(job.Payload, &payload) != nil || job.WorkspaceID == nil || !validUUID(payload.ID) {
		return jobs.ErrPermanent
	}
	ws := *job.WorkspaceID
	var brand, owner, status, messageID string
	var rec *string
	var sealed []byte
	err := s.pool.QueryRow(ctx, `SELECT brand_id::text, owner_id::text, record_id::text, status, message_id, content_sealed FROM crm_mail_sends WHERE workspace_id=$1 AND id=$2`,
		ws, payload.ID).Scan(&brand, &owner, &rec, &status, &messageID, &sealed)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (status == "sent" || status == "failed")) {
		return nil
	}
	if err != nil {
		return err
	}
	fail := func(code string) error {
		metrics.CRMMailSend("failed")
		return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE crm_mail_sends SET status='failed', error_code=$3, updated_at=now() WHERE workspace_id=$1 AND id=$2`, ws, payload.ID, code); err != nil {
				return err
			}
			return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "crm_mail.send_failed", TargetType: "crm_mail_send", TargetID: payload.ID,
				Data: map[string]any{"brand_id": brand, "error_code": code}})
		})
	}
	plain, err := s.box.Open(sealed, mailSendSeal(payload.ID))
	if err != nil {
		return err
	}
	var content mailSendContent
	if err := json.Unmarshal(plain, &content); err != nil {
		return jobs.ErrPermanent
	}
	if ok, err := eligibleCRMMail(ctx, s.pool, ws, brand, owner); err != nil {
		return err
	} else if !ok {
		return fail("not_eligible")
	}
	var connID string
	var integration *string
	var credsSealed []byte
	err = s.pool.QueryRow(ctx, `SELECT id::text, workspace_integration_id::text, credentials_sealed FROM crm_google_connections WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3 AND status='connected'`,
		ws, brand, owner).Scan(&connID, &integration, &credsSealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail("mailbox_not_connected")
	}
	if err != nil {
		return err
	}
	if integration == nil {
		// A person's own Google sign-in reads only; sending comes with the
		// shared sign-in (ADR 15, step 3).
		return fail("send_not_allowed")
	}
	o, err := s.integrations.Open(ctx, s.pool, ws, *integration)
	if err != nil {
		return err
	}
	sendToken, err := workspace.SendToken(ctx, o.Client, o.Creds, content.From)
	if err != nil {
		if k := connectors.KindOf(err); k == connectors.ErrAuth || k == connectors.ErrInvalid {
			return fail("send_not_allowed")
		}
		return err
	}
	readToken, err := workspace.Token(ctx, o.Client, o.Creds, content.From)
	if err != nil {
		return err
	}
	base := o.Client.Base("gmail", gmailSendAPI)
	// A send started before (a retry): look for it in Sent before sending again.
	var providerID string
	if status == "sending" {
		req, _ := http.NewRequest(http.MethodGet, base+"/users/me/messages?"+url.Values{"q": {"rfc822msgid:" + messageID}}.Encode(), nil)
		req.Header.Set("Authorization", "Bearer "+readToken)
		var found struct {
			Messages []struct {
				ID string `json:"id"`
			} `json:"messages"`
		}
		if _, err := connectors.DoJSON(ctx, o.Client, "Gmail", req, &found); err != nil {
			return err
		}
		if len(found.Messages) > 0 {
			providerID = found.Messages[0].ID
			metrics.CRMMailSend("deduplicated")
		}
	}
	if providerID == "" {
		if _, err := s.pool.Exec(ctx, `UPDATE crm_mail_sends SET status='sending', updated_at=now() WHERE workspace_id=$1 AND id=$2`, ws, payload.ID); err != nil {
			return err
		}
		raw := buildMailSend(content, messageID, time.Now())
		body, _ := json.Marshal(map[string]string{"raw": base64.RawURLEncoding.EncodeToString(raw)})
		req, _ := http.NewRequest(http.MethodPost, base+"/users/me/messages/send", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+sendToken)
		req.Header.Set("Content-Type", "application/json")
		var sent struct {
			ID string `json:"id"`
		}
		code, err := connectors.DoJSON(ctx, o.Client, "Gmail", req, &sent)
		if err != nil {
			if code == http.StatusBadRequest || code == http.StatusForbidden || code == http.StatusUnauthorized {
				return fail("send_refused")
			}
			// Unknown whether Gmail sent it: the next try looks in Sent first.
			return err
		}
		providerID = sent.ID
	}
	// Keep the sent email on the record, as captured mail is.
	var credentials crmGoogleCredentials
	if raw, err := s.box.Open(credsSealed, []byte(connID)); err == nil {
		_ = json.Unmarshal(raw, &credentials)
	}
	digest := sha256.Sum256([]byte("google:" + credentials.Grant.Subject + ":" + providerID))
	sourceHash := "google:" + hex.EncodeToString(digest[:])
	participants := append(append([]string{content.From}, content.To...), content.Cc...)
	m := crmmail.Message{Subject: content.Subject, Body: content.Body, Participants: participants, VisibleParticipants: participants, OwnerEmail: content.From,
		OccurredAt: time.Now().UTC(), MessageID: messageID, References: content.References}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var stored *string
		if rec != nil {
			id := newID()
			data, _ := json.Marshal(m)
			var got string
			err := tx.QueryRow(ctx, `INSERT INTO crm_mail_messages (id, workspace_id, brand_id, owner_id, record_id, content_sealed, source_hash, occurred_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT (brand_id, owner_id, source_hash) DO UPDATE SET source_hash = EXCLUDED.source_hash RETURNING id::text`,
				id, ws, brand, owner, *rec, s.box.Seal(data, mailAAD(id)), sourceHash, m.OccurredAt).Scan(&got)
			if err != nil {
				return err
			}
			stored = &got
			if err := queueMailPrivacyIfShared(ctx, tx, ws, brand); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE crm_mail_sends SET status='sent', provider_id=$3, crm_mail_message_id=$4, sent_at=now(), error_code=NULL, updated_at=now() WHERE workspace_id=$1 AND id=$2`,
			ws, payload.ID, providerID, stored); err != nil {
			return err
		}
		metrics.CRMMailSend("sent")
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "crm_mail.sent", TargetType: "crm_mail_send", TargetID: payload.ID,
			Data: map[string]any{"brand_id": brand, "recipients": len(content.To) + len(content.Cc)}})
	})
}

// buildMailSend is the email as RFC 5322 text.
func buildMailSend(c mailSendContent, messageID string, at time.Time) []byte {
	var b strings.Builder
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", c.From)
	h("To", strings.Join(c.To, ", "))
	if len(c.Cc) > 0 {
		h("Cc", strings.Join(c.Cc, ", "))
	}
	h("Subject", mime.QEncoding.Encode("utf-8", c.Subject))
	h("Date", at.UTC().Format(time.RFC1123Z))
	h("Message-ID", "<"+messageID+">")
	if c.InReplyTo != "" {
		h("In-Reply-To", "<"+c.InReplyTo+">")
		var refs []string
		for _, r := range c.References {
			refs = append(refs, "<"+strings.Trim(r, "<>")+">")
		}
		h("References", strings.Join(refs, " "))
	}
	h("MIME-Version", "1.0")
	h("Content-Type", "text/plain; charset=utf-8")
	h("Content-Transfer-Encoding", "base64")
	b.WriteString("\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(c.Body))
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return []byte(b.String())
}
