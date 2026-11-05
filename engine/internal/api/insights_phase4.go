package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/insights"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/smartlists"
)

// Insights, phase 4: saving a funnel's people as a list, what converters
// did differently, paths, and annotations.

// MaxSavedPeople is how many people one saved list takes from a funnel.
const MaxSavedPeople = 10_000

type saveFunnelPeopleInput struct {
	Funnel    funnelSpec `json:"funnel"`
	Step      int        `json:"step"`
	Dropped   bool       `json:"dropped,omitempty"`
	Breakdown *string    `json:"breakdown,omitempty"`
	// Name is the new list's name.
	Name string `json:"name"`
}

// saveFunnelPeople puts the identified people at a step, or who dropped
// off before it, into a new static list, so they can be mailed, scored
// or compared. Browsers nobody has identified have no record and are
// left out, counted as skipped.
func (s *Server) saveFunnelPeople(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in saveFunnelPeopleInput
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 200 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is 1 to 200 characters")
		return
	}
	start := time.Now()
	f, err := in.Funnel.funnel(start)
	if err == nil && f.By == insights.ByCompany {
		err = insights.Invalid("a company funnel's rows are companies: lists hold people, so save a funnel by person")
	}
	var ids []string
	skipped := 0
	if err == nil {
		err = s.readInsight(r.Context(), func(tx pgx.Tx) error {
			c, err := insights.Compile(r.Context(), tx, a.WorkspaceID, a.Brand, f)
			if err != nil {
				return err
			}
			pq := insights.PeopleQuery{Step: in.Step, Dropped: in.Dropped, Limit: 500}
			if in.Breakdown != nil {
				pq.Breakdown, pq.BreakdownSet = *in.Breakdown, true
			}
			for {
				people, more, err := c.People(r.Context(), tx, pq)
				if err != nil {
					return err
				}
				for _, p := range people {
					if p.RecordID == nil {
						skipped++
						continue
					}
					ids = append(ids, *p.RecordID)
				}
				if !more || len(ids) >= MaxSavedPeople {
					return nil
				}
				pq.Offset += pq.Limit
			}
		})
	}
	if err != nil {
		metrics.InsightRun("funnel_people_save", s.insightError(w, r, err), time.Since(start))
		return
	}
	if len(ids) > MaxSavedPeople {
		ids = ids[:MaxSavedPeople]
	}
	var id string
	added := 0
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		what := "reached"
		if in.Dropped {
			what = "dropped off before"
		}
		desc := "People who " + what + " step " + itoa(in.Step) + " of a funnel on " + start.UTC().Format("2 Jan 2006") + "."
		if err := tx.QueryRow(r.Context(), `INSERT INTO smart_lists (workspace_id, brand_id, name, description, kind, rules, match, refreshed_at, created_by, record_type)
			VALUES ($1, $2, $3, $4, 'static', $5, 'all', $6, $7, 'person') RETURNING id::text`,
			a.WorkspaceID, a.Brand, in.Name, desc, mustJSON(smartlists.NobodyRules), smartlists.Never, nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
			return err
		}
		res, err := smartlists.AddStatic(r.Context(), tx, a.WorkspaceID, id, ids, "funnel", "")
		if err != nil {
			return err
		}
		added = res.Changed
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "insight.people_saved", "smart_list", id,
			map[string]any{"brand_id": a.Brand, "step": in.Step, "dropped": in.Dropped, "added": added, "skipped_anonymous": skipped, "has_breakdown": in.Breakdown != nil})
	})
	if uniqueViolation(err) {
		metrics.InsightRun("funnel_people_save", "conflict", time.Since(start))
		writeError(w, r, http.StatusConflict, "conflict", "a list with this name already exists: pass another name")
		return
	}
	if err != nil {
		metrics.InsightRun("funnel_people_save", "error", time.Since(start))
		s.internal(w, r, err)
		return
	}
	metrics.InsightRun("funnel_people_save", "success", time.Since(start))
	writeJSON(w, http.StatusCreated, map[string]any{"list_id": id, "added": added, "skipped_anonymous": skipped})
}

type funnelCorrelationInput struct {
	Funnel funnelSpec `json:"funnel"`
	insights.CorrelationQuery
}

// funnelCorrelation says which events and pages went with reaching a
// step and which went with dropping off.
func (s *Server) funnelCorrelation(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in funnelCorrelationInput
	if !decode(w, r, &in) {
		return
	}
	start := time.Now()
	f, err := in.Funnel.funnel(start)
	var res insights.CorrelationResult
	if err == nil {
		err = s.readInsight(r.Context(), func(tx pgx.Tx) error {
			c, err := insights.Compile(r.Context(), tx, a.WorkspaceID, a.Brand, f)
			if err != nil {
				return err
			}
			res, err = c.Correlate(r.Context(), tx, in.CorrelationQuery)
			return err
		})
	}
	if err != nil {
		metrics.InsightRun("funnel_correlation", s.insightError(w, r, err), time.Since(start))
		return
	}
	metrics.InsightRun("funnel_correlation", "success", time.Since(start))
	writeJSON(w, http.StatusOK, res)
}

