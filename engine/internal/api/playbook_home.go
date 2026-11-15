package api

import (
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/playbooks"
)

// A playbook's home in a brand: what its setup kit put there (rules,
// journeys, lists, signal types), whether each is on and what it has
// done, and the signals it raised with how they ended. It reads what the
// brand already keeps; nothing is stored for it.

// homePart is one thing the playbook's kit set up in the brand.
type homePart struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
	// ID is the part's id; a signal type has none.
	ID     *string `json:"id"`
	Name   string  `json:"name"`
	Status string  `json:"status"`
	// Count is what the part has done: a rule's firings in the last 30
	// days, the records now in a journey or on a list, a signal type's
	// open signals.
	Count int `json:"count"`
}

type playbookHome struct {
	PlaybookID string `json:"playbook_id"`
	KitID      string `json:"kit_id"`
	// Installed says the brand has at least one of the kit's parts.
	Installed bool       `json:"installed"`
	Parts     []homePart `json:"parts"`
	Signals   struct {
		Open int `json:"open"`
		Late int `json:"late"`
		// Closed in the last 90 days, by outcome.
		Won       int `json:"won"`
		NoChange  int `json:"no_change"`
		NotUseful int `json:"not_useful"`
	} `json:"signals"`
}

func (s *Server) routesPlaybookHome(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/playbooks/{id}/home", s.onBrand(auth.ScopeCustomersRead, s.getPlaybookHome))
}

func (s *Server) getPlaybookHome(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	p, ok := playbooks.Get(r.PathValue("id"))
	if !ok {
		writeError(w, r, http.StatusNotFound, "not_found", "no playbook "+r.PathValue("id")+"; GET /v1/playbooks lists them")
		return
	}
	out := playbookHome{PlaybookID: p.ID, KitID: p.Kit, Parts: []homePart{}}
	k, ok := playbooks.GetKit(p.Kit)
	if !ok {
		writeJSON(w, http.StatusOK, out)
		return
	}
	ctx := r.Context()
	ws, brand := a.WorkspaceID, a.Brand
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		add := func(kind, sql string, args ...any) error {
			rows, err := tx.Query(ctx, sql, args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				part := homePart{Kind: kind}
				if err := rows.Scan(&part.Key, &part.ID, &part.Name, &part.Status, &part.Count); err != nil {
					return err
				}
				out.Parts = append(out.Parts, part)
			}
			return rows.Err()
		}
		if err := add("rule", `SELECT x.kit_key, x.id::text, x.name, x.status,
				(SELECT count(*)::int FROM rule_firings f WHERE f.workspace_id = x.workspace_id AND f.rule_id = x.id AND f.fired_at > now() - interval '30 days')
			FROM rules x WHERE x.workspace_id = $1 AND x.brand_id = $2 AND x.kit_id = $3 AND x.status <> 'archived' ORDER BY x.name`, ws, brand, k.ID); err != nil {
			return err
		}
		var journeyIDs []string
		if err := add("journey", `SELECT x.kit_key, x.id::text, x.name, x.status,
				(SELECT count(*)::int FROM journey_runs n WHERE n.workspace_id = x.workspace_id AND n.journey_id = x.id AND n.status = 'active')
			FROM journeys x WHERE x.workspace_id = $1 AND x.brand_id = $2 AND x.kit_id = $3 AND x.status <> 'archived' ORDER BY x.name`, ws, brand, k.ID); err != nil {
			return err
		}
		for _, part := range out.Parts {
			if part.Kind == "journey" {
				journeyIDs = append(journeyIDs, *part.ID)
			}
		}
		if err := add("smart_list", `SELECT x.kit_key, x.id::text, x.name, x.kind,
				(SELECT count(*)::int FROM smart_list_members m WHERE m.workspace_id = x.workspace_id AND m.list_id = x.id)
			FROM smart_lists x WHERE x.workspace_id = $1 AND x.brand_id = $2 AND x.kit_id = $3 ORDER BY x.name`, ws, brand, k.ID); err != nil {
			return err
		}
		keys := make([]string, 0, len(k.SignalTypes))
		for _, t := range k.SignalTypes {
			keys = append(keys, t.Key)
		}
		if err := add("signal_type", `SELECT t.key, NULL::text, t.label, CASE WHEN t.archived_at IS NULL THEN 'active' ELSE 'archived' END,
				(SELECT count(*)::int FROM signals g WHERE g.workspace_id = t.workspace_id AND g.brand_id = t.brand_id AND g.type = t.key AND g.status IN ('open', 'accepted'))
			FROM signal_types t WHERE t.workspace_id = $1 AND t.brand_id = $2 AND t.key = ANY ($3) ORDER BY t.label`, ws, brand, keys); err != nil {
			return err
		}
		// Metrics and scores are the brand's by key: the count is how many
		// records have a value.
		metricKeys, scoreKeys := []string{}, []string{}
		for _, m := range k.Metrics {
			metricKeys = append(metricKeys, m.Key)
		}
		for _, sc := range k.Scores {
			scoreKeys = append(scoreKeys, sc.Key)
		}
		if len(metricKeys) > 0 {
			if err := add("metric", `SELECT d.key, d.id::text, d.name, 'active',
					(SELECT count(DISTINCT v.record_id)::int FROM metric_values v WHERE v.metric_id = d.id)
				FROM metric_definitions d WHERE d.workspace_id = $1 AND d.brand_id = $2 AND d.key = ANY ($3) AND d.status = 'active' ORDER BY d.name`, ws, brand, metricKeys); err != nil {
				return err
			}
		}
		if len(scoreKeys) > 0 {
			if err := add("score", `SELECT d.key, d.id::text, d.label, 'active',
					(SELECT count(*)::int FROM records x WHERE x.workspace_id = d.workspace_id AND x.brand_id = d.brand_id AND x.record_type = d.record_type
						AND x.attributes->'scores' ? d.key AND x.merged_into IS NULL AND x.archived_at IS NULL AND x.deleted_at IS NULL)
				FROM score_definitions d WHERE d.workspace_id = $1 AND d.brand_id = $2 AND d.key = ANY ($3) AND d.status = 'active' ORDER BY d.label`, ws, brand, scoreKeys); err != nil {
				return err
			}
		}
		out.Installed = len(out.Parts) > 0
		// Its signals: of the kit's own types, or raised by its journeys.
		return tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE g.status IN ('open', 'accepted'))::int,
				count(*) FILTER (WHERE g.status IN ('open', 'accepted') AND g.due_at < now())::int,
				count(*) FILTER (WHERE g.outcome = 'won' AND g.closed_at > now() - interval '90 days')::int,
				count(*) FILTER (WHERE g.outcome = 'no_change' AND g.closed_at > now() - interval '90 days')::int,
				count(*) FILTER (WHERE g.outcome = 'not_useful' AND g.closed_at > now() - interval '90 days')::int
			FROM signals g WHERE g.workspace_id = $1 AND g.brand_id = $2 AND (g.type = ANY ($3) OR g.data->>'journey_id' = ANY ($4))`,
			ws, brand, keys, journeyIDs).Scan(&out.Signals.Open, &out.Signals.Late, &out.Signals.Won, &out.Signals.NoChange, &out.Signals.NotUseful)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
