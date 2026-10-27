package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/llm"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/sysmail"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The AI usage log: every call Outflare makes to a workspace's own AI
// model, with the tool that made it, so a workspace can see where its AI
// spend goes and cap each tool. The calls are billed by the provider to
// the workspace's own account; Outflare pays for none of them. Prompts and
// answers are never kept here, only counts.

const (
	// aiCallRetention is how long the log keeps a call: 13 months, so a
	// month can be compared with the same month a year before.
	aiCallRetention = 13 * 31 * 24 * time.Hour
	// jobAIBudgetAlert emails one admin that AI spend reached 80% or all
	// of a limit.
	jobAIBudgetAlert = "ai.budget_alert"
	// aiRecentCalls is how many of the month's calls GET /ai-usage lists.
	aiRecentCalls = 50
)

// aiTaskID is the shape of a tool id: a model task, or a name an extension
// gives its own AI calls (lead_finder).
var aiTaskID = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// aiCallTask is task, or "other" when a caller passed none or a malformed
// one, so every call is logged under some tool.
func aiCallTask(task string) string {
	if aiTaskID.MatchString(task) {
		return task
	}
	return "other"
}

// aiTools are the tools the usage page always lists, in order, even in a
// month they made no calls: the model tasks, AI visibility's sampling and
// meaning-based search's embeddings.
func aiTools() []aiTask {
	out := slices.Clone(aiTasks)
	return append(out, aiTask{taskVisibilitySampling, "AI visibility sampling", "Asking the AI models chosen under AI visibility what they answer about your brands."},
		aiTask{taskSemanticSearch, "Meaning-based search", "Turning your records, replies, meetings and deals into vectors, so search finds them by what they mean."})
}

// aiToolName is the name a person sees for a tool: lead_finder becomes
// "Lead finder" when no tool of the engine's has the id.
func aiToolName(task string) string {
	for _, t := range aiTools() {
		if t.ID == task {
			return t.Name
		}
	}
	if task == "" || task == "other" {
		return "Other"
	}
	name := strings.ReplaceAll(task, "_", " ")
	return strings.ToUpper(name[:1]) + name[1:]
}

// recordAICall writes a call to the usage log and its metrics. It reports
// false, writing nothing, when the ref was logged before (a retried job).
func recordAICall(ctx context.Context, q jobs.Querier, c aiChoice, ws string, missionID, projectID, actorID *string, u llm.Usage, cents int64, ref string) (bool, error) {
	task := aiCallTask(c.Task)
	var refArg *string
	if ref != "" {
		refArg = &ref
	}
	var integration *string
	if c.IntegrationID != "" {
		integration = &c.IntegrationID
	}
	model := u.Model
	if model == "" && c.Model != nil {
		model = c.Model.ID()
	}
	if len(model) > 200 {
		model = model[:200]
	}
	var id int64
	err := q.QueryRow(ctx, `
		INSERT INTO ai_calls (workspace_id, task, provider, model, integration_id, mission_id, project_id, actor_id,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, web_searches, cost_cents, cost_reported, ref)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT (workspace_id, ref) WHERE ref IS NOT NULL DO NOTHING
		RETURNING id`,
		ws, task, c.Provider.ID, model, integration, missionID, projectID, actorID,
		u.InputTokens, u.OutputTokens, u.CacheRead, u.CacheWrite, u.WebSearches, max(cents, 0), u.CostUSD > 0, refArg).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	metrics.AICall(c.Provider.ID, task, u.InputTokens, u.OutputTokens, u.CacheRead, u.CacheWrite, cents)
	return true, nil
}

// monthStart is the first instant of t's calendar month, in UTC.
func monthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// aiTaskMonthSpend is what one tool's AI calls cost the workspace this
// calendar month (UTC).
func aiTaskMonthSpend(ctx context.Context, q rowQuerier, ws, task string) (int64, error) {
	var cents int64
	err := q.QueryRow(ctx, `SELECT COALESCE(sum(cost_cents), 0)::bigint FROM ai_calls
		WHERE workspace_id = $1 AND task = $2 AND occurred_at >= $3`, ws, task, monthStart(time.Now())).Scan(&cents)
	return cents, err
}

