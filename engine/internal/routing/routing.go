// Package routing gives people and companies with no owner to the right
// teammate. A brand keeps an ordered list of rules ("companies with over
// 200 employees go to the enterprise team"); the first rule whose
// conditions hold for a record picks, from its pool of members, the one
// who owns the fewest records in the brand, so work is spread evenly.
// The pool is a list of people or a team (whoever is in it at the time),
// and a rule can keep to a territory: a list of countries.
//
// A round in the background routes records that have no owner as they
// arrive or change; a journey's "Assign an owner" step and the API can
// route one record at once. A record that has an owner keeps them unless
// the caller asks to replace them.
package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/journeys"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Limits on a brand's rules.
const (
	MaxRules      = 50
	MaxConditions = 5
	MaxMembers    = 50
	MaxCountries  = 100
	// roundBatch is how many records one brand gets routed per round.
	roundBatch = 500
)

// Rule is one routing rule.
type Rule struct {
	ID         string               `json:"id"`
	Name       string               `json:"name"`
	RecordType string               `json:"record_type"`
	Conditions []journeys.Condition `json:"conditions"`
	Match      string               `json:"match"`
	Members    []string             `json:"members"`
	// TeamID makes the pool a team: its members at the time a record is
	// routed. Members is empty then.
	TeamID *string `json:"team_id"`
	// Countries is the rule's territory, as two-letter codes; none means
	// anywhere.
	Countries []string `json:"countries"`
	Status    string   `json:"status"`
	Assigned  int64    `json:"assigned"`
}

// Check says what is wrong with a rule, or "".
func Check(r Rule) string {
	switch {
	case strings.TrimSpace(r.Name) == "" || len(r.Name) > 80:
		return "name is 1 to 80 characters"
	case r.RecordType != "person" && r.RecordType != "account":
		return "record_type is account (companies) or person"
	case r.Match != "all" && r.Match != "any":
		return "match is all or any"
	case len(r.Conditions) > MaxConditions:
		return fmt.Sprintf("a rule has at most %d conditions", MaxConditions)
	case r.TeamID != nil && len(r.Members) > 0:
		return "a rule's pool is a team (team_id) or a list of members, not both"
	case r.TeamID == nil && (len(r.Members) == 0 || len(r.Members) > MaxMembers):
		return fmt.Sprintf("a rule's pool is a team (team_id) or 1 to %d members", MaxMembers)
	case len(r.Countries) > MaxCountries:
		return fmt.Sprintf("a rule's territory has at most %d countries", MaxCountries)
	case r.Status != "active" && r.Status != "paused":
		return "status is active or paused"
	}
	seen := map[string]bool{}
	for _, c := range r.Countries {
		if len(c) != 2 || c != strings.ToUpper(c) || c[0] < 'A' || c[0] > 'Z' || c[1] < 'A' || c[1] > 'Z' {
			return "countries are two-letter country codes in capitals, such as DE or US"
		}
		if seen[c] {
			return "countries lists each country once"
		}
		seen[c] = true
	}
	for _, c := range r.Conditions {
		if c.Attribute == "" || c.Property != "" || c.Event != "" || c.Email != "" || c.Var != "" || c.Step != "" {
			return "conditions test attribute, a field on the record such as company_size or country"
		}
		if top, _, _ := strings.Cut(c.Attribute, "."); top == "owner" {
			return "a rule can't test the owner: it only looks at records that have none"
		}
		if msg := journeys.CheckCondition(c); msg != "" {
			return msg
		}
	}
	return ""
}

// Columns are a rule's columns, as Scan reads them.
const Columns = `id::text, name, record_type, conditions, match, members::text[], team_id::text, countries, status, assigned`

// Scan reads a rule.
func Scan(row pgx.CollectableRow) (Rule, error) {
	var r Rule
	var raw []byte
	if err := row.Scan(&r.ID, &r.Name, &r.RecordType, &raw, &r.Match, &r.Members, &r.TeamID, &r.Countries, &r.Status, &r.Assigned); err != nil {
		return r, err
	}
	if err := json.Unmarshal(raw, &r.Conditions); err != nil {
		return r, err
	}
	if r.Conditions == nil {
		r.Conditions = []journeys.Condition{}
	}
	if r.Members == nil {
		r.Members = []string{}
	}
	if r.Countries == nil {
		r.Countries = []string{}
	}
	return r, nil
}

