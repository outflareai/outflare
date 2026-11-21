// Package destinations forwards a brand's customer events, as they
// arrive, to the tools the brand already uses: a webhook of its own,
// Slack, Google Analytics 4, HubSpot, and a table in its BigQuery or
// Snowflake. Every destination runs on the workspace's own connection or
// key; Outflare holds none of its own.
//
// Each event a destination takes is a row in event_destination_sends,
// sent, skipped or failed with the reason, so a person can see exactly
// what reached each tool. Conversions for ad platforms (Meta, Google Ads)
// are engine/internal/adconv, which also checks ad consent.
package destinations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/secrets"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/webhooks"
)

const (
	JobSend = "destinations.send"
	// JobTest sends one made-up event to a destination, so a person can
	// see it works before real events arrive.
	JobTest = "destinations.test"
	// every is how often new events are matched to destinations.
	every = time.Minute
	// settle is how old an event must be before it is matched, so one
	// still being written is not passed over.
	settle = 10 * time.Second
	// batch is the most events one round of a job sends; rounds is how
	// many rounds one job runs before it leaves the rest to the next.
	batch  = 200
	rounds = 10
	// maxAttempts is how often an event is tried when the tool cannot be
	// reached before it is marked failed.
	maxAttempts = 5
	// ga4Collect is Google Analytics' Measurement Protocol endpoint.
	ga4Collect = "https://www.google-analytics.com/mp/collect"
)

// Kinds are the destinations there are, in the order they are listed.
var Kinds = []string{"webhook", "slack", "ga4", "hubspot", "bigquery", "snowflake"}

// Config is what a destination needs besides its events; which fields
// apply depends on the kind.
type Config struct {
	// webhook
	URL string `json:"url,omitempty"`
	// slack: the channel's id and, for showing, its name.
	Channel     string `json:"channel,omitempty"`
	ChannelName string `json:"channel_name,omitempty"`
	// ga4: the web stream's measurement id (G-XXXXXXX).
	MeasurementID string `json:"measurement_id,omitempty"`
	// hubspot: contact properties to set when the event happens, by
	// HubSpot's internal property name.
	Properties map[string]string `json:"properties,omitempty"`
	// bigquery: the Google Cloud project and dataset; snowflake: the
	// account identifier, the user whose key signs in, and where the
	// table lives. table is the table's name for both.
	Project   string `json:"project,omitempty"`
	Dataset   string `json:"dataset,omitempty"`
	Table     string `json:"table,omitempty"`
	Account   string `json:"account,omitempty"`
	User      string `json:"user,omitempty"`
	Database  string `json:"database,omitempty"`
	Schema    string `json:"schema,omitempty"`
	Warehouse string `json:"warehouse,omitempty"`
	Role      string `json:"role,omitempty"`
}

