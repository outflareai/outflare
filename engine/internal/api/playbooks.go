package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

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
	read, run := auth.ScopeMissionsRead, auth.ScopeMissionsRun
	mux.HandleFunc("GET /v1/playbooks", s.listPlaybookCatalog)
	mux.HandleFunc("GET /v1/playbooks/{id}", s.getPlaybookCatalog)
	mux.HandleFunc("GET /v1/workspaces/{ws}/playbooks", s.inWorkspace(read, s.listPlaybooks))
	mux.HandleFunc("GET /v1/workspaces/{ws}/playbooks/{id}", s.inWorkspace(read, s.getPlaybook))
	mux.HandleFunc("POST /v1/workspaces/{ws}/playbooks/{id}/runs", s.inWorkspace(run, s.startPlaybook))
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
	rows, err := s.pool.Query(ctx, `SELECT playbook_id, count(*)::int FROM missions WHERE workspace_id = $1 AND playbook_id IS NOT NULL GROUP BY 1`, ws)
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

// startPlaybook creates a mission draft from a playbook. Required
// connections must be in place: a playbook never falls back to anyone
// else's account. Missing data doesn't stop it; the agent reports what it
// found.
func (s *Server) startPlaybook(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	p, ok := playbooks.Get(r.PathValue("id"))
	if !ok {
		writeError(w, r, http.StatusNotFound, "not_found", "no playbook "+r.PathValue("id")+"; GET /v1/workspaces/{ws}/playbooks lists them")
		return
	}
	var in playbookRunInput
	if !decode(w, r, &in) {
		return
	}
	for k, v := range in.Inputs {
		known := false
		for _, def := range p.Inputs {
			known = known || def.Name == k
		}
		if !known {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "this playbook has no input "+k)
			return
		}
		if len(v) > 500 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "inputs are up to 500 characters each")
			return
		}
	}
	for _, def := range p.Inputs {
		if !def.Optional && strings.TrimSpace(in.Inputs[def.Name]) == "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", def.Name+" is required ("+def.Label+")")
			return
		}
	}
	var kit playbooks.Kit
	if in.ApplyKit {
		var ok bool
		if kit, ok = playbooks.GetKit(p.Kit); !ok {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "this playbook has no setup kit, so apply_kit is not for it")
			return
		}
		if !a.has(auth.ScopeCatalogManage) {
			writeError(w, r, http.StatusForbidden, "forbidden", "applying the setup kit needs the catalog.manage scope; start without apply_kit, or ask someone who can")
			return
		}
	}
	ready, err := s.playbookReadiness(ctx, a.WorkspaceID, []playbooks.Playbook{p})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if missing := ready[0].Readiness.MissingConnections; len(missing) > 0 {
		labels := make([]string, 0, len(missing))
		for _, m := range missing {
			labels = append(labels, m.Label)
		}
		writeError(w, r, http.StatusConflict, "connector_needed", "connect "+strings.Join(labels, " and ")+" under Connectors first; this playbook runs only on the workspace's own accounts")
		return
	}
	name, goal := p.Title, p.Summary
	instructions, plan := playbookBrief(p, in.Inputs)
	mi := missionInput{Name: &name, Goal: &goal, Instructions: &instructions, Plan: &plan, Mode: in.Mode,
		TokenID: in.TokenID, MaxSpendCents: in.MaxSpendCents,
		playbookID: &p.ID, playbookVersion: &p.Version}
	if err := mi.validate(); s.missionError(w, r, err) {
		return
	}
	var id string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if in.ApplyKit {
			// The playbook takes every input its kit does (playbooks test).
			kitInputs := map[string]string{}
			for _, ki := range kit.Inputs {
				if v, ok := in.Inputs[ki.Name]; ok {
					kitInputs[ki.Name] = v
				}
			}
			if _, err = applyKit(ctx, tx, a, kit, kitInputs, kitFromCatalog); err != nil {
				return err
			}
		}
		if id, err = s.insertMission(ctx, tx, a, &mi); err != nil {
			return err
		}
		props := map[string]any{"playbook_id": p.ID, "playbook_version": p.Version, "mode": modeOr(in.Mode), "publisher": p.Publisher, "kit_applied": in.ApplyKit}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "playbook.started", "mission", id,
			map[string]any{"playbook_id": p.ID, "playbook_version": p.Version, "mission_id": id, "mode": modeOr(in.Mode)}, props)
	})
	if (errors.As(err, new(errKitConflict)) || errors.As(err, new(errInvalid)) || errors.As(err, new(errDenied))) && s.kitError(w, r, err) {
		return
	}
	if s.missionError(w, r, err) {
		return
	}
	v, err := s.loadMission(ctx, a.WorkspaceID, id)
	if s.missionError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func modeOr(m *string) string {
	if m == nil {
		return missions.ModeDryRun
	}
	return *m
}

// playbookBrief is what the agent reads: the filled-in prompt, the steps
// with the API operations (MCP tools) each one uses, and the guardrails.
func playbookBrief(p playbooks.Playbook, inputs map[string]string) (instructions, plan string) {
	var b strings.Builder
	b.WriteString(p.Render(inputs))
	fmt.Fprintf(&b, "\n\nThis mission runs the playbook %q, version %d (GET /v1/playbooks/%s).", p.ID, p.Version, p.ID)
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
