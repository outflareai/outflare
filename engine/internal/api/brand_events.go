package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Events, kept (plans/brands/README.md): a brand's product events are
// stored first and processed in the background. POST
// /v1/brands/{brand}/events checks only the envelope, saves every event as
// sent in event_inbox and answers 202 with a receipt per event. The
// events.process job then runs each through the same ingest as the
// workspace route (ingestOne), in the order they arrived, and keeps what
// happened on the row:
//
//   - accepted: stored, with its person and customer event
//   - duplicate: a messageId the brand already has
//   - rejected: a code and what to fix; fixable and replayable
//   - held: a track name the catalog hasn't approved; stored here but
//     driving nothing until someone approves it and replays it
//
// Accepted and duplicate rows are kept 30 days, rejected and held 90.

const (
	jobEventsProcess = "events.process"
	// eventInboxChunk is how many events one transaction processes.
	eventInboxChunk = 100
	// eventInboxAttempts is how often an event may fail for a reason other
	// than itself (a database error) before it is rejected as internal.
	eventInboxAttempts = 5
	// eventInboxBudget is how long one job processes before handing the
	// rest to a new job, so a big backlog never holds a worker for long.
	eventInboxBudget = 30 * time.Second
	// maxReplay is how many events one replay call resets.
	maxReplay = 10000
)

var (
	inboxStatuses = []string{"pending", "accepted", "duplicate", "rejected", "held"}
	// How long each status is kept, in days.
	inboxRetention = map[string]int{"accepted": 30, "duplicate": 30, "rejected": 90, "held": 90}
)

func (s *Server) routesBrandEvents(mux *http.ServeMux) {
	read, manage := auth.ScopeCustomersRead, auth.ScopeCustomersManage
	mux.HandleFunc("POST /v1/brands/{brand}/events", s.onBrand(manage, s.ingestBrandEvents))
	mux.HandleFunc("GET /v1/brands/{brand}/events", s.onBrand(read, s.listBrandEvents))
	mux.HandleFunc("GET /v1/brands/{brand}/events/summary", s.onBrand(read, s.brandEventsSummary))
	mux.HandleFunc("GET /v1/brands/{brand}/events/{event}", s.onBrand(read, s.getBrandEvent))
	mux.HandleFunc("POST /v1/brands/{brand}/events/replay", s.onBrand(manage, s.replayBrandEvents))
}

// ---- Receiving ----

type eventReceipt struct {
	Index int    `json:"index"`
	ID    string `json:"id"`
}

func (s *Server) ingestBrandEvents(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	env, ok := s.readEventEnvelope(w, r)
	if !ok {
		return
	}
	// Each event as sent, with the batch's context under its own. Anything
	// that isn't an object is kept as is and rejected by the job, so the
	// sender can see it.
	raws := make([]string, len(env.items))
	messageIDs := make([]*string, len(env.items))
	positions := make([]int32, len(env.items))
	for i, item := range env.items {
		positions[i] = int32(i)
		raws[i] = string(item)
		var m eventMessage
		if json.Unmarshal(item, &m) != nil {
			continue
		}
		if id, err := m.id("messageId", "message_id"); err == nil && id != "" {
			messageIDs[i] = &id
		}
		if len(env.shared) == 0 {
			continue
		}
		own := map[string]any{}
		_ = json.Unmarshal(m.get("context"), &own)
		for k, v := range env.shared {
			if _, set := own[k]; !set {
				own[k] = v
			}
		}
		b, err := json.Marshal(own)
		if err != nil {
			continue
		}
		m["context"] = b
		if b, err := json.Marshal(m); err == nil {
			raws[i] = string(b)
		}
	}
	var batch string
	receipts := make([]eventReceipt, len(env.items))
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&batch); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `INSERT INTO event_inbox (workspace_id, brand_id, batch_id, position, source, message_id, raw,
				request_id, token_id, actor_id, actor_type, user_id)
			SELECT $1, $2, $3, p, $4, m, r::jsonb, $8, $9, $10, $11, $12
			FROM unnest($5::int[], $6::text[], $7::text[]) AS t (p, m, r)
			RETURNING position, id::text`,
			a.WorkspaceID, a.Brand, batch, env.source, positions, messageIDs, raws,
			nullIfEmpty(RequestID(ctx)), nullIfEmpty(a.TokenID), nullIfEmpty(a.ActorID), a.ActorType, nullIfEmpty(a.personID()))
		if err != nil {
			return err
		}
		for rows.Next() {
			var p int
			var id string
			if err := rows.Scan(&p, &id); err != nil {
				return err
			}
			receipts[p] = eventReceipt{Index: p, ID: id}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobEventsProcess, WorkspaceID: &a.WorkspaceID,
			Payload: map[string]string{"brand_id": a.Brand}, IdempotencyKey: "batch:" + batch})
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	metrics.EventInboxReceived(len(receipts))
	writeJSON(w, http.StatusAccepted, map[string]any{"batch_id": batch, "received": len(receipts), "events": receipts})
}

