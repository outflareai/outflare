package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/tgsession"
)

// The team directory (migration 0490): everyone who works at the company,
// whether or not they sign in, their departments and teams, and the work
// accounts the company owns and hands to them. Workspace-level, like
// members and teams.

func (s *Server) routesStaff(mux *http.ServeMux) {
	read, manage := auth.ScopeMembersRead, auth.ScopeMembersManage
	base := "/v1/workspaces/{ws}"
	mux.HandleFunc("GET "+base+"/staff", s.inWorkspace(read, s.listStaff))
	mux.HandleFunc("POST "+base+"/staff", s.inWorkspace(manage, s.createStaff))
	mux.HandleFunc("GET "+base+"/staff/{id}", s.inWorkspace(read, s.getStaff))
	mux.HandleFunc("PATCH "+base+"/staff/{id}", s.inWorkspace(manage, s.updateStaff))
	mux.HandleFunc("DELETE "+base+"/staff/{id}", s.inWorkspace(manage, s.deleteStaff))

	mux.HandleFunc("GET "+base+"/departments", s.inWorkspace(read, s.listDepartments))
	mux.HandleFunc("POST "+base+"/departments", s.inWorkspace(manage, s.createDepartment))
	mux.HandleFunc("PATCH "+base+"/departments/{id}", s.inWorkspace(manage, s.updateDepartment))
	mux.HandleFunc("DELETE "+base+"/departments/{id}", s.inWorkspace(manage, s.deleteDepartment))

	mux.HandleFunc("GET "+base+"/staff-accounts", s.inWorkspace(read, s.listStaffAccounts))
	mux.HandleFunc("POST "+base+"/staff-accounts", s.inWorkspace(manage, s.createStaffAccount))
	mux.HandleFunc("GET "+base+"/staff-accounts/{id}", s.inWorkspace(read, s.getStaffAccount))
	mux.HandleFunc("PATCH "+base+"/staff-accounts/{id}", s.inWorkspace(manage, s.updateStaffAccount))
	mux.HandleFunc("DELETE "+base+"/staff-accounts/{id}", s.inWorkspace(manage, s.deleteStaffAccount))
}

// ---- People ----

type staffAccountRef struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Address string `json:"address"`
	Label   string `json:"label"`
}