// warnAIBudget checks, after a call was booked, whether the month's AI
// spend has now reached 80% or all of the workspace's limit or the tool's
// own, and warns the workspace once a month for each: an audit event (and
// webhook), and an email to each owner and admin, sent by a job.
func warnAIBudget(ctx context.Context, q jobs.Querier, ws, task string) error {
	row, err := loadAISettings(ctx, q, ws)
	if err != nil {
		return err
	}
	type check struct {
		task  string // "" for the workspace's own limit
		limit *int64
		spent func() (int64, error)
	}
	checks := []check{
		{"", row.MonthlyLimit, func() (int64, error) { return aiMonthSpend(ctx, q, ws) }},
		{task, row.taskLimit(task), func() (int64, error) { return aiTaskMonthSpend(ctx, q, ws, task) }},
	}
	month := monthStart(time.Now())
	for _, c := range checks {
		if c.limit == nil || *c.limit <= 0 {
			continue
		}
		spent, err := c.spent()
		if err != nil {
			return err
		}
		level := 0
		switch {
		case spent >= *c.limit:
			level = 100
		case spent*10 >= *c.limit*8:
			level = 80
		default:
			continue
		}
		if level == 100 {
			// Reaching the limit in one step also counts as the warning,
			// so the 80% email never follows the "reached" one.
			if _, err := q.Exec(ctx, `INSERT INTO ai_budget_alerts (workspace_id, month, task, level, limit_cents, spent_cents)
				VALUES ($1, $2, $3, 80, $4, $5) ON CONFLICT DO NOTHING`, ws, month, c.task, *c.limit, spent); err != nil {
				return err
			}
		}
		var inserted bool
		err = q.QueryRow(ctx, `INSERT INTO ai_budget_alerts (workspace_id, month, task, level, limit_cents, spent_cents)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING RETURNING true`, ws, month, c.task, level, *c.limit, spent).Scan(&inserted)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		action := "ai_budget.warned"
		if level == 100 {
			action = "ai_budget.reached"
		}
		scope := "workspace"
		if c.task != "" {
			scope = "tool"
		}
		data := map[string]any{"task": c.task, "level": level, "limit_cents": *c.limit, "spent_cents": spent, "month": month.Format("2006-01")}
		if err := audit.Record(ctx, q, audit.Event{WorkspaceID: ws, ActorType: "system", Action: action,
			TargetType: "workspace", TargetID: ws, Data: data, Props: map[string]any{"task": c.task, "level": level, "scope": scope}}); err != nil {
			return err
		}
		metrics.AIBudgetAlert(level, c.task != "")
		if err := enqueueAIBudgetEmails(ctx, q, ws, c.task, level, month); err != nil {
			return err
		}
	}
	return nil
}

// enqueueAIBudgetEmails queues a warning email to each owner and admin
// with a confirmed address.
func enqueueAIBudgetEmails(ctx context.Context, q jobs.Querier, ws, task string, level int, month time.Time) error {
	var users []string
	if err := q.QueryRow(ctx, `
		SELECT COALESCE(array_agg(u.id::text ORDER BY u.id), '{}') FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.workspace_id = $1 AND m.role IN ('owner', 'admin') AND u.email_verified_at IS NOT NULL`, ws).Scan(&users); err != nil {
		return err
	}
	for _, u := range users {
		key := fmt.Sprintf("%s:%s:%s:%d:%s", ws, month.Format("2006-01"), task, level, u)
		if _, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: jobAIBudgetAlert, WorkspaceID: &ws, IdempotencyKey: key, MaxAttempts: 5,
			Payload: map[string]any{"user_id": u, "task": task, "level": level, "month": month.Format("2006-01")}}); err != nil {
			return err
		}
	}
	return nil
}

