package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/media"
)

// Skills (docs/creatives.md): a brand's saved recipes for one kind of ad
// each. A skill holds its prompts, the AI model and the sizes; every
// saved change to them is a new version, and a version is never edited,
// so each creative says exactly what made it. A version with more than
// one prompt is a split test: new creatives are shared between the
// prompts, and results are compared by prompt.

const (
	maxSkills        = 100
	maxSkillVariants = 4
	maxSkillVersions = 500
)

// skillKinds are what a skill makes.
var skillKinds = []string{connectors.MediaImage, connectors.MediaVideo}

var (
	skillSize   = regexp.MustCompile(`^\d{1,2}(\.\d{1,2})?:\d{1,2}$`)
	skillFillIn = regexp.MustCompile(`\{([A-Za-z][A-Za-z0-9 _]{0,39})\}`)
)

func (s *Server) routesCreativeSkills(mux *http.ServeMux) {
	read, manage := auth.ScopeContentRead, auth.ScopeContentManage
	base := "/v1/brands/{brand}/creative-skills"
	mux.HandleFunc("GET "+base, s.onBrand(read, s.listCreativeSkills))
	mux.HandleFunc("POST "+base, s.onBrand(manage, s.createCreativeSkill))
	mux.HandleFunc("GET "+base+"/{skill}", s.onBrand(read, s.getCreativeSkill))
	mux.HandleFunc("PATCH "+base+"/{skill}", s.onBrand(manage, s.updateCreativeSkill))
	mux.HandleFunc("GET "+base+"/{skill}/versions", s.onBrand(read, s.listCreativeSkillVersions))
}

// skillVariant is one prompt of a version.
type skillVariant struct {
	// Key is A, B, C or D, given in order.
	Key  string `json:"key"`
	Name string `json:"name"`
	// Picture is the prompt for the picture or video. Words in curly
	// brackets are filled in for each creative.
	Picture string `json:"picture"`
	// Copy says how to write the headline, second line and button that
	// are laid over the picture.
	Copy string `json:"copy"`
	// Share is this prompt's part of new creatives, against the others'.
	Share int `json:"share"`
	// FillIns are the words in curly brackets the prompt uses.
	FillIns []string `json:"fill_ins"`
}

type skillVersionView struct {
	Version   int            `json:"version"`
	Note      string         `json:"note"`
	Provider  string         `json:"provider"`
	Model     string         `json:"model"`
	Options   map[string]any `json:"options"`
	Sizes     []string       `json:"sizes"`
	Rules     string         `json:"rules"`
	Variants  []skillVariant `json:"variants"`
	CreatedBy *string        `json:"created_by"`
	CreatedAt time.Time      `json:"created_at"`
	// How many creatives this version made.
	Creatives int `json:"creatives"`
}