type staffView struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Email        *string           `json:"email"`
	Title        string            `json:"title"`
	DepartmentID *string           `json:"department_id"`
	TeamID       *string           `json:"team_id"`
	ManagerID    *string           `json:"manager_id"`
	UserID       *string           `json:"user_id"`
	Status       string            `json:"status"`
	StartedOn    *string           `json:"started_on"`
	LeftOn       *string           `json:"left_on"`
	Accounts     []staffAccountRef `json:"accounts"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

const staffColumns = `
	st.id, st.name, st.email, st.title, st.department_id::text, st.team_id::text, st.manager_id::text,
	st.user_id::text, st.status, to_char(st.started_on, 'YYYY-MM-DD'), to_char(st.left_on, 'YYYY-MM-DD'),
	COALESCE((SELECT jsonb_agg(jsonb_build_object('id', a.id, 'kind', a.kind, 'address', a.address, 'label', a.label)
	          ORDER BY a.kind, a.address)
	          FROM staff_accounts a WHERE a.holder_id = st.id AND a.status = 'active'), '[]'::jsonb),
	st.created_at, st.updated_at`

func (s *Server) loadStaff(ctx context.Context, ws string, id *string, department, team, status string) ([]staffView, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+staffColumns+`
		FROM staff st
		WHERE st.workspace_id = $1 AND ($2::uuid IS NULL OR st.id = $2)
		  AND ($3 = '' OR st.department_id::text = $3) AND ($4 = '' OR st.team_id::text = $4)
		  AND ($5 = '' OR st.status = $5)
		ORDER BY st.status = 'left', lower(st.name), st.id`, ws, id, department, team, status)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[staffView])
}

func (s *Server) listStaff(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	dept, team, status := q.Get("department_id"), q.Get("team_id"), q.Get("status")
	if (dept != "" && !validUUID(dept)) || (team != "" && !validUUID(team)) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "department_id and team_id must be ids")
		return
	}
	if status != "" && status != "active" && status != "leaving" && status != "left" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is active, leaving or left")
		return
	}
	list, err := s.loadStaff(r.Context(), a.WorkspaceID, nil, dept, team, status)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []staffView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) getStaff(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		s.allocError(w, r, errNotFound)
		return
	}
	list, err := s.loadStaff(r.Context(), a.WorkspaceID, &id, "", "", "")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if len(list) == 0 {
		s.allocError(w, r, errNotFound)
		return
	}
	writeJSON(w, http.StatusOK, list[0])
}

type staffInput struct {
	Name         *string          `json:"name"`
	Email        optional[string] `json:"email"`
	Title        *string          `json:"title"`
	DepartmentID optional[string] `json:"department_id"`
	TeamID       optional[string] `json:"team_id"`
	ManagerID    optional[string] `json:"manager_id"`
	UserID       optional[string] `json:"user_id"`
	Status       *string          `json:"status"`
	StartedOn    optional[string] `json:"started_on"`
	LeftOn       optional[string] `json:"left_on"`
}

func validDate(s *string) bool {
	if s == nil {
		return true
	}
	_, err := time.Parse(time.DateOnly, *s)
	return err == nil
}

func optionalID(o optional[string]) bool {
	return o.Value == nil || validUUID(*o.Value)
}

func (in *staffInput) validate() error {
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if *in.Name == "" || len(*in.Name) > 200 {
			return errInvalid("name must be 1 to 200 characters")
		}
	}
	if in.Email.Value != nil {
		e, err := normalizeEmail(*in.Email.Value)
		if err != nil {
			return errInvalid("email must be an email address")
		}
		in.Email.Value = &e
	}
	if in.Title != nil {
		*in.Title = strings.TrimSpace(*in.Title)
		if len(*in.Title) > 200 {
			return errInvalid("title must be at most 200 characters")
		}
	}
	if !optionalID(in.DepartmentID) || !optionalID(in.TeamID) || !optionalID(in.ManagerID) || !optionalID(in.UserID) {
		return errInvalid("department_id, team_id, manager_id and user_id must be ids")
	}
	if in.Status != nil && *in.Status != "active" && *in.Status != "leaving" && *in.Status != "left" {
		return errInvalid("status is active, leaving or left")
	}
	if !validDate(in.StartedOn.Value) || !validDate(in.LeftOn.Value) {
		return errInvalid("started_on and left_on are dates, YYYY-MM-DD")
	}
	return nil
}

// checkStaffLinks makes sure every id the input names belongs to the
// workspace: a department, a team, a manager in the directory, and a
// member for user_id.
func checkStaffLinks(ctx context.Context, tx pgx.Tx, ws string, in *staffInput, self string) error {
	checks := []struct {
		o   optional[string]
		sql string
		msg string
	}{
		{in.DepartmentID, `SELECT EXISTS (SELECT 1 FROM departments WHERE id = $1 AND workspace_id = $2)`, "department_id is not a department in this workspace"},
		{in.TeamID, `SELECT EXISTS (SELECT 1 FROM teams WHERE id = $1 AND workspace_id = $2)`, "team_id is not a team in this workspace"},
		{in.ManagerID, `SELECT EXISTS (SELECT 1 FROM staff WHERE id = $1 AND workspace_id = $2)`, "manager_id is not a person in this directory"},
		{in.UserID, `SELECT EXISTS (SELECT 1 FROM memberships WHERE user_id = $1 AND workspace_id = $2)`, "user_id is not a member of this workspace"},
	}
	for _, c := range checks {
		if c.o.Value == nil {
			continue
		}
		var ok bool
		if err := tx.QueryRow(ctx, c.sql, *c.o.Value, ws).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errInvalid(c.msg)
		}
	}
	if self != "" && in.ManagerID.Value != nil && *in.ManagerID.Value == self {
		return errInvalid("a person cannot be their own manager")
	}
	return nil
}

func staffUniqueError(err error) error {
	if !isUnique(err) {
		return err
	}
	if strings.Contains(err.Error(), "staff_user_idx") {
		return errInvalid("another person in the directory already has that sign-in")
	}
	return errInvalid("another person in the directory already has that email")
}

func (s *Server) createStaff(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req staffInput
	if !decode(w, r, &req) {
		return
	}
	if req.Name == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	if err := req.validate(); err != nil {
		s.allocError(w, r, err)
		return
	}
	status := "active"
	if req.Status != nil {
		status = *req.Status
	}
	title := ""
	if req.Title != nil {
		title = *req.Title
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := checkStaffLinks(ctx, tx, a.WorkspaceID, &req, ""); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO staff (workspace_id, name, email, title, department_id, team_id, manager_id, user_id, status, started_on, left_on)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::date, $11::date) RETURNING id`,
			a.WorkspaceID, *req.Name, req.Email.Value, title, req.DepartmentID.Value, req.TeamID.Value,
			req.ManagerID.Value, req.UserID.Value, status, req.StartedOn.Value, req.LeftOn.Value).Scan(&id)
		if err != nil {
			return staffUniqueError(err)
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "staff.created", "staff", id, nil,
			map[string]any{"status": status, "has_email": req.Email.Value != nil, "has_sign_in": req.UserID.Value != nil})
	})
	if s.allocError(w, r, err) {
		return
	}
	list, err := s.loadStaff(ctx, a.WorkspaceID, &id, "", "", "")
	if err != nil || len(list) == 0 {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, list[0])
}

