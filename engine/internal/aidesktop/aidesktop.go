// Package aidesktop is the engine side of Outflare for desktop
// (plans/ai-accounts, step 4): the tray app each person installs reads
// usage from the AI tools on their computer and reports only numbers.
//
// A computer is linked once, the way a TV app links to an account: the app
// asks for a link (NewLink) and shows a short user code, the person
// approves that code while signed in to Outflare (Approve), and the app's
// next poll gets a device token (Claim). The token then authenticates its
// reports (Apply) until the person or an admin unlinks it.
package aidesktop

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

const (
	// PrefixDevice is a linked computer's token; PrefixDeviceCode the
	// secret the app polls with while its link waits for approval.
	PrefixDevice     = "ofd_"
	PrefixDeviceCode = "ofdc_"

	// LinkTTL is how long a person has to approve a link.
	LinkTTL = 15 * time.Minute
	// PollInterval is how often the app may ask whether its link was
	// approved.
	PollInterval = 5 * time.Second
	// ReadingsKeptFor is how long usage readings are kept.
	ReadingsKeptFor = 90 * 24 * time.Hour
	// SilentAfter is how long a computer may go without reporting before
	// it shows as silent.
	SilentAfter = time.Hour
)

// Providers are the AI tools the app reads (ai_accounts providers).
var Providers = []string{"claude", "chatgpt", "cursor", "copilot"}

// Sources are the two ways the app reads a tool: official, the tool's own
// documented way; extended, the endpoints the tool's own app calls with
// the login already on the computer.
var Sources = []string{"official", "extended"}

// Platforms the app runs on.
var Platforms = []string{"macos", "windows", "linux"}

var (
	ErrNoLink      = errors.New("no link with that code")
	ErrLinkExpired = errors.New("the link expired")
	ErrLinkUsed    = errors.New("the link was already used")
	ErrPending     = errors.New("the link isn't approved yet")
	ErrBadToken    = errors.New("unknown or unlinked device token")
	ErrNotFound    = errors.New("no linked computer with that id")
)

// Querier is a pool or a transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Link is what the app gets when it asks to be linked.
type Link struct {
	// DeviceCode is the secret the app polls with; only its hash is kept.
	DeviceCode string `json:"device_code"`
	// UserCode is what the person checks and approves, like ABCD-2345.
	UserCode  string `json:"user_code"`
	ExpiresIn int    `json:"expires_in"`
	Interval  int    `json:"interval"`
}

// userCodeLetters leaves out letters people mix up (0/O, 1/I/L).
const userCodeLetters = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

func newUserCode() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	out := make([]byte, 0, 9)
	for i, c := range b {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, userCodeLetters[int(c)%len(userCodeLetters)])
	}
	return string(out)
}

// NormalizeUserCode upper-cases a typed code and puts back its dash.
func NormalizeUserCode(s string) string {
	s = strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(s)))
	if len(s) != 8 {
		return s
	}
	return s[:4] + "-" + s[4:]
}

// NewLink starts linking a computer.
func NewLink(ctx context.Context, q Querier, name, platform, appVersion string) (Link, error) {
	secret, hash := auth.NewToken(PrefixDeviceCode)
	for range 5 {
		code := newUserCode()
		_, err := q.Exec(ctx, `INSERT INTO ai_device_links (device_code_hash, user_code, name, platform, app_version, expires_at)
			VALUES ($1, $2, $3, $4, $5, now() + make_interval(secs => $6))`,
			hash, code, name, platform, appVersion, LinkTTL.Seconds())
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			continue // the user code was taken; try another
		}
		if err != nil {
			return Link{}, err
		}
		return Link{DeviceCode: secret, UserCode: code, ExpiresIn: int(LinkTTL.Seconds()), Interval: int(PollInterval.Seconds())}, nil
	}
	return Link{}, errors.New("no free user code")
}

// Approval is the computer a person just approved.
type Approval struct {
	Name     string `json:"name"`
	Platform string `json:"platform"`
}

// Approve links the computer showing userCode to userID in workspaceID,
// in tx. The app's next Claim gets its token.
func Approve(ctx context.Context, tx pgx.Tx, workspaceID, userID, userCode string) (Approval, error) {
	var a Approval
	var expired, approved bool
	err := tx.QueryRow(ctx, `SELECT name, platform, expires_at < now(), approved_at IS NOT NULL FROM ai_device_links
		WHERE user_code = $1 FOR UPDATE`, NormalizeUserCode(userCode)).Scan(&a.Name, &a.Platform, &expired, &approved)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return a, ErrNoLink
	case err != nil:
		return a, err
	case expired:
		return a, ErrLinkExpired
	case approved:
		return a, ErrLinkUsed
	}
	if _, err := tx.Exec(ctx, `UPDATE ai_device_links SET workspace_id = $2, user_id = $3, approved_at = now() WHERE user_code = $1`,
		NormalizeUserCode(userCode), workspaceID, userID); err != nil {
		return a, err
	}
	return a, nil
}