// pathsSpec is a paths question as the API, saved insights and MCP send
// it.
type pathsSpec struct {
	DateRange dateRange            `json:"date_range"`
	Start     *insights.PathPoint  `json:"start,omitempty"`
	End       *insights.PathPoint  `json:"end,omitempty"`
	Steps     int                  `json:"steps,omitempty"`
	Kinds     []string             `json:"kinds,omitempty"`
	Window    *insights.Window     `json:"window,omitempty"`
	Top       int                  `json:"top,omitempty"`
	Exclude   []insights.PathPoint `json:"exclude,omitempty"`
}

func (p pathsSpec) paths(now time.Time) (insights.Paths, error) {
	from, to, err := p.DateRange.resolve(now)
	if err != nil {
		return insights.Paths{}, err
	}
	out := insights.Paths{From: from, To: to, Start: p.Start, End: p.End, Steps: p.Steps, Kinds: p.Kinds, Window: p.Window, Top: p.Top, Exclude: p.Exclude}
	return out, out.Validate()
}

type pathsOut struct {
	insights.PathsResult
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// runPaths answers a paths question.
func (s *Server) runPaths(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in pathsSpec
	if !decode(w, r, &in) {
		return
	}
	start := time.Now()
	p, err := in.paths(start)
	var res insights.PathsResult
	if err == nil {
		err = s.readInsight(r.Context(), func(tx pgx.Tx) error {
			var err error
			res, err = insights.RunPaths(r.Context(), tx, a.WorkspaceID, a.Brand, p)
			return err
		})
	}
	if err != nil {
		metrics.InsightRun("paths", s.insightError(w, r, err), time.Since(start))
		return
	}
	metrics.InsightRun("paths", "success", time.Since(start))
	writeJSON(w, http.StatusOK, pathsOut{PathsResult: res, From: p.From, To: p.To})
}

// Annotations mark a day on every chart of a brand.

type annotationView struct {
	ID        string    `json:"id"`
	BrandID   string    `json:"brand_id"`
	Day       string    `json:"day"`
	Text      string    `json:"text"`
	CreatedBy *string   `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

const annotationColumns = `n.id::text, n.brand_id::text, to_char(n.day, 'YYYY-MM-DD'), n.text, n.created_by::text, n.created_at`

func scanAnnotation(row pgx.Row) (annotationView, error) {
	var v annotationView
	err := row.Scan(&v.ID, &v.BrandID, &v.Day, &v.Text, &v.CreatedBy, &v.CreatedAt)
	return v, err
}

// listAnnotations lists a brand's annotations between from and to (dates,
// both inclusive; the last 366 days when unset), oldest first.
func (s *Server) listAnnotations(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	from, to := today.AddDate(-1, 0, 0), today
	var err error
	if v := q.Get("from"); v != "" {
		if from, err = time.Parse(time.DateOnly, v); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "from is a date (YYYY-MM-DD)")
			return
		}
	}
	if v := q.Get("to"); v != "" {
		if to, err = time.Parse(time.DateOnly, v); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "to is a date (YYYY-MM-DD)")
			return
		}
	}
	if to.Before(from) || to.Sub(from) > insights.MaxRange {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "from and to are dates, from before to, at most 366 days apart")
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT `+annotationColumns+` FROM insight_annotations n
		WHERE n.workspace_id = $1 AND n.brand_id = $2 AND n.day >= $3::date AND n.day <= $4::date ORDER BY n.day, n.created_at, n.id LIMIT 1000`,
		a.WorkspaceID, a.Brand, from, to)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (annotationView, error) { return scanAnnotation(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) createAnnotation(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Day  string `json:"day"`
		Text string `json:"text"`
	}
	if !decode(w, r, &in) {
		return
	}
	day, err := time.Parse(time.DateOnly, in.Day)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "day is a date (YYYY-MM-DD)")
		return
	}
	in.Text = strings.TrimSpace(in.Text)
	if in.Text == "" || len(in.Text) > 300 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "text is 1 to 300 characters")
		return
	}
	var v annotationView
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(r.Context(), `INSERT INTO insight_annotations (workspace_id, brand_id, day, text, created_by) VALUES ($1, $2, $3, $4, $5) RETURNING id::text`,
			a.WorkspaceID, a.Brand, day, in.Text, nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
			return err
		}
		if err := record(r.Context(), tx, &a.principal, a.WorkspaceID, "annotation.created", "annotation", id, map[string]any{"brand_id": a.Brand, "day": in.Day}); err != nil {
			return err
		}
		var err error
		v, err = scanAnnotation(tx.QueryRow(r.Context(), `SELECT `+annotationColumns+` FROM insight_annotations n WHERE n.id = $1`, id))
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) deleteAnnotation(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("annotation")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such annotation")
		return
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `DELETE FROM insight_annotations WHERE id = $1 AND workspace_id = $2 AND brand_id = $3`, id, a.WorkspaceID, a.Brand)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "annotation.deleted", "annotation", id, map[string]any{"brand_id": a.Brand})
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such annotation")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
