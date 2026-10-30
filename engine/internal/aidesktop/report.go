package aidesktop

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/aiaccounts"
	"github.com/outflareai/outflare/engine/internal/audit"
)

// Report is what the app sends every few minutes: how it is, what it
// couldn't read, and the usage it read. An empty report is a heartbeat.
type Report struct {
	AppVersion string `json:"app_version"`
	// Status is the app's own view: ok, attention (something the person
	// can fix on the computer, such as signing in to a tool again) or
	// broken.
	Status   string           `json:"status"`
	Errors   []ReadError      `json:"errors"`
	Accounts []AccountReading `json:"accounts"`
}

// ReadError is a tool the app couldn't read, by code only.
type ReadError struct {
	Provider string `json:"provider"`
	Code     string `json:"code"`
}

// AccountReading is one AI account signed in on the computer and its
// limits.
type AccountReading struct {
	Provider string `json:"provider"`
	// AccountEmail is the email the tool is signed in with, when it says.
	AccountEmail string `json:"account_email"`
	Plan         string `json:"plan"`
	// ResetsAvailable is how many banked resets the account has, when the
	// tool says.
	ResetsAvailable *int      `json:"resets_available"`
	Source          string    `json:"source"`
	ObservedAt      time.Time `json:"observed_at"`
	Windows         []Window  `json:"windows"`
}

// Window is one usage limit: how much of it is used and when it resets.
type Window struct {
	Kind        string     `json:"kind"`
	UsedPercent float64    `json:"used_percent"`
	ResetsAt    *time.Time `json:"resets_at"`
}

// Applied says where one reading went.
type Applied struct {
	Provider  string `json:"provider"`
	AccountID string `json:"account_id,omitempty"`
	// Created: the account was new, so the reading added it.
	Created bool `json:"created,omitempty"`
	// Skipped: why the reading was left out (source_off, no_account).
	Skipped string `json:"skipped,omitempty"`
}

var (
	kindRe  = regexp.MustCompile(`^[a-z][a-z0-9_]{1,39}$`)
	codeRe  = regexp.MustCompile(`^[a-z][a-z0-9_]{1,59}$`)
	emailRe = regexp.MustCompile(`^[^@\s]{1,64}@[^@\s]{1,189}\.[a-z]{2,}$`)
)

// ErrorCodes are what the app says when it can't read a tool; anything
// else is stored as other.
var ErrorCodes = []string{"not_signed_in", "token_expired", "not_installed", "rate_limited", "forbidden", "bad_response",
	"network", "keychain_denied", "source_off", "other"}

// MaxAccounts and MaxWindows bound one report.
const (
	MaxAccounts = 20
	MaxWindows  = 12
)

// Validate checks a report and tidies it. now bounds the reading times.
func (r *Report) Validate(now time.Time) error {
	if r.Status == "" {
		r.Status = "ok"
	}
	if !slices.Contains([]string{"ok", "attention", "broken"}, r.Status) {
		return fmt.Errorf("status is ok, attention or broken")
	}
	if len(r.AppVersion) > 40 {
		return fmt.Errorf("app_version is at most 40 characters")
	}
	if len(r.Errors) > MaxAccounts || len(r.Accounts) > MaxAccounts {
		return fmt.Errorf("at most %d accounts and %d errors in a report", MaxAccounts, MaxAccounts)
	}
	for i, e := range r.Errors {
		if !slices.Contains(Providers, e.Provider) || !codeRe.MatchString(e.Code) {
			return fmt.Errorf("each error has a provider (%s) and a code like not_signed_in", strings.Join(Providers, ", "))
		}
		if !slices.Contains(ErrorCodes, e.Code) {
			r.Errors[i].Code = "other"
		}
	}
	for i := range r.Accounts {
		a := &r.Accounts[i]
		a.AccountEmail = strings.ToLower(strings.TrimSpace(a.AccountEmail))
		a.Plan = strings.TrimSpace(a.Plan)
		switch {
		case !slices.Contains(Providers, a.Provider):
			return fmt.Errorf("provider is one of %s", strings.Join(Providers, ", "))
		case !slices.Contains(Sources, a.Source):
			return fmt.Errorf("source is official or extended")
		case a.AccountEmail != "" && !emailRe.MatchString(a.AccountEmail):
			return fmt.Errorf("account_email isn't an email address")
		case len(a.Plan) > 100:
			return fmt.Errorf("plan is at most 100 characters")
		case a.ResetsAvailable != nil && (*a.ResetsAvailable < 0 || *a.ResetsAvailable > 1000):
			return fmt.Errorf("resets_available is 0 to 1000")
		case len(a.Windows) > MaxWindows:
			return fmt.Errorf("at most %d windows per account", MaxWindows)
		}
		if a.ObservedAt.IsZero() || a.ObservedAt.After(now.Add(5*time.Minute)) || a.ObservedAt.Before(now.Add(-24*time.Hour)) {
			a.ObservedAt = now
		}
		for _, w := range a.Windows {
			if !kindRe.MatchString(w.Kind) || math.IsNaN(w.UsedPercent) || w.UsedPercent < 0 || w.UsedPercent > 1000 {
				return fmt.Errorf("each window has a kind like five_hour and used_percent from 0")
			}
		}
	}
	return nil
}

var labels = map[string]string{"claude": "Claude", "chatgpt": "ChatGPT", "cursor": "Cursor", "copilot": "GitHub Copilot"}

