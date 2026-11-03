package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/missions"
	"github.com/outflareai/outflare/engine/internal/playbooks"
)

// Playbooks: ready-made recipes from the repository's playbooks/ folder.
// The list is public, so the website and anyone's AI app can read it. A
// workspace sees which ones it is ready for, and starting one creates a
// mission draft with the playbook's prompt, so nothing runs before a person
// approves it.

func (s *Server) routesPlaybooks(mux *http.ServeMux) {
	// Playbooks are the agent's: reading them is agents.read and running
	// one is agents.run. Starting one as a mission is retired (410); its
	// route keeps the scope it had until it is removed.
	read, run := auth.ScopeAgentsRead, auth.ScopeAgentsRun
	mux.HandleFunc("GET /v1/playbooks", s.listPlaybookCatalog)
	mux.HandleFunc("GET /v1/playbooks/{id}", s.getPlaybookCatalog)
	mux.HandleFunc("GET /v1/workspaces/{ws}/playbooks", s.inWorkspace(read, s.listPlaybooks))
	mux.HandleFunc("GET /v1/workspaces/{ws}/playbooks/{id}", s.inWorkspace(read, s.getPlaybook))
	mux.HandleFunc("POST /v1/workspaces/{ws}/playbooks/{id}/runs", s.inWorkspace(auth.ScopeMissionsRun, s.startPlaybook))
	// Start a playbook as an agent run on the action layer (agent_runs.go).
	mux.HandleFunc("POST /v1/workspaces/{ws}/playbooks/{id}/agent-runs", s.inBrand(run, s.runPlaybook))
}

// playbookSource is where a playbook's file lives on GitHub.
const playbookSource = "https://github.com/outflareai/outflare/blob/main/playbooks/%s/playbook.yaml"

type playbookView struct {
	playbooks.Playbook
	SourceURL string `json:"source_url"`
}

type workspacePlaybookView struct {
	playbookView
	Readiness playbooks.Readiness `json:"readiness"`
	// Missions started from this playbook in the workspace.
	Runs int `json:"runs"`
}

func viewPlaybook(p playbooks.Playbook) playbookView {
	return playbookView{Playbook: p, SourceURL: fmt.Sprintf(playbookSource, p.ID)}
}

func (s *Server) shippedPlaybooks(w http.ResponseWriter, r *http.Request) ([]playbooks.Playbook, bool) {
	list, err := playbooks.All()
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	return list, true
}