var (
	measurementID = regexp.MustCompile(`^G-[A-Z0-9]{4,20}$`)
	hubspotProp   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,99}$`)
	ga4Name       = regexp.MustCompile(`[^a-zA-Z0-9_]`)
)

// NeedsIntegration reports whether a kind sends through one of the
// workspace's connections, and NeedsSecret whether it takes a secret of
// its own.
func NeedsIntegration(kind string) bool { return kind == "slack" || kind == "hubspot" }
func NeedsSecret(kind string) bool {
	return kind == "ga4" || kind == "bigquery" || kind == "snowflake"
}

// Check reports why a kind's config cannot be used, or nil. allowPrivate
// is the webhooks setting for self-hosted servers.
func Check(kind string, c Config, allowPrivate bool) error {
	switch kind {
	case "webhook":
		return webhooks.CheckURL(c.URL, allowPrivate)
	case "slack":
		if strings.TrimSpace(c.Channel) == "" || len(c.Channel) > 100 {
			return errors.New("channel is required: pick the Slack channel to post in")
		}
	case "ga4":
		if !measurementID.MatchString(c.MeasurementID) {
			return errors.New("measurement_id must be the web stream's measurement id, such as G-ABC123DEF4")
		}
	case "hubspot":
		if len(c.Properties) > 10 {
			return errors.New("properties can set at most 10 contact properties")
		}
		for k, v := range c.Properties {
			if !hubspotProp.MatchString(k) || len(v) > 300 {
				return errors.New("properties must use HubSpot's internal property names (lowercase letters, numbers and underscores), with values of at most 300 characters")
			}
		}
	case "bigquery", "snowflake":
		return checkWarehouse(kind, c)
	default:
		return fmt.Errorf("kind must be one of %s", strings.Join(Kinds, ", "))
	}
	return nil
}

// Sender matches events to destinations and sends them as jobs.
type Sender struct {
	Pool  *pgxpool.Pool
	Store *connectors.Store
	Box   *secrets.Box
	Hooks *webhooks.Service
	Log   *slog.Logger
	// Client overrides the HTTP client, and the URLs the tools' own
	// endpoints, for tests.
	Client         *http.Client
	GA4URL         string
	GoogleTokenURL string
	BigQueryURL    string
	SnowflakeURL   string
}

type payload struct {
	Destination string `json:"destination_id"`
}

// Register adds the job handler.
func (s *Sender) Register(q *jobs.Queue) {
	q.Handle(JobSend, s.job)
	q.Handle(JobTest, s.testJob)
}

// Run matches and schedules sends until ctx ends.
func (s *Sender) Run(ctx context.Context) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.Log, track.Event{Name: "destinations.schedule_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick queues the new events each enabled destination takes, then asks
// for a send of every destination with events waiting.
func (s *Sender) Tick(ctx context.Context) error {
	if _, err := Match(ctx, s.Pool, ""); err != nil {
		return err
	}
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT q.destination_id, q.workspace_id FROM event_destination_sends q
		JOIN event_destinations d ON d.id = q.destination_id WHERE q.status = 'queued' AND d.enabled`)
	if err != nil {
		return err
	}
	type due struct{ id, ws string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (due, error) {
		var d due
		return d, r.Scan(&d.id, &d.ws)
	})
	if err != nil {
		return err
	}
	var oldest float64
	if err := s.Pool.QueryRow(ctx, `SELECT COALESCE(EXTRACT(EPOCH FROM now() - min(q.created_at)), 0) FROM event_destination_sends q
		JOIN event_destinations d ON d.id = q.destination_id WHERE q.status = 'queued' AND d.enabled`).Scan(&oldest); err != nil {
		return err
	}
	metrics.DestinationsOldest(time.Duration(oldest * float64(time.Second)))
	slot := time.Now().Truncate(every).Unix()
	for _, d := range list {
		if _, err := Enqueue(ctx, s.Pool, d.ws, d.id, fmt.Sprint(slot)); err != nil {
			return err
		}
	}
	return nil
}

// Querier is a pool or a transaction.
type Querier = jobs.Querier

// Match queues the events destinations take that arrived since each one
// last looked: the destination's brand's events, named in its list or,
// with an empty list, every tracked event. An event that arrived before
// the destination was made is never sent. ws "" matches every workspace.
func Match(ctx context.Context, q Querier, ws string) (int64, error) {
	var n int64
	err := q.QueryRow(ctx, `
		WITH upto AS (SELECT now() - $2::interval AS t),
		ins AS (
			INSERT INTO event_destination_sends (workspace_id, brand_id, destination_id, event_row_id, event_name)
			SELECT d.workspace_id, d.brand_id, d.id, e.id, e.name
			FROM event_destinations d, upto, customer_events e
			WHERE d.enabled AND ($1 = '' OR d.workspace_id::text = $1)
			  AND e.brand_id = d.brand_id AND e.workspace_id = d.workspace_id
			  AND e.received_at > d.matched_until AND e.received_at <= upto.t
			  AND ((cardinality(d.events) = 0 AND e.kind = 'track') OR e.name = ANY (d.events))
			ON CONFLICT (destination_id, event_row_id) DO NOTHING
			RETURNING 1),
		moved AS (
			UPDATE event_destinations d SET matched_until = upto.t FROM upto
			WHERE d.enabled AND ($1 = '' OR d.workspace_id::text = $1) AND d.matched_until < upto.t
			RETURNING 1)
		SELECT (SELECT count(*) FROM ins) + 0 * (SELECT count(*) FROM moved)`, ws, fmt.Sprintf("%d seconds", int(settle.Seconds()))).Scan(&n)
	return n, err
}