// Device is a linked computer.
type Device struct {
	ID            string     `json:"id"`
	WorkspaceID   string     `json:"-"`
	UserID        string     `json:"user_id"`
	UserName      string     `json:"user_name"`
	Name          string     `json:"name"`
	Platform      string     `json:"platform"`
	AppVersion    string     `json:"app_version"`
	Status        string     `json:"status"`
	LastErrorCode *string    `json:"last_error_code"`
	LastReportAt  *time.Time `json:"last_report_at"`
	// Silent: no report for over an hour (the app isn't running, or the
	// computer is off).
	Silent    bool      `json:"silent"`
	CreatedAt time.Time `json:"created_at"`
}

// Claim gives the app its token once its link is approved, in tx, and
// records ai_device.linked.
func Claim(ctx context.Context, tx pgx.Tx, deviceCode string) (token string, d Device, err error) {
	var id string
	var ws, user *string
	var expired, claimed bool
	err = tx.QueryRow(ctx, `SELECT id::text, workspace_id::text, user_id::text, name, platform, app_version, expires_at < now(), claimed_at IS NOT NULL
		FROM ai_device_links WHERE device_code_hash = $1 FOR UPDATE`, auth.HashToken(deviceCode)).
		Scan(&id, &ws, &user, &d.Name, &d.Platform, &d.AppVersion, &expired, &claimed)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", d, ErrNoLink
	case err != nil:
		return "", d, err
	case claimed:
		return "", d, ErrLinkUsed
	case ws == nil && expired:
		return "", d, ErrLinkExpired
	case ws == nil:
		return "", d, ErrPending
	}
	token, hash := auth.NewToken(PrefixDevice)
	d.WorkspaceID, d.UserID, d.Status = *ws, *user, "ok"
	if err := tx.QueryRow(ctx, `INSERT INTO ai_devices (workspace_id, user_id, token_hash, name, platform, app_version)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text, created_at`, d.WorkspaceID, d.UserID, hash, d.Name, d.Platform, d.AppVersion).
		Scan(&d.ID, &d.CreatedAt); err != nil {
		return "", d, err
	}
	if _, err := tx.Exec(ctx, `UPDATE ai_device_links SET claimed_at = now() WHERE id::text = $1`, id); err != nil {
		return "", d, err
	}
	return token, d, record(ctx, tx, d.WorkspaceID, d.UserID, "ai_device.linked", d.ID, map[string]any{"platform": d.Platform})
}

// Authenticate returns the linked computer a token belongs to.
func Authenticate(ctx context.Context, q Querier, token string) (Device, error) {
	var d Device
	if !strings.HasPrefix(token, PrefixDevice) {
		return d, ErrBadToken
	}
	err := q.QueryRow(ctx, `SELECT d.id::text, d.workspace_id::text, d.user_id::text, d.name, d.platform, d.app_version, d.status
		FROM ai_devices d
		JOIN memberships m ON m.workspace_id = d.workspace_id AND m.user_id = d.user_id
		WHERE d.token_hash = $1 AND d.unlinked_at IS NULL`, auth.HashToken(token)).
		Scan(&d.ID, &d.WorkspaceID, &d.UserID, &d.Name, &d.Platform, &d.AppVersion, &d.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrBadToken // unknown, unlinked, or its person left the workspace
	}
	return d, err
}

// List returns the workspace's linked computers, newest first: one
// person's when userID is set, else everyone's.
func List(ctx context.Context, q Querier, workspaceID, userID string) ([]Device, error) {
	rows, err := q.Query(ctx, `SELECT d.id::text, d.user_id::text, u.name, d.name, d.platform, d.app_version, d.status, d.last_error_code,
			d.last_report_at, coalesce(d.last_report_at, d.created_at) < now() - make_interval(secs => $3), d.created_at
		FROM ai_devices d JOIN users u ON u.id = d.user_id
		WHERE d.workspace_id = $1 AND d.unlinked_at IS NULL AND ($2 = '' OR d.user_id::text = $2)
		ORDER BY d.created_at DESC`, workspaceID, userID, SilentAfter.Seconds())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Device, error) {
		var d Device
		err := row.Scan(&d.ID, &d.UserID, &d.UserName, &d.Name, &d.Platform, &d.AppVersion, &d.Status, &d.LastErrorCode,
			&d.LastReportAt, &d.Silent, &d.CreatedAt)
		return d, err
	})
}

