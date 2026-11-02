package api

import (
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/lifecycle"
)

// Lifecycle stages per product (docs/product-lifecycle.md): a brand turns them
// on, edits its stages and the moves between them, and reads where a
// company or person stands. internal/lifecycle makes the moves.

func (s *Server) routesLifecycle(mux *http.ServeMux) {
	read, catalog := auth.ScopeCustomersRead, auth.ScopeCatalogManage
	base := "/v1/brands/{brand}/lifecycle"
	mux.HandleFunc("GET "+base, s.onBrand(read, s.getLifecycle))
	mux.HandleFunc("PATCH "+base, s.onBrand(catalog, s.updateLifecycle))
	mux.HandleFunc("PUT "+base+"/stages", s.onBrand(catalog, s.putLifecycleStages))
	mux.HandleFunc("PUT "+base+"/moves", s.onBrand(catalog, s.putLifecycleMoves))
	mux.HandleFunc("GET /v1/brands/{brand}/records/{record}/lifecycle", s.onBrand(read, s.recordLifecycle))
}

var lifecycleSources = []string{"billing", "api", "event", "manual"}

type lifecycleView struct {
	Enabled   bool              `json:"enabled"`
	EnabledAt *time.Time        `json:"enabled_at"`
	Sources   []string          `json:"sources"`
	Stages    []lifecycle.Stage `json:"stages"`
	Moves     []lifecycle.Move  `json:"moves"`
}

func readLifecycle(r *http.Request, tx pgx.Tx, a *access) (lifecycleView, error) {
	ctx := r.Context()
	v := lifecycleView{Sources: lifecycleSources}
	err := tx.QueryRow(ctx, `SELECT enabled_at, sources FROM lifecycle_settings WHERE brand_id = $1 AND workspace_id = $2`, a.Brand, a.WorkspaceID).
		Scan(&v.EnabledAt, &v.Sources)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return v, err
	}
	v.Enabled = v.EnabledAt != nil
	rows, err := tx.Query(ctx, `SELECT level, key, name, category, rank FROM lifecycle_stages WHERE brand_id = $1 ORDER BY level, rank DESC, key`, a.Brand)
	if err != nil {
		return v, err
	}
	if v.Stages, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (lifecycle.Stage, error) {
		var s lifecycle.Stage
		return s, row.Scan(&s.Level, &s.Key, &s.Name, &s.Category, &s.Rank)
	}); err != nil {
		return v, err
	}
	if v.Moves, err = lifecycle.LoadMoves(ctx, tx, a.Brand); err != nil {
		return v, err
	}
	v.Stages, v.Moves = nonNil(v.Stages), nonNil(v.Moves)
	for i := range v.Moves {
		v.Moves[i].FromStages = nonNil(v.Moves[i].FromStages)
	}
	return v, nil
}

