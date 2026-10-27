package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/inbox"
	"github.com/outflareai/outflare/engine/internal/webhooks"
)

// Webhooks: URLs a workspace registers to hear about events as they happen.
// The events come from the audit log; see the webhooks package.

// maxWebhooks is how many endpoints a workspace may have.
const maxWebhooks = 20

func (s *Server) routesWebhooks(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/webhook-events", s.listWebhookEvents)
	read, manage := auth.ScopeWebhooksRead, auth.ScopeWebhooksManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/events", s.inWorkspace(read, s.listEvents))
	mux.HandleFunc("GET /v1/workspaces/{ws}/webhooks", s.inWorkspace(read, s.listWebhooks))
	mux.HandleFunc("POST /v1/workspaces/{ws}/webhooks", s.inWorkspace(manage, s.createWebhook))
	mux.HandleFunc("GET /v1/workspaces/{ws}/webhooks/{id}", s.inWorkspace(read, s.getWebhook))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/webhooks/{id}", s.inWorkspace(manage, s.updateWebhook))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/webhooks/{id}", s.inWorkspace(manage, s.deleteWebhook))
	mux.HandleFunc("GET /v1/workspaces/{ws}/webhooks/{id}/secret", s.inWorkspace(manage, s.webhookSecret))
	mux.HandleFunc("POST /v1/workspaces/{ws}/webhooks/{id}/rotate-secret", s.inWorkspace(manage, s.rotateWebhookSecret))
	mux.HandleFunc("POST /v1/workspaces/{ws}/webhooks/{id}/test", s.inWorkspace(manage, s.testWebhook))
	mux.HandleFunc("GET /v1/workspaces/{ws}/webhooks/{id}/deliveries", s.inWorkspace(read, s.listWebhookDeliveries))
	mux.HandleFunc("POST /v1/workspaces/{ws}/webhooks/{id}/deliveries/{delivery}/redeliver", s.inWorkspace(manage, s.redeliverWebhook))
}

func (s *Server) listWebhookEvents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"data": webhooks.Catalog})
}

