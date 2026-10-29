// Package smartlists keeps smart lists up to date: groups of people on
// customer records that match a list's rules right now, such as "plan is
// pro and paid in the last 90 days". A refresh works out who matches in one
// statement, adds the people who started matching and removes the ones
// who stopped, so smart_list_members is always the current membership.
// Lists refresh when they are saved, when someone asks, and on a sweep
// every few minutes for rules that depend on time ("in the last 90 days").
// The API handlers in engine/internal/api own making and changing lists.
package smartlists

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

const (
	// JobRefresh refreshes one list.
	JobRefresh = "smart_list.refresh"
	// Every is how often each list is refreshed without being asked.
	Every = 15 * time.Minute
	// MaxRules is the most rules a list can have.
	MaxRules = 20
	// refreshTimeout bounds the one statement a refresh runs.
	refreshTimeout = "120s"
)

// Rule is one test a person must pass. Set exactly one of:
//
//   - attribute: a field on their customer record (plan, address.country),
//     compared with op and value as journeys compare them.
//   - event: whether they did this customer event (exists) or not
//     (not_exists), ever or in the last within_days days.
//   - in_list: whether they are a lead in this lead list (exists) or not
//     (not_exists).
//   - news_search: whether they wrote an article this news search found
//     (exists) or not (not_exists), ever or in the last within_days days:
//     a media list of the journalists who cover a topic.
type Rule struct {
	Attribute  string `json:"attribute,omitempty"`
	Event      string `json:"event,omitempty"`
	WithinDays int    `json:"within_days,omitempty"`
	InList     string `json:"in_list,omitempty"`
	NewsSearch string `json:"news_search,omitempty"`
	Op         string `json:"op"`
	Value      any    `json:"value,omitempty"`
}

var (
	fieldRE   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}(\.[a-z0-9_]{1,62})*$`)
	eventRE   = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$`)
	uuidRE    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	ops       = map[string]bool{"eq": true, "neq": true, "gt": true, "lt": true, "contains": true, "exists": true, "not_exists": true}
	numericRE = `'^\s*-?[0-9]+(\.[0-9]+)?\s*$'`
)

// Check reports what is wrong with a list's rules, or "".
func Check(rules []Rule, match string) string {
	if match != "all" && match != "any" {
		return "match is all or any"
	}
	if len(rules) > MaxRules {
		return fmt.Sprintf("a smart list has at most %d rules", MaxRules)
	}
	for i, r := range rules {
		if msg := checkRule(r); msg != "" {
			return fmt.Sprintf("rule %d: %s", i+1, msg)
		}
	}
	return ""
}

func checkRule(r Rule) string {
	set := 0
	for _, v := range []string{r.Attribute, r.Event, r.InList, r.NewsSearch} {
		if v != "" {
			set++
		}
	}
	if set != 1 {
		return "each rule tests one of attribute, event, in_list or news_search"
	}
	if !ops[r.Op] {
		return "op is eq, neq, gt, lt, contains, exists or not_exists"
	}
	switch {
	case r.Attribute != "" && (!fieldRE.MatchString(r.Attribute) || len(r.Attribute) > 200):
		return "attribute is a field name such as plan or address.country"
	case r.Event != "" && (!eventRE.MatchString(r.Event) || len(r.Event) > 100):
		return "event is an event name such as order_completed"
	case r.InList != "" && !uuidRE.MatchString(r.InList):
		return "in_list is a lead list's id"
	case r.NewsSearch != "" && !uuidRE.MatchString(r.NewsSearch):
		return "news_search is a news search's id"
	case (r.Event != "" || r.InList != "" || r.NewsSearch != "") && r.Op != "exists" && r.Op != "not_exists":
		return "event, in_list and news_search rules use exists (did, is in) or not_exists (didn't, isn't)"
	case r.WithinDays != 0 && r.Event == "" && r.NewsSearch == "":
		return "within_days is only for event and news_search rules"
	case r.WithinDays < 0 || r.WithinDays > 3650:
		return "within_days is 1 to 3650, or 0 for ever"
	case r.Op != "exists" && r.Op != "not_exists" && r.Value == nil:
		return "op " + r.Op + " needs a value"
	case r.Op == "exists" || r.Op == "not_exists":
		if r.Value != nil && r.Attribute != "" {
			return "op " + r.Op + " takes no value"
		}
	}
	switch v := r.Value.(type) {
	case nil, bool, float64, json.Number, int, int64:
	case string:
		if len(v) > 500 {
			return "value is at most 500 characters"
		}
	default:
		return "value is text, a number or true or false"
	}
	return ""
}