// ---- Processing ----

func (s *Server) eventsProcessJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		BrandID string `json:"brand_id"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || !validUUID(p.BrandID) {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	return s.processEventInbox(ctx, *job.WorkspaceID, p.BrandID)
}

// processEventInbox processes a brand's pending events, a chunk at a time,
// until none are left or its time is up; then a new job carries on.
func (s *Server) processEventInbox(ctx context.Context, ws, brand string) error {
	deadline := time.Now().Add(eventInboxBudget)
	for {
		n, err := s.processInboxChunk(ctx, ws, brand)
		if err != nil || n == 0 {
			return err
		}
		if time.Now().After(deadline) {
			_, err := jobs.Enqueue(ctx, s.pool, jobs.NewJob{Kind: jobEventsProcess, WorkspaceID: &ws,
				Payload: map[string]string{"brand_id": brand}, IdempotencyKey: "more:" + brand + ":" + strconv.FormatInt(time.Now().UnixNano(), 36)})
			return err
		}
	}
}

type inboxRow struct {
	id, batch, source string
	raw               json.RawMessage
	attempts          int
	requestID         *string
	tokenID, actorID  *string
	actorType         string
	userID            *string
}

// principal is who sent the row, so what it changes is recorded as theirs.
func (row inboxRow) principal() principal {
	p := principal{ActorType: row.actorType}
	if row.actorID != nil {
		p.ActorID = *row.actorID
	}
	if row.tokenID != nil {
		p.TokenID = *row.tokenID
	}
	if row.userID != nil {
		p.UserID = *row.userID
	}
	return p
}

// inboxOutcome is what processing one row decided.
type inboxOutcome struct {
	status, code, reason, name string
	record, event              *string
	took                       time.Duration
}

// errInboxRetry leaves the rest of a chunk pending for the job's next
// attempt.
var errInboxRetry = errors.New("an event could not be processed; it stays pending for the next attempt")

// processInboxChunk processes up to eventInboxChunk of a brand's pending
// events in one transaction, holding the brand's lock so its events are
// processed one at a time, in order. It returns how many it finished.
func (s *Server) processInboxChunk(ctx context.Context, ws, brand string) (int, error) {
	var outcomes []inboxOutcome
	retry := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		outcomes, retry = nil, false
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('event_inbox:' || $1, 0))`, brand); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text, batch_id::text, source, raw, attempts, request_id, token_id::text, actor_id::text, actor_type, user_id::text
			FROM event_inbox WHERE brand_id = $1 AND workspace_id = $2 AND status = 'pending'
			ORDER BY received_at, batch_id, position LIMIT $3 FOR UPDATE`, brand, ws, eventInboxChunk)
		if err != nil {
			return err
		}
		list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (inboxRow, error) {
			var x inboxRow
			err := r.Scan(&x.id, &x.batch, &x.source, &x.raw, &x.attempts, &x.requestID, &x.tokenID, &x.actorID, &x.actorType, &x.userID)
			return x, err
		})
		if err != nil || len(list) == 0 {
			return err
		}
		c, err := loadCatalog(ctx, tx, ws, false)
		if err != nil {
			return err
		}
		defs, err := inboxDefinitions(ctx, tx, ws, list)
		if err != nil {
			return err
		}
		type batchState struct {
			first  inboxRow
			totals ingestTotals
			counts map[string]int
		}
		var order []string
		batches := map[string]*batchState{}
		now := time.Now().UTC()
		for _, row := range list {
			b := batches[row.batch]
			if b == nil {
				b = &batchState{first: row, totals: ingestTotals{names: map[string]*seenName{}}, counts: map[string]int{}}
				batches[row.batch] = b
				order = append(order, row.batch)
			}
			start := time.Now()
			out, err := s.processInboxRow(ctx, tx, ws, brand, c, defs, row, now, &b.totals)
			if err != nil {
				if ctx.Err() != nil {
					return err
				}
				// Not the event's fault: it stays pending for the next
				// attempt, and after eventInboxAttempts is rejected so it
				// never blocks the brand's later events.
				ev := track.Event{Name: "customer_events.process_failed", WorkspaceID: ws, ActorType: "system", Surface: "job",
					Props: map[string]any{"brand_id": brand, "attempts": row.attempts + 1}}
				track.Fail(ctx, s.log, ev, "internal", fmt.Errorf("event inbox %s: %w", row.id, err))
				if row.attempts+1 < eventInboxAttempts {
					if _, err := tx.Exec(ctx, `UPDATE event_inbox SET attempts = attempts + 1 WHERE id = $1`, row.id); err != nil {
						return err
					}
					retry = true
					break
				}
				out = inboxOutcome{status: "rejected", code: "internal", took: time.Since(start),
					reason: "Outflare couldn't process this event after " + strconv.Itoa(eventInboxAttempts) + " tries; replay it, and if it fails again tell support with this event's id"}
			}
			if _, err := tx.Exec(ctx, `UPDATE event_inbox SET status = $2, code = $3, reason = $4, name = $5, record_id = $6, customer_event_id = $7,
					attempts = attempts + CASE WHEN $3 = 'internal' THEN 1 ELSE 0 END, processed_at = now(),
					purge_after = received_at + make_interval(days => $8) WHERE id = $1`,
				row.id, out.status, nullIfEmpty(out.code), nullIfEmpty(out.reason), nullIfEmpty(out.name), out.record, out.event, inboxRetention[out.status]); err != nil {
				return err
			}
			b.counts[out.status]++
			if out.code != "" {
				b.counts["code:"+out.code]++
			}
			outcomes = append(outcomes, out)
		}
		for _, id := range order {
			b := batches[id]
			if err := saveEventNames(ctx, tx, ws, b.totals.names); err != nil {
				return err
			}
			if len(b.counts) == 0 {
				continue
			}
			data := map[string]any{"batch_id": id, "source_kind": sourceKind(b.first.source), "accepted": b.counts["accepted"], "duplicate": b.counts["duplicate"],
				"rejected": b.counts["rejected"], "held": b.counts["held"], "events": b.counts["accepted"] + b.counts["duplicate"] + b.counts["rejected"] + b.counts["held"],
				"records_created": b.totals.created, "identities_added": b.totals.identities, "possible_duplicates": b.totals.duplicates,
				"records_merged": b.totals.merged, "names": len(b.totals.names)}
			codes := map[string]any{}
			for k, n := range b.counts {
				if code, ok := strings.CutPrefix(k, "code:"); ok {
					codes[code] = n
				}
			}
			if len(codes) > 0 {
				data["codes"] = codes
			}
			p := b.first.principal()
			rctx := ctx
			if b.first.requestID != nil {
				rctx = context.WithValue(ctx, requestIDKey, *b.first.requestID)
			}
			if err := record(rctx, tx, &p, ws, "customer_events.ingested", "brand", brand, data); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, o := range outcomes {
		metrics.EventInboxProcessed(o.status, o.code, o.took)
	}
	if retry {
		return len(outcomes), errInboxRetry
	}
	return len(outcomes), nil
}

// sourceKind is a source label as an analytics property: the common
// libraries by name, anything else as other, since a label is the
// workspace's own text.
func sourceKind(source string) string {
	switch source {
	case "api", "segment", "rudderstack", "mparticle", "posthog", "server", "backend", "app", "web", "ios", "android":
		return source
	}
	return "other"
}

// eventDef is the catalog's say on one track name.
type eventDef struct {
	status string
	props  []eventProperty
}

// inboxDefinitions reads the catalog entries for the track names in rows.
func inboxDefinitions(ctx context.Context, tx pgx.Tx, ws string, rows []inboxRow) (map[string]eventDef, error) {
	var names []string
	for _, row := range rows {
		var m eventMessage
		if json.Unmarshal(row.raw, &m) != nil || strings.ToLower(m.text("type")) != "track" {
			continue
		}
		if n, ok := eventNameFor(m.text("event")); ok && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	out := map[string]eventDef{}
	if len(names) == 0 {
		return out, nil
	}
	q, err := tx.Query(ctx, `SELECT name, status, properties FROM event_definitions WHERE workspace_id = $1 AND name = ANY($2)`, ws, names)
	if err != nil {
		return nil, err
	}
	defer q.Close()
	for q.Next() {
		var name string
		var d eventDef
		if err := q.Scan(&name, &d.status, &d.props); err != nil {
			return nil, err
		}
		out[name] = d
	}
	return out, q.Err()
}

// processInboxRow runs one event through the ingest in a savepoint. An
// error is one the event isn't to blame for; the savepoint is gone then.
func (s *Server) processInboxRow(ctx context.Context, tx pgx.Tx, ws, brand string, c *catalog, defs map[string]eventDef,
	row inboxRow, now time.Time, totals *ingestTotals) (inboxOutcome, error) {
	start := time.Now()
	reject := func(code, reason string) (inboxOutcome, error) {
		return inboxOutcome{status: "rejected", code: code, reason: reason, took: time.Since(start)}, nil
	}
	var m eventMessage
	if json.Unmarshal(row.raw, &m) != nil {
		return reject("invalid_event", `each event is a JSON object, such as {"type": "track", "userId": "u_123", "event": "Order Completed"}`)
	}
	// A track name the catalog hasn't approved is held, before it changes
	// anyone: it waits here until someone approves it and replays it.
	if strings.ToLower(m.text("type")) == "track" && (m.get("userId", "user_id") != nil || m.get("anonymousId", "anonymous_id") != nil) {
		if name, ok := eventNameFor(m.text("event")); ok {
			def, known := defs[name]
			if !known || def.status == "quarantined" {
				n := totals.names[name]
				if n == nil {
					n = &seenName{kind: "track", first: now, last: now}
					totals.names[name] = n
				}
				n.count++
				return inboxOutcome{status: "held", code: "name_not_approved", name: name, took: time.Since(start),
					reason: name + " is a new event name, held until someone approves it in the catalog; approve it, then replay this event"}, nil
			}
			if props, err := m.object(maxEventProperties, "properties"); err == nil {
				if key, want := propertyMismatch(def.props, props); key != "" {
					return reject("property_type", fmt.Sprintf("property %s must be %s, as the catalog defines it for %s; send %s, or change the property's type in the catalog, then replay this event",
						key, want, name, want))
				}
			}
		}
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return inboxOutcome{}, err
	}
	p := row.principal()
	a := &access{principal: p, WorkspaceID: ws, Brands: []string{brand}, Brand: brand}
	rctx := ctx
	if row.requestID != nil {
		rctx = context.WithValue(ctx, requestIDKey, *row.requestID)
	}
	before := *totals
	res := eventResult{}
	b := brand
	err = s.ingestOne(rctx, sp, a, c, &b, row.source, nil, m, now, &res, totals)
	_, code, msg, rejected := eventRejection(err)
	switch {
	case err == nil && res.Status == "duplicate":
		if err := sp.Rollback(ctx); err != nil {
			return inboxOutcome{}, err
		}
		return inboxOutcome{status: "duplicate", name: res.Name, took: time.Since(start),
			reason: "an event with this messageId was already stored, so this one was not stored again"}, nil
	case err == nil:
		if err := sp.Commit(ctx); err != nil {
			return inboxOutcome{}, err
		}
		return inboxOutcome{status: "accepted", name: res.Name, record: res.RecordID, event: res.EventID, took: time.Since(start)}, nil
	case rejected:
		totals.created, totals.identities, totals.duplicates, totals.merged = before.created, before.identities, before.duplicates, before.merged
		if err := sp.Rollback(ctx); err != nil {
			return inboxOutcome{}, err
		}
		if code == "other_brand" {
			msg = "this person belongs to another brand, and brands keep their data apart; send the event to that brand's events, or use another userId"
		}
		out, _ := reject(code, msg)
		out.name = res.Name
		return out, nil
	}
	if rbErr := sp.Rollback(ctx); rbErr != nil {
		return inboxOutcome{}, errors.Join(err, rbErr)
	}
	return inboxOutcome{}, err
}

// propertyMismatch finds a property whose value isn't the type the
// catalog defines for it, and says what it must be.
func propertyMismatch(defs []eventProperty, props map[string]any) (key, want string) {
	for _, d := range defs {
		v, ok := props[d.Key]
		if !ok || v == nil {
			continue
		}
		switch d.ValueType {
		case "number", "currency", "rating":
			if _, ok := v.(float64); !ok {
				return d.Key, "a number"
			}
		case "checkbox":
			if _, ok := v.(bool); !ok {
				return d.Key, "true or false"
			}
		case "text", "select", "status", "email", "phone", "url":
			if _, ok := v.(string); !ok {
				return d.Key, "text"
			}
		case "timestamp":
			if s, ok := v.(string); !ok || func() bool { _, err := time.Parse(time.RFC3339Nano, s); return err != nil }() {
				return d.Key, "an RFC 3339 time, such as 2026-10-26T09:30:00Z"
			}
		case "date":
			if s, ok := v.(string); !ok || func() bool { _, err := time.Parse(time.DateOnly, s); return err != nil }() {
				return d.Key, "a date, such as 2026-10-26"
			}
		}
	}
	return "", ""
}

// ---- Reading ----

type inboxEventView struct {
	ID              string     `json:"id"`
	BatchID         string     `json:"batch_id"`
	ReceivedAt      time.Time  `json:"received_at"`
	Status          string     `json:"status"`
	Code            *string    `json:"code"`
	Reason          *string    `json:"reason"`
	Type            *string    `json:"type"`
	Name            *string    `json:"name"`
	Source          string     `json:"source"`
	MessageID       *string    `json:"message_id"`
	RecordID        *string    `json:"record_id"`
	CustomerEventID *string    `json:"customer_event_id"`
	Attempts        int        `json:"attempts"`
	ProcessedAt     *time.Time `json:"processed_at"`
	ExpiresAt       *time.Time `json:"expires_at"`
}

const inboxEventColumns = `e.id::text, e.batch_id::text, e.received_at, e.status, e.code, e.reason, lower(e.raw->>'type'),
	COALESCE(e.name, e.raw->>'event', e.raw->>'name'), e.source, e.message_id, e.record_id::text, e.customer_event_id::text,
	e.attempts, e.processed_at, e.purge_after`

func scanInboxEvent(row pgx.CollectableRow) (inboxEventView, error) {
	var v inboxEventView
	err := row.Scan(&v.ID, &v.BatchID, &v.ReceivedAt, &v.Status, &v.Code, &v.Reason, &v.Type, &v.Name, &v.Source, &v.MessageID,
		&v.RecordID, &v.CustomerEventID, &v.Attempts, &v.ProcessedAt, &v.ExpiresAt)
	return v, err
}

// inboxFilter adds the filters the list and replay share to where.
func inboxFilter(status, code, source, batch, name string, where *[]string, args *[]any) error {
	add := func(cond string, v any) {
		*args = append(*args, v)
		*where = append(*where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(*args))))
	}
	if status != "" {
		if !slices.Contains(inboxStatuses, status) {
			return errInvalid("status is pending, accepted, duplicate, rejected or held")
		}
		add("e.status = ?", status)
	}
	if code != "" {
		add("e.code = ?", code)
	}
	if source != "" {
		add("e.source = ?", source)
	}
	if batch != "" {
		if !validUUID(batch) {
			return errInvalid("batch_id is the batch_id a send answered")
		}
		add("e.batch_id = ?", batch)
	}
	if name != "" {
		add("COALESCE(e.name, e.raw->>'event', e.raw->>'name') = ?", name)
	}
	return nil
}

func (s *Server) listBrandEvents(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	q := r.URL.Query()
	limit, err := limitParam(r, 50, 200)
	if s.allocError(w, r, err) {
		return
	}
	beforeAt, beforeID, err := timeCursor(q.Get("before"))
	if s.allocError(w, r, err) {
		return
	}
	where := []string{"e.brand_id = $1", "e.workspace_id = $2", "(e.received_at, e.id::text) < ($3, $4)"}
	args := []any{a.Brand, a.WorkspaceID, beforeAt, beforeID}
	if err := inboxFilter(q.Get("status"), q.Get("code"), q.Get("source"), q.Get("batch_id"), q.Get("name"), &where, &args); s.allocError(w, r, err) {
		return
	}
	for param, op := range map[string]string{"received_after": ">=", "received_before": "<"} {
		if v := q.Get(param); v != "" {
			t, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				writeError(w, r, http.StatusBadRequest, "invalid_request", param+" must be an RFC 3339 time")
				return
			}
			args = append(args, t)
			where = append(where, "e.received_at "+op+" $"+strconv.Itoa(len(args)))
		}
	}
	args = append(args, limit)
	var list []inboxEventView
	err = s.readTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+inboxEventColumns+` FROM event_inbox e WHERE `+strings.Join(where, " AND ")+`
			ORDER BY e.received_at DESC, e.id::text DESC LIMIT $`+strconv.Itoa(len(args)), args...)
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, scanInboxEvent)
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	if list == nil {
		list = []inboxEventView{}
	}
	var next *string
	if len(list) == limit {
		last := list[len(list)-1]
		v := last.ReceivedAt.Format(time.RFC3339Nano) + "|" + last.ID
		next = &v
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

func (s *Server) getBrandEvent(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	id := r.PathValue("event")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "event not found")
		return
	}
	var out struct {
		inboxEventView
		Raw json.RawMessage `json:"raw"`
	}
	err := s.pool.QueryRow(ctx, `SELECT `+inboxEventColumns+`, e.raw FROM event_inbox e WHERE e.id = $1 AND e.brand_id = $2 AND e.workspace_id = $3`,
		id, a.Brand, a.WorkspaceID).Scan(&out.ID, &out.BatchID, &out.ReceivedAt, &out.Status, &out.Code, &out.Reason, &out.Type, &out.Name, &out.Source,
		&out.MessageID, &out.RecordID, &out.CustomerEventID, &out.Attempts, &out.ProcessedAt, &out.ExpiresAt, &out.Raw)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "event not found")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type inboxCounts struct {
	Pending   int `json:"pending"`
	Accepted  int `json:"accepted"`
	Duplicate int `json:"duplicate"`
	Rejected  int `json:"rejected"`
	Held      int `json:"held"`
}

func (c *inboxCounts) add(status string, n int) {
	switch status {
	case "pending":
		c.Pending += n
	case "accepted":
		c.Accepted += n
	case "duplicate":
		c.Duplicate += n
	case "rejected":
		c.Rejected += n
	case "held":
		c.Held += n
	}
}

type inboxCodeCount struct {
	Code      string     `json:"code"`
	Count     int        `json:"count"`
	Reason    string     `json:"reason"`
	ExpiresAt *time.Time `json:"expires_at"`
}

type inboxNameCount struct {
	Name      string     `json:"name"`
	Count     int        `json:"count"`
	ExpiresAt *time.Time `json:"expires_at"`
}

func (s *Server) brandEventsSummary(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var out struct {
		Day             inboxCounts      `json:"last_24h"`
		Week            inboxCounts      `json:"last_7d"`
		Pending         int              `json:"pending"`
		OldestPendingAt *time.Time       `json:"oldest_pending_at"`
		Codes           []inboxCodeCount `json:"rejection_codes"`
		HeldNames       []inboxNameCount `json:"held_names"`
		RetentionDays   map[string]int   `json:"retention_days"`
	}
	out.RetentionDays = inboxRetention
	err := s.readTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT status, count(*) FILTER (WHERE received_at > now() - interval '1 day'), count(*)
			FROM event_inbox WHERE brand_id = $1 AND workspace_id = $2 AND received_at > now() - interval '7 days' GROUP BY status`, a.Brand, a.WorkspaceID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var status string
			var day, week int
			if err := rows.Scan(&status, &day, &week); err != nil {
				return err
			}
			out.Day.add(status, day)
			out.Week.add(status, week)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*), min(received_at) FROM event_inbox WHERE brand_id = $1 AND workspace_id = $2 AND status = 'pending'`,
			a.Brand, a.WorkspaceID).Scan(&out.Pending, &out.OldestPendingAt); err != nil {
			return err
		}
		// Every rejection still kept, by code, with its latest reason and
		// when the first of them goes.
		rows, err = tx.Query(ctx, `SELECT code, count(*), (array_agg(reason ORDER BY received_at DESC))[1], min(purge_after)
			FROM event_inbox WHERE brand_id = $1 AND workspace_id = $2 AND status = 'rejected' AND code IS NOT NULL
			GROUP BY code ORDER BY count(*) DESC, code LIMIT 10`, a.Brand, a.WorkspaceID)
		if err != nil {
			return err
		}
		if out.Codes, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (inboxCodeCount, error) {
			var c inboxCodeCount
			var reason *string
			err := r.Scan(&c.Code, &c.Count, &reason, &c.ExpiresAt)
			if reason != nil {
				c.Reason = *reason
			}
			return c, err
		}); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT name, count(*), min(purge_after) FROM event_inbox
			WHERE brand_id = $1 AND workspace_id = $2 AND status = 'held' AND name IS NOT NULL
			GROUP BY name ORDER BY count(*) DESC, name LIMIT 20`, a.Brand, a.WorkspaceID)
		if err != nil {
			return err
		}
		out.HeldNames, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (inboxNameCount, error) {
			var n inboxNameCount
			return n, r.Scan(&n.Name, &n.Count, &n.ExpiresAt)
		})
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	if out.Codes == nil {
		out.Codes = []inboxCodeCount{}
	}
	if out.HeldNames == nil {
		out.HeldNames = []inboxNameCount{}
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- Replay ----

type replayInput struct {
	IDs     []string `json:"ids"`
	Status  string   `json:"status"`
	Code    string   `json:"code"`
	Source  string   `json:"source"`
	BatchID string   `json:"batch_id"`
	Name    string   `json:"name"`
}

func (s *Server) replayBrandEvents(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var in replayInput
	if !decode(w, r, &in) {
		return
	}
	where := []string{"e.brand_id = $1", "e.workspace_id = $2", "e.status IN ('rejected', 'held')"}
	args := []any{a.Brand, a.WorkspaceID}
	switch {
	case len(in.IDs) > 0:
		if len(in.IDs) > 1000 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "replay at most 1000 ids at a time, or use a filter")
			return
		}
		for _, id := range in.IDs {
			if !validUUID(id) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "ids are event ids, as sending or listing them answered")
				return
			}
		}
		if in.Status != "" || in.Code != "" || in.Source != "" || in.BatchID != "" || in.Name != "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "send ids or a filter, not both")
			return
		}
		args = append(args, in.IDs)
		where = append(where, "e.id = ANY($3::uuid[])")
	case in.Status == "" && in.Code == "" && in.Source == "" && in.BatchID == "" && in.Name == "":
		writeError(w, r, http.StatusBadRequest, "invalid_request", `send ids, or a filter such as {"status": "rejected", "code": "missing_id"}`)
		return
	default:
		if in.Status != "" && in.Status != "rejected" && in.Status != "held" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "only rejected and held events can be replayed: status is rejected or held")
			return
		}
		if err := inboxFilter(in.Status, in.Code, in.Source, in.BatchID, in.Name, &where, &args); s.allocError(w, r, err) {
			return
		}
	}
	args = append(args, maxReplay)
	replayed := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE event_inbox SET status = 'pending', code = NULL, reason = NULL, attempts = 0, processed_at = NULL, purge_after = NULL
			WHERE id IN (SELECT e.id FROM event_inbox e WHERE `+strings.Join(where, " AND ")+`
				ORDER BY e.received_at, e.position LIMIT $`+strconv.Itoa(len(args))+` FOR UPDATE)`, args...)
		if err != nil {
			return err
		}
		replayed = int(tag.RowsAffected())
		if replayed == 0 {
			return nil
		}
		var key string
		if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&key); err != nil {
			return err
		}
		if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobEventsProcess, WorkspaceID: &a.WorkspaceID,
			Payload: map[string]string{"brand_id": a.Brand}, IdempotencyKey: "replay:" + key}); err != nil {
			return err
		}
		data := map[string]any{"replayed": replayed, "by_ids": len(in.IDs) > 0}
		if in.Status != "" {
			data["status"] = in.Status
		}
		if in.Code != "" {
			data["code"] = in.Code
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "customer_events.replayed", "brand", a.Brand, data)
	})
	if s.allocError(w, r, err) {
		return
	}
	skipped := 0
	if len(in.IDs) > 0 {
		skipped = len(in.IDs) - replayed
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"replayed": replayed, "skipped": skipped, "more": replayed == maxReplay})
}

// ---- Keeping it moving ----

// RunEventInbox keeps the event inbox healthy until ctx ends: it reports
// how many events wait and how long, queues processing for brands whose
// events have waited too long (a job lost or out of retries), and deletes
// rows at the end of their retention.
func (s *Server) RunEventInbox(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	lastPurge := time.Time{}
	for {
		if err := s.eventInboxRound(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.log, track.Event{Name: "customer_events.sweep_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		if time.Since(lastPurge) > 10*time.Minute {
			lastPurge = time.Now()
			if n, err := s.PruneEventInbox(ctx); err != nil && ctx.Err() == nil {
				track.Fail(ctx, s.log, track.Event{Name: "customer_events.purge_failed", ActorType: "system", Surface: "system"}, "internal", err)
			} else if n > 0 {
				s.log.Info("event inbox: deleted rows past their retention", "deleted", n)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) eventInboxRound(ctx context.Context) error {
	var pending int
	var oldest, overdue *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM event_inbox WHERE status = 'pending'),
			(SELECT min(received_at) FROM event_inbox WHERE status = 'pending'),
			(SELECT min(purge_after) FROM event_inbox WHERE purge_after < now())`).Scan(&pending, &oldest, &overdue); err != nil {
		return err
	}
	var age, late time.Duration
	if oldest != nil {
		age = time.Since(*oldest)
	}
	if overdue != nil {
		late = time.Since(*overdue)
	}
	metrics.SetEventInboxPending(pending, age)
	metrics.SetEventInboxPurgeOverdue(late)
	if oldest == nil || age < 2*time.Minute {
		return nil
	}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT workspace_id::text, brand_id::text FROM event_inbox
		WHERE status = 'pending' AND received_at < now() - interval '2 minutes' LIMIT 100`)
	if err != nil {
		return err
	}
	type due struct{ ws, brand string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (due, error) {
		var d due
		return d, r.Scan(&d.ws, &d.brand)
	})
	if err != nil {
		return err
	}
	// One job per brand per five minutes, however often this runs.
	slot := strconv.FormatInt(time.Now().Unix()/300, 10)
	for _, d := range list {
		if _, err := jobs.Enqueue(ctx, s.pool, jobs.NewJob{Kind: jobEventsProcess, WorkspaceID: &d.ws,
			Payload: map[string]string{"brand_id": d.brand}, IdempotencyKey: "stale:" + d.brand + ":" + slot}); err != nil {
			return err
		}
	}
	return nil
}

// PruneEventInbox deletes event inbox rows past their retention, a batch
// at a time, and returns how many it deleted.
func (s *Server) PruneEventInbox(ctx context.Context) (int, error) {
	total := 0
	for range 50 {
		tag, err := s.pool.Exec(ctx, `DELETE FROM event_inbox WHERE id IN (
			SELECT id FROM event_inbox WHERE purge_after < now() ORDER BY purge_after LIMIT 5000)`)
		if err != nil {
			return total, err
		}
		n := int(tag.RowsAffected())
		total += n
		metrics.EventInboxPurged(n)
		if n < 5000 {
			break
		}
	}
	return total, nil
}
