// Package adconv sends conversions back to the ad platforms: when a
// customer event a rule names happens (a sign-up, a payment), the platform
// that brought the person hears about it server to server, with the ad
// click id from the person's visits and their hashed email and phone, so
// its reporting and bidding learn from real outcomes.
//
// Each rule maps one event to one conversion on one ad account. Every
// event it matches is a row in ad_conversion_sends, sent or skipped with
// the reason, so a person can see exactly what reached each platform.
//
// Consent: inside the EEA, the UK and Switzerland, and wherever the
// person's country is unknown, a conversion is sent only when they granted
// ad data (consents channel ads, purpose ad_user_data). Elsewhere the
// workspace's consent_mode decides: opt_out sends unless they withdrew it,
// opt_in only with a grant. A withdrawal always stops it.
package adconv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

const (
	JobSend = "ads.conversions_send"
	// every is how often new events are matched to rules.
	every = 2 * time.Minute
	// window is how far back an event may have arrived to be sent at all;
	// a rule never sends events that arrived before it was made.
	window = 7 * 24 * time.Hour
	// clickWindow is how long a click id stays usable.
	clickWindow = 90 * 24 * time.Hour
	// batch is the most conversions sent in one call.
	batch = 500
	// maxAttempts is how often a platform that cannot be reached is tried.
	maxAttempts = 5
)

// Consent modes.
const (
	OptOut = "opt_out"
	OptIn  = "opt_in"
)

// strict are the countries where ad data needs a grant (EEA, UK, CH).
var strict = map[string]bool{}

func init() {
	for _, c := range strings.Fields("AT BE BG HR CY CZ DK EE FI FR DE GR HU IE IT LV LT LU MT NL PL PT RO SK SI ES SE IS LI NO GB CH") {
		strict[c] = true
	}
}

// Sender matches events to rules and sends them as jobs.
type Sender struct {
	Pool  *pgxpool.Pool
	Store *connectors.Store
	Log   *slog.Logger
	Now   func() time.Time
}

type payload struct {
	Rule string `json:"rule_id"`
}

// Register adds the job handler.
func (s *Sender) Register(q *jobs.Queue) { q.Handle(JobSend, s.job) }

