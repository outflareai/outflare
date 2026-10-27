package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adsync"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Paid media: the workspace's ad accounts on Google Ads, Meta, Microsoft
// Advertising, Reddit, X and LinkedIn, their campaigns and what each spent
// and got, from what the sync stored. Reads never call the platforms.

func (s *Server) routesAds(mux *http.ServeMux) {
	read := auth.ScopeAnalyticsRead
	mux.HandleFunc("GET /v1/workspaces/{ws}/ads/connections", s.inWorkspace(read, s.listAdConnections))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/ads/connections/{id}/setup", s.inWorkspace(auth.ScopeIntegrationsManage, s.setAdConnectionSetup))
	mux.HandleFunc("GET /v1/workspaces/{ws}/ads/accounts", s.inBrand(read, s.listAdAccounts))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/ads/accounts/{id}", s.inWorkspace(auth.ScopeIntegrationsManage, s.updateAdAccount))
	mux.HandleFunc("GET /v1/workspaces/{ws}/ads/campaigns", s.inBrand(read, s.listAdCampaigns))
	mux.HandleFunc("GET /v1/workspaces/{ws}/ads/summary", s.inBrand(read, s.adsSummary))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ads/sync", s.inWorkspace(read, s.syncAds))
}

// ---- connections ----

type adConnection struct {
	ID        string             `json:"id"`
	Connector string             `json:"connector"`
	Name      string             `json:"name"`
	Status    string             `json:"status"`
	Setup     []connectors.Field `json:"setup_needed"`
	Accounts  int                `json:"accounts"`
	LastRunAt *time.Time         `json:"last_run_at"`
	LastOKAt  *time.Time         `json:"last_ok_at"`
	LastError *string            `json:"last_error"`
}

// setupDone is the connection config key the setup endpoint sets, so
// listing connections needs no credentials.
const setupDone = "setup_done"

