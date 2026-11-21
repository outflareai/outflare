package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/destinations"
	"github.com/outflareai/outflare/engine/internal/webhooks"
)

// Destinations forward a brand's customer events to the tools it already
// uses (engine/internal/destinations). These routes set them up and show
// what each one sent.

const maxDestinations = 20

var destinationEvent = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$`)

func (s *Server) routesDestinations(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/destinations", s.onBrand(auth.ScopeCustomersRead, s.listDestinations))
	mux.HandleFunc("POST /v1/brands/{brand}/destinations", s.onBrand(auth.ScopeCustomersManage, s.createDestination))
	mux.HandleFunc("GET /v1/brands/{brand}/destinations/{destination}", s.onBrand(auth.ScopeCustomersRead, s.getDestination))
	mux.HandleFunc("PATCH /v1/brands/{brand}/destinations/{destination}", s.onBrand(auth.ScopeCustomersManage, s.updateDestination))
	mux.HandleFunc("DELETE /v1/brands/{brand}/destinations/{destination}", s.onBrand(auth.ScopeCustomersManage, s.deleteDestination))
	mux.HandleFunc("POST /v1/brands/{brand}/destinations/{destination}/test", s.onBrand(auth.ScopeCustomersManage, s.testDestination))
	mux.HandleFunc("GET /v1/brands/{brand}/destinations/{destination}/sends", s.onBrand(auth.ScopeCustomersRead, s.listDestinationSends))
}

type destinationView struct {
	ID            string              `json:"id"`
	Kind          string              `json:"kind"`
	Name          string              `json:"name"`
	Events        []string            `json:"events"`
	Config        destinations.Config `json:"config"`
	IntegrationID *string             `json:"integration_id"`
	HasSecret     bool                `json:"has_secret"`
	Enabled       bool                `json:"enabled"`
	FailingSince  *time.Time          `json:"failing_since"`
	LastError     string              `json:"last_error"`
	LastSuccessAt *time.Time          `json:"last_success_at"`
	Sent24h       int64               `json:"sent_24h"`
	Failed24h     int64               `json:"failed_24h"`
	Queued        int64               `json:"queued"`
	// The last "send a test event": Test.Running while one asked for
	// has not finished.
	Test      destinationTest `json:"test"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type destinationTest struct {
	Running     bool       `json:"running"`
	RequestedAt *time.Time `json:"requested_at"`
	At          *time.Time `json:"at"`
	OK          *bool      `json:"ok"`
	Reason      string     `json:"reason"`
}

// destinationCreated is a new destination with, for a webhook, the secret
// its deliveries are signed with: shown once.
type destinationCreated struct {
	destinationView
	SigningSecret string `json:"signing_secret,omitempty"`
}

type destinationInput struct {
	Kind          *string              `json:"kind"`
	Name          *string              `json:"name"`
	Events        *[]string            `json:"events"`
	Config        *destinations.Config `json:"config"`
	IntegrationID *string              `json:"integration_id"`
	// Secret is a GA4 Measurement Protocol API secret, a Google service
	// account's JSON key (bigquery) or a Snowflake user's private key:
	// taken, sealed and never returned.
	Secret  *string `json:"secret"`
	Enabled *bool   `json:"enabled"`
}