// Enqueue asks for one send of a destination's waiting events. key keeps
// two asks in the same minute to one job.
func Enqueue(ctx context.Context, q Querier, ws, id, key string) (int64, error) {
	return jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobSend, WorkspaceID: &ws, Payload: payload{Destination: id},
		IdempotencyKey: JobSend + ":" + id + ":" + key, MaxAttempts: 3})
}

func (s *Sender) job(ctx context.Context, job jobs.Job) error {
	var p payload
	if err := json.Unmarshal(job.Payload, &p); err != nil || p.Destination == "" || job.WorkspaceID == nil {
		return nil
	}
	res, err := s.Send(ctx, *job.WorkspaceID, p.Destination)
	if res.Kind != "" {
		metrics.DestinationSends(res.Kind, res.Sent, res.Skipped, res.Failed)
	}
	ev := track.Event{Name: "destination.events_sent", WorkspaceID: *job.WorkspaceID, ActorType: "system", Surface: "job",
		Props: map[string]any{"destination_id": p.Destination, "kind": res.Kind, "sent": res.Sent, "skipped": res.Skipped, "failed": res.Failed}}
	if err != nil {
		track.Fail(ctx, s.Log, ev, "internal", err)
		return err
	}
	if res.Unreachable {
		ev.Name = "destination.unreachable"
		ev.Outcome = track.Failure
		track.Emit(ctx, ev)
		return nil
	}
	if res.Sent+res.Skipped+res.Failed > 0 {
		track.Emit(ctx, ev)
	}
	return nil
}

// Result counts what one send did. Unreachable says the tool could not
// be reached and events are still waiting.
type Result struct {
	Kind        string `json:"kind"`
	Sent        int    `json:"sent"`
	Skipped     int    `json:"skipped"`
	Failed      int    `json:"failed"`
	Unreachable bool   `json:"unreachable"`
}

type destination struct {
	id, ws, brand, kind string
	config              Config
	integration         *string
	secret              string
	// fresh: nothing has reached it yet, so a warehouse's table may
	// still have to be made.
	fresh bool
}

// Event is one customer event as destinations see it; it is also the
// body a webhook destination receives.
type Event struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	RawName     string         `json:"raw_name,omitempty"`
	Kind        string         `json:"kind"`
	BrandID     string         `json:"brand_id"`
	OccurredAt  time.Time      `json:"occurred_at"`
	ReceivedAt  time.Time      `json:"received_at"`
	UserID      *string        `json:"user_id"`
	AnonymousID *string        `json:"anonymous_id"`
	RecordID    *string        `json:"record_id"`
	Properties  map[string]any `json:"properties"`
	Traits      map[string]any `json:"traits,omitempty"`

	// The person, for tools that need them; never in a webhook's body
	// beyond record_id (the API has the rest).
	personName, personEmail string
	sendID                  string
	attempts                int
}

type outcome int

const (
	sent outcome = iota
	skipped
	failed
	// retry: the tool could not be reached; try again later.
	retry
)