// Unlink revokes a computer's token, in tx, and records
// ai_device.unlinked. byUserID is who did it. The app finds out on its
// next report.
func Unlink(ctx context.Context, tx pgx.Tx, workspaceID, id, byUserID string) (Device, error) {
	var d Device
	err := tx.QueryRow(ctx, `UPDATE ai_devices SET unlinked_at = now()
		WHERE workspace_id = $1 AND id::text = $2 AND unlinked_at IS NULL
		RETURNING id::text, user_id::text, platform`, workspaceID, id).Scan(&d.ID, &d.UserID, &d.Platform)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return d, err
	}
	return d, record(ctx, tx, workspaceID, byUserID, "ai_device.unlinked", d.ID, map[string]any{"platform": d.Platform, "own": d.UserID == byUserID})
}

// Owner returns who a linked computer belongs to.
func Owner(ctx context.Context, q Querier, workspaceID, id string) (string, error) {
	var user string
	err := q.QueryRow(ctx, `SELECT user_id::text FROM ai_devices WHERE workspace_id = $1 AND id::text = $2 AND unlinked_at IS NULL`,
		workspaceID, id).Scan(&user)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return user, err
}

// SourcesOff returns the sources switched off for every computer, by
// provider.
func SourcesOff(ctx context.Context, q Querier) (map[string][]string, error) {
	rows, err := q.Query(ctx, `SELECT provider, source FROM ai_desktop_sources_off ORDER BY provider, source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var p, s string
		if err := rows.Scan(&p, &s); err != nil {
			return nil, err
		}
		out[p] = append(out[p], s)
	}
	return out, rows.Err()
}

// SetSource switches a provider's source on or off for every computer.
func SetSource(ctx context.Context, q Querier, provider, source string, on bool, reason string) error {
	if on {
		_, err := q.Exec(ctx, `DELETE FROM ai_desktop_sources_off WHERE provider = $1 AND source = $2`, provider, source)
		return err
	}
	_, err := q.Exec(ctx, `INSERT INTO ai_desktop_sources_off (provider, source, reason) VALUES ($1, $2, $3)
		ON CONFLICT (provider, source) DO UPDATE SET reason = EXCLUDED.reason`, provider, source, reason)
	return err
}

// Purge deletes readings older than ReadingsKeptFor and links that expired
// a day ago, and returns how many readings went.
func Purge(ctx context.Context, q Querier) (int64, error) {
	if _, err := q.Exec(ctx, `DELETE FROM ai_device_links WHERE expires_at < now() - interval '1 day'`); err != nil {
		return 0, err
	}
	tag, err := q.Exec(ctx, `DELETE FROM ai_usage_readings WHERE observed_at < now() - make_interval(secs => $1)`, ReadingsKeptFor.Seconds())
	return tag.RowsAffected(), err
}

// Counts returns how many linked computers report, and how many are
// silent, across the server.
func Counts(ctx context.Context, q Querier) (reporting, silent int64, err error) {
	err = q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE last_report_at >= now() - make_interval(secs => $1)),
			count(*) FILTER (WHERE coalesce(last_report_at, created_at) < now() - make_interval(secs => $1))
		FROM ai_devices WHERE unlinked_at IS NULL`, SilentAfter.Seconds()).Scan(&reporting, &silent)
	return reporting, silent, err
}

// Run keeps readings to 90 days and reports how many computers are
// reporting, now and then every interval.
func Run(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := Purge(ctx, pool); err != nil {
			if ctx.Err() == nil {
				track.Fail(ctx, log, track.Event{Name: "ai_device.purge_failed", ActorType: "system", Surface: "system"}, "internal", err)
			}
		} else {
			metrics.DesktopPurged(n)
		}
		if reporting, silent, err := Counts(ctx, pool); err == nil {
			metrics.DesktopDevices(reporting, silent)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// record writes an audit event by userID's actor.
func record(ctx context.Context, tx pgx.Tx, workspaceID, userID, action, deviceID string, props map[string]any) error {
	e := audit.Event{WorkspaceID: workspaceID, ActorType: "system", Action: action, TargetType: "ai_device", TargetID: deviceID,
		Data: props, Props: props}
	if userID != "" {
		if err := tx.QueryRow(ctx, `SELECT actor_id::text FROM users WHERE id::text = $1`, userID).Scan(&e.ActorID); err != nil {
			return err
		}
		e.ActorType, e.UserID = "human", userID
	}
	return audit.Record(ctx, tx, e)
}