func (s *Server) loadEventDestinations(ctx context.Context, ws, brand string, id *string) ([]destinationView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.id, d.kind, d.name, d.events, d.config, d.integration_id, d.secret IS NOT NULL, d.enabled, d.failing_since, d.last_error,
		       d.last_success_at, d.created_at, d.updated_at, d.test_requested_at, d.last_test_at, d.last_test_ok, d.last_test_reason,
		       (SELECT count(*) FROM event_destination_sends q WHERE q.destination_id = d.id AND q.status = 'sent' AND q.created_at > now() - interval '24 hours'),
		       (SELECT count(*) FROM event_destination_sends q WHERE q.destination_id = d.id AND q.status = 'failed' AND q.created_at > now() - interval '24 hours'),
		       (SELECT count(*) FROM event_destination_sends q WHERE q.destination_id = d.id AND q.status = 'queued')
		FROM event_destinations d
		WHERE d.workspace_id = $1 AND d.brand_id = $2 AND ($3::uuid IS NULL OR d.id = $3)
		ORDER BY d.created_at, d.id`, ws, brand, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (destinationView, error) {
		var v destinationView
		var cfg []byte
		if err := r.Scan(&v.ID, &v.Kind, &v.Name, &v.Events, &cfg, &v.IntegrationID, &v.HasSecret, &v.Enabled, &v.FailingSince, &v.LastError,
			&v.LastSuccessAt, &v.CreatedAt, &v.UpdatedAt, &v.Test.RequestedAt, &v.Test.At, &v.Test.OK, &v.Test.Reason, &v.Sent24h, &v.Failed24h, &v.Queued); err != nil {
			return v, err
		}
		// A test that never came back (its job was lost) stops showing
		// as running after five minutes.
		if t := v.Test.RequestedAt; t != nil && (v.Test.At == nil || v.Test.At.Before(*t)) {
			v.Test.Running = time.Since(*t) < 5*time.Minute
			if !v.Test.Running {
				no := false
				v.Test.At, v.Test.OK, v.Test.Reason = t, &no, "The test did not finish. Try again."
			}
		}
		if v.Events == nil {
			v.Events = []string{}
		}
		return v, json.Unmarshal(cfg, &v.Config)
	})
}

func (s *Server) listDestinations(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	list, err := s.loadEventDestinations(r.Context(), a.WorkspaceID, a.Brand, nil)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// oneDestination loads the destination in the path, or answers 404.
func (s *Server) oneDestination(w http.ResponseWriter, r *http.Request) (destinationView, bool) {
	a := accessFrom(r.Context())
	id := r.PathValue("destination")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "destination not found")
		return destinationView{}, false
	}
	list, err := s.loadEventDestinations(r.Context(), a.WorkspaceID, a.Brand, &id)
	if err != nil {
		s.internal(w, r, err)
		return destinationView{}, false
	}
	if len(list) == 0 {
		writeError(w, r, http.StatusNotFound, "not_found", "destination not found")
		return destinationView{}, false
	}
	return list[0], true
}

func (s *Server) getDestination(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.oneDestination(w, r); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// checkDestination reports why a destination can't be saved as it would
// stand, as a message for the caller, or "".
func (s *Server) checkDestination(ctx context.Context, a *access, v destinationView, hasSecret bool, secret string) (string, error) {
	if n := len([]rune(strings.TrimSpace(v.Name))); n == 0 || n > 120 {
		return "name is required, at most 120 characters", nil
	}
	if len(v.Events) > 50 {
		return "events can name at most 50 events; leave it empty to send every tracked event", nil
	}
	for _, e := range v.Events {
		if len(e) > 100 || !destinationEvent.MatchString(e) {
			return "events must be catalog names such as order_completed", nil
		}
	}
	if err := destinations.Check(v.Kind, v.Config, s.webhooks.AllowPrivate); err != nil {
		return err.Error(), nil
	}
	if destinations.NeedsSecret(v.Kind) && !hasSecret {
		what := map[string]string{"ga4": "the Measurement Protocol API secret from the data stream in Google Analytics",
			"bigquery":  "the JSON key file of a Google service account of yours that may write to the dataset",
			"snowflake": "the private key (PEM) of a Snowflake user of yours set up for key-pair sign-in"}[v.Kind]
		return "secret is required: " + what, nil
	}
	if secret != "" {
		if err := destinations.CheckSecret(v.Kind, secret); err != nil {
			return err.Error(), nil
		}
	}
	if !destinations.NeedsIntegration(v.Kind) {
		return "", nil
	}
	if v.IntegrationID == nil || !validUUID(*v.IntegrationID) {
		return "integration_id is required: the workspace's own " + v.Kind + " connection", nil
	}
	o, err := s.integrations.Open(ctx, s.pool, a.WorkspaceID, *v.IntegrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		return "integration_id is not a connection in this workspace", nil
	}
	if err != nil {
		return "", err
	}
	if o.BrandID != nil && *o.BrandID != a.Brand {
		return "integration_id is another brand's connection", nil
	}
	switch v.Kind {
	case "slack":
		if _, ok := o.Connector.(connectors.Notifier); !ok {
			return "that connection can't post messages; connect Slack under Connectors", nil
		}
	case "hubspot":
		if _, ok := o.Connector.(connectors.ContactPusher); !ok || o.Connector.Definition().ID != "hubspot" {
			return "that connection isn't HubSpot; connect HubSpot under Connectors", nil
		}
	}
	return "", nil
}

func destinationAudit(v destinationView) map[string]any {
	return map[string]any{"brand_id": nil, "kind": v.Kind, "events": len(v.Events), "enabled": v.Enabled}
}

func (s *Server) createDestination(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in destinationInput
	if !decode(w, r, &in) {
		return
	}
	v := destinationView{Kind: deref(in.Kind), Name: strings.TrimSpace(deref(in.Name)), Events: []string{}, IntegrationID: in.IntegrationID, Enabled: in.Enabled == nil || *in.Enabled}
	if in.Events != nil {
		v.Events = *in.Events
	}
	if in.Config != nil {
		v.Config = *in.Config
	}
	secret := strings.TrimSpace(deref(in.Secret))
	ctx := r.Context()
	msg, err := s.checkDestination(ctx, a, v, secret != "", secret)
	if err != nil {
		s.connectorFailed(w, r, err)
		return
	}
	if msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	if !destinations.NeedsIntegration(v.Kind) {
		v.IntegrationID = nil
	}
	signing := ""
	switch {
	case v.Kind == "webhook":
		signing = webhooks.NewSecret()
		secret = signing
	case !destinations.NeedsSecret(v.Kind):
		secret = ""
	}
	cfg, _ := json.Marshal(v.Config)
	var id string
	full := false
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM event_destinations WHERE workspace_id = $1 AND brand_id = $2`, a.WorkspaceID, a.Brand).Scan(&n); err != nil {
			return err
		}
		if n >= maxDestinations {
			full = true
			return nil
		}
		if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()`).Scan(&id); err != nil {
			return err
		}
		var sealed []byte
		if secret != "" {
			sealed = s.box.Seal([]byte(secret), []byte(id))
		}
		if _, err := tx.Exec(ctx, `INSERT INTO event_destinations (id, workspace_id, brand_id, kind, name, events, config, integration_id, secret, enabled, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, id, a.WorkspaceID, a.Brand, v.Kind, v.Name, v.Events, cfg, v.IntegrationID, sealed, v.Enabled, nullIfEmpty(a.ActorID)); err != nil {
			return err
		}
		data := destinationAudit(v)
		data["brand_id"] = a.Brand
		return record(ctx, tx, &a.principal, a.WorkspaceID, "destination.created", "destination", id, data)
	})
	if s.recordError(w, r, err) {
		return
	}
	if full {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a brand can have at most 20 destinations; one destination can take many events")
		return
	}
	list, err := s.loadEventDestinations(ctx, a.WorkspaceID, a.Brand, &id)
	if err != nil || len(list) == 0 {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, destinationCreated{list[0], signing})
}

