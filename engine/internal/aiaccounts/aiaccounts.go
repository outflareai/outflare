// Package aiaccounts keeps the AI subscriptions and API accounts a
// workspace's people pay for (plans/ai-accounts): which AI tool, the email
// the account is on, its plan, price and renewal, who pays, and the people
// it belongs to. People add them by hand for now; receipts, Outflare for
// desktop and admin keys fill them in later.
//
// Accounts belong to workspace members (users), not customer records. A
// member who leaves stays on the account, which then shows "owner left"
// until someone takes it over.
package aiaccounts

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Providers are the AI tools an account can be for, in display order.
var Providers = []string{"claude", "chatgpt", "cursor", "copilot", "anthropic_api", "openai_api"}

// Who pays for an account. Only company-paid accounts count in spend.
const (
	PaidByCompany = "company"
	PaidByPerson  = "person"
)

// BillingCycles are how often an account is charged.
var BillingCycles = []string{"monthly", "yearly", "prepaid", "usage"}

// Member roles: the owner answers for the account; a user also signs in
// to it.
const (
	RoleOwner = "owner"
	RoleUser  = "user"
)

var (
	ErrNotFound = errors.New("no AI account with that id")
	// ErrNotMember is returned when a member's user isn't in the
	// workspace.
	ErrNotMember = errors.New("every person on an AI account must be in the workspace")
)

// Member is a person an account belongs to.
type Member struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	// Left: they are no longer in the workspace.
	Left bool `json:"left"`
}

