// Package leadportal reads the workspace's own Lead Portal reporting API.
package leadportal

import (
	"context"
	"encoding/json"
	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const ID = "leadportal"

type Connector struct{}

func (Connector) Definition() c.Definition {
	return c.Definition{ID: ID, Name: "Lead Portal", Category: c.CategoryData, BrandKind: c.KindBrand, Summary: "Sales discovery, research, verification, tool costs and mailbox capacity.", Description: "Read your Lead Portal's Sales report. Assign this connection to its one owning brand. Uses an admin API key stored in this workspace; no lead or mail writes.", Auth: c.AuthAPIKey, Fields: []c.Field{{Name: "base_url", Label: "Portal URL", Placeholder: "https://leads.company.example"}, {Name: "api_key", Label: "Admin API key", Secret: true, Help: "Create an API key on an admin account in your Lead Portal."}}, Capabilities: []string{"sales.reporting"}}
}
func (Connector) Read(ctx context.Context, cl *c.Client, creds c.Creds, q url.Values) (result map[string]any, err error) {
	read := "summary"
	switch {
	case q.Get("check") == "1":
		read = "check"
	case q.Has("trace"):
		read = "trace"
	case q.Has("lanes"):
		read = "lanes"
	case q.Has("lead_ids"):
		read = "origins"
	case q.Has("stages"):
		read = "leads"
	}
	started := time.Now()
	defer func() { metrics.SalesReportingRead(read, err == nil, time.Since(started)) }()
	base := strings.TrimRight(strings.TrimSpace(creds["base_url"]), "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimSpace(creds["api_key"]) == "" {
		return nil, c.Errf(c.ErrInvalid, "Enter the portal URL and its admin API key")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cl.Base(ID, base)+"/api/v1/reports/sales-reporting?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+creds["api_key"])
	req.Header.Set("Accept", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		return nil, c.Errf(c.ErrUnavailable, "Lead Portal could not be reached")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, c.HTTPError("Lead Portal", resp.StatusCode, "The reporting API refused the request; check the connection and reporting deployment")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil || len(raw) > 8<<20 {
		return nil, c.Errf(c.ErrUnavailable, "Lead Portal report is too large or incomplete")
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil || out == nil {
		return nil, c.Errf(c.ErrUnavailable, "Lead Portal returned an invalid report")
	}
	if q.Get("check") != "1" && !validReport(out, q) {
		return nil, c.Errf(c.ErrUnavailable, "Lead Portal returned an invalid reporting shape")
	}
	// The discovery block is an optional extra: a malformed one is dropped, the rest of the report stands.
	if _, summary := out["totals"]; summary && !validDiscovery(out["discovery"]) {
		out["discovery"] = nil
	}
	return out, nil
}
func (v Connector) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	out, err := v.Read(ctx, cl, creds, url.Values{"check": {"1"}})
	if err != nil {
		return c.Account{}, err
	}
	if out["schema_version"] != float64(1) {
		return c.Account{}, c.Errf(c.ErrInvalid, "Update the portal to reporting API version 1")
	}
	return c.Account{Name: "Lead Portal"}, nil
}

// Validate untrusted provider data before the API combines it with native counts.
func validReport(out map[string]any, q url.Values) bool {
	if q.Get("trace") == "1" {
		return validTrace(out["trace"])
	}
	if q.Has("lead_ids") {
		origins, ok := out["origins"].(map[string]any)
		if !ok || len(origins) > 100 {
			return false
		}
		for id, value := range origins {
			if !strings.Contains(","+q.Get("lead_ids")+",", ","+id+",") || !validAttribution(value) {
				return false
			}
		}
		return true
	}
	if q.Has("lanes") {
		return validLanes(out["lanes"])
	}
	counts := func(v any) bool {
		m, ok := v.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range m {
			if !strings.Contains(",discovered,researched,verified,emailed,replied,", ","+k+",") {
				return false
			}
			n, ok := v.(float64)
			if !ok || n < 0 || math.Trunc(n) != n {
				return false
			}
		}
		return true
	}
	if q.Has("stages") {
		groups, ok := out["groups"].([]any)
		if !ok {
			return false
		}
		for _, x := range groups {
			g, ok := x.(map[string]any)
			if !ok {
				return false
			}
			stage, ok := g["stage"].(string)
			if !ok || !strings.Contains(","+q.Get("stages")+",", ","+stage+",") {
				return false
			}
			n, ok := g["total"].(float64)
			if !ok || n < 0 {
				return false
			}
			leads, ok := g["leads"].([]any)
			if !ok || len(leads) > 50 {
				return false
			}
			for _, x := range leads {
				l, ok := x.(map[string]any)
				if !ok {
					return false
				}
				if value, exists := l["attribution"]; exists && !validAttribution(value) {
					return false
				}
				for _, key := range []string{"id", "company", "status", "at"} {
					if _, ok := l[key].(string); !ok {
						return false
					}
				}
			}
		}
		return true
	}
	if out["schema_version"] != float64(1) || !counts(out["totals"]) {
		return false
	}
	if value, exists := out["agents"]; exists {
		agents, ok := value.([]any)
		if !ok || len(agents) > 10000 {
			return false
		}
		seen := map[string]bool{}
		for _, value := range agents {
			row, ok := value.(map[string]any)
			if !ok || !validAttribution(row["attribution"]) || !counts(row["counts"]) {
				return false
			}
			id := row["attribution"].(map[string]any)["id"].(string)
			if seen[id] {
				return false
			}
			seen[id] = true
			for _, key := range []string{"intake", "cohort"} {
				if row[key] == nil {
					continue
				}
				stats, ok := row[key].(map[string]any)
				if !ok {
					return false
				}
				for _, value := range stats {
					n, ok := value.(float64)
					if !ok || n < 0 || math.Trunc(n) != n {
						return false
					}
				}
			}
		}
	}
	bs, ok := out["buckets"].([]any)
	if !ok || len(bs) > 8784 {
		return false
	}
	for _, x := range bs {
		b, ok := x.(map[string]any)
		if !ok || !counts(b["counts"]) {
			return false
		}
		at, ok := b["at"].(string)
		if !ok {
			return false
		}
		if _, e := time.Parse(time.RFC3339, at); e != nil {
			return false
		}
	}
	for _, key := range []string{"generated_at", "from", "to"} {
		v, ok := out[key].(string)
		if !ok {
			return false
		}
		if _, e := time.Parse(time.RFC3339, v); e != nil {
			return false
		}
	}
	return out["bucket"] == "hour" || out["bucket"] == "day"
}

// validDiscovery checks the optional Sales discovery block: counts and cents only, bounded lists and text.
func validDiscovery(value any) bool {
	if value == nil {
		return true
	}
	d, ok := value.(map[string]any)
	if !ok {
		return false
	}
	amount := func(v any) bool {
		if v == nil {
			return true
		}
		n, ok := v.(float64)
		return ok && n >= 0 && !math.IsInf(n, 0) && !math.IsNaN(n)
	}
	whole := func(v any) bool {
		n, ok := v.(float64)
		return ok && n >= 0 && math.Trunc(n) == n
	}
	text := func(v any, max int) bool {
		t, ok := v.(string)
		return ok && len(t) > 0 && len(t) <= max
	}
	counts := func(v any) bool {
		m, ok := v.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range m {
			if !strings.Contains(",found,new,passed,graded_ab,sent,", ","+k+",") || !whole(v) {
				return false
			}
		}
		return true
	}
	missions, ok := d["missions"].([]any)
	if !ok || len(missions) > 50 || !counts(d["totals"]) {
		return false
	}
	for _, x := range missions {
		m, ok := x.(map[string]any)
		if !ok || !text(m["id"], 100) || !text(m["name"], 200) || !text(m["state"], 40) || !amount(m["budget_cents"]) || !amount(m["spent_cents"]) {
			return false
		}
	}
	buckets, ok := d["buckets"].([]any)
	if !ok || len(buckets) > 8784 {
		return false
	}
	for _, x := range buckets {
		b, ok := x.(map[string]any)
		if !ok || !counts(b["counts"]) {
			return false
		}
		at, ok := b["at"].(string)
		if !ok {
			return false
		}
		if _, e := time.Parse(time.RFC3339, at); e != nil {
			return false
		}
	}
	lanes, ok := d["lanes"].(map[string]any)
	if !ok || !whole(lanes["total"]) {
		return false
	}
	statuses, ok := lanes["by_status"].(map[string]any)
	if !ok || len(statuses) > 20 {
		return false
	}
	for k, v := range statuses {
		if len(k) == 0 || len(k) > 40 || !whole(v) {
			return false
		}
	}
	rows, ok := lanes["rows"].([]any)
	if !ok || len(rows) > 50 {
		return false
	}
	for _, x := range rows {
		r, ok := x.(map[string]any)
		if !ok || !text(r["title"], 200) || !text(r["status"], 40) || !whole(r["found"]) || !whole(r["new"]) || !whole(r["passed"]) {
			return false
		}
		if r["method"] != nil && !text(r["method"], 40) {
			return false
		}
	}
	spend, ok := d["spend"].(map[string]any)
	if !ok || !amount(spend["window_cents"]) {
		return false
	}
	tools, ok := spend["rows"].([]any)
	if !ok || len(tools) > 50 {
		return false
	}
	for _, x := range tools {
		t, ok := x.(map[string]any)
		if !ok || !text(t["tool"], 100) || !amount(t["units"]) || !amount(t["cents"]) {
			return false
		}
	}
	return true
}

// validLanes checks a page of the discovery lane board: bounded text, whole counts and short recipe lists.
func validLanes(value any) bool {
	page, ok := value.(map[string]any)
	if !ok {
		return false
	}
	whole := func(v any) bool {
		n, ok := v.(float64)
		return ok && n >= 0 && math.Trunc(n) == n
	}
	short := func(v any, max int) bool { // Optional text.
		if v == nil {
			return true
		}
		t, ok := v.(string)
		return ok && len(t) <= max
	}
	rows, ok := page["rows"].([]any)
	if !ok || len(rows) > 50 || !whole(page["total"]) || !whole(page["offset"]) {
		return false
	}
	for _, x := range rows {
		r, ok := x.(map[string]any)
		if !ok {
			return false
		}
		for _, key := range []string{"id", "title", "status"} {
			if t, ok := r[key].(string); !ok || len(t) == 0 || len(t) > 200 {
				return false
			}
		}
		for _, key := range []string{"method", "origin", "segment", "idea", "why", "verdict", "learned", "last_activity_at", "created_at"} {
			if !short(r[key], 1300) {
				return false
			}
		}
		for _, key := range []string{"found", "new", "known", "screened", "passed"} {
			if !whole(r[key]) {
				return false
			}
		}
		for _, key := range []string{"ai_cents", "expected_size"} {
			if n, ok := r[key].(float64); r[key] != nil && (!ok || n < 0) {
				return false
			}
		}
		recipe, ok := r["recipe"].(map[string]any)
		if !ok || len(recipe) > 5 {
			return false
		}
		for key, list := range recipe {
			items, ok := list.([]any)
			if !ok || len(items) > 12 || !strings.Contains(",queries,urls,seeds,describe,jobs,", ","+key+",") {
				return false
			}
			for _, item := range items {
				if t, ok := item.(string); !ok || len(t) > 300 {
					return false
				}
			}
		}
	}
	return true
}

var attributionID = regexp.MustCompile(`^(worker|api_key|sales_agent|manual):[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func validAttribution(value any) bool {
	row, ok := value.(map[string]any)
	if !ok {
		return false
	}
	id, a := row["id"].(string)
	name, b := row["name"].(string)
	kind, c := row["kind"].(string)
	if !a || !b || !c || len(id) == 0 || len(id) > 100 || len(name) == 0 || len(name) > 200 {
		return false
	}
	if !strings.Contains(",worker,api_key,sales_agent,manual,unknown,", ","+kind+",") {
		return false
	}
	if kind == "unknown" && id != "unknown" || kind != "unknown" && (!strings.HasPrefix(id, kind+":") || !attributionID.MatchString(id)) {
		return false
	}
	for key := range row {
		switch key {
		case "id", "name", "kind", "submitted_at", "event_id", "revoked":
		default:
			return false
		}
	}
	if value := row["submitted_at"]; value != nil {
		at, ok := value.(string)
		if !ok {
			return false
		}
		if _, err := time.Parse(time.RFC3339, at); err != nil {
			return false
		}
	}
	if value := row["event_id"]; value != nil {
		if id, ok := value.(string); !ok || len(id) > 100 {
			return false
		}
	}
	if value := row["revoked"]; value != nil {
		if _, ok := value.(bool); !ok {
			return false
		}
	}
	return true
}
