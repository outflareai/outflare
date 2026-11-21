package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// The brand profile is what a brand's agents know about the company: who
// it is, what it sells, who it serves, why it is different, and how it
// talks. It is kept as separate facts in sections, each with where it
// came from, how sure it is, whether it may be said to customers, and
// whether a person confirmed it. Personas (brand_personas.go) are the
// kinds of people the brand sells to.
//
// Two rules hold everywhere:
//
//   - Only a person confirms. What an API token (an agent) adds is a
//     suggestion until a person confirms it, and a suggestion is never
//     used with customers.
//   - Nothing is erased. A fact is retired, not deleted, and every change
//     is a row in brand_profile_changes.
//
// The brand's older context fields (about, audience, goals, voice, rules)
// each have one fact that mirrors them: changing either changes both, in
// one transaction, so there is one truth while both are read.

func (s *Server) routesBrandProfile(mux *http.ServeMux) {
	read, manage := auth.ScopeWorkspaceRead, auth.ScopeContextManage
	mux.HandleFunc("GET /v1/brands/{brand}/profile", s.onBrand(read, s.getBrandProfile))
	mux.HandleFunc("GET /v1/brands/{brand}/profile/facts", s.onBrand(read, s.listProfileFacts))
	mux.HandleFunc("POST /v1/brands/{brand}/profile/facts", s.onBrand(manage, s.createProfileFact))
	mux.HandleFunc("GET /v1/brands/{brand}/profile/facts/{id}", s.onBrand(read, s.getProfileFact))
	mux.HandleFunc("PATCH /v1/brands/{brand}/profile/facts/{id}", s.onBrand(manage, s.updateProfileFact))
	mux.HandleFunc("GET /v1/brands/{brand}/profile/changes", s.onBrand(read, s.listProfileChanges))
	mux.HandleFunc("GET /v1/brands/{brand}/personas", s.onBrand(read, s.listPersonas))
	mux.HandleFunc("POST /v1/brands/{brand}/personas", s.onBrand(manage, s.createPersona))
	mux.HandleFunc("GET /v1/brands/{brand}/personas/{id}", s.onBrand(read, s.getPersona))
	mux.HandleFunc("PATCH /v1/brands/{brand}/personas/{id}", s.onBrand(manage, s.updatePersona))
}

// profileSection is one part of the profile, in the order the page shows.
type profileSection struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// profileSections lists the sections. "personas" holds personas, not
// facts; every other key is a value of brand_profile_facts.section.
var profileSections = []profileSection{
	{"company", "Company", "Who the company is: what it does, where it is, how big it is and how it started."},
	{"offer", "Offer", "What it sells: products, plans, prices, guarantees and what is not on offer."},
	{"customers", "Customers and segments", "Who it serves: the kinds of companies or people, and who is not a fit."},
	{"personas", "Personas", "The kinds of people it sells to: what they want, what hurts and what holds them back."},
	{"positioning", "Positioning", "Why it is different: the problem it solves, for whom, and why it beats the alternatives."},
	{"proof", "Proof", "Why to believe it: results, numbers, customers that may be named, reviews and awards."},
	{"competitors", "Competitors", "How it compares: where it wins, where it loses, and what to say about each alternative."},
	{"objections", "Objections", "What holds buyers back, and the honest answer to each."},
	{"voice", "Voice", "How it sounds: tone, words to use, words to avoid."},
	{"goals", "Goals", "What the company wants from its marketing and sales this quarter and this year."},
	{"channels", "Channels", "Where it reaches people, and what has and hasn't worked."},
	{"rules", "Rules", "What must always or never be said or done: legal limits, claims, topics to avoid."},
}

var (
	profileFactSections   = []string{"company", "offer", "customers", "positioning", "proof", "competitors", "objections", "voice", "goals", "channels", "rules"}
	profileSources        = []string{"person", "interview", "website", "conversation", "agent", "import"}
	profileVisibilities   = []string{"customers", "internal"}
	profileFactStatuses   = []string{"suggested", "confirmed", "retired"}
	profileContextSection = map[string]string{"about": "company", "audience": "customers", "goals": "goals", "voice": "voice", "rules": "rules"}
	profileContextTitle   = map[string]string{"about": "What the brand is", "audience": "Who it is for", "goals": "Goals", "voice": "Voice", "rules": "Rules"}
	profileContextPublic  = map[string]bool{"about": true, "audience": true, "voice": true}
)