func (s *Server) updateStaff(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var req staffInput
	if !decode(w, r, &req) {
		return
	}
	if err := req.validate(); err != nil || !validUUID(id) {
		if err == nil {
			err = errNotFound
		}
		s.allocError(w, r, err)
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := checkStaffLinks(ctx, tx, a.WorkspaceID, &req, id); err != nil {
			return err
		}
		var before string
		err := tx.QueryRow(ctx, `SELECT status FROM staff WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, id, a.WorkspaceID).Scan(&before)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE staff SET
				name = COALESCE($3, name),
				email = CASE WHEN $4 THEN $5 ELSE email END,
				title = COALESCE($6, title),
				department_id = CASE WHEN $7 THEN $8::uuid ELSE department_id END,
				team_id = CASE WHEN $9 THEN $10::uuid ELSE team_id END,
				manager_id = CASE WHEN $11 THEN $12::uuid ELSE manager_id END,
				user_id = CASE WHEN $13 THEN $14::uuid ELSE user_id END,
				status = COALESCE($15, status),
				started_on = CASE WHEN $16 THEN $17::date ELSE started_on END,
				left_on = CASE WHEN $18 THEN $19::date
				               WHEN $15 = 'left' AND left_on IS NULL THEN current_date
				               ELSE left_on END,
				updated_at = now()
			WHERE id = $1 AND workspace_id = $2`,
			id, a.WorkspaceID, req.Name, req.Email.Set, req.Email.Value, req.Title,
			req.DepartmentID.Set, req.DepartmentID.Value, req.TeamID.Set, req.TeamID.Value,
			req.ManagerID.Set, req.ManagerID.Value, req.UserID.Set, req.UserID.Value, req.Status,
			req.StartedOn.Set, req.StartedOn.Value, req.LeftOn.Set, req.LeftOn.Value)
		if err != nil {
			return staffUniqueError(err)
		}
		action := "staff.updated"
		if req.Status != nil && *req.Status != before {
			action = map[string]string{"active": "staff.reactivated", "leaving": "staff.leaving", "left": "staff.left"}[*req.Status]
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, action, "staff", id, nil, map[string]any{"status_before": before})
	})
	if s.allocError(w, r, err) {
		return
	}
	list, err := s.loadStaff(ctx, a.WorkspaceID, &id, "", "", "")
	if err != nil || len(list) == 0 {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list[0])
}