// Load reads a brand's active rules for a record type, in order.
func Load(ctx context.Context, q pgx.Tx, ws, brand, recordType string) ([]Rule, error) {
	rows, err := q.Query(ctx, `SELECT `+Columns+` FROM routing_rules
		WHERE workspace_id = $1 AND brand_id = $2 AND record_type = $3 AND status = 'active' ORDER BY position, created_at, id`, ws, brand, recordType)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, Scan)
}

// Takes says whether a rule takes a record with these attributes: it is
// in the rule's territory and the rule's conditions hold.
func (r Rule) Takes(attrs map[string]any) bool {
	if len(r.Countries) > 0 {
		country, _ := attrs["country"].(string)
		country = strings.ToUpper(strings.TrimSpace(country))
		in := false
		for _, c := range r.Countries {
			if c == country {
				in = true
				break
			}
		}
		if !in {
			return false
		}
	}
	return len(r.Conditions) == 0 || journeys.Holds(attrs, nil, r.Conditions, r.Match)
}

// Result is what routing a record came to.
type Result struct {
	// Outcome is assigned, kept (it has an owner), no_rule (no rule
	// matches), no_member (the matching rule's members have all left) or
	// no_record.
	Outcome  string  `json:"outcome"`
	RuleID   *string `json:"rule_id"`
	RuleName *string `json:"rule_name"`
	OwnerID  *string `json:"owner_id"`
}

// AtWork is SQL for whether the routing_people row p says its person is
// at work now: true with no row or no hours.
const AtWork = `(p.user_id IS NULL OR (
	(cardinality(p.days) = 0 OR extract(isodow FROM now() AT TIME ZONE p.timezone)::int = ANY(p.days))
	AND (p.from_minute IS NULL OR CASE WHEN p.to_minute > p.from_minute
		THEN (extract(hour FROM now() AT TIME ZONE p.timezone) * 60 + extract(minute FROM now() AT TIME ZONE p.timezone))::int >= p.from_minute
			AND (extract(hour FROM now() AT TIME ZONE p.timezone) * 60 + extract(minute FROM now() AT TIME ZONE p.timezone))::int < p.to_minute
		ELSE (extract(hour FROM now() AT TIME ZONE p.timezone) * 60 + extract(minute FROM now() AT TIME ZONE p.timezone))::int >= p.from_minute
			OR (extract(hour FROM now() AT TIME ZONE p.timezone) * 60 + extract(minute FROM now() AT TIME ZONE p.timezone))::int < p.to_minute
		END)))`