func (s *Server) updateDestination(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.oneDestination(w, r)
	if !ok {
		return
	}
	var in destinationInput
	if !decode(w, r, &in) {
		return
	}
	if in.Kind != nil && *in.Kind != cur.Kind {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a destination's kind can't change; make a new one")
		return
	}
	next := cur
	if in.Name != nil {
		next.Name = strings.TrimSpace(*in.Name)
	}
	if in.Events != nil {
		next.Events = *in.Events
	}
	if in.Config != nil {
		next.Config = *in.Config
	}
	if in.IntegrationID != nil {
		next.IntegrationID = in.IntegrationID
	}
	if in.Enabled != nil {
		next.Enabled = *in.Enabled
	}
	secret := ""
	if destinations.NeedsSecret(cur.Kind) {
		secret = strings.TrimSpace(deref(in.Secret))
	}
	ctx := r.Context()
	msg, err := s.checkDestination(ctx, a, next, cur.HasSecret || secret != "", secret)
	if err != nil {
		s.connectorFailed(w, r, err)
		return
	}
	if msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	cfg, _ := json.Marshal(next.Config)
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var sealed []byte
		if secret != "" {
			sealed = s.box.Seal([]byte(secret), []byte(cur.ID))
		}
		// Switching one back on starts from now: what arrived while it
		// was off is not sent.
		if _, err := tx.Exec(ctx, `UPDATE event_destinations SET name = $3, events = $4, config = $5, integration_id = $6,
			secret = COALESCE($7, secret), enabled = $8, matched_until = CASE WHEN $8 AND NOT enabled THEN now() ELSE matched_until END,
			failing_since = NULL, last_error = '', updated_at = now() WHERE workspace_id = $1 AND id = $2`,
			a.WorkspaceID, cur.ID, next.Name, next.Events, cfg, next.IntegrationID, sealed, next.Enabled); err != nil {
			return err
		}
		data := destinationAudit(next)
		data["brand_id"] = a.Brand
		data["secret_changed"] = secret != ""
		return record(ctx, tx, &a.principal, a.WorkspaceID, "destination.updated", "destination", cur.ID, data)
	})
	if s.recordError(w, r, err) {
		return
	}
	list, err := s.loadEventDestinations(ctx, a.WorkspaceID, a.Brand, &cur.ID)
	if err != nil || len(list) == 0 {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list[0])
}

