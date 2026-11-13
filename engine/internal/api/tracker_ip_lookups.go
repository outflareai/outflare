package api

// Paid IP reputation lookups for tracker clicks (plans/ad-tracker.md, B7c;
// the owner's decision 12): each brand chooses whether its clicks are
// looked up on its workspace's own ip.score connection (IPQualityScore),
// and which: clicks a fraud signal flagged, clicks that convert, and a
// sample of the rest, up to a monthly cap. While lookups are on, the exact
// address of such a click is kept for 30 days in tracker_click_addresses:
// the owner's fraud exception, never anywhere else.

import (
	"context"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// clickAddressDays is how long a click's exact address is kept.
const clickAddressDays = 30

func (s *Server) routesTrackerIPLookups(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/ip-lookups", s.onBrand(auth.ScopeAnalyticsRead, s.getIPLookups))
	mux.HandleFunc("PUT /v1/brands/{brand}/ip-lookups", s.onBrand(auth.ScopeAnalyticsManage, s.putIPLookups))
}

type ipLookupSettings struct {
	IntegrationID *string   `json:"integration_id"`
	Enabled       bool      `json:"enabled"`
	Flagged       bool      `json:"flagged"`
	Converting    bool      `json:"converting"`
	SamplePercent float64   `json:"sample_percent"`
	MonthlyCap    int       `json:"monthly_cap"`
	Threshold     int       `json:"threshold"`
	UpdatedAt     time.Time `json:"updated_at"`
	// UsedThisMonth is how many lookups the brand's provider answered this
	// UTC month, against MonthlyCap.
	UsedThisMonth int `json:"used_this_month"`
}

// ipLookupsUsed counts the lookups a brand's provider answered this UTC
// month.
func (s *Server) ipLookupsUsed(ctx context.Context, ws, brand string) (int, error) {
	var used int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM tracker_ip_lookups
		WHERE workspace_id = $1 AND brand_id = $2 AND looked_up_at >= date_trunc('month', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`, ws, brand).Scan(&used)
	return used, err
}

// defaultIPLookups are the settings of a brand that hasn't chosen: off,
// and once on, flagged and converting clicks and 5% of the rest, up to
// 5,000 a month, flagged at a score of 75 (decision 12; IPQualityScore
// calls 75 suspicious).
func defaultIPLookups() ipLookupSettings {
	return ipLookupSettings{Flagged: true, Converting: true, SamplePercent: 5, MonthlyCap: 5000, Threshold: 75}
}

func (s *Server) loadIPLookups(ctx context.Context, q filterQuerier, ws, brand string) (ipLookupSettings, error) {
	v := defaultIPLookups()
	rows, err := q.Query(ctx, `SELECT integration_id::text, enabled, flagged, converting, sample_bp, monthly_cap, threshold, updated_at
		FROM tracker_ip_lookup_settings WHERE workspace_id = $1 AND brand_id = $2`, ws, brand)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var bp int
		if err := rows.Scan(&v.IntegrationID, &v.Enabled, &v.Flagged, &v.Converting, &bp, &v.MonthlyCap, &v.Threshold, &v.UpdatedAt); err != nil {
			return v, err
		}
		v.SamplePercent = float64(bp) / 100
	}
	return v, rows.Err()
}

func (s *Server) getIPLookups(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := s.loadIPLookups(r.Context(), s.pool, a.WorkspaceID, a.Brand)
	if err == nil {
		v.UsedThisMonth, err = s.ipLookupsUsed(r.Context(), a.WorkspaceID, a.Brand)
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type ipLookupInput struct {
	IntegrationID *string  `json:"integration_id"`
	Enabled       *bool    `json:"enabled"`
	Flagged       *bool    `json:"flagged"`
	Converting    *bool    `json:"converting"`
	SamplePercent *float64 `json:"sample_percent"`
	MonthlyCap    *int     `json:"monthly_cap"`
	Threshold     *int     `json:"threshold"`
}

func (s *Server) putIPLookups(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var in ipLookupInput
	if !decode(w, r, &in) {
		return
	}
	cur, err := s.loadIPLookups(ctx, s.pool, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if in.IntegrationID != nil {
		if *in.IntegrationID == "" {
			cur.IntegrationID = nil
		} else {
			id := *in.IntegrationID
			if s.integrations == nil || !validUUID(id) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "integration_id is one of the workspace's connections, such as its IPQualityScore account")
				return
			}
			i, err := s.integrations.Get(ctx, s.pool, a.WorkspaceID, id)
			if errors.Is(err, connectors.ErrNotFound) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "no connection with that id in this workspace")
				return
			}
			if err != nil {
				s.internal(w, r, err)
				return
			}
			if d, ok := s.integrations.Registry.Definition(i.Connector); !ok || !d.Has(connectors.CapIPScore) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", i.Name+" can't score addresses: connect IPQualityScore under Fraud checks")
				return
			}
			cur.IntegrationID = &id
		}
	}
	for dst, v := range map[*bool]*bool{&cur.Enabled: in.Enabled, &cur.Flagged: in.Flagged, &cur.Converting: in.Converting} {
		if v != nil {
			*dst = *v
		}
	}
	if in.SamplePercent != nil {
		if p := *in.SamplePercent; math.IsNaN(p) || p < 0 || p > 100 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "sample_percent is 0 to 100")
			return
		}
		cur.SamplePercent = *in.SamplePercent
	}
	if in.MonthlyCap != nil {
		if *in.MonthlyCap < 0 || *in.MonthlyCap > 10_000_000 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "monthly_cap is 0 to 10,000,000 lookups")
			return
		}
		cur.MonthlyCap = *in.MonthlyCap
	}
	if in.Threshold != nil {
		if *in.Threshold < 1 || *in.Threshold > 100 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "threshold is a risk score from 1 to 100")
			return
		}
		cur.Threshold = *in.Threshold
	}
	if cur.Enabled && cur.IntegrationID == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "lookups need a connection: connect IPQualityScore under Fraud checks and pass its integration_id")
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO tracker_ip_lookup_settings (brand_id, workspace_id, integration_id, enabled, flagged, converting, sample_bp, monthly_cap, threshold)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (brand_id) DO UPDATE SET integration_id = $3, enabled = $4, flagged = $5, converting = $6, sample_bp = $7, monthly_cap = $8,
				threshold = $9, updated_at = now()
			RETURNING updated_at`, a.Brand, a.WorkspaceID, cur.IntegrationID, cur.Enabled, cur.Flagged, cur.Converting,
			int(math.Round(cur.SamplePercent*100)), cur.MonthlyCap, cur.Threshold).Scan(&cur.UpdatedAt); err != nil {
			return err
		}
		if !cur.Enabled {
			// Off: the brand's kept addresses go at once, not in 30 days,
			// and the postbacks that waited for their lookups are queued.
			rows, err := tx.Query(ctx, `DELETE FROM tracker_click_addresses WHERE workspace_id = $1 AND brand_id = $2 RETURNING click_key, look_up`,
				a.WorkspaceID, a.Brand)
			if err != nil {
				return err
			}
			var held []string
			var key string
			var due bool
			if _, err := pgx.ForEachRow(rows, []any{&key, &due}, func() error {
				if due {
					held = append(held, key)
				}
				return nil
			}); err != nil {
				return err
			}
			if len(held) > 0 {
				if err := queueHeldPostbacks(ctx, tx, a.WorkspaceID, held); err != nil {
					return err
				}
			}
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "ip_lookups.updated", "brand", a.Brand,
			map[string]any{"brand_id": a.Brand, "enabled": cur.Enabled, "flagged": cur.Flagged, "converting": cur.Converting,
				"sample_percent": cur.SamplePercent, "monthly_cap": cur.MonthlyCap})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.redirects.clear()
	if cur.UsedThisMonth, err = s.ipLookupsUsed(ctx, a.WorkspaceID, a.Brand); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, cur)
}

