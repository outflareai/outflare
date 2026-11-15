package api

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/commissions"
	"github.com/outflareai/outflare/engine/internal/revenue"
)

// The sales team of a brand: the people in the workspace's team directory
// who carry a quota, own deals or are on a commission plan, with what
// each has won and is forecast to win against their quota, rolled up to
// their managers; and what each earns under the brand's commission plans.
// Commission is worked out, shown line by line and approved here; nothing
// here pays anyone.

func (s *Server) routesRevenueTeam(mux *http.ServeMux) {
	read, manage := auth.ScopeCustomersRead, auth.ScopeCatalogManage
	base := "/v1/brands/{brand}"
	mux.HandleFunc("GET "+base+"/revenue/team", s.onBrand(read, s.getRevenueTeam))
	mux.HandleFunc("PUT "+base+"/revenue/team/quotas", s.onBrand(manage, s.setSalesQuotas))
	mux.HandleFunc("GET "+base+"/commission-plans", s.onBrand(read, s.listCommissionPlans))
	mux.HandleFunc("POST "+base+"/commission-plans", s.onBrand(manage, s.createCommissionPlan))
	mux.HandleFunc("PATCH "+base+"/commission-plans/{id}", s.onBrand(manage, s.updateCommissionPlan))
	mux.HandleFunc("DELETE "+base+"/commission-plans/{id}", s.onBrand(manage, s.deleteCommissionPlan))
	mux.HandleFunc("GET "+base+"/commissions", s.onBrand(read, s.getCommissions))
	mux.HandleFunc("PUT "+base+"/commissions/{staff}", s.onBrand(manage, s.setCommissionStatement))
}

// teamPerson is someone in the team directory, as the sales pages need them.
type teamPerson struct {
	ID        string
	Name      string
	Title     string
	ManagerID *string
	UserID    *string
	Status    string
	// Role is their role on the brand's sales desk: sdr, ae, manager or "".
	Role string
}

// ownerKey is the id records carry when this person owns them: their
// user id, or their directory id when they have no sign-in.
func (p teamPerson) ownerKey() string {
	if p.UserID != nil {
		return *p.UserID
	}
	return p.ID
}

// coOwnerShare tidies a deal's second owner: nobody when it is the owner
// again, and their percent of the deal, half when the deal doesn't say.
func coOwnerShare(owner, co string, percent *float64) (string, float64) {
	if co == "" || co == owner {
		return "", 0
	}
	share := 50.0
	if percent != nil && *percent >= 1 && *percent <= 99 {
		share = *percent
	}
	return co, share
}

// splitAmount divides a deal's amount between its owner and its second
// owner, who gets share percent of it.
func splitAmount(v int64, share float64) (owner, co int64) {
	co = int64(math.Round(float64(v) * share / 100))
	return v - co, co
}

// ownerAlias maps an owner id read from a record to the key its person is
// known by: a record given to someone before they had a sign-in still
// carries their directory id.
func ownerAlias(people []teamPerson) func(string) string {
	alias := map[string]string{}
	for _, p := range people {
		if p.UserID != nil {
			alias[p.ID] = *p.UserID
		}
	}
	return func(id string) string {
		if to, ok := alias[id]; ok {
			return to
		}
		return id
	}
}

func (p teamPerson) person() commissions.Person {
	who := commissions.Person{StaffID: p.ID, Position: p.Title, Role: p.Role, UserID: p.ownerKey()}
	if p.ManagerID != nil {
		who.ManagerID = *p.ManagerID
	}
	return who
}

func loadTeamPeople(ctx context.Context, tx pgx.Tx, ws, brand string) ([]teamPerson, error) {
	rows, err := tx.Query(ctx, `SELECT st.id::text, st.name, st.title, st.manager_id::text, st.user_id::text, st.status, COALESCE(NULLIF(sm.role, 'none'), '')
		FROM staff st
		LEFT JOIN sales_members sm ON sm.workspace_id = st.workspace_id AND sm.brand_id = $2 AND sm.user_id = st.user_id
		WHERE st.workspace_id = $1 ORDER BY lower(st.name), st.id`, ws, brand)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[teamPerson])
}

// teamReports maps each person to everyone under them, however deep.
func teamReports(people []teamPerson) map[string][]string {
	direct := map[string][]string{}
	for _, p := range people {
		if p.ManagerID != nil {
			direct[*p.ManagerID] = append(direct[*p.ManagerID], p.ID)
		}
	}
	all := map[string][]string{}
	for _, p := range people {
		seen := map[string]bool{p.ID: true}
		queue := direct[p.ID]
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			if seen[id] {
				continue // a loop in who reports to whom
			}
			seen[id] = true
			all[p.ID] = append(all[p.ID], id)
			queue = append(queue, direct[id]...)
		}
	}
	return all
}

// teamViewer says whose numbers the caller may see: everyone's with the
// right to manage the catalog, otherwise their own and their reports'.
func teamViewer(a *access, people []teamPerson) func(staff string) bool {
	if a.has(auth.ScopeCatalogManage) {
		return func(string) bool { return true }
	}
	user := a.principal.UserID
	if user == "" {
		user = a.principal.TokenOwnerID
	}
	allowed := map[string]bool{}
	reports := teamReports(people)
	for _, p := range people {
		if user != "" && p.UserID != nil && *p.UserID == user {
			allowed[p.ID] = true
			for _, id := range reports[p.ID] {
				allowed[id] = true
			}
		}
	}
	return func(staff string) bool { return allowed[staff] }
}