func (s *Server) deleteDestination(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.oneDestination(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM event_destinations WHERE workspace_id = $1 AND brand_id = $2 AND id = $3`, a.WorkspaceID, a.Brand, cur.ID); err != nil {
			return err
		}
		data := destinationAudit(cur)
		data["brand_id"] = a.Brand
		return record(ctx, tx, &a.principal, a.WorkspaceID, "destination.deleted", "destination", cur.ID, data)
	})
	if s.recordError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testDestination asks for one made-up event to be sent to the
// destination, on or off. The result shows on the destination (test).
func (s *Server) testDestination(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.oneDestination(w, r)
	if !ok {
		return
	}
	if cur.Test.Running {
		writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE event_destinations SET test_requested_at = now() WHERE workspace_id = $1 AND brand_id = $2 AND id = $3`, a.WorkspaceID, a.Brand, cur.ID); err != nil {
			return err
		}
		if _, err := destinations.EnqueueTest(ctx, tx, a.WorkspaceID, cur.ID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "destination.test_requested", "destination", cur.ID, map[string]any{"brand_id": a.Brand, "kind": cur.Kind})
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

type destinationSendView struct {
	ID         string     `json:"id"`
	EventID    string     `json:"event_id"`
	EventName  string     `json:"event_name"`
	Status     string     `json:"status"`
	Reason     string     `json:"reason"`
	Attempts   int        `json:"attempts"`
	OccurredAt time.Time  `json:"occurred_at"`
	SentAt     *time.Time `json:"sent_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

// listDestinationSends lists what a destination did with its newest
// events: at most 200, optionally only one status.
func (s *Server) listDestinationSends(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.oneDestination(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "", "queued", "sent", "skipped", "failed":
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status must be queued, sent, skipped or failed")
		return
	}
	limit, err := limitParam(r, 50, 200)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT q.id, q.event_row_id, q.event_name, q.status, q.reason, q.attempts, e.occurred_at, q.sent_at, q.created_at
		FROM event_destination_sends q JOIN customer_events e ON e.id = q.event_row_id
		WHERE q.workspace_id = $1 AND q.brand_id = $2 AND q.destination_id = $3 AND ($4 = '' OR q.status = $4)
		ORDER BY q.created_at DESC, q.id LIMIT $5`, a.WorkspaceID, a.Brand, cur.ID, status, limit)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (destinationSendView, error) {
		var v destinationSendView
		return v, r.Scan(&v.ID, &v.EventID, &v.EventName, &v.Status, &v.Reason, &v.Attempts, &v.OccurredAt, &v.SentAt, &v.CreatedAt)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}
