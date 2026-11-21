package api

import (
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adtrack"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/paidads"
)

// Holdout tests for paid ads (paidads.MeasureHoldout). A holdout is a
// note of which countries the brand keeps its ads out of and when; the
// brand changes the campaigns' places itself, and Outflare only counts.
// Nothing here changes a campaign.

func (s *Server) routesAdHoldouts(mux *http.ServeMux) {
	manage := auth.ScopeAnalyticsManage
	mux.HandleFunc("GET /v1/brands/{brand}/ads/holdouts", s.onBrand(auth.ScopeAnalyticsRead, s.listAdHoldouts))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/holdouts", s.onBrand(manage, s.createAdHoldout))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/holdouts/{id}/end", s.onBrand(manage, s.endAdHoldout))
	mux.HandleFunc("DELETE /v1/brands/{brand}/ads/holdouts/{id}", s.onBrand(manage, s.deleteAdHoldout))
}

const (
	adHoldoutMaxDays = 90
	adHoldoutMinDays = 7
)

var adHoldoutCountry = regexp.MustCompile(`^[A-Z]{2}$`)

type adHoldoutOut struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Connector string   `json:"connector"`
	Countries []string `json:"countries"`
	StartsOn  string   `json:"starts_on"`
	EndsOn    string   `json:"ends_on"`
	// scheduled, running or finished, from the dates.
	Status    string                `json:"status"`
	CreatedAt time.Time             `json:"created_at"`
	Result    paidads.HoldoutResult `json:"result"`

	starts, ends time.Time
}

const adHoldoutCols = `id::text, name, connector, countries, starts_on, ends_on, created_at`

func scanAdHoldout(row pgx.Row) (adHoldoutOut, error) {
	var h adHoldoutOut
	err := row.Scan(&h.ID, &h.Name, &h.Connector, &h.Countries, &h.starts, &h.ends, &h.CreatedAt)
	h.StartsOn, h.EndsOn = h.starts.Format(time.DateOnly), h.ends.Format(time.DateOnly)
	return h, err
}

// measure fills the holdout's status and numbers as of now.
func (s *Server) measureAdHoldout(r *http.Request, a *access, h *adHoldoutOut) error {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	switch {
	case today.Before(h.starts):
		h.Status = "scheduled"
	case today.After(h.ends):
		h.Status = "finished"
	default:
		h.Status = "running"
	}
	res, err := paidads.MeasureHoldout(r.Context(), s.pool, a.WorkspaceID, a.Brand, h.Connector, h.Countries, h.starts, h.ends, time.Now())
	h.Result = res
	return err
}

func (s *Server) listAdHoldouts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), `SELECT `+adHoldoutCols+` FROM ad_holdouts WHERE workspace_id = $1 AND brand_id = $2 ORDER BY starts_on DESC, created_at DESC LIMIT 50`, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (adHoldoutOut, error) { return scanAdHoldout(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for i := range list {
		if err := s.measureAdHoldout(r, a, &list[i]); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) createAdHoldout(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Name      string   `json:"name"`
		Connector string   `json:"connector"`
		Countries []string `json:"countries"`
		StartsOn  string   `json:"starts_on"`
		EndsOn    string   `json:"ends_on"`
	}
	if !decode(w, r, &in) {
		return
	}
	bad := func(msg string) { writeError(w, r, http.StatusBadRequest, "invalid_request", msg) }
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > 120 {
		bad("name is what to call the holdout, at most 120 characters")
		return
	}
	if in.Connector != "" && !slices.ContainsFunc(adtrack.Networks, func(n adtrack.Network) bool { return n.Connector == in.Connector }) {
		bad("connector is an ad network's connector, such as google_ads, or empty for every network")
		return
	}
	countries := make([]string, 0, len(in.Countries))
	for _, c := range in.Countries {
		c = strings.ToUpper(strings.TrimSpace(c))
		if !adHoldoutCountry.MatchString(c) {
			bad("countries are two-letter country codes, such as PT")
			return
		}
		if !slices.Contains(countries, c) {
			countries = append(countries, c)
		}
	}
	if len(countries) == 0 || len(countries) > 50 {
		bad("countries names 1 to 50 countries to keep the ads out of")
		return
	}
	slices.Sort(countries)
	starts, err1 := time.Parse(time.DateOnly, in.StartsOn)
	ends, err2 := time.Parse(time.DateOnly, in.EndsOn)
	if err1 != nil || err2 != nil {
		bad("starts_on and ends_on are days, as YYYY-MM-DD")
		return
	}
	if days := int(ends.Sub(starts)/(24*time.Hour)) + 1; days < adHoldoutMinDays || days > adHoldoutMaxDays {
		bad("a holdout runs for 7 to 90 days")
		return
	}
	var actor *string
	if a.ActorID != "" {
		actor = &a.ActorID
	}
	var h adHoldoutOut
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		h, err = scanAdHoldout(tx.QueryRow(r.Context(), `
			INSERT INTO ad_holdouts (workspace_id, brand_id, name, connector, countries, starts_on, ends_on, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING `+adHoldoutCols, a.WorkspaceID, a.Brand, name, in.Connector, countries, starts, ends, actor))
		if err != nil {
			return err
		}
		return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "ad_holdout.created", "ad_holdout", h.ID,
			map[string]any{"brand_id": a.Brand, "name": name, "connector": in.Connector, "countries": countries, "starts_on": h.StartsOn, "ends_on": h.EndsOn},
			map[string]any{"brand_id": a.Brand, "connector": in.Connector, "countries": len(countries)})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if err := s.measureAdHoldout(r, a, &h); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, h)
}

// endAdHoldout ends a running holdout today: the ads are back on.
func (s *Server) endAdHoldout(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !isUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "holdout not found")
		return
	}
	var h adHoldoutOut
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		h, err = scanAdHoldout(tx.QueryRow(r.Context(), `
			UPDATE ad_holdouts SET ends_on = (now() AT TIME ZONE 'UTC')::date, updated_at = now()
			WHERE id = $1 AND workspace_id = $2 AND brand_id = $3
			  AND starts_on <= (now() AT TIME ZONE 'UTC')::date AND ends_on > (now() AT TIME ZONE 'UTC')::date
			RETURNING `+adHoldoutCols, id, a.WorkspaceID, a.Brand))
		if err != nil {
			return err
		}
		return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "ad_holdout.ended", "ad_holdout", h.ID,
			map[string]any{"brand_id": a.Brand, "ends_on": h.EndsOn}, map[string]any{"brand_id": a.Brand})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		_ = s.pool.QueryRow(r.Context(), `SELECT true FROM ad_holdouts WHERE id = $1 AND workspace_id = $2 AND brand_id = $3`, id, a.WorkspaceID, a.Brand).Scan(&exists)
		if exists {
			writeError(w, r, http.StatusConflict, "conflict", "only a running holdout can be ended early")
		} else {
			writeError(w, r, http.StatusNotFound, "not_found", "holdout not found")
		}
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if err := s.measureAdHoldout(r, a, &h); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) deleteAdHoldout(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !isUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "holdout not found")
		return
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var gone string
		if err := tx.QueryRow(r.Context(), `DELETE FROM ad_holdouts WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 RETURNING id::text`, id, a.WorkspaceID, a.Brand).Scan(&gone); err != nil {
			return err
		}
		return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "ad_holdout.deleted", "ad_holdout", id,
			map[string]any{"brand_id": a.Brand}, map[string]any{"brand_id": a.Brand})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "holdout not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