const (
	// maxProfileFacts keeps a profile something a person can read and an
	// agent can carry.
	maxProfileFacts    = 500
	maxProfileFactText = 4000
)

// profileEvidence is one reason a fact is believed.
type profileEvidence struct {
	Note string `json:"note"`
	// What it points at: a page, a document or a conversation. Optional.
	Ref string `json:"ref,omitempty"`
}

type profileFactView struct {
	ID         string            `json:"id"`
	BrandID    string            `json:"brand_id"`
	Section    string            `json:"section"`
	Title      string            `json:"title"`
	Text       string            `json:"text"`
	Source     string            `json:"source"`
	Evidence   []profileEvidence `json:"evidence"`
	Confidence string            `json:"confidence"`
	Visibility string            `json:"visibility"`
	Status     string            `json:"status"`
	// True only for a confirmed fact with visibility customers: the one
	// test for whether it may be used in anything a customer reads.
	SayToCustomers bool       `json:"say_to_customers"`
	ReplacesID     *string    `json:"replaces_id"`
	ContextField   *string    `json:"context_field"`
	ReviewAt       *time.Time `json:"review_at"`
	Due            bool       `json:"due"`
	ConfirmedBy    *string    `json:"confirmed_by"`
	ConfirmedAt    *time.Time `json:"confirmed_at"`
	RetiredAt      *time.Time `json:"retired_at"`
	CreatedBy      *string    `json:"created_by"`
	UpdatedBy      *string    `json:"updated_by"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

const profileFactCols = `f.id, f.brand_id, f.section, f.title, f.text, f.source, f.evidence, f.confidence, f.visibility, f.status,
	f.replaces_id, f.context_field, f.review_at, COALESCE(f.status <> 'retired' AND f.review_at <= now(), false),
	cf.display_name, f.confirmed_at, f.retired_at, cr.display_name, up.display_name, f.created_at, f.updated_at`

const profileFactFrom = `FROM brand_profile_facts f
	LEFT JOIN actors cf ON cf.id = f.confirmed_by
	LEFT JOIN actors cr ON cr.id = f.created_by
	LEFT JOIN actors up ON up.id = f.updated_by`

func scanProfileFact(row pgx.CollectableRow) (profileFactView, error) {
	var v profileFactView
	var evidence []byte
	err := row.Scan(&v.ID, &v.BrandID, &v.Section, &v.Title, &v.Text, &v.Source, &evidence, &v.Confidence, &v.Visibility, &v.Status,
		&v.ReplacesID, &v.ContextField, &v.ReviewAt, &v.Due, &v.ConfirmedBy, &v.ConfirmedAt, &v.RetiredAt,
		&v.CreatedBy, &v.UpdatedBy, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return v, err
	}
	if err := json.Unmarshal(evidence, &v.Evidence); err != nil {
		return v, err
	}
	if v.Evidence == nil {
		v.Evidence = []profileEvidence{}
	}
	v.SayToCustomers = v.Status == "confirmed" && v.Visibility == "customers"
	return v, nil
}

// querier is the pool or a transaction.
type profileQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// loadProfileFacts lists a brand's facts in the page's order: by section,
// then position, then oldest first. With retired false, retired facts
// are left out.
func loadProfileFacts(ctx context.Context, q profileQuerier, ws, brand string, retired bool) ([]profileFactView, error) {
	rows, err := q.Query(ctx, `SELECT `+profileFactCols+` `+profileFactFrom+`
		WHERE f.workspace_id = $1 AND f.brand_id = $2 AND ($3 OR f.status <> 'retired')
		ORDER BY array_position($4::text[], f.section), f.position, f.created_at, f.id LIMIT 2000`, ws, brand, retired, profileFactSections)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, scanProfileFact)
	if list == nil {
		list = []profileFactView{}
	}
	return list, err
}

func loadProfileFact(ctx context.Context, q profileQuerier, ws, brand, id string) (profileFactView, error) {
	if !validUUID(id) {
		return profileFactView{}, errNotFound
	}
	rows, err := q.Query(ctx, `SELECT `+profileFactCols+` `+profileFactFrom+`
		WHERE f.workspace_id = $1 AND f.brand_id = $2 AND f.id = $3`, ws, brand, id)
	if err != nil {
		return profileFactView{}, err
	}
	list, err := pgx.CollectRows(rows, scanProfileFact)
	if err != nil {
		return profileFactView{}, err
	}
	if len(list) == 0 {
		return profileFactView{}, errNotFound
	}
	return list[0], nil
}

// profileSectionCount is a section with how full it is.
type profileSectionCount struct {
	profileSection
	Confirmed int `json:"confirmed"`
	Suggested int `json:"suggested"`
	Due       int `json:"due"`
}

type profileCounts struct {
	Confirmed      int `json:"confirmed"`
	Suggested      int `json:"suggested"`
	Due            int `json:"due"`
	SectionsFilled int `json:"sections_filled"`
	SectionsTotal  int `json:"sections_total"`
}

func (s *Server) getBrandProfile(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	facts, err := loadProfileFacts(ctx, s.pool, a.WorkspaceID, a.Brand, false)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	personas, err := loadPersonas(ctx, s.pool, a.WorkspaceID, a.Brand, false)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	sections := make([]profileSectionCount, len(profileSections))
	at := map[string]*profileSectionCount{}
	for i, sec := range profileSections {
		sections[i] = profileSectionCount{profileSection: sec}
		at[sec.Key] = &sections[i]
	}
	for _, f := range facts {
		c := at[f.Section]
		if f.Status == "confirmed" {
			c.Confirmed++
		} else {
			c.Suggested++
		}
		if f.Due {
			c.Due++
		}
	}
	for _, p := range personas {
		if p.Status == "confirmed" {
			at["personas"].Confirmed++
		} else {
			at["personas"].Suggested++
		}
	}
	counts := profileCounts{SectionsTotal: len(sections)}
	for _, c := range sections {
		counts.Confirmed += c.Confirmed
		counts.Suggested += c.Suggested
		counts.Due += c.Due
		if c.Confirmed > 0 {
			counts.SectionsFilled++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"brand_id": a.Brand,
		"sections": sections,
		"facts":    facts,
		"personas": personas,
		"counts":   counts,
	})
}

func (s *Server) listProfileFacts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	qs := r.URL.Query()
	args := []any{a.WorkspaceID, a.Brand}
	where := `f.workspace_id = $1 AND f.brand_id = $2`
	if v := qs.Get("section"); v != "" {
		if !slices.Contains(profileFactSections, v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "section is one of "+strings.Join(profileFactSections, ", "))
			return
		}
		args = append(args, v)
		where += ` AND f.section = $` + strconv.Itoa(len(args))
	}
	switch v := qs.Get("status"); v {
	case "":
		where += ` AND f.status <> 'retired'`
	case "all":
	default:
		if !slices.Contains(profileFactStatuses, v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "status is suggested, confirmed, retired or all")
			return
		}
		args = append(args, v)
		where += ` AND f.status = $` + strconv.Itoa(len(args))
	}
	if v := qs.Get("due"); v != "" {
		due, err := strconv.ParseBool(v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "due is true or false")
			return
		}
		if due {
			where += ` AND f.status <> 'retired' AND f.review_at <= now()`
		}
	}
	before, ok := beforeTime(w, r)
	if !ok {
		return
	}
	limit, ok := pageLimit(w, r, 100, 200)
	if !ok {
		return
	}
	args = append(args, before)
	where += ` AND ($` + strconv.Itoa(len(args)) + `::timestamptz IS NULL OR f.created_at < $` + strconv.Itoa(len(args)) + `)`
	args = append(args, limit)
	rows, err := s.pool.Query(r.Context(), `SELECT `+profileFactCols+` `+profileFactFrom+` WHERE `+where+`
		ORDER BY f.created_at DESC, f.id DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, scanProfileFact)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out == nil {
		out = []profileFactView{}
	}
	var next *string
	if len(out) == limit {
		t := out[len(out)-1].CreatedAt.Format(time.RFC3339Nano)
		next = &t
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "next_cursor": nextCursor(pageBefore, next)})
}

