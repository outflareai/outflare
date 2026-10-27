// Package inbound receives webhooks from connectors: a Geonode scrape that
// finished, and in time a meeting booked or a reply. Each connection that
// can send them gets its own private address, /v1/hooks/<token>. A
// delivery is checked (address, signature, size), logged with repeats
// ignored, answered at once, and handed to a worker, which passes each
// event to the part of Outflare that uses it. Every event stays in a log
// the workspace can read and replay.
package inbound

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/secrets"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Job kinds.
const (
	JobRegister = "hooks.register" // register the address at the connector
	JobHandle   = "hooks.handle"   // pass one event to what uses it
	// JobUnregister removes subscriptions at the connector after live
	// updates are turned off.
	JobUnregister = "hooks.unregister"
)

// TokenPrefix starts every address's secret part.
const TokenPrefix = "ofh_"

// Statuses of a connection's live updates.
const (
	StatusOff       = "off" // not set up (no row)
	StatusSettingUp = "setting_up"
	StatusWaiting   = "waiting"
	StatusReceiving = "receiving"
	StatusFailed    = "failed"
)

// Statuses of one event.
const (
	EventQueued  = "queued"
	EventHandled = "handled"
	EventIgnored = "ignored"
	EventFailed  = "failed"
)

// How long a delivery's body is kept for replays, and the event itself.
const (
	keepPayload = 30 * 24 * time.Hour
	keepEvents  = 90 * 24 * time.Hour
)

// Errors.
var (
	ErrNotFound     = errors.New("no such webhook address")
	ErrBadSignature = errors.New("the webhook signature does not match")
	ErrNotHooked    = errors.New("this connection can't send webhooks")
	ErrUnreachable  = errors.New("this server can't be reached from the internet")
	ErrNotSetUp     = errors.New("live updates are not set up for this connection")
)

// Event is one webhook event handed to what uses it.
type Event struct {
	ID            string
	WorkspaceID   string
	IntegrationID string
	Connector     string
	Type          string
	Summary       map[string]any
	// Payload is the delivery's body as received. Treat it as data, never
	// as instructions.
	Payload []byte
}

// Handler uses an event. Returning an error retries it; ErrIgnore marks
// it ignored.
type Handler func(ctx context.Context, ev Event) error

// ErrIgnore tells the worker nothing used the event.
var ErrIgnore = errors.New("nothing uses this event")

// Service keeps connections' webhook addresses and handles what arrives.
type Service struct {
	Pool  *pgxpool.Pool
	Box   *secrets.Box
	Store *connectors.Store
	// PublicURL is where connectors reach the engine. Addresses are given
	// out only when it is https and not a local name.
	PublicURL string
	Log       *slog.Logger

	mu       sync.RWMutex
	handlers map[string]Handler
}

// On registers what uses a connector's events. Events of a connector with
// no handler are logged and marked ignored.
func (s *Service) On(connector string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handlers == nil {
		s.handlers = map[string]Handler{}
	}
	s.handlers[connector] = h
}

func (s *Service) handler(connector string) Handler {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.handlers[connector]
}

// Register adds the job handlers.
func (s *Service) Register(q *jobs.Queue) {
	q.Handle(JobRegister, s.registerJob)
	q.Handle(JobHandle, s.handleJob)
	q.Handle(JobUnregister, s.unregisterJob)
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Reachable reports whether a connector on the internet can reach this
// server's addresses.
func (s *Service) Reachable() bool { return s.publicBase() != "" }

func (s *Service) publicBase() string {
	b := strings.TrimRight(s.PublicURL, "/")
	u, err := url.Parse(b)
	if err != nil || u.Scheme != "https" {
		return ""
	}
	h := u.Hostname()
	if h == "localhost" || h == "127.0.0.1" || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".localhost") {
		return ""
	}
	return b
}

// URL is the address for a token, or "" when the server is unreachable.
func (s *Service) URL(token string) string {
	b := s.publicBase()
	if b == "" {
		return ""
	}
	return b + "/v1/hooks/" + token
}

// Hooked returns the connector's webhook support, if it has any.
func (s *Service) Hooked(connector string) (connectors.Hooked, bool) {
	c, ok := s.Store.Registry.Get(connector)
	if !ok {
		return nil, false
	}
	h, ok := c.(connectors.Hooked)
	return h, ok
}

func newToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return TokenPrefix + base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(t string) []byte {
	h := sha256.Sum256([]byte(t))
	return h[:]
}