// aiBudgetAlertJob emails one admin about the workspace's AI spend. It is
// Outflare's own account email (sysmail), like an invitation: it tells a
// person about their account and quotes nothing from the workspace's data
// but its name.
func (s *Server) aiBudgetAlertJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		UserID string `json:"user_id"`
		Task   string `json:"task"`
		Level  int    `json:"level"`
		Month  string `json:"month"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || !validUUID(p.UserID) {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	month, err := time.Parse("2006-01", p.Month)
	if err != nil {
		return fmt.Errorf("%w: bad month", jobs.ErrPermanent)
	}
	var email, workspace string
	var limit, spent int64
	err = s.pool.QueryRow(ctx, `
		SELECT u.email, w.name, a.limit_cents, a.spent_cents
		FROM ai_budget_alerts a
		JOIN workspaces w ON w.id = a.workspace_id
		JOIN memberships m ON m.workspace_id = a.workspace_id AND m.role IN ('owner', 'admin')
		JOIN users u ON u.id = m.user_id AND u.email_verified_at IS NOT NULL
		WHERE a.workspace_id = $1 AND a.month = $2 AND a.task = $3 AND a.level = $4 AND u.id = $5`,
		ws, month, p.Task, p.Level, p.UserID).Scan(&email, &workspace, &limit, &spent)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // no longer an admin, or the workspace is gone
	}
	if err != nil {
		return err
	}
	ev := track.Event{Name: "ai_budget.emailed", WorkspaceID: ws, UserID: p.UserID, ActorType: "system", Surface: "job",
		Props: map[string]any{"level": p.Level, "task": p.Task}}
	if err := s.sendAccountEmail(ctx, aiBudgetMessage(s.publicURL+"/"+s.workspaceSlug(ctx, ws)+"/ai-usage", email, workspace, p.Task, p.Level, limit, spent)); err != nil {
		track.Fail(ctx, s.log, ev, "email_failed", err)
		return err
	}
	track.Emit(ctx, ev)
	return nil
}

func aiBudgetMessage(url, email, workspace, task string, level int, limit, spent int64) sysmail.Message {
	what, limitName := "AI calls in "+workspace, "the workspace's monthly AI limit"
	if task != "" {
		what, limitName = aiToolName(task)+" in "+workspace, "the monthly limit for "+aiToolName(task)
	}
	m := sysmail.Email{To: email, Button: "See AI usage", URL: url,
		Note: "These calls run on your own AI provider account, with your own key, and your provider bills you for them. Outflare doesn't pay for them or take responsibility for them, so set a hard spend limit at your provider too."}
	body := fmt.Sprintf("%s have cost $%.2f this month, of %s of $%.2f.", what, float64(spent)/100, limitName, float64(limit)/100)
	if level >= 100 {
		m.Subject = "Outflare paused AI calls: monthly limit reached"
		m.Heading = "AI spend reached its limit"
		m.Preview = what + " reached their monthly limit."
		m.Body = []string{body, "Outflare makes no more of these AI calls until next month, or until an owner or admin raises the limit under Settings → AI usage."}
	} else {
		m.Subject = "Your Outflare AI spend is at 80% of its limit"
		m.Heading = "AI spend is at 80% of its limit"
		m.Preview = what + " are close to their monthly limit."
		m.Body = []string{body, "When it reaches the limit, Outflare stops these AI calls until next month. An owner or admin can change the limit under Settings → AI usage."}
	}
	return m.Message()
}

// RunAIUsage deletes calls older than the retention window, in batches so
// it never holds a long lock, every interval until ctx ends.
func (s *Server) RunAIUsage(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		s.PurgeAICalls(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// PurgeAICalls runs one pass of RunAIUsage now and returns how many calls
// it deleted.
func (s *Server) PurgeAICalls(ctx context.Context) int64 {
	cutoff := time.Now().Add(-aiCallRetention)
	var total int64
	for ctx.Err() == nil {
		tag, err := s.pool.Exec(ctx, `DELETE FROM ai_calls WHERE id IN (
			SELECT id FROM ai_calls WHERE occurred_at < $1 ORDER BY occurred_at LIMIT 5000)`, cutoff)
		if err != nil {
			track.Fail(ctx, s.log, track.Event{Name: "ai_calls.purge_failed", ActorType: "system", Surface: "system"}, "internal", err)
			return total
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < 5000 {
			break
		}
	}
	if total > 0 {
		track.Emit(ctx, track.Event{Name: "ai_calls.purged", ActorType: "system", Surface: "system", Props: map[string]any{"count": total}})
	}
	return total
}

// ---- API ----

func (s *Server) routesAIUsage(mux *http.ServeMux) {
	read, manage := auth.ScopeIntegrationsRead, auth.ScopeIntegrationsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/ai-usage", s.inWorkspace(read, s.getAIUsage))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/ai-usage/limits", s.inWorkspace(manage, s.putAIUsageLimits))
}

type aiUsageTotals struct {
	Calls        int64 `json:"calls"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	CacheRead    int64 `json:"cache_read_tokens"`
	CacheWrite   int64 `json:"cache_write_tokens"`
	WebSearches  int64 `json:"web_searches"`
	CostCents    int64 `json:"cost_cents"`
}

