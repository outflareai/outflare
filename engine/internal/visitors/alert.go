package visitors

import (
	"context"
	"fmt"

	"github.com/outflareai/outflare/engine/internal/alerts"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// AlertHot is the alert for a person or company whose visits to the
// website reached the workspace's hot score.
const AlertHot = "visitor.hot"

func init() {
	alerts.Register(alerts.Kind{ID: AlertHot, Title: "Hot website visitors",
		Description: "A known person or company is visiting the website a lot, at most once a week each. It goes to the owners and admins.",
		Scope:       auth.ScopeCustomersRead, Describe: describeHot})
}

func describeHot(ctx context.Context, q jobs.Querier, a alerts.Row) (alerts.View, error) {
	var typ, name string
	if err := q.QueryRow(ctx, `SELECT record_type, name FROM records WHERE id = $1 AND workspace_id = $2`,
		a.TargetID, a.WorkspaceID).Scan(&typ, &name); err != nil {
		return alerts.View{}, err
	}
	if name == "" {
		name = "Someone without a name yet"
		if typ == "account" {
			name = "A company without a name yet"
		}
	}
	score, _ := a.Data["score"].(float64)
	return alerts.View{Title: name + " is hot on your website",
		Text: fmt.Sprintf("Their visits scored %.0f. See which pages they read.", score), Path: "visitors"}, nil
}