func (s *Server) getProfileFact(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := loadProfileFact(r.Context(), s.pool, a.WorkspaceID, a.Brand, r.PathValue("id"))
	if s.profileFactError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) profileFactError(w http.ResponseWriter, r *http.Request, err error) bool {
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "profile fact not found")
		return true
	}
	return s.allocError(w, r, err)
}

type profileFactInput struct {
	Section    *string            `json:"section"`
	Title      *string            `json:"title"`
	Text       *string            `json:"text"`
	Source     *string            `json:"source"`
	Evidence   *[]profileEvidence `json:"evidence"`
	Confidence *string            `json:"confidence"`
	Visibility *string            `json:"visibility"`
	Status     *string            `json:"status"`
	// On a new suggestion: the fact it would replace once confirmed.
	ReplacesID optional[string]    `json:"replaces_id"`
	ReviewAt   optional[time.Time] `json:"review_at"`
}

func (in *profileFactInput) validate() error {
	if in.Section != nil && !slices.Contains(profileFactSections, *in.Section) {
		return errInvalid("section is one of " + strings.Join(profileFactSections, ", ") + "; personas have their own endpoints")
	}
	if in.Title != nil {
		*in.Title = strings.TrimSpace(*in.Title)
		if len(*in.Title) > 200 {
			return errInvalid("title is at most 200 characters")
		}
	}
	if in.Text != nil {
		*in.Text = strings.TrimSpace(*in.Text)
		// A fact that mirrors a context field may be as long as the field;
		// the handlers hold every other fact to maxProfileFactText.
		if *in.Text == "" || len(*in.Text) > 10000 {
			return errInvalid(errProfileFactText)
		}
	}
	if in.Source != nil && !slices.Contains(profileSources, *in.Source) {
		return errInvalid("source is one of " + strings.Join(profileSources, ", "))
	}
	if in.Evidence != nil {
		if len(*in.Evidence) > 20 {
			return errInvalid("evidence holds at most 20 items")
		}
		for i := range *in.Evidence {
			e := &(*in.Evidence)[i]
			e.Note, e.Ref = strings.TrimSpace(e.Note), strings.TrimSpace(e.Ref)
			if e.Note == "" || len(e.Note) > 500 {
				return errInvalid("each evidence item needs a note of 1 to 500 characters saying what it showed")
			}
			if len(e.Ref) > 500 {
				return errInvalid("an evidence ref is at most 500 characters")
			}
		}
	}
	if in.Confidence != nil && !slices.Contains(findingConfidences, *in.Confidence) {
		return errInvalid("confidence is low, medium or high")
	}
	if in.Visibility != nil && !slices.Contains(profileVisibilities, *in.Visibility) {
		return errInvalid("visibility is customers (may be said to customers) or internal (for the team only)")
	}
	if in.Status != nil && !slices.Contains(profileFactStatuses, *in.Status) {
		return errInvalid("status is suggested, confirmed or retired")
	}
	if in.ReplacesID.Set && in.ReplacesID.Value != nil && !validUUID(*in.ReplacesID.Value) {
		return errInvalid("replaces_id is a profile fact's id")
	}
	return nil
}