func (s *Server) getLifecycle(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var v lifecycleView
	err := s.readTx(r.Context(), func(tx pgx.Tx) (err error) {
		v, err = readLifecycle(r, tx, a)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// updateLifecycle turns stages on or off and sets where products come
// from. Turning them on starts from nothing (greenfield): events from
// before never move anyone. A brand with no stages gets the defaults.
func (s *Server) updateLifecycle(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var f struct {
		Enabled *bool     `json:"enabled"`
		Sources *[]string `json:"sources"`
	}
	if !decode(w, r, &f) {
		return
	}
	if f.Sources != nil {
		for _, src := range *f.Sources {
			if !slices.Contains(lifecycleSources, src) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "sources are billing, api, event and manual")
				return
			}
		}
	}
	ctx := r.Context()
	var v lifecycleView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var was *time.Time
		err := tx.QueryRow(ctx, `INSERT INTO lifecycle_settings (brand_id, workspace_id) VALUES ($1, $2)
			ON CONFLICT (brand_id) DO UPDATE SET updated_at = lifecycle_settings.updated_at RETURNING enabled_at`, a.Brand, a.WorkspaceID).Scan(&was)
		if err != nil {
			return err
		}
		if f.Sources != nil {
			if _, err := tx.Exec(ctx, `UPDATE lifecycle_settings SET sources = $2, updated_at = now() WHERE brand_id = $1`, a.Brand, *f.Sources); err != nil {
				return err
			}
		}
		action := "lifecycle.updated"
		if f.Enabled != nil && *f.Enabled != (was != nil) {
			if *f.Enabled {
				action = "lifecycle.enabled"
				if err := lifecycle.Seed(ctx, tx, a.WorkspaceID, a.Brand); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE lifecycle_settings SET enabled_at = now(), updated_at = now() WHERE brand_id = $1`, a.Brand); err != nil {
					return err
				}
			} else {
				action = "lifecycle.disabled"
				if _, err := tx.Exec(ctx, `UPDATE lifecycle_settings SET enabled_at = NULL, updated_at = now() WHERE brand_id = $1`, a.Brand); err != nil {
					return err
				}
			}
		}
		if v, err = readLifecycle(r, tx, a); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "brand", a.Brand,
			map[string]any{"enabled": v.Enabled, "sources": v.Sources, "stages": len(v.Stages), "moves": len(v.Moves)})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

var lifecycleKeyRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

var stageCategories = []string{lifecycle.CatLead, lifecycle.CatTrial, lifecycle.CatActive, lifecycle.CatAtRisk, lifecycle.CatChurned}

// putLifecycleStages replaces a brand's stages at one or both levels. A
// stage someone is in, or a move uses, can't be removed.
func (s *Server) putLifecycleStages(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var f struct {
		Stages []lifecycle.Stage `json:"stages"`
	}
	if !decode(w, r, &f) {
		return
	}
	levels := map[string]bool{}
	seen := map[string]bool{}
	for _, st := range f.Stages {
		switch {
		case st.Level != lifecycle.LevelCompany && st.Level != lifecycle.LevelPerson:
			writeError(w, r, http.StatusBadRequest, "invalid_request", "a stage's level is company or person")
			return
		case !lifecycleKeyRE.MatchString(st.Key) || st.Key == lifecycle.None:
			writeError(w, r, http.StatusBadRequest, "invalid_request", "a stage's key is lowercase letters, digits and underscores, such as at_risk (not none)")
			return
		case strings.TrimSpace(st.Name) == "" || len(st.Name) > 100:
			writeError(w, r, http.StatusBadRequest, "invalid_request", "a stage's name is 1 to 100 characters")
			return
		case !slices.Contains(stageCategories, st.Category):
			writeError(w, r, http.StatusBadRequest, "invalid_request", "a stage's category is lead, trial, active, at_risk or churned")
			return
		case st.Rank < 0 || st.Rank > 1000:
			writeError(w, r, http.StatusBadRequest, "invalid_request", "a stage's rank is 0 to 1000; the highest wins a company's overall status")
			return
		case seen[st.Level+":"+st.Key]:
			writeError(w, r, http.StatusBadRequest, "invalid_request", "stage "+st.Key+" is listed twice at level "+st.Level)
			return
		}
		seen[st.Level+":"+st.Key], levels[st.Level] = true, true
	}
	if len(f.Stages) == 0 || len(f.Stages) > 50 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send 1 to 50 stages")
		return
	}
	ctx := r.Context()
	var v lifecycleView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for level := range levels {
			var keep []string
			for _, st := range f.Stages {
				if st.Level == level {
					keep = append(keep, st.Key)
				}
			}
			var used string
			if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT stage FROM lifecycle_states WHERE brand_id = $1 AND level = $2 AND NOT stage = ANY($3) LIMIT 1),
					(SELECT m.to_stage FROM lifecycle_moves m WHERE m.brand_id = $1 AND m.level = $2 AND NOT m.to_stage = ANY($3) LIMIT 1),
					(SELECT f FROM lifecycle_moves m, unnest(m.from_stages) f WHERE m.brand_id = $1 AND m.level = $2 AND f <> 'none' AND NOT f = ANY($3) LIMIT 1), '')`,
				a.Brand, level, keep).Scan(&used); err != nil {
				return err
			}
			if used != "" {
				return errConflictMsg("stage " + used + " is in use (someone is in it, or a move uses it); change the moves first")
			}
			if _, err := tx.Exec(ctx, `DELETE FROM lifecycle_stages WHERE brand_id = $1 AND level = $2 AND NOT key = ANY($3)`, a.Brand, level, keep); err != nil {
				return err
			}
		}
		for _, st := range f.Stages {
			if _, err := tx.Exec(ctx, `INSERT INTO lifecycle_stages (workspace_id, brand_id, level, key, name, category, rank) VALUES ($1, $2, $3, $4, $5, $6, $7)
				ON CONFLICT (brand_id, level, key) DO UPDATE SET name = EXCLUDED.name, category = EXCLUDED.category, rank = EXCLUDED.rank, updated_at = now()`,
				a.WorkspaceID, a.Brand, st.Level, st.Key, strings.TrimSpace(st.Name), st.Category, st.Rank); err != nil {
				return err
			}
		}
		var err error
		if v, err = readLifecycle(r, tx, a); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "lifecycle.stages_changed", "brand", a.Brand, map[string]any{"stages": len(v.Stages)})
	})
	if s.productError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

