package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/assets"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/blobs"
	"github.com/outflareai/outflare/engine/internal/layout"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Creatives (docs/creatives.md): the ads a brand makes from its skills.
// Making one fills the skill's prompt in for one persona and one pain,
// and asks the brand's own AI account for the picture or video
// (brand_generations); the file is kept with the brand's files. Every
// creative records the skill, version and prompt that made it and its
// tags, so results can be compared by any of them, and it waits for a
// person's yes before it can go anywhere.

const creativesShown = 500

// creativeStatuses are where a creative stands: the AI account is still
// making it, it ended without a file, it waits for a person, or a person
// said yes or no.
var creativeStatuses = []string{"making", "failed", "needs_review", "approved", "rejected"}

func (s *Server) routesCreatives(mux *http.ServeMux) {
	read, manage := auth.ScopeContentRead, auth.ScopeContentManage
	base := "/v1/brands/{brand}/creatives"
	mux.HandleFunc("GET "+base, s.onBrand(read, s.listCreatives))
	mux.HandleFunc("POST "+base, s.onBrand(manage, s.createCreative))
	mux.HandleFunc("GET "+base+"/{creative}", s.onBrand(read, s.getCreative))
	mux.HandleFunc("PATCH "+base+"/{creative}", s.onBrand(manage, s.updateCreative))
	mux.HandleFunc("GET "+base+"/{creative}/ad", s.onBrand(read, s.creativeAd))
	mux.HandleFunc("POST "+base+"/{creative}/approve", s.onBrand(manage, s.approveCreative))
	mux.HandleFunc("POST "+base+"/{creative}/reject", s.onBrand(manage, s.rejectCreative))
}

