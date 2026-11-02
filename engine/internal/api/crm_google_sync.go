package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"

	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/crmmail"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/oauth"
)

const jobCRMGoogleSync = "crm_mail.google_sync"

type crmGoogleCredentials struct {
	Grant      oauth.PersonalGoogleGrant `json:"grant"`
	Query      string                    `json:"query,omitempty"`
	Page       string                    `json:"page,omitempty"`
	Started    time.Time                 `json:"started,omitempty"`
	Generation int                       `json:"generation"`
}

func (s *Server) enqueueCRMGoogle(ctx context.Context, q jobs.Querier, ws, id string, at time.Time, force ...string) error {
	key := id + ":" + strconv.FormatInt(at.Unix()/300, 10)
	if len(force) > 0 {
		key += ":" + force[0]
	}
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: jobCRMGoogleSync, WorkspaceID: &ws, Payload: map[string]string{"connection_id": id}, RunAt: at, MaxAttempts: 3, IdempotencyKey: key})
	return err
}
func (s *Server) queueCRMGoogleSync(w http.ResponseWriter, r *http.Request) {
	if !s.crmMailPerson(w, r) {
		return
	}
	a := accessFrom(r.Context())
	ok, err := eligibleCRMMail(r.Context(), s.pool, a.WorkspaceID, a.Brand, a.UserID)
	if s.allocError(w, r, err) {
		return
	}
	if !ok {
		writeError(w, r, 403, "forbidden", "you are not enabled for personal Google capture")
		return
	}
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(r.Context(), `SELECT id::text FROM crm_google_connections WHERE workspace_id=$1 AND brand_id=$2 AND owner_id=$3 AND status='connected'`, a.WorkspaceID, a.Brand, a.UserID).Scan(&id); errors.Is(err, pgx.ErrNoRows) {
			return errInvalid("connect your Google account before syncing")
		} else if err != nil {
			return err
		}
		if err := s.enqueueCRMGoogle(r.Context(), tx, a.WorkspaceID, id, time.Now(), newID()); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "crm_mail.sync_requested", "crm_mail_connection", id, map[string]any{"brand_id": a.Brand})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, map[string]any{"queued": true})
}