// InLists is every lead list the rules name, for the caller to check they
// belong to the workspace.
func InLists(rules []Rule) []string {
	out := []string{}
	for _, r := range rules {
		if r.InList != "" {
			out = append(out, r.InList)
		}
	}
	return out
}

// NewsSearches is every news search the rules name, for the caller to
// check they belong to the workspace.
func NewsSearches(rules []Rule) []string {
	out := []string{}
	for _, r := range rules {
		if r.NewsSearch != "" {
			out = append(out, r.NewsSearch)
		}
	}
	return out
}

// text is a value as journeys compare it.
func text(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case json.Number:
		return x.String()
	}
	return fmt.Sprint(v)
}

func number(v any) (string, bool) {
	switch x := v.(type) {
	case bool, nil:
		return "", false
	case string:
		if _, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err != nil {
			return "", false
		}
		return strings.TrimSpace(x), true
	}
	s := text(v)
	_, err := strconv.ParseFloat(s, 64)
	return s, err == nil
}

// builder collects a statement's arguments.
type builder struct{ args []any }

func (b *builder) arg(v any) string {
	b.args = append(b.args, v)
	return "$" + strconv.Itoa(len(b.args))
}

// Where returns the SQL that picks a list's people from records aliased r:
// people in the workspace (args[0] must be its id), not merged or archived,
// in the list's brand or shared by every brand, who pass the rules. No
// rules picks everyone. args is extended with the values it uses.
func Where(rules []Rule, match string, brandID *string, args []any) (string, []any) {
	b := &builder{args: args}
	where := `r.workspace_id = $1 AND r.record_type = 'person' AND r.merged_into IS NULL AND r.archived_at IS NULL`
	return b.where(where, rules, match, brandID)
}

// Seen narrows records r to people of the brands in (and the shared ones),
// for a caller limited to some brands; nil lets everyone through.
func Seen(in []string, args []any) (string, []any) {
	if in == nil {
		return "", args
	}
	args = append(args, in)
	return ` AND (r.brand_id IS NULL OR r.brand_id = ANY($` + strconv.Itoa(len(args)) + `::uuid[]))`, args
}

func (b *builder) where(where string, rules []Rule, match string, brandID *string) (string, []any) {
	eventBrand := ""
	if brandID != nil {
		p := b.arg(*brandID)
		where += ` AND (r.brand_id = ` + p + ` OR r.brand_id IS NULL)`
		eventBrand = ` AND (e.brand_id = ` + p + ` OR e.brand_id IS NULL)`
	}
	if len(rules) == 0 {
		return where, b.args
	}
	parts := make([]string, 0, len(rules))
	for _, r := range rules {
		parts = append(parts, `COALESCE(`+b.rule(r, eventBrand)+`, false)`)
	}
	join := " AND "
	if match == "any" {
		join = " OR "
	}
	return where + ` AND (` + strings.Join(parts, join) + `)`, b.args
}