var errProfileFactText = fmt.Sprintf("text must be 1 to %d characters: one fact, in plain words", maxProfileFactText)

// errOnlyPeopleConfirm is what an API token gets when it tries to confirm
// or to change what a person confirmed.
const errOnlyPeopleConfirm = errInvalid("only a person confirms or changes a confirmed fact: add a suggestion (with replaces_id for a fact it would replace) and a person will review it")

// profileChange writes one row of the profile's history.
func profileChange(ctx context.Context, tx pgx.Tx, a *access, factID, personaID *string, action string, before, after map[string]any) error {
	if before == nil {
		before = map[string]any{}
	}
	if after == nil {
		after = map[string]any{}
	}
	_, err := tx.Exec(ctx, `INSERT INTO brand_profile_changes (workspace_id, brand_id, fact_id, persona_id, action, before, after, actor_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, a.WorkspaceID, a.Brand, factID, personaID, action, before, after, actorRef(a))
	return err
}

func (s *Server) createProfileFact(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in profileFactInput
	if !decode(w, r, &in) {
		return
	}
	if in.Section == nil || in.Text == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "section and text are required")
		return
	}
	if err := in.validate(); s.allocError(w, r, err) {
		return
	}
	if len(*in.Text) > maxProfileFactText {
		s.allocError(w, r, errInvalid(errProfileFactText))
		return
	}
	human := a.ActorType == "human"
	status, source := "confirmed", "person"
	if !human {
		status, source = "suggested", "agent"
	}
	if in.Status != nil {
		switch {
		case *in.Status == "retired":
			s.allocError(w, r, errInvalid("a new fact is suggested or confirmed; retire it with a PATCH"))
			return
		case *in.Status == "confirmed" && !human:
			s.allocError(w, r, errOnlyPeopleConfirm)
			return
		}
		status = *in.Status
	}
	if in.Source != nil {
		source = *in.Source
	}
	if in.ReplacesID.Value != nil && status != "suggested" {
		s.allocError(w, r, errInvalid("replaces_id goes with a suggestion; to change a fact yourself, PATCH it"))
		return
	}
	evidence := []profileEvidence{}
	if in.Evidence != nil {
		evidence = *in.Evidence
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM brand_profile_facts WHERE brand_id = $1 AND status <> 'retired'`, a.Brand).Scan(&n); err != nil {
			return err
		}
		if n >= maxProfileFacts {
			return errInvalid(fmt.Sprintf("the profile already holds %d facts, the most it keeps; retire ones that no longer hold first", maxProfileFacts))
		}
		if in.ReplacesID.Value != nil {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM brand_profile_facts
				WHERE id = $1 AND brand_id = $2 AND workspace_id = $3 AND status = 'confirmed')`, *in.ReplacesID.Value, a.Brand, a.WorkspaceID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return errInvalid("replaces_id is not a confirmed fact of this brand")
			}
		}
		err := tx.QueryRow(ctx, `INSERT INTO brand_profile_facts (workspace_id, brand_id, section, title, text, source, evidence,
				confidence, visibility, status, replaces_id, review_at, confirmed_by, confirmed_at, created_by, updated_by, position)
			VALUES ($1, $2, $3, $4, $5, $6, $7, COALESCE($8, 'medium'), COALESCE($9, 'internal'), $10, $11, $12,
				CASE WHEN $10 = 'confirmed' THEN $13::uuid END, CASE WHEN $10 = 'confirmed' THEN now() END, $13, $13,
				(SELECT COALESCE(max(position), 0) + 1 FROM brand_profile_facts WHERE brand_id = $2 AND section = $3))
			RETURNING id`,
			a.WorkspaceID, a.Brand, *in.Section, deref(in.Title), *in.Text, source, evidence, in.Confidence, in.Visibility,
			status, in.ReplacesID.Value, in.ReviewAt.Value, actorRef(a)).Scan(&id)
		if err != nil {
			return err
		}
		if err := profileChange(ctx, tx, a, &id, nil, "created", nil,
			map[string]any{"section": *in.Section, "title": deref(in.Title), "text": *in.Text, "status": status}); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "profile_fact.created", "profile_fact", id,
			map[string]any{"brand_id": a.Brand, "section": *in.Section, "title": deref(in.Title), "status": status, "source": source},
			map[string]any{"brand_id": a.Brand, "section": *in.Section, "status": status, "source": source, "evidence": len(evidence)})
	})
	if s.profileFactError(w, r, err) {
		return
	}
	v, err := loadProfileFact(ctx, s.pool, a.WorkspaceID, a.Brand, id)
	if s.profileFactError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) updateProfileFact(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in profileFactInput
	if !decode(w, r, &in) {
		return
	}
	if err := in.validate(); s.allocError(w, r, err) {
		return
	}
	if in.Source != nil || in.ReplacesID.Set {
		s.allocError(w, r, errInvalid("source and replaces_id are set when a fact is added and don't change"))
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := loadProfileFact(ctx, tx, a.WorkspaceID, a.Brand, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM brand_profile_facts WHERE id = $1 FOR UPDATE`, cur.ID); err != nil {
			return err
		}
		human := a.ActorType == "human"
		if !human && (cur.Status != "suggested" || (in.Status != nil && *in.Status == "confirmed")) {
			return errOnlyPeopleConfirm
		}
		confirming := in.Status != nil && *in.Status == "confirmed" && cur.Status == "suggested"
		// Confirming a suggestion that replaces a fact writes it into that
		// fact, so the fact keeps its history, and retires the suggestion.
		if confirming && cur.ReplacesID != nil {
			return s.confirmReplacement(ctx, tx, a, cur, in)
		}
		before, after := map[string]any{}, map[string]any{}
		diff := func(key string, old string, new *string) {
			if new != nil && *new != old {
				before[key], after[key] = old, *new
			}
		}
		diff("section", cur.Section, in.Section)
		diff("title", cur.Title, in.Title)
		diff("text", cur.Text, in.Text)
		diff("confidence", cur.Confidence, in.Confidence)
		diff("visibility", cur.Visibility, in.Visibility)
		diff("status", cur.Status, in.Status)
		if in.Evidence != nil {
			after["evidence"] = len(*in.Evidence)
		}
		if in.ReviewAt.Set {
			after["review_at"] = in.ReviewAt.Value
		}
		if len(after) == 0 {
			return nil
		}
		if in.Text != nil && cur.ContextField == nil && len(*in.Text) > maxProfileFactText {
			return errInvalid(errProfileFactText)
		}
		if cur.ContextField != nil {
			if _, ok := after["section"]; ok {
				return errInvalid("this fact mirrors the brand's \"" + *cur.ContextField + "\" context field and stays in its section")
			}
			if after["status"] == "suggested" {
				return errInvalid("this fact mirrors the brand's \"" + *cur.ContextField + "\" context field: change or retire it")
			}
		}
		newStatus := cur.Status
		if in.Status != nil {
			newStatus = *in.Status
		}
		if _, err := tx.Exec(ctx, `UPDATE brand_profile_facts SET
				section = COALESCE($2, section), title = COALESCE($3, title), text = COALESCE($4, text),
				evidence = COALESCE($5, evidence), confidence = COALESCE($6, confidence), visibility = COALESCE($7, visibility),
				status = $8,
				review_at = CASE WHEN $9 THEN $10::timestamptz ELSE review_at END,
				confirmed_by = CASE WHEN $8 = 'confirmed' AND status <> 'confirmed' THEN $11::uuid WHEN $8 = 'suggested' THEN NULL ELSE confirmed_by END,
				confirmed_at = CASE WHEN $8 = 'confirmed' AND status <> 'confirmed' THEN now() WHEN $8 = 'suggested' THEN NULL ELSE confirmed_at END,
				retired_at = CASE WHEN $8 = 'retired' AND status <> 'retired' THEN now() WHEN $8 <> 'retired' THEN NULL ELSE retired_at END,
				updated_by = $11, updated_at = now()
			WHERE id = $1`,
			cur.ID, in.Section, in.Title, in.Text, in.Evidence, in.Confidence, in.Visibility, newStatus,
			in.ReviewAt.Set, in.ReviewAt.Value, actorRef(a)); err != nil {
			return err
		}
		if cur.ContextField != nil {
			text := cur.Text
			if in.Text != nil {
				text = *in.Text
			}
			if newStatus == "retired" {
				text = ""
			}
			if err := writeContextField(ctx, tx, a, *cur.ContextField, text); err != nil {
				return err
			}
		}
		action, event := "updated", "profile_fact.updated"
		switch {
		case newStatus == cur.Status:
		case newStatus == "confirmed" && cur.Status == "suggested":
			action, event = "confirmed", "profile_fact.confirmed"
		case newStatus == "retired":
			action, event = "retired", "profile_fact.retired"
		case cur.Status == "retired":
			action, event = "restored", "profile_fact.restored"
		}
		if err := profileChange(ctx, tx, a, &cur.ID, nil, action, before, after); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, event, "profile_fact", cur.ID,
			map[string]any{"brand_id": a.Brand, "section": cur.Section, "fields": mapKeys(after)},
			map[string]any{"brand_id": a.Brand, "section": cur.Section, "source": cur.Source, "fields": mapKeys(after)})
	})
	if s.profileFactError(w, r, err) {
		return
	}
	v, err := loadProfileFact(ctx, s.pool, a.WorkspaceID, a.Brand, id)
	if s.profileFactError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// confirmReplacement applies a suggestion to the fact it replaces: the