// Send delivers a destination's waiting events, oldest first, and stops
// at the first one the tool cannot be reached for.
func (s *Sender) Send(ctx context.Context, ws, id string) (Result, error) {
	var res Result
	d, err := s.load(ctx, ws, id, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return res, nil
	}
	if err != nil {
		return res, err
	}
	res.Kind = d.kind
	deliver, why := s.deliverer(ctx, d)
	for round := 0; round < rounds; round++ {
		events, err := s.loadQueued(ctx, d)
		if err != nil {
			return res, err
		}
		var results []result
		if deliver.many != nil && len(events) > 0 {
			results = deliver.many(ctx, events)
		}
		for i, e := range events {
			o, reason := failed, why
			switch {
			case results != nil:
				o, reason = results[i].o, results[i].reason
			case deliver.one != nil:
				o, reason = deliver.one(ctx, e)
			}
			if o == retry && e.attempts+1 < maxAttempts {
				if err := s.waiting(ctx, d, e, reason); err != nil {
					return res, err
				}
				res.Unreachable = true
				if results == nil {
					// One at a time: the rest would not get through either.
					return res, nil
				}
				continue
			}
			if o == retry {
				o = failed
			}
			if err := s.mark(ctx, d, e, o, reason); err != nil {
				return res, err
			}
			switch o {
			case sent:
				res.Sent++
			case skipped:
				res.Skipped++
			default:
				res.Failed++
			}
		}
		if len(events) < batch || res.Unreachable {
			break
		}
	}
	return res, nil
}

// load reads a destination; off says whether one that is turned off will
// do (a test may be sent before it is turned on).
func (s *Sender) load(ctx context.Context, ws, id string, off bool) (destination, error) {
	d := destination{id: id, ws: ws}
	var cfg, sealed []byte
	err := s.Pool.QueryRow(ctx, `SELECT brand_id, kind, config, integration_id, secret, last_success_at IS NULL FROM event_destinations
		WHERE workspace_id = $1 AND id = $2 AND (enabled OR $3)`, ws, id, off).Scan(&d.brand, &d.kind, &cfg, &d.integration, &sealed, &d.fresh)
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(cfg, &d.config); err != nil {
		return d, err
	}
	if len(sealed) > 0 {
		plain, err := s.Box.Open(sealed, []byte(id))
		if err != nil {
			return d, fmt.Errorf("open destination secret: %w", err)
		}
		d.secret = string(plain)
	}
	return d, nil
}

func (s *Sender) loadQueued(ctx context.Context, d destination) ([]Event, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT q.id, q.attempts, e.id, e.name, e.raw_name, e.kind, e.occurred_at, e.received_at, e.user_id, e.anonymous_id,
		       e.record_id, e.properties, e.traits, COALESCE(r.name, ''), COALESCE(r.attributes->>'email', '')
		FROM event_destination_sends q
		JOIN customer_events e ON e.id = q.event_row_id
		LEFT JOIN records r ON r.id = e.record_id AND r.workspace_id = q.workspace_id
		WHERE q.workspace_id = $1 AND q.destination_id = $2 AND q.status = 'queued'
		ORDER BY q.created_at, q.id LIMIT $3`, d.ws, d.id, batch)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Event, error) {
		e := Event{Type: "event", BrandID: d.brand}
		var props, traits []byte
		if err := r.Scan(&e.sendID, &e.attempts, &e.ID, &e.Name, &e.RawName, &e.Kind, &e.OccurredAt, &e.ReceivedAt, &e.UserID, &e.AnonymousID,
			&e.RecordID, &props, &traits, &e.personName, &e.personEmail); err != nil {
			return e, err
		}
		_ = json.Unmarshal(props, &e.Properties)
		_ = json.Unmarshal(traits, &e.Traits)
		if e.Properties == nil {
			e.Properties = map[string]any{}
		}
		return e, nil
	})
}

func (s *Sender) mark(ctx context.Context, d destination, e Event, o outcome, reason string) error {
	status := map[outcome]string{sent: "sent", skipped: "skipped", failed: "failed"}[o]
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE event_destination_sends SET status = $3, reason = $4, attempts = attempts + 1,
			sent_at = CASE WHEN $3 = 'sent' THEN now() END, updated_at = now() WHERE workspace_id = $1 AND id = $2`, d.ws, e.sendID, status, trim(reason)); err != nil {
			return err
		}
		switch o {
		case sent:
			_, err := tx.Exec(ctx, `UPDATE event_destinations SET last_success_at = now(), failing_since = NULL, last_error = '' WHERE workspace_id = $1 AND id = $2`, d.ws, d.id)
			return err
		case failed:
			_, err := tx.Exec(ctx, `UPDATE event_destinations SET failing_since = COALESCE(failing_since, now()), last_error = $3 WHERE workspace_id = $1 AND id = $2`, d.ws, d.id, trim(reason))
			return err
		}
		return nil
	})
}