func (s *Server) adConnections(ctx context.Context, ws, id string) ([]adConnection, error) {
	ids := adsync.Connectors(s.integrations.Registry)
	rows, err := s.pool.Query(ctx, `
		SELECT i.id, i.connector, i.name, i.status, COALESCE((i.config->>'`+setupDone+`')::boolean, false),
			(SELECT count(*) FROM ad_accounts a WHERE a.integration_id = i.id AND a.status = 'active'),
			y.last_run_at, y.last_ok_at, COALESCE(y.last_error, i.last_error)
		FROM integrations i LEFT JOIN ad_syncs y ON y.integration_id = i.id
		WHERE i.workspace_id = $1 AND i.connector = ANY($2) AND ($3 = '' OR i.id::text = $3)
		ORDER BY i.created_at, i.id`, ws, ids, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []adConnection{}
	for rows.Next() {
		var c adConnection
		var done bool
		if err := rows.Scan(&c.ID, &c.Connector, &c.Name, &c.Status, &done, &c.Accounts, &c.LastRunAt, &c.LastOKAt, &c.LastError); err != nil {
			return nil, err
		}
		c.Setup = []connectors.Field{}
		if conn, ok := s.integrations.Registry.Get(c.Connector); ok && !done {
			if n, ok := conn.(connectors.SetupNeeder); ok {
				c.Setup = n.SetupFields()
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Server) listAdConnections(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	list, err := s.adConnections(r.Context(), accessFrom(r.Context()).WorkspaceID, "")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// setAdConnectionSetup adds what a platform needs after its sign-in (a
// Google Ads or Microsoft Advertising developer token), sealed with the
// connection's other credentials. The token is checked with one read
// before it is kept.
func (s *Server) setAdConnectionSetup(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var req struct {
		Values map[string]string `json:"values"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !isUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "ad connection not found")
		return
	}
	o, err := s.integrations.Open(r.Context(), s.pool, a.WorkspaceID, id)
	if errors.Is(err, connectors.ErrNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "ad connection not found")
		return
	}
	if err != nil {
		s.connectorFailed(w, r, err)
		return
	}
	reader, ok := o.Connector.(connectors.AdsReader)
	needer, ok2 := o.Connector.(connectors.SetupNeeder)
	if !ok {
		writeError(w, r, http.StatusNotFound, "not_found", "ad connection not found")
		return
	}
	if !ok2 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", o.Connector.Definition().Name+" needs nothing beyond its sign-in")
		return
	}
	creds := connectors.Creds{}
	for k, v := range o.Creds {
		creds[k] = v
	}
	var names []string
	for _, f := range needer.SetupFields() {
		v := strings.TrimSpace(req.Values[f.Name])
		if v == "" && !f.Optional {
			writeError(w, r, http.StatusBadRequest, "invalid_request", f.Label+" is required")
			return
		}
		if len(v) > 4096 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", f.Label+" is too long")
			return
		}
		if v != "" {
			creds[f.Name] = v
			names = append(names, f.Name)
		}
	}
	found, err := reader.AdAccounts(r.Context(), o.Client, creds)
	if err != nil {
		s.connectorFailed(w, r, err)
		return
	}
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if err := s.integrations.UpdateCreds(r.Context(), tx, a.WorkspaceID, id, creds); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE integrations SET config = jsonb_set(config, '{`+setupDone+`}', 'true'::jsonb),
				status = 'connected', last_error = NULL, updated_at = now() WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, id); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "integration.setup_added", "integration", id,
			map[string]any{"connector": o.Connector.Definition().ID, "fields": names, "accounts": len(found)})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if s.adSync != nil {
		_, _ = adsync.Enqueue(r.Context(), s.pool, a.WorkspaceID, id, "")
	}
	list, err := s.adConnections(r.Context(), a.WorkspaceID, id)
	if err != nil || len(list) == 0 {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list[0])
}

// connectorFailed answers a platform's refusal with the catalog's code.
func (s *Server) connectorFailed(w http.ResponseWriter, r *http.Request, err error) {
	var ce *connectors.Error
	if !errors.As(err, &ce) {
		s.internal(w, r, err)
		return
	}
	switch ce.Kind {
	case connectors.ErrAuth:
		writeError(w, r, http.StatusUnprocessableEntity, "connector_auth", ce.Message)
	case connectors.ErrQuota:
		writeError(w, r, http.StatusTooManyRequests, "connector_quota", ce.Message)
	case connectors.ErrInvalid:
		writeError(w, r, http.StatusUnprocessableEntity, "connector_invalid", ce.Message)
	default:
		writeError(w, r, http.StatusBadGateway, "connector_unavailable", ce.Message)
	}
}

// ---- accounts ----

type adAccountOut struct {
	ID            string     `json:"id"`
	IntegrationID string     `json:"integration_id"`
	Connector     string     `json:"connector"`
	ExternalID    string     `json:"external_id"`
	Name          string     `json:"name"`
	Currency      string     `json:"currency"`
	TimeZone      string     `json:"time_zone"`
	BrandID       *string    `json:"brand_id"`
	SyncOn        bool       `json:"sync_on"`
	Status        string     `json:"status"`
	FirstDay      *string    `json:"first_day"`
	LastDay       *string    `json:"last_day"`
	LastSyncedAt  *time.Time `json:"last_synced_at"`
	LastError     *string    `json:"last_error"`
	CreatedAt     time.Time  `json:"created_at"`
}

const adAccountCols = `a.id, a.integration_id, a.connector, a.external_id, a.name, a.currency, a.time_zone, a.brand_id, a.sync_on, a.status,
	to_char(a.first_day, 'YYYY-MM-DD'), to_char(a.last_day, 'YYYY-MM-DD'), a.last_synced_at, a.last_error, a.created_at`

func scanAdAccount(row pgx.Row) (adAccountOut, error) {
	var x adAccountOut
	err := row.Scan(&x.ID, &x.IntegrationID, &x.Connector, &x.ExternalID, &x.Name, &x.Currency, &x.TimeZone, &x.BrandID, &x.SyncOn, &x.Status,
		&x.FirstDay, &x.LastDay, &x.LastSyncedAt, &x.LastError, &x.CreatedAt)
	return x, err
}

func (s *Server) listAdAccounts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID}
	where := ``
	if c := r.URL.Query().Get("connector"); c != "" {
		args = append(args, c)
		where += fmt.Sprintf(` AND a.connector = $%d`, len(args))
	}
	where += brand.sql(`a.brand_id`, &args)
	rows, err := s.pool.Query(r.Context(), `SELECT `+adAccountCols+` FROM ad_accounts a WHERE a.workspace_id = $1`+where+
		` ORDER BY a.status, a.connector, lower(a.name), a.id`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	defer rows.Close()
	out := []adAccountOut{}
	for rows.Next() {
		x, err := scanAdAccount(rows)
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

func (s *Server) updateAdAccount(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var req struct {
		Brand  *string `json:"brand"`
		SyncOn *bool   `json:"sync_on"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !isUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "ad account not found")
		return
	}
	var brandID *string
	if req.Brand != nil {
		switch ref := strings.ToLower(strings.TrimSpace(*req.Brand)); ref {
		case "", "none":
		default:
			bid, err := s.brandIDFor(r.Context(), a.WorkspaceID, ref)
			if errors.Is(err, errNotFound) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "brand not found: brand is a brand's id or handle, or none for shared")
				return
			}
			if err != nil {
				s.internal(w, r, err)
				return
			}
			brandID = &bid
		}
	}
	var out adAccountOut
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanAdAccount(tx.QueryRow(r.Context(), `
			UPDATE ad_accounts a SET brand_id = CASE WHEN $3 THEN $4::uuid ELSE a.brand_id END,
				sync_on = COALESCE($5, a.sync_on), updated_at = now()
			WHERE a.workspace_id = $1 AND a.id = $2 RETURNING `+adAccountCols,
			a.WorkspaceID, id, req.Brand != nil, brandID, req.SyncOn))
		if err != nil {
			return err
		}
		if req.Brand != nil {
			// Spend already in the ledger follows the account's new brand,
			// except campaigns that have a brand of their own.
			if _, err := tx.Exec(r.Context(), `UPDATE spend_entries x SET brand_id = $3 FROM ad_entities e
				WHERE x.workspace_id = $1 AND x.ad_entity_id = e.id AND e.ad_account_id = $2 AND e.brand_id IS NULL`, a.WorkspaceID, id, brandID); err != nil {
				return err
			}
		}
		meta := map[string]any{"connector": out.Connector}
		if req.Brand != nil {
			meta["brand_id"] = brandID
		}
		if req.SyncOn != nil {
			meta["sync_on"] = *req.SyncOn
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "ad_account.updated", "ad_account", id, meta)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "ad account not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if req.SyncOn != nil && *req.SyncOn && s.adSync != nil {
		_, _ = adsync.Enqueue(r.Context(), s.pool, a.WorkspaceID, out.IntegrationID, "")
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- numbers ----

// adTotals are a set of campaigns' numbers over a range, in one currency.
// Conversions and their value are what the platforms claim.
type adTotals struct {
	SpendMicros           int64    `json:"spend_micros"`
	Impressions           int64    `json:"impressions"`
	Clicks                int64    `json:"clicks"`
	Conversions           *float64 `json:"conversions"`
	ConversionValueMicros *int64   `json:"conversion_value_micros"`
	// Worked out: cost per click, click-through rate (0 to 1), cost per
	// conversion, and the platform's return on ad spend.
	CPCMicros         *int64   `json:"cpc_micros"`
	CTR               *float64 `json:"ctr"`
	CostPerConvMicros *int64   `json:"cost_per_conversion_micros"`
	PlatformROAS      *float64 `json:"platform_roas"`
}

func (t *adTotals) derive() {
	if t.Clicks > 0 {
		v := t.SpendMicros / t.Clicks
		t.CPCMicros = &v
	}
	if t.Impressions > 0 {
		v := float64(t.Clicks) / float64(t.Impressions)
		t.CTR = &v
	}
	if t.Conversions != nil && *t.Conversions > 0 {
		v := int64(float64(t.SpendMicros) / *t.Conversions)
		t.CostPerConvMicros = &v
	}
	if t.ConversionValueMicros != nil && t.SpendMicros > 0 {
		v := float64(*t.ConversionValueMicros) / float64(t.SpendMicros)
		t.PlatformROAS = &v
	}
}

const adSums = `COALESCE(sum(m.spend_micros), 0)::bigint, COALESCE(sum(m.impressions), 0)::bigint, COALESCE(sum(m.clicks), 0)::bigint,
	sum(m.conversions)::float8, sum(m.conversion_value_micros)::bigint`

type adFilter struct {
	from, to string
	where    string
	args     []any
}

// adQuery reads the shared filters: from and to (default the last 28
// days), brand, connector, account_id. $1 is the workspace, $2 and $3 the
// dates; e is ad_entities and a ad_accounts.
func (s *Server) adQuery(w http.ResponseWriter, r *http.Request) (adFilter, bool) {
	q := r.URL.Query()
	to := time.Now().UTC()
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.DateOnly, v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "to should be a date like 2026-10-24")
			return adFilter{}, false
		}
		to = t
	}
	from := to.AddDate(0, 0, -27)
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.DateOnly, v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "from should be a date like 2026-09-27")
			return adFilter{}, false
		}
		from = t
	}
	if to.Before(from) || to.Sub(from) > 3*366*24*time.Hour {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "from must be on or before to, and at most three years before it")
		return adFilter{}, false
	}
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return adFilter{}, false
	}
	f := adFilter{from: from.Format(time.DateOnly), to: to.Format(time.DateOnly)}
	f.args = []any{accessFrom(r.Context()).WorkspaceID, f.from, f.to}
	if c := q.Get("connector"); c != "" {
		f.args = append(f.args, c)
		f.where += ` AND a.connector = $` + strconv.Itoa(len(f.args))
	}
	if id := q.Get("account_id"); id != "" {
		if !isUUID(id) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "account_id is an ad account's id, from GET .../ads/accounts")
			return adFilter{}, false
		}
		f.args = append(f.args, id)
		f.where += ` AND a.id = $` + strconv.Itoa(len(f.args))
	}
	f.where += brand.sql(`COALESCE(e.brand_id, a.brand_id)`, &f.args)
	return f, true
}