// fact takes the suggestion's text (after any edit in the same request),
// evidence and confidence and is confirmed by this person; the suggestion
// is retired. Both changes are in the history.
func (s *Server) confirmReplacement(ctx context.Context, tx pgx.Tx, a *access, sug profileFactView, in profileFactInput) error {
	target, err := loadProfileFact(ctx, tx, a.WorkspaceID, a.Brand, *sug.ReplacesID)
	if errors.Is(err, errNotFound) || (err == nil && target.Status == "retired") {
		return errInvalid("the fact this suggestion would replace is retired: retire the suggestion and add it as a fact of its own")
	}
	if err != nil {
		return err
	}
	text, title := sug.Text, sug.Title
	if in.Text != nil {
		text = *in.Text
	}
	if len(text) > maxProfileFactText && target.ContextField == nil {
		return errInvalid(errProfileFactText)
	}
	if in.Title != nil {
		title = *in.Title
	}
	if title == "" {
		title = target.Title
	}
	evidence := sug.Evidence
	if in.Evidence != nil {
		evidence = *in.Evidence
	}
	confidence := sug.Confidence
	if in.Confidence != nil {
		confidence = *in.Confidence
	}
	if _, err := tx.Exec(ctx, `UPDATE brand_profile_facts SET title = $2, text = $3, evidence = $4, confidence = $5,
			confirmed_by = $6, confirmed_at = now(), updated_by = $6, updated_at = now() WHERE id = $1`,
		target.ID, title, text, evidence, confidence, actorRef(a)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE brand_profile_facts SET status = 'retired', retired_at = now(), updated_by = $2, updated_at = now() WHERE id = $1`,
		sug.ID, actorRef(a)); err != nil {
		return err
	}
	if target.ContextField != nil {
		if err := writeContextField(ctx, tx, a, *target.ContextField, text); err != nil {
			return err
		}
	}
	if err := profileChange(ctx, tx, a, &target.ID, nil, "updated",
		map[string]any{"text": target.Text, "title": target.Title},
		map[string]any{"text": text, "title": title, "from_suggestion": sug.ID}); err != nil {
		return err
	}
	if err := profileChange(ctx, tx, a, &sug.ID, nil, "confirmed",
		map[string]any{"status": "suggested"}, map[string]any{"status": "retired", "written_into": target.ID}); err != nil {
		return err
	}
	return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "profile_fact.confirmed", "profile_fact", target.ID,
		map[string]any{"brand_id": a.Brand, "section": target.Section, "suggestion_id": sug.ID},
		map[string]any{"brand_id": a.Brand, "section": target.Section, "source": sug.Source, "replaced": true})
}

// writeContextField sets one of the brand's older context fields from its
// mirror fact, with the context's own audit event.
func writeContextField(ctx context.Context, tx pgx.Tx, a *access, field, text string) error {
	if _, ok := profileContextSection[field]; !ok {
		return fmt.Errorf("unknown context field %q", field)
	}
	for _, f := range brandContextFields {
		if f.key == field && len(text) > f.max {
			return errInvalid(fmt.Sprintf("this fact mirrors the brand's %q context field, which holds at most %d characters", field, f.max))
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO brand_context (brand_id, workspace_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, a.Brand, a.WorkspaceID); err != nil {
		return err
	}
	var version int
	// The column name comes from the fixed list above, never from input.
	err := tx.QueryRow(ctx, `UPDATE brand_context SET `+field+` = $2, version = version + 1, updated_at = now(), updated_by = $3
		WHERE brand_id = $1 AND `+field+` IS DISTINCT FROM $2 RETURNING version`, a.Brand, text, actorRef(a)).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return record(ctx, tx, &a.principal, a.WorkspaceID, "brand.context_updated", "brand", a.Brand,
		map[string]any{"fields": []string{field}, "version": version, "via": "profile"})
}

// mirrorContextField keeps a context field's mirror fact in step after
// the field itself was written: the fact takes the new text, is made when
// the field first has text, and is retired when the field is emptied.
func mirrorContextField(ctx context.Context, tx pgx.Tx, a *access, brand, field, text string) error {
	b := *a
	b.Brand = brand
	var id, old, status string
	err := tx.QueryRow(ctx, `SELECT id, text, status FROM brand_profile_facts
		WHERE brand_id = $1 AND workspace_id = $2 AND context_field = $3 FOR UPDATE`, brand, a.WorkspaceID, field).Scan(&id, &old, &status)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if text == "" {
			return nil
		}
		visibility := "internal"
		if profileContextPublic[field] {
			visibility = "customers"
		}
		if err := tx.QueryRow(ctx, `INSERT INTO brand_profile_facts (workspace_id, brand_id, section, title, text, source, visibility,
				status, context_field, confirmed_by, confirmed_at, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, 'context', $6, 'confirmed', $7, $8, now(), $8, $8) RETURNING id`,
			a.WorkspaceID, brand, profileContextSection[field], profileContextTitle[field], text, visibility, field, actorRef(a)).Scan(&id); err != nil {
			return err
		}
		return profileChange(ctx, tx, &b, &id, nil, "created", nil, map[string]any{"text": text, "section": profileContextSection[field]})
	case err != nil:
		return err
	case text == "":
		if status == "retired" {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE brand_profile_facts SET status = 'retired', retired_at = now(), updated_by = $2, updated_at = now() WHERE id = $1`, id, actorRef(a)); err != nil {
			return err
		}
		return profileChange(ctx, tx, &b, &id, nil, "retired", map[string]any{"status": status}, map[string]any{"status": "retired"})
	case text == old && status != "retired":
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE brand_profile_facts SET text = $2, status = 'confirmed', retired_at = NULL,
			confirmed_by = $3, confirmed_at = now(), updated_by = $3, updated_at = now() WHERE id = $1`, id, text, actorRef(a)); err != nil {
		return err
	}
	action := "updated"
	if status == "retired" {
		action = "restored"
	}
	return profileChange(ctx, tx, &b, &id, nil, action, map[string]any{"text": old}, map[string]any{"text": text})
}

// profileBrief is a confirmed fact as an agent reads it in the brand's
// context: the text and whether it may be said to customers.
type profileBrief struct {
	Section        string `json:"section"`
	Title          string `json:"title"`
	Text           string `json:"text"`
	SayToCustomers bool   `json:"say_to_customers"`
}

// loadProfileBrief lists the brand's confirmed facts for its context,
// without the ones that mirror a context field (the context already
// carries those fields).
func (s *Server) loadProfileBrief(ctx context.Context, ws, brand string, limit int) ([]profileBrief, error) {
	rows, err := s.pool.Query(ctx, `SELECT section, title, text, visibility = 'customers' FROM brand_profile_facts
		WHERE workspace_id = $1 AND brand_id = $2 AND status = 'confirmed' AND context_field IS NULL
		ORDER BY array_position($3::text[], section), position, created_at, id LIMIT $4`, ws, brand, profileFactSections, limit)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[profileBrief])
	if list == nil {
		list = []profileBrief{}
	}
	return list, err
}

// profileChangeView is one row of the profile's history.
type profileChangeView struct {
	ID        string         `json:"id"`
	FactID    *string        `json:"fact_id"`
	PersonaID *string        `json:"persona_id"`
	Action    string         `json:"action"`
	Before    map[string]any `json:"before"`
	After     map[string]any `json:"after"`
	Actor     *string        `json:"actor"`
	ActorType *string        `json:"actor_type"`
	CreatedAt time.Time      `json:"created_at"`
}

func (s *Server) listProfileChanges(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	qs := r.URL.Query()
	args := []any{a.WorkspaceID, a.Brand}
	where := `c.workspace_id = $1 AND c.brand_id = $2`
	for _, k := range []string{"fact_id", "persona_id"} {
		if v := qs.Get(k); v != "" {
			if !validUUID(v) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", k+" is an id")
				return
			}
			args = append(args, v)
			where += ` AND c.` + k + ` = $` + strconv.Itoa(len(args))
		}
	}
	before, ok := beforeTime(w, r)
	if !ok {
		return
	}
	limit, ok := pageLimit(w, r, 50, 200)
	if !ok {
		return
	}
	args = append(args, before)
	where += ` AND ($` + strconv.Itoa(len(args)) + `::timestamptz IS NULL OR c.created_at < $` + strconv.Itoa(len(args)) + `)`
	args = append(args, limit)
	rows, err := s.pool.Query(r.Context(), `SELECT c.id, c.fact_id, c.persona_id, c.action, c.before, c.after, ac.display_name, ac.type, c.created_at
		FROM brand_profile_changes c LEFT JOIN actors ac ON ac.id = c.actor_id
		WHERE `+where+` ORDER BY c.created_at DESC, c.id DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[profileChangeView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out == nil {
		out = []profileChangeView{}
	}
	var next *string
	if len(out) == limit {
		t := out[len(out)-1].CreatedAt.Format(time.RFC3339Nano)
		next = &t
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "next_cursor": nextCursor(pageBefore, next)})
}
