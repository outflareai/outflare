package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/journeys"
	"github.com/outflareai/outflare/engine/internal/routing"
)

// Routing (docs/routing.md): the brand's ordered rules for which teammate
// gets a person or company that has no owner. internal/routing applies
// them.

func (s *Server) routesRouting(mux *http.ServeMux) {
	base := "/v1/brands/{brand}/routing"
	mux.HandleFunc("GET "+base, s.onBrand(auth.ScopeCustomersRead, s.getRouting))
	mux.HandleFunc("PUT "+base, s.onBrand(auth.ScopeCatalogManage, s.setRouting))
	rec := "/v1/brands/{brand}/records/{record}/routing"
	mux.HandleFunc("GET "+rec, s.onBrand(auth.ScopeCustomersRead, s.previewRouting))
	mux.HandleFunc("POST "+rec, s.onBrand(auth.ScopeCustomersManage, s.routeRecord))
}

// routingList is the brand's rules in order, with how many records have
// no owner.
func routingList(r *http.Request, tx pgx.Tx, a *access) (map[string]any, error) {
	ctx := r.Context()
	rows, err := tx.Query(ctx, `SELECT `+routing.Columns+` FROM routing_rules WHERE workspace_id = $1 AND brand_id = $2 ORDER BY position, created_at, id`,
		a.WorkspaceID, a.Brand)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, routing.Scan)
	if err != nil {
		return nil, err
	}
	var people, companies int
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE record_type = 'person'), count(*) FILTER (WHERE record_type = 'account') FROM records
		WHERE workspace_id = $1 AND brand_id = $2 AND record_type IN ('person', 'account') AND COALESCE(attributes->>'owner', '') = ''
			AND merged_into IS NULL AND archived_at IS NULL AND deleted_at IS NULL`, a.WorkspaceID, a.Brand).Scan(&people, &companies); err != nil {
		return nil, err
	}
	return map[string]any{"data": nonNil(list), "unowned": map[string]int{"person": people, "account": companies}}, nil
}

func (s *Server) getRouting(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var out map[string]any
	err := s.readTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		out, err = routingList(r, tx, a)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type routingRuleInput struct {
	ID         string               `json:"id"`
	Name       string               `json:"name"`
	RecordType string               `json:"record_type"`
	Conditions []journeys.Condition `json:"conditions"`
	Match      string               `json:"match"`
	Members    []string             `json:"members"`
	TeamID     *string              `json:"team_id"`
	Countries  []string             `json:"countries"`
	Status     string               `json:"status"`
}

// setRouting replaces the brand's rules, in the order given. A rule sent
// with its id keeps its count; one left out is removed.
func (s *Server) setRouting(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Rules *[]routingRuleInput `json:"rules"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Rules == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "rules is required: the brand's routing rules in order (an empty list removes them all)")
		return
	}
	if len(*in.Rules) > routing.MaxRules {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a brand keeps at most 50 routing rules")
		return
	}
	ctx := r.Context()
	var out map[string]any
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		keep := []string{}
		for i, x := range *in.Rules {
			rule := routing.Rule{ID: x.ID, Name: strings.TrimSpace(x.Name), RecordType: x.RecordType, Conditions: x.Conditions, Match: x.Match, Members: x.Members, TeamID: x.TeamID, Countries: x.Countries, Status: x.Status}
			if rule.Members == nil {
				rule.Members = []string{}
			}
			if rule.Countries == nil {
				rule.Countries = []string{}
			}
			for k, c := range rule.Countries {
				rule.Countries[k] = strings.ToUpper(strings.TrimSpace(c))
			}
			if rule.Match == "" {
				rule.Match = "all"
			}
			if rule.Status == "" {
				rule.Status = "active"
			}
			if rule.Conditions == nil {
				rule.Conditions = []journeys.Condition{}
			}
			at := "rule " + itoa(i+1) + ": "
			if msg := routing.Check(rule); msg != "" {
				return errInvalid(at + msg)
			}
			for _, m := range rule.Members {
				if !validUUID(m) {
					return errInvalid(at + "members are workspace members' user ids")
				}
			}
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM memberships WHERE workspace_id = $1 AND user_id::text = ANY($2::text[])`, a.WorkspaceID, rule.Members).Scan(&n); err != nil {
				return err
			}
			if n != len(rule.Members) {
				return errInvalid(at + "every member of the pool must be in the workspace, and listed once")
			}
			if rule.TeamID != nil {
				var ok bool
				if validUUID(*rule.TeamID) {
					if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM teams WHERE id = $1 AND workspace_id = $2)`, *rule.TeamID, a.WorkspaceID).Scan(&ok); err != nil {
						return err
					}
				}
				if !ok {
					return errInvalid(at + "team_id is the id of one of the workspace's teams")
				}
			}
			conds, err := json.Marshal(rule.Conditions)
			if err != nil {
				return err
			}
			var id string
			if rule.ID != "" {
				if !validUUID(rule.ID) {
					return errInvalid(at + "id is the rule's id from the list, or left out for a new rule")
				}
				err = tx.QueryRow(ctx, `UPDATE routing_rules SET position = $4, name = $5, record_type = $6, conditions = $7, match = $8, members = $9::uuid[], status = $10, team_id = $11, countries = $12, updated_at = now()
					WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 RETURNING id::text`,
					rule.ID, a.WorkspaceID, a.Brand, i, rule.Name, rule.RecordType, conds, rule.Match, rule.Members, rule.Status, rule.TeamID, rule.Countries).Scan(&id)
				if errors.Is(err, pgx.ErrNoRows) {
					return errInvalid(at + "no rule of this brand has that id")
				}
			} else {
				err = tx.QueryRow(ctx, `INSERT INTO routing_rules (workspace_id, brand_id, position, name, record_type, conditions, match, members, status, team_id, countries)
					VALUES ($1, $2, $3, $4, $5, $6, $7, $8::uuid[], $9, $10, $11) RETURNING id::text`,
					a.WorkspaceID, a.Brand, i, rule.Name, rule.RecordType, conds, rule.Match, rule.Members, rule.Status, rule.TeamID, rule.Countries).Scan(&id)
			}
			if err != nil {
				return err
			}
			keep = append(keep, id)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM routing_rules WHERE workspace_id = $1 AND brand_id = $2 AND NOT (id::text = ANY($3::text[]))`, a.WorkspaceID, a.Brand, keep); err != nil {
			return err
		}
		// Every record with no owner is looked at again under the new rules.
		if err := routing.Reset(ctx, tx, a.WorkspaceID, a.Brand); err != nil {
			return err
		}
		var err error
		if out, err = routingList(r, tx, a); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "routing.updated", "brand", a.Brand, map[string]any{"rules": len(keep)})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// previewRouting says which rule matches a record and who would get it,
// changing nothing.
func (s *Server) previewRouting(w http.ResponseWriter, r *http.Request) {
	s.routeOne(w, r, true, true)
}

// routeRecord gives a record to the member the rules pick.
func (s *Server) routeRecord(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Replace bool `json:"replace"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	s.routeOne(w, r, in.Replace, false)
}

func (s *Server) routeOne(w http.ResponseWriter, r *http.Request, replace, dry bool) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	id := r.PathValue("record")
	var res routing.Result
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var ok bool
		if validUUID(id) {
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM records WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 AND record_type IN ('person', 'account')
				AND merged_into IS NULL AND archived_at IS NULL AND deleted_at IS NULL)`, id, a.WorkspaceID, a.Brand).Scan(&ok); err != nil {
				return err
			}
		}
		if !ok {
			return errNotFound
		}
		var err error
		if res, err = routing.Route(ctx, tx, a.WorkspaceID, id, nil, replace, dry); err != nil {
			return err
		}
		if dry || res.Outcome != "assigned" {
			return nil
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "record.routed", "record", id, map[string]any{"record_id": id, "rule_id": res.RuleID, "owner_id": res.OwnerID})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, res)
}
