// Package webhooks sends workspace events to the URLs people register, so
// their own systems and agents hear about replies, bounces and paused
// mailboxes without polling.
//
// Events come from the audit log: audit.Record calls Enqueue in the same
// transaction as the change, which writes one delivery per subscribed
// endpoint and queues a job for each. Deliveries are signed the Standard
// Webhooks way (https://www.standardwebhooks.com), so any of its libraries
// verifies them, and retried with backoff for about three and a half hours.
package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/secrets"
)

// JobDeliver sends one delivery.
const JobDeliver = "webhook.deliver"

// MaxAttempts is how many times a delivery is tried: with the queue's
// backoff (5s doubling, at most an hour apart) about three and a half hours.
const MaxAttempts = 12

// Envelope is the body of every delivery.
type Envelope struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"`
	CreatedAt   time.Time `json:"created_at"`
	WorkspaceID string    `json:"workspace_id"`
	Actor       Ref       `json:"actor"`
	Target      *Ref      `json:"target"`
	// RequestID is the API request that made the change, when one did:
	// the same id as the X-Request-Id answer header and audit log entry.
	RequestID *string `json:"request_id"`
	// BrandID is the brand of the changed record, when it has one.
	BrandID *string        `json:"brand_id"`
	Data    map[string]any `json:"data"`
}

// Ref names an actor or a record.
type Ref struct {
	Type string  `json:"type"`
	ID   *string `json:"id"`
}

// FromAudit builds the event for an audit log row; the webhooks and the
// events feed send the same shape.
func FromAudit(id int64, at time.Time, workspaceID, actorType string, actorID *string, action string, targetType, targetID, requestID *string, data map[string]any) Envelope {
	ev := Envelope{
		ID:          "evt_" + strconv.FormatInt(id, 10),
		Type:        action,
		CreatedAt:   at.UTC(),
		WorkspaceID: workspaceID,
		Actor:       Ref{Type: actorType, ID: actorID},
		RequestID:   requestID,
		Data:        data,
	}
	if ev.Data == nil {
		ev.Data = map[string]any{}
	}
	if targetType != nil && *targetType != "" {
		ev.Target = &Ref{Type: *targetType, ID: targetID}
	}
	return ev
}

// Matching lists the catalog types that patterns select ("*", a type, or
// "lead.*"); none selects every type.
func Matching(patterns []string) []string {
	if len(patterns) == 0 {
		patterns = []string{"*"}
	}
	var out []string
	for _, e := range Catalog {
		if e.Type == TestEvent {
			continue
		}
		for _, p := range patterns {
			if p == "*" || p == e.Type || (strings.HasSuffix(p, ".*") && strings.HasPrefix(e.Type, strings.TrimSuffix(p, "*"))) {
				out = append(out, e.Type)
				break
			}
		}
	}
	return out
}

// Querier is what Enqueue needs: a transaction or the pool.
type Querier = jobs.Querier

// Enqueue writes a delivery, and queues a job, for every enabled endpoint
// in the workspace subscribed to the event: every brand's, or the event's
// brand's. It does nothing for events outside the catalog. Call it in the
// transaction that made the change.
func Enqueue(ctx context.Context, q Querier, ev Envelope) error {
	if ev.WorkspaceID == "" || !Delivered(ev.Type) || ev.Type == TestEvent {
		return nil
	}
	if ev.Data == nil {
		ev.Data = map[string]any{}
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	// One statement: endpoints are matched, deliveries written and their
	// jobs queued together. With no endpoints it inserts nothing.
	tag, err := q.Exec(ctx, `
		WITH d AS (
			INSERT INTO webhook_deliveries (endpoint_id, workspace_id, event_id, event_type, payload)
			SELECT e.id, e.workspace_id, $2, $3, $4
			  FROM webhook_endpoints e
			 WHERE e.workspace_id = $1 AND e.enabled AND (e.brand_id IS NULL OR e.brand_id = $7::uuid)
			   AND EXISTS (SELECT 1 FROM unnest(e.events) p
			         WHERE p = '*' OR p = $3 OR (right(p, 2) = '.*' AND $3 LIKE left(p, -1) || '%'))
			   -- An endpoint listening for some reply labels skips replies
			   -- with other labels, or none yet.
			   AND (cardinality(e.categories) = 0 OR NOT ($4::jsonb -> 'data' ? 'category')
			        OR ($4::jsonb -> 'data' ->> 'category') = ANY(e.categories))
			RETURNING id, workspace_id)
		INSERT INTO jobs (kind, payload, workspace_id, run_at, max_attempts)
		SELECT $5, jsonb_build_object('delivery_id', d.id), d.workspace_id, now(), $6 FROM d`,
		ev.WorkspaceID, ev.ID, ev.Type, body, JobDeliver, MaxAttempts, ev.BrandID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		_, err = q.Exec(ctx, `SELECT pg_notify('outflare_jobs', '')`)
	}
	return err
}

// NewSecret makes a signing secret in the Standard Webhooks format.
func NewSecret() string {
	b := make([]byte, 24)
	rand.Read(b) //nolint:errcheck
	return "whsec_" + base64.StdEncoding.EncodeToString(b)
}

// Sign returns the webhook-signature header value for a delivery.
func Sign(secret, msgID string, ts time.Time, body []byte) (string, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "%s.%d.", msgID, ts.Unix())
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

func randomID() string {
	b := make([]byte, 8)
	rand.Read(b) //nolint:errcheck
	return fmt.Sprintf("%x", b)
}

// ---- Delivering ----

// Service delivers queued webhooks.
type Service struct {
	Pool *pgxpool.Pool
	Box  *secrets.Box
	Log  *slog.Logger
	// AllowPrivate lets endpoints on private, loopback and link-local
	// addresses (and plain http) through: for self-hosted servers whose
	// receivers are on the same network. Off, only public https URLs work.
	AllowPrivate bool
	// Client overrides the HTTP client, for tests.
	Client *http.Client
}

// Register adds the delivery job handler.
func (s *Service) Register(q *jobs.Queue) { q.Handle(JobDeliver, s.deliverJob) }

// CheckURL reports why a URL cannot be an endpoint, or nil.
func CheckURL(raw string, allowPrivate bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("url must be an absolute http(s) URL")
	}
	if u.User != nil {
		return errors.New("url must not contain a user name or password; put credentials in the path or query if the receiver needs them")
	}
	if allowPrivate {
		return nil
	}
	if u.Scheme != "https" {
		return errors.New("url must use https")
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".local") {
		return errors.New("url must be reachable on the public internet")
	}
	if ip, err := netip.ParseAddr(host); err == nil && !publicAddr(ip) {
		return errors.New("url must be reachable on the public internet")
	}
	return nil
}

