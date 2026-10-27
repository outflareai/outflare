package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adconv"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Sending conversions back: rules that map a customer event to a
// conversion on an ad account, the log of what each sent, and the
// workspace's consent setting. The sends themselves run in jobs
// (engine/internal/adconv).

func (s *Server) routesAdConversions(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeIntegrationsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/ads/conversion-targets", s.inWorkspace(manage, s.listAdConversionTargets))
	mux.HandleFunc("GET /v1/workspaces/{ws}/ads/conversion-rules", s.inWorkspace(read, s.listAdConversionRules))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ads/conversion-rules", s.inWorkspace(manage, s.createAdConversionRule))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/ads/conversion-rules/{id}", s.inWorkspace(manage, s.updateAdConversionRule))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/ads/conversion-rules/{id}", s.inWorkspace(manage, s.deleteAdConversionRule))
	mux.HandleFunc("GET /v1/workspaces/{ws}/ads/conversion-sends", s.inWorkspace(read, s.listAdConversionSends))
	mux.HandleFunc("GET /v1/workspaces/{ws}/ads/settings", s.inWorkspace(read, s.getAdSettings))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/ads/settings", s.inWorkspace(manage, s.setAdSettings))
}

var (
	adEventName = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$`)
	adCurrency  = regexp.MustCompile(`^[A-Z]{3}$`)
)

func trimTo(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// adConversionAccount opens the connection behind an ad account and its
// conversion sender, answering the error itself when it can't.
func (s *Server) adConversionAccount(w http.ResponseWriter, r *http.Request, ws, accountID string) (*connectors.Opened, connectors.ConversionSender, connectors.AdAccount, bool) {
	var acct connectors.AdAccount
	var integrationID string
	var meta map[string]string
	if !isUUID(accountID) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "account_id must be an ad account's id")
		return nil, nil, acct, false
	}
	err := s.pool.QueryRow(r.Context(), `SELECT integration_id, external_id, name, currency, time_zone, meta FROM ad_accounts
		WHERE workspace_id = $1 AND id = $2 AND status = 'active'`, ws, accountID).Scan(&integrationID, &acct.ID, &acct.Name, &acct.Currency, &acct.TimeZone, &meta)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "account_id is not an active ad account of this workspace")
		return nil, nil, acct, false
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, nil, acct, false
	}
	acct.Meta = meta
	o, err := s.integrations.Open(r.Context(), s.pool, ws, integrationID)
	if err != nil {
		s.connectorFailed(w, r, err)
		return o, nil, acct, false
	}
	sender, ok := o.Connector.(connectors.ConversionSender)
	if !ok {
		writeError(w, r, http.StatusUnprocessableEntity, "connector_invalid", o.Connector.Definition().Name+" cannot take conversions yet")
		return o, nil, acct, false
	}
	return o, sender, acct, true
}

// setupNeededFor lists the optional setup fields a conversion sender still
// lacks, such as Reddit's Conversions API token.
func setupNeededFor(o *connectors.Opened) []connectors.Field {
	out := []connectors.Field{}
	if n, ok := o.Connector.(connectors.SetupNeeder); ok {
		for _, f := range n.SetupFields() {
			if o.Creds[f.Name] == "" {
				out = append(out, f)
			}
		}
	}
	return out
}

// listAdConversionTargets asks the platform where an account's
// conversions can go: Google conversion actions, Meta pixels, Microsoft
// offline conversion goals, Reddit's Conversions API.
func (s *Server) listAdConversionTargets(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	o, sender, acct, ok := s.adConversionAccount(w, r, a.WorkspaceID, r.URL.Query().Get("account_id"))
	if !ok {
		return
	}
	need := setupNeededFor(o)
	list, err := sender.ConversionTargets(r.Context(), o.Client, o.Creds, acct)
	if err != nil {
		if connectors.KindOf(err) == connectors.ErrAuth && len(need) > 0 {
			writeJSON(w, http.StatusOK, map[string]any{"data": []any{}, "setup_needed": need, "integration_id": o.Integration.ID})
			return
		}
		s.connectorFailed(w, r, err)
		return
	}
	if list == nil {
		list = []connectors.ConversionTarget{}
	}
	out := make([]map[string]any, 0, len(list))
	for _, t := range list {
		types := t.EventTypes
		if types == nil {
			types = []string{}
		}
		out = append(out, map[string]any{"id": t.ID, "name": t.Name, "event_types": types})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "setup_needed": need, "integration_id": o.Integration.ID})
}

type adConversionRule struct {
	ID               string     `json:"id"`
	AdAccountID      string     `json:"ad_account_id"`
	Connector        string     `json:"connector"`
	EventName        string     `json:"event_name"`
	TargetID         string     `json:"target_id"`
	TargetName       string     `json:"target_name"`
	EventType        string     `json:"event_type"`
	ValueProperty    string     `json:"value_property"`
	ValueUnit        string     `json:"value_unit"`
	FixedValueMicros *int64     `json:"fixed_value_micros"`
	Currency         string     `json:"currency"`
	Enabled          bool       `json:"enabled"`
	Sent30d          int64      `json:"sent_30d"`
	Skipped30d       int64      `json:"skipped_30d"`
	Failed30d        int64      `json:"failed_30d"`
	Queued           int64      `json:"queued"`
	LastSentAt       *time.Time `json:"last_sent_at"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

const adRuleSelect = `SELECT r.id, r.ad_account_id, a.connector, r.event_name, r.target_id, r.target_name, r.event_type,
		r.value_property, r.value_unit, r.fixed_value_micros, r.currency, r.enabled,
		COALESCE(st.sent, 0), COALESCE(st.skipped, 0), COALESCE(st.failed, 0), COALESCE(st.queued, 0), st.last_sent_at,
		r.created_at, r.updated_at
	FROM ad_conversion_rules r JOIN ad_accounts a ON a.id = r.ad_account_id
	LEFT JOIN LATERAL (
		SELECT count(*) FILTER (WHERE status = 'sent' AND created_at > now() - interval '30 days') AS sent,
			count(*) FILTER (WHERE status = 'skipped' AND created_at > now() - interval '30 days') AS skipped,
			count(*) FILTER (WHERE status = 'failed' AND created_at > now() - interval '30 days') AS failed,
			count(*) FILTER (WHERE status = 'queued') AS queued,
			max(sent_at) AS last_sent_at
		FROM ad_conversion_sends q WHERE q.workspace_id = r.workspace_id AND q.rule_id = r.id
	) st ON true`

func scanAdRule(row pgx.Row) (adConversionRule, error) {
	var x adConversionRule
	err := row.Scan(&x.ID, &x.AdAccountID, &x.Connector, &x.EventName, &x.TargetID, &x.TargetName, &x.EventType,
		&x.ValueProperty, &x.ValueUnit, &x.FixedValueMicros, &x.Currency, &x.Enabled,
		&x.Sent30d, &x.Skipped30d, &x.Failed30d, &x.Queued, &x.LastSentAt, &x.CreatedAt, &x.UpdatedAt)
	return x, err
}

func (s *Server) adRule(ctx context.Context, q pgx.Tx, ws, id string) (adConversionRule, error) {
	return scanAdRule(q.QueryRow(ctx, adRuleSelect+` WHERE r.workspace_id = $1 AND r.id = $2`, ws, id))
}

func (s *Server) listAdConversionRules(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	acct := r.URL.Query().Get("account_id")
	if acct != "" && !isUUID(acct) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "account_id must be an ad account's id")
		return
	}
	rows, err := s.pool.Query(r.Context(), adRuleSelect+` WHERE r.workspace_id = $1 AND ($2 = '' OR r.ad_account_id::text = $2)
		ORDER BY a.connector, r.event_name, r.created_at, r.id`, a.WorkspaceID, acct)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	defer rows.Close()
	out := []adConversionRule{}
	for rows.Next() {
		x, err := scanAdRule(rows)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		out = append(out, x)
	}
	if rows.Err() != nil {
		s.internal(w, r, rows.Err())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// adRuleInput is a rule's settable fields; nil leaves a field as it is.
type adRuleInput struct {
	AccountID        *string `json:"account_id"`
	EventName        *string `json:"event_name"`
	TargetID         *string `json:"target_id"`
	EventType        *string `json:"event_type"`
	ValueProperty    *string `json:"value_property"`
	ValueUnit        *string `json:"value_unit"`
	FixedValueMicros *int64  `json:"fixed_value_micros"`
	Currency         *string `json:"currency"`
	Enabled          *bool   `json:"enabled"`
}

// check validates the plain fields, answering the error itself.
func (in *adRuleInput) check(w http.ResponseWriter, r *http.Request) bool {
	bad := func(msg string) bool {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return false
	}
	if in.EventName != nil {
		v := strings.TrimSpace(*in.EventName)
		if !adEventName.MatchString(v) || len(v) > 100 {
			return bad("event_name is a customer event's name, such as signed_up or order_completed")
		}
		in.EventName = &v
	}
	if in.ValueProperty != nil {
		v := strings.TrimSpace(*in.ValueProperty)
		if len(v) > 100 {
			return bad("value_property is at most 100 characters")
		}
		in.ValueProperty = &v
	}
	if in.ValueUnit != nil && *in.ValueUnit != "major" && *in.ValueUnit != "minor" {
		return bad("value_unit is major (12.50) or minor (1250)")
	}
	if in.FixedValueMicros != nil && *in.FixedValueMicros < 0 {
		return bad("fixed_value_micros is 0 or more")
	}
	if in.Currency != nil {
		v := strings.ToUpper(strings.TrimSpace(*in.Currency))
		if v != "" && !adCurrency.MatchString(v) {
			return bad("currency is a three-letter code such as USD, or empty for the event's or the account's")
		}
		in.Currency = &v
	}
	return true
}

// target finds the rule's conversion on the platform, so a rule never
// points at something the platform doesn't have.
func (s *Server) adRuleTarget(w http.ResponseWriter, r *http.Request, ws, accountID, targetID, eventType string) (connectors.ConversionTarget, bool) {
	o, sender, acct, ok := s.adConversionAccount(w, r, ws, accountID)
	if !ok {
		return connectors.ConversionTarget{}, false
	}
	list, err := sender.ConversionTargets(r.Context(), o.Client, o.Creds, acct)
	if err != nil {
		s.connectorFailed(w, r, err)
		return connectors.ConversionTarget{}, false
	}
	for _, t := range list {
		if t.ID != targetID {
			continue
		}
		if len(t.EventTypes) > 0 && !slices.Contains(t.EventTypes, eventType) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "event_type is one of "+strings.Join(t.EventTypes, ", "))
			return t, false
		}
		if len(t.EventTypes) == 0 && eventType != "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "this conversion takes no event_type")
			return t, false
		}
		return t, true
	}
	writeError(w, r, http.StatusBadRequest, "invalid_request", "target_id is not a conversion this ad account has: list them with GET .../ads/conversion-targets")
	return connectors.ConversionTarget{}, false
}