type teamSum struct {
	Quota    int64 `json:"quota_minor"`
	Won      int64 `json:"won_minor"`
	Forecast int64 `json:"forecast_minor"`
	BestCase int64 `json:"best_case_minor"`
	Open     int   `json:"open"`
	// Percent of quota won; null with no quota.
	Attainment *float64 `json:"attainment"`
}

func (s *teamSum) finish() {
	if s.Quota > 0 {
		v := math.Round(float64(s.Won)/float64(s.Quota)*1000) / 10
		s.Attainment = &v
	}
}

type revenueTeamMember struct {
	StaffID string `json:"staff_id"`
	Name    string `json:"name"`
	Title   string `json:"title"`
	// Their role on the brand's sales desk: sdr, ae, manager or "".
	Role      string  `json:"role"`
	ManagerID *string `json:"manager_id"`
	UserID    *string `json:"user_id"`
	HasQuota  bool    `json:"has_quota"`
	teamSum
	// People under them on the team, however deep.
	Reports int `json:"reports"`
	// Team is them and everyone under them; null for someone with no reports.
	Team *teamSum `json:"team"`
	// Signals is how they handle the signals given to them.
	Signals teamSignals `json:"signals"`
}

// teamSignals is one person's signals: what waits on them now, and what
// they closed in the quarter.
type teamSignals struct {
	// Open is waiting on them now (open or accepted), and Late those of
	// them past their due time that nobody has picked up.
	Open int `json:"open"`
	Late int `json:"late"`
	// Handled is closed in the quarter, done or dismissed, and Won those
	// closed with the outcome won.
	Handled int `json:"handled"`
	Won     int `json:"won"`
	// MedianHours is the usual time from a signal being raised to its
	// being closed, for those handled in the quarter; null with none.
	MedianHours *float64 `json:"median_hours"`
}

