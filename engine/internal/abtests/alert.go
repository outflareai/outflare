package abtests

import (
	"context"

	"github.com/outflareai/outflare/engine/internal/alerts"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// Alerts the background check of running tests raises, once each per test
// (and per guardrail and version).
const (
	// AlertReady: a version is the winner, or the control is, so the test
	// is safe to stop.
	AlertReady = "ab_test.ready"
	// AlertSplit: the split between versions is broken.
	AlertSplit = "ab_test.split_broken"
	// AlertGuardrail: a version clearly hurts a guardrail.
	AlertGuardrail = "ab_test.guardrail_hurt"
)

func init() {
	alerts.Register(alerts.Kind{ID: AlertReady, Title: "A/B test ready",
		Description: "A running A/B test has a clear result and is safe to stop. It goes to the owners and admins.",
		Scope:       auth.ScopeAnalyticsRead, Describe: describe})
	alerts.Register(alerts.Kind{ID: AlertSplit, Title: "A/B test split broken",
		Description: "A running A/B test's visitors split far from the flag's weights, so its results can't be trusted until the cause is fixed.",
		Scope:       auth.ScopeAnalyticsRead, Describe: describe})
	alerts.Register(alerts.Kind{ID: AlertGuardrail, Title: "A/B test guardrail hurt",
		Description: "A version of a running A/B test clearly makes one of its guardrails worse.",
		Scope:       auth.ScopeAnalyticsRead, Describe: describe})
}

func describe(ctx context.Context, q jobs.Querier, a alerts.Row) (alerts.View, error) {
	var name string
	if err := q.QueryRow(ctx, `SELECT name FROM ab_tests WHERE id = $1 AND workspace_id = $2`, a.TargetID, a.WorkspaceID).Scan(&name); err != nil {
		return alerts.View{}, err
	}
	variant, _ := a.Data["variant"].(string)
	v := alerts.View{Path: "ab-tests"}
	switch a.Kind {
	case AlertReady:
		v.Title, v.Text = name+" is ready", "It has a clear result and is safe to stop. Open it to see the winner and end it."
		if variant != "" {
			v.Text = variant + " is the winner, and it is safe to stop. Open it to end it and ship the winner."
		}
	case AlertSplit:
		v.Title, v.Text = name+": the split is broken", "Visitors split far from the flag's weights, so there is no winner until the cause (a redirect, a cache, bots) is fixed."
	case AlertGuardrail:
		v.Title, v.Text = name+": a guardrail is hurt", variant+" clearly makes one of the test's guardrails worse. Weigh that before shipping it."
	}
	return v, nil
}
