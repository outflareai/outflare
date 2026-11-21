package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

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
	mux.HandleFunc("PUT "+base+"/people", s.onBrand(auth.ScopeCatalogManage, s.setRoutingPeople))
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
	who, err := routingPeople(ctx, tx, a)
	if err != nil {
		return nil, err
	}
	return map[string]any{"data": nonNil(list), "unowned": map[string]int{"person": people, "account": companies}, "people": who}, nil
}

// routingPerson is what routing knows about one teammate in a brand:
// when they work, how many records they take, and whether they are away.
type routingPerson struct {
	UserID   string `json:"user_id"`
	Capacity *int   `json:"capacity"`
	// Days are 1 (Monday) to 7 (Sunday); none means every day.
	Days []int `json:"days"`
	// From and To are HH:MM in Timezone; both null means all day.
	From     *string `json:"from"`
	To       *string `json:"to"`
	Timezone string  `json:"timezone"`
	Paused   bool    `json:"paused"`
	// Owned is how many of the brand's people and companies they own now,
	// and AtWork whether their hours cover this moment.
	Owned  int  `json:"owned"`
	AtWork bool `json:"at_work"`
}

func clock(minute *int) *string {
	if minute == nil {
		return nil
	}
	v := fmt.Sprintf("%02d:%02d", *minute/60, *minute%60)
	return &v
}

// minuteOf reads HH:MM; 24:00 is allowed as an end.
func minuteOf(v string, end bool) (int, bool) {
	t, err := time.Parse("15:04", v)
	if err != nil {
		if end && v == "24:00" {
			return 1440, true
		}
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

// routingPeople lists the teammates with hours, a capacity or away set
// in the brand, by user id.
func routingPeople(ctx context.Context, tx pgx.Tx, a *access) ([]routingPerson, error) {
	rows, err := tx.Query(ctx, `SELECT p.user_id::text, p.capacity, p.days::int[], p.from_minute::int, p.to_minute::int, p.timezone, p.paused,
			(SELECT count(*) FROM records r WHERE r.workspace_id = p.workspace_id AND r.brand_id = p.brand_id AND r.attributes->>'owner' = p.user_id::text
				AND r.record_type IN ('person', 'account') AND r.archived_at IS NULL AND r.merged_into IS NULL AND r.deleted_at IS NULL),
			`+routing.AtWork+`
		FROM routing_people p WHERE p.workspace_id = $1 AND p.brand_id = $2 ORDER BY p.user_id`, a.WorkspaceID, a.Brand)
	if err != nil {
		return nil, err
	}
	out := []routingPerson{}
	var p routingPerson
	var from, to *int
	_, err = pgx.ForEachRow(rows, []any{&p.UserID, &p.Capacity, &p.Days, &from, &to, &p.Timezone, &p.Paused, &p.Owned, &p.AtWork}, func() error {
		x := p
		x.Days = append([]int{}, p.Days...)
		x.From, x.To = clock(from), clock(to)
		out = append(out, x)
		return nil
	})
	return out, err
}

// setRoutingPeople replaces the brand's hours, capacity and away
// settings. Someone left out goes back to always at work with no limit.
func (s *Server) setRoutingPeople(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		People *[]struct {
			UserID   string  `json:"user_id"`
			Capacity *int    `json:"capacity"`
			Days     []int   `json:"days"`
			From     *string `json:"from"`
			To       *string `json:"to"`
			Timezone string  `json:"timezone"`
			Paused   bool    `json:"paused"`
		} `json:"people"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.People == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "people is required: every teammate with hours, a capacity or away set (an empty list clears them all)")
		return
	}
	if len(*in.People) > 500 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "at most 500 people")
		return
	}
	ctx := r.Context()
	var out map[string]any
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM routing_people WHERE workspace_id = $1 AND brand_id = $2`, a.WorkspaceID, a.Brand); err != nil {
			return err
		}
		seen := map[string]bool{}
		limited, away := 0, 0
		for i, p := range *in.People {
			at := "person " + itoa(i+1) + ": "
			if !validUUID(p.UserID) || seen[p.UserID] {
				return errInvalid(at + "user_id is a workspace member's user id, once")
			}
			seen[p.UserID] = true
			var member bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM memberships WHERE workspace_id = $1 AND user_id = $2)`, a.WorkspaceID, p.UserID).Scan(&member); err != nil {
				return err
			}
			if !member {
				return errInvalid(at + "nobody in this workspace has that user id")
			}
			if p.Capacity != nil && (*p.Capacity < 0 || *p.Capacity > 1000000) {
				return errInvalid(at + "capacity is 0 to 1,000,000, or null for no limit")
			}
			days := []int{}
			for _, d := range p.Days {
				if d < 1 || d > 7 {
					return errInvalid(at + "days are 1 (Monday) to 7 (Sunday)")
				}
				dup := false
				for _, have := range days {
					dup = dup || have == d
				}
				if !dup {
					days = append(days, d)
				}
			}
			sort.Ints(days)
			var from, to *int
			if (p.From == nil) != (p.To == nil) {
				return errInvalid(at + "send both from and to, or neither for all day")
			}
			if p.From != nil {
				f, ok1 := minuteOf(*p.From, false)
				t, ok2 := minuteOf(*p.To, true)
				if !ok1 || !ok2 || f == t {
					return errInvalid(at + "from and to are times such as 09:00 and 17:30, and differ")
				}
				from, to = &f, &t
			}
			zone := strings.TrimSpace(p.Timezone)
			if zone == "" {
				zone = "UTC"
			}
			if _, err := time.LoadLocation(zone); err != nil || len(zone) > 64 || zone == "Local" {
				return errInvalid(at + "timezone is a name such as Europe/Lisbon or America/New_York")
			}
			if p.Capacity != nil {
				limited++
			}
			if p.Paused {
				away++
			}
			if _, err := tx.Exec(ctx, `INSERT INTO routing_people (workspace_id, brand_id, user_id, capacity, days, from_minute, to_minute, timezone, paused)
				VALUES ($1, $2, $3, $4, $5::smallint[], $6, $7, $8, $9)`, a.WorkspaceID, a.Brand, p.UserID, p.Capacity, days, from, to, zone, p.Paused); err != nil {
				return err
			}
		}
		// Someone with room again may take what was left without an owner.
		if err := routing.Reset(ctx, tx, a.WorkspaceID, a.Brand); err != nil {
			return err
		}
		var err error
		if out, err = routingList(r, tx, a); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "routing.people_updated", "brand", a.Brand, map[string]any{"people": len(*in.People), "with_capacity": limited, "away": away})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
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