func (b *builder) rule(r Rule, eventBrand string) string {
	switch {
	case r.Event != "":
		// Browser events can be sent by anyone with the site key, so
		// they never put someone in a list, as in journeys.
		sql := `EXISTS (SELECT 1 FROM customer_events e WHERE e.workspace_id = $1 AND e.record_id = r.id AND e.name = ` + b.arg(r.Event) +
			` AND e.source <> 'browser'` + eventBrand
		if r.WithinDays > 0 {
			sql += ` AND e.occurred_at >= now() - make_interval(days => ` + b.arg(r.WithinDays) + `::int)`
		}
		sql += `)`
		if r.Op == "not_exists" {
			return `NOT ` + sql
		}
		return sql
	case r.NewsSearch != "":
		sql := `EXISTS (SELECT 1 FROM news_article_authors aa JOIN news_hits h ON h.article_id = aa.article_id
			JOIN news_articles x ON x.id = aa.article_id
			WHERE aa.workspace_id = $1 AND aa.record_id = r.id AND h.search_id = ` + b.arg(r.NewsSearch) + `::uuid`
		if r.WithinDays > 0 {
			sql += ` AND coalesce(x.published_at, x.first_seen_at) >= now() - make_interval(days => ` + b.arg(r.WithinDays) + `::int)`
		}
		sql += `)`
		if r.Op == "not_exists" {
			return `NOT ` + sql
		}
		return sql
	case r.InList != "":
		sql := `EXISTS (SELECT 1 FROM lead_list_members m JOIN leads l ON l.id = m.lead_id
			JOIN lead_lists ll ON ll.id = m.list_id AND ll.workspace_id = $1
			WHERE m.list_id = ` + b.arg(r.InList) + ` AND l.record_id = r.id)`
		if r.Op == "not_exists" {
			return `NOT ` + sql
		}
		return sql
	}
	path := b.arg(strings.Split(r.Attribute, "."))
	j := `(r.attributes #> ` + path + `::text[])`
	t := `(r.attributes #>> ` + path + `::text[])`
	switch r.Op {
	case "exists":
		return `COALESCE(` + t + `, '') <> ''`
	case "not_exists":
		return `COALESCE(` + t + `, '') = ''`
	}
	s := b.arg(text(r.Value))
	eq := func() string {
		if n, ok := number(r.Value); ok {
			return `(CASE WHEN ` + t + ` ~ ` + numericRE + ` THEN ` + t + `::numeric = ` + b.arg(n) + `::text::numeric ELSE lower(` + t + `) = lower(` + s + `) END)`
		}
		return `lower(` + t + `) = lower(` + s + `)`
	}
	switch r.Op {
	case "eq":
		return eq()
	case "neq":
		return `(` + t + ` IS NULL OR NOT COALESCE(` + eq() + `, false))`
	case "gt", "lt":
		cmp := ">"
		if r.Op == "lt" {
			cmp = "<"
		}
		// Numbers compare as numbers, everything else as text, so ISO
		// dates sort.
		asText := `(` + t + ` COLLATE "C") ` + cmp + ` (` + s + ` COLLATE "C")`
		if n, ok := number(r.Value); ok {
			return `(CASE WHEN ` + t + ` ~ ` + numericRE + ` THEN ` + t + `::numeric ` + cmp + ` ` + b.arg(n) + `::text::numeric ELSE ` + asText + ` END)`
		}
		return asText
	case "contains":
		return `(CASE WHEN jsonb_typeof(` + j + `) = 'array'
			THEN EXISTS (SELECT 1 FROM jsonb_array_elements_text(` + j + `) x WHERE lower(x) = lower(` + s + `))
			ELSE strpos(lower(` + t + `), lower(` + s + `)) > 0 END)`
	}
	return `false`
}

// Querier is a pool or a transaction.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Person is one member, as a preview or a member list shows them.
type Person struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	EnteredAt time.Time `json:"entered_at,omitzero"`
}

// personCols is a person's name and first verified email, from records r.
const personCols = `r.id, r.name, COALESCE((SELECT i.value FROM record_identities i WHERE i.record_id = r.id AND i.namespace = 'email'
	AND i.valid_to IS NULL ORDER BY i.verified DESC, i.valid_from, i.id LIMIT 1), '')`