type aiUsageTool struct {
	Task        string `json:"task"`
	Name        string `json:"name"`
	Description string `json:"description"`
	aiUsageTotals
	MonthlyLimit *int64 `json:"monthly_limit_cents"`
}

type aiUsageModel struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	aiUsageTotals
}

type aiUsageDay struct {
	Date      string `json:"date"`
	Calls     int64  `json:"calls"`
	CostCents int64  `json:"cost_cents"`
}

type aiUsageCall struct {
	ID         int64     `json:"id"`
	OccurredAt time.Time `json:"occurred_at"`
	Task       string    `json:"task"`
	ToolName   string    `json:"tool_name"`
	Provider   string    `json:"provider"`
	Model      string    `json:"model"`
	MissionID  *string   `json:"mission_id"`
	aiUsageTotals
	CostReported bool `json:"cost_reported"`
}

var monthParam = regexp.MustCompile(`^(19|20)\d\d-(0[1-9]|1[0-2])$`)

// getAIUsage is one month of the workspace's AI calls, by tool, by model
// and by day, with the most recent calls.
func (s *Server) getAIUsage(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	from := monthStart(time.Now())
	if m := r.URL.Query().Get("month"); m != "" {
		if !monthParam.MatchString(m) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "month is a calendar month as YYYY-MM, such as 2026-10")
			return
		}
		from, _ = time.Parse("2006-01", m)
	}
	to := from.AddDate(0, 1, 0)
	row, err := s.loadAISettings(ctx, s.pool, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := s.aiUsage(ctx, a.WorkspaceID, row, from, to)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) aiUsage(ctx context.Context, ws string, row aiSettingsRow, from, to time.Time) (map[string]any, error) {
	scanTotals := func(t *aiUsageTotals) []any {
		return []any{&t.Calls, &t.InputTokens, &t.OutputTokens, &t.CacheRead, &t.CacheWrite, &t.WebSearches, &t.CostCents}
	}
	const sums = `count(*), COALESCE(sum(input_tokens), 0)::bigint, COALESCE(sum(output_tokens), 0)::bigint,
		COALESCE(sum(cache_read_tokens), 0)::bigint, COALESCE(sum(cache_write_tokens), 0)::bigint,
		COALESCE(sum(web_searches), 0)::bigint, COALESCE(sum(cost_cents), 0)::bigint`
	const where = `workspace_id = $1 AND occurred_at >= $2 AND occurred_at < $3`

	var total aiUsageTotals
	if err := s.pool.QueryRow(ctx, `SELECT `+sums+` FROM ai_calls WHERE `+where, ws, from, to).Scan(scanTotals(&total)...); err != nil {
		return nil, err
	}

	// By tool: every tool the engine has, then any other that made calls
	// or has a limit (an extension's).
	seen := map[string]aiUsageTotals{}
	rows, err := s.pool.Query(ctx, `SELECT task, `+sums+` FROM ai_calls WHERE `+where+` GROUP BY task`, ws, from, to)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var task string
		var t aiUsageTotals
		if err := rows.Scan(append([]any{&task}, scanTotals(&t)...)...); err != nil {
			rows.Close()
			return nil, err
		}
		seen[task] = t
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	tools := []aiUsageTool{}
	listed := map[string]bool{}
	for _, t := range aiTools() {
		tools = append(tools, aiUsageTool{Task: t.ID, Name: t.Name, Description: t.Description, aiUsageTotals: seen[t.ID], MonthlyLimit: row.taskLimit(t.ID)})
		listed[t.ID] = true
	}
	extra := []string{}
	for id := range seen {
		if !listed[id] {
			extra = append(extra, id)
		}
	}
	for id := range row.TaskLimits {
		if !listed[id] && seen[id] == (aiUsageTotals{}) {
			extra = append(extra, id)
		}
	}
	slices.Sort(extra)
	for _, id := range slices.Compact(extra) {
		tools = append(tools, aiUsageTool{Task: id, Name: aiToolName(id), aiUsageTotals: seen[id], MonthlyLimit: row.taskLimit(id)})
	}
	// Most spent first; tools with no calls keep their order at the end.
	slices.SortStableFunc(tools, func(x, y aiUsageTool) int {
		switch {
		case x.CostCents != y.CostCents:
			return int(min(max(y.CostCents-x.CostCents, -1), 1))
		default:
			return int(min(max(y.Calls-x.Calls, -1), 1))
		}
	})

	byModel := []aiUsageModel{}
	rows, err = s.pool.Query(ctx, `SELECT provider, model, `+sums+` FROM ai_calls WHERE `+where+`
		GROUP BY provider, model ORDER BY sum(cost_cents) DESC, count(*) DESC, provider, model`, ws, from, to)
	if err != nil {
		return nil, err
	}
	byModel, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (aiUsageModel, error) {
		var m aiUsageModel
		return m, r.Scan(append([]any{&m.Provider, &m.Model}, scanTotals(&m.aiUsageTotals)...)...)
	})
	if err != nil {
		return nil, err
	}

	rows, err = s.pool.Query(ctx, `SELECT to_char(date_trunc('day', occurred_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD'), count(*), COALESCE(sum(cost_cents), 0)::bigint
		FROM ai_calls WHERE `+where+` GROUP BY 1 ORDER BY 1`, ws, from, to)
	if err != nil {
		return nil, err
	}
	days, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (aiUsageDay, error) {
		var d aiUsageDay
		return d, r.Scan(&d.Date, &d.Calls, &d.CostCents)
	})
	if err != nil {
		return nil, err
	}

	rows, err = s.pool.Query(ctx, `SELECT id, occurred_at, task, provider, model, mission_id::text,
			1, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, web_searches, cost_cents, cost_reported
		FROM ai_calls WHERE `+where+` ORDER BY occurred_at DESC, id DESC LIMIT $4`, ws, from, to, aiRecentCalls)
	if err != nil {
		return nil, err
	}
	recent, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (aiUsageCall, error) {
		var c aiUsageCall
		err := r.Scan(append([]any{&c.ID, &c.OccurredAt, &c.Task, &c.Provider, &c.Model, &c.MissionID}, append(scanTotals(&c.aiUsageTotals), &c.CostReported)...)...)
		c.ToolName = aiToolName(c.Task)
		return c, err
	})
	if err != nil {
		return nil, err
	}

	// The ledger the workspace limit counts from (it also has calls booked
	// before the usage log began).
	var ledger int64
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(sum(amount_cents), 0) FROM spend_entries
		WHERE workspace_id = $1 AND source = 'auto' AND category = 'software' AND provider = ANY($4)
		  AND occurred_at >= $2 AND occurred_at < $3`, ws, from, to, llm.Vendors()).Scan(&ledger); err != nil {
		return nil, err
	}
	return map[string]any{
		"month":               from.Format("2006-01"),
		"spent_cents":         ledger,
		"monthly_limit_cents": row.MonthlyLimit,
		"total":               total,
		"tools":               tools,
		"models":              byModel,
		"days":                days,
		"recent":              recent,
		"retention_days":      int(aiCallRetention / (24 * time.Hour)),
	}, nil
}

// putAIUsageLimits replaces the per-tool monthly limits.
func (s *Server) putAIUsageLimits(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Limits map[string]*int64 `json:"limits"`
	}
	if !decode(w, r, &req) {
		return
	}
	bad := func(msg string) { writeError(w, r, http.StatusBadRequest, "invalid_request", msg) }
	if len(req.Limits) > 50 {
		bad("limits has at most 50 tools")
		return
	}
	out := map[string]int64{}
	for id, v := range req.Limits {
		switch {
		case !aiTaskID.MatchString(id):
			bad("limits keys are tool ids as GET /ai-usage lists them, such as reply_labelling")
			return
		case v == nil:
			continue
		case *v < 0 || *v > 10_000_000:
			bad("limits." + id + " is between 0 and 10000000 cents (US$100,000), or null for no limit of its own")
			return
		}
		out[id] = *v
	}
	limits, err := json.Marshal(out)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	tools := make([]string, 0, len(out))
	for id := range out {
		tools = append(tools, id)
	}
	slices.Sort(tools)
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO workspace_ai_settings (workspace_id, task_limits, updated_at, updated_by) VALUES ($1, $2, now(), $3)
			ON CONFLICT (workspace_id) DO UPDATE SET task_limits = $2, updated_at = now(), updated_by = $3`,
			a.WorkspaceID, limits, a.ActorID); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "ai_limits.updated", "workspace", a.WorkspaceID,
			map[string]any{"tools": tools, "limits_cents": out})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.getAIUsage(w, r)
}
