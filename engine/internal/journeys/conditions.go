package journeys

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/jobs"
)

// subject is what conditions test: a person's fields and what started
// their run.
type subject struct {
	ws, runID, recordID string
	attributes          map[string]any
	context             map[string]any
	vars                map[string]any
	entered             time.Time
}

// holds reports whether the conditions hold: all of them, or any when
// match is any.
func holds(ctx context.Context, q jobs.Querier, s subject, conds []Condition, match string) (bool, error) {
	for _, c := range conds {
		ok, err := test(ctx, q, s, c)
		if err != nil {
			return false, err
		}
		if match == "any" && ok {
			return true, nil
		}
		if match != "any" && !ok {
			return false, nil
		}
	}
	return match != "any", nil
}

func test(ctx context.Context, q jobs.Querier, s subject, c Condition) (bool, error) {
	switch {
	case c.Attribute != "":
		v, ok := lookup(s.attributes, c.Attribute)
		return compare(v, ok, c.Op, c.Value), nil
	case c.Property != "":
		v, ok := lookup(s.context, c.Property)
		return compare(v, ok, c.Op, c.Value), nil
	case c.Var != "":
		v, ok := lookup(s.vars, c.Var)
		return compare(v, ok, c.Op, c.Value), nil
	case c.Event != "":
		var did bool
		// For a company, what its people did counts.
		err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM customer_events WHERE workspace_id = $1 AND name = $3 AND occurred_at >= $4
				AND source <> 'browser' AND (record_id = $2 OR account_id = $2 OR record_id IN (
					SELECT from_id FROM record_relationships WHERE to_id = $2 AND kind = 'companies' AND valid_to IS NULL AND workspace_id = $1)))`,
			s.ws, s.recordID, c.Event, s.entered).Scan(&did)
		return did == (c.Op == "exists"), err
	case c.Email != "":
		var did bool
		err := q.QueryRow(ctx, `SELECT CASE $3 WHEN 'opened' THEN e.opened_at IS NOT NULL WHEN 'clicked' THEN e.clicked_at IS NOT NULL
				ELSE e.delivered_at IS NOT NULL OR e.status IN ('delivered', 'complained') END
			FROM journey_run_steps rs JOIN emails e ON e.id = rs.email_id
			WHERE rs.run_id = $1 AND rs.step = $2 ORDER BY rs.id DESC LIMIT 1`, s.runID, c.Step, c.Email).Scan(&did)
		if errors.Is(err, pgx.ErrNoRows) {
			did, err = false, nil
		}
		return did == (c.Op == "exists"), err
	}
	return false, nil
}

func lookup(m map[string]any, path string) (any, bool) {
	var cur any = m
	for _, part := range strings.Split(path, ".") {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = mm[part]; !ok {
			return nil, false
		}
	}
	return cur, cur != nil
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	}
	return 0, false
}

func text(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if f, ok := v.(float64); ok && f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	return fmt.Sprint(v)
}

var relativeDateRE = regexp.MustCompile(`^(today|[+-]\d{1,4}d)$`)

// RelativeDate reads a condition value that is a date relative to today:
// "today", "+60d" (60 days from now) or "-14d" (14 days ago). It returns
// that day as YYYY-MM-DD in UTC.
func RelativeDate(want any, now time.Time) (string, bool) {
	s, isText := want.(string)
	if !isText || !relativeDateRE.MatchString(s) {
		return "", false
	}
	days := 0
	if s != "today" {
		days, _ = strconv.Atoi(strings.TrimSuffix(s, "d"))
	}
	return now.UTC().AddDate(0, 0, days).Format("2006-01-02"), true
}

// compare applies op to a value that may be missing (ok false).
func compare(v any, ok bool, op string, want any) bool {
	switch op {
	case "exists":
		return ok && text(v) != ""
	case "not_exists":
		return !ok || text(v) == ""
	}
	if !ok {
		return op == "neq"
	}
	// A relative date compares with the value's day: a date, or the day of
	// a time. A value that is no date never matches.
	if day, relative := RelativeDate(want, time.Now()); relative {
		got := text(v)
		if len(got) < 10 || got[4] != '-' || got[7] != '-' {
			return op == "neq"
		}
		c := strings.Compare(got[:10], day)
		return (op == "eq" && c == 0) || (op == "neq" && c != 0) || (op == "gt" && c > 0) || (op == "lt" && c < 0)
	}
	switch op {
	case "eq", "neq":
		a, aok := number(v)
		b, bok := number(want)
		same := strings.EqualFold(text(v), text(want))
		if aok && bok {
			same = a == b
		}
		if bv, isBool := v.(bool); isBool {
			wb, _ := want.(bool)
			same = bv == wb || strings.EqualFold(text(want), strconv.FormatBool(bv))
		}
		return same == (op == "eq")
	case "gt", "lt":
		a, aok := number(v)
		b, bok := number(want)
		if aok && bok {
			return (op == "gt" && a > b) || (op == "lt" && a < b)
		}
		// Dates and other text compare as text (ISO dates sort).
		c := strings.Compare(text(v), text(want))
		return (op == "gt" && c > 0) || (op == "lt" && c < 0)
	case "contains":
		if list, isList := v.([]any); isList {
			for _, x := range list {
				if strings.EqualFold(text(x), text(want)) {
					return true
				}
			}
			return false
		}
		return strings.Contains(strings.ToLower(text(v)), strings.ToLower(text(want)))
	}
	return false
}

// Holds reports whether attribute and property conditions hold for a
// record's fields and an event's properties: all of them, or any when
// match is any. Other features (rules) test the same way journeys do.
func Holds(attributes, properties map[string]any, conds []Condition, match string) bool {
	for _, c := range conds {
		var ok bool
		switch {
		case c.Attribute != "":
			v, found := lookup(attributes, c.Attribute)
			ok = compare(v, found, c.Op, c.Value)
		case c.Property != "":
			v, found := lookup(properties, c.Property)
			ok = compare(v, found, c.Op, c.Value)
		}
		if match == "any" && ok {
			return true
		}
		if match != "any" && !ok {
			return false
		}
	}
	return match != "any"
}

// Lookup reads a dotted path such as metrics.usage_7d from a record's
// fields, and whether it is set.
func Lookup(m map[string]any, path string) (any, bool) { return lookup(m, path) }
