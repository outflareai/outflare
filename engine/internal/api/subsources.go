package api

// Sub-sources say where within their Source a person came from: the event
// or conference the sales team met them at, for Source Events and
// conferences. They are the options of the brand's sub_source option set,
// each tagged with the Source it belongs to, and fill the built-in
// Sub-source field on people. Anyone who may add people may add one, by
// picking it or typing a new name where people are added, so the list grows
// as the team goes to events; the same name twice is the same sub-source.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

const subSourceSet = "sub_source"

func (s *Server) subSourceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/sub-sources", s.onBrand("customers.read", s.listSubSources))
	mux.HandleFunc("POST /v1/brands/{brand}/sub-sources", s.onBrand("customers.manage", s.editsRecords(s.addSubSource)))
	mux.HandleFunc("PATCH /v1/brands/{brand}/sub-sources/{key}", s.onBrand("customers.manage", s.editsRecords(s.updateSubSource)))
}

type subSourceView struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Source   string `json:"source"`
	Archived bool   `json:"archived"`
	// People is how many of the brand's people have it.
	People int `json:"people"`
}

func (s *Server) listSubSources(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	source := r.URL.Query().Get("source")
	if source != "" && !slices.Contains(personSources, source) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "source is one of "+strings.Join(personSources, ", "))
		return
	}
	archived := r.URL.Query().Get("archived") == "true"
	var opts []catalogOption
	if err := s.pool.QueryRow(ctx, `SELECT options FROM option_sets WHERE workspace_id = $1 AND brand_id = $2 AND key = $3`,
		a.WorkspaceID, a.Brand, subSourceSet).Scan(&opts); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.internal(w, r, err)
		return
	}
	counts := map[string]int{}
	rows, err := s.pool.Query(ctx, `SELECT attributes ->> 'sub_source', count(*) FROM records
		WHERE workspace_id = $1 AND brand_id = $2 AND record_type = 'person' AND attributes ? 'sub_source'
		  AND merged_into IS NULL AND deleted_at IS NULL
		GROUP BY 1`, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var k string
	var n int
	if _, err := pgx.ForEachRow(rows, []any{&k, &n}, func() error { counts[k] = n; return nil }); err != nil {
		s.internal(w, r, err)
		return
	}
	out := []subSourceView{}
	for _, o := range opts {
		if (source != "" && o.Source != source) || (o.Archived && !archived) {
			continue
		}
		out = append(out, subSourceView{Key: o.Key, Label: o.Label, Source: o.Source, Archived: o.Archived, People: counts[o.Key]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// addSubSource adds a sub-source, or returns the one of the same name (any
// case or spacing) under the same Source, bringing it back if archived.
func (s *Server) addSubSource(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var in struct {
		Source string `json:"source"`
		Label  string `json:"label"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Label = cleanSubSourceLabel(in.Label)
	switch {
	case !slices.Contains(personSources, in.Source):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "source is the Source it belongs to: "+strings.Join(personSources, ", "))
		return
	case in.Label == "" || len(in.Label) > 120:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "label is its name, such as the event or conference, 1 to 120 characters")
		return
	}
	var out catalogOption
	created := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, created, err = ensureSubSource(ctx, tx, a, a.Brand, in.Source, in.Label)
		return err
	})
	if s.recordError(w, r, err) {
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, subSourceView{Key: out.Key, Label: out.Label, Source: out.Source, Archived: out.Archived})
}

// ensureSubSource finds or adds brand's sub-source called label under
// source, in tx, and says whether it added it (or brought it back).
func ensureSubSource(ctx context.Context, tx pgx.Tx, a *access, brand, source, label string) (catalogOption, bool, error) {
	opts, err := lockSubSources(ctx, tx, a.WorkspaceID, brand)
	if err != nil {
		return catalogOption{}, false, err
	}
	fold := strings.ToLower(label)
	for i, o := range opts {
		if o.Source == source && strings.ToLower(o.Label) == fold {
			if !o.Archived {
				return o, false, nil
			}
			opts[i].Archived = false
			if err := saveSubSources(ctx, tx, a, brand, opts, "sub_source.updated", opts[i], map[string]any{"archived": false}); err != nil {
				return catalogOption{}, false, err
			}
			return opts[i], true, nil
		}
	}
	key := subSourceKey(label, func(k string) bool {
		return slices.ContainsFunc(opts, func(o catalogOption) bool { return o.Key == k })
	})
	o := catalogOption{Key: key, Label: label, Source: source}
	opts = append(opts, o)
	if err := saveSubSources(ctx, tx, a, brand, opts, "sub_source.created", o, map[string]any{"source": source}); err != nil {
		return catalogOption{}, false, err
	}
	return o, true, nil
}

func (s *Server) updateSubSource(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var in struct {
		Label    *string `json:"label"`
		Archived *bool   `json:"archived"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Label != nil {
		*in.Label = cleanSubSourceLabel(*in.Label)
		if *in.Label == "" || len(*in.Label) > 120 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "label is its name, 1 to 120 characters")
			return
		}
	}
	key := r.PathValue("key")
	var out catalogOption
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		opts, err := lockSubSources(ctx, tx, a.WorkspaceID, a.Brand)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(opts, func(o catalogOption) bool { return o.Key == key })
		if i < 0 {
			return errNotFound
		}
		data := map[string]any{}
		if in.Label != nil && *in.Label != opts[i].Label {
			fold := strings.ToLower(*in.Label)
			if slices.ContainsFunc(opts, func(o catalogOption) bool {
				return o.Key != key && o.Source == opts[i].Source && strings.ToLower(o.Label) == fold
			}) {
				return recordConflict{"conflict", "another sub-source of this Source is already called " + *in.Label}
			}
			opts[i].Label = *in.Label
			data["renamed"] = true
		}
		if in.Archived != nil && *in.Archived != opts[i].Archived {
			opts[i].Archived = *in.Archived
			data["archived"] = *in.Archived
		}
		out = opts[i]
		if len(data) == 0 {
			return nil
		}
		return saveSubSources(ctx, tx, a, a.Brand, opts, "sub_source.updated", opts[i], data)
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "sub-source not found")
		return
	}
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, subSourceView{Key: out.Key, Label: out.Label, Source: out.Source, Archived: out.Archived})
}