type skillView struct {
	ID          string `json:"id"`
	BrandID     string `json:"brand_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Kind        string `json:"kind"`
	Enabled     bool   `json:"enabled"`
	// Version is the number of the current version.
	Version int `json:"version"`
	// SplitTest is true while the current version has more than one
	// prompt taking a share.
	SplitTest  bool             `json:"split_test"`
	Current    skillVersionView `json:"current"`
	Creatives  int              `json:"creatives"`
	ArchivedAt *time.Time       `json:"archived_at"`
	CreatedAt  time.Time        `json:"created_at"`
	UpdatedAt  time.Time        `json:"updated_at"`
}

const skillVersionColumns = `v.version, v.note, v.provider, v.model, v.options, v.sizes, v.rules, v.variants, v.created_by::text, v.created_at,
	(SELECT count(*) FROM creatives c WHERE c.skill_id = v.skill_id AND c.skill_version = v.version)::int`

const skillColumns = `k.id::text, k.brand_id::text, k.name, k.description, k.kind, k.enabled, k.current_version, k.archived_at, k.created_at, k.updated_at,
	(SELECT count(*) FROM creatives c WHERE c.skill_id = k.id)::int, ` + skillVersionColumns

func scanSkillVersion(dest *skillVersionView) []any {
	return []any{&dest.Version, &dest.Note, &dest.Provider, &dest.Model, &jsonInto{&dest.Options}, &dest.Sizes, &dest.Rules, &jsonInto{&dest.Variants}, &dest.CreatedBy, &dest.CreatedAt, &dest.Creatives}
}

// jsonInto scans a jsonb column into a Go value.
type jsonInto struct{ v any }

func (j *jsonInto) Scan(src any) error {
	switch b := src.(type) {
	case []byte:
		return json.Unmarshal(b, j.v)
	case string:
		return json.Unmarshal([]byte(b), j.v)
	}
	return nil
}

func scanSkill(row pgx.CollectableRow) (skillView, error) {
	var v skillView
	err := row.Scan(append([]any{&v.ID, &v.BrandID, &v.Name, &v.Description, &v.Kind, &v.Enabled, &v.Version, &v.ArchivedAt, &v.CreatedAt, &v.UpdatedAt, &v.Creatives},
		scanSkillVersion(&v.Current)...)...)
	if v.Current.Options == nil {
		v.Current.Options = map[string]any{}
	}
	if v.Current.Sizes == nil {
		v.Current.Sizes = []string{}
	}
	shares := 0
	for _, x := range v.Current.Variants {
		if x.Share > 0 {
			shares++
		}
	}
	v.SplitTest = shares > 1
	return v, err
}

type pgQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func findSkill(ctx context.Context, q pgQuerier, ws, brand, id string) (skillView, error) {
	if !validUUID(id) {
		return skillView{}, errNotFound
	}
	rows, err := q.Query(ctx, `SELECT `+skillColumns+` FROM creative_skills k
		JOIN creative_skill_versions v ON v.skill_id = k.id AND v.version = k.current_version
		WHERE k.id = $1 AND k.workspace_id = $2 AND k.brand_id = $3`, id, ws, brand)
	if err != nil {
		return skillView{}, err
	}
	v, err := pgx.CollectExactlyOneRow(rows, scanSkill)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	return v, err
}

func (s *Server) listCreativeSkills(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	archived := r.URL.Query().Get("archived") == "true"
	rows, err := s.pool.Query(r.Context(), `SELECT `+skillColumns+` FROM creative_skills k
		JOIN creative_skill_versions v ON v.skill_id = k.id AND v.version = k.current_version
		WHERE k.workspace_id = $1 AND k.brand_id = $2 AND (k.archived_at IS NOT NULL) = $3
		ORDER BY lower(k.name)`, a.WorkspaceID, a.Brand, archived)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, scanSkill)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) getCreativeSkill(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := findSkill(r.Context(), s.pool, a.WorkspaceID, a.Brand, r.PathValue("skill"))
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) listCreativeSkillVersions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	q := s.pool
	k, err := findSkill(ctx, q, a.WorkspaceID, a.Brand, r.PathValue("skill"))
	if s.allocError(w, r, err) {
		return
	}
	rows, err := q.Query(ctx, `SELECT `+skillVersionColumns+` FROM creative_skill_versions v
		WHERE v.skill_id = $1 AND v.workspace_id = $2 AND v.brand_id = $3 ORDER BY v.version DESC`, k.ID, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (skillVersionView, error) {
		var v skillVersionView
		err := row.Scan(scanSkillVersion(&v)...)
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "current": k.Version})
}

// skillRecipe is the part of a skill that is versioned.
type skillRecipe struct {
	Provider *string         `json:"provider"`
	Model    *string         `json:"model"`
	Options  *map[string]any `json:"options"`
	Sizes    *[]string       `json:"sizes"`
	Rules    *string         `json:"rules"`
	Variants *[]skillVariant `json:"variants"`
}

func (f skillRecipe) any() bool {
	return f.Provider != nil || f.Model != nil || f.Options != nil || f.Sizes != nil || f.Rules != nil || f.Variants != nil
}

// fillInKey is the name a fill-in goes by: {Brand fact} is brand_fact.
func fillInKey(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), " ", "_")
}

// fillIns lists the fill-ins a text uses, in order.
func fillIns(texts ...string) []string {
	out := []string{}
	for _, t := range texts {
		for _, m := range skillFillIn.FindAllStringSubmatch(t, -1) {
			if k := fillInKey(m[1]); !slices.Contains(out, k) {
				out = append(out, k)
			}
		}
	}
	return out
}

// apply lays the given fields over a version and checks the result.
func (f skillRecipe) apply(kind string, v skillVersionView) (skillVersionView, error) {
	if f.Provider != nil {
		v.Provider = strings.TrimSpace(*f.Provider)
	}
	if f.Model != nil {
		v.Model = strings.TrimSpace(*f.Model)
	}
	if f.Options != nil {
		v.Options = *f.Options
	}
	if f.Sizes != nil {
		v.Sizes = *f.Sizes
	}
	if f.Rules != nil {
		v.Rules = strings.TrimSpace(*f.Rules)
	}
	if f.Variants != nil {
		v.Variants = *f.Variants
	}
	if v.Provider != "" && !slices.Contains(media.Providers[kind], v.Provider) {
		return v, errInvalid("provider for a " + kind + " is one of " + strings.Join(media.Providers[kind], ", ") + ", or empty for the first one connected")
	}
	if _, err := (connectors.MediaRequest{Kind: kind, Model: v.Model, Prompt: "x"}).Normal(); err != nil {
		return v, errInvalid("model is the provider's own id for the AI model this skill uses; there is no default, since providers retire models often")
	}
	if v.Options == nil {
		v.Options = map[string]any{}
	}
	if raw, err := json.Marshal(v.Options); err != nil || len(raw) > 20000 || len(v.Options) > 50 {
		return v, errInvalid("options holds at most 50 settings and 20,000 characters of JSON")
	}
	sizes := []string{}
	for _, z := range v.Sizes {
		z = strings.TrimSpace(z)
		if !skillSize.MatchString(z) {
			return v, errInvalid("a size is a shape such as 1:1, 4:5, 9:16 or 16:9")
		}
		if !slices.Contains(sizes, z) {
			sizes = append(sizes, z)
		}
	}
	if len(sizes) > 8 {
		return v, errInvalid("a skill has at most 8 sizes")
	}
	v.Sizes = sizes
	if len(v.Rules) > 4000 {
		return v, errInvalid("rules is at most 4000 characters")
	}
	if len(v.Variants) == 0 || len(v.Variants) > maxSkillVariants {
		return v, errInvalid("variants holds 1 to " + strconv.Itoa(maxSkillVariants) + " prompts")
	}
	variants, total := make([]skillVariant, len(v.Variants)), 0
	for i, x := range v.Variants {
		x.Key = string(rune('A' + i))
		x.Name, x.Picture, x.Copy = strings.TrimSpace(x.Name), strings.TrimSpace(x.Picture), strings.TrimSpace(x.Copy)
		if x.Name == "" {
			x.Name = "Prompt " + x.Key
		}
		switch {
		case len(x.Name) > 100:
			return v, errInvalid("a prompt's name is at most 100 characters")
		case x.Picture == "" || len(x.Picture) > 8000:
			return v, errInvalid("a prompt's picture is 1 to 8000 characters: what the AI model is asked to make")
		case len(x.Copy) > 4000:
			return v, errInvalid("a prompt's copy is at most 4000 characters")
		case x.Share < 0 || x.Share > 100:
			return v, errInvalid("a prompt's share is 0 to 100")
		}
		if len(v.Variants) == 1 && x.Share == 0 {
			x.Share = 100
		}
		total += x.Share
		x.FillIns = fillIns(x.Picture, x.Copy, v.Rules)
		variants[i] = x
	}
	if total == 0 {
		return v, errInvalid("at least one prompt needs a share above 0")
	}
	v.Variants = variants
	return v, nil
}

type skillCreate struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Kind        string `json:"kind"`
	skillRecipe
}

func checkSkillName(name, description string) error {
	if name == "" || len(name) > 100 {
		return errInvalid("name is 1 to 100 characters")
	}
	if len(description) > 500 {
		return errInvalid("description is at most 500 characters")
	}
	return nil
}

func insertSkillVersion(ctx context.Context, tx pgx.Tx, a *access, skill string, v skillVersionView) error {
	options, _ := json.Marshal(v.Options)
	variants, _ := json.Marshal(v.Variants)
	var by *string
	if a.ActorID != "" {
		by = &a.ActorID
	}
	_, err := tx.Exec(ctx, `INSERT INTO creative_skill_versions (workspace_id, brand_id, skill_id, version, note, provider, model, options, sizes, rules, variants, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		a.WorkspaceID, a.Brand, skill, v.Version, v.Note, v.Provider, v.Model, options, v.Sizes, v.Rules, variants, by)
	return err
}