func (s *Server) getRevenueTeam(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	out := map[string]any{}
	err := s.readTx(ctx, func(tx pgx.Tx) error {
		fc, err := buildForecast(r, tx, a, r.URL.Query().Get("quarter"))
		if err != nil {
			return err
		}
		people, err := loadTeamPeople(ctx, tx, a.WorkspaceID, a.Brand)
		if err != nil {
			return err
		}
		quotas := map[string]int64{}
		rows, err := tx.Query(ctx, `SELECT staff_id::text, target_minor FROM sales_quotas WHERE workspace_id = $1 AND brand_id = $2 AND period = $3`,
			a.WorkspaceID, a.Brand, fc.Quarter)
		if err != nil {
			return err
		}
		var sid string
		var target int64
		if _, err := pgx.ForEachRow(rows, []any{&sid, &target}, func() error { quotas[sid] = target; return nil }); err != nil {
			return err
		}
		plans, err := loadCommissionPlans(ctx, tx, a, true)
		if err != nil {
			return err
		}
		byUser := map[string]*forecastOwner{}
		unassigned := teamSum{}
		for i := range fc.Owners {
			if o := &fc.Owners[i]; o.OwnerID != nil {
				byUser[*o.OwnerID] = o
			}
		}
		// On the team: a quota this quarter, numbers of their own, or a
		// plan that covers them; and everyone above those.
		byID := map[string]teamPerson{}
		for _, p := range people {
			byID[p.ID] = p
		}
		on := map[string]bool{}
		linked := map[string]bool{}
		for _, p := range people {
			linked[p.ownerKey()] = true
			if p.Status == "left" {
				continue
			}
			_, quota := quotas[p.ID]
			covered := false
			for _, pl := range plans {
				covered = covered || pl.plan().Covers(p.person())
			}
			if quota || covered || p.Role != "" || byUser[p.ownerKey()] != nil {
				for id, hops := p.ID, 0; id != "" && hops < 20; hops++ {
					on[id] = true
					next := byID[id].ManagerID
					if next == nil {
						break
					}
					id = *next
				}
			}
		}
		for i := range fc.Owners {
			if o := &fc.Owners[i]; o.OwnerID == nil || !linked[*o.OwnerID] {
				unassigned.Won += o.Won
				unassigned.Forecast += o.Forecast
				unassigned.BestCase += o.BestCase
				unassigned.Open += o.Open
			}
		}
		// Signals by owner: waiting now, and closed in the quarter.
		signals := map[string]teamSignals{}
		{
			from, _ := time.Parse(time.DateOnly, fc.StartsOn)
			to, _ := time.Parse(time.DateOnly, fc.EndsOn)
			rows, err := tx.Query(ctx, `SELECT owner_id::text,
					count(*) FILTER (WHERE status IN ('open', 'accepted')),
					count(*) FILTER (WHERE status = 'open' AND due_at < now()),
					count(*) FILTER (WHERE status IN ('done', 'dismissed') AND closed_at >= $3 AND closed_at < $4),
					count(*) FILTER (WHERE status IN ('done', 'dismissed') AND outcome = 'won' AND closed_at >= $3 AND closed_at < $4),
					(percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM closed_at - created_at) / 3600)
						FILTER (WHERE status IN ('done', 'dismissed') AND closed_at >= $3 AND closed_at < $4))::float8
				FROM signals
				WHERE workspace_id = $1 AND brand_id = $2 AND owner_id IS NOT NULL AND (status IN ('open', 'accepted') OR closed_at >= $3)
				GROUP BY 1`, a.WorkspaceID, a.Brand, from, to.AddDate(0, 0, 1))
			if err != nil {
				return err
			}
			var who string
			var sg teamSignals
			if _, err := pgx.ForEachRow(rows, []any{&who, &sg.Open, &sg.Late, &sg.Handled, &sg.Won, &sg.MedianHours}, func() error {
				v := sg
				if v.MedianHours != nil {
					h := math.Round(*v.MedianHours*10) / 10
					v.MedianHours = &h
				}
				signals[who] = v
				return nil
			}); err != nil {
				return err
			}
		}
		sees := teamViewer(a, people)
		reports := teamReports(people)
		members := []revenueTeamMember{}
		own := map[string]teamSum{}
		for _, p := range people {
			if !on[p.ID] {
				continue
			}
			sum := teamSum{Quota: quotas[p.ID]}
			if o := byUser[p.ownerKey()]; o != nil {
				sum.Won, sum.Forecast, sum.BestCase, sum.Open = o.Won, o.Forecast, o.BestCase, o.Open
			}
			own[p.ID] = sum
		}
		total := teamSum{}
		for _, p := range people {
			if !on[p.ID] {
				continue
			}
			sum := own[p.ID]
			total.Quota, total.Won, total.Forecast, total.BestCase, total.Open = total.Quota+sum.Quota, total.Won+sum.Won, total.Forecast+sum.Forecast, total.BestCase+sum.BestCase, total.Open+sum.Open
			if !sees(p.ID) {
				continue
			}
			_, has := quotas[p.ID]
			m := revenueTeamMember{StaffID: p.ID, Name: p.Name, Title: p.Title, Role: p.Role, ManagerID: p.ManagerID, UserID: p.UserID, HasQuota: has, teamSum: sum}
			if p.UserID != nil {
				m.Signals = signals[*p.UserID]
			}
			m.finish()
			team := sum
			for _, id := range reports[p.ID] {
				if !on[id] {
					continue
				}
				m.Reports++
				x := own[id]
				team.Quota, team.Won, team.Forecast, team.BestCase, team.Open = team.Quota+x.Quota, team.Won+x.Won, team.Forecast+x.Forecast, team.BestCase+x.BestCase, team.Open+x.Open
			}
			if m.Reports > 0 {
				team.finish()
				m.Team = &team
			}
			members = append(members, m)
		}
		total.finish()
		out = map[string]any{"quarter": fc.Quarter, "starts_on": fc.StartsOn, "ends_on": fc.EndsOn, "days_left": fc.DaysLeft, "currency": fc.Currency,
			"members": members, "can_manage": a.has(auth.ScopeCatalogManage)}
		if a.has(auth.ScopeCatalogManage) {
			out["total"], out["unassigned"] = total, unassigned
		} else {
			out["total"], out["unassigned"] = nil, nil
		}
		return nil
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// setSalesQuotas sets people's quotas for a quarter; a null target takes
// one away.
func (s *Server) setSalesQuotas(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Quarter string `json:"quarter"`
		Quotas  []struct {
			StaffID string `json:"staff_id"`
			Target  *int64 `json:"target_minor"`
		} `json:"quotas"`
	}
	if !decode(w, r, &in) {
		return
	}
	if _, _, ok := quarterSpan(in.Quarter); !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "quarter is a quarter such as "+quarterOf(time.Now().UTC()))
		return
	}
	if len(in.Quotas) == 0 || len(in.Quotas) > 500 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "quotas is 1 to 500 people, each with staff_id and target_minor (null takes the quota away)")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		set, cleared := 0, 0
		for i, q := range in.Quotas {
			at := "quota " + itoa(i+1) + ": "
			if !validUUID(q.StaffID) {
				return errInvalid(at + "staff_id is the id of a person in the team directory")
			}
			if q.Target == nil {
				if _, err := tx.Exec(ctx, `DELETE FROM sales_quotas WHERE workspace_id = $1 AND brand_id = $2 AND staff_id = $3 AND period = $4`,
					a.WorkspaceID, a.Brand, q.StaffID, in.Quarter); err != nil {
					return err
				}
				cleared++
				continue
			}
			if *q.Target < 0 || *q.Target > 1_000_000_000_000 {
				return errInvalid(at + "target_minor is an amount in the brand's currency, in its smallest unit, and not negative")
			}
			tag, err := tx.Exec(ctx, `INSERT INTO sales_quotas (workspace_id, brand_id, staff_id, period, target_minor)
				SELECT $1, $2, st.id, $4, $5 FROM staff st WHERE st.id = $3 AND st.workspace_id = $1
				ON CONFLICT (brand_id, staff_id, period) DO UPDATE SET target_minor = EXCLUDED.target_minor, updated_at = now()`,
				a.WorkspaceID, a.Brand, q.StaffID, in.Quarter, *q.Target)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return errInvalid(at + "staff_id is not a person in this workspace's team directory")
			}
			set++
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "sales_quota.updated", "brand", a.Brand, map[string]any{"quarter": in.Quarter, "set": set, "cleared": cleared})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"quarter": in.Quarter, "updated": len(in.Quotas)})
}

// ---- Commission plans ----

