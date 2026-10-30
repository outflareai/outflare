package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/engain"
	"github.com/outflareai/outflare/engine/internal/missions"
)

// Reddit campaigns: comments, replies, posts, thread groups and upvotes
// placed through a managed account network (Engain) with the workspace's
// own key, by people or agents, within each brand's budget. Every action is
// logged with what it tested; a poller (reddit_campaigns_jobs.go) follows
// it on Engain and on Reddit as a logged-out visitor sees it, so how long
// each mention lived is known. Links carry no tracking parameters.

func (s *Server) routesRedditCampaigns(mux *http.ServeMux) {
	read, manage, post := auth.ScopeSocialRead, auth.ScopeSocialManage, auth.ScopeSocialPost
	mux.HandleFunc("GET /v1/workspaces/{ws}/reddit/settings", s.inBrand(read, s.listRedditSettings))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/reddit/settings/{brand}", s.inBrand(manage, s.putRedditSettings))
	mux.HandleFunc("GET /v1/workspaces/{ws}/reddit/budget", s.inBrand(read, s.getRedditBudget))
	mux.HandleFunc("GET /v1/workspaces/{ws}/reddit/experiments", s.inBrand(read, s.listRedditExperiments))
	mux.HandleFunc("POST /v1/workspaces/{ws}/reddit/experiments", s.inBrand(manage, s.createRedditExperiment))
	mux.HandleFunc("GET /v1/workspaces/{ws}/reddit/experiments/{id}", s.inBrand(read, s.getRedditExperiment))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/reddit/experiments/{id}", s.inBrand(manage, s.updateRedditExperiment))
	mux.HandleFunc("GET /v1/workspaces/{ws}/reddit/actions", s.inBrand(read, s.listRedditActions))
	mux.HandleFunc("POST /v1/workspaces/{ws}/reddit/actions", s.inBrand(manage, s.createRedditAction))
	mux.HandleFunc("GET /v1/workspaces/{ws}/reddit/actions/{id}", s.inBrand(read, s.getRedditAction))
	mux.HandleFunc("POST /v1/workspaces/{ws}/reddit/actions/{id}/approve", s.inBrand(post, s.approveRedditAction))
	mux.HandleFunc("POST /v1/workspaces/{ws}/reddit/actions/{id}/cancel", s.inBrand(manage, s.cancelRedditAction))
	mux.HandleFunc("POST /v1/workspaces/{ws}/reddit/actions/{id}/check", s.inBrand(manage, s.checkRedditActionNow))
	mux.HandleFunc("POST /v1/workspaces/{ws}/reddit/preflight", s.inBrand(manage, s.redditPreflight))
	mux.HandleFunc("GET /v1/workspaces/{ws}/reddit/subreddits/{name}", s.inBrand(read, s.getRedditSubreddit))
	mux.HandleFunc("GET /v1/workspaces/{ws}/reddit/threads", s.inBrand(read, s.listRedditThreads))
	mux.HandleFunc("GET /v1/workspaces/{ws}/reddit/report", s.inBrand(read, s.getRedditReport))
}

// Estimated credits per action, before Engain's ledger says the real
// figure (Engain's published prices).
const (
	redditCreditsComment = 5.0
	redditCreditsPost    = 10.0
	redditCreditsVote    = 0.1
)

var (
	redditKinds      = []string{"comment", "reply", "post", "thread", "upvotes"}
	redditLinkTypes  = []string{"none", "brand_site", "review_site", "docs", "third_party", "other"}
	redditTiers      = []string{"", "shared", "dedicated"}
	redditStarts     = []string{"", "immediately", "after_3_days", "after_5_days", "after_7_days"}
	redditStatuses   = []string{"draft", "approved", "sending", "submitted", "live", "frozen", "removed", "hidden", "gone", "failed", "cancelled", "done"}
	redditURLPattern = regexp.MustCompile(`^https://(www\.|old\.|new\.|np\.)?reddit\.com/r/([A-Za-z0-9_]{2,21})(/.*)?$`)
	redditIDsPattern = regexp.MustCompile(`/comments/([a-z0-9]+)(?:/[^/]*/([a-z0-9]+))?`)
	redditSubPattern = regexp.MustCompile(`^[A-Za-z0-9_]{2,21}$`)
)

// redditTarget reads a Reddit URL: its subreddit and whether it is the
// subreddit, a thread or a comment.
func redditTarget(raw string) (sub, kind string, ok bool) {
	raw = strings.TrimSpace(raw)
	m := redditURLPattern.FindStringSubmatch(raw)
	if m == nil {
		return "", "", false
	}
	sub = m[2]
	ids := redditIDsPattern.FindStringSubmatch(raw)
	switch {
	case ids == nil:
		return sub, "subreddit", true
	case ids[2] != "":
		return sub, "comment", true
	}
	return sub, "thread", true
}

// redditFullname is the Reddit id of the item a permalink points at: t1_
// for a comment, t3_ for a post.
func redditFullname(permalink string) string {
	ids := redditIDsPattern.FindStringSubmatch(permalink)
	switch {
	case ids == nil:
		return ""
	case ids[2] != "":
		return "t1_" + ids[2]
	}
	return "t3_" + ids[1]
}

// ---- Settings and budget ----

type redditSettingsView struct {
	BrandID                   string   `json:"brand_id"`
	IntegrationID             *string  `json:"integration_id"`
	EngainProjectID           string   `json:"engain_project_id"`
	MonthlyCreditLimit        *float64 `json:"monthly_credit_limit"`
	DailyCreditLimit          *float64 `json:"daily_credit_limit"`
	MaxActionsPerSubredditDay int      `json:"max_actions_per_subreddit_day"`
	MaxUpvotesPerItem         int      `json:"max_upvotes_per_item"`
	RequireApproval           bool     `json:"require_approval"`
	CreditPriceCents          float64  `json:"credit_price_cents"`
	Currency                  string   `json:"currency"`
	Saved                     bool     `json:"saved"`
}

func defaultRedditSettings(brand string) redditSettingsView {
	return redditSettingsView{BrandID: brand, MaxActionsPerSubredditDay: 2, MaxUpvotesPerItem: 50, Currency: "USD"}
}

