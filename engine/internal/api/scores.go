package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/journeys"
	"github.com/outflareai/outflare/engine/internal/scores"
)

// Scores (docs/scores.md): a number from 0 to 100 a brand keeps on every
// company or person, such as a health score or a lead score, built from
// factors anyone can read. internal/scores keeps them up to date.

func (s *Server) routesScores(mux *http.ServeMux) {
	read, catalog := auth.ScopeCustomersRead, auth.ScopeCatalogManage
	base := "/v1/brands/{brand}/scores"
	mux.HandleFunc("GET "+base, s.onBrand(read, s.listScores))
	mux.HandleFunc("POST "+base, s.onBrand(catalog, s.createScore))
	mux.HandleFunc("PATCH "+base+"/{score}", s.onBrand(catalog, s.updateScore))
	mux.HandleFunc("POST "+base+"/{score}/archive", s.onBrand(catalog, s.archiveScore))
	mux.HandleFunc("GET "+base+"/{score}/track-record", s.onBrand(read, s.scoreTrackRecord))
	mux.HandleFunc("GET /v1/brands/{brand}/records/{record}/scores", s.onBrand(read, s.recordScores))
}

type scoreCounts struct {
	High   int `json:"high"`
	Medium int `json:"medium"`
	Low    int `json:"low"`
}

