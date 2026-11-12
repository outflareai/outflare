package adsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Change events (plans/paid-ads/plan.md, sync and alerts): every 15
// minutes, for each connection whose network reports its change history
// (Google's change_event), a job reads what changed since the last read
// into ad_change_events: when, what, how, who (the user's email hashed,
// never kept) and from where (the web client, the API, an automated rule,
// a recommendation Google applied on its own). A change Outflare made
// itself (an ad_changes row applied around then, or the connection's own
// sign-in through the API) is marked outflare, so the Changes tab tells
// Outflare's changes, people's and Google's apart, and the auto-apply
// audit knows what Google did on its own.

const (
	JobChangeEvents = "ads.change_events"
	// changeEventInterval is how often each connection's history is read.
	changeEventInterval = 15 * time.Minute
	// changeEventKeep is how far back a first read goes (Google keeps 30
	// days), and changeEventOverlap how far before the watermark each
	// read starts again, since a change shows up to three minutes late.
	changeEventKeep    = 30 * 24 * time.Hour
	changeEventOverlap = 10 * time.Minute
	// ownChangeWindow is how close in time an ad_changes row must be to a
	// platform change on the same campaign to count as the same change.
	ownChangeWindow = 15 * time.Minute
	// Actors that aren't a hashed user.
	actorOutflare = "outflare"
	actorGoogle   = "google"
)

// ChangeReaders lists the ad connectors this build has that report their
// change history.
func ChangeReaders(r *connectors.Registry) []string {
	var out []string
	for _, d := range r.All() {
		if c, ok := r.Get(d.ID); ok && d.Status == connectors.Available {
			if _, ok := c.(connectors.AdChangeReader); ok {
				out = append(out, d.ID)
			}
		}
	}
	return out
}

// QueueChangeEvents queues a read of each connected history once per
// 15-minute slot.
func (s *Syncer) QueueChangeEvents(ctx context.Context) error {
	readers := ChangeReaders(s.Store.Registry)
	if len(readers) == 0 {
		return nil
	}
	rows, err := s.Pool.Query(ctx, `SELECT i.id, i.workspace_id FROM integrations i
		WHERE i.connector = ANY($1) AND i.status = 'connected' AND NOT COALESCE((i.config->>'sync_paused')::boolean, false)`, readers)
	if err != nil {
		return err
	}
	type due struct{ id, ws string }
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.ws); err != nil {
			rows.Close()
			return err
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	slot := s.now().Truncate(changeEventInterval).Unix()
	for _, d := range list {
		if err := EnqueueChangeEvents(ctx, s.Pool, d.ws, d.id, fmt.Sprintf("%s:%d", d.id, slot)); err != nil {
			return err
		}
	}
	return nil
}

// EnqueueChangeEvents asks for one read of a connection's change history;
// key de-duplicates.
func EnqueueChangeEvents(ctx context.Context, q jobs.Querier, ws, integrationID, key string) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobChangeEvents, WorkspaceID: &ws, Payload: payload{Integration: integrationID},
		IdempotencyKey: "ads.change_events:" + key, MaxAttempts: 2})
	return err
}

func (s *Syncer) changeEventsJob(ctx context.Context, job jobs.Job) error {
	var p payload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return nil
	}
	n, err := s.ReadChangeEvents(ctx, *job.WorkspaceID, p.Integration)
	if errors.Is(err, connectors.ErrNotFound) {
		return nil
	}
	var db dbError
	if errors.As(err, &db) {
		return err
	}
	if err != nil && connectors.KindOf(err) == connectors.ErrUnavailable {
		return err
	}
	_ = n
	return nil
}

// ReadChangeEvents reads one connection's change history now and returns
// how many changes it stored. Errors from the platform are recorded on
// the event and metric and returned; a database failure is a dbError.
func (s *Syncer) ReadChangeEvents(ctx context.Context, ws, integrationID string) (int, error) {
	o, err := s.Store.Open(ctx, s.Pool, ws, integrationID)
	if err != nil {
		return 0, err
	}
	connector := o.Connector.Definition().ID
	ev := track.Event{Name: "ad_change_events.read", WorkspaceID: ws, ActorType: "system", Surface: "job", Props: map[string]any{"connector": connector}}
	reader, ok := o.Connector.(connectors.AdChangeReader)
	if !ok {
		return 0, nil
	}
	accounts, err := s.loadAccounts(ctx, ws, integrationID)
	if err != nil {
		return 0, dbError{err}
	}
	var mark *time.Time
	_ = s.Pool.QueryRow(ctx, `SELECT last_change_event_at FROM ad_syncs WHERE integration_id = $1`, integrationID).Scan(&mark)
	now := s.now()
	since := now.Add(-changeEventKeep)
	if mark != nil && mark.Add(-changeEventOverlap).After(since) {
		since = mark.Add(-changeEventOverlap)
	}
	// The connection's own sign-in: changes it made through the API are
	// Outflare's.
	ownHash := connectors.HashEmail(o.Account.Email)
	latest := mark
	stored := 0
	var firstErr error
	for _, a := range accounts {
		if !a.syncOn {
			continue
		}
		events, err := reader.AdChangeEvents(ctx, o.Client, o.Creds, a.ad(), since)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if connectors.KindOf(err) == connectors.ErrAuth {
				break
			}
			continue
		}
		n, err := s.saveChangeEvents(ctx, ws, a, ownHash, events)
		if err != nil {
			return stored, dbError{err}
		}
		stored += n
		for _, e := range events {
			if latest == nil || e.At.After(*latest) {
				t := e.At
				latest = &t
			}
		}
	}
	if latest != nil && (mark == nil || latest.After(*mark)) {
		if _, err := s.Pool.Exec(ctx, `INSERT INTO ad_syncs (integration_id, workspace_id, last_change_event_at) VALUES ($1, $2, $3)
			ON CONFLICT (integration_id) DO UPDATE SET last_change_event_at = GREATEST(COALESCE(ad_syncs.last_change_event_at, '-infinity'), EXCLUDED.last_change_event_at)`,
			integrationID, ws, *latest); err != nil {
			return stored, dbError{err}
		}
	}
	ev.Props["changes"] = stored
	if firstErr != nil {
		code := "connector_" + connectors.KindOf(firstErr)
		metrics.AdChangeEventsRead(connector, code, stored)
		if connectors.KindOf(firstErr) == connectors.ErrUnavailable {
			ev.Outcome = track.Error
		} else {
			ev.Outcome = track.Failure
		}
		track.Fail(ctx, s.Log, ev, code, firstErr)
		return stored, firstErr
	}
	metrics.AdChangeEventsRead(connector, "success", stored)
	track.Emit(ctx, ev)
	return stored, nil
}