// redditSettings loads a brand's settings, or the defaults. With lock it
// holds the row for the transaction, creating it first, so two spends are
// checked one after the other.
func redditSettings(ctx context.Context, q pgx.Tx, ws, brand string, lock bool) (redditSettingsView, error) {
	if lock {
		if _, err := q.Exec(ctx, `INSERT INTO reddit_campaign_settings (workspace_id, brand_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, ws, brand); err != nil {
			return redditSettingsView{}, err
		}
	}
	v := defaultRedditSettings(brand)
	sql := `SELECT integration_id::text, engain_project_id, monthly_credit_limit::float8, daily_credit_limit::float8, max_actions_per_subreddit_day,
		max_upvotes_per_item, require_approval, credit_price_cents::float8, currency FROM reddit_campaign_settings WHERE workspace_id = $1 AND brand_id = $2`
	if lock {
		sql += ` FOR UPDATE`
	}
	err := q.QueryRow(ctx, sql, ws, brand).Scan(&v.IntegrationID, &v.EngainProjectID, &v.MonthlyCreditLimit, &v.DailyCreditLimit,
		&v.MaxActionsPerSubredditDay, &v.MaxUpvotesPerItem, &v.RequireApproval, &v.CreditPriceCents, &v.Currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, nil
	}
	v.Saved = err == nil
	return v, err
}

type redditBudgetView struct {
	BrandID            string   `json:"brand_id"`
	MonthCredits       float64  `json:"month_credits"`
	DayCredits         float64  `json:"day_credits"`
	MonthlyCreditLimit *float64 `json:"monthly_credit_limit"`
	DailyCreditLimit   *float64 `json:"daily_credit_limit"`
	MonthRemaining     *float64 `json:"month_remaining"`
	DayRemaining       *float64 `json:"day_remaining"`
	// Money spent this month at the brand's credit price.
	MonthCostCents float64 `json:"month_cost_cents"`
	Currency       string  `json:"currency"`
	// What Engain says the workspace has left, across every brand.
	EngainCredits *float64 `json:"engain_credits"`
}

// redditSpentSQL sums what a brand's sent and approved actions cost since
// $3: Engain's figure when known, else the estimate.
const redditSpentSQL = `SELECT coalesce(sum(coalesce(credits, estimated_credits)), 0)::float8 FROM reddit_actions
	WHERE workspace_id = $1 AND brand_id = $2 AND approved_at >= $3 AND status <> 'cancelled' AND NOT (status = 'failed' AND sent_at IS NULL)`

func redditBudget(ctx context.Context, q pgx.Tx, ws string, set redditSettingsView) (redditBudgetView, error) {
	now := time.Now().UTC()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	b := redditBudgetView{BrandID: set.BrandID, MonthlyCreditLimit: set.MonthlyCreditLimit, DailyCreditLimit: set.DailyCreditLimit, Currency: set.Currency}
	if err := q.QueryRow(ctx, redditSpentSQL, ws, set.BrandID, month).Scan(&b.MonthCredits); err != nil {
		return b, err
	}
	if err := q.QueryRow(ctx, redditSpentSQL, ws, set.BrandID, day).Scan(&b.DayCredits); err != nil {
		return b, err
	}
	if l := set.MonthlyCreditLimit; l != nil {
		r := max(*l-b.MonthCredits, 0)
		b.MonthRemaining = &r
	}
	if l := set.DailyCreditLimit; l != nil {
		r := max(*l-b.DayCredits, 0)
		b.DayRemaining = &r
	}
	b.MonthCostCents = b.MonthCredits * set.CreditPriceCents
	return b, nil
}

// redditBrand resolves a brand id or handle the caller may see.
func (s *Server) redditBrand(w http.ResponseWriter, r *http.Request, a *access, ref string) (string, bool) {
	if strings.TrimSpace(ref) == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "brand is required: a brand's id or handle")
		return "", false
	}
	id, err := s.brandIDFor(r.Context(), a.WorkspaceID, ref)
	if err == nil && !a.sees(&id) {
		err = errNotFound
	}
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "brand not found")
		return "", false
	}
	if err != nil {
		s.internal(w, r, err)
		return "", false
	}
	return id, true
}

// visibleBrands is the brands a list covers: the ?brand= one, or every
// brand the caller sees.
func (s *Server) redditBrands(ctx context.Context, a *access, sel brandSel) ([]string, error) {
	if sel.id != "" && sel.id != "none" {
		return []string{sel.id}, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text FROM brands WHERE workspace_id = $1 ORDER BY created_at`, a.WorkspaceID)
	if err != nil {
		return nil, err
	}
	all, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, id := range all {
		if a.sees(&id) {
			out = append(out, id)
		}
	}
	return out, nil
}

func (s *Server) listRedditSettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	brands, err := s.redditBrands(r.Context(), a, sel)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := []map[string]any{}
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		for _, b := range brands {
			set, err := redditSettings(r.Context(), tx, a.WorkspaceID, b, false)
			if err != nil {
				return err
			}
			bud, err := redditBudget(r.Context(), tx, a.WorkspaceID, set)
			if err != nil {
				return err
			}
			out = append(out, map[string]any{"settings": set, "budget": bud})
		}
		return nil
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// putRedditSettings saves a brand's connection, project and limits. Only a
// person changes limits: an agent never raises its own budget.
func (s *Server) putRedditSettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.redditBrand(w, r, a, r.PathValue("brand"))
	if !ok {
		return
	}
	if !a.writes(&brand) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	var req struct {
		IntegrationID             *string  `json:"integration_id"`
		EngainProjectID           *string  `json:"engain_project_id"`
		MonthlyCreditLimit        *float64 `json:"monthly_credit_limit"`
		DailyCreditLimit          *float64 `json:"daily_credit_limit"`
		NoMonthlyLimit            bool     `json:"no_monthly_limit"`
		NoDailyLimit              bool     `json:"no_daily_limit"`
		MaxActionsPerSubredditDay *int     `json:"max_actions_per_subreddit_day"`
		MaxUpvotesPerItem         *int     `json:"max_upvotes_per_item"`
		RequireApproval           *bool    `json:"require_approval"`
		CreditPriceCents          *float64 `json:"credit_price_cents"`
		Currency                  *string  `json:"currency"`
	}
	if !decode(w, r, &req) {
		return
	}
	limits := req.MonthlyCreditLimit != nil || req.DailyCreditLimit != nil || req.NoMonthlyLimit || req.NoDailyLimit ||
		req.MaxActionsPerSubredditDay != nil || req.MaxUpvotesPerItem != nil || req.RequireApproval != nil
	if limits && a.ActorType != "human" {
		writeError(w, r, http.StatusForbidden, "forbidden", "only a person changes budgets, limits and approval, in Outflare under Reddit")
		return
	}
	bad := func(param, msg string) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", param+": "+msg)
	}
	if v := req.MonthlyCreditLimit; v != nil && (*v < 0 || *v > 1e9) {
		bad("monthly_credit_limit", "0 or more credits")
		return
	}
	if v := req.DailyCreditLimit; v != nil && (*v < 0 || *v > 1e9) {
		bad("daily_credit_limit", "0 or more credits")
		return
	}
	if v := req.MaxActionsPerSubredditDay; v != nil && (*v < 1 || *v > 100) {
		bad("max_actions_per_subreddit_day", "1 to 100")
		return
	}
	if v := req.MaxUpvotesPerItem; v != nil && (*v < 0 || *v > 2000) {
		bad("max_upvotes_per_item", "0 to 2000")
		return
	}
	if v := req.CreditPriceCents; v != nil && (*v < 0 || *v > 1e6) {
		bad("credit_price_cents", "0 or more")
		return
	}
	if v := req.Currency; v != nil {
		*v = strings.ToUpper(strings.TrimSpace(*v))
		if len(*v) != 3 {
			bad("currency", "a three-letter code such as USD")
			return
		}
	}
	if v := req.IntegrationID; v != nil && *v != "" {
		if !validUUID(*v) {
			bad("integration_id", "an Engain connection's id")
			return
		}
		var connector string
		err := s.pool.QueryRow(r.Context(), `SELECT connector FROM integrations WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, *v).Scan(&connector)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && connector != engain.ID) {
			bad("integration_id", "not an Engain connection in this workspace")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
	}
	ctx := r.Context()
	var out redditSettingsView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := redditSettings(ctx, tx, a.WorkspaceID, brand, true); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE reddit_campaign_settings SET
				integration_id = CASE WHEN $3::text IS NULL THEN integration_id ELSE NULLIF($3, '')::uuid END,
				engain_project_id = coalesce($4, engain_project_id),
				monthly_credit_limit = CASE WHEN $12 THEN NULL ELSE coalesce($5, monthly_credit_limit) END,
				daily_credit_limit = CASE WHEN $13 THEN NULL ELSE coalesce($6, daily_credit_limit) END,
				max_actions_per_subreddit_day = coalesce($7, max_actions_per_subreddit_day),
				max_upvotes_per_item = coalesce($8, max_upvotes_per_item),
				require_approval = coalesce($9, require_approval),
				credit_price_cents = coalesce($10, credit_price_cents),
				currency = coalesce($11, currency), updated_at = now()
			WHERE workspace_id = $1 AND brand_id = $2`,
			a.WorkspaceID, brand, req.IntegrationID, req.EngainProjectID, req.MonthlyCreditLimit, req.DailyCreditLimit, req.MaxActionsPerSubredditDay,
			req.MaxUpvotesPerItem, req.RequireApproval, req.CreditPriceCents, req.Currency, req.NoMonthlyLimit, req.NoDailyLimit)
		if err != nil {
			return err
		}
		if out, err = redditSettings(ctx, tx, a.WorkspaceID, brand, false); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "reddit_settings.updated", "brand", brand,
			map[string]any{"limits_changed": limits, "require_approval": out.RequireApproval})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getRedditBudget(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.redditBrand(w, r, a, r.URL.Query().Get("brand"))
	if !ok {
		return
	}
	ctx := r.Context()
	var set redditSettingsView
	var bud redditBudgetView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if set, err = redditSettings(ctx, tx, a.WorkspaceID, brand, false); err != nil {
			return err
		}
		bud, err = redditBudget(ctx, tx, a.WorkspaceID, set)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if o, err := s.openEngain(ctx, a.WorkspaceID, brand, set); err == nil {
		if b, err := (engain.Engain{}).Balance(ctx, o.Client, o.Creds); err == nil {
			bud.EngainCredits = b.Credits
		}
	}
	writeJSON(w, http.StatusOK, bud)
}

