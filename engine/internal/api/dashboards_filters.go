package api

import (
	"encoding/json"
	"strconv"

	"github.com/outflareai/outflare/engine/internal/insights"
)

// maxDashboardFilters is how many filters a dashboard can put on its tiles.
const maxDashboardFilters = 5

func checkDashboardFilters(fs []insights.Filter) error {
	if len(fs) > maxDashboardFilters {
		return errInvalid("settings.filters has at most " + strconv.Itoa(maxDashboardFilters) + " filters")
	}
	for i, f := range fs {
		if err := insights.ValidEventFilter(f, "settings.filters["+strconv.Itoa(i)+"]"); err != nil {
			return errInvalid(err.Error())
		}
	}
	return nil
}

// withDashboardFilters adds a dashboard's filters to every event and page
// view part of a tile's spec: each series of a trend, each step of a
// funnel, the start and return of retention and the step of lifecycle.
// Visits, payments, conversions and ad numbers have other fields, so
// they and paths keep their own filters only. The web app does the same
// (web/src/lib/insight-embed.ts, withFilters).
func withDashboardFilters(kind string, spec []byte, filters []insights.Filter) ([]byte, error) {
	if len(filters) == 0 {
		return spec, nil
	}
	var m map[string]any
	if err := json.Unmarshal(spec, &m); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(filters)
	if err != nil {
		return nil, err
	}
	add := func(part any) {
		p, ok := part.(map[string]any)
		if !ok {
			return
		}
		if k, _ := p["kind"].(string); k != insights.KindEvent && k != insights.KindPage {
			return
		}
		var extra []any
		_ = json.Unmarshal(raw, &extra)
		cur, _ := p["filters"].([]any)
		p["filters"] = append(append([]any{}, cur...), extra...)
	}
	each := func(v any) {
		list, _ := v.([]any)
		for _, x := range list {
			add(x)
		}
	}
	switch kind {
	case "trend":
		each(m["series"])
	case "funnel":
		each(m["steps"])
	case "retention":
		add(m["start"])
		add(m["return"])
	case "lifecycle", "stickiness":
		add(m["step"])
	}
	return json.Marshal(m)
}