// Account is an AI subscription or API account.
type Account struct {
	ID           string  `json:"id"`
	Provider     string  `json:"provider"`
	Name         string  `json:"name"`
	AccountEmail *string `json:"account_email"`
	Plan         *string `json:"plan"`
	Seats        *int    `json:"seats"`
	PaidBy       string  `json:"paid_by"`
	BillingCycle *string `json:"billing_cycle"`
	// RenewsOn is the next renewal day, YYYY-MM-DD.
	RenewsOn *string `json:"renews_on"`
	// PriceCents is the price per billing cycle in US cents. It is null
	// when unknown, and for callers who may not see the money on this
	// account (PriceHidden).
	PriceCents      *int64 `json:"price_cents"`
	PriceHidden     bool   `json:"price_hidden"`
	ResetsAvailable *int   `json:"resets_available"`
	Cancelled       bool   `json:"cancelled"`
	// OwnerLeft: nobody who owns it is still in the workspace.
	OwnerLeft bool      `json:"owner_left"`
	Members   []Member  `json:"members"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Input is what a person sets on an account. Fields left out keep their
// values on an update.
type Input struct {
	Provider        *string        `json:"provider"`
	Name            *string        `json:"name"`
	AccountEmail    *string        `json:"account_email"`
	Plan            *string        `json:"plan"`
	Seats           *int           `json:"seats"`
	PaidBy          *string        `json:"paid_by"`
	BillingCycle    *string        `json:"billing_cycle"`
	RenewsOn        *string        `json:"renews_on"`
	PriceCents      *int64         `json:"price_cents"`
	ResetsAvailable *int           `json:"resets_available"`
	Cancelled       *bool          `json:"cancelled"`
	Members         *[]MemberInput `json:"members"`
}

// MemberInput puts a person on an account.
type MemberInput struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// Validate checks what is set and tidies it: trims text, lower-cases the
// email, turns empty optional text into a clear, and gives members the
// owner role by default. create requires provider and name.
func (in *Input) Validate(create bool) error {
	if create && (in.Provider == nil || in.Name == nil) {
		return errors.New("provider and name are required")
	}
	if in.Provider != nil && !slices.Contains(Providers, *in.Provider) {
		return fmt.Errorf("provider must be one of %s", strings.Join(Providers, ", "))
	}
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if *in.Name == "" || len([]rune(*in.Name)) > 100 {
			return errors.New("name must be 1 to 100 characters")
		}
	}
	if in.AccountEmail != nil {
		e := strings.ToLower(strings.TrimSpace(*in.AccountEmail))
		in.AccountEmail = &e
		if e != "" {
			if a, err := mail.ParseAddress(e); err != nil || a.Address != e || len(e) > 254 {
				return errors.New("account_email must be an email address")
			}
		}
	}
	if in.Plan != nil {
		p := strings.TrimSpace(*in.Plan)
		in.Plan = &p
		if len([]rune(p)) > 100 {
			return errors.New("plan must be at most 100 characters")
		}
	}
	if in.Seats != nil && (*in.Seats < 1 || *in.Seats > 100000) {
		return errors.New("seats must be 1 to 100000")
	}
	if in.PaidBy != nil && *in.PaidBy != PaidByCompany && *in.PaidBy != PaidByPerson {
		return errors.New("paid_by must be company or person")
	}
	if in.BillingCycle != nil && *in.BillingCycle != "" && !slices.Contains(BillingCycles, *in.BillingCycle) {
		return fmt.Errorf("billing_cycle must be one of %s", strings.Join(BillingCycles, ", "))
	}
	if in.RenewsOn != nil && *in.RenewsOn != "" {
		if _, err := time.Parse(time.DateOnly, *in.RenewsOn); err != nil {
			return errors.New("renews_on must be a date (YYYY-MM-DD)")
		}
	}
	if in.PriceCents != nil && (*in.PriceCents < 0 || *in.PriceCents > 100_000_000_000) {
		return errors.New("price_cents must be 0 to 100000000000")
	}
	if in.ResetsAvailable != nil && (*in.ResetsAvailable < 0 || *in.ResetsAvailable > 1000) {
		return errors.New("resets_available must be 0 to 1000")
	}
	if in.Members != nil {
		seen := map[string]bool{}
		for i := range *in.Members {
			m := &(*in.Members)[i]
			if m.Role == "" {
				m.Role = RoleOwner
			}
			if m.Role != RoleOwner && m.Role != RoleUser {
				return errors.New("a member's role must be owner or user")
			}
			if m.UserID == "" || seen[m.UserID] {
				return errors.New("each member needs a user_id, once")
			}
			seen[m.UserID] = true
		}
	}
	return nil
}

// HasMember reports whether userID is among members.
func HasMember(members []MemberInput, userID string) bool {
	return slices.ContainsFunc(members, func(m MemberInput) bool { return m.UserID == userID })
}

// Querier is satisfied by *pgxpool.Pool, *pgx.Conn and pgx.Tx.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Filter narrows List.
type Filter struct {
	// UserID keeps the accounts this person belongs to.
	UserID   string
	Provider string
}

const columns = `a.id, a.provider, a.name, a.account_email, a.plan, a.seats, a.paid_by, a.billing_cycle,
	a.renews_on::text, a.price_cents, a.resets_available, a.cancelled_at IS NOT NULL, a.created_at, a.updated_at`

func scan(row pgx.Row, a *Account) error {
	return row.Scan(&a.ID, &a.Provider, &a.Name, &a.AccountEmail, &a.Plan, &a.Seats, &a.PaidBy, &a.BillingCycle,
		&a.RenewsOn, &a.PriceCents, &a.ResetsAvailable, &a.Cancelled, &a.CreatedAt, &a.UpdatedAt)
}

// List returns the workspace's accounts, by provider then name, with their
// members.
func List(ctx context.Context, q Querier, workspaceID string, f Filter) ([]Account, error) {
	rows, err := q.Query(ctx, `SELECT `+columns+` FROM ai_accounts a
		WHERE a.workspace_id = $1
		  AND ($2 = '' OR EXISTS (SELECT 1 FROM ai_account_members m WHERE m.account_id = a.id AND m.user_id::text = $2))
		  AND ($3 = '' OR a.provider = $3)
		ORDER BY array_position($4::text[], a.provider), lower(a.name), a.id`, workspaceID, f.UserID, f.Provider, Providers)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Account, error) {
		var a Account
		return a, scan(row, &a)
	})
	if err != nil {
		return nil, err
	}
	return list, addMembers(ctx, q, workspaceID, list)
}

// Get returns one account with its members.
func Get(ctx context.Context, q Querier, workspaceID, id string) (Account, error) {
	var a Account
	err := scan(q.QueryRow(ctx, `SELECT `+columns+` FROM ai_accounts a WHERE a.workspace_id = $1 AND a.id::text = $2`, workspaceID, id), &a)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	list := []Account{a}
	if err := addMembers(ctx, q, workspaceID, list); err != nil {
		return a, err
	}
	return list[0], nil
}

// addMembers fills each account's members and OwnerLeft.
func addMembers(ctx context.Context, q Querier, workspaceID string, list []Account) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]string, len(list))
	for i, a := range list {
		ids[i] = a.ID
		list[i].Members = []Member{}
	}
	byAccount := map[string][]Member{}
	rows, err := q.Query(ctx, `
		SELECT m.account_id::text, m.user_id::text, m.role, u.name, u.email,
		       NOT EXISTS (SELECT 1 FROM memberships s WHERE s.workspace_id = m.workspace_id AND s.user_id = m.user_id)
		FROM ai_account_members m JOIN users u ON u.id = m.user_id
		WHERE m.workspace_id = $1 AND m.account_id::text = ANY($2)
		ORDER BY m.role, m.added_at, m.user_id`, workspaceID, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var account string
		var m Member
		if err := rows.Scan(&account, &m.UserID, &m.Role, &m.Name, &m.Email, &m.Left); err != nil {
			rows.Close()
			return err
		}
		byAccount[account] = append(byAccount[account], m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range list {
		if ms := byAccount[list[i].ID]; ms != nil {
			list[i].Members = ms
		}
		owners, stayed := 0, 0
		for _, m := range list[i].Members {
			if m.Role == RoleOwner {
				owners++
				if !m.Left {
					stayed++
				}
			}
		}
		list[i].OwnerLeft = owners > 0 && stayed == 0
	}
	return nil
}

// IsMember reports whether userID belongs to the account.
func IsMember(ctx context.Context, q Querier, workspaceID, accountID, userID string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ai_account_members WHERE workspace_id = $1 AND account_id::text = $2 AND user_id::text = $3)`,
		workspaceID, accountID, userID).Scan(&ok)
	return ok, err
}

