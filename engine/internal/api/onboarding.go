package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Onboarding: the steps a new workspace is walked through, each one
// skippable. It aims at a first real result, not a full settings page, and
// sets up one brand: the brand, an AI key (with why it is needed), what to
// do first, the brand's website script and DNS in one go, the tools that
// goal needs, a star and a follow, then a first mission. Inviting the team comes after, on Home. Whether a step is done
// is read from the workspace itself (a brand exists, an AI is connected, a
// mission was drafted), so doing the same thing anywhere else in the app,
// or through the API or MCP, ticks it off too. Only the goal, skips and
// closing the walk-through are stored. Nothing here is required: Outflare
// runs with every step skipped.

func (s *Server) routesOnboarding(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/onboarding", s.inWorkspace(auth.ScopeWorkspaceRead, s.getOnboarding))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/onboarding", s.inWorkspace(auth.ScopeWorkspaceManage, s.updateOnboarding))
}

type onboardingStepDef struct {
	id, title, detail, page, tool string
	// walkthrough: shown on the full-page walk-through; the others are only
	// on Home's checklist, for once Outflare has shown what it does.
	walkthrough bool
}

// onboardingSteps are the steps in the order they are shown. tool is the
// MCP tool an agent calls to do the step, empty when a person must.
var onboardingSteps = []onboardingStepDef{
	{"brand", "Add your brand", "Start with one brand: its website and a line on what it does. Every module uses it, so you never type it twice. Add more brands later.", "brands", "create_brand", true},
	{"ai", "Add your AI key", "Outflare has no AI of its own and never pays for yours. Your key lets its agent plan missions, write emails and research companies, billed by your AI provider.", "ai", "request_connection", true},
	{"goal", "Pick what to do first", "Outflare suggests the tools and the first mission for it.", "get-started", "update_onboarding", true},
	{"website", "Connect your website", "One script on your pages and one set of DNS records, so this brand's visits, sign-ups and search show up from today.", "setup", "get_brand_dns", true},
	{"connect", "Connect your tools", "Only what your first goal needs, such as analytics, Search Console or your CRM.", "connectors", "request_connection", true},
	{"community", "Star and follow Outflare", "Outflare is free and open source. A star helps other teams find it, and on X you hear about new features first.", "get-started", "", true},
	{"mission", "Start your first mission", "Say what you want in plain words; your AI drafts the plan for you to approve.", "missions", "plan_mission", true},
	{"team", "Invite your team", "Teammates share the same connections, context and inbox.", "members", "", false},
}

// onboardingGoals are what a workspace can pick to do first.
var onboardingGoals = []string{"outreach", "search", "reports", "nurture"}

func onboardingStep(id string) bool {
	return slices.ContainsFunc(onboardingSteps, func(st onboardingStepDef) bool { return st.id == id })
}

// stepsFor is the steps this server shows: the community step only when
// it has community links (GET /v1/me/community).
func (s *Server) stepsFor() []onboardingStepDef {
	if len(s.community) > 0 {
		return onboardingSteps
	}
	return slices.DeleteFunc(slices.Clone(onboardingSteps), func(st onboardingStepDef) bool { return st.id == "community" })
}

type onboardingStepView struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	// Done: the workspace already has it, however it was added.
	Done bool `json:"done"`
	// Skipped: a person chose to leave it for later. A done step is never
	// skipped.
	Skipped bool `json:"skipped"`
	// Walkthrough: part of the full-page walk-through; false for steps
	// left to Home's checklist.
	Walkthrough bool `json:"walkthrough"`
	// The dashboard page that does the step, and the MCP tool, if an agent can.
	Page string `json:"page"`
	Tool string `json:"tool"`
}

type onboardingView struct {
	Steps []onboardingStepView `json:"steps"`
	Done  int                  `json:"done"`
	Total int                  `json:"total"`
	// The first step neither done nor skipped; null when none is left.
	NextStep *string `json:"next_step"`
	// Every step is done or skipped.
	Complete bool `json:"complete"`
	// What the workspace picked to do first; null until it picks.
	Goal *string `json:"goal"`
	// A person closed the walk-through.
	Dismissed   bool       `json:"dismissed"`
	DismissedAt *time.Time `json:"dismissed_at"`
	// Whether the app should open the walk-through by itself: nothing done
	// or skipped yet, and nobody closed it.
	Show bool `json:"show"`
	// The brand the walk-through sets up: the workspace's first active
	// brand, null until there is one. More brands come later, on Brands.
	Brand *onboardingBrand `json:"brand"`
}

