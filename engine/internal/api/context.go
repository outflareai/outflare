package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/missions"
)

// Workspace context: what Outflare remembers about the business, so people
// never have to tell each agent, chat or teammate again. The company
// context is one record per workspace; memories are short facts agents and
// people save as they work. Both are data agents read: they never change
// limits, scopes or the send policy.

func (s *Server) routesContext(mux *http.ServeMux) {
	read, manage := auth.ScopeWorkspaceRead, auth.ScopeContextManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/context", s.inBrand(read, s.getContext))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/context", s.inWorkspace(manage, s.updateContext))
	mux.HandleFunc("GET /v1/workspaces/{ws}/memories", s.inBrand(read, s.listMemories))
	mux.HandleFunc("POST /v1/workspaces/{ws}/memories", s.inBrand(manage, s.createMemory))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/memories/{id}", s.inBrand(manage, s.updateMemory))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/memories/{id}", s.inBrand(manage, s.deleteMemory))
}

// contextFields are the company context's text fields, in the order they
// are shown, with their limits and labels. The first four are the ones
// onboarding asks for.
var contextFields = []struct {
	key, label string
	max        int
}{
	{"company", "Company", 200},
	{"about", "What the company does", 4000},
	{"goals", "Goals", 4000},
	{"voice", "Voice", 4000},
	{"website", "Website", 253},
	{"rules", "Rules", 4000},
	{"notes", "Notes", 100000},
	{"brands", "How our brands relate", 4000},
}

// onboardingFields are asked for until they are filled or a person
// dismisses the question.
var onboardingFields = []string{"company", "about", "goals", "voice"}

type contextView struct {
	Company string `json:"company"`
	Website string `json:"website"`
	About   string `json:"about"`
	Goals   string `json:"goals"`
	Voice   string `json:"voice"`
	Rules   string `json:"rules"`
	Notes   string `json:"notes"`
	// How the workspace's brands relate to each other, for companies that
	// market more than one.
	Brands string `json:"brands"`
	// The company's logo and colours, which the consent banner and
	// anything else on brand start from.
	LogoURL   string     `json:"logo_url"`
	Colors    lookColors `json:"colors"`
	Version   int        `json:"version"`
	UpdatedAt *time.Time `json:"updated_at"`
	UpdatedBy *string    `json:"updated_by"`
	// Onboarding fields still empty.
	Missing []string `json:"missing"`
	// Whether an agent should ask for the missing fields: false once they
	// are all filled or a person dismissed the question.
	AskForMissing bool `json:"ask_for_missing"`
}

func (v *contextView) field(key string) *string {
	switch key {
	case "company":
		return &v.Company
	case "website":
		return &v.Website
	case "about":
		return &v.About
	case "goals":
		return &v.Goals
	case "voice":
		return &v.Voice
	case "rules":
		return &v.Rules
	case "notes":
		return &v.Notes
	case "brands":
		return &v.Brands
	}
	return nil
}

// loadContext reads "About your company" for a brand: each brand's own
// (brands are fully separate), in the shape the company context had, so
// clients that read it keep working. An empty brand reads the
// workspace's first brand; a workspace with no brand has an empty context
// at version 1.
func (s *Server) loadContext(ctx context.Context, ws, brand string) (contextView, error) {
	v := contextView{Version: 1, Missing: []string{}}
	if brand == "" {
		if err := s.pool.QueryRow(ctx, `SELECT COALESCE(outflare_first_brand($1)::text, '')`, ws).Scan(&brand); err != nil {
			return v, err
		}
	}
	if brand == "" {
		v.Missing = append(v.Missing, onboardingFields...)
		v.AskForMissing = true
		return v, nil
	}
	b, err := s.loadBrandContext(ctx, ws, brand)
	if err != nil {
		return v, err
	}
	return companyView(b), nil
}

// companyView is a brand's context in the company context's shape.
func companyView(b brandContextView) contextView {
	v := contextView{Company: b.Name, Website: b.Website, About: b.About, Goals: b.Goals, Voice: b.Voice, Rules: b.Rules,
		Notes: b.Notes, LogoURL: b.LogoURL, Colors: b.Colors, Version: b.Version, UpdatedAt: b.UpdatedAt, UpdatedBy: b.UpdatedBy,
		Missing: b.Missing, AskForMissing: b.AskForMissing}
	return v
}