var lifecycleKinds = []string{lifecycle.KindEvent, lifecycle.KindUsage, lifecycle.KindInactivity}

// putLifecycleMoves replaces a brand's moves at the levels sent. Moves are
// tried in the order sent: the first that fits an event wins.
func (s *Server) putLifecycleMoves(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var f struct {
		Moves []lifecycle.Move `json:"moves"`
	}
	if !decode(w, r, &f) {
		return
	}
	if len(f.Moves) == 0 || len(f.Moves) > 100 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send 1 to 100 moves")
		return
	}
	ctx := r.Context()
	var v lifecycleView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := readLifecycle(r, tx, a)
		if err != nil {
			return err
		}
		stages := map[string]bool{}
		for _, st := range cur.Stages {
			stages[st.Level+":"+st.Key] = true
		}
		levels := map[string]bool{}
		seen := map[string]bool{}
		for i := range f.Moves {
			m := &f.Moves[i]
			if msg := checkMove(*m, stages); msg != "" {
				return errInvalid(msg)
			}
			if seen[m.Level+":"+m.Key] {
				return errInvalid("move " + m.Key + " is listed twice at level " + m.Level)
			}
			seen[m.Level+":"+m.Key], levels[m.Level] = true, true
			m.Position = i
		}
		for level := range levels {
			if _, err := tx.Exec(ctx, `DELETE FROM lifecycle_moves WHERE brand_id = $1 AND level = $2`, a.Brand, level); err != nil {
				return err
			}
		}
		for _, m := range f.Moves {
			if err := lifecycle.InsertMove(ctx, tx, a.WorkspaceID, a.Brand, m); err != nil {
				return err
			}
		}
		// New inactivity moves change when clocks run out.
		if err := lifecycle.Recheck(ctx, tx, a.Brand, ""); err != nil {
			return err
		}
		if v, err = readLifecycle(r, tx, a); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "lifecycle.moves_changed", "brand", a.Brand, map[string]any{"moves": len(v.Moves)})
	})
	if s.productError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// checkMove says what is wrong with a move, or "".
func checkMove(m lifecycle.Move, stages map[string]bool) string {
	switch {
	case m.Level != lifecycle.LevelCompany && m.Level != lifecycle.LevelPerson:
		return "a move's level is company or person"
	case !lifecycleKeyRE.MatchString(m.Key):
		return "a move's key is lowercase letters, digits and underscores, such as churned"
	case strings.TrimSpace(m.Name) == "" || len(m.Name) > 100:
		return "a move's name is 1 to 100 characters"
	case !slices.Contains(lifecycleKinds, m.Kind):
		return "a move's kind is event, usage or inactivity"
	case (m.Kind == lifecycle.KindEvent) != (m.Event != ""):
		return "an event move names its event, and only an event move does"
	case m.Event != "" && !validEventName(m.Event):
		return "event is an event name, such as subscription.canceled"
	case (m.Kind == lifecycle.KindInactivity) != (m.After != ""):
		return "an inactivity move has after (at_risk_days or churn_days), and only an inactivity move does"
	case m.After != "" && m.After != "at_risk_days" && m.After != "churn_days":
		return "after is at_risk_days or churn_days"
	case m.ChurnMode != "" && m.ChurnMode != "cancellation" && m.ChurnMode != "inactivity":
		return "churn_mode is cancellation or inactivity, or left out for every product"
	case !stages[m.Level+":"+m.ToStage]:
		return "to_stage " + m.ToStage + " is not a " + m.Level + " stage of this brand"
	case !validEventName(m.Emit):
		return "emit_event is an event name, such as product.churned"
	case len(m.FromStages) == 0 && !m.AnyStage:
		return "a move needs from_stages (none for someone with no stage yet) or any_stage"
	case len(m.Conditions) > 20:
		return "a move has at most 20 conditions"
	}
	for _, f := range m.FromStages {
		if f != lifecycle.None && !stages[m.Level+":"+f] {
			return "from stage " + f + " is not a " + m.Level + " stage of this brand"
		}
	}
	for _, c := range m.Conditions {
		if c.Property == "" || c.Attribute != "" || c.Event != "" || c.Email != "" || c.Var != "" || c.Step != "" {
			return "a move's conditions test the event's properties: property, op and value"
		}
		if !slices.Contains([]string{"exists", "not_exists", "eq", "neq", "gt", "lt", "contains"}, c.Op) {
			return "a condition's op is exists, not_exists, eq, neq, gt, lt or contains"
		}
	}
	return ""
}

var eventNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*([._][a-z0-9_]+)*$`)

func validEventName(s string) bool { return len(s) <= 100 && eventNameRE.MatchString(s) }

type lifecycleStateView struct {
	ProductID  string     `json:"product_id"`
	ProductKey string     `json:"product_key"`
	Level      string     `json:"level"`
	Stage      string     `json:"stage"`
	Category   *string    `json:"category"`
	EnteredAt  time.Time  `json:"entered_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	DueAt      *time.Time `json:"due_at"`
}

type lifecycleTransitionView struct {
	ID        string    `json:"id"`
	ProductID *string   `json:"product_id"`
	Level     string    `json:"level"`
	Move      string    `json:"move"`
	FromStage *string   `json:"from_stage"`
	ToStage   string    `json:"to_stage"`
	EventID   *string   `json:"event_id"`
	At        time.Time `json:"at"`
}

// recordLifecycle is where a company or person stands with each product,
// the company's overall status, and the latest moves.
func (s *Server) recordLifecycle(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	out := map[string]any{}
	err := s.readTx(ctx, func(tx pgx.Tx) error {
		rec, err := loadRecord(ctx, tx, a, r.PathValue("record"))
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT s.product_id::text, p.key, s.level, s.stage, st.category, s.entered_at, s.last_used_at, s.due_at
			FROM lifecycle_states s JOIN products p ON p.id = s.product_id
			LEFT JOIN lifecycle_stages st ON st.brand_id = s.brand_id AND st.level = s.level AND st.key = s.stage
			WHERE s.record_id = $1 AND s.workspace_id = $2 ORDER BY p.key, s.level`, rec.ID, a.WorkspaceID)
		if err != nil {
			return err
		}
		states, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (lifecycleStateView, error) {
			var v lifecycleStateView
			return v, row.Scan(&v.ProductID, &v.ProductKey, &v.Level, &v.Stage, &v.Category, &v.EnteredAt, &v.LastUsedAt, &v.DueAt)
		})
		if err != nil {
			return err
		}
		var overall *lifecycle.Summary
		var sum lifecycle.Summary
		err = tx.QueryRow(ctx, `SELECT status, flags, active_products FROM lifecycle_overall WHERE record_id = $1 AND workspace_id = $2`, rec.ID, a.WorkspaceID).
			Scan(&sum.Status, &sum.Flags, &sum.ActiveProducts)
		if err == nil {
			overall = &sum
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT id::text, product_id::text, level, move, from_stage, to_stage, event_id::text, at FROM lifecycle_transitions
			WHERE record_id = $1 AND workspace_id = $2 ORDER BY at DESC, id DESC LIMIT 50`, rec.ID, a.WorkspaceID)
		if err != nil {
			return err
		}
		moves, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (lifecycleTransitionView, error) {
			var v lifecycleTransitionView
			return v, row.Scan(&v.ID, &v.ProductID, &v.Level, &v.Move, &v.FromStage, &v.ToStage, &v.EventID, &v.At)
		})
		if err != nil {
			return err
		}
		out["record_id"], out["states"], out["overall"], out["moves"] = rec.ID, nonNil(states), overall, nonNil(moves)
		return nil
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}