func payload[T any](job jobs.Job) (T, string, error) {
	var p T
	if job.WorkspaceID == nil {
		return p, "", fmt.Errorf("%w: no workspace", jobs.ErrPermanent)
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return p, "", fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	return p, *job.WorkspaceID, nil
}

// ---- Setting up ----

type registerPayload struct {
	IntegrationID string `json:"integration_id"`
	// Old are subscriptions to remove first (after a new address).
	Old []string `json:"old,omitempty"`
}

// Connected sets up live updates for a new connection that can send them.
// Register it as a connectors.Store OnConnect hook.
func (s *Service) Connected(ctx context.Context, tx pgx.Tx, i connectors.Integration) error {
	h, ok := s.Hooked(i.Connector)
	if !ok || !h.HookSetup().Auto || !s.Reachable() {
		return nil
	}
	return s.setUp(ctx, tx, i.WorkspaceID, i.ID, i.Connector)
}

// SetUp turns live updates on for a connection: it makes the address and,
// when the connector can, registers it there. Already on, it registers
// again only if the last attempt failed.
func (s *Service) SetUp(ctx context.Context, tx pgx.Tx, ws, integrationID string) error {
	i, err := s.Store.Get(ctx, tx, ws, integrationID)
	if err != nil {
		return err
	}
	if _, ok := s.Hooked(i.Connector); !ok {
		return ErrNotHooked
	}
	if !s.Reachable() {
		return ErrUnreachable
	}
	return s.setUp(ctx, tx, ws, integrationID, i.Connector)
}

func (s *Service) setUp(ctx context.Context, tx pgx.Tx, ws, integrationID, connector string) error {
	h, _ := s.Hooked(connector)
	auto := h.HookSetup().Auto
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM connection_hooks WHERE integration_id = $1 AND workspace_id = $2 FOR UPDATE`, integrationID, ws).Scan(&status)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		token := newToken()
		st, setup := StatusWaiting, "manual"
		if auto {
			st, setup = StatusSettingUp, "auto"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO connection_hooks (integration_id, workspace_id, connector, token, token_hash, setup, status)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			integrationID, ws, connector, s.Box.Seal([]byte(token), []byte(integrationID)), hashToken(token), setup, st); err != nil {
			return err
		}
	case err != nil:
		return err
	case status != StatusFailed || !auto:
		return nil
	default:
		if _, err := tx.Exec(ctx, `UPDATE connection_hooks SET status = 'setting_up', setup = 'auto', last_error = NULL, updated_at = now() WHERE integration_id = $1`, integrationID); err != nil {
			return err
		}
	}
	if !auto {
		return nil
	}
	_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobRegister, WorkspaceID: &ws, Payload: registerPayload{IntegrationID: integrationID}, MaxAttempts: 4})
	return err
}

// Rotate gives the connection a new address; the old one stops at once.
// An address Outflare registered is replaced at the connector too.
func (s *Service) Rotate(ctx context.Context, tx pgx.Tx, ws, integrationID string) error {
	var setup string
	var remote []string
	err := tx.QueryRow(ctx, `SELECT setup, remote_ids FROM connection_hooks WHERE integration_id = $1 AND workspace_id = $2 FOR UPDATE`, integrationID, ws).Scan(&setup, &remote)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotSetUp
	}
	if err != nil {
		return err
	}
	token := newToken()
	status := StatusWaiting
	if setup == "auto" {
		status = StatusSettingUp
	}
	if _, err := tx.Exec(ctx, `UPDATE connection_hooks SET token = $2, token_hash = $3, status = $4, last_error = NULL,
		remote_ids = CASE WHEN setup = 'auto' THEN '{}' ELSE remote_ids END,
		signing_secrets = CASE WHEN setup = 'auto' THEN NULL ELSE signing_secrets END,
		rotated_at = now(), updated_at = now() WHERE integration_id = $1`,
		integrationID, s.Box.Seal([]byte(token), []byte(integrationID)), hashToken(token), status); err != nil {
		return err
	}
	if setup != "auto" {
		return nil
	}
	_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobRegister, WorkspaceID: &ws, Payload: registerPayload{IntegrationID: integrationID, Old: remote}, MaxAttempts: 4})
	return err
}

// SetSecret saves the signing secret a person copied from the connector
// after pasting the address there themselves. Empty removes it, leaving
// the address as the only lock.
func (s *Service) SetSecret(ctx context.Context, tx pgx.Tx, ws, integrationID, secret string) error {
	var sealed []byte
	if secret != "" {
		b, _ := json.Marshal([]string{secret})
		sealed = s.Box.Seal(b, []byte(integrationID))
	}
	tag, err := tx.Exec(ctx, `UPDATE connection_hooks SET signing_secrets = $3, updated_at = now() WHERE integration_id = $1 AND workspace_id = $2`, integrationID, ws, sealed)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotSetUp
	}
	return err
}

// Remove turns live updates off: the address stops working, and
// subscriptions Outflare made are removed at the connector, best effort.
// The event log stays.
func (s *Service) Remove(ctx context.Context, tx pgx.Tx, ws, integrationID string) error {
	var remote []string
	err := tx.QueryRow(ctx, `DELETE FROM connection_hooks WHERE integration_id = $1 AND workspace_id = $2 RETURNING remote_ids`, integrationID, ws).Scan(&remote)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotSetUp
	}
	if err != nil || len(remote) == 0 {
		return err
	}
	_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobUnregister, WorkspaceID: &ws, Payload: registerPayload{IntegrationID: integrationID, Old: remote}, MaxAttempts: 3})
	return err
}

func (s *Service) unregisterJob(ctx context.Context, job jobs.Job) error {
	p, ws, err := payload[registerPayload](job)
	if err != nil {
		return err
	}
	s.deleteRemote(ctx, s.Pool, ws, p.IntegrationID, p.Old)
	return nil
}

// Disconnect removes the connector's subscriptions before the connection
// is deleted. Register it as a connectors.Store OnDisconnect hook. It is
// best effort: an unreachable connector must not stop a disconnect, and a
// leftover subscription only gets 404s.
func (s *Service) Disconnect(ctx context.Context, tx pgx.Tx, i connectors.Integration) error {
	var remote []string
	err := tx.QueryRow(ctx, `SELECT remote_ids FROM connection_hooks WHERE integration_id = $1`, i.ID).Scan(&remote)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	s.deleteRemote(ctx, tx, i.WorkspaceID, i.ID, remote)
	return nil
}

func (s *Service) deleteRemote(ctx context.Context, q connectors.Querier, ws, integrationID string, ids []string) {
	if len(ids) == 0 {
		return
	}
	o, err := s.Store.Open(ctx, q, ws, integrationID)
	if err != nil {
		return
	}
	r, ok := o.Connector.(connectors.HookRegistrar)
	if !ok {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := r.DeleteHooks(cctx, o.Client, o.Creds, ids); err != nil {
		s.log().Warn("could not remove the connector's webhook", "integration", integrationID, "connector", o.Integration.Connector, "err", err)
	}
}

// registerJob registers the address at the connector. Its failures are
// shown on the connection rather than left to die quietly.
func (s *Service) registerJob(ctx context.Context, job jobs.Job) error {
	p, ws, err := payload[registerPayload](job)
	if err != nil {
		return err
	}
	ev := track.Event{Name: "connection_hook.registered", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"integration_id": p.IntegrationID}}
	o, err := s.Store.Open(ctx, s.Pool, ws, p.IntegrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	ev.Props["connector"] = o.Integration.Connector
	r, ok := o.Connector.(connectors.HookRegistrar)
	if !ok {
		return nil
	}
	if len(p.Old) > 0 {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		if err := r.DeleteHooks(cctx, o.Client, o.Creds, p.Old); err != nil {
			s.log().Warn("could not remove the connector's old webhook", "integration", p.IntegrationID, "err", err)
		}
		cancel()
	}
	var sealed []byte
	var status string
	err = s.Pool.QueryRow(ctx, `SELECT token, status FROM connection_hooks WHERE integration_id = $1 AND workspace_id = $2`, p.IntegrationID, ws).Scan(&sealed, &status)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != StatusSettingUp) {
		return nil
	}
	if err != nil {
		return err
	}
	token, err := s.Box.Open(sealed, []byte(p.IntegrationID))
	if err != nil {
		return err
	}
	hookURL := s.URL(string(token))
	if hookURL == "" {
		return s.registerFailed(ctx, ws, p.IntegrationID, ev, "not_configured", ErrUnreachable)
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	made, err := r.RegisterHooks(cctx, o.Client, o.Creds, hookURL)
	cancel()
	if err != nil {
		k := connectors.KindOf(err)
		if (k == connectors.ErrUnavailable || k == connectors.ErrQuota) && job.Attempt < job.MaxAttempts {
			return err
		}
		return s.registerFailed(ctx, ws, p.IntegrationID, ev, "connector_"+k, err)
	}
	ids, keys := make([]string, len(made)), make([]string, len(made))
	for i, h := range made {
		ids[i], keys[i] = h.ID, h.Secret
	}
	b, _ := json.Marshal(keys)
	tag, err := s.Pool.Exec(ctx, `UPDATE connection_hooks SET remote_ids = $3, signing_secrets = $4,
		status = CASE WHEN last_event_at IS NULL THEN 'waiting' ELSE 'receiving' END, last_error = NULL, updated_at = now()
		WHERE integration_id = $1 AND workspace_id = $2 AND token_hash = $5 AND status = 'setting_up'`,
		p.IntegrationID, ws, ids, s.Box.Seal(b, []byte(p.IntegrationID)), hashToken(string(token)))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// Turned off or given a new address meanwhile: undo this one.
		_ = r.DeleteHooks(ctx, o.Client, o.Creds, ids)
		return nil
	}
	ev.Props["subscriptions"] = len(made)
	track.Emit(ctx, ev)
	return nil
}

func (s *Service) registerFailed(ctx context.Context, ws, integrationID string, ev track.Event, code string, err error) error {
	ev.Name = "connection_hook.register_failed"
	track.Fail(ctx, s.log(), ev, code, err)
	msg := err.Error()
	if len(msg) > 300 {
		msg = msg[:300]
	}
	_, uerr := s.Pool.Exec(ctx, `UPDATE connection_hooks SET status = 'failed', last_error = $3, updated_at = now()
		WHERE integration_id = $1 AND workspace_id = $2 AND status = 'setting_up'`, integrationID, ws, msg)
	return uerr
}

// ---- Receiving ----

// Received is what a delivery added.
type Received struct {
	Connector  string
	Events     int // new events queued
	Duplicates int // events already received
}

// Receive checks and records one delivery and queues its new events. A
// repeat of an event is accepted and ignored.
func (s *Service) Receive(ctx context.Context, token string, h http.Header, body []byte) (Received, error) {
	out := Received{Connector: "unknown"}
	if !strings.HasPrefix(token, TokenPrefix) || len(token) > 100 {
		metrics.HookDelivery(out.Connector, "not_found")
		return out, ErrNotFound
	}
	var ws, integrationID, connector string
	var sealedSecrets []byte
	err := s.Pool.QueryRow(ctx, `SELECT workspace_id, integration_id, connector, signing_secrets FROM connection_hooks WHERE token_hash = $1`,
		hashToken(token)).Scan(&ws, &integrationID, &connector, &sealedSecrets)
	if errors.Is(err, pgx.ErrNoRows) {
		metrics.HookDelivery(out.Connector, "not_found")
		return out, ErrNotFound
	}
	if err != nil {
		metrics.HookDelivery(out.Connector, "error")
		return out, err
	}
	out.Connector = connector
	ev := track.Event{Name: "connection_hook.received", WorkspaceID: ws, ActorType: "system", Surface: "system",
		Props: map[string]any{"integration_id": integrationID, "connector": connector}}
	hk, ok := s.Hooked(connector)
	if !ok {
		metrics.HookDelivery(connector, "not_found")
		return out, ErrNotFound
	}
	if sealedSecrets != nil {
		raw, err := s.Box.Open(sealedSecrets, []byte(integrationID))
		if err != nil {
			metrics.HookDelivery(connector, "error")
			return out, err
		}
		var keys []string
		_ = json.Unmarshal(raw, &keys)
		if err := hk.VerifyHook(keys, h, body); err != nil {
			metrics.HookDelivery(connector, "bad_signature")
			ev.Outcome = track.Failure
			track.Fail(ctx, s.log(), ev, "bad_signature", err)
			return out, ErrBadSignature
		}
	}
	events, err := hk.ParseHook(h, body)
	if err != nil {
		metrics.HookDelivery(connector, "invalid")
		ev.Outcome = track.Failure
		track.Fail(ctx, s.log(), ev, "invalid_request", err)
		return out, err
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		for _, e := range events {
			summary, _ := json.Marshal(e.Summary)
			if e.Summary == nil {
				summary = []byte(`{}`)
			}
			var id string
			err := tx.QueryRow(ctx, `INSERT INTO connection_hook_events (workspace_id, integration_id, connector, event_id, event_type, summary)
				VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (integration_id, event_id) DO NOTHING RETURNING id`,
				ws, integrationID, connector, truncate(e.ID, 200), truncate(e.Type, 100), summary).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				out.Duplicates++
				if _, err := tx.Exec(ctx, `UPDATE connection_hook_events SET duplicates = duplicates + 1 WHERE integration_id = $1 AND event_id = $2`,
					integrationID, truncate(e.ID, 200)); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			// The body is sealed per event, so a replay reads what arrived.
			if _, err := tx.Exec(ctx, `UPDATE connection_hook_events SET payload = $2 WHERE id = $1`, id, s.Box.Seal(body, []byte(id))); err != nil {
				return err
			}
			if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobHandle, WorkspaceID: &ws, Payload: handlePayload{EventID: id}, MaxAttempts: 5}); err != nil {
				return err
			}
			out.Events++
		}
		_, err := tx.Exec(ctx, `UPDATE connection_hooks SET last_event_at = now(),
			status = CASE WHEN status IN ('waiting', 'failed') THEN 'receiving' ELSE status END, updated_at = now()
			WHERE integration_id = $1`, integrationID)
		return err
	})
	if err != nil {
		metrics.HookDelivery(connector, "error")
		return out, err
	}
	outcome := "accepted"
	if out.Events == 0 && out.Duplicates > 0 {
		outcome = "duplicate"
	}
	metrics.HookDelivery(connector, outcome)
	ev.Props["events"], ev.Props["duplicates"] = out.Events, out.Duplicates
	track.Emit(ctx, ev)
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

type handlePayload struct {
	EventID string `json:"event_id"`
}

// handleJob passes one event to what uses its connector's events.
func (s *Service) handleJob(ctx context.Context, job jobs.Job) error {
	p, ws, err := payload[handlePayload](job)
	if err != nil {
		return err
	}
	var ev Event
	var sealed []byte
	var summary []byte
	var receivedAt time.Time
	err = s.Pool.QueryRow(ctx, `SELECT id, workspace_id, integration_id, connector, event_type, summary, payload, received_at
		FROM connection_hook_events WHERE id = $1 AND workspace_id = $2`, p.EventID, ws).
		Scan(&ev.ID, &ev.WorkspaceID, &ev.IntegrationID, &ev.Connector, &ev.Type, &summary, &sealed, &receivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_ = json.Unmarshal(summary, &ev.Summary)
	if sealed != nil {
		if ev.Payload, err = s.Box.Open(sealed, []byte(ev.ID)); err != nil {
			return err
		}
	}
	te := track.Event{Name: "connection_hook.event_handled", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"integration_id": ev.IntegrationID, "connector": ev.Connector, "event_type": ev.Type, "attempt": job.Attempt}}
	status, code := EventHandled, ""
	if h := s.handler(ev.Connector); h == nil {
		status = EventIgnored
	} else if herr := h(ctx, ev); errors.Is(herr, ErrIgnore) {
		status = EventIgnored
	} else if herr != nil {
		code = "internal"
		var cerr *connectors.Error
		if errors.As(herr, &cerr) {
			code = "connector_" + cerr.Kind
		}
		if job.Attempt < job.MaxAttempts && !errors.Is(herr, jobs.ErrPermanent) {
			metrics.HookEvent(ev.Connector, "retry", 0)
			_, _ = s.Pool.Exec(ctx, `UPDATE connection_hook_events SET attempts = $2, error_code = $3 WHERE id = $1`, ev.ID, job.Attempt, code)
			return herr
		}
		status = EventFailed
		te.Name = "connection_hook.event_failed"
		track.Fail(ctx, s.log(), te, code, herr)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE connection_hook_events SET status = $2, error_code = NULLIF($3, ''), attempts = $4, handled_at = now() WHERE id = $1`,
		ev.ID, status, code, job.Attempt); err != nil {
		return err
	}
	metrics.HookEvent(ev.Connector, status, time.Since(receivedAt))
	if status != EventFailed {
		te.Props["result"] = status
		track.Emit(ctx, te)
	}
	return nil
}