type creativeView struct {
	ID      string `json:"id"`
	BrandID string `json:"brand_id"`
	// Code is the creative's short name within the brand, such as CR-0142.
	Code string `json:"code"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// making, failed, needs_review, approved or rejected.
	Status       string   `json:"status"`
	SkillID      *string  `json:"skill_id"`
	SkillName    *string  `json:"skill_name"`
	SkillVersion int      `json:"skill_version"`
	Variant      string   `json:"variant"`
	Persona      string   `json:"persona"`
	Pain         string   `json:"pain"`
	Hook         string   `json:"hook"`
	Tags         []string `json:"tags"`
	// Sizes are the shapes its skill wants the ad in, such as 1:1 and 4:5.
	Sizes    []string `json:"sizes"`
	Headline string   `json:"headline"`
	Subline  string   `json:"subline"`
	CTA      string   `json:"cta"`
	Prompt   string   `json:"prompt"`
	// The request to the AI account, and what it used.
	GenerationID *string `json:"generation_id"`
	Provider     *string `json:"provider"`
	Model        *string `json:"model"`
	// Why the AI account made no file, as a code and in words.
	ErrorCode *string `json:"error_code"`
	Error     *string `json:"error"`
	// The brand file it became, once made.
	AssetID      *string    `json:"asset_id"`
	ReviewReason string     `json:"review_reason"`
	ReviewedBy   *string    `json:"reviewed_by"`
	ReviewedAt   *time.Time `json:"reviewed_at"`
	// liked, not_this or empty, and why.
	Rating       string     `json:"rating"`
	RatingReason string     `json:"rating_reason"`
	RatedAt      *time.Time `json:"rated_at"`
	Version      int        `json:"version"`
	CreatedBy    *string    `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

const creativeStatus = `CASE WHEN g.status = 'failed' THEN 'failed' WHEN g.status IN ('queued', 'running') THEN 'making'
	WHEN c.review = 'waiting' THEN 'needs_review' ELSE c.review END`

const creativeColumns = `c.id::text, c.brand_id::text, c.number, c.name, c.kind, ` + creativeStatus + `, c.skill_id::text, k.name, c.skill_version, c.variant,
	c.persona, c.pain, c.hook, c.tags, COALESCE(sv.sizes, '{}'), c.headline, c.subline, c.cta, c.prompt, c.generation_id::text, g.connector, g.model,
	NULLIF(g.error_code, ''), NULLIF(g.error, ''), g.asset_id::text, c.review_reason, c.reviewed_by::text, c.reviewed_at, c.version, c.created_by::text, c.created_at, c.updated_at, c.rating, c.rating_reason, c.rated_at`

const creativeFrom = ` FROM creatives c
	LEFT JOIN brand_generations g ON g.id = c.generation_id AND g.workspace_id = c.workspace_id AND g.brand_id = c.brand_id
	LEFT JOIN creative_skills k ON k.id = c.skill_id AND k.workspace_id = c.workspace_id AND k.brand_id = c.brand_id
	LEFT JOIN creative_skill_versions sv ON sv.skill_id = c.skill_id AND sv.version = c.skill_version AND sv.workspace_id = c.workspace_id AND sv.brand_id = c.brand_id`

func scanCreative(row pgx.CollectableRow) (creativeView, error) {
	var v creativeView
	var number int
	err := row.Scan(&v.ID, &v.BrandID, &number, &v.Name, &v.Kind, &v.Status, &v.SkillID, &v.SkillName, &v.SkillVersion, &v.Variant,
		&v.Persona, &v.Pain, &v.Hook, &v.Tags, &v.Sizes, &v.Headline, &v.Subline, &v.CTA, &v.Prompt, &v.GenerationID, &v.Provider, &v.Model,
		&v.ErrorCode, &v.Error, &v.AssetID, &v.ReviewReason, &v.ReviewedBy, &v.ReviewedAt, &v.Version, &v.CreatedBy, &v.CreatedAt, &v.UpdatedAt, &v.Rating, &v.RatingReason, &v.RatedAt)
	v.Code = fmt.Sprintf("CR-%04d", number)
	if v.Tags == nil {
		v.Tags = []string{}
	}
	if v.Sizes == nil {
		v.Sizes = []string{}
	}
	return v, err
}

func findCreative(ctx context.Context, q pgQuerier, ws, brand, id string) (creativeView, error) {
	if !validUUID(id) {
		return creativeView{}, errNotFound
	}
	rows, err := q.Query(ctx, `SELECT `+creativeColumns+creativeFrom+` WHERE c.id = $1 AND c.workspace_id = $2 AND c.brand_id = $3`, id, ws, brand)
	if err != nil {
		return creativeView{}, err
	}
	v, err := pgx.CollectExactlyOneRow(rows, scanCreative)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	return v, err
}

// creativeData is what a creative's events carry: ids and enums, never
// its name, its words or its prompt.
func creativeData(v creativeView) map[string]any {
	d := map[string]any{"kind": v.Kind, "status": v.Status, "skill_version": v.SkillVersion, "variant": v.Variant}
	if v.SkillID != nil {
		d["skill_id"] = *v.SkillID
	}
	return d
}

func (s *Server) listCreatives(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	status, skill := q.Get("status"), q.Get("skill_id")
	if status != "" && !slices.Contains(creativeStatuses, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is one of "+strings.Join(creativeStatuses, ", "))
		return
	}
	if skill != "" && !validUUID(skill) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "skill_id is a skill's id")
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT `+creativeColumns+creativeFrom+`
		WHERE c.workspace_id = $1 AND c.brand_id = $2
		  AND ($3 = '' OR `+creativeStatus+` = $3)
		  AND ($4 = '' OR c.skill_id = NULLIF($4, '')::uuid)
		  AND ($5 = '' OR c.kind = $5)
		  AND ($6 = '' OR lower(c.persona) = lower($6))
		  AND ($7 = '' OR lower(c.pain) = lower($7))
		  AND ($8 = '' OR lower($8) = ANY (c.tags))
		  AND ($9 = '' OR c.name ILIKE '%' || $9 || '%' OR c.headline ILIKE '%' || $9 || '%')
		ORDER BY c.created_at DESC, c.number DESC LIMIT `+strconv.Itoa(creativesShown),
		a.WorkspaceID, a.Brand, status, skill, q.Get("kind"), q.Get("persona"), q.Get("pain"), q.Get("tag"), likeEscape(q.Get("q")))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, scanCreative)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) getCreative(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := findCreative(r.Context(), s.pool, a.WorkspaceID, a.Brand, r.PathValue("creative"))
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// creativeWords are the tags and words a person or agent sets on a
// creative.
type creativeWords struct {
	Name     *string   `json:"name"`
	Persona  *string   `json:"persona"`
	Pain     *string   `json:"pain"`
	Hook     *string   `json:"hook"`
	Tags     *[]string `json:"tags"`
	Headline *string   `json:"headline"`
	Subline  *string   `json:"subline"`
	CTA      *string   `json:"cta"`
}

// apply lays the given words over a creative and checks them.
func (f creativeWords) apply(v creativeView) (creativeView, error) {
	set := func(dst *string, src *string, what string, max int) error {
		if src == nil {
			return nil
		}
		if *dst = strings.TrimSpace(*src); len(*dst) > max {
			return errInvalid(what + " is at most " + strconv.Itoa(max) + " characters")
		}
		return nil
	}
	for _, e := range []error{set(&v.Name, f.Name, "name", 200), set(&v.Persona, f.Persona, "persona", 200), set(&v.Pain, f.Pain, "pain", 200),
		set(&v.Hook, f.Hook, "hook", 200), set(&v.Headline, f.Headline, "headline", 200), set(&v.Subline, f.Subline, "subline", 300), set(&v.CTA, f.CTA, "cta", 60)} {
		if e != nil {
			return v, e
		}
	}
	if f.Tags != nil {
		tags, err := cleanTags(*f.Tags)
		if err != nil {
			return v, err
		}
		v.Tags = tags
	}
	return v, nil
}

type creativeCreate struct {
	SkillID string `json:"skill_id"`
	// Variant names one of the skill's prompts (A, B, ...). Left out, the
	// prompt furthest behind its share makes it.
	Variant string `json:"variant"`
	// FillIns are the words for the curly brackets in the prompt, beyond
	// persona, pain, hook and brand.
	FillIns map[string]string `json:"fill_ins"`
	// Note is added to the prompt for this creative only.
	Note string `json:"note"`
	creativeWords
}

// fillPrompt puts the words into a prompt's curly brackets. Brackets
// with no word are returned, so nothing is paid for a prompt with a hole
// in it.
func fillPrompt(text string, words map[string]string) (string, []string) {
	missing := []string{}
	out := skillFillIn.ReplaceAllStringFunc(text, func(m string) string {
		k := fillInKey(m[1 : len(m)-1])
		if w := strings.TrimSpace(words[k]); w != "" {
			return w
		}
		if !slices.Contains(missing, k) {
			missing = append(missing, k)
		}
		return m
	})
	return out, missing
}

// pickVariant chooses the prompt that makes a creative: the one named,
// or the one furthest behind its share of what this version has made.
func pickVariant(ctx context.Context, tx pgx.Tx, k skillView, named string) (skillVariant, error) {
	if named = strings.ToUpper(strings.TrimSpace(named)); named != "" {
		for _, x := range k.Current.Variants {
			if x.Key == named {
				return x, nil
			}
		}
		return skillVariant{}, errInvalid("variant is one of this skill's prompts: A to " + string(rune('A'+len(k.Current.Variants)-1)))
	}
	made := map[string]int{}
	rows, err := tx.Query(ctx, `SELECT variant, count(*)::int FROM creatives WHERE skill_id = $1 AND skill_version = $2 GROUP BY variant`, k.ID, k.Version)
	if err != nil {
		return skillVariant{}, err
	}
	var key string
	var n int
	if _, err := pgx.ForEachRow(rows, []any{&key, &n}, func() error { made[key] = n; return nil }); err != nil {
		return skillVariant{}, err
	}
	var best skillVariant
	for _, x := range k.Current.Variants {
		// Compare made/share without dividing: a/b < c/d is a*d < c*b.
		if x.Share > 0 && (best.Key == "" || made[x.Key]*best.Share < made[best.Key]*x.Share) {
			best = x
		}
	}
	return best, nil
}

// createCreative makes one creative from a skill: it fills the prompt
// in, asks the brand's own AI account for the file, and keeps the
// creative waiting for a person's yes.
func (s *Server) createCreative(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !s.needFiles(w, r) {
		return
	}
	var in creativeCreate
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	k, err := findSkill(ctx, s.pool, a.WorkspaceID, a.Brand, in.SkillID)
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "skill_id is one of the brand's skills")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !k.Enabled || k.ArchivedAt != nil {
		writeError(w, r, http.StatusConflict, "conflict", "this skill is off: turn it on before making creatives with it")
		return
	}
	if note := strings.TrimSpace(in.Note); len(note) > 2000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "note is at most 2000 characters")
		return
	}
	words, err := in.creativeWords.apply(creativeView{Tags: []string{}})
	if s.allocError(w, r, err) {
		return
	}
	var brandName string
	if err := s.pool.QueryRow(ctx, `SELECT name FROM brands WHERE id = $1 AND workspace_id = $2`, a.Brand, a.WorkspaceID).Scan(&brandName); err != nil {
		s.internal(w, r, err)
		return
	}
	var v creativeView
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// One creative at a time per brand, so numbers and shares are
		// counted on what is already there.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('creatives:' || $1, 0))`, a.Brand); err != nil {
			return err
		}
		variant, err := pickVariant(ctx, tx, k, in.Variant)
		if err != nil {
			return err
		}
		fill := map[string]string{}
		for key, w := range in.FillIns {
			if len(w) > 1000 {
				return errInvalid("a fill-in is at most 1000 characters")
			}
			fill[fillInKey(key)] = w
		}
		for key, w := range map[string]string{"persona": words.Persona, "pain": words.Pain, "hook": words.Hook, "brand": brandName} {
			if fill[key] == "" {
				fill[key] = w
			}
		}
		text := variant.Picture
		if k.Current.Rules != "" {
			text += "\n" + k.Current.Rules
		}
		// What the brand taught its creatives goes into every prompt.
		lessons, err := brandLessons(ctx, tx, a.WorkspaceID, a.Brand)
		if err != nil {
			return err
		}
		if rules := lessonRules(lessons, k.Kind); rules != "" {
			text += "\n" + strings.NewReplacer("{", "(", "}", ")").Replace(rules)
		}
		prompt, missing := fillPrompt(text, fill)
		if len(missing) > 0 {
			return errInvalid("the prompt needs a word for {" + strings.Join(missing, "}, {") + "}: give each in fill_ins (persona, pain and hook have their own fields)")
		}
		if note := strings.TrimSpace(in.Note); note != "" {
			prompt += "\n" + note
		}
		var number int
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(number), 0) + 1 FROM creatives WHERE workspace_id = $1 AND brand_id = $2`, a.WorkspaceID, a.Brand).Scan(&number); err != nil {
			return err
		}
		name := words.Name
		if name == "" {
			if name = words.Headline; name == "" {
				name = fmt.Sprintf("%s %d", k.Name, number)
			}
		}
		g, err := s.readyGeneration(ctx, a.WorkspaceID, a.Brand, generationRequest{Kind: k.Kind, Provider: k.Current.Provider, Model: k.Current.Model,
			Prompt: prompt, Options: k.Current.Options, AssetKind: "other", Name: name})
		if err != nil {
			return err
		}
		gen, err := queueGeneration(ctx, tx, a, g)
		if err != nil {
			return err
		}
		var by *string
		if a.ActorID != "" {
			by = &a.ActorID
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO creatives (workspace_id, brand_id, number, name, kind, skill_id, skill_version, variant, persona, pain, hook, tags,
				headline, subline, cta, prompt, generation_id, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18) RETURNING id::text`,
			a.WorkspaceID, a.Brand, number, name, k.Kind, k.ID, k.Version, variant.Key, words.Persona, words.Pain, words.Hook, words.Tags,
			words.Headline, words.Subline, words.CTA, prompt, gen.ID, by).Scan(&id); err != nil {
			return err
		}
		if err := addHistory(ctx, tx, a.WorkspaceID, a.Brand, "creative", id, "made", strings.TrimSpace(in.Note), a.ActorID, map[string]any{"skill_version": k.Version, "variant": variant.Key}); err != nil {
			return err
		}
		if v, err = findCreative(ctx, tx, a.WorkspaceID, a.Brand, id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "creative.requested", "creative", id, creativeData(v))
	})
	if s.generationError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