// Preview counts who the rules pick now and returns up to limit of them,
// newest records first, without saving anything.
//
// seen limits it to some brands' people (Seen); nil counts everyone.
func Preview(ctx context.Context, q Querier, ws string, rules []Rule, match string, brandID *string, seen []string, limit int) (int, []Person, error) {
	where, args := Where(rules, match, brandID, []any{ws})
	more, args := Seen(seen, args)
	where += more
	var n int
	if err := q.QueryRow(ctx, `SELECT count(*)::int FROM records r WHERE `+where, args...).Scan(&n); err != nil {
		return 0, nil, err
	}
	rows, err := q.Query(ctx, `SELECT `+personCols+` FROM records r WHERE `+where+` ORDER BY r.created_at DESC, r.id DESC LIMIT `+strconv.Itoa(limit), args...)
	if err != nil {
		return 0, nil, err
	}
	sample, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Person, error) {
		var p Person
		return p, row.Scan(&p.ID, &p.Name, &p.Email)
	})
	if sample == nil {
		sample = []Person{}
	}
	return n, sample, err
}

// Members lists a list's members, newest to join first, after the cursor
// (the entered_at and record id of the last one seen). seen limits it to
// some brands' people (Seen); nil lists everyone.
func Members(ctx context.Context, q Querier, ws, listID string, seen []string, after *time.Time, afterID string, limit int) ([]Person, error) {
	args := []any{ws, listID, limit}
	cond := ""
	if after != nil {
		args = append(args, *after, afterID)
		cond = ` AND (m.entered_at, m.record_id) < ($4, $5::uuid)`
	}
	more, args := Seen(seen, args)
	cond += more
	rows, err := q.Query(ctx, `SELECT `+personCols+`, m.entered_at FROM smart_list_members m JOIN records r ON r.id = m.record_id
		WHERE m.workspace_id = $1 AND m.list_id = $2`+cond+` ORDER BY m.entered_at DESC, m.record_id DESC LIMIT $3`, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Person, error) {
		var p Person
		return p, row.Scan(&p.ID, &p.Name, &p.Email, &p.EnteredAt)
	})
	if out == nil {
		out = []Person{}
	}
	return out, err
}

// Result is what one refresh changed.
type Result struct {
	Joined  int `json:"joined"`
	Left    int `json:"left"`
	Members int `json:"members"`
}

// ErrGone means the list was deleted before it refreshed.
var ErrGone = errors.New("smart list not found")

// Refresh brings one list's members up to date in tx and reports who
// joined and left.
func Refresh(ctx context.Context, tx pgx.Tx, ws, listID string) (Result, error) {
	var res Result
	var raw []byte
	var match string
	var brandID *string
	err := tx.QueryRow(ctx, `SELECT rules, match, brand_id FROM smart_lists WHERE workspace_id = $1 AND id = $2 FOR UPDATE`, ws, listID).
		Scan(&raw, &match, &brandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return res, ErrGone
	}
	if err != nil {
		return res, err
	}
	var rules []Rule
	if err := json.Unmarshal(raw, &rules); err != nil {
		return res, fmt.Errorf("%w: rules: %v", jobs.ErrPermanent, err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '`+refreshTimeout+`'`); err != nil {
		return res, err
	}
	// Rules that act on people joining or leaving need to know who did;
	// otherwise counts are enough.
	watched, err := customers.ListWatched(ctx, tx, ws, listID)
	if err != nil {
		return res, err
	}
	who := `'{}'::text[], '{}'::text[]`
	if watched {
		who = `(SELECT COALESCE(array_agg(record_id::text), '{}') FROM added), (SELECT COALESCE(array_agg(record_id::text), '{}') FROM gone)`
	}
	where, args := Where(rules, match, brandID, []any{ws})
	args = append(args, listID)
	l := "$" + strconv.Itoa(len(args))
	var joined, left []string
	err = tx.QueryRow(ctx, `WITH want AS (SELECT r.id FROM records r WHERE `+where+`),
		gone AS (DELETE FROM smart_list_members m WHERE m.list_id = `+l+`
			AND NOT EXISTS (SELECT 1 FROM want w WHERE w.id = m.record_id) RETURNING m.record_id),
		added AS (INSERT INTO smart_list_members (workspace_id, list_id, record_id) SELECT $1, `+l+`, id FROM want
			ON CONFLICT (list_id, record_id) DO NOTHING RETURNING record_id)
		SELECT (SELECT count(*) FROM added)::int, (SELECT count(*) FROM gone)::int, (SELECT count(*) FROM want)::int, `+who, args...).
		Scan(&res.Joined, &res.Left, &res.Members, &joined, &left)
	if err != nil {
		return res, err
	}
	at := time.Now()
	for on, ids := range map[string][]string{"enter": joined, "leave": left} {
		if len(ids) == 0 {
			continue
		}
		if err := customers.ListChanged(ctx, tx, ws, customers.ListChange{ListID: listID, On: on, RecordIDs: ids, At: at}); err != nil {
			return res, err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE smart_lists SET members = $3, refreshed_at = now(), error_code = NULL WHERE workspace_id = $1 AND id = $2`,
		ws, listID, res.Members)
	return res, err
}

type payload struct {
	List   string `json:"list_id"`
	Manual bool   `json:"manual,omitempty"`
}

// Enqueue asks for a refresh of one list in q's transaction. manual marks
// one someone asked for (saving the list, or Refresh now), which is
// recorded as an event even when nobody joined or left.
func Enqueue(ctx context.Context, q jobs.Querier, ws, listID string, manual bool) (int64, error) {
	key := fmt.Sprintf("smart_list:%s:%d", listID, time.Now().UnixNano())
	if !manual {
		key = fmt.Sprintf("smart_list:%s:%d", listID, time.Now().Truncate(Every).Unix())
	}
	return jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobRefresh, WorkspaceID: &ws, Payload: payload{List: listID, Manual: manual},
		IdempotencyKey: key, MaxAttempts: 3})
}