func emptyToNil[T comparable](p *T) *T {
	var zero T
	if p == nil || *p == zero {
		return nil
	}
	return p
}

// Create adds an account and its members in tx. in must be valid.
func Create(ctx context.Context, tx pgx.Tx, workspaceID string, in Input, createdBy *string) (string, error) {
	paidBy := PaidByCompany
	if in.PaidBy != nil {
		paidBy = *in.PaidBy
	}
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO ai_accounts (workspace_id, provider, name, account_email, plan, seats, paid_by, billing_cycle, renews_on,
			price_cents, resets_available, cancelled_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::date, $10, $11, CASE WHEN $12 THEN now() END, $13)
		RETURNING id::text`,
		workspaceID, *in.Provider, *in.Name, emptyToNil(in.AccountEmail), emptyToNil(in.Plan), in.Seats, paidBy,
		emptyToNil(in.BillingCycle), emptyToNil(in.RenewsOn), in.PriceCents, in.ResetsAvailable,
		in.Cancelled != nil && *in.Cancelled, createdBy).Scan(&id)
	if err != nil {
		return "", err
	}
	if in.Members != nil {
		if err := setMembers(ctx, tx, workspaceID, id, *in.Members); err != nil {
			return "", err
		}
	}
	return id, nil
}

// Update changes the fields set in in, in tx. in must be valid. An empty
// string clears optional text; null leaves it.
func Update(ctx context.Context, tx pgx.Tx, workspaceID, id string, in Input) error {
	set, args := []string{}, []any{workspaceID, id}
	add := func(col string, v any) {
		args = append(args, v)
		set = append(set, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if in.Provider != nil {
		add("provider", *in.Provider)
	}
	if in.Name != nil {
		add("name", *in.Name)
	}
	if in.AccountEmail != nil {
		add("account_email", emptyToNil(in.AccountEmail))
	}
	if in.Plan != nil {
		add("plan", emptyToNil(in.Plan))
	}
	if in.Seats != nil {
		add("seats", *in.Seats)
	}
	if in.PaidBy != nil {
		add("paid_by", *in.PaidBy)
	}
	if in.BillingCycle != nil {
		add("billing_cycle", emptyToNil(in.BillingCycle))
	}
	if in.RenewsOn != nil {
		args = append(args, emptyToNil(in.RenewsOn))
		set = append(set, fmt.Sprintf("renews_on = $%d::date", len(args)))
	}
	if in.PriceCents != nil {
		add("price_cents", *in.PriceCents)
	}
	if in.ResetsAvailable != nil {
		add("resets_available", *in.ResetsAvailable)
	}
	if in.Cancelled != nil {
		set = append(set, map[bool]string{true: "cancelled_at = coalesce(cancelled_at, now())", false: "cancelled_at = NULL"}[*in.Cancelled])
	}
	set = append(set, "updated_at = now()")
	tag, err := tx.Exec(ctx, `UPDATE ai_accounts SET `+strings.Join(set, ", ")+` WHERE workspace_id = $1 AND id::text = $2`, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if in.Members != nil {
		return setMembers(ctx, tx, workspaceID, id, *in.Members)
	}
	return nil
}

// setMembers replaces an account's members. People already on it keep
// when they were added, so someone who left the workspace can stay.
func setMembers(ctx context.Context, tx pgx.Tx, workspaceID, accountID string, members []MemberInput) error {
	users, roles := make([]string, len(members)), make([]string, len(members))
	for i, m := range members {
		users[i], roles[i] = m.UserID, m.Role
	}
	// New people must be in the workspace; people already on the account
	// may have left it.
	var bad int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM unnest($3::text[]) u(id)
		WHERE NOT EXISTS (SELECT 1 FROM memberships s WHERE s.workspace_id = $1 AND s.user_id::text = u.id)
		  AND NOT EXISTS (SELECT 1 FROM ai_account_members m WHERE m.account_id::text = $2 AND m.user_id::text = u.id)`,
		workspaceID, accountID, users).Scan(&bad); err != nil {
		return err
	}
	if bad > 0 {
		return ErrNotMember
	}
	if _, err := tx.Exec(ctx, `DELETE FROM ai_account_members WHERE account_id::text = $1 AND NOT (user_id::text = ANY($2))`, accountID, users); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO ai_account_members (account_id, workspace_id, user_id, role)
		SELECT $1::uuid, $2, u.id::uuid, u.role FROM unnest($3::text[], $4::text[]) u(id, role)
		ON CONFLICT (account_id, user_id) DO UPDATE SET role = EXCLUDED.role`, accountID, workspaceID, users, roles)
	return err
}

// Delete removes an account and its members in tx.
func Delete(ctx context.Context, tx pgx.Tx, workspaceID, id string) (Account, error) {
	a, err := Get(ctx, tx, workspaceID, id)
	if err != nil {
		return a, err
	}
	_, err = tx.Exec(ctx, `DELETE FROM ai_accounts WHERE workspace_id = $1 AND id::text = $2`, workspaceID, id)
	return a, err
}
