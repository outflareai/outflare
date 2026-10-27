package playbooks

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Readiness is whether a workspace can run a playbook now, and if not,
// what it should add first.
type Readiness struct {
	Ready bool `json:"ready"`
	// Required connections the workspace hasn't connected, and optional
	// ones that would make the playbook better.
	MissingConnections []ConnectionNeed `json:"missing_connections"`
	MissingOptional    []ConnectionNeed `json:"missing_optional"`
	// Data the workspace doesn't collect yet.
	MissingData []DataNeed `json:"missing_data"`
}

// Check says whether a workspace with the connected connectors and the
// data in have can run p. Bring your own keys: only the workspace's own
// connections count, so a missing one keeps the playbook from starting.
func (p Playbook) Check(connected []ConnectorInfo, have map[string]bool) Readiness {
	r := Readiness{MissingConnections: []ConnectionNeed{}, MissingOptional: []ConnectionNeed{}, MissingData: []DataNeed{}}
	for _, n := range p.Needs.Connections {
		found := false
		for _, c := range connected {
			if n.matches(c) {
				found = true
				break
			}
		}
		switch {
		case found:
		case n.Optional:
			r.MissingOptional = append(r.MissingOptional, n)
		default:
			r.MissingConnections = append(r.MissingConnections, n)
		}
	}
	for _, d := range p.Needs.Data {
		if !have[d.Check] {
			r.MissingData = append(r.MissingData, d)
		}
	}
	r.Ready = len(r.MissingConnections) == 0 && len(r.MissingData) == 0
	return r
}

// dataQueries answer each data check with one cheap EXISTS, always for one
// workspace.
var dataQueries = map[string]string{
	"records":         `SELECT EXISTS (SELECT 1 FROM records WHERE workspace_id = $1)`,
	"customer_events": `SELECT EXISTS (SELECT 1 FROM customer_events WHERE workspace_id = $1)`,
	"inbox":           `SELECT EXISTS (SELECT 1 FROM inbound_messages WHERE workspace_id = $1)`,
	"site_tracking":   `SELECT EXISTS (SELECT 1 FROM site_sessions WHERE workspace_id = $1)`,
	"seo_site":        `SELECT EXISTS (SELECT 1 FROM seo_sites WHERE workspace_id = $1)`,
	"mailboxes":       `SELECT EXISTS (SELECT 1 FROM mailboxes WHERE workspace_id = $1)`,
	"leads":           `SELECT EXISTS (SELECT 1 FROM leads WHERE workspace_id = $1)`,
}

// Querier is a pool or a transaction.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// HaveData answers the data checks the playbooks use for workspace ws.
func HaveData(ctx context.Context, q Querier, ws string, list []Playbook) (map[string]bool, error) {
	have := map[string]bool{}
	for _, p := range list {
		for _, d := range p.Needs.Data {
			if _, done := have[d.Check]; done {
				continue
			}
			var ok bool
			if err := q.QueryRow(ctx, dataQueries[d.Check], ws).Scan(&ok); err != nil {
				return nil, err
			}
			have[d.Check] = ok
		}
	}
	return have, nil
}