type onboardingBrand struct {
	ID      string `json:"id"`
	Handle  string `json:"handle"`
	Name    string `json:"name"`
	Website string `json:"website"`
}

// connectorsIn lists the connector ids in the given categories, or, with
// not set, in every other category.
func (s *Server) connectorsIn(not bool, categories ...string) []string {
	out := []string{}
	if s.integrations == nil {
		return out
	}
	for _, d := range s.integrations.Registry.All() {
		if slices.Contains(categories, d.Category) != not {
			out = append(out, d.ID)
		}
	}
	return out
}

func (s *Server) loadOnboarding(ctx context.Context, ws string) (onboardingView, error) {
	v := onboardingView{Steps: []onboardingStepView{}}
	skipped := []string{}
	err := s.pool.QueryRow(ctx, `SELECT skipped, goal, dismissed_at FROM workspace_onboarding WHERE workspace_id = $1`, ws).Scan(&skipped, &v.Goal, &v.DismissedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return v, err
	}
	models := s.connectorsIn(false, connectors.CategoryModel)
	tools := s.connectorsIn(true, connectors.CategoryModel)
	done := map[string]bool{"goal": v.Goal != nil}
	var brand, team, ai, connected, mission bool
	// An AI is connected when the workspace has its own model key, or an
	// AI app (Claude, ChatGPT) runs Outflare over MCP.
	err = s.pool.QueryRow(ctx, `
		SELECT
			EXISTS (SELECT 1 FROM brands WHERE workspace_id = $1 AND status = 'active'),
			(SELECT count(*) FROM memberships WHERE workspace_id = $1) > 1
				OR EXISTS (SELECT 1 FROM invites WHERE workspace_id = $1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()),
			EXISTS (SELECT 1 FROM integrations WHERE workspace_id = $1 AND connector = ANY($2))
				OR EXISTS (SELECT 1 FROM oauth_grants g JOIN api_tokens t ON t.id = g.api_token_id
					WHERE g.workspace_id = $1 AND t.revoked_at IS NULL AND g.expires_at > now()),
			EXISTS (SELECT 1 FROM integrations WHERE workspace_id = $1 AND connector = ANY($3)),
			EXISTS (SELECT 1 FROM missions WHERE workspace_id = $1)`,
		ws, models, tools,
	).Scan(&brand, &team, &ai, &connected, &mission)
	if err != nil {
		return v, err
	}
	done["brand"], done["team"], done["ai"], done["connect"], done["mission"] = brand, team, ai, connected, mission

	// The website step is the first brand's script and DNS: done once each
	// is in place or Outflare is waiting to see it (a first visit, the
	// record showing in DNS), since the person has done their part.
	if brand {
		first, err := s.firstBrand(ctx, ws)
		if err != nil {
			return v, err
		}
		if first != nil {
			v.Brand = &onboardingBrand{ID: first.ID, Handle: first.Handle, Name: first.Name, Website: first.Website}
			setup, err := s.loadBrandSetup(ctx, ws, first)
			if err != nil {
				return v, err
			}
			started := map[string]bool{}
			for _, st := range setup.Steps {
				started[st.ID] = st.Status == "done" || st.Status == "waiting"
			}
			done["website"] = started[stepSiteScript] && started[stepVerifySite]
		}
	}

	// Starring and following is each person's own, so the step is done for
	// whoever opened every community link.
	user := ""
	if a := accessFrom(ctx); a != nil {
		user = a.UserID
	} else if p := principalFrom(ctx); p != nil {
		user = p.UserID
	}
	if user != "" && len(s.community) > 0 {
		var clicked []string
		if err := s.pool.QueryRow(ctx, `SELECT community_clicked FROM users WHERE id = $1`, user).Scan(&clicked); err != nil {
			return v, err
		}
		done["community"] = !slices.ContainsFunc(s.community, func(l communityLink) bool { return !slices.Contains(clicked, l.ID) })
	}

	skips := 0
	base := s.appPath(ctx, ws, "")
	for _, st := range s.stepsFor() {
		sv := onboardingStepView{
			ID: st.id, Title: st.title, Detail: st.detail, Tool: st.tool, Walkthrough: st.walkthrough,
			Done: done[st.id], Page: base + st.page,
		}
		sv.Skipped = !sv.Done && slices.Contains(skipped, st.id)
		if sv.Done {
			v.Done++
		}
		if sv.Skipped {
			skips++
		}
		if !sv.Done && !sv.Skipped && v.NextStep == nil {
			id := st.id
			v.NextStep = &id
		}
		v.Steps = append(v.Steps, sv)
	}
	v.Total = len(v.Steps)
	v.Complete = v.NextStep == nil
	v.Dismissed = v.DismissedAt != nil
	v.Show = !v.Dismissed && v.Done == 0 && skips == 0
	return v, nil
}