type webhookView struct {
	ID          string   `json:"id"`
	URL         string   `json:"url"`
	Description string   `json:"description"`
	Events      []string `json:"events"`
	// Reply labels this endpoint hears about; empty for all. Events
	// without a label are sent regardless.
	Categories     []string   `json:"categories"`
	Enabled        bool       `json:"enabled"`
	DisabledReason *string    `json:"disabled_reason"`
	FailingSince   *time.Time `json:"failing_since"`
	LastSuccessAt  *time.Time `json:"last_success_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	// Set for an endpoint that gets only one brand's events.
	BrandID *string `json:"brand_id"`
}

const webhookColumns = `id, url, description, events, categories, enabled, disabled_reason, failing_since, last_success_at, created_at, updated_at, brand_id::text`

func (s *Server) loadWebhooks(ctx context.Context, ws string, id *string) ([]webhookView, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+webhookColumns+` FROM webhook_endpoints
		WHERE workspace_id = $1 AND ($2::uuid IS NULL OR id = $2) ORDER BY created_at`, ws, id)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[webhookView])
	if list == nil {
		list = []webhookView{}
	}
	return list, err
}

func (s *Server) listWebhooks(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	list, err := s.loadWebhooks(r.Context(), a.WorkspaceID, nil)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// oneWebhook loads the endpoint in the path, or answers 404.
func (s *Server) oneWebhook(w http.ResponseWriter, r *http.Request) (webhookView, bool) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "webhook not found")
		return webhookView{}, false
	}
	list, err := s.loadWebhooks(r.Context(), a.WorkspaceID, &id)
	if err != nil {
		s.internal(w, r, err)
		return webhookView{}, false
	}
	if len(list) == 0 {
		writeError(w, r, http.StatusNotFound, "not_found", "webhook not found")
		return webhookView{}, false
	}
	return list[0], true
}

func (s *Server) getWebhook(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.oneWebhook(w, r); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

type webhookInput struct {
	URL         *string  `json:"url"`
	Description *string  `json:"description"`
	Events      []string `json:"events"`
	// Categories takes reply labels or groups (hot, later, closed,
	// automatic); groups are stored as their labels.
	Categories []string `json:"categories"`
	Enabled    *bool    `json:"enabled"`
	// One brand's id or handle for only its events; null or "" for every
	// brand's.
	Brand json.RawMessage `json:"brand"`
}

// brand resolves the input's brand: whether it was given, and the brand id
// (nil for every brand).
func (s *Server) webhookBrand(ctx context.Context, ws string, in *webhookInput) (bool, *string, error) {
	if len(in.Brand) == 0 {
		return false, nil, nil
	}
	var ref *string
	if err := json.Unmarshal(in.Brand, &ref); err != nil {
		return false, nil, errInvalid("brand must be a brand's id or handle, or null for every brand")
	}
	if ref == nil || strings.TrimSpace(*ref) == "" {
		return true, nil, nil
	}
	id, err := s.brandIDFor(ctx, ws, strings.ToLower(strings.TrimSpace(*ref)))
	if errors.Is(err, errNotFound) {
		return false, nil, errInvalid("brand not found: brand is a brand's id or handle, or null for every brand")
	}
	return true, &id, err
}

func (in *webhookInput) validate(allowPrivate bool) error {
	if in.URL != nil {
		*in.URL = strings.TrimSpace(*in.URL)
		if len(*in.URL) > 2048 {
			return errInvalid("url must be at most 2048 characters")
		}
		if err := webhooks.CheckURL(*in.URL, allowPrivate); err != nil {
			return errInvalid(err.Error())
		}
	}
	if in.Description != nil {
		*in.Description = strings.TrimSpace(*in.Description)
		if len(*in.Description) > 200 {
			return errInvalid("description must be at most 200 characters")
		}
	}
	if in.Events != nil {
		if len(in.Events) == 0 || len(in.Events) > 100 {
			return errInvalid(`events must list 1 to 100 event types; use ["*"] for every event`)
		}
		for i, e := range in.Events {
			in.Events[i] = strings.TrimSpace(e)
			if !webhooks.ValidPattern(in.Events[i]) {
				return errInvalid("unknown event type " + strconv.Quote(e) + "; GET /v1/webhook-events lists them, and \"lead.*\" or \"*\" match several")
			}
		}
		in.Events = slices.Compact(slices.Sorted(slices.Values(in.Events)))
	}
	if in.Categories != nil {
		for i := range in.Categories {
			in.Categories[i] = strings.TrimSpace(in.Categories[i])
		}
		cats, bad := inbox.Expand(in.Categories)
		if bad != "" {
			return errInvalid("unknown category " + strconv.Quote(bad) + "; use reply labels (" + strings.Join(inbox.Categories, ", ") +
				") or groups (" + strings.Join(inbox.GroupNames, ", ") + ")")
		}
		if cats == nil {
			cats = []string{}
		}
		in.Categories = cats
	}
	return nil
}

// webhookSecretView is an endpoint with its signing secret, shown when it is
// made, rotated or asked for.
type webhookSecretView struct {
	webhookView
	Secret string `json:"secret"`
}

func (s *Server) createWebhook(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req webhookInput
	if !decode(w, r, &req) {
		return
	}
	if req.URL == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "url is required")
		return
	}
	if req.Events == nil {
		req.Events = []string{"*"}
	}
	if err := req.validate(s.webhooks.AllowPrivate); err != nil {
		s.allocError(w, r, err)
		return
	}
	ctx := r.Context()
	_, brand, err := s.webhookBrand(ctx, a.WorkspaceID, &req)
	if s.allocError(w, r, err) {
		return
	}
	secret := webhooks.NewSecret()
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM webhook_endpoints WHERE workspace_id = $1`, a.WorkspaceID).Scan(&n); err != nil {
			return err
		}
		if n >= maxWebhooks {
			return errInvalid("a workspace can have at most 20 webhooks; one endpoint can subscribe to many events")
		}
		if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()`).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO webhook_endpoints (id, workspace_id, url, description, events, secret, enabled, created_by, brand_id, categories)
			VALUES ($1, $2, $3, $4, $5, $6, coalesce($7, true), $8, $9, coalesce($10::text[], '{}'))`,
			id, a.WorkspaceID, *req.URL, deref(req.Description), req.Events, s.box.Seal([]byte(secret), []byte(id)), req.Enabled, a.ActorID, brand, req.Categories); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "webhook.created", "webhook", id, map[string]any{"url": *req.URL, "events": req.Events, "brand_id": brand, "categories": req.Categories})
	})
	if s.allocError(w, r, err) {
		return
	}
	list, err := s.loadWebhooks(ctx, a.WorkspaceID, &id)
	if err != nil || len(list) == 0 {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, webhookSecretView{list[0], secret})
}