// contextMarkdown is a brand's context and the memories read with it as
// one document an agent can keep in its prompt. A nil brand is a workspace
// with no brand yet.
func contextMarkdown(b *brandContextView, memories []memoryView) string {
	var sb strings.Builder
	if b == nil {
		sb.WriteString("# About your company\n\nNo brand yet: make one with create_brand, and save what it does in its context.\n")
	} else {
		sb.WriteString(brandMarkdown(b))
	}
	if len(memories) > 0 {
		sb.WriteString("\n## Remembered\n\n")
		for _, m := range memories {
			fmt.Fprintf(&sb, "- (%s) %s\n", m.Kind, m.Text)
		}
	}
	return sb.String()
}

// contextBrand is one line about a brand, so an agent knows which brands
// exist and can ask which one the work is for.
type contextBrand struct {
	ID        string   `json:"id"`
	Handle    string   `json:"handle"`
	Name      string   `json:"name"`
	Website   string   `json:"website"`
	Platforms []string `json:"platforms"`
	// The first line of the brand's about, or "".
	Summary string `json:"summary"`
}

// brandMarkdown is a brand's context: everything Outflare knows about it,
// its own and shared with no other brand.
func brandMarkdown(b *brandContextView) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n", b.Name)
	if b.Website != "" || len(b.Platforms) > 0 {
		fmt.Fprintf(&sb, "\n%s", b.Website)
		if len(b.Platforms) > 0 {
			if b.Website != "" {
				sb.WriteString(", ")
			}
			fmt.Fprintf(&sb, "on %s", strings.Join(b.Platforms, ", "))
		}
		sb.WriteString("\n")
	}
	if len(b.Links) > 0 {
		sb.WriteString("\nAlso at:\n")
		for _, l := range b.Links {
			name := l.Label
			if name == "" {
				name = strings.ReplaceAll(l.Kind, "_", " ")
			}
			fmt.Fprintf(&sb, "- %s: %s\n", name, l.URL)
		}
	}
	empty := true
	for _, f := range brandContextFields {
		if body := *b.field(f.key); body != "" {
			fmt.Fprintf(&sb, "\n## %s\n\n%s\n", f.label, body)
			empty = false
		}
	}
	if empty {
		sb.WriteString("\nNothing saved about this brand yet.\n")
	}
	return sb.String()
}