type creativePatch struct {
	creativeWords
	Version *int `json:"version"`
}

// changeCreative runs one change to a creative in a transaction and
// records it.
func (s *Server) changeCreative(w http.ResponseWriter, r *http.Request, action string, change func(ctx context.Context, tx pgx.Tx, cur creativeView) error) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var v creativeView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if !validUUID(r.PathValue("creative")) {
			return errNotFound
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM creatives WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 FOR UPDATE`, r.PathValue("creative"), a.WorkspaceID, a.Brand); err != nil {
			return err
		}
		cur, err := findCreative(ctx, tx, a.WorkspaceID, a.Brand, r.PathValue("creative"))
		if err != nil {
			return err
		}
		if err := change(ctx, tx, cur); err != nil {
			return err
		}
		if v, err = findCreative(ctx, tx, a.WorkspaceID, a.Brand, cur.ID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "creative", cur.ID, creativeData(v))
	})
	if s.assetError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) updateCreative(w http.ResponseWriter, r *http.Request) {
	var f creativePatch
	if !decode(w, r, &f) {
		return
	}
	s.changeCreative(w, r, "creative.updated", func(ctx context.Context, tx pgx.Tx, cur creativeView) error {
		if f.Version != nil && *f.Version != cur.Version {
			return errConflictMsg("the creative changed since you read it (version " + strconv.Itoa(cur.Version) + "); read it again")
		}
		next, err := f.creativeWords.apply(cur)
		if err != nil {
			return err
		}
		if next.Name == "" {
			return errInvalid("name is 1 to 200 characters")
		}
		_, err = tx.Exec(ctx, `UPDATE creatives SET name = $2, persona = $3, pain = $4, hook = $5, tags = $6, headline = $7, subline = $8, cta = $9,
			version = version + 1, updated_at = now() WHERE id = $1`,
			cur.ID, next.Name, next.Persona, next.Pain, next.Hook, next.Tags, next.Headline, next.Subline, next.CTA)
		if err != nil {
			return err
		}
		return addHistory(ctx, tx, accessFrom(ctx).WorkspaceID, accessFrom(ctx).Brand, "creative", cur.ID, "words_changed", "", accessFrom(ctx).ActorID, nil)
	})
}

// reviewCreative records a person's yes or no. Only a creative whose
// file was made can be answered.
func (s *Server) reviewCreative(w http.ResponseWriter, r *http.Request, review, reason string) {
	a := accessFrom(r.Context())
	s.changeCreative(w, r, "creative."+review, func(ctx context.Context, tx pgx.Tx, cur creativeView) error {
		switch cur.Status {
		case "making":
			return errConflictMsg("this creative is still being made; answer it once its file is there")
		case "failed":
			return errConflictMsg("this creative has no file: the AI account made none, so there is nothing to answer")
		case review:
			return nil
		}
		var by *string
		if a.ActorID != "" {
			by = &a.ActorID
		}
		if _, err := tx.Exec(ctx, `UPDATE creatives SET review = $2, review_reason = $3, reviewed_by = $4, reviewed_at = now(), version = version + 1, updated_at = now() WHERE id = $1`,
			cur.ID, review, reason, by); err != nil {
			return err
		}
		return addHistory(ctx, tx, a.WorkspaceID, a.Brand, "creative", cur.ID, review, reason, a.ActorID, nil)
	})
}

func (s *Server) approveCreative(w http.ResponseWriter, r *http.Request) {
	s.reviewCreative(w, r, "approved", "")
}

func (s *Server) rejectCreative(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	if in.Reason = strings.TrimSpace(in.Reason); len(in.Reason) > 500 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "reason is at most 500 characters")
		return
	}
	s.reviewCreative(w, r, "rejected", in.Reason)
}

// creativeAd draws a creative as an ad: its picture cropped to the size
// asked for, with the headline, second line, button and the brand's logo
// laid over it. It is drawn each time from the creative as it is now, so
// changed words show at once and no copy of it is kept.
func (s *Server) creativeAd(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !s.needFiles(w, r) {
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	bad := func(msg string) { writeError(w, r, http.StatusBadRequest, "invalid_request", msg) }
	v, err := findCreative(ctx, s.pool, a.WorkspaceID, a.Brand, r.PathValue("creative"))
	if s.allocError(w, r, err) {
		return
	}
	if v.Kind != "image" {
		bad("only a picture is drawn as an ad; a video is its own file (asset_id)")
		return
	}
	if v.AssetID == nil {
		writeError(w, r, http.StatusConflict, "conflict", "this creative has no picture yet: its status is "+v.Status)
		return
	}
	width := 1080
	if raw := q.Get("width"); raw != "" {
		if width, err = strconv.Atoi(raw); err != nil || width < layout.MinWidth || width > layout.MaxWidth {
			bad("width is " + strconv.Itoa(layout.MinWidth) + " to " + strconv.Itoa(layout.MaxWidth) + " pixels")
			return
		}
	}
	shape, size := 0.0, q.Get("size")
	if size != "" {
		var ok bool
		if shape, ok = layout.Shape(size); !ok {
			bad("size is a shape such as 1:1, 4:5, 9:16 or 16:9")
			return
		}
	}
	format := q.Get("format")
	if format == "" {
		format = "jpeg"
	}
	if format != "jpeg" && format != "png" {
		bad("format is jpeg or png")
		return
	}
	// The brand's newest logo and font, and its button colour.
	var logoID, fontID *string
	var primary string
	if err := s.pool.QueryRow(ctx, `SELECT
			(SELECT id::text FROM brand_assets WHERE workspace_id = $1 AND brand_id = $2 AND kind = 'logo' AND archived_at IS NULL
				AND content_type IN ('image/png', 'image/jpeg', 'image/webp', 'image/gif') ORDER BY created_at DESC LIMIT 1),
			(SELECT id::text FROM brand_assets WHERE workspace_id = $1 AND brand_id = $2 AND kind = 'font' AND archived_at IS NULL
				AND content_type IN ('font/ttf', 'font/otf') ORDER BY created_at DESC LIMIT 1),
			COALESCE((SELECT colors->>'primary' FROM brands WHERE workspace_id = $1 AND id = $2), '')`, a.WorkspaceID, a.Brand).Scan(&logoID, &fontID, &primary); err != nil {
		s.internal(w, r, err)
		return
	}
	if q.Get("logo") == "false" {
		logoID = nil
	}
	part := func(id *string) string {
		if id == nil {
			return ""
		}
		return *id
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{v.ID, strconv.Itoa(v.Version), *v.AssetID, size, strconv.Itoa(width), format, part(logoID), part(fontID), primary}, "|")))
	etag := `"` + hex.EncodeToString(sum[:12]) + `"`
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Cache-Control", "private, no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	start := time.Now()
	failed := func(err error) {
		metrics.CreativeRendered("failed", 0)
		if errors.Is(err, blobs.ErrNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "the creative's picture is missing from storage")
			return
		}
		track.Fail(ctx, s.log, track.Event{Name: "creative.render_failed", WorkspaceID: a.WorkspaceID, ActorType: "system", Surface: "system"}, "unavailable", err)
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "the ad could not be drawn; try again")
	}
	data, err := s.files.Get(ctx, assetKey(a.WorkspaceID, a.Brand, *v.AssetID, "original"))
	if err != nil {
		failed(err)
		return
	}
	ad := layout.Ad{Headline: v.Headline, Subline: v.Subline, CTA: v.CTA}
	if ad.Picture, err = assets.Decode(data); err != nil {
		failed(err)
		return
	}
	// A logo or font that can't be read is left out: the ad still draws.
	if logoID != nil {
		if raw, err := s.files.Get(ctx, assetKey(a.WorkspaceID, a.Brand, *logoID, "original")); err == nil {
			ad.Logo, _ = assets.Decode(raw)
		}
	}
	if fontID != nil {
		ad.Font, _ = s.files.Get(ctx, assetKey(a.WorkspaceID, a.Brand, *fontID, "original"))
	}
	if c, ok := layout.HexColor(primary); ok {
		ad.Accent = &c
	}
	if shape == 0 {
		b := ad.Picture.Bounds()
		shape = min(max(float64(b.Dx())/float64(max(b.Dy(), 1)), 1.0/3), 3)
	}
	m, err := layout.Render(ad, shape, width)
	if err != nil {
		failed(err)
		return
	}
	out, ct, err := layout.Encode(m, format)
	if err != nil {
		failed(err)
		return
	}
	metrics.CreativeRendered("drawn", time.Since(start))
	h.Set("Content-Type", ct)
	h.Set("Content-Length", strconv.Itoa(len(out)))
	h.Set("X-Content-Type-Options", "nosniff")
	disposition := "inline"
	if q.Get("download") == "true" {
		disposition = "attachment"
	}
	ext := map[string]string{"jpeg": ".jpg", "png": ".png"}[format]
	h.Set("Content-Disposition", disposition+`; filename="`+v.Code+"-"+strings.ReplaceAll(size, ":", "x")+ext+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}