func (s *Server) updateWebhook(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.oneWebhook(w, r)
	if !ok {
		return
	}
	var req webhookInput
	if !decode(w, r, &req) {
		return
	}
	if err := req.validate(s.webhooks.AllowPrivate); err != nil {
		s.allocError(w, r, err)
		return
	}
	ctx := r.Context()
	setBrand, brand, err := s.webhookBrand(ctx, a.WorkspaceID, &req)
	if s.allocError(w, r, err) {
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Turning an endpoint back on clears why it was off and starts its
		// failure count afresh.
		if _, err := tx.Exec(ctx, `
			UPDATE webhook_endpoints SET url = coalesce($3, url), description = coalesce($4, description),
			       events = coalesce($5, events), enabled = coalesce($6, enabled), categories = coalesce($9::text[], categories),
			       disabled_reason = CASE WHEN $6 THEN NULL WHEN $6 = false THEN coalesce(disabled_reason, 'turned off by hand') ELSE disabled_reason END,
			       failing_since = CASE WHEN $6 AND NOT enabled THEN NULL ELSE failing_since END,
			       brand_id = CASE WHEN $7 THEN $8::uuid ELSE brand_id END,
			       updated_at = now()
			 WHERE id = $1 AND workspace_id = $2`,
			cur.ID, a.WorkspaceID, req.URL, req.Description, req.Events, req.Enabled, setBrand, brand, req.Categories); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "webhook.updated", "webhook", cur.ID, nil)
	})
	if s.allocError(w, r, err) {
		return
	}
	s.getWebhook(w, r)
}