// getContext is what an agent reads at the start of every conversation:
// one brand's context (brands share none), its memories with the ones no
// brand has, and the brands. The brand is ?brand=, else the one brand the
// caller is kept to, else the workspace's first.
func (s *Server) getContext(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	brandID := ""
	if b := strings.TrimSpace(r.URL.Query().Get("brand")); b != "" && b != "all" {
		id, err := s.brandIDFor(ctx, a.WorkspaceID, b)
		if errors.Is(err, errNotFound) {
			writeError(w, r, http.StatusNotFound, "not_found", "brand not found: brand is a brand's id or handle")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if !a.sees(&id) {
			writeError(w, r, http.StatusNotFound, "not_found", "brand not found: brand is a brand's id or handle")
			return
		}
		brandID = id
	}
	// Someone kept to one brand reads that brand's context, and anyone
	// else the workspace's first brand's.
	if brandID == "" && len(a.Brands) == 1 {
		brandID = a.Brands[0]
	}
	named := brandID != ""
	if brandID == "" {
		if err := s.pool.QueryRow(ctx, `SELECT COALESCE(outflare_first_brand($1)::text, '')`, a.WorkspaceID).Scan(&brandID); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	v := contextView{Version: 1, Missing: append([]string{}, onboardingFields...), AskForMissing: true}
	var brand *brandContextView
	if brandID != "" {
		b, err := s.loadBrandContext(ctx, a.WorkspaceID, brandID)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		brand, v = &b, companyView(b)
	}
	f := memoryFilter{limit: 30, brand: brandSel{id: "none"}}
	if brandID != "" {
		f = memoryFilter{limit: 30, layer: brandID}
	}
	mems, total, err := s.loadMemories(ctx, a, f)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err := s.pool.Query(ctx, `
		SELECT b.id, b.handle, b.name, b.website, b.platforms, COALESCE(split_part(trim(c.about), E'\n', 1), '')
		FROM brands b LEFT JOIN brand_context c ON c.brand_id = b.id
		WHERE b.workspace_id = $1 AND b.status = 'active'
		  AND ($2::uuid[] IS NULL OR b.id = ANY($2)) AND (NOT $3 OR b.id::text = $4)
		ORDER BY lower(b.name)`, a.WorkspaceID, a.Brands, named, brandID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	brands, err := pgx.CollectRows(rows, pgx.RowToStructByPos[contextBrand])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if brands == nil {
		brands = []contextBrand{}
	}
	knowledge, err := s.knowledgeSummary(ctx, a, brandID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	md := contextMarkdown(brand, mems)
	if !named && len(brands) > 1 {
		var sb strings.Builder
		sb.WriteString("\n## Other brands\n\nThis is one brand's context, and each brand's is its own. This company markets several brands: ask which one the work is for, and read get_context with its brand.\n\n")
		for _, b := range brands {
			if b.ID == brandID {
				continue
			}
			fmt.Fprintf(&sb, "- %s (brand %s", b.Name, b.Handle)
			if b.Website != "" {
				fmt.Fprintf(&sb, ", %s", b.Website)
			}
			sb.WriteString(")")
			if b.Summary != "" {
				fmt.Fprintf(&sb, ": %s", b.Summary)
			}
			sb.WriteString("\n")
		}
		md += sb.String()
	}
	md += knowledgeMarkdown(knowledge)
	writeJSON(w, http.StatusOK, map[string]any{
		"context":      v,
		"brand":        brand,
		"brands":       brands,
		"memories":     mems,
		"memory_count": total,
		"knowledge":    knowledge,
		"markdown":     md,
	})
}

// nilIfEmpty is nil for "", for optional SQL arguments.
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

type contextInput struct {
	Company *string `json:"company"`
	Website *string `json:"website"`
	About   *string `json:"about"`
	Goals   *string `json:"goals"`
	Voice   *string `json:"voice"`
	Rules   *string `json:"rules"`
	Notes   *string `json:"notes"`
	Brands  *string `json:"brands"`
	// The company's logo (https, "" for none) and colours.
	LogoURL *string     `json:"logo_url"`
	Colors  *lookColors `json:"colors"`
	// The version the edit was made against. When set and the context has
	// changed since, the update fails with 409 instead of overwriting.
	Version *int `json:"version"`
	// True stops agents asking for the missing fields; false asks again.
	OnboardingDismissed *bool `json:"onboarding_dismissed"`
}

func (in *contextInput) field(key string) *string {
	return map[string]*string{
		"company": in.Company, "website": in.Website, "about": in.About, "goals": in.Goals,
		"voice": in.Voice, "rules": in.Rules, "notes": in.Notes, "brands": in.Brands,
	}[key]
}

// updateContext edits "About your company" for a brand (?brand=, else
// the one the caller is kept to, else the workspace's first): each brand's
// is its own. The brand's name and website change on the brand itself.
func (s *Server) updateContext(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req contextInput
	if !decode(w, r, &req) {
		return
	}
	for _, f := range contextFields {
		if p := req.field(f.key); p != nil {
			*p = strings.TrimSpace(*p)
			if len(*p) > f.max {
				writeError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("%s must be at most %d characters", f.key, f.max))
				return
			}
		}
	}
	if req.LogoURL != nil {
		logo, err := cleanLogoURL(*req.LogoURL)
		if s.allocError(w, r, err) {
			return
		}
		req.LogoURL = &logo
	}
	if req.Colors != nil && s.allocError(w, r, req.Colors.clean()) {
		return
	}
	if req.Website != nil {
		*req.Website = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(*req.Website, "https://"), "http://"), "/")
	}
	ctx := r.Context()
	brandID := ""
	if ref := strings.TrimSpace(r.URL.Query().Get("brand")); ref != "" {
		id, err := s.brandIDFor(ctx, a.WorkspaceID, ref)
		if errors.Is(err, errNotFound) || (err == nil && !a.sees(&id)) {
			writeError(w, r, http.StatusNotFound, "not_found", "brand not found: brand is a brand's id or handle")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		brandID = id
	} else if len(a.Brands) == 1 {
		brandID = a.Brands[0]
	} else if err := s.pool.QueryRow(ctx, `SELECT COALESCE(outflare_first_brand($1)::text, '')`, a.WorkspaceID).Scan(&brandID); err != nil {
		s.internal(w, r, err)
		return
	}
	if brandID == "" {
		writeError(w, r, http.StatusConflict, "conflict", "each brand has its own context: make a brand first (POST /v1/workspaces/{ws}/brands)")
		return
	}
	cur, err := s.loadBrandContext(ctx, a.WorkspaceID, brandID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if req.Brands != nil && *req.Brands != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "brands: each brand's context is its own now; say how the brands relate in each brand's notes")
		return
	}
	if (req.Company != nil && *req.Company != "" && *req.Company != cur.Name) || (req.Website != nil && *req.Website != cur.Website) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "company and website are the brand's name and website: change them with PATCH /v1/workspaces/{ws}/brands/{brand}")
		return
	}
	in := brandContextInput{About: req.About, Goals: req.Goals, Voice: req.Voice, Rules: req.Rules, Notes: req.Notes,
		Version: req.Version, OnboardingDismissed: req.OnboardingDismissed}
	err = s.writeBrandContext(ctx, a, brandID, in)
	if err == nil && (req.LogoURL != nil && *req.LogoURL != cur.LogoURL || req.Colors != nil && *req.Colors != cur.Colors) {
		err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE brands SET logo_url = COALESCE($3, logo_url), colors = COALESCE($4, colors), updated_at = now()
				WHERE id = $1 AND workspace_id = $2`, brandID, a.WorkspaceID, req.LogoURL, req.Colors); err != nil {
				return err
			}
			return record(ctx, tx, &a.principal, a.WorkspaceID, "brand.updated", "brand", brandID, map[string]any{"fields": []string{"logo_url", "colors"}})
		})
	}
	if errors.Is(err, errStaleBrief) {
		writeError(w, r, http.StatusConflict, "context_changed", "the brand's context changed since you loaded it; reload it and apply your edit again")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v, err := s.loadContext(ctx, a.WorkspaceID, brandID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// ---- Memories ----

var memoryKinds = []string{"preference", "decision", "fact", "feedback"}

// maxMemories caps a workspace's memories, so they stay a short list worth
// reading rather than a log.
const maxMemories = 1000

type memoryView struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Text string `json:"text"`
	// The brand it is true for; null: the whole company.
	BrandID     *string   `json:"brand_id"`
	Pinned      bool      `json:"pinned"`
	CreatedBy   *string   `json:"created_by"`
	CreatedByID *string   `json:"created_by_id"`
	MissionID   *string   `json:"mission_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type memoryFilter struct {
	q, kind string
	// brand: a brandFilter value. layer: a brand id whose memories are read
	// with the company's (those with no brand).
	brand         brandSel
	layer         string
	pinned        *bool
	limit, offset int
	id            string
}