// skillData is what a skill's events carry: never its name or prompts.
func skillData(v skillView) map[string]any {
	return map[string]any{"kind": v.Kind, "version": v.Version, "variants": len(v.Current.Variants), "provider": v.Current.Provider, "model": v.Current.Model, "enabled": v.Enabled}
}

func (s *Server) createCreativeSkill(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in skillCreate
	if !decode(w, r, &in) {
		return
	}
	in.Name, in.Description = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	ctx := r.Context()
	var v skillView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := checkSkillName(in.Name, in.Description); err != nil {
			return err
		}
		if !slices.Contains(skillKinds, in.Kind) {
			return errInvalid("kind is what the skill makes: " + strings.Join(skillKinds, " or "))
		}
		ver, err := in.skillRecipe.apply(in.Kind, skillVersionView{Version: 1})
		if err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM creative_skills WHERE workspace_id = $1 AND brand_id = $2`, a.WorkspaceID, a.Brand).Scan(&n); err != nil {
			return err
		}
		if n >= maxSkills {
			return errConflictMsg("a brand keeps at most " + strconv.Itoa(maxSkills) + " skills")
		}
		var by *string
		if a.ActorID != "" {
			by = &a.ActorID
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO creative_skills (workspace_id, brand_id, name, description, kind, created_by)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`, a.WorkspaceID, a.Brand, in.Name, in.Description, in.Kind, by).Scan(&id); err != nil {
			if uniqueViolation(err) {
				return errConflictMsg("this brand already has a skill with that name")
			}
			return err
		}
		if err := insertSkillVersion(ctx, tx, a, id, ver); err != nil {
			return err
		}
		if v, err = findSkill(ctx, tx, a.WorkspaceID, a.Brand, id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "creative_skill.created", "creative_skill", id, skillData(v))
	})
	if s.assetError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