type adCampaignOut struct {
	ID                string   `json:"id"`
	AdAccountID       string   `json:"ad_account_id"`
	Connector         string   `json:"connector"`
	AccountName       string   `json:"account_name"`
	ExternalID        string   `json:"external_id"`
	Name              string   `json:"name"`
	Status            string   `json:"status"`
	ProviderStatus    string   `json:"provider_status"`
	Objective         string   `json:"objective"`
	DailyBudgetMicros *int64   `json:"daily_budget_micros"`
	BrandID           *string  `json:"brand_id"`
	Currency          string   `json:"currency"`
	Totals            adTotals `json:"totals"`
}

func (s *Server) listAdCampaigns(w http.ResponseWriter, r *http.Request) {
	f, ok := s.adQuery(w, r)
	if !ok {
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit is 1 to 1000")
			return
		}
		limit = n
	}
	where := f.where
	if st := r.URL.Query().Get("status"); st != "" {
		if !slices.Contains([]string{connectors.AdActive, connectors.AdPaused, connectors.AdRemoved, connectors.AdOther}, st) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "status is active, paused, removed or other")
			return
		}
		f.args = append(f.args, st)
		where += ` AND e.status = $` + strconv.Itoa(len(f.args))
	}
	f.args = append(f.args, limit)
	rows, err := s.pool.Query(r.Context(), `
		SELECT e.id, a.id, a.connector, a.name, e.external_id, e.name, e.status, e.provider_status, e.objective, e.daily_budget_micros,
			COALESCE(e.brand_id, a.brand_id), a.currency, `+adSums+`
		FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id
		LEFT JOIN ad_metrics_daily m ON m.ad_entity_id = e.id AND m.day BETWEEN $2 AND $3
		WHERE e.workspace_id = $1 AND e.level = 'campaign'`+where+`
		GROUP BY e.id, a.id
		HAVING sum(m.spend_micros) > 0 OR e.status = 'active'
		ORDER BY COALESCE(sum(m.spend_micros), 0) DESC, lower(e.name), e.id
		LIMIT $`+strconv.Itoa(len(f.args)), f.args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	defer rows.Close()
	out := []adCampaignOut{}
	for rows.Next() {
		var c adCampaignOut
		t := &c.Totals
		if err := rows.Scan(&c.ID, &c.AdAccountID, &c.Connector, &c.AccountName, &c.ExternalID, &c.Name, &c.Status, &c.ProviderStatus, &c.Objective,
			&c.DailyBudgetMicros, &c.BrandID, &c.Currency, &t.SpendMicros, &t.Impressions, &t.Clicks, &t.Conversions, &t.ConversionValueMicros); err != nil {
			s.internal(w, r, err)
			return
		}
		t.derive()
		out = append(out, c)
	}
	if rows.Err() != nil {
		s.internal(w, r, rows.Err())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": f.from, "to": f.to, "data": out})
}

type adSummaryRow struct {
	Key      string   `json:"key"`
	Currency string   `json:"currency"`
	Totals   adTotals `json:"totals"`
}

// adGroups maps ?group= to the column a summary groups by.
var adGroups = map[string]string{
	"connector": `a.connector`,
	"brand":     `COALESCE(COALESCE(e.brand_id, a.brand_id)::text, 'none')`,
	"account":   `a.id::text`,
	"day":       `to_char(m.day, 'YYYY-MM-DD')`,
}

func (s *Server) adsSummary(w http.ResponseWriter, r *http.Request) {
	f, ok := s.adQuery(w, r)
	if !ok {
		return
	}
	group := r.URL.Query().Get("group")
	if group == "" {
		group = "connector"
	}
	col, ok := adGroups[group]
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "group is connector, brand, account or day")
		return
	}
	query := func(key string) ([]adSummaryRow, error) {
		rows, err := s.pool.Query(r.Context(), `
			SELECT `+key+`, a.currency, `+adSums+`
			FROM ad_metrics_daily m JOIN ad_entities e ON e.id = m.ad_entity_id JOIN ad_accounts a ON a.id = e.ad_account_id
			WHERE m.workspace_id = $1 AND m.day BETWEEN $2 AND $3`+f.where+`
			GROUP BY 1, 2 ORDER BY 1, 2`, f.args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []adSummaryRow{}
		for rows.Next() {
			var x adSummaryRow
			t := &x.Totals
			if err := rows.Scan(&x.Key, &x.Currency, &t.SpendMicros, &t.Impressions, &t.Clicks, &t.Conversions, &t.ConversionValueMicros); err != nil {
				return nil, err
			}
			t.derive()
			out = append(out, x)
		}
		return out, rows.Err()
	}
	data, err := query(col)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	totals, err := query(`'all'`)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": f.from, "to": f.to, "group": group, "data": data, "totals": totals,
		"notes": []string{"Money is in micros (millionths) of each account's currency; rows in different currencies are never added together.",
			"Conversions, their value and platform_roas are what each platform claims. Platforms count overlapping conversions, so never add them across platforms."}})
}

func (s *Server) syncAds(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	if s.adSync == nil {
		writeError(w, r, http.StatusServiceUnavailable, "not_configured", "the ad sync is not running on this server")
		return
	}
	ws := accessFrom(r.Context()).WorkspaceID
	list, err := s.adConnections(r.Context(), ws, "")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	queued := []string{}
	for _, c := range list {
		if c.Status != connectors.StatusConnected {
			continue
		}
		if _, err := adsync.Enqueue(r.Context(), s.pool, ws, c.ID, ""); err != nil {
			s.internal(w, r, err)
			return
		}
		queued = append(queued, c.ID)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": queued})
}