// Replay hands an event to a worker again, as if it had just arrived.
func (s *Service) Replay(ctx context.Context, tx pgx.Tx, ws, eventID string) error {
	var hasPayload bool
	err := tx.QueryRow(ctx, `UPDATE connection_hook_events SET status = 'queued', error_code = NULL, handled_at = NULL
		WHERE id = $1 AND workspace_id = $2 RETURNING payload IS NOT NULL`, eventID, ws).Scan(&hasPayload)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobHandle, WorkspaceID: &ws, Payload: handlePayload{EventID: eventID}, MaxAttempts: 5})
	return err
}

// ---- Reading ----

// View is a connection's live updates, as the API shows them.
type View struct {
	IntegrationID string               `json:"integration_id"`
	Connector     string               `json:"connector"`
	Status        string               `json:"status"`
	Setup         *string              `json:"setup"`
	URL           *string              `json:"url"`
	Signed        bool                 `json:"signed"`
	LastError     *string              `json:"last_error"`
	LastEventAt   *time.Time           `json:"last_event_at"`
	RotatedAt     *time.Time           `json:"rotated_at"`
	CreatedAt     *time.Time           `json:"created_at"`
	Reachable     bool                 `json:"reachable"`
	Supports      connectors.HookSetup `json:"supports"`
}