// Apply stores a report from computer d, in tx: it updates the computer's
// status, and puts each reading on the AI account it belongs to. An
// account is found by the email the tool is signed in with (anyone's in
// the workspace, since logins can be shared: the reporting person is
// added to it), else the person's only account for that tool. A reading
// for an account nobody has added yet adds it, owned by the person.
// Readings from a source switched off are left out.
func Apply(ctx context.Context, tx pgx.Tx, d Device, r Report) ([]Applied, error) {
	var firstErr *string
	if len(r.Errors) > 0 {
		firstErr = &r.Errors[0].Code
	}
	version := r.AppVersion
	if version == "" {
		version = d.AppVersion
	}
	if _, err := tx.Exec(ctx, `UPDATE ai_devices SET status = $2, last_error_code = $3, app_version = $4, last_report_at = now()
		WHERE id::text = $1`, d.ID, r.Status, firstErr, version); err != nil {
		return nil, err
	}
	off, err := SourcesOff(ctx, tx)
	if err != nil {
		return nil, err
	}
	out := []Applied{}
	for _, a := range r.Accounts {
		if slices.Contains(off[a.Provider], a.Source) {
			out = append(out, Applied{Provider: a.Provider, Skipped: "source_off"})
			continue
		}
		id, created, err := accountFor(ctx, tx, d, a)
		if err != nil {
			return nil, err
		}
		if id == "" {
			out = append(out, Applied{Provider: a.Provider, Skipped: "no_account"})
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE ai_accounts SET
				plan = CASE WHEN $3 <> '' THEN $3 ELSE plan END,
				resets_available = coalesce($4, resets_available),
				updated_at = CASE WHEN ($3 <> '' AND plan IS DISTINCT FROM $3) OR ($4::int IS NOT NULL AND resets_available IS DISTINCT FROM $4) THEN now() ELSE updated_at END
			WHERE workspace_id = $1 AND id::text = $2`, d.WorkspaceID, id, a.Plan, a.ResetsAvailable); err != nil {
			return nil, err
		}
		for _, w := range a.Windows {
			if _, err := tx.Exec(ctx, `INSERT INTO ai_usage_readings (workspace_id, account_id, device_id, window_kind, used_percent, resets_at, source, observed_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, d.WorkspaceID, id, d.ID, w.Kind, math.Round(w.UsedPercent*100)/100, w.ResetsAt, a.Source, a.ObservedAt); err != nil {
				return nil, err
			}
		}
		out = append(out, Applied{Provider: a.Provider, AccountID: id, Created: created})
	}
	return out, nil
}

// accountFor finds or adds the account a reading belongs to. It returns an
// empty id when it can't tell (no email, and the person has none or
// several accounts for that tool).
func accountFor(ctx context.Context, tx pgx.Tx, d Device, a AccountReading) (id string, created bool, err error) {
	if a.AccountEmail != "" {
		err = tx.QueryRow(ctx, `SELECT a.id::text FROM ai_accounts a
			WHERE a.workspace_id = $1 AND a.provider = $2 AND lower(a.account_email) = $3 AND a.cancelled_at IS NULL
			ORDER BY EXISTS (SELECT 1 FROM ai_account_members m WHERE m.account_id = a.id AND m.user_id::text = $4) DESC, a.created_at
			LIMIT 1`, d.WorkspaceID, a.Provider, a.AccountEmail, d.UserID).Scan(&id)
		if err == nil {
			// A shared login: whoever reads it uses it.
			_, err = tx.Exec(ctx, `INSERT INTO ai_account_members (account_id, workspace_id, user_id, role) VALUES ($1, $2, $3, 'user')
				ON CONFLICT DO NOTHING`, id, d.WorkspaceID, d.UserID)
			return id, false, err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", false, err
		}
	} else {
		rows, err := tx.Query(ctx, `SELECT a.id::text FROM ai_accounts a JOIN ai_account_members m ON m.account_id = a.id
			WHERE a.workspace_id = $1 AND a.provider = $2 AND m.user_id::text = $3 AND a.cancelled_at IS NULL LIMIT 2`,
			d.WorkspaceID, a.Provider, d.UserID)
		if err != nil {
			return "", false, err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil || len(ids) != 1 {
			return "", false, err
		}
		return ids[0], false, nil
	}
	// Nobody has added this account yet: add it for the person.
	name := labels[a.Provider]
	if a.Plan != "" {
		name += " " + a.Plan
	}
	provider, email, plan := a.Provider, a.AccountEmail, a.Plan
	in := aiaccounts.Input{Provider: &provider, Name: &name, AccountEmail: &email, Plan: &plan,
		Members: &[]aiaccounts.MemberInput{{UserID: d.UserID, Role: aiaccounts.RoleOwner}}}
	if err := in.Validate(true); err != nil {
		return "", false, err
	}
	if id, err = aiaccounts.Create(ctx, tx, d.WorkspaceID, in, &d.UserID); err != nil {
		return "", false, err
	}
	var actor string
	if err := tx.QueryRow(ctx, `SELECT actor_id::text FROM users WHERE id::text = $1`, d.UserID).Scan(&actor); err != nil {
		return "", false, err
	}
	props := map[string]any{"provider": a.Provider, "source": "desktop"}
	return id, true, audit.Record(ctx, tx, audit.Event{WorkspaceID: d.WorkspaceID, ActorID: actor, ActorType: "human", UserID: d.UserID,
		Action: "ai_account.created", TargetType: "ai_account", TargetID: id, Data: props, Props: props})
}