type commissionPlanView struct {
	ID        string             `json:"id"`
	Name      string             `json:"name"`
	Status    string             `json:"status"`
	Positions []string           `json:"positions"`
	StaffIDs  []string           `json:"staff_ids"`
	LapseDays int                `json:"lapse_days"`
	Rules     []commissions.Rule `json:"rules"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
}

func (v commissionPlanView) plan() commissions.Plan {
	return commissions.Plan{ID: v.ID, Name: v.Name, Positions: v.Positions, StaffIDs: v.StaffIDs, LapseDays: v.LapseDays, Rules: v.Rules}
}

func loadCommissionPlans(ctx context.Context, tx pgx.Tx, a *access, activeOnly bool) ([]commissionPlanView, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, name, status, positions, staff_ids::text[], lapse_days, rules, created_at, updated_at FROM commission_plans
		WHERE workspace_id = $1 AND brand_id = $2 AND ($3 = false OR status = 'active')
		ORDER BY status = 'archived', lower(name), id`, a.WorkspaceID, a.Brand, activeOnly)
	if err != nil {
		return nil, err
	}
	list := []commissionPlanView{}
	var v commissionPlanView
	var rules []byte
	_, err = pgx.ForEachRow(rows, []any{&v.ID, &v.Name, &v.Status, &v.Positions, &v.StaffIDs, &v.LapseDays, &rules, &v.CreatedAt, &v.UpdatedAt}, func() error {
		p := v
		p.Rules = []commissions.Rule{}
		if err := json.Unmarshal(rules, &p.Rules); err != nil {
			return err
		}
		p.Positions, p.StaffIDs = nonNil(p.Positions), nonNil(p.StaffIDs)
		list = append(list, p)
		return nil
	})
	return list, err
}

func (s *Server) listCommissionPlans(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var list []commissionPlanView
	err := s.readTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		list, err = loadCommissionPlans(r.Context(), tx, a, false)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

type commissionPlanInput struct {
	Name      *string             `json:"name"`
	Status    *string             `json:"status"`
	Positions *[]string           `json:"positions"`
	StaffIDs  *[]string           `json:"staff_ids"`
	LapseDays *int                `json:"lapse_days"`
	Rules     *[]commissions.Rule `json:"rules"`
}

const maxCommissionPlans = 50

// check tidies the input and says what is wrong with it.
func (in *commissionPlanInput) check(ctx context.Context, tx pgx.Tx, ws string) error {
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if *in.Name == "" || len(*in.Name) > 80 {
			return errInvalid("name is 1 to 80 characters")
		}
	}
	if in.Status != nil && *in.Status != "draft" && *in.Status != "active" && *in.Status != "archived" {
		return errInvalid("status is draft, active or archived")
	}
	if in.LapseDays != nil && (*in.LapseDays < 1 || *in.LapseDays > 1000) {
		return errInvalid("lapse_days is 1 to 1000: the days without a payment after which paying again is a reactivation")
	}
	if in.Positions != nil {
		if len(*in.Positions) > 30 {
			return errInvalid("a plan names at most 30 job titles")
		}
		for i, p := range *in.Positions {
			p = strings.TrimSpace(p)
			if p == "" || len(p) > 200 {
				return errInvalid("positions are job titles from the team directory, 1 to 200 characters each, or role:sdr, role:ae or role:manager for a role on the sales desk")
			}
			if role, ok := strings.CutPrefix(p, commissions.RolePrefix); ok && role != "sdr" && role != "ae" && role != "manager" {
				return errInvalid("a role is role:sdr, role:ae or role:manager")
			}
			(*in.Positions)[i] = p
		}
	}
	if in.StaffIDs != nil {
		if len(*in.StaffIDs) > 500 {
			return errInvalid("a plan names at most 500 people")
		}
		for _, id := range *in.StaffIDs {
			if !validUUID(id) {
				return errInvalid("staff_ids are ids of people in the team directory")
			}
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM staff WHERE workspace_id = $1 AND id::text = ANY($2::text[])`, ws, *in.StaffIDs).Scan(&n); err != nil {
			return err
		}
		if n != len(*in.StaffIDs) {
			return errInvalid("every person in staff_ids must be in this workspace's team directory, and listed once")
		}
	}
	if in.Rules != nil {
		for i := range *in.Rules {
			(*in.Rules)[i].Name = strings.TrimSpace((*in.Rules)[i].Name)
		}
		if msg := commissions.Check(*in.Rules); msg != "" {
			return errInvalid(msg)
		}
	}
	return nil
}

func (s *Server) createCommissionPlan(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in commissionPlanInput
	if !decode(w, r, &in) {
		return
	}
	if in.Name == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	ctx := r.Context()
	var out commissionPlanView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := in.check(ctx, tx, a.WorkspaceID); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM commission_plans WHERE workspace_id = $1 AND brand_id = $2`, a.WorkspaceID, a.Brand).Scan(&n); err != nil {
			return err
		}
		if n >= maxCommissionPlans {
			return errInvalid("a brand keeps at most 50 commission plans: delete one you no longer use")
		}
		status, positions, staff, rules := "draft", []string{}, []string{}, []commissions.Rule{}
		lapse := commissions.DefaultLapseDays
		if in.LapseDays != nil {
			lapse = *in.LapseDays
		}
		if in.Status != nil {
			status = *in.Status
		}
		if in.Positions != nil {
			positions = *in.Positions
		}
		if in.StaffIDs != nil {
			staff = *in.StaffIDs
		}
		if in.Rules != nil {
			rules = *in.Rules
		}
		raw, err := json.Marshal(rules)
		if err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO commission_plans (workspace_id, brand_id, name, status, positions, staff_ids, rules, lapse_days)
			VALUES ($1, $2, $3, $4, $5, $6::uuid[], $7, $8) RETURNING id::text`, a.WorkspaceID, a.Brand, *in.Name, status, positions, staff, raw, lapse).Scan(&id); err != nil {
			return err
		}
		if out, err = commissionPlan(ctx, tx, a, id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "commission_plan.created", "commission_plan", id, map[string]any{"status": status, "rules": len(rules)})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func commissionPlan(ctx context.Context, tx pgx.Tx, a *access, id string) (commissionPlanView, error) {
	list, err := loadCommissionPlans(ctx, tx, a, false)
	if err != nil {
		return commissionPlanView{}, err
	}
	for _, p := range list {
		if p.ID == id {
			return p, nil
		}
	}
	return commissionPlanView{}, errNotFound
}

func (s *Server) updateCommissionPlan(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var in commissionPlanInput
	if !decode(w, r, &in) {
		return
	}
	if !validUUID(id) {
		s.allocError(w, r, errNotFound)
		return
	}
	ctx := r.Context()
	var out commissionPlanView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := in.check(ctx, tx, a.WorkspaceID); err != nil {
			return err
		}
		var rules []byte
		if in.Rules != nil {
			var err error
			if rules, err = json.Marshal(*in.Rules); err != nil {
				return err
			}
		}
		var staff, positions any
		if in.StaffIDs != nil {
			staff = *in.StaffIDs
		}
		if in.Positions != nil {
			positions = *in.Positions
		}
		tag, err := tx.Exec(ctx, `UPDATE commission_plans SET name = COALESCE($4, name), status = COALESCE($5, status),
				positions = COALESCE($6::text[], positions), staff_ids = COALESCE($7::uuid[], staff_ids), rules = COALESCE($8::jsonb, rules),
				lapse_days = COALESCE($9, lapse_days), updated_at = now()
			WHERE id = $1 AND workspace_id = $2 AND brand_id = $3`, id, a.WorkspaceID, a.Brand, in.Name, in.Status, positions, staff, rules, in.LapseDays)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		if out, err = commissionPlan(ctx, tx, a, id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "commission_plan.updated", "commission_plan", id, map[string]any{"status": out.Status, "rules": len(out.Rules)})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteCommissionPlan(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		s.allocError(w, r, errNotFound)
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM commission_plans WHERE id = $1 AND workspace_id = $2 AND brand_id = $3`, id, a.WorkspaceID, a.Brand)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "commission_plan.deleted", "commission_plan", id, nil)
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Statements ----