type scoreView struct {
	scores.Definition
	Status    string      `json:"status"`
	Scoring   bool        `json:"scoring"`
	ScoredAt  *time.Time  `json:"scored_at"`
	Counts    scoreCounts `json:"counts"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
}

const scoreColumns = scores.Columns + `, status, scoring_since IS NOT NULL, scored_at, created_at, updated_at`

func scanScore(row pgx.CollectableRow) (scoreView, error) {
	var v scoreView
	var raw, parts []byte
	d := &v.Definition
	if err := row.Scan(append(d.Dest(&raw, &parts), &v.Status, &v.Scoring, &v.ScoredAt, &v.CreatedAt, &v.UpdatedAt)...); err != nil {
		return v, err
	}
	return v, d.Fill(raw, parts)
}

// findScore reads one of the brand's scores, with how its records spread
// over the bands.
func findScore(r *http.Request, tx pgx.Tx, a *access, id string) (scoreView, error) {
	if !validUUID(id) {
		return scoreView{}, errNotFound
	}
	rows, err := tx.Query(r.Context(), `SELECT `+scoreColumns+` FROM score_definitions WHERE id = $1 AND workspace_id = $2 AND brand_id = $3`,
		id, a.WorkspaceID, a.Brand)
	if err != nil {
		return scoreView{}, err
	}
	v, err := pgx.CollectExactlyOneRow(rows, scanScore)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	if err != nil {
		return v, err
	}
	return v, countScore(r, tx, a, &v)
}

// countScore fills in how many records are in each band.
func countScore(r *http.Request, tx pgx.Tx, a *access, v *scoreView) error {
	if v.Status != "active" {
		return nil
	}
	return tx.QueryRow(r.Context(), `SELECT count(*) FILTER (WHERE band = 'high'), count(*) FILTER (WHERE band = 'medium'), count(*) FILTER (WHERE band = 'low')
		FROM (SELECT attributes->'scores'->>$4 AS band FROM records
			WHERE workspace_id = $1 AND brand_id = $2 AND record_type = $3 AND attributes ? 'scores'
			  AND merged_into IS NULL AND archived_at IS NULL AND deleted_at IS NULL) x`,
		a.WorkspaceID, a.Brand, v.RecordType, v.Key+"_band").Scan(&v.Counts.High, &v.Counts.Medium, &v.Counts.Low)
}

func (s *Server) listScores(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	status := "active"
	if r.URL.Query().Get("archived") == "true" {
		status = "archived"
	}
	var list []scoreView
	err := s.readTx(r.Context(), func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT `+scoreColumns+` FROM score_definitions WHERE workspace_id = $1 AND brand_id = $2 AND status = $3
			ORDER BY record_type, key`, a.WorkspaceID, a.Brand, status)
		if err != nil {
			return err
		}
		if list, err = pgx.CollectRows(rows, scanScore); err != nil {
			return err
		}
		for i := range list {
			if err := countScore(r, tx, a, &list[i]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// Scores to start from. A deal's chance of being won starts from its
	// stage, so that one gets a factor for each of the brand's stages.
	var stages []forecastStage
	err = s.readTx(r.Context(), func(tx pgx.Tx) error {
		c, err := loadCatalog(r.Context(), tx, a.WorkspaceID, a.Brand, false)
		if err != nil {
			return err
		}
		set, err := loadForecastSettings(r.Context(), tx, a)
		stages = forecastStages(c, set.Stages)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	starters := []scoreStarter{}
	for _, d := range scores.Starters {
		st := scoreStarter{Key: d.Key, Label: d.Label, Description: d.Description, RecordType: d.RecordType, Base: d.Base,
			Factors: d.Factors, HighFrom: d.HighFrom, LowBelow: d.LowBelow, Nature: d.Nature, Parts: []scores.Part{}, HigherIsBetter: d.HigherIsBetter, Needs: d.Needs}
		if d.Key == scores.WinKey {
			st.Base, st.Factors = winFactors(d, stages)
		}
		starters = append(starters, st)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": nonNil(list), "starters": starters})
}

// scoreStarter is a score to start from, as createScore takes it.
type scoreStarter struct {
	Key            string          `json:"key"`
	Label          string          `json:"label"`
	Description    string          `json:"description"`
	RecordType     string          `json:"record_type"`
	Base           int             `json:"base"`
	Factors        []scores.Factor `json:"factors"`
	HighFrom       int             `json:"high_from"`
	LowBelow       int             `json:"low_below"`
	Nature         string          `json:"nature"`
	Parts          []scores.Part   `json:"parts"`
	HigherIsBetter bool            `json:"higher_is_better"`
	Needs          string          `json:"needs"`
}

// winFactors is the win likelihood starter for a brand's own stages: it
// starts low and each stage adds the share of deals the forecast counts
// there, so a deal in a late stage with nothing wrong scores near that
// stage's weight.
func winFactors(d scores.Definition, stages []forecastStage) (int, []scores.Factor) {
	if len(stages) == 0 {
		return d.Base, d.Factors
	}
	out := []scores.Factor{}
	for _, st := range stages {
		if len(out) == scores.MaxFactors-len(d.Factors) {
			break
		}
		if points := st.Weight - 10; points > 0 {
			out = append(out, scores.Factor{Label: truncate("In the stage "+st.Label, 120), Match: "all", Points: points,
				Conditions: []journeys.Condition{{Attribute: "stage", Op: "eq", Value: st.Key}}})
		}
	}
	return 10, append(out, d.Factors...)
}

type scoreFields struct {
	Key            *string          `json:"key"`
	Label          *string          `json:"label"`
	Description    *string          `json:"description"`
	RecordType     *string          `json:"record_type"`
	Base           *int             `json:"base"`
	Factors        *[]scores.Factor `json:"factors"`
	HighFrom       *int             `json:"high_from"`
	LowBelow       *int             `json:"low_below"`
	Nature         *string          `json:"nature"`
	Parts          *[]scores.Part   `json:"parts"`
	HigherIsBetter *bool            `json:"higher_is_better"`
	Needs          *string          `json:"needs"`
}

// apply sets f on d and reports whether how it scores changed.
func (f scoreFields) apply(d *scores.Definition) (bool, error) {
	before := *d
	if f.Label != nil {
		d.Label = strings.TrimSpace(*f.Label)
	}
	if f.Description != nil {
		d.Description = strings.TrimSpace(*f.Description)
	}
	if f.Base != nil {
		d.Base = *f.Base
	}
	if f.HighFrom != nil {
		d.HighFrom = *f.HighFrom
	}
	if f.LowBelow != nil {
		d.LowBelow = *f.LowBelow
	}
	if f.Factors != nil {
		d.Factors = make([]scores.Factor, len(*f.Factors))
		for i, x := range *f.Factors {
			x.Label = strings.TrimSpace(x.Label)
			if x.Match == "" {
				x.Match = "all"
			}
			d.Factors[i] = x
		}
	}
	if f.Nature != nil {
		d.Nature = *f.Nature
	}
	if f.HigherIsBetter != nil {
		d.HigherIsBetter = *f.HigherIsBetter
	}
	if f.Needs != nil {
		d.Needs = strings.TrimSpace(*f.Needs)
	}
	if f.Parts != nil {
		d.Parts = append([]scores.Part{}, *f.Parts...)
	}
	if len(d.Parts) > 0 {
		// A headline score is its parts' average: it has no points of its own.
		d.Base = 0
	}
	if msg := scores.Check(*d); msg != "" {
		return false, errInvalid(msg)
	}
	return d.Base != before.Base || d.HighFrom != before.HighFrom || d.LowBelow != before.LowBelow || d.Needs != before.Needs ||
		!reflect.DeepEqual(d.Factors, before.Factors) || !reflect.DeepEqual(d.Parts, before.Parts), nil
}

// checkScoreSet checks d against the brand's other active scores for its
// record type.
func checkScoreSet(r *http.Request, tx pgx.Tx, a *access, d scores.Definition) error {
	others, err := scores.Load(r.Context(), tx, a.WorkspaceID, a.Brand, d.RecordType)
	if err != nil {
		return err
	}
	if msg := scores.CheckSet(d, others); msg != "" {
		return errInvalid(msg)
	}
	return nil
}

func scoreData(v scoreView) map[string]any {
	return map[string]any{"score_id": v.ID, "record_type": v.RecordType, "factors": len(v.Factors), "parts": len(v.Parts), "nature": v.Nature, "version": v.Version}
}

// createScore adds a score and scores every record of its type, quietly.
func (s *Server) createScore(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var f scoreFields
	if !decode(w, r, &f) {
		return
	}
	if f.Key == nil || f.Label == nil || f.RecordType == nil || (f.Factors == nil && f.Parts == nil) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "key, label, record_type and factors (or parts, for a headline score) are required")
		return
	}
	d := scores.Definition{Key: *f.Key, RecordType: *f.RecordType, Base: 50, HighFrom: 70, LowBelow: 40, Nature: scores.Measured, HigherIsBetter: true,
		Factors: []scores.Factor{}, Parts: []scores.Part{}}
	if _, err := f.apply(&d); s.allocError(w, r, err) {
		return
	}
	factors, err := json.Marshal(d.Factors)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	parts, err := json.Marshal(d.Parts)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	ctx := r.Context()
	var v scoreView
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM score_definitions WHERE workspace_id = $1 AND brand_id = $2 AND status = 'active'`,
			a.WorkspaceID, a.Brand).Scan(&n); err != nil {
			return err
		}
		if n >= scores.MaxPerBrand {
			return errInvalid(fmt.Sprintf("a brand keeps at most %d scores; archive one first", scores.MaxPerBrand))
		}
		if err := checkScoreSet(r, tx, a, d); err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO score_definitions (workspace_id, brand_id, key, label, description, record_type, base, factors, high_from, low_below, watch_fields,
				nature, parts, higher_is_better, needs)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15) RETURNING id::text`,
			a.WorkspaceID, a.Brand, d.Key, d.Label, d.Description, d.RecordType, d.Base, factors, d.HighFrom, d.LowBelow, scores.WatchFields(d),
			d.Nature, parts, d.HigherIsBetter, d.Needs).Scan(&id); err != nil {
			return err
		}
		if err := scores.EnqueuePass(ctx, tx, a.WorkspaceID, a.Brand, d.RecordType, id, 1); err != nil {
			return err
		}
		if v, err = findScore(r, tx, a, id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "score.created", "brand", a.Brand, scoreData(v))
	})
	if uniqueViolation(err) {
		writeError(w, r, http.StatusConflict, "conflict", "the brand already has a score with this key")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// updateScore changes a score. A change to how it scores makes a new
// version, and every record is scored again, quietly.
func (s *Server) updateScore(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var f scoreFields
	if !decode(w, r, &f) {
		return
	}
	if f.Key != nil || f.RecordType != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a score's key and record_type never change, so rules and lists keep working; make a new score instead")
		return
	}
	ctx := r.Context()
	var v scoreView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := findScore(r, tx, a, r.PathValue("score"))
		if err != nil {
			return err
		}
		if cur.Status != "active" {
			return errInvalid("this score is archived")
		}
		d := cur.Definition
		changed, err := f.apply(&d)
		if err != nil {
			return err
		}
		if err := checkScoreSet(r, tx, a, d); err != nil {
			return err
		}
		factors, err := json.Marshal(d.Factors)
		if err != nil {
			return err
		}
		parts, err := json.Marshal(d.Parts)
		if err != nil {
			return err
		}
		version := cur.Version
		if changed {
			version++
		}
		if _, err := tx.Exec(ctx, `UPDATE score_definitions SET label = $2, description = $3, base = $4, factors = $5, high_from = $6, low_below = $7,
				watch_fields = $8, version = $9, nature = $10, parts = $11, higher_is_better = $12, needs = $13, updated_at = now() WHERE id = $1`,
			cur.ID, d.Label, d.Description, d.Base, factors, d.HighFrom, d.LowBelow, scores.WatchFields(d), version, d.Nature, parts, d.HigherIsBetter, d.Needs); err != nil {
			return err
		}
		if changed {
			if err := scores.EnqueuePass(ctx, tx, a.WorkspaceID, a.Brand, d.RecordType, cur.ID, version); err != nil {
				return err
			}
		}
		if v, err = findScore(r, tx, a, cur.ID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "score.updated", "brand", a.Brand, scoreData(v))
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// archiveScore takes a score off every record, quietly.
func (s *Server) archiveScore(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var v scoreView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := findScore(r, tx, a, r.PathValue("score"))
		if err != nil {
			return err
		}
		if cur.Status == "active" {
			others, err := scores.Load(ctx, tx, a.WorkspaceID, a.Brand, cur.RecordType)
			if err != nil {
				return err
			}
			for _, o := range others {
				if o.ID != cur.ID && slices.Contains(o.Uses(), cur.Key) {
					return errInvalid(o.Label + " is made of or reads this score: take it out of that one first")
				}
			}
			// The key is free again for a new score.
			if _, err := tx.Exec(ctx, `UPDATE score_definitions SET status = 'archived', key = left(key, 30) || '_' || substr(id::text, 1, 8),
				version = version + 1, updated_at = now() WHERE id = $1`, cur.ID); err != nil {
				return err
			}
			if err := scores.EnqueuePass(ctx, tx, a.WorkspaceID, a.Brand, cur.RecordType, cur.ID, cur.Version+1); err != nil {
				return err
			}
		}
		if v, err = findScore(r, tx, a, cur.ID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "score.archived", "brand", a.Brand, scoreData(v))
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type recordScoreFactor struct {
	Label  string `json:"label"`
	Points int    `json:"points"`
	Holds  bool   `json:"holds"`
}

// recordScorePart is one of the scores a headline score is made of. Value
// and Band are null when the record has no value for it.
type recordScorePart struct {
	Key    string  `json:"key"`
	Label  string  `json:"label"`
	Weight int     `json:"weight"`
	Value  *int    `json:"value"`
	Band   *string `json:"band"`
}

type recordScorePoint struct {
	Day   string `json:"day"`
	Value int    `json:"value"`
}

type recordScore struct {
	ScoreID        string              `json:"score_id"`
	Key            string              `json:"key"`
	Label          string              `json:"label"`
	Description    string              `json:"description"`
	Nature         string              `json:"nature"`
	HigherIsBetter bool                `json:"higher_is_better"`
	Value          int                 `json:"value"`
	Band           string              `json:"band"`
	Base           int                 `json:"base"`
	Factors        []recordScoreFactor `json:"factors"`
	Parts          []recordScorePart   `json:"parts"`
	// PartOf is the key of the headline score this one is a part of, or
	// null.
	PartOf *string `json:"part_of"`
	// History is the value on each day it changed in the last 90 days,
	// oldest first; Change is how far it moved in the last 30 days, or
	// null when it is newer than that.
	History []recordScorePoint `json:"history"`
	Change  *int               `json:"change"`
	// Evidence is, for a score that reads sentiment, the customer's own
	// words from the latest conversations the brand's AI read.
	Evidence []recordScoreQuote `json:"evidence"`
}

type recordScoreQuote struct {
	Label   string    `json:"label"`
	Quote   string    `json:"quote"`
	Channel string    `json:"channel"`
	ReadAt  time.Time `json:"read_at"`
}

// recordScores is a company's, person's or deal's scores, each with the
// factors or parts behind it, worked out from the record as it is now,
// and where each has been.
func (s *Server) recordScores(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("record")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "not found")
		return
	}
	out := []recordScore{}
	ctx := r.Context()
	err := s.readTx(ctx, func(tx pgx.Tx) error {
		var recordType string
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT record_type, attributes FROM records WHERE id = $1 AND workspace_id = $2 AND brand_id = $3
			AND merged_into IS NULL AND deleted_at IS NULL`, id, a.WorkspaceID, a.Brand).Scan(&recordType, &raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		defs, err := scores.Load(ctx, tx, a.WorkspaceID, a.Brand, recordType)
		if err != nil {
			return err
		}
		attrs := map[string]any{}
		_ = json.Unmarshal(raw, &attrs)
		view, err := scores.View(ctx, tx, a.WorkspaceID, id, recordType, attrs)
		if err != nil {
			return err
		}
		results := scores.All(defs, view)
		byKey, partOf := map[string]scores.Definition{}, map[string]string{}
		for _, d := range defs {
			byKey[d.Key] = d
			for _, p := range d.Parts {
				if _, taken := partOf[p.Key]; !taken {
					partOf[p.Key] = d.Key
				}
			}
		}
		history := map[string][]recordScorePoint{}
		before := map[string]int{}
		rows, err := tx.Query(ctx, `SELECT score_id::text, day::text, value, day <= current_date - 30 FROM score_history
			WHERE workspace_id = $1 AND brand_id = $2 AND record_id = $3 AND day >= current_date - 120 ORDER BY day`, a.WorkspaceID, a.Brand, id)
		if err != nil {
			return err
		}
		var sid, day string
		var value int
		var old bool
		if _, err := pgx.ForEachRow(rows, []any{&sid, &day, &value, &old}, func() error {
			if old {
				before[sid] = value
			}
			history[sid] = append(history[sid], recordScorePoint{Day: day, Value: value})
			return nil
		}); err != nil {
			return err
		}
		quotes := []recordScoreQuote{}
		if slices.ContainsFunc(defs, scores.Definition.ReadsSentiment) {
			rows, err := tx.Query(ctx, `SELECT label, quote, channel, read_at FROM conversation_sentiments
				WHERE workspace_id = $1 AND brand_id = $2 AND record_id = $3 AND quote <> '' ORDER BY read_at DESC LIMIT 3`, a.WorkspaceID, a.Brand, id)
			if err != nil {
				return err
			}
			if quotes, err = pgx.CollectRows(rows, pgx.RowToStructByPos[recordScoreQuote]); err != nil {
				return err
			}
		}
		for _, d := range defs {
			res, ok := results[d.Key]
			if !ok {
				continue
			}
			rs := recordScore{Evidence: []recordScoreQuote{}, ScoreID: d.ID, Key: d.Key, Label: d.Label, Description: d.Description, Nature: d.Nature, HigherIsBetter: d.HigherIsBetter,
				Value: res.Value, Band: res.Band, Base: d.Base, Factors: []recordScoreFactor{}, Parts: []recordScorePart{}, History: history[d.ID]}
			if rs.History == nil {
				rs.History = []recordScorePoint{}
			}
			if len(rs.History) > 90 {
				rs.History = rs.History[len(rs.History)-90:]
			}
			if d.ReadsSentiment() {
				rs.Evidence = quotes
			}
			if was, ok := before[d.ID]; ok {
				change := res.Value - was
				rs.Change = &change
			}
			if head, ok := partOf[d.Key]; ok {
				if _, shown := results[head]; shown {
					rs.PartOf = &head
				}
			}
			for i, f := range d.Factors {
				rs.Factors = append(rs.Factors, recordScoreFactor{Label: f.Label, Points: f.Points, Holds: res.Held[i]})
			}
			for _, p := range d.Parts {
				part := recordScorePart{Key: p.Key, Label: byKey[p.Key].Label, Weight: p.Weight}
				if pr, ok := results[p.Key]; ok {
					part.Value, part.Band = &pr.Value, &pr.Band
				}
				rs.Parts = append(rs.Parts, part)
			}
			out = append(out, rs)
		}
		return nil
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

type trackBand struct {
	Band     string `json:"band"`
	Records  int    `json:"records"`
	Happened int    `json:"happened"`
	// Rate is the percent of them it happened to, or null with none.
	Rate *int `json:"rate"`
}

// trackEnough is how many records a track record needs before anyone
// should read much into it.
const trackEnough = 20

type scoreTrackRecord struct {
	// Outcome is what the score is checked against: won for a deal's
	// score, churned for a company's or person's score where high is bad
	// news, or null when there is nothing to check it against.
	Outcome *string     `json:"outcome"`
	Days    int         `json:"days"`
	Records int         `json:"records"`
	Enough  bool        `json:"enough"`
	Bands   []trackBand `json:"bands"`
}

// scoreTrackRecord checks a score against what really happened, so a
// prediction is never only a number. A deal's score: of the deals closed
// in the last year, how many in each band (as they stood the day before
// they closed) were won. A risk score on companies or people: of those in
// each band 90 days ago, how many canceled or were marked churned since.
func (s *Server) scoreTrackRecord(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	out := scoreTrackRecord{Bands: []trackBand{}}
	err := s.readTx(ctx, func(tx pgx.Tx) error {
		v, err := findScore(r, tx, a, r.PathValue("score"))
		if err != nil {
			return err
		}
		var sql, outcome string
		switch {
		case v.RecordType == "deal":
			outcome, out.Days = "won", 365
			sql = `SELECT h.band, count(*), count(*) FILTER (WHERE d.state = 'won')
				FROM deals d
				JOIN LATERAL (SELECT band FROM score_history h WHERE h.record_id = d.id AND h.score_id = $3 AND h.day < d.closed_at::date
					ORDER BY h.day DESC LIMIT 1) h ON true
				WHERE d.workspace_id = $1 AND d.brand_id = $2 AND d.state IN ('won', 'lost') AND d.closed_at > now() - make_interval(days => $4)
				GROUP BY h.band`
		case !v.HigherIsBetter:
			outcome, out.Days = "churned", 90
			sql = `SELECT h.band, count(*), count(*) FILTER (WHERE EXISTS (SELECT 1 FROM attribute_changes c
					WHERE c.workspace_id = r.workspace_id AND c.record_id = r.id AND c.changed_at > now() - make_interval(days => $4)
					  AND ((c.field_key = 'billing_status' AND c.new_value = '"canceled"') OR (c.field_key = 'lifecycle_stage' AND c.new_value = '"churned"'))))
				FROM records r
				JOIN LATERAL (SELECT band FROM score_history h WHERE h.record_id = r.id AND h.score_id = $3 AND h.day <= current_date - $4::int
					ORDER BY h.day DESC LIMIT 1) h ON true
				WHERE r.workspace_id = $1 AND r.brand_id = $2 AND r.merged_into IS NULL AND r.deleted_at IS NULL
				GROUP BY h.band`
		default:
			return nil
		}
		out.Outcome = &outcome
		byBand := map[string]trackBand{}
		rows, err := tx.Query(ctx, sql, a.WorkspaceID, a.Brand, v.ID, out.Days)
		if err != nil {
			return err
		}
		var b trackBand
		if _, err := pgx.ForEachRow(rows, []any{&b.Band, &b.Records, &b.Happened}, func() error {
			byBand[b.Band] = b
			return nil
		}); err != nil {
			return err
		}
		for _, band := range []string{scores.High, scores.Medium, scores.Low} {
			b := byBand[band]
			b.Band = band
			if b.Records > 0 {
				rate := (b.Happened*100 + b.Records/2) / b.Records
				b.Rate = &rate
			}
			out.Records += b.Records
			out.Bands = append(out.Bands, b)
		}
		out.Enough = out.Records >= trackEnough
		return nil
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}