func (s *Server) listPlaybookCatalog(w http.ResponseWriter, r *http.Request) {
	list, ok := s.shippedPlaybooks(w, r)
	if !ok {
		return
	}
	out := make([]playbookView, 0, len(list))
	for _, p := range list {
		out = append(out, viewPlaybook(p))
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (s *Server) getPlaybookCatalog(w http.ResponseWriter, r *http.Request) {
	p, ok := playbooks.Get(r.PathValue("id"))
	if !ok {
		writeError(w, r, http.StatusNotFound, "not_found", "no playbook "+r.PathValue("id")+"; GET /v1/playbooks lists them")
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, viewPlaybook(p))
}

// readiness works out, for every playbook in list, whether workspace ws can
// run it with its own connections and data, and how often it has.
func (s *Server) playbookReadiness(ctx context.Context, ws string, list []playbooks.Playbook) ([]workspacePlaybookView, error) {
	var connected []playbooks.ConnectorInfo
	if s.integrations != nil {
		rows, err := s.pool.Query(ctx, `SELECT DISTINCT connector FROM integrations WHERE workspace_id = $1`, ws)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			if d, ok := s.integrations.Registry.Definition(id); ok {
				connected = append(connected, playbooks.ConnectorInfo{Category: d.Category, Capabilities: d.Capabilities})
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	have, err := playbooks.HaveData(ctx, s.pool, ws, list)
	if err != nil {
		return nil, err
	}
	runs := map[string]int{}
	rows, err := s.pool.Query(ctx, `SELECT playbook_id, sum(n)::int FROM (
		SELECT playbook_id, count(*) AS n FROM agent_runs WHERE workspace_id = $1 AND playbook_id IS NOT NULL GROUP BY 1
		UNION ALL SELECT playbook_id, count(*) FROM missions WHERE workspace_id = $1 AND playbook_id IS NOT NULL GROUP BY 1) t GROUP BY 1`, ws)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			rows.Close()
			return nil, err
		}
		runs[id] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]workspacePlaybookView, 0, len(list))
	for _, p := range list {
		out = append(out, workspacePlaybookView{playbookView: viewPlaybook(p), Readiness: p.Check(connected, have), Runs: runs[p.ID]})
	}
	return out, nil
}

func (s *Server) listPlaybooks(w http.ResponseWriter, r *http.Request) {
	list, ok := s.shippedPlaybooks(w, r)
	if !ok {
		return
	}
	out, err := s.playbookReadiness(r.Context(), accessFrom(r.Context()).WorkspaceID, list)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (s *Server) getPlaybook(w http.ResponseWriter, r *http.Request) {
	p, ok := playbooks.Get(r.PathValue("id"))
	if !ok {
		writeError(w, r, http.StatusNotFound, "not_found", "no playbook "+r.PathValue("id")+"; GET /v1/workspaces/{ws}/playbooks lists them")
		return
	}
	out, err := s.playbookReadiness(r.Context(), accessFrom(r.Context()).WorkspaceID, []playbooks.Playbook{p})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out[0])
}

type playbookRunInput struct {
	Inputs        map[string]string `json:"inputs"`
	Mode          *string           `json:"mode"`
	TokenID       optional[string]  `json:"token_id"`
	MaxSpendCents optional[int64]   `json:"max_spend_cents"`
	// ApplyKit sets up the playbook's kit in the same step (catalog.manage,
	// and emails.manage when it adds email drafts).
	ApplyKit bool `json:"apply_kit"`
}

// startPlaybook is retired: a playbook started by hand is an agent run
// (runPlaybook, POST .../playbooks/{id}/agent-runs). It answers 410 so an
// agent or script still posting here is told where to go.
func (s *Server) startPlaybook(w http.ResponseWriter, r *http.Request) {
	if _, ok := playbooks.Get(r.PathValue("id")); !ok {
		writeError(w, r, http.StatusNotFound, "not_found", "no playbook "+r.PathValue("id")+"; GET /v1/workspaces/{ws}/playbooks lists them")
		return
	}
	writeError(w, r, http.StatusGone, "mission_retired", "starting a playbook as a mission is retired: start it as an agent run with POST /v1/workspaces/{ws}/playbooks/"+r.PathValue("id")+"/agent-runs (the MCP tool run_playbook), with the same inputs, mode and apply_kit")
}

// checkPlaybookInputs checks a playbook's inputs: known names, up to 500
// characters each, and every required one given. It answers the message
// for the person, or "".
func checkPlaybookInputs(p playbooks.Playbook, inputs map[string]string) string {
	for k, v := range inputs {
		known := false
		for _, def := range p.Inputs {
			known = known || def.Name == k
		}
		if !known {
			return "this playbook has no input " + k
		}
		if len(v) > 500 {
			return "inputs are up to 500 characters each"
		}
	}
	for _, def := range p.Inputs {
		if !def.Optional && strings.TrimSpace(inputs[def.Name]) == "" {
			return def.Name + " is required (" + def.Label + ")"
		}
	}
	return ""
}

func modeOr(m *string) string {
	if m == nil {
		return missions.ModeDryRun
	}
	return *m
}

// playbookBrief is what the agent reads: the filled-in prompt, the steps
// with the API operations (MCP tools) each one uses, and the guardrails.
func playbookBrief(p playbooks.Playbook, inputs map[string]string, asRun bool) (instructions, plan string) {
	var b strings.Builder
	b.WriteString(p.Render(inputs))
	what := "This mission runs"
	if asRun {
		what = "This run carries out"
	}
	fmt.Fprintf(&b, "\n\n%s the playbook %q, version %d (GET /v1/playbooks/%s).", what, p.ID, p.Version, p.ID)
	if p.Kit != "" {
		fmt.Fprintf(&b, " It works on what its setup kit %q adds (GET /v1/kits/%s): check GET /v1/workspaces/{ws}/kits/%s/preview, and if anything is still to add, ask a person to apply it rather than building it yourself.", p.Kit, p.Kit, p.Kit)
	}
	if len(p.Guardrails) > 0 {
		b.WriteString("\n\nGuardrails:")
		for _, g := range p.Guardrails {
			b.WriteString("\n- " + g)
		}
	}
	if len(p.Measure) > 0 {
		b.WriteString("\n\nWhen you report, say how these moved:")
		for _, m := range p.Measure {
			b.WriteString("\n- " + m.Metric + ": " + m.How)
		}
	}
	var pl strings.Builder
	for i, st := range p.Steps {
		fmt.Fprintf(&pl, "%d. %s", i+1, st.Say)
		if len(st.Tools) > 0 {
			pl.WriteString(" (" + strings.Join(st.Tools, ", ") + ")")
		}
		pl.WriteString("\n")
	}
	return b.String(), strings.TrimSpace(pl.String())
}
