package leadportal

import (
	"math"
	"strings"
	"time"
)

// Private operational evidence is bounded and validated separately from aggregate reporting.
func validTrace(value any) bool {
	p, ok := value.(map[string]any)
	if !ok || p["scope"] != "latest_20_runs" {
		return false
	}
	number := func(v any, nullable bool) bool {
		if v == nil {
			return nullable
		}
		n, ok := v.(float64)
		return ok && n >= 0 && !math.IsInf(n, 0) && !math.IsNaN(n)
	}
	whole := func(v any) bool { n, ok := v.(float64); return ok && number(v, false) && math.Trunc(n) == n }
	text := func(v any, max int, nullable bool) bool {
		if v == nil {
			return nullable
		}
		s, ok := v.(string)
		return ok && len(s) <= max
	}
	date := func(v any, nullable bool) bool {
		if v == nil {
			return nullable
		}
		s, ok := v.(string)
		if !ok {
			return false
		}
		_, e := time.Parse(time.RFC3339Nano, s)
		return e == nil
	}
	if !whole(p["total"]) || !whole(p["offset"]) {
		return false
	}
	summary, ok := p["summary"].(map[string]any)
	if !ok {
		return false
	}
	for _, k := range []string{"calls", "errors", "unknown_cost_calls"} {
		if !whole(summary[k]) {
			return false
		}
	}
	if !number(summary["recorded_cents"], true) {
		return false
	}
	for _, spec := range []struct {
		key   string
		limit int
	}{{"runs", 20}, {"tools", 100}, {"lanes", 50}, {"rows", 50}} {
		rows, ok := p[spec.key].([]any)
		if !ok || len(rows) > spec.limit {
			return false
		}
		for _, v := range rows {
			r, ok := v.(map[string]any)
			if !ok {
				return false
			}
			switch spec.key {
			case "runs":
				if !text(r["id"], 36, false) || !text(r["mission_id"], 36, false) || !date(r["started_at"], false) || !date(r["finished_at"], true) || !date(r["last_event_at"], true) {
					return false
				}
				if !oneOf(r["status"], "running", "completed", "failed") {
					return false
				}
			case "tools", "lanes":
				for _, k := range []string{"calls", "errors", "unknown_cost_calls"} {
					if !whole(r[k]) {
						return false
					}
				}
				if !number(r["recorded_cents"], true) {
					return false
				}
				if spec.key == "tools" {
					if !text(r["provider"], 100, false) || !text(r["tool"], 100, false) || !text(r["unit_kind"], 40, false) || !number(r["units"], false) || !whole(r["empty"]) || !whole(r["duration_ms"]) {
						return false
					}
				} else {
					if !text(r["title"], 1000, true) || !text(r["lane_id"], 36, true) || !text(r["status"], 40, true) {
						return false
					}
					for _, k := range []string{"companies", "screened", "passed", "graded_ab", "sent"} {
						if !whole(r[k]) {
							return false
						}
					}
				}
			case "rows":
				for _, k := range []string{"id", "run_id", "mission_id", "operation_id"} {
					if !text(r[k], 36, false) {
						return false
					}
				}
				for _, k := range []string{"lane_id", "company_id"} {
					if !text(r[k], 36, true) {
						return false
					}
				}
				for _, k := range []string{"agent", "phase", "tool"} {
					if !text(r[k], 100, false) {
						return false
					}
				}
				for _, k := range []string{"provider", "company", "grade", "qualification", "lane_title"} {
					if !text(r[k], 1000, true) {
						return false
					}
				}
				if !oneOf(r["status"], "ok", "error", "empty") || !oneOf(r["kind"], "tool", "screen", "grade", "lane") || !date(r["started_at"], false) || !whole(r["duration_ms"]) || !whole(r["attempt"]) || !number(r["cost_cents"], true) {
					return false
				}
				for _, k := range []string{"input", "output"} {
					if _, ok := r[k].(map[string]any); !ok || !safeEvidence(r[k], 0) {
						return false
					}
				}
			}
		}
	}
	return true
}
func oneOf(value any, choices ...string) bool {
	s, ok := value.(string)
	if !ok {
		return false
	}
	for _, choice := range choices {
		if s == choice {
			return true
		}
	}
	return false
}
func safeEvidence(value any, depth int) bool {
	if depth > 7 {
		return false
	}
	switch v := value.(type) {
	case nil, bool:
		return true
	case float64:
		return !math.IsInf(v, 0) && !math.IsNaN(v)
	case string:
		return len(v) <= 8000
	case []any:
		if len(v) > 12 {
			return false
		}
		for _, x := range v {
			if !safeEvidence(x, depth+1) {
				return false
			}
		}
		return true
	case map[string]any:
		if len(v) > 24 {
			return false
		}
		for k, x := range v {
			if len(k) > 60 {
				return false
			}
			for _, secret := range []string{"password", "secret", "authorization", "cookie", "api_key", "access_token"} {
				if strings.Contains(strings.ToLower(k), secret) {
					return false
				}
			}
			if !safeEvidence(x, depth+1) {
				return false
			}
		}
		return true
	}
	return false
}