// deleteStaff removes someone added by mistake. Someone who has held a
// company account is marked as left instead, so the account's history
// keeps who held it.
func (s *Server) deleteStaff(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		s.allocError(w, r, errNotFound)
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var held bool
		err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM staff_account_holders WHERE staff_id = st.id)
			FROM staff st WHERE st.id = $1 AND st.workspace_id = $2 FOR UPDATE`, id, a.WorkspaceID).Scan(&held)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if held {
			return errInvalid("this person has held a company account: mark them as left instead, so its history keeps them")
		}
		if _, err := tx.Exec(ctx, `DELETE FROM staff WHERE id = $1 AND workspace_id = $2`, id, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "staff.deleted", "staff", id, nil)
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Departments ----

type departmentView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	HeadID    *string   `json:"head_id"`
	People    int       `json:"people"`
	Teams     int       `json:"teams"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) loadDepartments(ctx context.Context, ws string, id *string) ([]departmentView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.id, d.name, d.head_id::text,
		       (SELECT count(*) FROM staff WHERE department_id = d.id AND status <> 'left')::int,
		       (SELECT count(*) FROM teams WHERE department_id = d.id)::int,
		       d.created_at
		FROM departments d WHERE d.workspace_id = $1 AND ($2::uuid IS NULL OR d.id = $2)
		ORDER BY lower(d.name)`, ws, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[departmentView])
}

type departmentInput struct {
	Name   *string          `json:"name"`
	HeadID optional[string] `json:"head_id"`
}

func (in *departmentInput) validate() error {
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if *in.Name == "" || len(*in.Name) > 100 {
			return errInvalid("name must be 1 to 100 characters")
		}
	}
	if !optionalID(in.HeadID) {
		return errInvalid("head_id must be an id")
	}
	return nil
}

func checkStaffID(ctx context.Context, tx pgx.Tx, ws string, id *string, field string) error {
	if id == nil {
		return nil
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM staff WHERE id = $1 AND workspace_id = $2)`, *id, ws).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return errInvalid(field + " is not a person in this directory")
	}
	return nil
}

func (s *Server) listDepartments(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	list, err := s.loadDepartments(r.Context(), a.WorkspaceID, nil)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []departmentView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) createDepartment(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req departmentInput
	if !decode(w, r, &req) {
		return
	}
	if req.Name == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	if err := req.validate(); err != nil {
		s.allocError(w, r, err)
		return
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := checkStaffID(ctx, tx, a.WorkspaceID, req.HeadID.Value, "head_id"); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `INSERT INTO departments (workspace_id, name, head_id) VALUES ($1, $2, $3) RETURNING id`,
			a.WorkspaceID, *req.Name, req.HeadID.Value).Scan(&id)
		if isUnique(err) {
			return errInvalid("a department with that name already exists")
		}
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "department.created", "department", id, nil)
	})
	if s.allocError(w, r, err) {
		return
	}
	list, err := s.loadDepartments(ctx, a.WorkspaceID, &id)
	if err != nil || len(list) == 0 {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, list[0])
}