func (s *Server) createAdConversionRule(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	var in adRuleInput
	if !decode(w, r, &in) || !in.check(w, r) {
		return
	}
	if in.AccountID == nil || in.EventName == nil || in.TargetID == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "account_id, event_name and target_id are required")
		return
	}
	eventType := ""
	if in.EventType != nil {
		eventType = strings.TrimSpace(*in.EventType)
	}
	t, ok := s.adRuleTarget(w, r, a.WorkspaceID, *in.AccountID, *in.TargetID, eventType)
	if !ok {
		return
	}
	if eventType == "" && len(t.EventTypes) > 0 {
		eventType = t.EventTypes[0]
	}
	str := func(p *string, def string) string {
		if p == nil {
			return def
		}
		return *p
	}
	enabled := in.Enabled == nil || *in.Enabled
	var createdBy *string
	if a.ActorID != "" {
		createdBy = &a.ActorID
	}
	var out adConversionRule
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(r.Context(), `INSERT INTO ad_conversion_rules (workspace_id, ad_account_id, event_name, target_id, target_name, event_type,
				value_property, value_unit, fixed_value_micros, currency, enabled, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id`,
			a.WorkspaceID, *in.AccountID, *in.EventName, t.ID, trimTo(t.Name, 300), eventType,
			str(in.ValueProperty, ""), str(in.ValueUnit, "major"), in.FixedValueMicros, str(in.Currency, ""), enabled, createdBy).Scan(&id)
		if err != nil {
			return err
		}
		if out, err = s.adRule(r.Context(), tx, a.WorkspaceID, id); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "ad_conversion_rule.created", "ad_conversion_rule", id,
			map[string]any{"connector": out.Connector, "ad_account_id": out.AdAccountID, "event_type": eventType, "has_value": out.ValueProperty != "" || out.FixedValueMicros != nil})
	})
	if isUnique(err) {
		writeError(w, r, http.StatusConflict, "conflict", "this ad account already sends that event to that conversion")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) updateAdConversionRule(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var in adRuleInput
	if !decode(w, r, &in) || !in.check(w, r) {
		return
	}
	if !isUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "conversion rule not found")
		return
	}
	if in.AccountID != nil || in.TargetID != nil || in.EventType != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a rule's account, conversion and event type are fixed: add a new rule instead")
		return
	}
	var out adConversionRule
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `UPDATE ad_conversion_rules SET event_name = COALESCE($3, event_name),
				value_property = COALESCE($4, value_property), value_unit = COALESCE($5, value_unit),
				fixed_value_micros = COALESCE($6, fixed_value_micros), currency = COALESCE($7, currency),
				enabled = COALESCE($8, enabled), updated_at = now()
			WHERE workspace_id = $1 AND id = $2`,
			a.WorkspaceID, id, in.EventName, in.ValueProperty, in.ValueUnit, in.FixedValueMicros, in.Currency, in.Enabled)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		if in.Enabled != nil && !*in.Enabled {
			// Switched off: what was waiting is not sent later.
			if _, err := tx.Exec(r.Context(), `UPDATE ad_conversion_sends SET status = 'skipped', reason = 'the rule was switched off', updated_at = now()
				WHERE workspace_id = $1 AND rule_id = $2 AND status = 'queued'`, a.WorkspaceID, id); err != nil {
				return err
			}
		}
		if out, err = s.adRule(r.Context(), tx, a.WorkspaceID, id); err != nil {
			return err
		}
		meta := map[string]any{"connector": out.Connector}
		if in.Enabled != nil {
			meta["enabled"] = *in.Enabled
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "ad_conversion_rule.updated", "ad_conversion_rule", id, meta)
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, r, http.StatusNotFound, "not_found", "conversion rule not found")
	case isUnique(err):
		writeError(w, r, http.StatusConflict, "conflict", "this ad account already sends that event to that conversion")
	case err != nil:
		s.internal(w, r, err)
	default:
		writeJSON(w, http.StatusOK, out)
	}
}