// loadMemories lists memories, pinned first, then the most recently
// changed.
func (s *Server) loadMemories(ctx context.Context, a *access, f memoryFilter) ([]memoryView, int, error) {
	var id *string
	if f.id != "" {
		id = &f.id
	}
	q := strings.TrimSpace(f.q)
	rows, err := s.pool.Query(ctx, `
		SELECT m.id, m.kind, m.text, m.brand_id, m.pinned, ac.display_name, m.created_by, m.mission_id,
		       m.created_at, m.updated_at, count(*) OVER ()::int
		FROM memories m LEFT JOIN actors ac ON ac.id = m.created_by
		WHERE m.workspace_id = $1
		  AND ($2 = '' OR m.text ILIKE '%' || $2 || '%')
		  AND ($3 = '' OR m.kind = $3)
		  AND ($4::boolean IS NULL OR m.pinned = $4)
		  AND ($5::uuid IS NULL OR m.id = $5)
		  AND ($8 = '' OR ($8 = 'none' AND m.brand_id IS NULL) OR m.brand_id::text = $8)
		  AND ($9 = '' OR m.brand_id::text = $9)
		  AND ($10::uuid[] IS NULL OR m.brand_id = ANY($10))
		ORDER BY m.pinned DESC, m.updated_at DESC, m.id
		LIMIT $6 OFFSET $7`,
		a.WorkspaceID, likeEscape(q), f.kind, f.pinned, id, f.limit, f.offset, f.brand.id, f.layer, a.Brands)
	if err != nil {
		return nil, 0, err
	}
	total := 0
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (memoryView, error) {
		var m memoryView
		err := row.Scan(&m.ID, &m.Kind, &m.Text, &m.BrandID, &m.Pinned, &m.CreatedBy, &m.CreatedByID, &m.MissionID,
			&m.CreatedAt, &m.UpdatedAt, &total)
		return m, err
	})
	if list == nil {
		list = []memoryView{}
	}
	return list, total, err
}