// Route finds the rule for a record and, unless dry, makes the member it
// picks the record's owner, in tx. rules are the brand's active rules for
// the record's type, or nil to read them. A record with an owner keeps
// them unless replace.
func Route(ctx context.Context, tx pgx.Tx, ws, rec string, rules []Rule, replace, dry bool) (Result, error) {
	var recordType string
	var brand *string
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT record_type, brand_id::text, attributes FROM records
		WHERE id = $1 AND workspace_id = $2 AND merged_into IS NULL AND archived_at IS NULL AND deleted_at IS NULL FOR NO KEY UPDATE`, rec, ws).
		Scan(&recordType, &brand, &raw)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && brand == nil) {
		return Result{Outcome: "no_record"}, nil
	}
	if err != nil {
		return Result{}, err
	}
	attrs := map[string]any{}
	_ = json.Unmarshal(raw, &attrs)
	current, _ := attrs["owner"].(string)
	if current != "" && !replace {
		return Result{Outcome: "kept", OwnerID: &current}, nil
	}
	if rules == nil {
		if rules, err = Load(ctx, tx, ws, *brand, recordType); err != nil {
			return Result{}, err
		}
	}
	var rule *Rule
	for i := range rules {
		if rules[i].RecordType == recordType && rules[i].Takes(attrs) {
			rule = &rules[i]
			break
		}
	}
	if rule == nil {
		return Result{Outcome: "no_rule"}, nil
	}
	out := Result{RuleID: &rule.ID, RuleName: &rule.Name}
	// The pool: the rule's own list, or whoever is in its team now.
	pool := rule.Members
	if rule.TeamID != nil {
		rows, err := tx.Query(ctx, `SELECT tm.user_id::text FROM team_members tm JOIN teams t ON t.id = tm.team_id
			WHERE tm.team_id = $1 AND t.workspace_id = $2 ORDER BY tm.user_id`, *rule.TeamID, ws)
		if err != nil {
			return Result{}, err
		}
		if pool, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return Result{}, err
		}
	}
	// The member of the pool who owns the fewest of the brand's records;
	// on a tie, the one listed first. Someone who is away or at their
	// capacity gets nothing. People at work now come before people who are
	// off, and when nobody is at work the record still goes to the next
	// person, so nothing waits without an owner.
	var owner string
	err = tx.QueryRow(ctx, `SELECT m.user_id::text FROM memberships m
		LEFT JOIN routing_people p ON p.brand_id = $3 AND p.user_id = m.user_id
		CROSS JOIN LATERAL (SELECT count(*) AS n FROM records r WHERE r.workspace_id = $1 AND r.brand_id = $3 AND r.attributes->>'owner' = m.user_id::text
			AND r.archived_at IS NULL AND r.merged_into IS NULL AND r.deleted_at IS NULL) owned
		WHERE m.workspace_id = $1 AND m.user_id::text = ANY($2::text[])
			AND NOT COALESCE(p.paused, false) AND (p.capacity IS NULL OR owned.n < p.capacity)
		ORDER BY `+AtWork+` DESC, owned.n, array_position($2::text[], m.user_id::text)
		LIMIT 1`, ws, pool, *brand).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		out.Outcome = "no_member"
		return out, nil
	}
	if err != nil {
		return Result{}, err
	}
	out.OwnerID = &owner
	if owner == current {
		out.Outcome = "kept"
		return out, nil
	}
	out.Outcome = "assigned"
	if dry {
		return out, nil
	}
	val, _ := json.Marshal(owner)
	if _, err := tx.Exec(ctx, `UPDATE records SET attributes = jsonb_set(COALESCE(attributes, '{}'), '{owner}', $2), updated_at = now() WHERE id = $1`, rec, val); err != nil {
		return Result{}, err
	}
	// The change is part of the record's own history.
	var before any
	if current != "" {
		before = current
	}
	oldJSON, _ := json.Marshal(before)
	if _, err := tx.Exec(ctx, `INSERT INTO attribute_changes (workspace_id, record_id, brand_id, field_key, old_value, new_value, source)
		VALUES ($1, $2, $3, 'owner', $4, $5, 'routing')`, ws, rec, *brand, oldJSON, val); err != nil {
		return Result{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE routing_rules SET assigned = assigned + 1 WHERE id = $1`, rule.ID); err != nil {
		return Result{}, err
	}
	change := customers.FieldChange{RecordID: rec, Field: "owner", HadOld: current != "", Source: "routing"}
	if current != "" {
		change.Old = current
	}
	return out, customers.FieldChanged(ctx, tx, ws, change)
}

// Router is the routing rules as a journey's "Assign an owner" step uses
// them.
type Router struct{}

// RouteRecord routes one record and says what happened, as a step
// outcome.
func (Router) RouteRecord(ctx context.Context, tx pgx.Tx, ws, rec string, replace bool) (string, string, error) {
	res, err := Route(ctx, tx, ws, rec, nil, replace, false)
	if err != nil {
		return "", "", err
	}
	metrics.RecordRouted(res.Outcome)
	switch res.Outcome {
	case "assigned":
		return "done", "routed by " + *res.RuleName, nil
	case "kept":
		return "skipped", "already has an owner", nil
	case "no_rule":
		return "skipped", "no routing rule matches", nil
	case "no_member":
		return "failed", "nobody in the pool of " + *res.RuleName + " is in the workspace any more", nil
	}
	return "skipped", "the record is gone", nil
}