type skillPatch struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Enabled     *bool   `json:"enabled"`
	Archived    *bool   `json:"archived"`
	// Note says what changed, for the list of versions.
	Note *string `json:"note"`
	// RestoreVersion starts the new version from an earlier one.
	RestoreVersion *int `json:"restore_version"`
	// Version is the version the caller read: the change is refused if
	// the skill has moved on since.
	Version *int `json:"version"`
	skillRecipe
}

// updateCreativeSkill changes a skill. Its name, description and switch
// change in place; a change to its prompts, model, sizes or rules is
// saved as a new version, and the earlier ones stay as they were.
func (s *Server) updateCreativeSkill(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var f skillPatch
	if !decode(w, r, &f) {
		return
	}
	ctx := r.Context()
	var v skillView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if !validUUID(r.PathValue("skill")) {
			return errNotFound
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM creative_skills WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 FOR UPDATE`, r.PathValue("skill"), a.WorkspaceID, a.Brand); err != nil {
			return err
		}
		cur, err := findSkill(ctx, tx, a.WorkspaceID, a.Brand, r.PathValue("skill"))
		if err != nil {
			return err
		}
		if f.Version != nil && *f.Version != cur.Version {
			return errConflictMsg("the skill changed since you read it (it is at version " + strconv.Itoa(cur.Version) + "); read it again")
		}
		name, description := cur.Name, cur.Description
		if f.Name != nil {
			name = strings.TrimSpace(*f.Name)
		}
		if f.Description != nil {
			description = strings.TrimSpace(*f.Description)
		}
		if err := checkSkillName(name, description); err != nil {
			return err
		}
		enabled, archived := cur.Enabled, cur.ArchivedAt != nil
		if f.Enabled != nil {
			enabled = *f.Enabled
		}
		if f.Archived != nil {
			archived = *f.Archived
		}
		version, saved := cur.Version, false
		if f.skillRecipe.any() || f.RestoreVersion != nil {
			base := cur.Current
			if f.RestoreVersion != nil {
				rows, err := tx.Query(ctx, `SELECT `+skillVersionColumns+` FROM creative_skill_versions v WHERE v.skill_id = $1 AND v.version = $2`, cur.ID, *f.RestoreVersion)
				if err != nil {
					return err
				}
				if base, err = pgx.CollectExactlyOneRow(rows, func(row pgx.CollectableRow) (skillVersionView, error) {
					var b skillVersionView
					err := row.Scan(scanSkillVersion(&b)...)
					return b, err
				}); errors.Is(err, pgx.ErrNoRows) {
					return errInvalid("restore_version is a version this skill has had")
				} else if err != nil {
					return err
				}
			}
			next, err := f.skillRecipe.apply(cur.Kind, base)
			if err != nil {
				return err
			}
			var top int
			if err := tx.QueryRow(ctx, `SELECT max(version) FROM creative_skill_versions WHERE skill_id = $1`, cur.ID).Scan(&top); err != nil {
				return err
			}
			if top >= maxSkillVersions {
				return errConflictMsg("a skill keeps at most " + strconv.Itoa(maxSkillVersions) + " versions; copy it into a new skill")
			}
			next.Version, next.Note = top+1, ""
			if f.Note != nil {
				next.Note = strings.TrimSpace(*f.Note)
			}
			if next.Note == "" && f.RestoreVersion != nil {
				next.Note = "Went back to version " + strconv.Itoa(*f.RestoreVersion)
			}
			if len(next.Note) > 500 {
				return errInvalid("note is at most 500 characters")
			}
			if err := insertSkillVersion(ctx, tx, a, cur.ID, next); err != nil {
				return err
			}
			version, saved = next.Version, true
		}
		if _, err := tx.Exec(ctx, `UPDATE creative_skills SET name = $2, description = $3, enabled = $4, current_version = $5,
				archived_at = CASE WHEN $6 THEN coalesce(archived_at, now()) END, updated_at = now() WHERE id = $1`,
			cur.ID, name, description, enabled, version, archived); err != nil {
			if uniqueViolation(err) {
				return errConflictMsg("this brand already has a skill with that name")
			}
			return err
		}
		if v, err = findSkill(ctx, tx, a.WorkspaceID, a.Brand, cur.ID); err != nil {
			return err
		}
		action := "creative_skill.updated"
		if saved {
			action = "creative_skill.version_saved"
		}
		data := skillData(v)
		data["restored"] = f.RestoreVersion != nil
		data["archived"] = archived
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "creative_skill", cur.ID, data)
	})
	if s.assetError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}