// saveChangeEvents stores one account's changes, skipping ones already
// stored, and returns how many were new.
func (s *Syncer) saveChangeEvents(ctx context.Context, ws string, a account, ownHash string, events []connectors.AdChangeEvent) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}
	n := 0
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		for _, e := range events {
			if e.ID == "" {
				continue
			}
			actor, err := s.changeActor(ctx, tx, a, ownHash, e)
			if err != nil {
				return err
			}
			detail, _ := json.Marshal(map[string]any{"changed_fields": e.ChangedFields, "old": trimJSON(e.Old), "new": trimJSON(e.New), "client_type": e.ClientType})
			tag, err := tx.Exec(ctx, `INSERT INTO ad_change_events (workspace_id, brand_id, ad_account_id, external_id, changed_at, actor, source, resource_type, operation, campaign_external_id, summary, detail)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
				ON CONFLICT (ad_account_id, external_id) DO NOTHING`,
				ws, a.brandID, a.id, trim(e.ID, 300), e.At, actor, changeSource(e.ClientType), trim(strings.ToLower(e.ResourceType), 60),
				trim(strings.ToLower(e.Operation), 30), trim(e.CampaignID, 100), changeSummary(e), detail)
			if err != nil {
				return err
			}
			n += int(tag.RowsAffected())
		}
		return nil
	})
	return n, err
}

// changeActor says who made a platform change: outflare for one of
// Outflare's own (a change Outflare applied on the same campaign within
// ownChangeWindow, or the connection's sign-in acting through the API),
// google for the platform's own automation, else the user's hash.
func (s *Syncer) changeActor(ctx context.Context, tx pgx.Tx, a account, ownHash string, e connectors.AdChangeEvent) (string, error) {
	api := strings.Contains(e.ClientType, "API")
	if api && e.CampaignID != "" {
		var own bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ad_changes c JOIN ad_entities e ON e.id = c.ad_entity_id
			WHERE e.ad_account_id = $1 AND e.external_id = $2 AND c.status IN ('queued', 'applied')
			  AND COALESCE(c.applied_at, c.created_at) BETWEEN $3::timestamptz - $4::interval AND $3::timestamptz + $4::interval)`,
			a.id, e.CampaignID, e.At, ownChangeWindow.String()).Scan(&own); err != nil {
			return "", err
		}
		if own {
			return actorOutflare, nil
		}
	}
	if api && ownHash != "" && e.UserHash == ownHash {
		return actorOutflare, nil
	}
	// Google's own doing: a rule or recommendation it applied, or a change
	// with no user behind it.
	if e.UserHash == "" || strings.Contains(e.ClientType, "AUTOMATED") || strings.Contains(e.ClientType, "RECOMMENDATION") {
		return actorGoogle, nil
	}
	return e.UserHash, nil
}

// changeSource is a client type in plain words: web, api, automated_rule,
// recommendation_auto_apply, script, editor, bulk, other.
func changeSource(clientType string) string {
	t := strings.ToUpper(clientType)
	switch {
	case strings.Contains(t, "WEB_CLIENT"):
		return "web"
	case strings.Contains(t, "RECOMMENDATION"):
		return "recommendation_auto_apply"
	case strings.Contains(t, "AUTOMATED_RULE"):
		return "automated_rule"
	case strings.Contains(t, "SCRIPT"):
		return "script"
	case strings.Contains(t, "EDITOR"):
		return "editor"
	case strings.Contains(t, "BULK"):
		return "bulk"
	case strings.Contains(t, "API"):
		return "api"
	case t == "":
		return ""
	}
	return "other"
}

// changeSummary is one line for the Changes tab: "updated campaign:
// status, name".
func changeSummary(e connectors.AdChangeEvent) string {
	verb := strings.ToLower(e.Operation)
	switch verb {
	case "create":
		verb = "created"
	case "update":
		verb = "updated"
	case "remove":
		verb = "removed"
	}
	what := strings.ReplaceAll(strings.ToLower(e.ResourceType), "_", " ")
	fields := make([]string, 0, len(e.ChangedFields))
	for _, f := range e.ChangedFields {
		fields = append(fields, f[strings.LastIndex(f, ".")+1:])
	}
	out := strings.TrimSpace(verb + " " + what)
	if len(fields) > 0 {
		out += ": " + strings.Join(fields, ", ")
	}
	return trim(out, 300)
}

// trimJSON keeps a resource's fields to a size the Changes tab can show.
func trimJSON(m map[string]any) any {
	if m == nil {
		return nil
	}
	b, _ := json.Marshal(m)
	if len(b) <= 4000 {
		return m
	}
	return map[string]any{"truncated": true}
}