func (s *Sender) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Run matches and schedules sends until ctx ends.
func (s *Sender) Run(ctx context.Context) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.Log, track.Event{Name: "ad_conversions.schedule_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick queues every new event an enabled rule matches, then asks for a
// send of each rule with conversions waiting.
func (s *Sender) Tick(ctx context.Context) error {
	if _, err := Match(ctx, s.Pool, ""); err != nil {
		return err
	}
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT q.rule_id, q.workspace_id FROM ad_conversion_sends q
		JOIN ad_conversion_rules r ON r.id = q.rule_id WHERE q.status = 'queued' AND r.enabled`)
	if err != nil {
		return err
	}
	type due struct{ rule, ws string }
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.rule, &d.ws); err != nil {
			rows.Close()
			return err
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var oldest float64
	if err := s.Pool.QueryRow(ctx, `SELECT COALESCE(EXTRACT(EPOCH FROM now() - min(q.created_at)), 0) FROM ad_conversion_sends q
		JOIN ad_conversion_rules r ON r.id = q.rule_id WHERE q.status = 'queued' AND r.enabled`).Scan(&oldest); err != nil {
		return err
	}
	metrics.AdConversionsOldest(time.Duration(oldest * float64(time.Second)))
	slot := s.now().Truncate(every).Unix()
	for _, d := range list {
		if _, err := Enqueue(ctx, s.Pool, d.ws, d.rule, fmt.Sprint(slot)); err != nil {
			return err
		}
	}
	return nil
}

// Match queues the events rules match that have not been queued yet: the
// event's name is the rule's, it arrived after the rule was made and in
// the last week, and its brand is the account's (or either is shared).
// ws "" matches every workspace.
func Match(ctx context.Context, q jobs.Querier, ws string) (int64, error) {
	tag, err := q.Exec(ctx, `
		INSERT INTO ad_conversion_sends (workspace_id, rule_id, event_row_id, record_id, event_id, occurred_at)
		SELECT r.workspace_id, r.id, e.id, e.record_id, COALESCE(NULLIF(e.message_id, ''), e.id::text), e.occurred_at
		FROM ad_conversion_rules r
		JOIN ad_accounts a ON a.id = r.ad_account_id AND a.status = 'active'
		JOIN customer_events e ON e.workspace_id = r.workspace_id AND e.name = r.event_name
			AND e.occurred_at > now() - interval '30 days'
			AND e.received_at >= r.created_at AND e.received_at > now() - $2::interval
		WHERE r.enabled AND ($1 = '' OR r.workspace_id::text = $1)
		  AND (e.brand_id IS NULL OR a.brand_id IS NULL OR e.brand_id = a.brand_id)
		ON CONFLICT (rule_id, event_row_id) DO NOTHING`, ws, fmt.Sprintf("%d seconds", int(window.Seconds())))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Enqueue asks for a send of one rule's waiting conversions.
func Enqueue(ctx context.Context, q jobs.Querier, ws, ruleID, key string) (int64, error) {
	return jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobSend, WorkspaceID: &ws, Payload: payload{Rule: ruleID},
		IdempotencyKey: "adconv:" + ruleID + ":" + key, MaxAttempts: 3})
}

func (s *Sender) job(ctx context.Context, job jobs.Job) error {
	var p payload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return nil
	}
	res, err := s.SendRule(ctx, *job.WorkspaceID, p.Rule)
	if res.Connector != "" {
		metrics.AdConversions(res.Connector, res.Sent, res.Skipped, res.Failed)
	}
	ev := track.Event{Name: "ad_conversions.sent", WorkspaceID: *job.WorkspaceID, ActorType: "system", Surface: "job",
		Props: map[string]any{"rule_id": p.Rule, "connector": res.Connector, "sent": res.Sent, "skipped": res.Skipped, "failed": res.Failed}}
	var db dbError
	switch {
	case err == nil:
		if res.Sent+res.Skipped+res.Failed > 0 {
			track.Emit(ctx, ev)
		}
		return nil
	case errors.Is(err, connectors.ErrNotFound):
		return nil
	case errors.As(err, &db):
		track.Fail(ctx, s.Log, ev, "internal", err)
		return err
	}
	kind := connectors.KindOf(err)
	ev.Outcome = track.Failure
	if kind == connectors.ErrUnavailable {
		ev.Outcome = track.Error
	}
	track.Fail(ctx, s.Log, ev, "connector_"+kind, err)
	return nil
}

// Result counts what one send did.
type Result struct {
	Connector string `json:"connector"`
	Sent      int    `json:"sent"`
	Skipped   int    `json:"skipped"`
	Failed    int    `json:"failed"`
}

type dbError struct{ error }

func (e dbError) Unwrap() error { return e.error }

type rule struct {
	id, ws, integrationID, connector, eventType, targetID, targetName string
	valueProperty, valueUnit, currency                                string
	fixed                                                             *int64
	acct                                                              connectors.AdAccount
}

type queued struct {
	id, eventID      string
	recordID, anonID *string
	occurred         time.Time
	props            map[string]any
	attempts         int
}

// SendRule sends one rule's waiting conversions, a batch at a time.
func (s *Sender) SendRule(ctx context.Context, ws, ruleID string) (Result, error) {
	var res Result
	r, err := s.loadRule(ctx, ws, ruleID)
	if err != nil {
		return res, err
	}
	res.Connector = r.connector
	o, err := s.Store.Open(ctx, s.Pool, ws, r.integrationID)
	if err != nil {
		if connectors.KindOf(err) == connectors.ErrAuth {
			s.failAll(ctx, ruleID, err.Error(), &res)
		}
		return res, err
	}
	sender, ok := o.Connector.(connectors.ConversionSender)
	if !ok {
		err := connectors.Errf(connectors.ErrInvalid, "%s cannot take conversions yet", o.Connector.Definition().Name)
		s.failAll(ctx, ruleID, err.Error(), &res)
		return res, err
	}
	mode, err := s.consentMode(ctx, ws)
	if err != nil {
		return res, dbError{err}
	}
	target := connectors.ConversionTarget{ID: r.targetID, Name: r.targetName}
	for round := 0; round < 20; round++ {
		list, err := s.loadQueued(ctx, ws, ruleID)
		if err != nil {
			return res, dbError{err}
		}
		if len(list) == 0 {
			return res, nil
		}
		var convs []connectors.AdConversion
		byIndex := map[int]queued{}
		for i, q := range list {
			cv, reason, err := s.build(ctx, ws, r, mode, q)
			if err != nil {
				return res, dbError{err}
			}
			if reason != "" {
				if err := s.mark(ctx, q.id, "skipped", reason, nil, nil, ""); err != nil {
					return res, dbError{err}
				}
				res.Skipped++
				continue
			}
			cv.Index = i
			byIndex[i] = q
			convs = append(convs, cv)
		}
		if len(convs) == 0 {
			continue
		}
		results, err := sender.SendConversions(ctx, o.Client, o.Creds, r.acct, target, convs)
		if err != nil {
			if connectors.KindOf(err) == connectors.ErrUnavailable {
				// Try again later; give up after maxAttempts.
				for _, q := range byIndex {
					if q.attempts+1 >= maxAttempts {
						_ = s.mark(ctx, q.id, "failed", err.Error(), nil, nil, "")
						res.Failed++
					} else {
						_, _ = s.Pool.Exec(ctx, `UPDATE ad_conversion_sends SET attempts = attempts + 1, reason = $2, updated_at = now() WHERE id = $1`, q.id, trim(err.Error(), 500))
					}
				}
				return res, err
			}
			for _, q := range byIndex {
				_ = s.mark(ctx, q.id, "failed", err.Error(), nil, nil, "")
				res.Failed++
			}
			return res, err
		}
		for _, cr := range results {
			q, ok := byIndex[cr.Index]
			if !ok {
				continue
			}
			delete(byIndex, cr.Index)
			var cv connectors.AdConversion
			for _, c := range convs {
				if c.Index == cr.Index {
					cv = c
				}
			}
			status := "failed"
			switch {
			case cr.OK:
				status = "sent"
				res.Sent++
			case cr.Skipped:
				status = "skipped"
				res.Skipped++
			default:
				res.Failed++
			}
			if err := s.mark(ctx, q.id, status, cr.Reason, cr.MatchKeys, cv.ValueMicros, cv.Currency); err != nil {
				return res, dbError{err}
			}
		}
		// A conversion the platform said nothing about is not left waiting.
		for _, q := range byIndex {
			if err := s.mark(ctx, q.id, "failed", "the platform gave no answer for it", nil, nil, ""); err != nil {
				return res, dbError{err}
			}
			res.Failed++
		}
	}
	return res, nil
}

func (s *Sender) loadRule(ctx context.Context, ws, id string) (rule, error) {
	var r rule
	var meta []byte
	err := s.Pool.QueryRow(ctx, `
		SELECT r.id, r.workspace_id, a.integration_id, a.connector, r.event_type, r.target_id, r.target_name,
			r.value_property, r.value_unit, r.currency, r.fixed_value_micros,
			a.external_id, a.name, a.currency, a.time_zone, a.meta
		FROM ad_conversion_rules r JOIN ad_accounts a ON a.id = r.ad_account_id
		WHERE r.workspace_id = $1 AND r.id = $2 AND r.enabled`, ws, id).Scan(
		&r.id, &r.ws, &r.integrationID, &r.connector, &r.eventType, &r.targetID, &r.targetName,
		&r.valueProperty, &r.valueUnit, &r.currency, &r.fixed,
		&r.acct.ID, &r.acct.Name, &r.acct.Currency, &r.acct.TimeZone, &meta)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, connectors.ErrNotFound
	}
	if err != nil {
		return r, dbError{err}
	}
	_ = json.Unmarshal(meta, &r.acct.Meta)
	return r, nil
}

func (s *Sender) consentMode(ctx context.Context, ws string) (string, error) {
	mode := OptOut
	err := s.Pool.QueryRow(ctx, `SELECT consent_mode FROM ad_settings WHERE workspace_id = $1`, ws).Scan(&mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return OptOut, nil
	}
	return mode, err
}

func (s *Sender) loadQueued(ctx context.Context, ws, ruleID string) ([]queued, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT q.id, q.event_id, q.record_id, e.anonymous_id, q.occurred_at, e.properties, q.attempts
		FROM ad_conversion_sends q JOIN customer_events e ON e.id = q.event_row_id
		WHERE q.workspace_id = $1 AND q.rule_id = $2 AND q.status = 'queued'
		ORDER BY q.created_at, q.id LIMIT $3`, ws, ruleID, batch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []queued
	for rows.Next() {
		var q queued
		var props []byte
		if err := rows.Scan(&q.id, &q.eventID, &q.recordID, &q.anonID, &q.occurred, &props, &q.attempts); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(props, &q.props)
		out = append(out, q)
	}
	return out, rows.Err()
}

// build gathers what one conversion carries, or the reason it is not sent.
func (s *Sender) build(ctx context.Context, ws string, r rule, mode string, q queued) (connectors.AdConversion, string, error) {
	cv := connectors.AdConversion{EventID: q.eventID, Time: q.occurred, EventType: r.eventType, ClickIDs: map[string]string{}}
	send, granted, reason, err := Consent(ctx, s.Pool, ws, q.recordID, q.anonID, mode)
	if err != nil || !send {
		return cv, reason, err
	}
	cv.ConsentGranted = granted
	if err := s.clicks(ctx, ws, q, &cv); err != nil {
		return cv, "", err
	}
	if q.recordID != nil {
		cv.ExternalID = connectors.HashID(*q.recordID)
		rows, err := s.Pool.Query(ctx, `SELECT namespace, value FROM record_identities
			WHERE workspace_id = $1 AND record_id = $2 AND valid_to IS NULL AND namespace IN ('email', 'phone')
			ORDER BY verified DESC, created_at DESC LIMIT 6`, ws, *q.recordID)
		if err != nil {
			return cv, "", err
		}
		for rows.Next() {
			var ns, v string
			if err := rows.Scan(&ns, &v); err != nil {
				rows.Close()
				return cv, "", err
			}
			switch ns {
			case "email":
				cv.Emails = append(cv.Emails, connectors.HashID(v))
			case "phone":
				// E.164 with the plus; Meta wants digits only.
				cv.Phones = append(cv.Phones, connectors.HashID(v))
				cv.PhoneDigits = append(cv.PhoneDigits, connectors.HashID(strings.TrimPrefix(v, "+")))
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return cv, "", err
		}
	}
	cv.ValueMicros, cv.Currency = value(r, q.props)
	return cv, "", nil
}

// clicks fills the click ids: the event's own, then the latest of each
// from the person's visits before it, within the click window.
func (s *Sender) clicks(ctx context.Context, ws string, q queued, cv *connectors.AdConversion) error {
	if ids, ok := q.props["click_ids"].(map[string]any); ok {
		for k, v := range ids {
			if str, ok := v.(string); ok && str != "" {
				cv.ClickIDs[k] = str
			}
		}
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT click_ids, started_at FROM site_sessions
		WHERE workspace_id = $1 AND (record_id = $2 OR ($3::text IS NOT NULL AND anonymous_id = $3))
		  AND click_ids <> '{}' AND started_at <= $4 AND started_at > $4 - $5::interval
		ORDER BY started_at DESC LIMIT 20`, ws, q.recordID, q.anonID, q.occurred, fmt.Sprintf("%d seconds", int(clickWindow.Seconds())))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var at time.Time
		if err := rows.Scan(&raw, &at); err != nil {
			return err
		}
		var ids map[string]any
		_ = json.Unmarshal(raw, &ids)
		for k, v := range ids {
			if str, ok := v.(string); ok && str != "" && cv.ClickIDs[k] == "" {
				cv.ClickIDs[k] = str
				if cv.ClickTime.IsZero() {
					cv.ClickTime = at
				}
			}
		}
	}
	return rows.Err()
}

// Consent says whether a person's conversion may be sent, whether they
// granted ad data, and why not when it may not.
func Consent(ctx context.Context, q jobs.Querier, ws string, recordID, anonID *string, mode string) (send, granted bool, reason string, err error) {
	var country string
	err = q.QueryRow(ctx, `SELECT country FROM site_sessions WHERE workspace_id = $1
		AND (record_id = $2 OR ($3::text IS NOT NULL AND anonymous_id = $3)) AND country <> ''
		ORDER BY started_at DESC LIMIT 1`, ws, recordID, anonID).Scan(&country)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, false, "", err
	}
	// The person's latest choice, or, for a visitor nobody has identified
	// yet, the one their browser made on the site's consent banner.
	status := ""
	if recordID != nil || anonID != nil {
		err = q.QueryRow(ctx, `SELECT status FROM consents WHERE workspace_id = $1 AND channel = 'ads'
			AND purpose = 'ad_user_data' AND topic = ''
			AND (record_id = $2 OR ($2::uuid IS NULL AND $3::text IS NOT NULL AND anonymous_id = $3))
			ORDER BY occurred_at DESC, created_at DESC LIMIT 1`, ws, recordID, anonID).Scan(&status)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, false, "", err
		}
	}
	switch {
	case status == "withdrawn":
		return false, false, "the person withdrew consent for ad data", nil
	case status == "granted":
		return true, true, "", nil
	case country == "":
		return false, false, "no consent for ad data, and their country is unknown", nil
	case strict[country]:
		return false, false, "no consent for ad data (needed in the EEA, the UK and Switzerland)", nil
	case mode == OptIn:
		return false, false, "no consent for ad data (this workspace sends only with consent)", nil
	}
	return true, false, "", nil
}

// value is the conversion's value in micros and its currency, or nil.
func value(r rule, props map[string]any) (*int64, string) {
	cur := r.currency
	if cur == "" {
		if c, ok := props["currency"].(string); ok && len(strings.TrimSpace(c)) == 3 {
			cur = strings.ToUpper(strings.TrimSpace(c))
		}
	}
	if cur == "" {
		cur = r.acct.Currency
	}
	if r.valueProperty != "" {
		if v, ok := num(props[r.valueProperty]); ok && v >= 0 {
			scale := 1e6
			if r.valueUnit == "minor" {
				scale = 1e4
			}
			m := int64(math.Round(v * scale))
			return &m, cur
		}
	}
	if r.fixed != nil {
		m := *r.fixed
		return &m, cur
	}
	return nil, ""
}

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

func (s *Sender) mark(ctx context.Context, id, status, reason string, keys []string, valueMicros *int64, currency string) error {
	if keys == nil {
		keys = []string{}
	}
	_, err := s.Pool.Exec(ctx, `UPDATE ad_conversion_sends SET status = $2, reason = $3, match_keys = $4, value_micros = $5, currency = $6,
			attempts = attempts + CASE WHEN $2 IN ('sent', 'failed') THEN 1 ELSE 0 END,
			sent_at = CASE WHEN $2 = 'sent' THEN now() ELSE sent_at END, updated_at = now()
		WHERE id = $1`, id, status, trim(reason, 500), keys, valueMicros, currency)
	return err
}

// failAll fails every waiting conversion of a rule that cannot be sent at
// all, such as a connection that was refused.
func (s *Sender) failAll(ctx context.Context, ruleID, reason string, res *Result) {
	tag, err := s.Pool.Exec(ctx, `UPDATE ad_conversion_sends SET status = 'failed', reason = $2, updated_at = now()
		WHERE rule_id = $1 AND status = 'queued'`, ruleID, trim(reason, 500))
	if err == nil {
		res.Failed += int(tag.RowsAffected())
	}
}

func trim(s string, n int) string {
	if r := []rune(strings.TrimSpace(s)); len(r) > n {
		return string(r[:n])
	}
	return strings.TrimSpace(s)
}