// Reset has the next round look at every record with no owner in the
// brand again: call it in the transaction that changes the rules.
func Reset(ctx context.Context, tx pgx.Tx, ws, brand string) error {
	_, err := tx.Exec(ctx, `INSERT INTO routing_state (brand_id, workspace_id) VALUES ($1, $2)
		ON CONFLICT (brand_id) DO UPDATE SET checked_until = NULL, checked_id = NULL, updated_at = now()`, brand, ws)
	return err
}

// Service routes records in the background.
type Service struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
}

// Run routes every brand's new and changed records now and then every
// tick, until ctx ends.
func (s *Service) Run(ctx context.Context, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		if _, err := s.Round(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.Log, track.Event{Name: "routing.round_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Round routes, for every brand with active rules, the records with no
// owner that arrived or changed since its last round, and reports how
// many got an owner.
func (s *Service) Round(ctx context.Context) (int, error) {
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT workspace_id::text, brand_id::text FROM routing_rules WHERE status = 'active'`)
	if err != nil {
		return 0, err
	}
	type ref struct{ ws, brand string }
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ref, error) {
		var r ref
		return r, row.Scan(&r.ws, &r.brand)
	})
	if err != nil {
		return 0, err
	}
	total, waiting := 0, 0
	var first error
	for _, r := range list {
		n, more, err := s.Brand(ctx, r.ws, r.brand)
		total += n
		if more {
			waiting++
		}
		if err != nil && first == nil {
			first = err
		}
	}
	metrics.SetRoutingBehind(waiting)
	return total, first
}

// Brand routes one batch of a brand's records with no owner and reports
// how many got one, and whether more are waiting.
func (s *Service) Brand(ctx context.Context, ws, brand string) (int, bool, error) {
	var since *time.Time
	var sinceID *string
	err := s.Pool.QueryRow(ctx, `SELECT checked_until, checked_id::text FROM routing_state WHERE brand_id = $1`, brand).Scan(&since, &sinceID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT r.id::text, r.updated_at FROM records r
		WHERE r.workspace_id = $1 AND r.brand_id = $2 AND COALESCE(r.attributes->>'owner', '') = ''
			AND r.record_type IN (SELECT record_type FROM routing_rules WHERE brand_id = $2 AND status = 'active')
			AND r.merged_into IS NULL AND r.archived_at IS NULL AND r.deleted_at IS NULL
			AND ($3::timestamptz IS NULL OR (r.updated_at, r.id) > ($3, COALESCE($5::uuid, '00000000-0000-0000-0000-000000000000')))
		ORDER BY r.updated_at, r.id LIMIT $4`, ws, brand, since, roundBatch, sinceID)
	if err != nil {
		return 0, false, err
	}
	type item struct {
		id string
		at time.Time
	}
	batch, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (item, error) {
		var it item
		return it, row.Scan(&it.id, &it.at)
	})
	if err != nil || len(batch) == 0 {
		return 0, false, err
	}
	assigned := 0
	for _, it := range batch {
		var res Result
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			var err error
			res, err = Route(ctx, tx, ws, it.id, nil, false, false)
			return err
		})
		if err != nil {
			metrics.RecordRouted("error")
			return assigned, true, err
		}
		metrics.RecordRouted(res.Outcome)
		if res.Outcome == "assigned" {
			assigned++
		}
	}
	// Only move on when the rules didn't change meanwhile.
	last := batch[len(batch)-1]
	_, err = s.Pool.Exec(ctx, `INSERT INTO routing_state (brand_id, workspace_id, checked_until, checked_id) VALUES ($1, $2, $3, $4)
		ON CONFLICT (brand_id) DO UPDATE SET checked_until = EXCLUDED.checked_until, checked_id = EXCLUDED.checked_id, updated_at = now()
		WHERE routing_state.checked_until IS NOT DISTINCT FROM $5`, brand, ws, last.at, last.id, since)
	if assigned > 0 {
		track.Emit(ctx, track.Event{Name: "routing.assigned", WorkspaceID: ws, ActorType: "system", Surface: "system", Props: map[string]any{"records": assigned}})
	}
	return assigned, len(batch) == roundBatch, err
}