// openEngain opens the brand's Engain connection: the one its settings
// name, or any connected one the brand can use.
func (s *Server) openEngain(ctx context.Context, ws, brand string, set redditSettingsView) (*connectors.Opened, error) {
	if s.integrations == nil {
		return nil, connectors.ErrNoCapable
	}
	if set.IntegrationID != nil {
		return s.integrations.Open(ctx, s.pool, ws, *set.IntegrationID)
	}
	return s.integrations.OpenCapable(ctx, s.pool, ws, brand, connectors.CapRedditCampaigns)
}

// engainProject is the Engain project for the brand: its setting, or the
// account's only project.
func (s *Server) engainProject(ctx context.Context, o *connectors.Opened, set redditSettingsView) (string, error) {
	if set.EngainProjectID != "" {
		return set.EngainProjectID, nil
	}
	ps, err := (engain.Engain{}).Projects(ctx, o.Client, o.Creds)
	if err != nil {
		return "", err
	}
	if len(ps) == 1 {
		return ps[0].ID, nil
	}
	return "", connectors.Errf(connectors.ErrInvalid, "the Engain account has %d projects; choose one for this brand in PUT .../reddit/settings/{brand} (engain_project_id)", len(ps))
}

// ---- Experiments ----

type redditExperimentView struct {
	ID         string     `json:"id"`
	BrandID    string     `json:"brand_id"`
	Name       string     `json:"name"`
	Hypothesis string     `json:"hypothesis"`
	Strategy   string     `json:"strategy"`
	Status     string     `json:"status"`
	Conclusion string     `json:"conclusion"`
	MissionID  *string    `json:"mission_id"`
	CreatedBy  *string    `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	ClosedAt   *time.Time `json:"closed_at"`
	Actions    int        `json:"actions"`
}

const redditExperimentSelect = `SELECT e.id::text, e.brand_id::text, e.name, e.hypothesis, e.strategy, e.status, e.conclusion, e.mission_id::text,
	e.created_by::text, e.created_at, e.closed_at, (SELECT count(*) FROM reddit_actions x WHERE x.experiment_id = e.id)::int
	FROM reddit_experiments e WHERE e.workspace_id = $1`

func scanRedditExperiment(row pgx.Row) (redditExperimentView, error) {
	var v redditExperimentView
	err := row.Scan(&v.ID, &v.BrandID, &v.Name, &v.Hypothesis, &v.Strategy, &v.Status, &v.Conclusion, &v.MissionID, &v.CreatedBy, &v.CreatedAt, &v.ClosedAt, &v.Actions)
	return v, err
}

func (s *Server) listRedditExperiments(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && status != "open" && status != "closed" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is open or closed")
		return
	}
	limit, ok := pageLimit(w, r, 50, 200)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, status, limit}
	rows, err := s.pool.Query(r.Context(), redditExperimentSelect+` AND ($2 = '' OR e.status = $2)`+sel.sql("e.brand_id", &args)+
		` ORDER BY e.created_at DESC LIMIT $3`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (redditExperimentView, error) { return scanRedditExperiment(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := []redditExperimentView{}
	for _, v := range list {
		if a.sees(&v.BrandID) {
			out = append(out, v)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (s *Server) findRedditExperiment(w http.ResponseWriter, r *http.Request, a *access, id string) (*redditExperimentView, bool) {
	if validUUID(id) {
		v, err := scanRedditExperiment(s.pool.QueryRow(r.Context(), redditExperimentSelect+` AND e.id = $2`, a.WorkspaceID, id))
		if err == nil && a.sees(&v.BrandID) {
			return &v, true
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return nil, false
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "experiment not found")
	return nil, false
}

func (s *Server) getRedditExperiment(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if v, ok := s.findRedditExperiment(w, r, a, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

func (s *Server) createRedditExperiment(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Brand      string `json:"brand"`
		Name       string `json:"name"`
		Hypothesis string `json:"hypothesis"`
		Strategy   string `json:"strategy"`
	}
	if !decode(w, r, &req) {
		return
	}
	brand, ok := s.redditBrand(w, r, a, req.Brand)
	if !ok {
		return
	}
	if !a.writes(&brand) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 200 || len(req.Hypothesis) > 2000 || len(req.Strategy) > 100 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name (1 to 200 characters) is required; hypothesis up to 2000, strategy up to 100")
		return
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var createdBy, mission *string
		if a.ActorID != "" {
			createdBy = &a.ActorID
		}
		if m := missions.ID(ctx); m != "" {
			mission = &m
		}
		if err := tx.QueryRow(ctx, `INSERT INTO reddit_experiments (workspace_id, brand_id, name, hypothesis, strategy, created_by, mission_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id::text`, a.WorkspaceID, brand, req.Name, strings.TrimSpace(req.Hypothesis),
			strings.TrimSpace(req.Strategy), createdBy, mission).Scan(&id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "reddit_experiment.created", "reddit_experiment", id, map[string]any{"brand_id": brand})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findRedditExperiment(w, r, a, id); ok {
		writeJSON(w, http.StatusCreated, v)
	}
}

// updateRedditExperiment edits an experiment, or closes it with its
// conclusion (status closed).
func (s *Server) updateRedditExperiment(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findRedditExperiment(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	if !a.writes(&cur.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	var req struct {
		Name       *string `json:"name"`
		Hypothesis *string `json:"hypothesis"`
		Strategy   *string `json:"strategy"`
		Status     *string `json:"status"`
		Conclusion *string `json:"conclusion"`
	}
	if !decode(w, r, &req) {
		return
	}
	if (req.Name != nil && (strings.TrimSpace(*req.Name) == "" || len(*req.Name) > 200)) || (req.Hypothesis != nil && len(*req.Hypothesis) > 2000) ||
		(req.Strategy != nil && len(*req.Strategy) > 100) || (req.Conclusion != nil && len(*req.Conclusion) > 5000) ||
		(req.Status != nil && *req.Status != "open" && *req.Status != "closed") {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name 1 to 200 characters, hypothesis up to 2000, strategy up to 100, conclusion up to 5000, status open or closed")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE reddit_experiments SET name = coalesce($3, name), hypothesis = coalesce($4, hypothesis), strategy = coalesce($5, strategy),
				conclusion = coalesce($7, conclusion), status = coalesce($6, status),
				closed_at = CASE WHEN $6 = 'closed' AND status <> 'closed' THEN now() WHEN $6 = 'open' THEN NULL ELSE closed_at END, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, cur.ID, req.Name, req.Hypothesis, req.Strategy, req.Status, req.Conclusion)
		if err != nil {
			return err
		}
		action := "reddit_experiment.updated"
		if req.Status != nil && *req.Status == "closed" && cur.Status != "closed" {
			action = "reddit_experiment.closed"
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "reddit_experiment", cur.ID, map[string]any{"brand_id": cur.BrandID})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findRedditExperiment(w, r, a, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// ---- Actions ----

type redditChild struct {
	ID           string  `json:"id"`
	ParentID     *string `json:"parent_id"`
	Type         string  `json:"type"`
	Account      int     `json:"account"`
	Content      string  `json:"content"`
	DelayMinutes int     `json:"delay_minutes"`
}

type redditEventView struct {
	Kind       string          `json:"kind"`
	FromStatus string          `json:"from_status"`
	ToStatus   string          `json:"to_status"`
	Detail     json.RawMessage `json:"detail"`
	At         time.Time       `json:"at"`
}

type redditSnapshotView struct {
	At              time.Time `json:"at"`
	State           string    `json:"state"`
	Score           *int      `json:"score"`
	ReplyCount      *int      `json:"reply_count"`
	RemovalCategory string    `json:"removal_category"`
	Locked          bool      `json:"locked"`
	Archived        bool      `json:"archived"`
}

type redditActionView struct {
	ID               string          `json:"id"`
	BrandID          string          `json:"brand_id"`
	ExperimentID     *string         `json:"experiment_id"`
	Kind             string          `json:"kind"`
	Strategy         string          `json:"strategy"`
	TargetURL        string          `json:"target_url"`
	TargetKind       string          `json:"target_kind"`
	Subreddit        string          `json:"subreddit"`
	Title            string          `json:"title"`
	Content          string          `json:"content"`
	Children         json.RawMessage `json:"children"`
	LinkURL          string          `json:"link_url"`
	LinkType         string          `json:"link_type"`
	BrandMentioned   bool            `json:"brand_mentioned"`
	Competitors      []string        `json:"competitors"`
	Tone             string          `json:"tone"`
	AccountTier      string          `json:"account_tier"`
	UpvotesGoal      int             `json:"upvotes_goal"`
	UpvotesPerDay    int             `json:"upvotes_per_day"`
	UpvotesStart     string          `json:"upvotes_start"`
	ParentActionID   *string         `json:"parent_action_id"`
	ScheduledAt      *time.Time      `json:"scheduled_at"`
	Notes            string          `json:"notes"`
	Status           string          `json:"status"`
	StatusReason     *string         `json:"status_reason"`
	EstimatedCredits float64         `json:"estimated_credits"`
	Credits          *float64        `json:"credits"`
	Preflight        json.RawMessage `json:"preflight"`
	EngainIDs        []string        `json:"engain_ids"`
	EngainStatus     string          `json:"engain_status"`
	RedditFullname   string          `json:"reddit_fullname"`
	Permalink        string          `json:"permalink"`
	Author           string          `json:"author"`
	Score            *int            `json:"score"`
	PeakScore        *int            `json:"peak_score"`
	ReplyCount       *int            `json:"reply_count"`
	UpvotesDelivered *int            `json:"upvotes_delivered"`
	RemovalCategory  string          `json:"removal_category"`
	SentAt           *time.Time      `json:"sent_at"`
	LiveAt           *time.Time      `json:"live_at"`
	EndedAt          *time.Time      `json:"ended_at"`
	// Days the action has been, or was, live.
	LiveDays      *float64   `json:"live_days"`
	LastCheckedAt *time.Time `json:"last_checked_at"`
	NextCheckAt   *time.Time `json:"next_check_at"`
	CheckError    *string    `json:"check_error"`
	MissionID     *string    `json:"mission_id"`
	CreatedBy     *string    `json:"created_by"`
	ApprovedBy    *string    `json:"approved_by"`
	ApprovedAt    *time.Time `json:"approved_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	// The detail view only.
	EngainRaw json.RawMessage      `json:"engain_raw,omitempty"`
	Events    []redditEventView    `json:"events,omitempty"`
	Snapshots []redditSnapshotView `json:"snapshots,omitempty"`
}

const redditActionSelect = `SELECT a.id::text, a.brand_id::text, a.experiment_id::text, a.kind, a.strategy, a.target_url, a.target_kind, a.subreddit, a.title,
	a.content, a.children, a.link_url, a.link_type, a.brand_mentioned, a.competitors, a.tone, a.account_tier, a.upvotes_goal, a.upvotes_per_day,
	a.upvotes_start, a.parent_action_id::text, a.scheduled_at, a.notes, a.status, a.status_reason, a.estimated_credits::float8, a.credits::float8,
	a.preflight, a.engain_ids, a.engain_status, a.reddit_fullname, a.permalink, a.author, a.score, a.peak_score, a.reply_count, a.upvotes_delivered,
	a.removal_category, a.sent_at, a.live_at, a.ended_at, a.last_checked_at, a.next_check_at, a.check_error, a.mission_id::text, a.created_by::text,
	a.approved_by::text, a.approved_at, a.created_at, a.updated_at
	FROM reddit_actions a WHERE a.workspace_id = $1`

func scanRedditAction(row pgx.Row) (redditActionView, error) {
	var v redditActionView
	err := row.Scan(&v.ID, &v.BrandID, &v.ExperimentID, &v.Kind, &v.Strategy, &v.TargetURL, &v.TargetKind, &v.Subreddit, &v.Title,
		&v.Content, &v.Children, &v.LinkURL, &v.LinkType, &v.BrandMentioned, &v.Competitors, &v.Tone, &v.AccountTier, &v.UpvotesGoal, &v.UpvotesPerDay,
		&v.UpvotesStart, &v.ParentActionID, &v.ScheduledAt, &v.Notes, &v.Status, &v.StatusReason, &v.EstimatedCredits, &v.Credits,
		&v.Preflight, &v.EngainIDs, &v.EngainStatus, &v.RedditFullname, &v.Permalink, &v.Author, &v.Score, &v.PeakScore, &v.ReplyCount, &v.UpvotesDelivered,
		&v.RemovalCategory, &v.SentAt, &v.LiveAt, &v.EndedAt, &v.LastCheckedAt, &v.NextCheckAt, &v.CheckError, &v.MissionID, &v.CreatedBy,
		&v.ApprovedBy, &v.ApprovedAt, &v.CreatedAt, &v.UpdatedAt)
	if v.LiveAt != nil {
		end := time.Now()
		if v.EndedAt != nil {
			end = *v.EndedAt
		}
		d := end.Sub(*v.LiveAt).Hours() / 24
		d = float64(int(d*10)) / 10
		v.LiveDays = &d
	}
	if v.Competitors == nil {
		v.Competitors = []string{}
	}
	if v.EngainIDs == nil {
		v.EngainIDs = []string{}
	}
	return v, err
}

func (s *Server) listRedditActions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	status, kind, sub, exp := q.Get("status"), q.Get("kind"), strings.TrimPrefix(q.Get("subreddit"), "r/"), q.Get("experiment_id")
	for _, st := range strings.Split(status, ",") {
		if st != "" && !slices.Contains(redditStatuses, st) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "status is a comma-separated list of "+strings.Join(redditStatuses, ", "))
			return
		}
	}
	if kind != "" && !slices.Contains(redditKinds, kind) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is "+strings.Join(redditKinds, ", "))
		return
	}
	if exp != "" && !validUUID(exp) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "experiment_id is an id")
		return
	}
	before, ok := beforeTime(w, r)
	if !ok {
		return
	}
	limit, ok := pageLimit(w, r, 50, 200)
	if !ok {
		return
	}
	statuses := []string{}
	if status != "" {
		statuses = strings.Split(status, ",")
	}
	args := []any{a.WorkspaceID, statuses, kind, sub, exp, before, limit + 1}
	rows, err := s.pool.Query(r.Context(), redditActionSelect+`
		AND (cardinality($2::text[]) = 0 OR a.status = ANY($2)) AND ($3 = '' OR a.kind = $3) AND ($4 = '' OR lower(a.subreddit) = lower($4))
		AND ($5 = '' OR a.experiment_id = NULLIF($5, '')::uuid) AND ($6::timestamptz IS NULL OR a.created_at < $6)`+sel.sql("a.brand_id", &args)+
		` ORDER BY a.created_at DESC LIMIT $7`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (redditActionView, error) { return scanRedditAction(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := []redditActionView{}
	for _, v := range list {
		if a.sees(&v.BrandID) {
			out = append(out, v)
		}
	}
	var next *string
	if len(out) > limit {
		out = out[:limit]
		n := out[limit-1].CreatedAt.Format(time.RFC3339Nano)
		next = &n
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "next_before": next})
}

func (s *Server) findRedditAction(w http.ResponseWriter, r *http.Request, a *access, id string) (*redditActionView, bool) {
	if validUUID(id) {
		v, err := scanRedditAction(s.pool.QueryRow(r.Context(), redditActionSelect+` AND a.id = $2`, a.WorkspaceID, id))
		if err == nil && a.sees(&v.BrandID) {
			return &v, true
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return nil, false
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "action not found")
	return nil, false
}

// getRedditAction answers one action with its events, snapshots and
// Engain's last answer.
func (s *Server) getRedditAction(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.findRedditAction(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	ctx := r.Context()
	if err := s.pool.QueryRow(ctx, `SELECT engain_raw FROM reddit_actions WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, v.ID).Scan(&v.EngainRaw); err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err := s.pool.Query(ctx, `SELECT kind, from_status, to_status, detail, at FROM reddit_action_events WHERE workspace_id = $1 AND action_id = $2 ORDER BY at, id`,
		a.WorkspaceID, v.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v.Events, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (redditEventView, error) {
		var e redditEventView
		err := row.Scan(&e.Kind, &e.FromStatus, &e.ToStatus, &e.Detail, &e.At)
		return e, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err = s.pool.Query(ctx, `SELECT at, state, score, reply_count, removal_category, locked, archived FROM reddit_action_snapshots
		WHERE workspace_id = $1 AND action_id = $2 ORDER BY at DESC LIMIT 500`, a.WorkspaceID, v.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v.Snapshots, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (redditSnapshotView, error) {
		var x redditSnapshotView
		err := row.Scan(&x.At, &x.State, &x.Score, &x.ReplyCount, &x.RemovalCategory, &x.Locked, &x.Archived)
		return x, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v.Events == nil {
		v.Events = []redditEventView{}
	}
	if v.Snapshots == nil {
		v.Snapshots = []redditSnapshotView{}
	}
	writeJSON(w, http.StatusOK, v)
}

type redditActionRequest struct {
	Brand          string          `json:"brand"`
	ExperimentID   *string         `json:"experiment_id"`
	Kind           string          `json:"kind"`
	Strategy       string          `json:"strategy"`
	TargetURL      string          `json:"target_url"`
	Title          string          `json:"title"`
	Content        string          `json:"content"`
	Children       []redditChild   `json:"children"`
	LinkURL        string          `json:"link_url"`
	LinkType       string          `json:"link_type"`
	BrandMentioned bool            `json:"brand_mentioned"`
	Competitors    []string        `json:"competitors"`
	Tone           string          `json:"tone"`
	AccountTier    string          `json:"account_tier"`
	UpvotesGoal    int             `json:"upvotes_goal"`
	UpvotesPerDay  int             `json:"upvotes_per_day"`
	UpvotesStart   string          `json:"upvotes_start"`
	ParentActionID *string         `json:"parent_action_id"`
	ScheduledAt    *time.Time      `json:"scheduled_at"`
	Notes          string          `json:"notes"`
	Preflight      json.RawMessage `json:"preflight"`
	// Submit sends it at once when the brand needs no approval (default
	// true); false keeps it a draft.
	Submit *bool `json:"submit"`
}

// redditEstimate is what an action is expected to cost in credits.
func redditEstimate(kind string, children, upvotes int) float64 {
	c := 0.0
	switch kind {
	case "comment", "reply":
		c = redditCreditsComment
	case "post":
		c = redditCreditsPost
	case "thread":
		c = redditCreditsPost + float64(children)*redditCreditsComment
	}
	return c + float64(upvotes)*redditCreditsVote
}

// validate checks an action request and fills what it derives: the
// subreddit and target kind. It returns the field and why when it's wrong.
func (q *redditActionRequest) validate() (subreddit, targetKind, param, msg string) {
	if !slices.Contains(redditKinds, q.Kind) {
		return "", "", "kind", strings.Join(redditKinds, ", ")
	}
	q.Content = strings.TrimSpace(q.Content)
	q.Title = strings.TrimSpace(q.Title)
	if q.Kind == "upvotes" && q.UpvotesPerDay == 0 {
		q.UpvotesPerDay = 5
	}
	if q.LinkType == "" {
		q.LinkType = "none"
		if q.LinkURL != "" {
			q.LinkType = "other"
		}
	}
	switch {
	case !slices.Contains(redditLinkTypes, q.LinkType):
		return "", "", "link_type", strings.Join(redditLinkTypes, ", ")
	case !slices.Contains(redditTiers, q.AccountTier):
		return "", "", "account_tier", "shared or dedicated"
	case !slices.Contains(redditStarts, q.UpvotesStart):
		return "", "", "upvotes_start", strings.Join(redditStarts[1:], ", ")
	case q.UpvotesGoal < 0 || q.UpvotesGoal > 2000:
		return "", "", "upvotes_goal", "0 to 2000"
	case q.UpvotesPerDay < 0 || q.UpvotesPerDay > 10:
		return "", "", "upvotes_per_day", "0 to 10"
	case q.UpvotesGoal > 0 && q.UpvotesPerDay == 0:
		return "", "", "upvotes_per_day", "1 to 10 when there is an upvotes_goal"
	case len(q.Content) > 40000 || len(q.Title) > 300 || len(q.Notes) > 5000 || len(q.Strategy) > 100 || len(q.Tone) > 50:
		return "", "", "content", "content up to 40000 characters, title 300, notes 5000, strategy 100, tone 50"
	case len(q.Competitors) > 20:
		return "", "", "competitors", "at most 20"
	case q.LinkURL != "" && !strings.HasPrefix(q.LinkURL, "https://") && !strings.HasPrefix(q.LinkURL, "http://"):
		return "", "", "link_url", "a web address"
	}
	if strings.Contains(strings.ToLower(q.LinkURL), "utm_") {
		return "", "", "link_url", "no tracking parameters: links stay clean so they read as organic"
	}
	if q.Kind == "upvotes" {
		if q.UpvotesGoal < 1 {
			return "", "", "upvotes_goal", "1 to 2000 upvotes"
		}
		if q.ParentActionID != nil && q.TargetURL == "" {
			return "", "", "", "" // the parent's permalink, filled later
		}
	}
	sub, tk, ok := redditTarget(q.TargetURL)
	if !ok {
		return "", "", "target_url", "a reddit.com address: the subreddit for a post, a thread for a comment, a comment for a reply"
	}
	switch q.Kind {
	case "comment":
		if tk != "thread" {
			return "", "", "target_url", "a comment goes on a thread: https://www.reddit.com/r/<sub>/comments/<id>/..."
		}
	case "reply":
		if tk != "comment" {
			return "", "", "target_url", "a reply goes under a comment: the comment's permalink"
		}
	case "post":
		if tk != "subreddit" {
			return "", "", "target_url", "a post goes in a subreddit: https://www.reddit.com/r/<sub>"
		}
		if q.Title == "" {
			return "", "", "title", "a post needs a title"
		}
	case "thread":
		if tk == "subreddit" && q.Title == "" {
			return "", "", "title", "a thread that starts with a post needs a title"
		}
		if len(q.Children) == 0 || len(q.Children) > 20 {
			return "", "", "children", "1 to 20 replies"
		}
		seen := map[string]bool{"root": true}
		for i, c := range q.Children {
			c.Content = strings.TrimSpace(c.Content)
			q.Children[i].Content = c.Content
			if c.Type == "" {
				q.Children[i].Type = "reply"
				if c.ParentID == nil {
					q.Children[i].Type = "comment"
				}
			}
			if c.ID == "" {
				q.Children[i].ID = "c" + strconv.Itoa(i+1)
			}
			switch {
			case c.Content == "":
				return "", "", fmt.Sprintf("children[%d].content", i), "required"
			case c.Account < 1 || c.Account > 10:
				return "", "", fmt.Sprintf("children[%d].account", i), "1 to 10: which pool account replies"
			case c.ParentID != nil && !seen[*c.ParentID]:
				return "", "", fmt.Sprintf("children[%d].parent_id", i), "an earlier child's id, or null for a direct reply"
			}
			if q.Children[i].DelayMinutes < 1 {
				q.Children[i].DelayMinutes = 30
			}
			seen[q.Children[i].ID] = true
		}
	case "upvotes":
		if tk == "subreddit" {
			return "", "", "target_url", "upvotes go on a thread or a comment"
		}
	}
	if q.Kind != "upvotes" && q.Content == "" {
		return "", "", "content", "required"
	}
	return sub, tk, "", ""
}

func (s *Server) createRedditAction(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req redditActionRequest
	if !decodeLarge(w, r, &req, 1<<20) {
		return
	}
	brand, ok := s.redditBrand(w, r, a, req.Brand)
	if !ok {
		return
	}
	if !a.writes(&brand) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	sub, tk, param, msg := req.validate()
	if param != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", param+": "+msg)
		return
	}
	ctx := r.Context()
	if req.ExperimentID != nil {
		exp, ok := s.findRedditExperiment(w, r, a, *req.ExperimentID)
		if !ok {
			return
		}
		if exp.BrandID != brand || exp.Status != "open" {
			writeError(w, r, http.StatusConflict, "conflict", "the experiment must be open and the same brand's")
			return
		}
		if req.Strategy == "" {
			req.Strategy = exp.Strategy
		}
	}
	if req.ParentActionID != nil {
		p, ok := s.findRedditAction(w, r, a, *req.ParentActionID)
		if !ok {
			return
		}
		if p.BrandID != brand {
			writeError(w, r, http.StatusConflict, "conflict", "parent_action_id is another brand's action")
			return
		}
		if req.TargetURL == "" {
			if p.Permalink == "" {
				writeError(w, r, http.StatusConflict, "conflict", "the parent action is not live yet, so it has no Reddit address to act on")
				return
			}
			req.TargetURL = p.Permalink
			if sub, tk, ok = redditTarget(p.Permalink); !ok {
				writeError(w, r, http.StatusConflict, "conflict", "the parent action's address is not a Reddit address")
				return
			}
		}
	}
	upvotes := req.UpvotesGoal
	est := redditEstimate(req.Kind, len(req.Children), upvotes)
	children, _ := json.Marshal(req.Children)
	if req.Children == nil {
		children = []byte("[]")
	}
	if len(req.Preflight) == 0 || string(req.Preflight) == "null" {
		req.Preflight = nil
	}
	submit := req.Submit == nil || *req.Submit
	var id string
	var refusal *redditRefusal
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		set, err := redditSettings(ctx, tx, a.WorkspaceID, brand, true)
		if err != nil {
			return err
		}
		var createdBy, mission *string
		if a.ActorID != "" {
			createdBy = &a.ActorID
		}
		if m := missions.ID(ctx); m != "" {
			mission = &m
		}
		competitors := req.Competitors
		if competitors == nil {
			competitors = []string{}
		}
		err = tx.QueryRow(ctx, `INSERT INTO reddit_actions (workspace_id, brand_id, experiment_id, kind, strategy, target_url, target_kind, subreddit,
				title, content, children, link_url, link_type, brand_mentioned, competitors, tone, account_tier, upvotes_goal, upvotes_per_day, upvotes_start,
				parent_action_id, scheduled_at, notes, estimated_credits, preflight, mission_id, created_by, request_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, NULLIF($28, ''))
			RETURNING id::text`,
			a.WorkspaceID, brand, req.ExperimentID, req.Kind, strings.TrimSpace(req.Strategy), req.TargetURL, tk, sub, req.Title, req.Content, children,
			req.LinkURL, req.LinkType, req.BrandMentioned, competitors, strings.TrimSpace(req.Tone), req.AccountTier, req.UpvotesGoal, req.UpvotesPerDay,
			req.UpvotesStart, req.ParentActionID, req.ScheduledAt, req.Notes, est, req.Preflight, mission, createdBy, RequestID(ctx)).Scan(&id)
		if err != nil {
			return err
		}
		if err := redditEvent(ctx, tx, a.WorkspaceID, id, "created", "", "draft", nil); err != nil {
			return err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "reddit_action.created", "reddit_action", id,
			map[string]any{"brand_id": brand, "kind": req.Kind, "estimated_credits": est}); err != nil {
			return err
		}
		if !submit || (set.RequireApproval && a.ActorType != "human") || !a.has(auth.ScopeSocialPost) {
			return nil
		}
		refusal, err = s.approveRedditTx(ctx, tx, a, set, id)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if refusal != nil {
		// The action stays a draft; say why it was not sent.
		_, _ = s.pool.Exec(ctx, `UPDATE reddit_actions SET status_reason = $3, updated_at = now() WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, id, refusal.message)
	}
	v, ok := s.findRedditAction(w, r, a, id)
	if !ok {
		return
	}
	if refusal != nil {
		writeError(w, r, http.StatusConflict, refusal.code, refusal.message+" The action was saved as a draft ("+id+").")
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// redditRefusal is a limit an action would pass.
type redditRefusal struct{ code, message string }

// approveRedditTx checks the brand's limits for a draft and, when they
// allow it, marks it approved and queues it. It runs in the transaction
// that holds the brand's settings row, so spends are checked in turn.
func (s *Server) approveRedditTx(ctx context.Context, tx pgx.Tx, a *access, set redditSettingsView, id string) (*redditRefusal, error) {
	var kind, sub, target string
	var est float64
	var goal int
	var scheduled *time.Time
	err := tx.QueryRow(ctx, `SELECT kind, subreddit, target_url, estimated_credits::float8, upvotes_goal, scheduled_at FROM reddit_actions
		WHERE workspace_id = $1 AND id = $2 AND status = 'draft' FOR UPDATE`, a.WorkspaceID, id).Scan(&kind, &sub, &target, &est, &goal, &scheduled)
	if errors.Is(err, pgx.ErrNoRows) {
		return &redditRefusal{"conflict", "Only a draft can be approved."}, nil
	}
	if err != nil {
		return nil, err
	}
	bud, err := redditBudget(ctx, tx, a.WorkspaceID, set)
	if err != nil {
		return nil, err
	}
	if bud.MonthRemaining != nil && est > *bud.MonthRemaining {
		return &redditRefusal{"reddit_budget_reached", fmt.Sprintf("This needs about %.1f credits and the brand has %.1f left this month.", est, *bud.MonthRemaining)}, nil
	}
	if bud.DayRemaining != nil && est > *bud.DayRemaining {
		return &redditRefusal{"reddit_budget_reached", fmt.Sprintf("This needs about %.1f credits and the brand has %.1f left today (UTC).", est, *bud.DayRemaining)}, nil
	}
	if kind != "upvotes" && sub != "" {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM reddit_actions WHERE workspace_id = $1 AND brand_id = $2 AND lower(subreddit) = lower($3)
				AND kind <> 'upvotes' AND approved_at >= date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AND status <> 'cancelled'
				AND NOT (status = 'failed' AND sent_at IS NULL)`,
			a.WorkspaceID, set.BrandID, sub).Scan(&n); err != nil {
			return nil, err
		}
		if n >= set.MaxActionsPerSubredditDay {
			return &redditRefusal{"reddit_limit_reached", fmt.Sprintf("The brand already has %d actions in r/%s today; the limit is %d a day.", n, sub, set.MaxActionsPerSubredditDay)}, nil
		}
	}
	if goal > 0 {
		var votes int
		if err := tx.QueryRow(ctx, `SELECT coalesce(sum(upvotes_goal), 0)::int FROM reddit_actions WHERE workspace_id = $1 AND brand_id = $2 AND target_url = $3
				AND kind = 'upvotes' AND approved_at IS NOT NULL AND status NOT IN ('cancelled', 'failed')`, a.WorkspaceID, set.BrandID, target).Scan(&votes); err != nil {
			return nil, err
		}
		if kind != "upvotes" {
			votes = 0
		}
		if votes+goal > set.MaxUpvotesPerItem {
			return &redditRefusal{"reddit_limit_reached", fmt.Sprintf("That would be %d upvotes on one item; the brand's limit is %d.", votes+goal, set.MaxUpvotesPerItem)}, nil
		}
	}
	var approver *string
	if a.ActorID != "" {
		approver = &a.ActorID
	}
	var approvedAt time.Time
	if err := tx.QueryRow(ctx, `UPDATE reddit_actions SET status = 'approved', status_reason = NULL, approved_by = $3, approved_at = now(), updated_at = now()
		WHERE workspace_id = $1 AND id = $2 RETURNING approved_at`, a.WorkspaceID, id, approver).Scan(&approvedAt); err != nil {
		return nil, err
	}
	if err := redditEvent(ctx, tx, a.WorkspaceID, id, "approved", "draft", "approved", nil); err != nil {
		return nil, err
	}
	if err := enqueueRedditSend(ctx, tx, a.WorkspaceID, id, approvedAt); err != nil {
		return nil, err
	}
	return nil, record(ctx, tx, &a.principal, a.WorkspaceID, "reddit_action.approved", "reddit_action", id,
		map[string]any{"brand_id": set.BrandID, "kind": kind, "estimated_credits": est})
}

func (s *Server) approveRedditAction(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findRedditAction(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	if !a.writes(&cur.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	ctx := r.Context()
	var refusal *redditRefusal
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		set, err := redditSettings(ctx, tx, a.WorkspaceID, cur.BrandID, true)
		if err != nil {
			return err
		}
		if set.RequireApproval && a.ActorType != "human" {
			refusal = &redditRefusal{"forbidden", "This brand needs a person to approve each action, in Outflare under Reddit."}
			return nil
		}
		refusal, err = s.approveRedditTx(ctx, tx, a, set, cur.ID)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if refusal != nil {
		status := http.StatusConflict
		if refusal.code == "forbidden" {
			status = http.StatusForbidden
		}
		writeError(w, r, status, refusal.code, refusal.message)
		return
	}
	if v, ok := s.findRedditAction(w, r, a, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// cancelRedditAction withdraws a draft or an approved action not yet sent.
func (s *Server) cancelRedditAction(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findRedditAction(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	if !a.writes(&cur.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var from string
		err := tx.QueryRow(ctx, `UPDATE reddit_actions x SET status = 'cancelled', updated_at = now()
			FROM (SELECT status FROM reddit_actions WHERE workspace_id = $1 AND id = $2 FOR UPDATE) old
			WHERE x.workspace_id = $1 AND x.id = $2 AND x.status IN ('draft', 'approved') RETURNING old.status`, a.WorkspaceID, cur.ID).Scan(&from)
		if errors.Is(err, pgx.ErrNoRows) {
			return errConflict
		}
		if err != nil {
			return err
		}
		if err := redditEvent(ctx, tx, a.WorkspaceID, cur.ID, "cancelled", from, "cancelled", nil); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "reddit_action.cancelled", "reddit_action", cur.ID, map[string]any{"brand_id": cur.BrandID})
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", "the action was already sent to Engain ("+cur.Status+"); it can't be withdrawn here")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findRedditAction(w, r, a, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// checkRedditActionNow queues a check of a sent action straight away.
func (s *Server) checkRedditActionNow(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findRedditAction(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	if cur.SentAt == nil {
		writeError(w, r, http.StatusConflict, "conflict", "the action has not been sent to Engain yet")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return enqueueRedditCheck(ctx, tx, a.WorkspaceID, cur.ID, "now:"+strconv.FormatInt(time.Now().Unix()/60, 10))
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

// redditEvent adds an event to an action's timeline.
func redditEvent(ctx context.Context, q pgx.Tx, ws, id, kind, from, to string, detail map[string]any) error {
	if detail == nil {
		detail = map[string]any{}
	}
	_, err := q.Exec(ctx, `INSERT INTO reddit_action_events (workspace_id, action_id, kind, from_status, to_status, detail) VALUES ($1, $2, $3, $4, $5, $6)`,
		ws, id, kind, from, to, detail)
	return err
}

// ---- Through to Engain ----

// redditEngainFor opens the brand's Engain connection, answering 409
// connector_needed when there is none.
func (s *Server) redditEngainFor(w http.ResponseWriter, r *http.Request, a *access, brand string) (*connectors.Opened, redditSettingsView, bool) {
	ctx := r.Context()
	var set redditSettingsView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		set, err = redditSettings(ctx, tx, a.WorkspaceID, brand, false)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return nil, set, false
	}
	o, err := s.openEngain(ctx, a.WorkspaceID, brand, set)
	if errors.Is(err, connectors.ErrNoCapable) {
		writeError(w, r, http.StatusConflict, "connector_needed", "connect Engain under Connectors with your own Engain API key first")
		return nil, set, false
	}
	if err != nil {
		connectorError(w, r, err)
		return nil, set, false
	}
	return o, set, true
}

// redditPreflight runs Engain's rule check on up to 25 drafts for one
// subreddit. It spends nothing.
func (s *Server) redditPreflight(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Brand     string           `json:"brand"`
		Subreddit string           `json:"subreddit"`
		Items     []map[string]any `json:"items"`
	}
	if !decodeLarge(w, r, &req, 1<<20) {
		return
	}
	brand, ok := s.redditBrand(w, r, a, req.Brand)
	if !ok {
		return
	}
	sub := strings.TrimPrefix(strings.TrimSpace(req.Subreddit), "r/")
	if !redditSubPattern.MatchString(sub) || len(req.Items) == 0 || len(req.Items) > 25 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "subreddit (a name) and 1 to 25 items, each {key, taskType: post|comment|reply, content, title?, context?}")
		return
	}
	o, set, ok := s.redditEngainFor(w, r, a, brand)
	if !ok {
		return
	}
	body := map[string]any{"subreddit": sub, "items": req.Items}
	if p, err := s.engainProject(r.Context(), o, set); err == nil {
		body["projectId"] = p
	}
	out, err := (engain.Engain{}).RuleCheck(r.Context(), o.Client, o.Creds, body)
	if err != nil {
		connectorError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, json.RawMessage(out))
}

// getRedditSubreddit answers Engain's removal rates for a subreddit beside
// the brand's own record there.
func (s *Server) getRedditSubreddit(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sub := strings.TrimPrefix(r.PathValue("name"), "r/")
	if !redditSubPattern.MatchString(sub) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is a subreddit name")
		return
	}
	brand, ok := s.redditBrand(w, r, a, r.URL.Query().Get("brand"))
	if !ok {
		return
	}
	ctx := r.Context()
	own, err := s.redditReport(ctx, a.WorkspaceID, brand, redditReportFilter{subreddit: sub})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := map[string]any{"subreddit": sub, "ours": own.Totals, "engain": nil}
	var set redditSettingsView
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		set, err = redditSettings(ctx, tx, a.WorkspaceID, brand, false)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if o, err := s.openEngain(ctx, a.WorkspaceID, brand, set); err != nil {
		out["engain_error"] = "connect Engain under Connectors to see its removal rates"
	} else if stats, err := (engain.Engain{}).SubredditStats(ctx, o.Client, o.Creds, sub); err != nil {
		out["engain_error"] = err.Error()
	} else {
		out["engain"] = json.RawMessage(stats)
	}
	writeJSON(w, http.StatusOK, out)
}

// listRedditThreads answers threads Engain found for the brand's project:
// type new (fresh threads) or seo (threads ranking on Google).
func (s *Server) listRedditThreads(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	brand, ok := s.redditBrand(w, r, a, q.Get("brand"))
	if !ok {
		return
	}
	kind := map[string]string{"": "new_opportunities", "new": "new_opportunities", "seo": "seo_opportunities"}[q.Get("type")]
	if kind == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "type is new or seo")
		return
	}
	n, _ := strconv.Atoi(q.Get("limit"))
	if n < 1 || n > 100 {
		n = 25
	}
	o, set, ok := s.redditEngainFor(w, r, a, brand)
	if !ok {
		return
	}
	project, err := s.engainProject(r.Context(), o, set)
	if err != nil {
		connectorError(w, r, err)
		return
	}
	out, err := (engain.Engain{}).Opportunities(r.Context(), o.Client, o.Creds, project, kind, q.Get("engain_cursor"), n)
	if err != nil {
		connectorError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, json.RawMessage(out))
}

// ---- Report ----

type redditReportRow struct {
	Group         string   `json:"group"`
	Actions       int      `json:"actions"`
	MentionsLive  int      `json:"mentions_live"`
	MentionsEver  int      `json:"mentions_placed"`
	LinksLive     int      `json:"links_live"`
	LinksEver     int      `json:"links_placed"`
	Removed       int      `json:"removed"`
	Pending       int      `json:"pending"`
	Credits       float64  `json:"credits"`
	CostCents     float64  `json:"cost_cents"`
	CreditsPer    *float64 `json:"credits_per_mention"`
	MedianDays    *float64 `json:"median_live_days"`
	MeanDays      *float64 `json:"mean_live_days"`
	Survived1d    *float64 `json:"survived_1d"`
	Survived7d    *float64 `json:"survived_7d"`
	Survived30d   *float64 `json:"survived_30d"`
	UpvotesBought int      `json:"upvotes_bought"`
}

type redditReportOut struct {
	BrandID  string            `json:"brand_id"`
	GroupBy  string            `json:"group_by"`
	From     *time.Time        `json:"from"`
	Totals   redditReportRow   `json:"totals"`
	Groups   []redditReportRow `json:"groups"`
	Currency string            `json:"currency"`
}

type redditReportFilter struct {
	experiment, subreddit, groupBy string
	from                           *time.Time
}

var redditGroupCols = map[string]string{
	"subreddit": "lower(a.subreddit)", "strategy": "a.strategy", "kind": "a.kind", "link_type": "a.link_type",
	"account_tier": "a.account_tier", "experiment": "coalesce(e.name, '')", "tone": "a.tone", "author": "a.author",
}

// redditReport counts mentions and links placed and live, what they cost
// and how long they lived. A mention is an action that names the brand and
// went live; a link, one with a link that went live.
func (s *Server) redditReport(ctx context.Context, ws, brand string, f redditReportFilter) (redditReportOut, error) {
	out := redditReportOut{BrandID: brand, GroupBy: f.groupBy, From: f.from, Groups: []redditReportRow{}}
	group := "''"
	if f.groupBy != "" {
		group = redditGroupCols[f.groupBy]
	}
	var price float64
	_ = s.pool.QueryRow(ctx, `SELECT credit_price_cents::float8, currency FROM reddit_campaign_settings WHERE workspace_id = $1 AND brand_id = $2`, ws, brand).
		Scan(&price, &out.Currency)
	if out.Currency == "" {
		out.Currency = "USD"
	}
	rows, err := s.pool.Query(ctx, `
		WITH x AS (
			SELECT `+group+` AS g, a.*, CASE WHEN a.live_at IS NULL THEN NULL
				ELSE extract(epoch FROM coalesce(a.ended_at, now()) - a.live_at) / 86400 END AS days,
				extract(epoch FROM now() - a.live_at) / 86400 AS age
			FROM reddit_actions a LEFT JOIN reddit_experiments e ON e.id = a.experiment_id
			WHERE a.workspace_id = $1 AND a.brand_id = $2 AND a.status NOT IN ('draft', 'cancelled')
			  AND ($3 = '' OR a.experiment_id = NULLIF($3, '')::uuid) AND ($4 = '' OR lower(a.subreddit) = lower($4))
			  AND ($5::timestamptz IS NULL OR a.created_at >= $5))
		SELECT g, count(*)::int,
			count(*) FILTER (WHERE kind <> 'upvotes' AND brand_mentioned AND status IN ('live', 'frozen'))::int,
			count(*) FILTER (WHERE kind <> 'upvotes' AND brand_mentioned AND live_at IS NOT NULL)::int,
			count(*) FILTER (WHERE kind <> 'upvotes' AND link_type <> 'none' AND status IN ('live', 'frozen'))::int,
			count(*) FILTER (WHERE kind <> 'upvotes' AND link_type <> 'none' AND live_at IS NOT NULL)::int,
			count(*) FILTER (WHERE status IN ('removed', 'hidden', 'gone'))::int,
			count(*) FILTER (WHERE status IN ('approved', 'sending', 'submitted'))::int,
			coalesce(sum(coalesce(credits, estimated_credits)) FILTER (WHERE NOT (status = 'failed' AND sent_at IS NULL)), 0)::float8,
			(percentile_cont(0.5) WITHIN GROUP (ORDER BY days) FILTER (WHERE kind <> 'upvotes'))::float8,
			(avg(days) FILTER (WHERE kind <> 'upvotes'))::float8,
			(avg(CASE WHEN days >= 1 THEN 1.0 ELSE 0.0 END) FILTER (WHERE kind <> 'upvotes' AND age >= 1))::float8,
			(avg(CASE WHEN days >= 7 THEN 1.0 ELSE 0.0 END) FILTER (WHERE kind <> 'upvotes' AND age >= 7))::float8,
			(avg(CASE WHEN days >= 30 THEN 1.0 ELSE 0.0 END) FILTER (WHERE kind <> 'upvotes' AND age >= 30))::float8,
			coalesce(sum(upvotes_goal), 0)::int
		FROM x GROUP BY ROLLUP (g) ORDER BY g NULLS FIRST`, ws, brand, f.experiment, f.subreddit, f.from)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var g *string
		var row redditReportRow
		if err := rows.Scan(&g, &row.Actions, &row.MentionsLive, &row.MentionsEver, &row.LinksLive, &row.LinksEver, &row.Removed, &row.Pending,
			&row.Credits, &row.MedianDays, &row.MeanDays, &row.Survived1d, &row.Survived7d, &row.Survived30d, &row.UpvotesBought); err != nil {
			return out, err
		}
		row.CostCents = row.Credits * price
		if row.MentionsEver > 0 {
			c := row.Credits / float64(row.MentionsEver)
			row.CreditsPer = &c
		}
		if g == nil {
			row.Group = "all"
			out.Totals = row
			continue
		}
		row.Group = *g
		if f.groupBy != "" {
			out.Groups = append(out.Groups, row)
		}
	}
	if out.Totals.Group == "" {
		out.Totals.Group = "all"
	}
	return out, rows.Err()
}

func (s *Server) getRedditReport(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	brand, ok := s.redditBrand(w, r, a, q.Get("brand"))
	if !ok {
		return
	}
	f := redditReportFilter{experiment: q.Get("experiment_id"), subreddit: strings.TrimPrefix(q.Get("subreddit"), "r/"), groupBy: q.Get("group_by")}
	if f.experiment != "" && !validUUID(f.experiment) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "experiment_id is an id")
		return
	}
	if _, ok := redditGroupCols[f.groupBy]; f.groupBy != "" && !ok {
		keys := []string{}
		for k := range redditGroupCols {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		writeError(w, r, http.StatusBadRequest, "invalid_request", "group_by is "+strings.Join(keys, ", "))
		return
	}
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			if t, err = time.Parse("2006-01-02", v); err != nil {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "from is a date (2026-10-01) or an RFC 3339 time")
				return
			}
		}
		f.from = &t
	}
	out, err := s.redditReport(r.Context(), a.WorkspaceID, brand, f)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// redditPermalink makes a Reddit address absolute.
func redditPermalink(p string) string {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "/") {
		return "https://www.reddit.com" + p
	}
	if u, err := url.Parse(p); err == nil && strings.HasSuffix(u.Host, "reddit.com") {
		u.Host = "www.reddit.com"
		u.RawQuery = ""
		return u.String()
	}
	return p
}