// lookupPolicy is what the redirect needs of a brand's settings.
type lookupPolicy struct {
	enabled, flagged, converting bool
	sampleBP                     int
}

func (p lookupPolicy) from(v ipLookupSettings) lookupPolicy {
	return lookupPolicy{enabled: v.Enabled && v.IntegrationID != nil, flagged: v.Flagged, converting: v.Converting,
		sampleBP: int(math.Round(v.SamplePercent * 100))}
}

// keep says whether a click's address is kept, and whether it is looked
// up now (flagged or sampled) rather than only if it converts. roll is a
// number from 0 to 9,999.
func (p lookupPolicy) keep(flaggedClick bool, roll int) (keep, now bool) {
	if !p.enabled {
		return false, false
	}
	now = (p.flagged && flaggedClick) || roll < p.sampleBP
	return now || p.converting, now
}

// ipLookupResultDays is how long a lookup's result (a score, no address)
// is kept: the click log shows it for a year.
const ipLookupResultDays = 400

// PruneClickAddresses deletes kept click addresses older than 30 days, and
// lookup results older than 400.
func (s *Server) PruneClickAddresses(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM tracker_click_addresses WHERE created_at < now() - make_interval(days => $1)`, clickAddressDays)
	if err != nil {
		return 0, err
	}
	res, err := s.pool.Exec(ctx, `DELETE FROM tracker_ip_lookups WHERE looked_up_at < now() - make_interval(days => $1)`, ipLookupResultDays)
	return tag.RowsAffected() + res.RowsAffected(), err
}