func (s *Server) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.oneWebhook(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM webhook_endpoints WHERE id = $1 AND workspace_id = $2`, cur.ID, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "webhook.deleted", "webhook", cur.ID, map[string]any{"url": cur.URL})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) webhookSecret(w http.ResponseWriter, r *http.Request) {
	cur, ok := s.oneWebhook(w, r)
	if !ok {
		return
	}
	var sealed []byte
	if err := s.pool.QueryRow(r.Context(), `SELECT secret FROM webhook_endpoints WHERE id = $1`, cur.ID).Scan(&sealed); err != nil {
		s.internal(w, r, err)
		return
	}
	secret, err := s.box.Open(sealed, []byte(cur.ID))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, webhookSecretView{cur, string(secret)})
}

// rotateWebhookSecret makes a new signing secret. The old one keeps signing
// alongside it for a day, so receivers can switch without missing anything;
// expire_now: true drops it at once (for a leaked secret).
func (s *Server) rotateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.oneWebhook(w, r)
	if !ok {
		return
	}
	var req struct {
		ExpireNow bool `json:"expire_now"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	secret := webhooks.NewSecret()
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE webhook_endpoints SET previous_secret = CASE WHEN $3 THEN NULL ELSE secret END,
			       previous_secret_expires_at = CASE WHEN $3 THEN NULL ELSE now() + interval '24 hours' END,
			       secret = $2, updated_at = now()
			 WHERE id = $1`, cur.ID, s.box.Seal([]byte(secret), []byte(cur.ID)), req.ExpireNow); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "webhook.secret_rotated", "webhook", cur.ID, map[string]any{"expire_now": req.ExpireNow})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, webhookSecretView{cur, secret})
}

type webhookDeliveryView struct {
	ID             int64          `json:"id"`
	EventID        string         `json:"event_id"`
	EventType      string         `json:"event_type"`
	Status         string         `json:"status"`
	Attempts       int            `json:"attempts"`
	LastStatusCode *int           `json:"last_status_code"`
	LastError      *string        `json:"last_error"`
	LastAttemptAt  *time.Time     `json:"last_attempt_at"`
	DeliveredAt    *time.Time     `json:"delivered_at"`
	CreatedAt      time.Time      `json:"created_at"`
	Payload        map[string]any `json:"payload"`
}

const deliveryColumns = `id, event_id, event_type, status, attempts, last_status_code, last_error, last_attempt_at, delivered_at, created_at, payload`

func (s *Server) loadDelivery(ctx context.Context, endpointID string, id int64) (webhookDeliveryView, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+deliveryColumns+` FROM webhook_deliveries WHERE endpoint_id = $1 AND id = $2`, endpointID, id)
	if err != nil {
		return webhookDeliveryView{}, err
	}
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[webhookDeliveryView])
}

// listWebhookDeliveries returns an endpoint's deliveries newest first, for
// the last 30 days. Page with ?before=<id>&limit=<n>; filter with
// ?status= and ?event_type=.
func (s *Server) listWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	cur, ok := s.oneWebhook(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be 1 to 200")
			return
		}
		limit = n
	}
	var before int64 = 1<<63 - 1
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "before must be a delivery id")
			return
		}
		before = n
	}
	status := q.Get("status")
	if status != "" && !slices.Contains([]string{"pending", "delivered", "failed", "cancelled"}, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is pending, delivered, failed or cancelled")
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT `+deliveryColumns+` FROM webhook_deliveries
		WHERE endpoint_id = $1 AND id < $2 AND ($3 = '' OR status = $3) AND ($4 = '' OR event_type = $4)
		ORDER BY id DESC LIMIT $5`, cur.ID, before, status, q.Get("event_type"), limit)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[webhookDeliveryView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []webhookDeliveryView{}
	}
	var next *int64
	if len(list) == limit {
		next = &list[len(list)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

// testWebhook sends a webhook.test event now and answers with the delivery,
// so the result (status code or error) is in the answer.
func (s *Server) testWebhook(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.oneWebhook(w, r)
	if !ok {
		return
	}
	actor := webhooks.Ref{Type: a.ActorType, ID: &a.ActorID}
	id, err := s.webhooks.SendTest(r.Context(), a.WorkspaceID, cur.ID, actor)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	d, err := s.loadDelivery(r.Context(), cur.ID, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// redeliverWebhook sends a delivery again now and answers with the result.
func (s *Server) redeliverWebhook(w http.ResponseWriter, r *http.Request) {
	cur, ok := s.oneWebhook(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("delivery"), 10, 64)
	if err == nil {
		_, err = s.loadDelivery(r.Context(), cur.ID, id)
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, strconv.ErrSyntax) || errors.Is(err, strconv.ErrRange) {
			writeError(w, r, http.StatusNotFound, "not_found", "delivery not found")
			return
		}
		s.internal(w, r, err)
		return
	}
	if err := s.webhooks.Redeliver(r.Context(), id); err != nil {
		s.internal(w, r, err)
		return
	}
	d, err := s.loadDelivery(r.Context(), cur.ID, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// PruneWebhookDeliveries deletes deliveries older than 30 days.
func (s *Server) PruneWebhookDeliveries(ctx context.Context) (int64, error) {
	return s.webhooks.Prune(ctx)
}

// PruneInboundWebhookIDs forgets the ids of suppliers' webhook deliveries
// after 30 days. Suppliers retry for hours, not weeks, so that is long
// enough to drop every repeat; the table no longer grows forever.
func (s *Server) PruneInboundWebhookIDs(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM vendor_webhook_events WHERE received_at < now() - interval '30 days'`)
	return tag.RowsAffected(), err
}

// listEvents is the events feed: the same events webhooks send, for callers
// that cannot receive them (agents, scripts behind a firewall). Oldest
// first; poll with ?after=next_after. Events are kept as long as the audit
// log.
func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	limit := 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be 1 to 500")
			return
		}
		limit = n
	}
	var after int64
	if q.Get("after") == "latest" {
		// Start from now: no events, and next_after is the newest one, so
		// a listener only sees what happens from here on.
		var latest int64
		if err := s.pool.QueryRow(r.Context(), `SELECT coalesce(max(id), 0) FROM audit_events WHERE workspace_id = $1`, a.WorkspaceID).Scan(&latest); err != nil {
			s.internal(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": []webhooks.Envelope{}, "next_after": "evt_" + strconv.FormatInt(latest, 10), "has_more": false})
		return
	} else if v := strings.TrimPrefix(q.Get("after"), "evt_"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "after must be an event id (evt_...)")
			return
		}
		after = n
	}
	var patterns []string
	for _, t := range strings.Split(q.Get("types"), ",") {
		if t = strings.TrimSpace(t); t == "" {
			continue
		}
		if !webhooks.ValidPattern(t) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "unknown event type "+strconv.Quote(t)+"; GET /v1/webhook-events lists them")
			return
		}
		patterns = append(patterns, t)
	}
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, after, webhooks.Matching(patterns), limit}
	rows, err := s.pool.Query(r.Context(), `
		SELECT id, created_at, actor_type, actor_id::text, action, target_type, target_id, request_id, data, brand_id::text
		  FROM audit_events WHERE workspace_id = $1 AND id > $2 AND action = ANY($3)`+sel.sql("brand_id", &args)+`
		 ORDER BY id LIMIT $4`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	defer rows.Close()
	list := []webhooks.Envelope{}
	var last int64
	for rows.Next() {
		var (
			id                             int64
			at                             time.Time
			actorType, action              string
			actorID, tType, tID, requestID *string
			data                           map[string]any
			brand                          *string
		)
		if err := rows.Scan(&id, &at, &actorType, &actorID, &action, &tType, &tID, &requestID, &data, &brand); err != nil {
			s.internal(w, r, err)
			return
		}
		ev := webhooks.FromAudit(id, at, a.WorkspaceID, actorType, actorID, action, tType, tID, requestID, data)
		ev.BrandID = brand
		list = append(list, ev)
		last = id
	}
	if err := rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	// next_after is always set, so a caller can keep polling from it; with
	// nothing new it is the cursor it sent.
	next := "evt_" + strconv.FormatInt(max(last, after), 10)
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_after": next, "has_more": len(list) == limit})
}