// waiting leaves an event queued after a try that got no answer.
func (s *Sender) waiting(ctx context.Context, d destination, e Event, reason string) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE event_destination_sends SET attempts = attempts + 1, reason = $3, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, d.ws, e.sendID, trim(reason)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE event_destinations SET failing_since = COALESCE(failing_since, now()), last_error = $3 WHERE workspace_id = $1 AND id = $2`, d.ws, d.id, trim(reason))
		return err
	})
}

// delivery is how a destination sends: one event at a time, or (many) a
// batch in one call. Neither set: it cannot send at all.
type delivery struct {
	one  func(context.Context, Event) (outcome, string)
	many func(context.Context, []Event) []result
}

// deliverer returns how this destination sends, or nothing and why it
// cannot send at all (its connection is gone, its key unreadable).
func (s *Sender) deliverer(ctx context.Context, d destination) (delivery, string) {
	one, why := s.single(ctx, d)
	if one != nil || why != "" {
		return delivery{one: one}, why
	}
	switch d.kind {
	case "bigquery":
		many, why := s.bigQuery(d)
		return delivery{many: many}, why
	case "snowflake":
		many, why := s.snowflake(d)
		return delivery{many: many}, why
	}
	return delivery{}, "this version cannot send to a " + d.kind + " destination"
}

// single is deliverer for the kinds that send one event per call.
func (s *Sender) single(ctx context.Context, d destination) (func(context.Context, Event) (outcome, string), string) {
	switch d.kind {
	case "webhook":
		return func(ctx context.Context, e Event) (outcome, string) {
			body, err := json.Marshal(e)
			if err != nil {
				return failed, "the event could not be written as JSON"
			}
			code, msg := s.Hooks.Post(ctx, d.config.URL, d.secret, "evt_"+e.sendID, body)
			return httpOutcome(code, msg)
		}, ""
	case "ga4":
		return func(ctx context.Context, e Event) (outcome, string) { return s.ga4(ctx, d, e) }, ""
	case "slack", "hubspot":
		if d.integration == nil {
			return nil, "its connection was removed; pick another under Destinations"
		}
		o, err := s.Store.Open(ctx, s.Pool, d.ws, *d.integration)
		if err != nil {
			return nil, "its connection could not be opened: " + connectors.KindOf(err)
		}
		if d.kind == "slack" {
			n, ok := o.Connector.(connectors.Notifier)
			if !ok {
				return nil, "its connection can't post messages"
			}
			return func(ctx context.Context, e Event) (outcome, string) {
				return connOutcome(n.Post(ctx, o.Client, o.Creds, d.config.Channel, notice(e)))
			}, ""
		}
		p, ok := o.Connector.(connectors.ContactPusher)
		if !ok {
			return nil, "its connection can't update contacts"
		}
		return func(ctx context.Context, e Event) (outcome, string) {
			if e.personEmail == "" {
				return skipped, "the event's person has no email address"
			}
			first, last, _ := strings.Cut(e.personName, " ")
			_, err := p.PushContact(ctx, o.Client, o.Creds, connectors.PushedContact{Email: e.personEmail, FirstName: first, LastName: last, Properties: d.config.Properties})
			return connOutcome(err)
		}, ""
	}
	return nil, ""
}

func httpOutcome(code int, msg string) (outcome, string) {
	switch {
	case code >= 200 && code < 300:
		return sent, ""
	case code == 0 || code == 429 || code >= 500:
		return retry, msg
	}
	return failed, msg
}

func connOutcome(err error) (outcome, string) {
	if err == nil {
		return sent, ""
	}
	// Never the tool's own words: they can quote the event.
	kind := connectors.KindOf(err)
	if kind == connectors.ErrUnavailable {
		return retry, "the tool could not be reached"
	}
	return failed, "the tool refused it: " + kind
}

// notice is what Slack shows for an event: its name, who did it and its
// first few properties.
func notice(e Event) connectors.Notice {
	title := e.RawName
	if title == "" {
		title = e.Name
	}
	var parts []string
	switch {
	case e.personName != "" && e.personEmail != "":
		parts = append(parts, e.personName+" ("+e.personEmail+")")
	case e.personName != "" || e.personEmail != "":
		parts = append(parts, e.personName+e.personEmail)
	}
	keys := make([]string, 0, len(e.Properties))
	for k, v := range e.Properties {
		switch v.(type) {
		case string, float64, bool:
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) > 6 {
		keys = keys[:6]
	}
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %s", k, trimTo(fmt.Sprint(e.Properties[k]), 80)))
	}
	return connectors.Notice{Title: title, Text: strings.Join(parts, " · ")}
}

// ga4 sends one event through the Measurement Protocol with the
// workspace's own API secret. Google takes at most 25 parameters, with
// names of letters, numbers and underscores up to 40 characters.
func (s *Sender) ga4(ctx context.Context, d destination, e Event) (outcome, string) {
	if d.secret == "" {
		return failed, "the API secret is missing; add it under Destinations"
	}
	client := ""
	if e.AnonymousID != nil {
		client = *e.AnonymousID
	}
	if client == "" && e.UserID != nil {
		client = *e.UserID
	}
	if client == "" {
		return skipped, "the event names no browser or user"
	}
	if time.Since(e.OccurredAt) > 72*time.Hour {
		return skipped, "Google Analytics takes events up to 72 hours old"
	}
	params := map[string]any{"engagement_time_msec": 1}
	keys := make([]string, 0, len(e.Properties))
	for k := range e.Properties {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(params) >= 25 {
			break
		}
		name := trimTo(ga4Name.ReplaceAllString(k, "_"), 40)
		switch v := e.Properties[k].(type) {
		case string:
			params[name] = trimTo(v, 100)
		case float64, bool:
			params[name] = v
		}
	}
	body := map[string]any{"client_id": client, "timestamp_micros": e.OccurredAt.UnixMicro(),
		"events": []map[string]any{{"name": trimTo(ga4Name.ReplaceAllString(e.Name, "_"), 40), "params": params}}}
	if e.UserID != nil && *e.UserID != "" {
		body["user_id"] = *e.UserID
	}
	raw, _ := json.Marshal(body)
	base := s.GA4URL
	if base == "" {
		base = ga4Collect
	}
	q := url.Values{"measurement_id": {d.config.MeasurementID}, "api_secret": {d.secret}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"?"+q.Encode(), bytes.NewReader(raw))
	if err != nil {
		return failed, "the request could not be built"
	}
	req.Header.Set("Content-Type", "application/json")
	client2 := s.Client
	if client2 == nil {
		client2 = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client2.Do(req)
	if err != nil {
		// Never the error's text: it carries the URL with the secret.
		return retry, "Google Analytics could not be reached"
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return httpOutcome(resp.StatusCode, "Google Analytics answered "+resp.Status)
}

func trim(s string) string { return trimTo(s, 500) }

func trimTo(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