func (s *Server) updateDepartment(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var req departmentInput
	if !decode(w, r, &req) {
		return
	}
	if err := req.validate(); err != nil || !validUUID(id) {
		if err == nil {
			err = errNotFound
		}
		s.allocError(w, r, err)
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := checkStaffID(ctx, tx, a.WorkspaceID, req.HeadID.Value, "head_id"); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE departments SET name = COALESCE($3, name), head_id = CASE WHEN $4 THEN $5::uuid ELSE head_id END, updated_at = now()
			WHERE id = $1 AND workspace_id = $2`, id, a.WorkspaceID, req.Name, req.HeadID.Set, req.HeadID.Value)
		if isUnique(err) {
			return errInvalid("a department with that name already exists")
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "department.updated", "department", id, nil)
	})
	if s.allocError(w, r, err) {
		return
	}
	list, err := s.loadDepartments(ctx, a.WorkspaceID, &id)
	if err != nil || len(list) == 0 {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list[0])
}

func (s *Server) deleteDepartment(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		s.allocError(w, r, errNotFound)
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM departments WHERE id = $1 AND workspace_id = $2`, id, a.WorkspaceID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "department.deleted", "department", id, nil)
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Work accounts ----

type staffAccountHolder struct {
	StaffID   string     `json:"staff_id"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
}

type staffAccountView struct {
	ID        string               `json:"id"`
	Kind      string               `json:"kind"`
	Address   string               `json:"address"`
	Label     string               `json:"label"`
	HolderID  *string              `json:"holder_id"`
	Status    string               `json:"status"`
	RenewsOn  *string              `json:"renews_on"`
	CreatedAt time.Time            `json:"created_at"`
	UpdatedAt time.Time            `json:"updated_at"`
	Holders   []staffAccountHolder `json:"holders,omitempty"`
}

func (s *Server) loadStaffAccounts(ctx context.Context, ws string, id *string, kind, holder string) ([]staffAccountView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.kind, a.address, a.label, a.holder_id::text, a.status, to_char(a.renews_on, 'YYYY-MM-DD'), a.created_at, a.updated_at
		FROM staff_accounts a
		WHERE a.workspace_id = $1 AND ($2::uuid IS NULL OR a.id = $2) AND ($3 = '' OR a.kind = $3) AND ($4 = '' OR a.holder_id::text = $4)
		ORDER BY a.status = 'retired', a.kind, a.address`, ws, id, kind, holder)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (staffAccountView, error) {
		var v staffAccountView
		err := row.Scan(&v.ID, &v.Kind, &v.Address, &v.Label, &v.HolderID, &v.Status, &v.RenewsOn, &v.CreatedAt, &v.UpdatedAt)
		return v, err
	})
}

// normalizeAccountAddress puts an address in the form staff_accounts
// stores: emails lower case, phones in E.164, Telegram usernames without
// the @ in lower case, or the account's phone number until it has one.
func normalizeAccountAddress(kind, address string) (string, error) {
	switch kind {
	case "email":
		return normalizeEmail(address)
	case "phone":
		return normalizePhone(address)
	case "telegram":
		a := strings.TrimSpace(address)
		a = strings.TrimPrefix(strings.TrimPrefix(a, "https://t.me/"), "t.me/")
		if strings.HasPrefix(a, "+") {
			return normalizePhone(a)
		}
		a = strings.ToLower(strings.TrimPrefix(a, "@"))
		if !telegramUsername(a) {
			return "", errInvalid("a Telegram account is its username (5 to 32 letters, digits or _) or its phone number")
		}
		return a, nil
	}
	return "", errInvalid("kind is telegram, email or phone")
}