// firstBrand is the workspace's oldest active brand, the one Get started
// sets up, or nil.
func (s *Server) firstBrand(ctx context.Context, ws string) (*brandRecord, error) {
	var id string
	err := s.pool.QueryRow(ctx, `SELECT id FROM brands WHERE workspace_id = $1 AND status = 'active' ORDER BY created_at, id LIMIT 1`, ws).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	list, err := s.loadBrands(ctx, ws, &id, "all")
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

func (s *Server) getOnboarding(w http.ResponseWriter, r *http.Request) {
	v, err := s.loadOnboarding(r.Context(), accessFrom(r.Context()).WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type onboardingInput struct {
	// Steps to leave for later, and skipped steps to bring back.
	Skip   []string `json:"skip"`
	Unskip []string `json:"unskip"`
	// What to do first: outreach, search, reports or nurture.
	Goal *string `json:"goal"`
	// True closes the walk-through; false opens it again.
	Dismissed *bool `json:"dismissed"`
}

func (s *Server) updateOnboarding(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req onboardingInput
	if !decode(w, r, &req) {
		return
	}
	for _, id := range append(slices.Clone(req.Skip), req.Unskip...) {
		if !onboardingStep(id) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "no onboarding step "+id+": use brand, goal, ai, connect, community, mission or team")
			return
		}
	}
	if req.Goal != nil && !slices.Contains(onboardingGoals, *req.Goal) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "goal must be outreach, search, reports or nurture")
		return
	}
	ctx := r.Context()
	before, err := s.loadOnboarding(ctx, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO workspace_onboarding (workspace_id) VALUES ($1) ON CONFLICT DO NOTHING`, a.WorkspaceID); err != nil {
			return err
		}
		var skipped []string
		var goal *string
		var dismissed *time.Time
		if err := tx.QueryRow(ctx, `SELECT skipped, goal, dismissed_at FROM workspace_onboarding WHERE workspace_id = $1 FOR UPDATE`, a.WorkspaceID).Scan(&skipped, &goal, &dismissed); err != nil {
			return err
		}
		var added, removed []string
		for _, id := range req.Skip {
			if !slices.Contains(skipped, id) && !slices.Contains(added, id) {
				skipped = append(skipped, id)
				added = append(added, id)
			}
		}
		for _, id := range req.Unskip {
			if i := slices.Index(skipped, id); i >= 0 {
				skipped = slices.Delete(skipped, i, i+1)
				removed = append(removed, id)
			}
		}
		closing := req.Dismissed != nil && *req.Dismissed && dismissed == nil
		opening := req.Dismissed != nil && !*req.Dismissed && dismissed != nil
		picked := req.Goal != nil && (goal == nil || *goal != *req.Goal)
		if picked {
			goal = req.Goal
		}
		if len(added) == 0 && len(removed) == 0 && !closing && !opening && !picked {
			return nil
		}
		_, err := tx.Exec(ctx, `
			UPDATE workspace_onboarding SET skipped = $2, goal = $6,
				dismissed_at = CASE WHEN $3 THEN now() WHEN $4 THEN NULL ELSE dismissed_at END,
				updated_at = now(), updated_by = $5
			WHERE workspace_id = $1`, a.WorkspaceID, skipped, closing, opening, a.ActorID, goal)
		if err != nil {
			return err
		}
		for _, id := range added {
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, "onboarding.step_skipped", "onboarding_step", id, map[string]any{"step": id}); err != nil {
				return err
			}
		}
		if picked {
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, "onboarding.goal_chosen", "onboarding_goal", *goal, map[string]any{"goal": *goal}); err != nil {
				return err
			}
		}
		for _, id := range removed {
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, "onboarding.step_unskipped", "onboarding_step", id, map[string]any{"step": id}); err != nil {
				return err
			}
		}
		if closing || opening {
			action := "onboarding.dismissed"
			if opening {
				action = "onboarding.reopened"
			}
			return record(ctx, tx, &a.principal, a.WorkspaceID, action, "workspace", a.WorkspaceID, map[string]any{"done": before.Done, "skipped": len(skipped), "total": before.Total})
		}
		return nil
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v, err := s.loadOnboarding(ctx, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
