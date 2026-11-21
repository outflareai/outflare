package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/leadportal"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Named credential identities only. Never return a key, hash or key prefix.
type salesAttribution struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Kind        string  `json:"kind"`
	Status      string  `json:"status,omitempty"`
	SubmittedAt *string `json:"submitted_at,omitempty"`
	EventID     *string `json:"event_id,omitempty"`
	Revoked     bool    `json:"revoked"`
}

func unknownSalesAttribution(status string) salesAttribution {
	return salesAttribution{ID: "unknown", Name: "Unknown", Kind: "unknown", Status: status}
}

// Source IDs are taken from already-authorized native conversations, never from a caller's
// arbitrary lookup. Batch reads also resolve historical imports without changing
// their CRM records or requiring the outbound bridge to be switched on.
func (s *Server) salesAttributions(ctx context.Context, a *access, ids []string, connection *connectors.Integration) map[string]salesAttribution {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	failed := func(phase string, count int, err error) {
		// The code says why, so a refused key or a spent quota (the
		// workspace's to fix) isn't taken for a portal that is down.
		ev := track.Event{Name: "sales.origin_lookup_failed", WorkspaceID: a.WorkspaceID, UserID: a.UserID, Props: map[string]any{"phase": phase, "requested": count}}
		kind := connectors.KindOf(err)
		if kind != connectors.ErrUnavailable {
			ev.Outcome = track.Failure
		}
		track.Fail(ctx, s.log, ev, "connector_"+kind, err)
	}
	out := map[string]salesAttribution{}
	unique := []string{}
	for _, id := range ids {
		if !validUUID(id) {
			continue
		}
		if _, exists := out[id]; !exists {
			out[id] = unknownSalesAttribution("unavailable")
			unique = append(unique, id)
		}
	}
	if len(unique) == 0 || s.integrations == nil {
		return out
	}
	if connection == nil {
		list, err := s.integrations.List(ctx, s.pool, a.WorkspaceID, "")
		if err != nil {
			failed("connections", len(unique), errors.New("Sales origin connections could not be read"))
			return out
		}
		for _, i := range list {
			if i.Connector == leadportal.ID && i.BrandID != nil && *i.BrandID == a.Brand {
				if connection != nil {
					return out
				} // Do not guess between source portals.
				v := i
				connection = &v
			}
		}
	}
	if connection == nil {
		return out
	}
	opened, err := s.integrations.Open(ctx, s.pool, a.WorkspaceID, connection.ID)
	if err != nil {
		failed("credentials", len(unique), errors.New("Sales origin connection could not be opened"))
		return out
	}
	jobs := make(chan []string, (len(unique)+99)/100)
	for start := 0; start < len(unique); start += 100 {
		jobs <- unique[start:min(start+100, len(unique))]
	}
	close(jobs)
	var mu sync.Mutex
	var workers sync.WaitGroup
	for range min(4, len(jobs)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for batch := range jobs {
				if ctx.Err() != nil {
					return
				}
				q := url.Values{"lead_ids": {strings.Join(batch, ",")}}
				result, err := (leadportal.Connector{}).Read(ctx, opened.Client, opened.Creds, q)
				if err != nil && connectors.KindOf(err) == connectors.ErrUnavailable && ctx.Err() == nil {
					// A portal that failed for a moment is asked once more.
					result, err = (leadportal.Connector{}).Read(ctx, opened.Client, opened.Creds, q)
				}
				if err != nil {
					failed("read", len(batch), err)
					continue
				}
				origins, _ := result["origins"].(map[string]any)
				mu.Lock()
				for _, id := range batch {
					origin := unknownSalesAttribution("unknown")
					if value, ok := origins[id]; ok {
						raw, _ := json.Marshal(value)
						if json.Unmarshal(raw, &origin) != nil {
							continue
						}
						if origin.ID != "unknown" {
							origin.Status = "known"
						}
					}
					out[id] = origin
				}
				mu.Unlock()
			}
		}()
	}
	workers.Wait()
	return out
}

func (s *Server) enrichSalesLeads(ctx context.Context, a *access, leads []salesLead) {
	ids := []string{}
	for _, l := range leads {
		if l.Source == "leads_portal" {
			ids = append(ids, l.SourceID)
		}
	}
	origins := s.salesAttributions(ctx, a, ids, nil)
	for i := range leads {
		origin := unknownSalesAttribution("unknown")
		if leads[i].Source == "leads_portal" {
			if found, ok := origins[leads[i].SourceID]; ok {
				origin = found
			}
		}
		leads[i].Attribution = &origin
	}
}

func (s *Server) enrichSalesReport(ctx context.Context, a *access, out, native map[string]any, connection *connectors.Integration, detail bool) {
	items := []map[string]any{}
	if detail {
		groups, _ := native["groups"].([]any)
		for _, group := range groups {
			leads, _ := group.(map[string]any)["leads"].([]any)
			for _, lead := range leads {
				items = append(items, lead.(map[string]any))
			}
		}
	} else {
		events, _ := native["source_outcomes"].([]any)
		for _, event := range events {
			items = append(items, event.(map[string]any))
		}
	}
	ids := []string{}
	for _, item := range items {
		if item["source"] == "leads_portal" {
			if id, ok := item["source_id"].(string); ok {
				ids = append(ids, id)
			}
		}
	}
	origins := s.salesAttributions(ctx, a, ids, connection)
	agents, _ := out["agents"].([]any)
	if agents == nil {
		agents = []any{}
	}
	if len(agents) == 0 {
		if counts, ok := out["totals"].(map[string]any); ok && len(counts) > 0 {
			copyCounts := map[string]any{}
			for stage, count := range counts {
				copyCounts[stage] = count
			}
			agents = append(agents, map[string]any{"attribution": map[string]any{"id": "unknown", "name": "Unknown", "kind": "unknown"}, "counts": copyCounts, "intake": nil, "cohort": nil})
		}
	}
	byID := map[string]map[string]any{}
	for _, item := range agents {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		origin, _ := row["attribution"].(map[string]any)
		if id, ok := origin["id"].(string); ok {
			byID[id] = row
		}
	}
	known, unavailable := 0, 0
	for _, item := range items {
		origin := unknownSalesAttribution("unknown")
		if item["source"] == "leads_portal" {
			if v, ok := origins[item["source_id"].(string)]; ok {
				origin = v
			}
		}
		item["attribution"] = origin
		delete(item, "source")
		delete(item, "source_id")
		if detail {
			continue
		}
		if origin.Status == "known" {
			known++
		}
		if origin.Status == "unavailable" {
			unavailable++
		}
		row := byID[origin.ID]
		if row == nil {
			row = map[string]any{"attribution": origin, "counts": map[string]any{}, "intake": nil, "cohort": nil}
			byID[origin.ID] = row
			agents = append(agents, row)
		}
		counts, _ := row["counts"].(map[string]any)
		if counts == nil {
			counts = map[string]any{}
			row["counts"] = counts
		}
		stage := item["stage"].(string)
		n, _ := counts[stage].(float64)
		counts[stage] = n + 1
	}
	if !detail {
		out["agents"] = agents
		out["attribution_coverage"] = map[string]int{"native_milestones": len(items), "known": known, "unavailable": unavailable}
	}
	delete(native, "source_outcomes")
}