func (s *Server) deleteAdConversionRule(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !isUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "conversion rule not found")
		return
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var connector string
		err := tx.QueryRow(r.Context(), `DELETE FROM ad_conversion_rules r USING ad_accounts a
			WHERE r.workspace_id = $1 AND r.id = $2 AND a.id = r.ad_account_id RETURNING a.connector`, a.WorkspaceID, id).Scan(&connector)
		if err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "ad_conversion_rule.deleted", "ad_conversion_rule", id, map[string]any{"connector": connector})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "conversion rule not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type adConversionSend struct {
	ID          string     `json:"id"`
	RuleID      string     `json:"rule_id"`
	Connector   string     `json:"connector"`
	EventName   string     `json:"event_name"`
	RecordID    *string    `json:"record_id"`
	EventID     string     `json:"event_id"`
	OccurredAt  time.Time  `json:"occurred_at"`
	Status      string     `json:"status"`
	Reason      string     `json:"reason"`
	MatchKeys   []string   `json:"match_keys"`
	ValueMicros *int64     `json:"value_micros"`
	Currency    string     `json:"currency"`
	Attempts    int        `json:"attempts"`
	SentAt      *time.Time `json:"sent_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

var adSendStatuses = []string{"queued", "sent", "skipped", "failed"}

// listAdConversionSends is the send log, newest first: every event a rule
// matched, what was sent (which kinds of match keys, never their values)
// or why not.
func (s *Server) listAdConversionSends(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	ruleID, status, recordID := q.Get("rule_id"), q.Get("status"), q.Get("record_id")
	if (ruleID != "" && !isUUID(ruleID)) || (recordID != "" && !isUUID(recordID)) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "rule_id and record_id are ids")
		return
	}
	if status != "" && !slices.Contains(adSendStatuses, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is "+strings.Join(adSendStatuses, ", "))
		return
	}
	before, ok := beforeTime(w, r)
	if !ok {
		return
	}
	limit, ok := pageLimit(w, r, 50, 200)
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT q.id, q.rule_id, a.connector, x.event_name, q.record_id, q.event_id, q.occurred_at, q.status, q.reason, q.match_keys,
			q.value_micros, q.currency, q.attempts, q.sent_at, q.created_at
		FROM ad_conversion_sends q JOIN ad_conversion_rules x ON x.id = q.rule_id JOIN ad_accounts a ON a.id = x.ad_account_id
		WHERE q.workspace_id = $1 AND ($2 = '' OR q.rule_id::text = $2) AND ($3 = '' OR q.status = $3)
		  AND ($4 = '' OR q.record_id::text = $4) AND ($5::timestamptz IS NULL OR q.created_at < $5)
		ORDER BY q.created_at DESC, q.id DESC LIMIT $6`, a.WorkspaceID, ruleID, status, recordID, before, limit)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	defer rows.Close()
	out := []adConversionSend{}
	for rows.Next() {
		var x adConversionSend
		if err := rows.Scan(&x.ID, &x.RuleID, &x.Connector, &x.EventName, &x.RecordID, &x.EventID, &x.OccurredAt, &x.Status, &x.Reason, &x.MatchKeys,
			&x.ValueMicros, &x.Currency, &x.Attempts, &x.SentAt, &x.CreatedAt); err != nil {
			s.internal(w, r, err)
			return
		}
		out = append(out, x)
	}
	if rows.Err() != nil {
		s.internal(w, r, rows.Err())
		return
	}
	var next *string
	if len(out) == limit {
		t := out[len(out)-1].CreatedAt.Format(time.RFC3339Nano)
		next = &t
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "next_before": next})
}