// Get returns a connection's live updates; Status is off when they are
// not set up.
func (s *Service) Get(ctx context.Context, q connectors.Querier, ws string, i connectors.Integration) (View, error) {
	h, ok := s.Hooked(i.Connector)
	if !ok {
		return View{}, ErrNotHooked
	}
	v := View{IntegrationID: i.ID, Connector: i.Connector, Status: StatusOff, Reachable: s.Reachable(), Supports: h.HookSetup()}
	if v.Supports.Events == nil {
		v.Supports.Events = []string{}
	}
	var sealed []byte
	var setup string
	err := q.QueryRow(ctx, `SELECT token, setup, status, signing_secrets IS NOT NULL, last_error, last_event_at, rotated_at, created_at
		FROM connection_hooks WHERE integration_id = $1 AND workspace_id = $2`, i.ID, ws).
		Scan(&sealed, &setup, &v.Status, &v.Signed, &v.LastError, &v.LastEventAt, &v.RotatedAt, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	v.Setup = &setup
	token, err := s.Box.Open(sealed, []byte(i.ID))
	if err != nil {
		return v, err
	}
	if u := s.URL(string(token)); u != "" {
		v.URL = &u
	}
	return v, nil
}

// LoggedEvent is one row of the event log.
type LoggedEvent struct {
	ID            string         `json:"id"`
	IntegrationID string         `json:"integration_id"`
	Connector     string         `json:"connector"`
	Type          string         `json:"type"`
	Status        string         `json:"status"`
	ErrorCode     *string        `json:"error_code"`
	Summary       map[string]any `json:"summary"`
	Duplicates    int            `json:"duplicates"`
	Attempts      int            `json:"attempts"`
	Replayable    bool           `json:"replayable"`
	ReceivedAt    time.Time      `json:"received_at"`
	HandledAt     *time.Time     `json:"handled_at"`
}

// Filter narrows the event log.
type Filter struct {
	IntegrationID string
	Status        string
	Before        *time.Time // received before, for paging
	Limit         int
}

// Events lists a workspace's webhook events, newest first.
func (s *Service) Events(ctx context.Context, q connectors.Querier, ws string, f Filter) ([]LoggedEvent, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	rows, err := q.Query(ctx, `SELECT id, integration_id, connector, event_type, status, error_code, summary, duplicates, attempts,
			payload IS NOT NULL, received_at, handled_at
		FROM connection_hook_events
		WHERE workspace_id = $1 AND ($2 = '' OR integration_id::text = $2) AND ($3 = '' OR status = $3)
			AND ($4::timestamptz IS NULL OR received_at < $4)
		ORDER BY received_at DESC, id LIMIT $5`, ws, f.IntegrationID, f.Status, f.Before, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LoggedEvent{}
	for rows.Next() {
		var e LoggedEvent
		var summary []byte
		if err := rows.Scan(&e.ID, &e.IntegrationID, &e.Connector, &e.Type, &e.Status, &e.ErrorCode, &summary, &e.Duplicates, &e.Attempts,
			&e.Replayable, &e.ReceivedAt, &e.HandledAt); err != nil {
			return nil, err
		}
		e.Summary = map[string]any{}
		_ = json.Unmarshal(summary, &e.Summary)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- Housekeeping ----

// Run trims the log every hour until ctx ends: bodies after 30 days,
// events after 90.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if err := s.Prune(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.log(), track.Event{Name: "connection_hook.prune_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Prune removes old bodies and events.
func (s *Service) Prune(ctx context.Context) error {
	if _, err := s.Pool.Exec(ctx, `UPDATE connection_hook_events SET payload = NULL WHERE payload IS NOT NULL AND received_at < $1`,
		time.Now().Add(-keepPayload)); err != nil {
		return err
	}
	_, err := s.Pool.Exec(ctx, `DELETE FROM connection_hook_events WHERE received_at < $1`, time.Now().Add(-keepEvents))
	return err
}