var gmailMessageID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func (s *Server) crmGoogleSyncJob(ctx context.Context, job jobs.Job) (runErr error) {
	var payload struct {
		ID string `json:"connection_id"`
	}
	if json.Unmarshal(job.Payload, &payload) != nil || job.WorkspaceID == nil || !validUUID(payload.ID) {
		return jobs.ErrPermanent
	}
	if !s.crmGoogleConfigured() {
		return errors.New("personal Google OAuth is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ws := *job.WorkspaceID
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var locked bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, "crm-google-sync:"+payload.ID).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return nil
	}
	defer func() {
		unlock, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(unlock, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, "crm-google-sync:"+payload.ID)
	}()
	var brand, owner, status string
	var sealed []byte
	var generation int
	var last *time.Time
	err = s.pool.QueryRow(ctx, `SELECT brand_id::text,owner_id::text,status,credentials_sealed,version,last_synced_at FROM crm_google_connections WHERE workspace_id=$1 AND id=$2`, ws, payload.ID).Scan(&brand, &owner, &status, &sealed, &generation, &last)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status != "connected" {
		return nil
	}
	allowed, err := eligibleCRMMail(ctx, s.pool, ws, brand, owner)
	if err != nil {
		return err
	}
	if !allowed {
		return nil
	}
	// Every job uses the existing queue metrics, lag/retry/dead-job alerts.
	// Schedule one following poll; duplicate requests share the next time bucket.
	defer func() {
		next, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := s.enqueueCRMGoogle(next, s.pool, ws, payload.ID, time.Now().Add(5*time.Minute)); err != nil && runErr == nil {
			runErr = err
		}
	}()
	defer func() {
		if runErr != nil {
			update, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := s.crmGoogleFailed(update, ws, payload.ID, "sync_failed", false); err != nil {
				runErr = errors.Join(runErr, err)
			}
		}
	}()
	a := &access{WorkspaceID: ws, Brand: brand}
	a.UserID = owner
	rows, err := s.pool.Query(ctx, `SELECT c.record_id::text FROM crm_mail_capture_targets c JOIN records r ON r.id=c.record_id AND r.workspace_id=c.workspace_id AND r.brand_id=c.brand_id WHERE c.workspace_id=$1 AND c.brand_id=$2 AND c.owner_id=$3 AND c.enabled AND r.deleted_at IS NULL AND r.merged_into IS NULL AND r.archived_at IS NULL ORDER BY c.record_id LIMIT 100`, ws, brand, owner)
	if err != nil {
		return err
	}
	targets, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}
	contexts := map[string]mailContext{}
	terms := []string{}
	for _, rec := range targets {
		c, err := s.crmMailContext(ctx, a, owner, rec)
		if err != nil {
			return err
		}
		contexts[rec] = c
		for _, value := range append(append([]string{}, c.emails...), c.domains...) {
			value = strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
			for _, header := range []string{"from", "to", "cc", "bcc"} {
				terms = append(terms, header+`:"`+value+`"`)
			}
		}
	}
	if len(terms) == 0 {
		return nil
	}
	raw, err := s.box.Open(sealed, []byte(payload.ID))
	if err != nil {
		return err
	}
	var credentials crmGoogleCredentials
	if err = json.Unmarshal(raw, &credentials); err != nil {
		return err
	}
	var currentEmail string
	if err := s.pool.QueryRow(ctx, `SELECT lower(email) FROM users WHERE id=$1`, owner).Scan(&currentEmail); err != nil {
		return err
	}
	if credentials.Grant.Email != currentEmail {
		return s.crmGoogleFailed(ctx, ws, payload.ID, "account_changed", true)
	}
	if credentials.Generation != generation {
		credentials.Query = ""
		credentials.Page = ""
		credentials.Generation = generation
	}
	if credentials.Query == "" {
		since := time.Now().Add(-7 * 24 * time.Hour)
		if last != nil && last.Add(-time.Hour).After(since) {
			since = last.Add(-time.Hour)
		}
		credentials.Query = "after:" + strconv.FormatInt(since.Unix(), 10) + " -in:spam -in:trash -in:drafts -in:scheduled {" + strings.Join(terms, " ") + "}"
		credentials.Started = time.Now()
	}
	if len(credentials.Query) > 16000 {
		return s.crmGoogleFailed(ctx, ws, payload.ID, "capture_too_broad", false)
	}
	token, rotated, err := s.oauth.RefreshPersonalGoogle(ctx, credentials.Grant.RefreshToken)
	if err != nil {
		var denied *oauth.Error
		permanent := (errors.As(err, &denied) && slices.Contains([]string{"invalid_grant", "unauthorized_client", "invalid_client", "access_denied", "consent_required", "interaction_required"}, denied.Code)) || errors.Is(err, oauth.ErrPersonalGoogleScope)
		_ = s.crmGoogleFailed(ctx, ws, payload.ID, "google_connection_failed", permanent)
		if permanent {
			return nil
		}
		return errors.New("personal Google connection could not refresh")
	}
	if rotated != "" {
		credentials.Grant.RefreshToken = rotated
	}
	// Save rotating credentials before using them; concurrent disconnect/reconnect
	// or target changes prevent stale workers from committing any correspondence.
	if ok, err := s.saveCRMGoogleProgress(ctx, ws, payload.ID, generation, credentials, nil); err != nil {
		return err
	} else if !ok {
		return nil
	}
	stored := 0
	for page := 0; page < 5; page++ {
		var result struct {
			Messages []struct {
				ID string `json:"id"`
			} `json:"messages"`
			Next string `json:"nextPageToken"`
		}
		err = s.oauth.ReadPersonalGoogle(ctx, token, "messages", url.Values{"q": {credentials.Query}, "maxResults": {"50"}, "pageToken": {credentials.Page}}, &result)
		if err != nil {
			credentials.Query = ""
			credentials.Page = ""
			if _, saveErr := s.saveCRMGoogleProgress(ctx, ws, payload.ID, generation, credentials, nil); saveErr != nil {
				return saveErr
			}
			_ = s.crmGoogleFailed(ctx, ws, payload.ID, "google_read_failed", false)
			return errors.New("personal Google mail could not be listed")
		}
		for _, item := range result.Messages {
			if !gmailMessageID.MatchString(item.ID) {
				continue
			}
			sourceDigest := sha256.Sum256([]byte("google:" + credentials.Grant.Subject + ":" + item.ID))
			sourceHash := "google:" + hex.EncodeToString(sourceDigest[:])
			var active, known bool
			if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM crm_google_connections WHERE workspace_id=$1 AND id=$2 AND version=$3 AND status='connected'), (EXISTS(SELECT 1 FROM crm_mail_messages WHERE workspace_id=$1 AND brand_id=$4 AND owner_id=$5 AND source_hash=$6) OR EXISTS(SELECT 1 FROM crm_mail_suppressions WHERE workspace_id=$1 AND brand_id=$4 AND owner_id=$5 AND source_hash=$6))`, ws, payload.ID, generation, brand, owner, sourceHash).Scan(&active, &known); err != nil {
				return err
			}
			if !active {
				return nil
			}
			if known {
				continue
			}
			// Only headers are fetched until the full participant set passes policy.
			var metadata struct {
				Labels  []string `json:"labelIds"`
				Payload struct {
					Headers []struct{ Name, Value string } `json:"headers"`
				} `json:"payload"`
			}
			if err = s.oauth.ReadPersonalGoogle(ctx, token, "messages/"+url.PathEscape(item.ID), url.Values{"format": {"metadata"}, "fields": {"labelIds,payload(headers)"}, "metadataHeaders": {"From", "To", "Cc", "Bcc", "Sender", "Reply-To"}}, &metadata); err != nil {
				return errors.New("personal Google message headers could not be read")
			}
			if privateGoogleLabels(metadata.Labels) {
				continue
			}
			var headers strings.Builder
			invalidHeaders := false
			for _, h := range metadata.Payload.Headers {
				if strings.ContainsAny(h.Name+h.Value, "\r\n") {
					invalidHeaders = true
					break
				}
				switch strings.ToLower(h.Name) {
				case "from", "to", "cc", "bcc", "sender", "reply-to":
					headers.WriteString(h.Name + ": " + h.Value + "\r\n")
				}
			}
			if invalidHeaders {
				continue
			}
			headers.WriteString("\r\n")
			m, err := crmmail.Parse(headers.String(), credentials.Grant.Email)
			if err != nil {
				continue
			}
			rec := ""
			ambiguous := false
			for _, id := range targets {
				if contexts[id].eligible(m) {
					if rec != "" {
						ambiguous = true
						break
					}
					rec = id
				}
			}
			if rec == "" || ambiguous {
				continue
			}
			var full gmailContent
			if err = s.oauth.ReadPersonalGoogle(ctx, token, "messages/"+url.PathEscape(item.ID), url.Values{"format": {"full"}, "fields": {"internalDate,labelIds,payload"}}, &full); err != nil {
				return errors.New("personal Google message could not be read")
			}
			if privateGoogleLabels(full.Labels) {
				continue
			}
			body, err := gmailTextMessage(full.Payload)
			if err != nil {
				continue
			}

			m, err = crmmail.Parse(body, credentials.Grant.Email)
			if err != nil || !contexts[rec].eligible(m) {
				continue
			}
			if millis, err := strconv.ParseInt(full.InternalDate, 10, 64); err == nil && millis > 0 {
				m.OccurredAt = time.UnixMilli(millis).UTC()
			}
			id := newID()
			data, _ := json.Marshal(m)
			cipher := s.box.Seal(data, mailAAD(id))
			err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
				// Re-evaluate membership, exclusions, active target and generation after
				// network calls, serialized against administrative rollout changes.
				if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "crm-rollout:"+brand); err != nil {
					return err
				}
				ok, err := eligibleCRMMail(ctx, tx, ws, brand, owner)
				if err != nil {
					return err
				}
				if !ok {
					return nil
				}
				if err := s.mailRecord(ctx, tx, a, rec); err != nil {
					if errors.Is(err, errNotFound) {
						return nil
					}
					return err
				}
				var active bool
				if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM crm_google_connections g JOIN crm_mail_capture_targets c ON c.workspace_id=g.workspace_id AND c.brand_id=g.brand_id AND c.owner_id=g.owner_id JOIN records r ON r.id=c.record_id AND r.workspace_id=c.workspace_id AND r.brand_id=c.brand_id WHERE g.workspace_id=$1 AND g.id=$2 AND g.version=$3 AND g.status='connected' AND c.record_id=$4 AND c.enabled AND r.deleted_at IS NULL AND r.merged_into IS NULL AND r.archived_at IS NULL)`, ws, payload.ID, generation, rec).Scan(&active); err != nil {
					return err
				}
				if !active {
					return nil
				}
				// Reload live policy, not just the earlier header-match snapshot.
				live, err := s.crmMailContextWith(ctx, tx, a, owner, rec)
				if err != nil {
					return err
				}
				if !live.eligible(m) {
					return nil
				}
				tag, err := tx.Exec(ctx, `INSERT INTO crm_mail_messages(id,workspace_id,brand_id,owner_id,record_id,content_sealed,source_hash,occurred_at)SELECT $1,$2,$3,$4,$5,$6,$7,$8 WHERE NOT EXISTS(SELECT 1 FROM crm_mail_suppressions WHERE workspace_id=$2 AND brand_id=$3 AND owner_id=$4 AND source_hash=$7) ON CONFLICT(brand_id,owner_id,source_hash) DO NOTHING`, id, ws, brand, owner, rec, cipher, sourceHash, m.OccurredAt)
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 0 {
					return nil
				}
				stored++
				return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "crm_mail.captured", TargetType: "crm_mail_message", TargetID: id, Data: map[string]any{"brand_id": brand}})
			})
			if err != nil {
				return err
			}
		}
		credentials.Page = result.Next
		var finished *time.Time
		if result.Next == "" {
			at := credentials.Started
			finished = &at
			credentials.Query = ""
		}
		ok, err := s.saveCRMGoogleProgress(ctx, ws, payload.ID, generation, credentials, finished)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if finished != nil {
			return s.auditCRMGoogleSync(ctx, ws, brand, payload.ID, stored)
		}
	}
	// Large backfills continue from the sealed cursor rather than silently truncating.
	digest := sha256.Sum256([]byte(credentials.Query + credentials.Page))
	if err := s.enqueueCRMGoogle(ctx, s.pool, ws, payload.ID, time.Now(), "page:"+hex.EncodeToString(digest[:])); err != nil {
		return err
	}
	return s.auditCRMGoogleSync(ctx, ws, brand, payload.ID, stored)
}
func (s *Server) saveCRMGoogleProgress(ctx context.Context, ws, id string, generation int, c crmGoogleCredentials, finished *time.Time) (bool, error) {
	raw, _ := json.Marshal(c)
	sealed := s.box.Seal(raw, []byte(id))
	tag, err := s.pool.Exec(ctx, `UPDATE crm_google_connections SET credentials_sealed=$4,last_synced_at=COALESCE($5,last_synced_at),error_code=NULL,updated_at=now() WHERE workspace_id=$1 AND id=$2 AND version=$3 AND status='connected'`, ws, id, generation, sealed, finished)
	return tag.RowsAffected() > 0, err
}
func (s *Server) crmGoogleFailed(ctx context.Context, ws, id, code string, permanent bool) error {
	state := "connected"
	if permanent {
		state = "reconnect_required"
	}
	_, err := s.pool.Exec(ctx, `UPDATE crm_google_connections SET status=$3,error_code=$4,updated_at=now() WHERE workspace_id=$1 AND id=$2`, ws, id, state, code)
	return err
}
func (s *Server) auditCRMGoogleSync(ctx context.Context, ws, brand, id string, stored int) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "crm_mail.synced", TargetType: "crm_mail_connection", TargetID: id, Data: map[string]any{"brand_id": brand, "stored_count": stored}})
	})
}

type gmailHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type gmailPart struct {
	Mime     string        `json:"mimeType"`
	Filename string        `json:"filename"`
	Headers  []gmailHeader `json:"headers"`
	Body     struct {
		Data       string `json:"data"`
		Attachment string `json:"attachmentId"`
	} `json:"body"`
	Parts []gmailPart `json:"parts"`
}
type gmailContent struct {
	Payload      gmailPart `json:"payload"`
	Labels       []string  `json:"labelIds"`
	InternalDate string    `json:"internalDate"`
}

func privateGoogleLabels(labels []string) bool {
	for _, label := range []string{"DRAFT", "SCHEDULED", "SPAM", "TRASH"} {
		if slices.Contains(labels, label) {
			return true
		}
	}
	return false
}

// Pull only an inline text part from Gmail's parsed MIME tree. Attachment
// identifiers are never followed, so a large PDF does not require downloading
// or retaining the attachment just to capture the accompanying email text.
func gmailTextMessage(root gmailPart) (string, error) {
	var plain, html *gmailPart
	var visit func(*gmailPart, int)
	visit = func(p *gmailPart, depth int) {
		if depth > 20 || p.Filename != "" || p.Body.Attachment != "" {
			return
		}
		if p.Mime == "text/plain" && plain == nil {
			plain = p
		}
		if p.Mime == "text/html" && html == nil {
			html = p
		}
		for i := range p.Parts {
			visit(&p.Parts[i], depth+1)
		}
	}
	visit(&root, 0)
	selected := plain
	if selected == nil {
		selected = html
	}
	var text []byte
	contentType := "text/plain; charset=utf-8"
	if selected != nil {
		if len(selected.Body.Data) > 300000 {
			return "", errors.New("email text exceeds capture limit")
		}
		decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(selected.Body.Data, "="))
		if err != nil {
			return "", err
		}
		text = decoded
		contentType = selected.Mime + "; charset=utf-8"
		for _, h := range selected.Headers {
			if strings.EqualFold(h.Name, "Content-Type") {
				contentType = h.Value
			}
		}
	}
	if strings.ContainsAny(contentType, "\r\n") {
		return "", errors.New("invalid MIME header")
	}
	var out strings.Builder
	for _, h := range root.Headers {
		if strings.ContainsAny(h.Name+h.Value, "\r\n") {
			return "", errors.New("invalid email headers")
		}
		switch strings.ToLower(h.Name) {
		case "from", "to", "cc", "bcc", "sender", "reply-to", "date", "subject", "message-id":
			out.WriteString(h.Name + ": " + h.Value + "\r\n")
		}
	}
	out.WriteString("Content-Type: " + contentType + "\r\n\r\n")
	out.Write(text)
	return out.String(), nil
}