func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() &&
		!netip.MustParsePrefix("100.64.0.0/10").Contains(ip) && !netip.MustParsePrefix("fc00::/7").Contains(ip)
}

func (s *Service) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	if !s.AllowPrivate {
		// Checked on the address actually dialled, so a name that
		// resolves to a private address (or changes to one) is refused.
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil || !publicAddr(ip) {
				return fmt.Errorf("refusing to deliver to a non-public address %s", host)
			}
			return nil
		}
	}
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second},
		// A redirect could point anywhere; receivers must answer directly.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type deliverPayload struct {
	DeliveryID int64 `json:"delivery_id"`
}

func (s *Service) deliverJob(ctx context.Context, job jobs.Job) error {
	var p deliverPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	retry, err := s.attempt(ctx, p.DeliveryID, job.Attempt >= job.MaxAttempts, false)
	if err != nil {
		return err
	}
	if retry != "" {
		return fmt.Errorf("webhook delivery %d: %s", p.DeliveryID, retry)
	}
	return nil
}

// FailingFor is how long an endpoint may fail every delivery before it is
// turned off.
const FailingFor = 5 * 24 * time.Hour

// attempt sends a delivery once and records the result. lastTry marks it
// failed if this try fails; otherwise it stays pending and attempt returns
// what went wrong, for the queue to retry. force sends it whatever its
// status, for a redelivery by hand.
func (s *Service) attempt(ctx context.Context, deliveryID int64, lastTry, force bool) (retry string, err error) {
	var (
		endpointID, eventID, rawURL string
		payload, sealed, prevSealed []byte
		prevExpires                 *time.Time
		enabled                     bool
		status                      string
	)
	err = s.Pool.QueryRow(ctx, `
		SELECT d.endpoint_id, d.event_id, d.payload, d.status, e.url, e.secret, e.previous_secret, e.previous_secret_expires_at, e.enabled
		  FROM webhook_deliveries d JOIN webhook_endpoints e ON e.id = d.endpoint_id
		 WHERE d.id = $1`, deliveryID).Scan(&endpointID, &eventID, &payload, &status, &rawURL, &sealed, &prevSealed, &prevExpires, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil // the endpoint was deleted
	}
	if err != nil {
		return "", err
	}
	if status != "pending" && !force {
		return "", nil
	}
	if !enabled && !force {
		_, err := s.Pool.Exec(ctx, `UPDATE webhook_deliveries SET status = 'cancelled', last_error = 'endpoint disabled', updated_at = now() WHERE id = $1`, deliveryID)
		return "", err
	}
	secret, err := s.Box.Open(sealed, []byte(endpointID))
	if err != nil {
		return "", fmt.Errorf("%w: open secret: %v", jobs.ErrPermanent, err)
	}
	secrets := []string{string(secret)}
	if prevSealed != nil && prevExpires != nil && time.Now().Before(*prevExpires) {
		if prev, err := s.Box.Open(prevSealed, []byte(endpointID)); err == nil {
			secrets = append(secrets, string(prev))
		}
	}

	code, errText := s.send(ctx, rawURL, secrets, eventID, payload)
	ok := code >= 200 && code < 300
	final := ok || lastTry || code == http.StatusGone
	newStatus := "pending"
	switch {
	case ok:
		newStatus = "delivered"
	case final:
		newStatus = "failed"
	}
	var codePtr *int
	if code != 0 {
		codePtr = &code
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE webhook_deliveries SET status = $2, attempts = attempts + 1, last_status_code = $3, last_error = $4,
			       last_attempt_at = now(), delivered_at = CASE WHEN $2 = 'delivered' THEN now() END, updated_at = now()
			 WHERE id = $1`, deliveryID, newStatus, codePtr, nullIf(errText)); err != nil {
			return err
		}
		if ok {
			_, err := tx.Exec(ctx, `UPDATE webhook_endpoints SET last_success_at = now(), failing_since = NULL WHERE id = $1`, endpointID)
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE webhook_endpoints SET failing_since = coalesce(failing_since, now()) WHERE id = $1`, endpointID); err != nil {
			return err
		}
		reason := ""
		switch {
		case code == http.StatusGone:
			reason = "the receiver answered 410 Gone"
		case final:
			reason = "every delivery failed for 5 days"
		}
		if reason == "" {
			return nil
		}
		// A receiver that says it is gone, or that has failed everything for
		// days, is turned off rather than retried forever. Turning it back
		// on (PATCH enabled: true) starts afresh.
		_, err := tx.Exec(ctx, `
			UPDATE webhook_endpoints SET enabled = false, disabled_reason = $2, updated_at = now()
			 WHERE id = $1 AND enabled AND ($3 OR failing_since < now() - $4::interval)`,
			endpointID, reason, code == http.StatusGone, fmt.Sprintf("%d seconds", int(FailingFor.Seconds())))
		return err
	})
	if err != nil || final {
		return "", err
	}
	return errText, nil
}