type commissionStatement struct {
	StaffID string `json:"staff_id"`
	Name    string `json:"name"`
	Title   string `json:"title"`
	// open (worked out now), approved (kept as it was) or paid.
	Status     string             `json:"status"`
	Total      int64              `json:"total_minor"`
	Lines      []commissions.Line `json:"lines"`
	Quota      *int64             `json:"quota_minor"`
	Attainment *float64           `json:"attainment"`
	ApprovedAt *time.Time         `json:"approved_at"`
	PaidAt     *time.Time         `json:"paid_at"`
}

var monthRe = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

const commissionEvents = 5000 // the most deals or payments read for a month

// commissionMonth works out every covered person's statement for a month,
// keeping the approved ones as they were approved.
func commissionMonth(ctx context.Context, tx pgx.Tx, a *access, month string) (string, []commissionStatement, []teamPerson, error) {
	start, _ := time.Parse("2006-01", month)
	end := start.AddDate(0, 1, 0)
	quarter := quarterOf(start)
	qStart, qEnd, _ := quarterSpan(quarter)
	var currency string
	if err := tx.QueryRow(ctx, `SELECT currency FROM brands WHERE id = $1 AND workspace_id = $2`, a.Brand, a.WorkspaceID).Scan(&currency); err != nil {
		return "", nil, nil, err
	}
	rates, err := revenue.Rates(ctx, tx)
	if err != nil {
		return "", nil, nil, err
	}
	people, err := loadTeamPeople(ctx, tx, a.WorkspaceID, a.Brand)
	if err != nil {
		return "", nil, nil, err
	}
	plans, err := loadCommissionPlans(ctx, tx, a, true)
	if err != nil {
		return "", nil, nil, err
	}

	// Deals won in the quarter: those of the month earn, and all of them
	// count towards each owner's share of quota.
	events := []commissions.Event{}
	// For each event, the records whose payments make its customer.
	payers := [][]string{}
	wonBy := map[string]int64{}
	aka := ownerAlias(people)
	rows, err := tx.Query(ctx, `SELECT d.id::text, d.name, COALESCE(d.amount_cents, 0), d.currency, d.closed_at, d.stage, d.renewal_of IS NOT NULL,
			COALESCE(p.id, c.id)::text, COALESCE(p.name, c.name), COALESCE(dr.attributes, '{}'::jsonb), COALESCE(c.attributes, '{}'::jsonb),
			COALESCE(NULLIF(dr.attributes->>'owner', ''), NULLIF(p.attributes->>'owner', ''), c.attributes->>'owner', ''),
			COALESCE(c.attributes->>'owner', ''), COALESCE(dr.attributes->>'sourced_by', ''),
			COALESCE(dr.attributes->>'co_owner', ''), CASE WHEN jsonb_typeof(dr.attributes->'co_owner_percent') = 'number' THEN (dr.attributes->>'co_owner_percent')::float8 END,
			CASE WHEN dr.attributes->>'lead_source' IN ('inbound', 'outbound') THEN dr.attributes->>'lead_source'
				WHEN d.lead_id IS NOT NULL OR d.sequence_id IS NOT NULL OR d.attribution IN ('lead', 'company') THEN 'outbound' ELSE 'inbound' END,
			COALESCE(p.id::text, ''), COALESCE(c.id::text, ''),
			EXISTS (SELECT 1 FROM transactions t WHERE t.workspace_id = d.workspace_id AND t.brand_id = d.brand_id AND t.kind = 'payment' AND t.status = 'succeeded'
				AND t.occurred_at < d.closed_at AND (t.record_id = d.record_id OR t.record_id = c.id))
		FROM deals d
		LEFT JOIN records dr ON dr.id = d.id AND dr.workspace_id = d.workspace_id
		LEFT JOIN records p ON p.id = d.record_id AND p.workspace_id = d.workspace_id AND p.deleted_at IS NULL
		LEFT JOIN records c ON c.workspace_id = d.workspace_id AND c.brand_id = d.brand_id AND c.deleted_at IS NULL AND c.merged_into IS NULL
			AND c.id::text = COALESCE(NULLIF(dr.attributes->>'associated_company', ''), CASE WHEN p.record_type = 'account' THEN p.id::text ELSE p.attributes->>'company' END)
		WHERE d.workspace_id = $1 AND d.brand_id = $2 AND d.state = 'won' AND d.closed_at >= $3 AND d.closed_at < $4
		ORDER BY d.closed_at, d.id LIMIT $5`, a.WorkspaceID, a.Brand, qStart, qEnd.AddDate(0, 0, 1), commissionEvents)
	if err != nil {
		return "", nil, nil, err
	}
	{
		var id, name, code, stage, owner, companyOwner, sourced, coOwner, source, personID, companyID string
		var coPercent *float64
		var amount int64
		var closed time.Time
		var renewal, paying bool
		var recordID, recordName *string
		var attrs, company map[string]any
		if _, err := pgx.ForEachRow(rows, []any{&id, &name, &amount, &code, &closed, &stage, &renewal, &recordID, &recordName, &attrs, &company, &owner, &companyOwner, &sourced, &coOwner, &coPercent, &source, &personID, &companyID, &paying}, func() error {
			owner, companyOwner, sourced, coOwner = aka(owner), aka(companyOwner), aka(sourced), aka(coOwner)
			v, ok := revenue.Convert(rates, amount, code, currency)
			if !ok {
				return nil
			}
			coOwner, share := coOwnerShare(owner, coOwner, coPercent)
			mine, theirs := splitAmount(v, share)
			if owner != "" {
				wonBy[owner] += mine
			}
			if coOwner != "" {
				wonBy[coOwner] += theirs
			}
			if closed.Before(start) || !closed.Before(end) {
				return nil
			}
			kind := "new"
			switch {
			case renewal:
				kind = "renewal"
			case paying:
				kind = "expansion"
			}
			at := map[string]any{}
			for k, x := range attrs {
				at[k] = x
			}
			at["amount"], at["stage"], at["deal_kind"], at["company"] = float64(v)/100, stage, kind, company
			e := commissions.Event{Kind: "deal", ID: id, Name: name, On: closed.UTC().Format(time.DateOnly), Amount: v, DealKind: kind,
				Attrs: at, Owner: owner, CompanyOwner: companyOwner, SourcedBy: sourced, CoOwner: coOwner, CoShare: share, Source: source, Day: int(closed.Unix() / 86400)}
			payers = append(payers, []string{personID, companyID})
			if recordID != nil {
				e.RecordID = *recordID
			}
			if recordName != nil {
				e.RecordName = *recordName
			}
			events = append(events, e)
			return nil
		}); err != nil {
			return "", nil, nil, err
		}
	}

	// Money collected and refunded in the month.
	rows, err = tx.Query(ctx, `SELECT t.id::text, t.kind, t.brand_amount_minor, t.occurred_at, COALESCE(NULLIF(t.description, ''), pr.name, ''), COALESCE(pr.key, ''),
			r.id::text, r.name, r.attributes, COALESCE(c.attributes, '{}'::jsonb),
			COALESCE(NULLIF(r.attributes->>'owner', ''), c.attributes->>'owner', ''), COALESCE(c.attributes->>'owner', ''),
			-- Inbound or outbound: what the customer's record says, then their latest won deal, then inbound.
			COALESCE((SELECT v FROM unnest(ARRAY[r.attributes->>'lead_source', c.attributes->>'lead_source']) v WHERE v IN ('inbound', 'outbound') LIMIT 1),
				(SELECT CASE WHEN wr.attributes->>'lead_source' IN ('inbound', 'outbound') THEN wr.attributes->>'lead_source'
						WHEN w.lead_id IS NOT NULL OR w.sequence_id IS NOT NULL OR w.attribution IN ('lead', 'company') THEN 'outbound' ELSE 'inbound' END
					FROM deals w LEFT JOIN records wr ON wr.id = w.id AND wr.workspace_id = w.workspace_id
					WHERE w.workspace_id = t.workspace_id AND w.brand_id = t.brand_id AND w.state = 'won'
						AND (w.record_id = r.id OR w.record_id = c.id OR wr.attributes->>'associated_company' IN (r.id::text, c.id::text))
					ORDER BY w.closed_at DESC NULLS LAST LIMIT 1), 'inbound'),
			COALESCE(c.id::text, '')
		FROM transactions t
		JOIN records r ON r.id = t.record_id AND r.workspace_id = t.workspace_id AND r.deleted_at IS NULL
		LEFT JOIN records c ON c.workspace_id = r.workspace_id AND c.brand_id = r.brand_id AND c.deleted_at IS NULL AND c.merged_into IS NULL
			AND c.id::text = CASE WHEN r.record_type = 'account' THEN r.id::text ELSE r.attributes->>'company' END
		LEFT JOIN products pr ON pr.id = t.product_id AND pr.workspace_id = t.workspace_id
		WHERE t.workspace_id = $1 AND t.brand_id = $2 AND t.status = 'succeeded' AND t.kind IN ('payment', 'refund') AND t.brand_amount_minor IS NOT NULL
			AND t.occurred_at >= $3 AND t.occurred_at < $4
		ORDER BY t.occurred_at, t.id LIMIT $5`, a.WorkspaceID, a.Brand, start, end, commissionEvents)
	if err != nil {
		return "", nil, nil, err
	}
	{
		var id, kind, name, product, recordID, recordName, owner, companyOwner, source, companyID string
		var amount int64
		var on time.Time
		var attrs, company map[string]any
		if _, err := pgx.ForEachRow(rows, []any{&id, &kind, &amount, &on, &name, &product, &recordID, &recordName, &attrs, &company, &owner, &companyOwner, &source, &companyID}, func() error {
			owner, companyOwner = aka(owner), aka(companyOwner)
			at := map[string]any{}
			for k, x := range attrs {
				at[k] = x
			}
			if amount < 0 {
				amount = -amount
			}
			at["amount"], at["product"], at["company"] = float64(amount)/100, product, company
			if name == "" {
				name = "Payment"
				if kind == "refund" {
					name = "Refund"
				}
			}
			events = append(events, commissions.Event{Kind: kind, ID: id, Name: name, On: on.UTC().Format(time.DateOnly), Amount: amount,
				Attrs: at, Owner: owner, CompanyOwner: companyOwner, RecordID: recordID, RecordName: recordName, Source: source, Day: int(on.Unix() / 86400)})
			payers = append(payers, []string{recordID, companyID})
			return nil
		}); err != nil {
			return "", nil, nil, err
		}
	}

	// Every earlier day each customer paid, so a plan can tell a new
	// customer from one who came back, and count days since they started.
	ids := []string{}
	seen := map[string]bool{}
	for _, pair := range payers {
		for _, id := range pair {
			if id != "" && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	paid := map[string][]int{}
	if len(ids) > 0 {
		rows, err = tx.Query(ctx, `SELECT DISTINCT record_id::text, (extract(epoch FROM occurred_at) / 86400)::int FROM transactions
			WHERE workspace_id = $1 AND brand_id = $2 AND kind = 'payment' AND status = 'succeeded' AND record_id::text = ANY($3::text[]) AND occurred_at < $4
			ORDER BY 2 LIMIT 200000`, a.WorkspaceID, a.Brand, ids, end)
		if err != nil {
			return "", nil, nil, err
		}
		var rid string
		var day int
		if _, err := pgx.ForEachRow(rows, []any{&rid, &day}, func() error { paid[rid] = append(paid[rid], day); return nil }); err != nil {
			return "", nil, nil, err
		}
	}
	for i := range events {
		days := []int{}
		for _, id := range payers[i] {
			for _, d := range paid[id] {
				// A payment on the day itself is this one, or part of the same sale.
				if d < events[i].Day {
					days = append(days, d)
				}
			}
		}
		sort.Ints(days)
		events[i].Paid = days
	}

	quotas := map[string]int64{}
	rows, err = tx.Query(ctx, `SELECT staff_id::text, target_minor FROM sales_quotas WHERE workspace_id = $1 AND brand_id = $2 AND period = $3`, a.WorkspaceID, a.Brand, quarter)
	if err != nil {
		return "", nil, nil, err
	}
	var sid string
	var target int64
	if _, err := pgx.ForEachRow(rows, []any{&sid, &target}, func() error { quotas[sid] = target; return nil }); err != nil {
		return "", nil, nil, err
	}

	managerOf := map[string]string{}
	for _, p := range people {
		if p.ManagerID != nil {
			managerOf[p.ownerKey()] = *p.ManagerID
		}
	}
	full := make([]commissions.Plan, len(plans))
	for i, p := range plans {
		full[i] = p.plan()
	}
	byStaff := map[string]*commissionStatement{}
	out := []commissionStatement{}
	for _, p := range people {
		who := p.person()
		st := commissionStatement{StaffID: p.ID, Name: p.Name, Title: p.Title, Status: "open"}
		if q, ok := quotas[p.ID]; ok {
			st.Quota = &q
			if q > 0 {
				v := math.Round(float64(wonBy[who.UserID])/float64(q)*1000) / 10
				st.Attainment, who.Attainment = &v, &v
			}
		}
		covered := false
		for _, pl := range full {
			covered = covered || pl.Covers(who)
		}
		if !covered || p.Status == "left" {
			continue
		}
		st.Lines = commissions.Statement(who, full, append([]commissions.Event(nil), events...), func(user string) string { return managerOf[user] })
		for _, l := range st.Lines {
			st.Total += l.Amount
		}
		out = append(out, st)
	}
	for i := range out {
		byStaff[out[i].StaffID] = &out[i]
	}

	// Approved and paid statements read as they were kept.
	rows, err = tx.Query(ctx, `SELECT cs.staff_id::text, st.name, st.title, cs.status, cs.total_minor, cs.lines, cs.approved_at, cs.paid_at
		FROM commission_statements cs JOIN staff st ON st.id = cs.staff_id AND st.workspace_id = cs.workspace_id
		WHERE cs.workspace_id = $1 AND cs.brand_id = $2 AND cs.month = $3`, a.WorkspaceID, a.Brand, month)
	if err != nil {
		return "", nil, nil, err
	}
	var kept commissionStatement
	var lines []byte
	var approved time.Time
	extra := []commissionStatement{}
	if _, err := pgx.ForEachRow(rows, []any{&kept.StaffID, &kept.Name, &kept.Title, &kept.Status, &kept.Total, &lines, &approved, &kept.PaidAt}, func() error {
		k := kept
		at := approved
		k.ApprovedAt = &at
		k.Lines = []commissions.Line{}
		if err := json.Unmarshal(lines, &k.Lines); err != nil {
			return err
		}
		if cur := byStaff[k.StaffID]; cur != nil {
			k.Quota, k.Attainment = cur.Quota, cur.Attainment
			*cur = k
		} else {
			extra = append(extra, k)
		}
		return nil
	}); err != nil {
		return "", nil, nil, err
	}
	out = append(out, extra...)
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return currency, out, people, nil
}

func commissionMonthParam(v string) (string, error) {
	if v == "" {
		return time.Now().UTC().Format("2006-01"), nil
	}
	if !monthRe.MatchString(v) {
		return "", errInvalid("month is a month such as " + time.Now().UTC().Format("2006-01"))
	}
	return v, nil
}

func (s *Server) getCommissions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	month, err := commissionMonthParam(r.URL.Query().Get("month"))
	if s.allocError(w, r, err) {
		return
	}
	format := r.URL.Query().Get("format")
	if format != "" && format != "json" && format != "csv" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "format is json or csv")
		return
	}
	var out map[string]any
	var list []commissionStatement
	var cur string
	err = s.readTx(r.Context(), func(tx pgx.Tx) error {
		currency, all, people, err := commissionMonth(r.Context(), tx, a, month)
		if err != nil {
			return err
		}
		sees := teamViewer(a, people)
		shown := []commissionStatement{}
		var total int64
		for _, st := range all {
			if sees(st.StaffID) {
				shown = append(shown, st)
				total += st.Total
			}
		}
		out = map[string]any{"month": month, "currency": currency, "total_minor": total, "statements": shown, "can_manage": a.has(auth.ScopeCatalogManage)}
		list, cur = shown, currency
		return nil
	})
	if s.allocError(w, r, err) {
		return
	}
	if format == "csv" {
		// One row a line, for payroll: who, what paid them and how much.
		header := []string{"month", "person", "title", "status", "date", "kind", "name", "customer", "plan", "rule", "paid_as", "deal_or_payment_amount", "percent", "split_percent", "amount", "currency"}
		rows := [][]string{}
		money := func(v int64) string { return strconv.FormatFloat(float64(v)/100, 'f', 2, 64) }
		pct := func(v float64) string {
			if v == 0 {
				return ""
			}
			return strconv.FormatFloat(v, 'f', -1, 64)
		}
		for _, st := range list {
			for _, l := range st.Lines {
				rows = append(rows, []string{month, st.Name, st.Title, st.Status, l.Event.On, l.Event.Kind, l.Event.Name, l.Event.RecordName, l.PlanName, l.RuleName, l.Who,
					money(l.Event.Amount), pct(l.Percent), pct(l.SplitPercent), money(l.Amount), cur})
			}
		}
		writeCSV(w, "commission-"+month, header, rows)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// setCommissionStatement approves a person's month (keeping its lines as
// they are now), marks it paid, or reopens it so it is worked out again.
func (s *Server) setCommissionStatement(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	staff := r.PathValue("staff")
	var in struct {
		Month  string `json:"month"`
		Status string `json:"status"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !validUUID(staff) {
		s.allocError(w, r, errNotFound)
		return
	}
	if !monthRe.MatchString(in.Month) || (in.Status != "open" && in.Status != "approved" && in.Status != "paid") {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "month is a month such as 2026-11, and status is approved, paid or open (worked out again)")
		return
	}
	ctx := r.Context()
	var out commissionStatement
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var before string
		err := tx.QueryRow(ctx, `SELECT status FROM commission_statements WHERE workspace_id = $1 AND brand_id = $2 AND staff_id = $3 AND month = $4 FOR UPDATE`,
			a.WorkspaceID, a.Brand, staff, in.Month).Scan(&before)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if before == "" {
			before = "open"
		}
		find := func() error {
			_, list, _, err := commissionMonth(ctx, tx, a, in.Month)
			if err != nil {
				return err
			}
			for _, st := range list {
				if st.StaffID == staff {
					out = st
					return nil
				}
			}
			return errNotFound
		}
		switch in.Status {
		case "open":
			if _, err := tx.Exec(ctx, `DELETE FROM commission_statements WHERE workspace_id = $1 AND brand_id = $2 AND staff_id = $3 AND month = $4`,
				a.WorkspaceID, a.Brand, staff, in.Month); err != nil {
				return err
			}
		case "approved", "paid":
			if before == "open" {
				// Keep the month as it is worked out now.
				if err := find(); err != nil {
					return err
				}
				lines, err := json.Marshal(out.Lines)
				if err != nil {
					return err
				}
				var currency string
				if err := tx.QueryRow(ctx, `SELECT currency FROM brands WHERE id = $1 AND workspace_id = $2`, a.Brand, a.WorkspaceID).Scan(&currency); err != nil {
					return err
				}
				var by *string
				if a.principal.UserID != "" {
					by = &a.principal.UserID
				}
				if _, err := tx.Exec(ctx, `INSERT INTO commission_statements (workspace_id, brand_id, staff_id, month, status, total_minor, currency, lines, approved_by)
					VALUES ($1, $2, $3, $4, 'approved', $5, $6, $7, $8)`, a.WorkspaceID, a.Brand, staff, in.Month, out.Total, currency, lines, by); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE commission_statements SET status = $5, paid_at = CASE WHEN $5 = 'paid' THEN COALESCE(paid_at, now()) END
				WHERE workspace_id = $1 AND brand_id = $2 AND staff_id = $3 AND month = $4`, a.WorkspaceID, a.Brand, staff, in.Month, in.Status); err != nil {
				return err
			}
		}
		if err := find(); err != nil {
			return err
		}
		action := map[string]string{"open": "commission.reopened", "approved": "commission.approved", "paid": "commission.paid"}[in.Status]
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "staff", staff, map[string]any{"month": in.Month, "lines": len(out.Lines)})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}