// likeEscape makes s match literally inside an ILIKE pattern.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (s *Server) listMemories(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	qs := r.URL.Query()
	f := memoryFilter{q: qs.Get("q"), kind: qs.Get("kind"), limit: 50}
	if f.kind != "" && !slices.Contains(memoryKinds, f.kind) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is preference, decision, fact or feedback")
		return
	}
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	f.brand = brand
	if p := qs.Get("pinned"); p != "" {
		b, err := strconv.ParseBool(p)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "pinned is true or false")
			return
		}
		f.pinned = &b
	}
	if v := qs.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be a whole number from 1 to 200")
			return
		}
		f.limit = n
	}
	if f.offset, ok = pageOffsetParam(w, r); !ok {
		return
	}
	list, total, err := s.loadMemories(r.Context(), a, f)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *int
	if f.offset+len(list) < total {
		n := f.offset + len(list)
		next = &n
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "total": total, "next_cursor": nextCursor(pageOffset, next)})
}

type memoryInput struct {
	Kind *string `json:"kind"`
	Text *string `json:"text"`
	// A brand's id or handle.
	BrandID optional[string] `json:"brand_id"`
	Pinned  *bool            `json:"pinned"`
}

func (in *memoryInput) validate() string {
	if in.Kind != nil && !slices.Contains(memoryKinds, *in.Kind) {
		return "kind is preference, decision, fact or feedback"
	}
	if in.Text != nil {
		*in.Text = strings.Join(strings.Fields(*in.Text), " ")
		if *in.Text == "" || len(*in.Text) > 500 {
			return "text must be 1 to 500 characters: one lasting fact, preference or decision, not a transcript"
		}
	}
	return ""
}

// missionFrom is the mission a call acts in, from the mission header, when
// it is one of the workspace's.
func (s *Server) missionFrom(ctx context.Context, r *http.Request, ws string) (*string, error) {
	id := strings.TrimSpace(r.Header.Get(missions.Header))
	if !validUUID(id) {
		return nil, nil
	}
	var found bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM missions WHERE id = $1 AND workspace_id = $2)`, id, ws).Scan(&found); err != nil || !found {
		return nil, err
	}
	return &id, nil
}

func (s *Server) createMemory(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req memoryInput
	if !decode(w, r, &req) {
		return
	}
	if req.Text == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "text is required")
		return
	}
	if msg := req.validate(); msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	kind := "fact"
	if req.Kind != nil {
		kind = *req.Kind
	}
	ctx := r.Context()
	mission, err := s.missionFrom(ctx, r, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	brand, err := s.brandRef(ctx, a.WorkspaceID, req.BrandID.Value)
	if s.allocError(w, r, err) {
		return
	}
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if brand, err = ownBrand(ctx, tx, a, brand); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM memories WHERE workspace_id = $1`, a.WorkspaceID).Scan(&n); err != nil {
			return err
		}
		if n >= maxMemories {
			return errInvalid(fmt.Sprintf("the workspace already has %d memories, the most it keeps; delete ones that are out of date first", maxMemories))
		}
		// Saving the same thing twice keeps one memory.
		err := tx.QueryRow(ctx, `
			SELECT id FROM memories WHERE workspace_id = $1 AND lower(text) = lower($2) AND brand_id IS NOT DISTINCT FROM $3::uuid`,
			a.WorkspaceID, *req.Text, brand).Scan(&id)
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE memories SET kind = $2, pinned = pinned OR COALESCE($3, false), updated_at = now() WHERE id = $1`, id, kind, req.Pinned)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO memories (workspace_id, kind, text, pinned, created_by, mission_id, brand_id)
			VALUES ($1, $2, $3, COALESCE($4, false), $5, $6, $7) RETURNING id`,
			a.WorkspaceID, kind, *req.Text, req.Pinned, a.ActorID, mission, brand).Scan(&id)
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "memory.created", "memory", id, map[string]any{"kind": kind, "text": *req.Text, "brand_id": brand, "mission_id": mission})
	})
	if s.allocError(w, r, err) {
		return
	}
	s.writeMemory(w, r, a, id, http.StatusCreated)
}

