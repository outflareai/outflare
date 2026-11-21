package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/journeys"
	"github.com/outflareai/outflare/engine/internal/playbooks"
)

// Did a playbook work? Each journey its kit set up that holds some
// people or companies back at random (a random split with a holdout,
// which setting the kit up with holdout_percent adds) is compared with
// the ones it held back: what those who went through did afterwards
// against what the others did anyway. A journey that holds nobody back
// can't be measured, and says so.

// playbookLiftJourney is one of the playbook's journeys.
type playbookLiftJourney struct {
	JourneyID string `json:"journey_id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	// Measured is true when the journey holds some back.
	Measured bool `json:"measured"`
	// Step is the split that holds them back, and HoldoutPercent its share.
	Step           *string `json:"step"`
	HoldoutPercent *int    `json:"holdout_percent"`
	// Ran is everyone who went through, HeldBack the ones kept out; the
	// lift, verdict and incremental revenue are on Ran.
	Ran      *liftPath `json:"ran"`
	HeldBack *liftPath `json:"held_back"`
}

var playbookGroups = []journeys.Path{{ID: "ran"}, {ID: "held_back", Holdout: true}}

func (s *Server) routesPlaybookLift(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/playbooks/{id}/lift", s.onBrand(auth.ScopeAnalyticsRead, s.getPlaybookLift))
}

func (s *Server) getPlaybookLift(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	p, ok := playbooks.Get(r.PathValue("id"))
	if !ok {
		writeError(w, r, http.StatusNotFound, "not_found", "no playbook "+r.PathValue("id")+"; GET /v1/playbooks lists them")
		return
	}
	days, event, ok := liftQuery(w, r)
	if !ok {
		return
	}
	list := []playbookLiftJourney{}
	measured := false
	if p.Kit != "" {
		var err error
		if list, measured, err = s.journeysLift(r.Context(), a, `kit_id = $3`, p.Kit, days, event); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"playbook_id": p.ID, "days": days, "event": event, "measured": measured,
		"min_people": liftMinPeople, "journeys": list})
}

// journeysLift measures each of the brand's journeys that where picks
// (SQL on journeys, with $3 = arg): the ones that went through against
// the ones held back. measured says at least one holds some back.
func (s *Server) journeysLift(ctx context.Context, a *access, where string, arg any, days int, event string) ([]playbookLiftJourney, bool, error) {
	list := []playbookLiftJourney{}
	type found struct {
		playbookLiftJourney
		path string
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, name, status, steps FROM journeys
		WHERE workspace_id = $1 AND brand_id = $2 AND `+where+` AND status <> 'archived' ORDER BY name, id`, a.WorkspaceID, a.Brand, arg)
	if err != nil {
		return nil, false, err
	}
	all, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (found, error) {
		var f found
		var raw []byte
		if err := row.Scan(&f.JourneyID, &f.Name, &f.Status, &raw); err != nil {
			return f, err
		}
		var steps []journeys.Step
		if err := json.Unmarshal(raw, &steps); err != nil {
			return f, err
		}
		if step, path, percent, ok := journeys.Holdout(steps); ok {
			f.Measured, f.Step, f.HoldoutPercent, f.path = true, &step, &percent, path
		}
		return f, nil
	})
	if err != nil {
		return nil, false, err
	}
	measured := false
	for _, f := range all {
		if f.Measured {
			measured = true
			// Each run counts once, on the first time it took the split.
			taken := `SELECT DISTINCT ON (rs.run_id) rs.run_id::text AS run_id,
					CASE WHEN rs.outcome = $4 THEN 'held_back' ELSE 'ran' END AS outcome, rs.at, jr.record_id
				FROM journey_run_steps rs JOIN journey_runs jr ON jr.id = rs.run_id
				WHERE rs.workspace_id = $1 AND rs.journey_id = $2 AND rs.step = $3 AND rs.kind = 'split'
				ORDER BY rs.run_id, rs.at, rs.id`
			paths, err := s.liftPaths(ctx, taken, []any{a.WorkspaceID, f.JourneyID, *f.Step, f.path}, days, event, playbookGroups, "held_back")
			if err != nil {
				return nil, false, err
			}
			for _, x := range paths {
				switch x.ID {
				case "ran":
					f.Ran = x
				case "held_back":
					f.HeldBack = x
				}
			}
		}
		list = append(list, f.playbookLiftJourney)
	}
	return list, measured, nil
}
