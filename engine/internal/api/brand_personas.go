package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Personas are the kinds of people a brand sells to: a part of the brand
// profile (brand_profile.go) with the same two rules. Only a person
// confirms one, and none is erased: a persona is archived, and every
// change is in the profile's history. A persona is a type, never a real
// person; real people live in customer records.

const maxPersonas = 50

var personaStatuses = []string{"suggested", "confirmed", "archived"}

type personaView struct {
	ID          string     `json:"id"`
	BrandID     string     `json:"brand_id"`
	Name        string     `json:"name"`
	Role        string     `json:"role"`
	Summary     string     `json:"summary"`
	Goals       []string   `json:"goals"`
	Pains       []string   `json:"pains"`
	Triggers    []string   `json:"triggers"`
	Objections  []string   `json:"objections"`
	Channels    []string   `json:"channels"`
	Source      string     `json:"source"`
	Status      string     `json:"status"`
	ConfirmedBy *string    `json:"confirmed_by"`
	ConfirmedAt *time.Time `json:"confirmed_at"`
	CreatedBy   *string    `json:"created_by"`
	UpdatedBy   *string    `json:"updated_by"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

const personaSelect = `SELECT p.id, p.brand_id, p.name, p.role, p.summary, p.goals, p.pains, p.triggers, p.objections, p.channels,
		p.source, p.status, cf.display_name, p.confirmed_at, cr.display_name, up.display_name, p.created_at, p.updated_at
	FROM brand_personas p
	LEFT JOIN actors cf ON cf.id = p.confirmed_by
	LEFT JOIN actors cr ON cr.id = p.created_by
	LEFT JOIN actors up ON up.id = p.updated_by
	WHERE p.workspace_id = $1 AND p.brand_id = $2`

func scanPersona(row pgx.CollectableRow) (personaView, error) {
	var v personaView
	var lists [5][]byte
	err := row.Scan(&v.ID, &v.BrandID, &v.Name, &v.Role, &v.Summary, &lists[0], &lists[1], &lists[2], &lists[3], &lists[4],
		&v.Source, &v.Status, &v.ConfirmedBy, &v.ConfirmedAt, &v.CreatedBy, &v.UpdatedBy, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return v, err
	}
	for i, dst := range []*[]string{&v.Goals, &v.Pains, &v.Triggers, &v.Objections, &v.Channels} {
		if err := json.Unmarshal(lists[i], dst); err != nil {
			return v, err
		}
		if *dst == nil {
			*dst = []string{}
		}
	}
	return v, nil
}

// loadPersonas lists a brand's personas in the page's order. With
// archived false, archived ones are left out.
func loadPersonas(ctx context.Context, q profileQuerier, ws, brand string, archived bool) ([]personaView, error) {
	rows, err := q.Query(ctx, personaSelect+` AND ($3 OR p.status <> 'archived') ORDER BY p.position, p.created_at, p.id`, ws, brand, archived)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, scanPersona)
	if list == nil {
		list = []personaView{}
	}
	return list, err
}

func loadPersona(ctx context.Context, q profileQuerier, ws, brand, id string) (personaView, error) {
	if !validUUID(id) {
		return personaView{}, errNotFound
	}
	rows, err := q.Query(ctx, personaSelect+` AND p.id = $3`, ws, brand, id)
	if err != nil {
		return personaView{}, err
	}
	list, err := pgx.CollectRows(rows, scanPersona)
	if err != nil {
		return personaView{}, err
	}
	if len(list) == 0 {
		return personaView{}, errNotFound
	}
	return list[0], nil
}

func (s *Server) listPersonas(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	status := r.URL.Query().Get("status")
	if status != "" && status != "all" && !slices.Contains(personaStatuses, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is suggested, confirmed, archived or all")
		return
	}
	list, err := loadPersonas(r.Context(), s.pool, a.WorkspaceID, a.Brand, status == "all" || status == "archived")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if status != "" && status != "all" {
		list = slices.DeleteFunc(list, func(p personaView) bool { return p.Status != status })
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) personaError(w http.ResponseWriter, r *http.Request, err error) bool {
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "persona not found")
		return true
	}
	return s.allocError(w, r, err)
}

func (s *Server) getPersona(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := loadPersona(r.Context(), s.pool, a.WorkspaceID, a.Brand, r.PathValue("id"))
	if s.personaError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type personaInput struct {
	Name       *string   `json:"name"`
	Role       *string   `json:"role"`
	Summary    *string   `json:"summary"`
	Goals      *[]string `json:"goals"`
	Pains      *[]string `json:"pains"`
	Triggers   *[]string `json:"triggers"`
	Objections *[]string `json:"objections"`
	Channels   *[]string `json:"channels"`
	Source     *string   `json:"source"`
	Status     *string   `json:"status"`
}

func (in *personaInput) lists() map[string]*[]string {
	return map[string]*[]string{"goals": in.Goals, "pains": in.Pains, "triggers": in.Triggers, "objections": in.Objections, "channels": in.Channels}
}

func (in *personaInput) validate() error {
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if *in.Name == "" || len(*in.Name) > 100 {
			return errInvalid("name must be 1 to 100 characters, such as \"Head of growth at a small software company\"")
		}
	}
	if in.Role != nil {
		*in.Role = strings.TrimSpace(*in.Role)
		if len(*in.Role) > 200 {
			return errInvalid("role is at most 200 characters")
		}
	}
	if in.Summary != nil {
		*in.Summary = strings.TrimSpace(*in.Summary)
		if len(*in.Summary) > maxShortField {
			return errInvalid("summary is at most 4000 characters")
		}
	}
	for name, list := range in.lists() {
		if list == nil {
			continue
		}
		clean := []string{}
		for _, v := range *list {
			v = strings.TrimSpace(v)
			if len(v) > 500 {
				return errInvalid(name + " items are at most 500 characters each")
			}
			if v != "" && !slices.Contains(clean, v) {
				clean = append(clean, v)
			}
		}
		if len(clean) > 20 {
			return errInvalid(name + " holds at most 20 items")
		}
		*list = clean
	}
	if in.Source != nil && !slices.Contains(profileSources, *in.Source) {
		return errInvalid("source is one of " + strings.Join(profileSources, ", "))
	}
	if in.Status != nil && !slices.Contains(personaStatuses, *in.Status) {
		return errInvalid("status is suggested, confirmed or archived")
	}
	return nil
}

func (s *Server) createPersona(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in personaInput
	if !decode(w, r, &in) {
		return
	}
	if in.Name == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	if err := in.validate(); s.allocError(w, r, err) {
		return
	}
	human := a.ActorType == "human"
	status, source := "confirmed", "person"
	if !human {
		status, source = "suggested", "agent"
	}
	if in.Status != nil {
		switch {
		case *in.Status == "archived":
			s.allocError(w, r, errInvalid("a new persona is suggested or confirmed; archive it with a PATCH"))
			return
		case *in.Status == "confirmed" && !human:
			s.allocError(w, r, errOnlyPeopleConfirm)
			return
		}
		status = *in.Status
	}
	if in.Source != nil {
		source = *in.Source
	}
	list := func(p *[]string) []string {
		if p == nil {
			return []string{}
		}
		return *p
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM brand_personas WHERE brand_id = $1 AND status <> 'archived'`, a.Brand).Scan(&n); err != nil {
			return err
		}
		if n >= maxPersonas {
			return errInvalid(fmt.Sprintf("a brand keeps at most %d personas; archive ones it no longer sells to first", maxPersonas))
		}
		err := tx.QueryRow(ctx, `INSERT INTO brand_personas (workspace_id, brand_id, name, role, summary, goals, pains, triggers,
				objections, channels, source, status, confirmed_by, confirmed_at, created_by, updated_by, position)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
				CASE WHEN $12 = 'confirmed' THEN $13::uuid END, CASE WHEN $12 = 'confirmed' THEN now() END, $13, $13,
				(SELECT COALESCE(max(position), 0) + 1 FROM brand_personas WHERE brand_id = $2))
			RETURNING id`,
			a.WorkspaceID, a.Brand, *in.Name, deref(in.Role), deref(in.Summary), list(in.Goals), list(in.Pains), list(in.Triggers),
			list(in.Objections), list(in.Channels), source, status, actorRef(a)).Scan(&id)
		if isUnique(err) {
			return errInvalid("the brand already has a persona with that name")
		}
		if err != nil {
			return err
		}
		if err := profileChange(ctx, tx, a, nil, &id, "created", nil, map[string]any{"name": *in.Name, "status": status}); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "persona.created", "persona", id,
			map[string]any{"brand_id": a.Brand, "name": *in.Name, "status": status, "source": source},
			map[string]any{"brand_id": a.Brand, "status": status, "source": source})
	})
	if s.personaError(w, r, err) {
		return
	}
	v, err := loadPersona(ctx, s.pool, a.WorkspaceID, a.Brand, id)
	if s.personaError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) updatePersona(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in personaInput
	if !decode(w, r, &in) {
		return
	}
	if err := in.validate(); s.allocError(w, r, err) {
		return
	}
	if in.Source != nil {
		s.allocError(w, r, errInvalid("source is set when a persona is added and doesn't change"))
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := loadPersona(ctx, tx, a.WorkspaceID, a.Brand, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM brand_personas WHERE id = $1 FOR UPDATE`, cur.ID); err != nil {
			return err
		}
		if a.ActorType != "human" && (cur.Status != "suggested" || (in.Status != nil && *in.Status == "confirmed")) {
			return errOnlyPeopleConfirm
		}
		before, after := map[string]any{}, map[string]any{}
		diff := func(key, old string, new *string) {
			if new != nil && *new != old {
				before[key], after[key] = old, *new
			}
		}
		diff("name", cur.Name, in.Name)
		diff("role", cur.Role, in.Role)
		diff("summary", cur.Summary, in.Summary)
		diff("status", cur.Status, in.Status)
		for name, old := range map[string][]string{"goals": cur.Goals, "pains": cur.Pains, "triggers": cur.Triggers, "objections": cur.Objections, "channels": cur.Channels} {
			if p := in.lists()[name]; p != nil && !slices.Equal(*p, old) {
				before[name], after[name] = old, *p
			}
		}
		if len(after) == 0 {
			return nil
		}
		newStatus := cur.Status
		if in.Status != nil {
			newStatus = *in.Status
		}
		_, err = tx.Exec(ctx, `UPDATE brand_personas SET
				name = COALESCE($2, name), role = COALESCE($3, role), summary = COALESCE($4, summary),
				goals = COALESCE($5, goals), pains = COALESCE($6, pains), triggers = COALESCE($7, triggers),
				objections = COALESCE($8, objections), channels = COALESCE($9, channels), status = $10,
				confirmed_by = CASE WHEN $10 = 'confirmed' AND status <> 'confirmed' THEN $11::uuid WHEN $10 = 'suggested' THEN NULL ELSE confirmed_by END,
				confirmed_at = CASE WHEN $10 = 'confirmed' AND status <> 'confirmed' THEN now() WHEN $10 = 'suggested' THEN NULL ELSE confirmed_at END,
				updated_by = $11, updated_at = now()
			WHERE id = $1`,
			cur.ID, in.Name, in.Role, in.Summary, in.Goals, in.Pains, in.Triggers, in.Objections, in.Channels, newStatus, actorRef(a))
		if isUnique(err) {
			return errInvalid("the brand already has a persona with that name")
		}
		if err != nil {
			return err
		}
		action, event := "updated", "persona.updated"
		switch {
		case newStatus == cur.Status:
		case newStatus == "archived":
			action, event = "retired", "persona.archived"
		case cur.Status == "archived":
			action, event = "restored", "persona.restored"
		case newStatus == "confirmed":
			action, event = "confirmed", "persona.confirmed"
		}
		if err := profileChange(ctx, tx, a, nil, &cur.ID, action, before, after); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, event, "persona", cur.ID,
			map[string]any{"brand_id": a.Brand, "fields": mapKeys(after)},
			map[string]any{"brand_id": a.Brand, "source": cur.Source, "fields": mapKeys(after)})
	})
	if s.personaError(w, r, err) {
		return
	}
	v, err := loadPersona(ctx, s.pool, a.WorkspaceID, a.Brand, id)
	if s.personaError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// personaBrief is a confirmed persona as an agent reads it in the brand's
// context.
type personaBrief struct {
	Name       string   `json:"name"`
	Role       string   `json:"role"`
	Summary    string   `json:"summary"`
	Goals      []string `json:"goals"`
	Pains      []string `json:"pains"`
	Triggers   []string `json:"triggers"`
	Objections []string `json:"objections"`
	Channels   []string `json:"channels"`
}

func (s *Server) loadPersonaBrief(ctx context.Context, ws, brand string, limit int) ([]personaBrief, error) {
	list, err := loadPersonas(ctx, s.pool, ws, brand, false)
	out := []personaBrief{}
	for _, p := range list {
		if p.Status == "confirmed" && len(out) < limit {
			out = append(out, personaBrief{p.Name, p.Role, p.Summary, p.Goals, p.Pains, p.Triggers, p.Objections, p.Channels})
		}
	}
	return out, err
}