type adSettings struct {
	ConsentMode string     `json:"consent_mode"`
	UpdatedAt   *time.Time `json:"updated_at"`
}

func (s *Server) adSettings(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, ws string) (adSettings, error) {
	out := adSettings{ConsentMode: adconv.OptOut}
	err := q.QueryRow(ctx, `SELECT consent_mode, updated_at FROM ad_settings WHERE workspace_id = $1`, ws).Scan(&out.ConsentMode, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return out, err
}

func (s *Server) getAdSettings(w http.ResponseWriter, r *http.Request) {
	out, err := s.adSettings(r.Context(), s.pool, accessFrom(r.Context()).WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) setAdSettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		ConsentMode *string `json:"consent_mode"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ConsentMode == nil || (*in.ConsentMode != adconv.OptOut && *in.ConsentMode != adconv.OptIn) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("consent_mode is %s or %s", adconv.OptOut, adconv.OptIn))
		return
	}
	var out adSettings
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `INSERT INTO ad_settings (workspace_id, consent_mode) VALUES ($1, $2)
			ON CONFLICT (workspace_id) DO UPDATE SET consent_mode = EXCLUDED.consent_mode, updated_at = now()`, a.WorkspaceID, *in.ConsentMode); err != nil {
			return err
		}
		var err error
		if out, err = s.adSettings(r.Context(), tx, a.WorkspaceID); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "ad_settings.updated", "workspace", a.WorkspaceID, map[string]any{"consent_mode": out.ConsentMode})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