// SendTest sends a webhook.test event to one endpoint now, and returns the
// delivery with the result.
func (s *Service) SendTest(ctx context.Context, workspaceID, endpointID string, actor Ref) (int64, error) {
	ev := Envelope{ID: "evt_test_" + randomID(), Type: TestEvent, CreatedAt: time.Now().UTC(), WorkspaceID: workspaceID, Actor: actor,
		Data: map[string]any{"message": "This is a test event from Outflare."}}
	body, err := json.Marshal(ev)
	if err != nil {
		return 0, err
	}
	var id int64
	if err := s.Pool.QueryRow(ctx, `
		INSERT INTO webhook_deliveries (endpoint_id, workspace_id, event_id, event_type, payload)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`, endpointID, workspaceID, ev.ID, ev.Type, body).Scan(&id); err != nil {
		return 0, err
	}
	_, err = s.attempt(ctx, id, true, true)
	return id, err
}

// Redeliver sends a delivery again now, with the same webhook-id so a
// receiver that already handled it can tell. A delivery still being retried
// stays on its schedule if this try fails too.
func (s *Service) Redeliver(ctx context.Context, deliveryID int64) error {
	var status string
	if err := s.Pool.QueryRow(ctx, `SELECT status FROM webhook_deliveries WHERE id = $1`, deliveryID).Scan(&status); err != nil {
		return err
	}
	_, err := s.attempt(ctx, deliveryID, status != "pending", true)
	return err
}

// send posts one delivery and returns the status code (0 when there was no
// answer) and what went wrong.
func (s *Service) send(ctx context.Context, rawURL string, secrets []string, msgID string, body []byte) (int, string) {
	if err := CheckURL(rawURL, s.AllowPrivate); err != nil {
		return 0, err.Error()
	}
	now := time.Now()
	// While a rotated secret is still valid, both sign: receivers on
	// either secret keep verifying.
	var sigs []string
	for _, secret := range secrets {
		sig, err := Sign(secret, msgID, now, body)
		if err != nil {
			return 0, "bad signing secret"
		}
		sigs = append(sigs, sig)
	}
	sig := strings.Join(sigs, " ")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return 0, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Outflare-Webhooks/1 (+https://outflare.ai/docs/api/webhooks/)")
	req.Header.Set("webhook-id", msgID)
	req.Header.Set("webhook-timestamp", strconv.FormatInt(now.Unix(), 10))
	req.Header.Set("webhook-signature", sig)
	resp, err := s.client().Do(req)
	if err != nil {
		return 0, truncate(err.Error())
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, ""
	}
	msg := resp.Status
	if s := strings.TrimSpace(string(snippet)); s != "" {
		msg += ": " + s
	}
	return resp.StatusCode, truncate(msg)
}

func truncate(s string) string {
	if len(s) > 500 {
		return s[:500]
	}
	return s
}

func nullIf(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Prune deletes deliveries older than 30 days.
func (s *Service) Prune(ctx context.Context) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM webhook_deliveries WHERE created_at < now() - interval '30 days'`)
	return tag.RowsAffected(), err
}
