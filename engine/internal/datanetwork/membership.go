package datanetwork

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/outcomes"
)

// Querier is a pool or a transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Membership statuses.
const (
	NotJoined = "not_joined"
	Joined    = "joined"
	Left      = "left"
)

// Membership is a workspace's place in the network.
type Membership struct {
	Status        string
	JoinedAt      *time.Time
	LeftAt        *time.Time
	SharedThrough *time.Time
	// WithdrawDueAt is when what it shared must be gone, while that is
	// still to happen.
	WithdrawDueAt *time.Time
	WithdrawnAt   *time.Time
}

// Load returns a workspace's membership; NotJoined when it never joined.
func Load(ctx context.Context, q Querier, ws string) (Membership, error) {
	m := Membership{}
	var joinedAt time.Time
	err := q.QueryRow(ctx, `
		SELECT status, joined_at, left_at, shared_through, withdraw_due_at, withdrawn_at
		FROM data_network_members WHERE workspace_id = $1`, ws).
		Scan(&m.Status, &joinedAt, &m.LeftAt, &m.SharedThrough, &m.WithdrawDueAt, &m.WithdrawnAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Membership{Status: NotJoined}, nil
	}
	if err != nil {
		return m, err
	}
	m.JoinedAt = &joinedAt
	return m, nil
}

// Join makes the workspace a member and reports whether that changed
// anything. Run it in the transaction that records the audit event. A
// workspace that left and comes back before its withdrawal ran keeps what
// it shared; one whose withdrawal ran shares its past sends again.
func Join(ctx context.Context, q Querier, ws, userID string) (bool, error) {
	tag, err := q.Exec(ctx, `
		INSERT INTO data_network_members (workspace_id, status, joined_at, joined_by)
		VALUES ($1, 'joined', now(), $2)
		ON CONFLICT (workspace_id) DO UPDATE SET
			status = 'joined', joined_at = now(), joined_by = $2, left_at = NULL, left_by = NULL,
			shared_through = CASE WHEN data_network_members.withdrawn_at IS NOT NULL THEN NULL
				ELSE data_network_members.shared_through END,
			withdraw_due_at = NULL, withdrawn_at = NULL, updated_at = now()
		WHERE data_network_members.status <> 'joined'`, ws, nullUUID(userID))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// Leave stops sharing and lookups beyond the allowance, and queues the
// withdrawal of everything the workspace shared. It reports whether that
// changed anything. Run it in the transaction that records the audit event.
func Leave(ctx context.Context, q Querier, ws, userID string) (bool, error) {
	var due time.Time
	err := q.QueryRow(ctx, `
		UPDATE data_network_members SET status = 'left', left_at = now(), left_by = $2,
			withdraw_due_at = now() + $3 * interval '1 second', withdrawn_at = NULL, updated_at = now()
		WHERE workspace_id = $1 AND status = 'joined'
		RETURNING withdraw_due_at`, ws, nullUUID(userID), int64(WithdrawWithin/time.Second)).Scan(&due)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, enqueueWithdraw(ctx, q, ws, due, "leave")
}

// enqueueWithdraw queues a withdrawal; attempt makes the key unique per
// try, so a withdrawal whose job died is queued again on the next day.
func enqueueWithdraw(ctx context.Context, q jobs.Querier, ws string, due time.Time, attempt string) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobWithdraw, WorkspaceID: &ws, MaxAttempts: 10,
		IdempotencyKey: "datanetwork.withdraw:" + ws + ":" + due.UTC().Format(time.RFC3339) + ":" + attempt})
	return err
}

// Preview counts what joining would share now: the addresses the
// workspace emailed, by outcome, and their domains. Names, lists and
// replies are never shared, so they have no count.
type Preview struct {
	Addresses int
	Delivered int
	Bounced   int
	Domains   int
}

// PreviewCounts counts what would be shared if the workspace joined now.
func PreviewCounts(ctx context.Context, q Querier, ws string) (Preview, error) {
	var p Preview
	err := q.QueryRow(ctx, `
		WITH shared AS (`+sharedSQL+`)
		SELECT count(*)::int, count(*) FILTER (WHERE NOT bounced)::int, count(*) FILTER (WHERE bounced)::int,
		       count(DISTINCT domain)::int
		FROM (SELECT email, split_part(email, '@', 2) AS domain, bool_or(bounced) AS bounced
		      FROM shared GROUP BY email) a`,
		ws, time.Time{}, time.Now().Add(-SettleAfter), maxPreviewRows, outcomes.FreeMailDomains()).
		Scan(&p.Addresses, &p.Delivered, &p.Bounced, &p.Domains)
	return p, err
}

// maxPreviewRows caps the sends a preview reads, so it stays quick.
const maxPreviewRows = 1_000_000

func nullUUID(s string) any {
	if s == "" {
		return nil
	}
	return s
}