func telegramUsername(s string) bool {
	if len(s) < 5 || len(s) > 32 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

type staffAccountInput struct {
	Kind     *string          `json:"kind"`
	Address  *string          `json:"address"`
	Label    *string          `json:"label"`
	HolderID optional[string] `json:"holder_id"`
	Status   *string          `json:"status"`
	RenewsOn optional[string] `json:"renews_on"`
}

func (in *staffAccountInput) validate(kind string) error {
	if in.Address != nil {
		a, err := normalizeAccountAddress(kind, *in.Address)
		if err != nil {
			return err
		}
		in.Address = &a
	}
	if in.Label != nil {
		*in.Label = strings.TrimSpace(*in.Label)
		if len(*in.Label) > 100 {
			return errInvalid("label must be at most 100 characters")
		}
	}
	if !optionalID(in.HolderID) {
		return errInvalid("holder_id must be an id")
	}
	if in.Status != nil && *in.Status != "active" && *in.Status != "retired" {
		return errInvalid("status is active or retired")
	}
	if !validDate(in.RenewsOn.Value) {
		return errInvalid("renews_on is a date, YYYY-MM-DD")
	}
	return nil
}

// assignHolder closes the account's open holder row and opens one for
// holder (nil: nobody holds it now). It returns whether anything changed.
func assignHolder(ctx context.Context, tx pgx.Tx, p *principal, ws, account string, holder *string) (bool, error) {
	var current *string
	if err := tx.QueryRow(ctx, `SELECT staff_id::text FROM staff_account_holders WHERE account_id = $1 AND ended_at IS NULL`, account).Scan(&current); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if (current == nil && holder == nil) || (current != nil && holder != nil && *current == *holder) {
		return false, nil
	}
	if holder != nil {
		var active bool
		err := tx.QueryRow(ctx, `SELECT status <> 'left' FROM staff WHERE id = $1 AND workspace_id = $2`, *holder, ws).Scan(&active)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, errInvalid("holder_id is not a person in this directory")
		}
		if err != nil {
			return false, err
		}
		if !active {
			return false, errInvalid("this person has left: pick someone who still works here")
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE staff_account_holders SET ended_at = now() WHERE account_id = $1 AND ended_at IS NULL`, account); err != nil {
		return false, err
	}
	if holder != nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO staff_account_holders (workspace_id, account_id, staff_id, assigned_by) VALUES ($1, $2, $3, $4)`,
			ws, account, *holder, p.ActorID); err != nil {
			return false, err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE staff_accounts SET holder_id = $2, updated_at = now() WHERE id = $1`, account, holder)
	return true, err
}

func (s *Server) listStaffAccounts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	kind, holder := q.Get("kind"), q.Get("holder_id")
	if kind != "" && kind != "telegram" && kind != "email" && kind != "phone" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is telegram, email or phone")
		return
	}
	if holder != "" && !validUUID(holder) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "holder_id must be an id")
		return
	}
	list, err := s.loadStaffAccounts(r.Context(), a.WorkspaceID, nil, kind, holder)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []staffAccountView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) staffAccountWithHolders(ctx context.Context, ws, id string) (*staffAccountView, error) {
	list, err := s.loadStaffAccounts(ctx, ws, &id, "", "")
	if err != nil || len(list) == 0 {
		if err == nil {
			err = errNotFound
		}
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT staff_id::text, started_at, ended_at FROM staff_account_holders
		WHERE account_id = $1 AND workspace_id = $2 ORDER BY started_at DESC, id`, id, ws)
	if err != nil {
		return nil, err
	}
	holders, err := pgx.CollectRows(rows, pgx.RowToStructByPos[staffAccountHolder])
	if err != nil {
		return nil, err
	}
	v := list[0]
	v.Holders = holders
	if v.Holders == nil {
		v.Holders = []staffAccountHolder{}
	}
	return &v, nil
}

func (s *Server) getStaffAccount(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		s.allocError(w, r, errNotFound)
		return
	}
	v, err := s.staffAccountWithHolders(r.Context(), a.WorkspaceID, id)
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) createStaffAccount(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req staffAccountInput
	if !decode(w, r, &req) {
		return
	}
	if req.Kind == nil || req.Address == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind and address are required")
		return
	}
	if err := req.validate(*req.Kind); err != nil {
		s.allocError(w, r, err)
		return
	}
	label := ""
	if req.Label != nil {
		label = *req.Label
	}
	status := "active"
	if req.Status != nil {
		status = *req.Status
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO staff_accounts (workspace_id, kind, address, label, status, renews_on)
			VALUES ($1, $2, $3, $4, $5, $6::date) RETURNING id`,
			a.WorkspaceID, *req.Kind, *req.Address, label, status, req.RenewsOn.Value).Scan(&id)
		if isUnique(err) {
			return errInvalid("that account is already in the directory")
		}
		if err != nil {
			return err
		}
		if err := recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "staff_account.created", "staff_account", id, nil,
			map[string]any{"kind": *req.Kind}); err != nil {
			return err
		}
		if req.HolderID.Value == nil {
			return nil
		}
		if _, err := assignHolder(ctx, tx, &a.principal, a.WorkspaceID, id, req.HolderID.Value); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "staff_account.assigned", "staff_account", id,
			map[string]any{"staff_id": *req.HolderID.Value}, map[string]any{"kind": *req.Kind})
	})
	if s.allocError(w, r, err) {
		return
	}
	v, err := s.staffAccountWithHolders(ctx, a.WorkspaceID, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) updateStaffAccount(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var req staffAccountInput
	if !decode(w, r, &req) {
		return
	}
	if !validUUID(id) {
		s.allocError(w, r, errNotFound)
		return
	}
	if req.Kind != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "an account's kind can't change: add a new account instead")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var kind string
		err := tx.QueryRow(ctx, `SELECT kind FROM staff_accounts WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, id, a.WorkspaceID).Scan(&kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if err := req.validate(kind); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE staff_accounts SET
				address = COALESCE($3, address), label = COALESCE($4, label), status = COALESCE($5, status),
				renews_on = CASE WHEN $6 THEN $7::date ELSE renews_on END, updated_at = now()
			WHERE id = $1 AND workspace_id = $2`,
			id, a.WorkspaceID, req.Address, req.Label, req.Status, req.RenewsOn.Set, req.RenewsOn.Value)
		if isUnique(err) {
			return errInvalid("that account is already in the directory")
		}
		if err != nil {
			return err
		}
		props := map[string]any{"kind": kind}
		if err := recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "staff_account.updated", "staff_account", id, nil, props); err != nil {
			return err
		}
		holder := req.HolderID.Value
		if req.Status != nil && *req.Status == "retired" {
			// A retired account has no holder; its history stays. Its
			// Telegram session, if any, is signed out.
			req.HolderID.Set, holder = true, nil
			signedOut, err := signOutTelegram(ctx, tx, a.WorkspaceID, id)
			if err != nil {
				return err
			}
			if signedOut {
				if err := recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "telegram_account.signed_out", "staff_account", id,
					map[string]any{"by": "retired"}, map[string]any{"by": "retired"}); err != nil {
					return err
				}
			}
		}
		if !req.HolderID.Set {
			return nil
		}
		changed, err := assignHolder(ctx, tx, &a.principal, a.WorkspaceID, id, holder)
		if err != nil || !changed {
			return err
		}
		if holder == nil {
			return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "staff_account.unassigned", "staff_account", id, nil, props)
		}
		if err := tgsession.Handover(ctx, tx, a.WorkspaceID, id, *holder, RequestID(ctx)); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "staff_account.assigned", "staff_account", id,
			map[string]any{"staff_id": *holder}, props)
	})
	if s.allocError(w, r, err) {
		return
	}
	v, err := s.staffAccountWithHolders(ctx, a.WorkspaceID, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// deleteStaffAccount removes an account added by mistake. One that has
// been held is retired instead, so its history stays.
func (s *Server) deleteStaffAccount(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		s.allocError(w, r, errNotFound)
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var held, signedIn bool
		var kind string
		err := tx.QueryRow(ctx, `
			SELECT a.kind, EXISTS (SELECT 1 FROM staff_account_holders WHERE account_id = a.id),
			       EXISTS (SELECT 1 FROM telegram_sessions t WHERE t.account_id = a.id AND t.status NOT IN ('signed_out', 'expired', 'failed'))
			FROM staff_accounts a WHERE a.id = $1 AND a.workspace_id = $2 FOR UPDATE`, id, a.WorkspaceID).Scan(&kind, &held, &signedIn)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if held {
			return errInvalid("this account has been held by someone: retire it instead, so its history stays")
		}
		if signedIn {
			return errInvalid("this account is signed in to Telegram here: sign it out first")
		}
		if _, err := tx.Exec(ctx, `DELETE FROM staff_accounts WHERE id = $1 AND workspace_id = $2`, id, a.WorkspaceID); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "staff_account.deleted", "staff_account", id, nil, map[string]any{"kind": kind})
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