// lockSubSources makes sure the brand has its sub_source option set row
// and locks it, so two people adding the same event at once get one.
func lockSubSources(ctx context.Context, tx pgx.Tx, ws, brand string) ([]catalogOption, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO option_sets (workspace_id, brand_id, key, name, options)
		SELECT $1, $2, $3, 'Sub-sources', '[]'
		WHERE NOT EXISTS (SELECT 1 FROM option_sets WHERE workspace_id = $1 AND brand_id = $2 AND key = $3)
		ON CONFLICT DO NOTHING`, ws, brand, subSourceSet); err != nil {
		return nil, err
	}
	var opts []catalogOption
	err := tx.QueryRow(ctx, `SELECT options FROM option_sets WHERE workspace_id = $1 AND brand_id = $2 AND key = $3 FOR UPDATE`,
		ws, brand, subSourceSet).Scan(&opts)
	if opts == nil {
		opts = []catalogOption{}
	}
	return opts, err
}

func saveSubSources(ctx context.Context, tx pgx.Tx, a *access, brand string, opts []catalogOption, action string, o catalogOption, data map[string]any) error {
	b, err := json.Marshal(opts)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE option_sets SET options = $4, updated_at = now() WHERE workspace_id = $1 AND brand_id = $2 AND key = $3`,
		a.WorkspaceID, brand, subSourceSet, b); err != nil {
		return err
	}
	data["key"], data["brand_id"] = o.Key, brand
	return record(ctx, tx, &a.principal, a.WorkspaceID, action, "brand", brand, data)
}

var subSourceSpaces = regexp.MustCompile(`\s+`)

func cleanSubSourceLabel(s string) string {
	return strings.TrimSpace(subSourceSpaces.ReplaceAllString(s, " "))
}

var subSourceNonKey = regexp.MustCompile(`[^a-z0-9]+`)

// subSourceKey is a stable key for a new sub-source: its name in lowercase
// letters, digits and underscores ("Web Summit 2026" is web_summit_2026),
// numbered when taken. Keys never change; a rename changes only the label.
func subSourceKey(label string, taken func(string) bool) string {
	base := strings.Trim(subSourceNonKey.ReplaceAllString(strings.ToLower(label), "_"), "_")
	if base == "" || base[0] < 'a' || base[0] > 'z' {
		base = "s_" + base
	}
	base = strings.TrimRight(base, "_")
	if len(base) > 36 {
		base = strings.TrimRight(base[:36], "_")
	}
	key := base
	for n := 2; taken(key); n++ {
		key = base + "_" + strconv.Itoa(n)
	}
	return key
}