func (s *Server) writeMemory(w http.ResponseWriter, r *http.Request, a *access, id string, status int) {
	list, _, err := s.loadMemories(r.Context(), a, memoryFilter{id: id, limit: 1})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if len(list) == 0 {
		writeError(w, r, http.StatusNotFound, "not_found", "memory not found")
		return
	}
	writeJSON(w, status, list[0])
}

// memoryFor checks the caller may change the memory: it is the
// workspace's and in the caller's brands.
func memoryFor(ctx context.Context, tx pgx.Tx, a *access, id string) error {
	if !validUUID(id) {
		return errNotFound
	}
	var brand *string
	err := tx.QueryRow(ctx, `SELECT brand_id::text FROM memories WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, id, a.WorkspaceID).Scan(&brand)
	if errors.Is(err, pgx.ErrNoRows) {
		return errNotFound
	}
	if err != nil {
		return err
	}
	if !a.sees(brand) {
		return errNotFound
	}
	if !a.writes(brand) {
		return errSharedRecord
	}
	return nil
}

func (s *Server) updateMemory(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var req memoryInput
	if !decode(w, r, &req) {
		return
	}
	if msg := req.validate(); msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	ctx := r.Context()
	brand, err := s.brandRef(ctx, a.WorkspaceID, req.BrandID.Value)
	if s.allocError(w, r, err) {
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := memoryFor(ctx, tx, a, id); err != nil {
			return err
		}
		// A move must land in one of a brand-limited caller's brands.
		if a.Brands != nil && req.BrandID.Set {
			if brand == nil {
				return errInvalid("you can only keep memories in your brands")
			}
			if _, err := brandWrite(ctx, tx, a, brand); err != nil {
				return err
			}
		}
		if req.BrandID.Set {
			if err := refuseNoBrand(ctx, tx, a.WorkspaceID, brand); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `
			UPDATE memories SET kind = COALESCE($2, kind), text = COALESCE($3, text), pinned = COALESCE($4, pinned),
				brand_id = CASE WHEN $5 THEN $6::uuid ELSE brand_id END, updated_at = now()
			WHERE id = $1`, id, req.Kind, req.Text, req.Pinned, req.BrandID.Set, brand)
		if err != nil {
			return err
		}
		data := map[string]any{}
		if req.Text != nil {
			data["text"] = *req.Text
		}
		if req.Kind != nil {
			data["kind"] = *req.Kind
		}
		if req.Pinned != nil {
			data["pinned"] = *req.Pinned
		}
		if req.BrandID.Set {
			data["brand_id"] = brand
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "memory.updated", "memory", id, data)
	})
	if s.allocError(w, r, err) {
		return
	}
	s.writeMemory(w, r, a, id, http.StatusOK)
}

func (s *Server) deleteMemory(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := memoryFor(ctx, tx, a, id); err != nil {
			return err
		}
		var text string
		if err := tx.QueryRow(ctx, `DELETE FROM memories WHERE id = $1 RETURNING text`, id).Scan(&text); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "memory.deleted", "memory", id, map[string]any{"text": text})
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