// Service runs refreshes as jobs and schedules the sweep.
type Service struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
}

// Register adds the job handler.
func (s *Service) Register(q *jobs.Queue) { q.Handle(JobRefresh, s.job) }

func (s *Service) job(ctx context.Context, job jobs.Job) error {
	var p payload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || p.List == "" {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	start := time.Now()
	var res Result
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		res, err = Refresh(ctx, tx, ws, p.List)
		return err
	})
	if errors.Is(err, ErrGone) {
		return nil
	}
	ev := track.Event{Name: "smart_list.refreshed", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"list_id": p.List, "joined": res.Joined, "left": res.Left, "members": res.Members,
			"manual": p.Manual, "duration_ms": time.Since(start).Milliseconds()}}
	if err != nil {
		code := "internal"
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "57014" {
			code = "query_timeout"
		}
		track.Fail(ctx, s.Log, ev, code, err)
		if _, uerr := s.Pool.Exec(ctx, `UPDATE smart_lists SET error_code = $3 WHERE workspace_id = $1 AND id = $2`, ws, p.List, code); uerr != nil {
			s.Log.Error("smart lists: record refresh error", "err", uerr)
		}
		return err
	}
	if p.Manual || res.Joined > 0 || res.Left > 0 {
		track.Emit(ctx, ev)
	}
	return nil
}

// Run schedules a refresh of every list not refreshed for Every, until ctx
// ends.
func (s *Service) Run(ctx context.Context, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		if err := s.Sweep(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.Log, track.Event{Name: "smart_list.sweep_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Sweep enqueues a refresh for every list that is due.
func (s *Service) Sweep(ctx context.Context) error {
	rows, err := s.Pool.Query(ctx, `SELECT workspace_id::text, id::text FROM smart_lists
		WHERE refreshed_at IS NULL OR refreshed_at < now() - $1::interval ORDER BY refreshed_at NULLS FIRST LIMIT 1000`,
		fmt.Sprintf("%d seconds", int(Every.Seconds())))
	if err != nil {
		return err
	}
	type due struct{ ws, id string }
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
		var d due
		return d, row.Scan(&d.ws, &d.id)
	})
	if err != nil {
		return err
	}
	for _, d := range list {
		if _, err := Enqueue(ctx, s.Pool, d.ws, d.id, false); err != nil {
			return err
		}
	}
	return nil
}
