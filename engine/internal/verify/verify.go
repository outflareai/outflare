// Package verify checks that lead addresses exist before they are emailed,
// with the workspace's chosen verification connector or the free built-in
// checks, and keeps the latest result per address.
//
// Undeliverable addresses are never emailed. The workspace decides whether
// risky, catch-all and unknown addresses are blocked too, and whether a
// lead's first email waits for a fresh result.
package verify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/verifiers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/mailbox"
)

// JobVerify verifies a batch of addresses in one workspace.
const JobVerify = "verify.emails"

// MaxBatch is the most addresses one job verifies.
const MaxBatch = 200

// Settings is a workspace's verification policy.
type Settings struct {
	IntegrationID    *string `json:"integration_id"`
	VerifyOnImport   bool    `json:"verify_on_import"`
	VerifyBeforeSend bool    `json:"verify_before_send"`
	BlockRisky       bool    `json:"block_risky"`
	BlockCatchAll    bool    `json:"block_catch_all"`
	BlockUnknown     bool    `json:"block_unknown"`
	MaxAgeDays       int     `json:"max_age_days"`
}

// Defaults apply until a workspace saves its own settings.
var Defaults = Settings{VerifyOnImport: true, VerifyBeforeSend: true, MaxAgeDays: 90}

// Querier is a pool or a transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// LoadSettings returns the workspace's settings, or the defaults.
func LoadSettings(ctx context.Context, q Querier, ws string) (Settings, error) {
	s := Defaults
	err := q.QueryRow(ctx, `
		SELECT integration_id, verify_on_import, verify_before_send, block_risky, block_catch_all, block_unknown, max_age_days
		FROM verification_settings WHERE workspace_id = $1`, ws).
		Scan(&s.IntegrationID, &s.VerifyOnImport, &s.VerifyBeforeSend, &s.BlockRisky, &s.BlockCatchAll, &s.BlockUnknown, &s.MaxAgeDays)
	if errors.Is(err, pgx.ErrNoRows) {
		return Defaults, nil
	}
	return s, err
}

// SaveSettings stores the workspace's settings.
func SaveSettings(ctx context.Context, tx pgx.Tx, ws string, s Settings) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO verification_settings (workspace_id, integration_id, verify_on_import, verify_before_send,
			block_risky, block_catch_all, block_unknown, max_age_days, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
		ON CONFLICT (workspace_id) DO UPDATE SET integration_id = EXCLUDED.integration_id,
			verify_on_import = EXCLUDED.verify_on_import, verify_before_send = EXCLUDED.verify_before_send,
			block_risky = EXCLUDED.block_risky, block_catch_all = EXCLUDED.block_catch_all,
			block_unknown = EXCLUDED.block_unknown, max_age_days = EXCLUDED.max_age_days, updated_at = now()`,
		ws, s.IntegrationID, s.VerifyOnImport, s.VerifyBeforeSend, s.BlockRisky, s.BlockCatchAll, s.BlockUnknown, s.MaxAgeDays)
	return err
}

// Result is a stored verification.
type Result struct {
	connectors.Verification
	Provider      string    `json:"provider"`
	IntegrationID *string   `json:"integration_id"`
	CheckedAt     time.Time `json:"checked_at"`
}

// Blocked says whether settings forbid emailing an address with this result,
// and why.
func (s Settings) Blocked(r Result) (bool, string) {
	switch {
	case r.Status == connectors.Undeliverable:
		return true, "verification_undeliverable"
	case r.CatchAll && s.BlockCatchAll:
		return true, "verification_catch_all"
	case r.Status == connectors.Risky && s.BlockRisky:
		return true, "verification_risky"
	case r.Status == connectors.Unknown && s.BlockUnknown:
		return true, "verification_unknown"
	}
	return false, ""
}

// Fresh says whether a result is recent enough to rely on.
func (s Settings) Fresh(r Result, now time.Time) bool {
	return now.Sub(r.CheckedAt) < time.Duration(s.MaxAgeDays)*24*time.Hour
}

const resultCols = `email, status, reason, catch_all, disposable, role, free, suggestion, score, provider, provider_status, integration_id, checked_at`

func scanResult(row pgx.Row) (Result, error) {
	var r Result
	var score *int16
	err := row.Scan(&r.Email, &r.Status, &r.Reason, &r.CatchAll, &r.Disposable, &r.Role, &r.Free, &r.Suggestion,
		&score, &r.Provider, &r.ProviderStatus, &r.IntegrationID, &r.CheckedAt)
	if score != nil {
		n := int(*score)
		r.Score = &n
	}
	return r, err
}

// Get returns the stored result for an address, or nil.
func Get(ctx context.Context, q Querier, ws, email string) (*Result, error) {
	r, err := scanResult(q.QueryRow(ctx, `SELECT `+resultCols+` FROM email_verifications WHERE workspace_id = $1 AND email = $2`,
		ws, strings.ToLower(strings.TrimSpace(email))))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &r, err
}

// List returns stored results, newest first, optionally for one status.
// List answers results newest first, skipping offset of them.
func List(ctx context.Context, q Querier, ws, status string, emails []string, limit, offset int) ([]Result, error) {
	for i := range emails {
		emails[i] = strings.ToLower(strings.TrimSpace(emails[i]))
	}
	rows, err := q.Query(ctx, `SELECT `+resultCols+` FROM email_verifications
		WHERE workspace_id = $1 AND ($2 = '' OR status = $2) AND (cardinality($3::text[]) = 0 OR email = ANY($3))
		ORDER BY checked_at DESC, email LIMIT $4 OFFSET $5`, ws, status, emails, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Result{}
	for rows.Next() {
		r, err := scanResult(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type payload struct {
	Emails []string `json:"emails"`
	// Force checks again even when a fresh result exists.
	Force bool `json:"force"`
}

// Enqueue schedules verification of emails in batches. Call it in the
// transaction that made the addresses known. It returns the job ids.
func Enqueue(ctx context.Context, q jobs.Querier, ws string, emails []string, force bool) ([]int64, error) {
	seen := map[string]bool{}
	var clean []string
	for _, e := range emails {
		e = strings.ToLower(strings.TrimSpace(e))
		if e != "" && !seen[e] {
			seen[e] = true
			clean = append(clean, e)
		}
	}
	var ids []int64
	for len(clean) > 0 {
		n := min(len(clean), MaxBatch)
		key := ""
		if n == 1 && !force {
			// One pending check per address per day, however many sends ask.
			key = ws + ":" + clean[0] + ":" + time.Now().UTC().Format("2006-01-02")
		}
		id, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobVerify, WorkspaceID: &ws,
			Payload: payload{Emails: clean[:n], Force: force}, IdempotencyKey: key, MaxAttempts: 5})
		if err != nil {
			return ids, err
		}
		ids = append(ids, id)
		clean = clean[n:]
	}
	return ids, nil
}

// OnImport queues verification of newly imported addresses when the
// workspace verifies on import.
func OnImport(ctx context.Context, tx pgx.Tx, ws string, emails []string) error {
	s, err := LoadSettings(ctx, tx, ws)
	if err != nil || !s.VerifyOnImport || len(emails) == 0 {
		return err
	}
	_, err = Enqueue(ctx, tx, ws, emails, false)
	return err
}

// Service runs verification jobs.
type Service struct {
	Pool    *pgxpool.Pool
	Store   *connectors.Store
	Builtin verifiers.Builtin
	Log     *slog.Logger
	// Concurrency is how many addresses are checked at once per job.
	Concurrency int
}

// Register adds the job handler.
func (s *Service) Register(q *jobs.Queue) { q.Handle(JobVerify, s.job) }

func (s *Service) job(ctx context.Context, job jobs.Job) error {
	var p payload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	_, err := s.Verify(ctx, *job.WorkspaceID, p.Emails, p.Force)
	return err
}

// Verify checks emails now with the workspace's verifier and stores the
// results. Addresses with a fresh result are skipped unless force is set.
// If the connected service refuses the key or is out of credits, the
// batch falls back to the built-in checks so sending is not held up, and
// the integration is marked with the error.
func (s *Service) Verify(ctx context.Context, ws string, emails []string, force bool) ([]Result, error) {
	set, err := LoadSettings(ctx, s.Pool, ws)
	if err != nil {
		return nil, err
	}
	if !force {
		existing, err := List(ctx, s.Pool, ws, "", append([]string(nil), emails...), len(emails), 0)
		if err != nil {
			return nil, err
		}
		fresh := map[string]bool{}
		now := time.Now()
		for _, r := range existing {
			if set.Fresh(r, now) {
				fresh[r.Email] = true
			}
		}
		var todo []string
		for _, e := range emails {
			if !fresh[strings.ToLower(e)] {
				todo = append(todo, e)
			}
		}
		emails = todo
	}
	if len(emails) == 0 {
		return []Result{}, nil
	}
	test, err := mailbox.IsTestWorkspace(ctx, s.Pool, ws)
	if err != nil {
		return nil, err
	}
	if test {
		// A test workspace never spends a provider's credits.
		results := make([]Result, len(emails))
		for i, e := range emails {
			results[i] = Result{Verification: Simulated(e), Provider: SimulatedProvider, CheckedAt: time.Now()}
		}
		return results, s.save(ctx, ws, results)
	}

	var v connectors.Verifier = s.Builtin
	var opened *connectors.Opened
	if set.IntegrationID != nil && s.Store != nil {
		opened, err = s.Store.Open(ctx, s.Pool, ws, *set.IntegrationID)
		switch {
		case errors.Is(err, connectors.ErrNotFound):
			opened = nil
		case err != nil && connectors.KindOf(err) == connectors.ErrAuth:
			opened = nil
		case err != nil:
			return nil, err
		default:
			pv, ok := opened.Connector.(connectors.Verifier)
			if !ok {
				return nil, fmt.Errorf("%w: integration %s cannot verify emails", jobs.ErrPermanent, opened.ID)
			}
			v = pv
		}
	}

	conc := s.Concurrency
	if conc <= 0 {
		conc = 5
	}
	results := make([]Result, len(emails))
	errs := make([]error, len(emails))
	var wg sync.WaitGroup
	sem := make(chan struct{}, conc)
	var stopped error
	var mu sync.Mutex
	for i, e := range emails {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, e string) {
			defer wg.Done()
			defer func() { <-sem }()
			mu.Lock()
			halt := stopped
			mu.Unlock()
			if halt != nil {
				errs[i] = halt
				return
			}
			cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
			defer cancel()
			var r connectors.Verification
			var err error
			if opened != nil {
				r, err = v.Verify(cctx, opened.Client, opened.Creds, e)
			} else {
				r, err = s.Builtin.Verify(cctx, nil, nil, e)
			}
			if err != nil {
				errs[i] = err
				if k := connectors.KindOf(err); k == connectors.ErrAuth || k == connectors.ErrQuota {
					mu.Lock()
					stopped = err
					mu.Unlock()
				}
				return
			}
			r.Email = strings.ToLower(e)
			res := Result{Verification: r, Provider: verifiers.BuiltinID, CheckedAt: time.Now()}
			if opened != nil {
				res.Provider, res.IntegrationID = opened.Connector.Definition().ID, &opened.ID
			}
			results[i] = res
		}(i, e)
	}
	wg.Wait()

	if opened != nil {
		_ = s.Store.Checked(ctx, s.Pool, ws, opened.ID, &opened.Account, stopped)
	}
	var keep []Result
	var retry []string
	var lastErr error
	for i := range emails {
		switch {
		case errs[i] == nil:
			keep = append(keep, results[i])
		case stopped != nil:
			// Out of credits or a refused key: fall back to the free checks.
			r, err := s.Builtin.Verify(ctx, nil, nil, emails[i])
			if err != nil {
				retry = append(retry, emails[i])
				continue
			}
			r.Email = strings.ToLower(emails[i])
			r.Reason = strings.TrimSpace(r.Reason + " (built-in fallback: " + connectors.KindOf(stopped) + ")")
			keep = append(keep, Result{Verification: r, Provider: verifiers.BuiltinID, CheckedAt: time.Now()})
		default:
			retry, lastErr = append(retry, emails[i]), errs[i]
		}
	}
	if err := s.save(ctx, ws, keep); err != nil {
		return nil, err
	}
	if stopped != nil && s.Log != nil {
		s.Log.Warn("verification fell back to built-in checks", "workspace", ws, "err", stopped)
	}
	if len(retry) > 0 {
		if _, err := Enqueue(ctx, s.Pool, ws, retry, force); err != nil {
			return keep, err
		}
		if s.Log != nil {
			s.Log.Warn("verification will retry", "workspace", ws, "count", len(retry), "err", lastErr)
		}
	}
	return keep, nil
}

func (s *Service) save(ctx context.Context, ws string, rs []Result) error {
	if len(rs) == 0 {
		return nil
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		for _, r := range rs {
			var score *int
			if r.Score != nil {
				score = r.Score
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO email_verifications (workspace_id, email, status, reason, catch_all, disposable, role, free,
					suggestion, score, provider, provider_status, integration_id, checked_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
				ON CONFLICT (workspace_id, email) DO UPDATE SET status = EXCLUDED.status, reason = EXCLUDED.reason,
					catch_all = EXCLUDED.catch_all, disposable = EXCLUDED.disposable, role = EXCLUDED.role, free = EXCLUDED.free,
					suggestion = EXCLUDED.suggestion, score = EXCLUDED.score, provider = EXCLUDED.provider,
					provider_status = EXCLUDED.provider_status, integration_id = EXCLUDED.integration_id, checked_at = EXCLUDED.checked_at`,
				ws, r.Email, r.Status, truncate(r.Reason, 200), r.CatchAll, r.Disposable, r.Role, r.Free,
				truncate(r.Suggestion, 320), score, r.Provider, truncate(r.ProviderStatus, 100), r.IntegrationID, r.CheckedAt); err != nil {
				return err
			}
		}
		return nil
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Gate decides whether a lead's email may go now. It is part of the send
// policy: blocked addresses end the enrollment; an address waiting for a
// result defers the send and queues the check.
type Gate struct {
	Allow  bool
	Defer  bool
	Reason string
}

// Check runs the gate for one address inside the send transaction.
func Check(ctx context.Context, tx pgx.Tx, ws, email string, firstEmail bool, now time.Time) (Gate, error) {
	set, err := LoadSettings(ctx, tx, ws)
	if err != nil {
		return Gate{}, err
	}
	r, err := Get(ctx, tx, ws, email)
	if err != nil {
		return Gate{}, err
	}
	if r != nil {
		if blocked, why := set.Blocked(*r); blocked {
			return Gate{Reason: why}, nil
		}
	}
	// Follow-ups never wait: the lead already got the first email.
	if !firstEmail || !set.VerifyBeforeSend || (r != nil && set.Fresh(*r, now)) {
		return Gate{Allow: true}, nil
	}
	if _, err := Enqueue(ctx, tx, ws, []string{email}, false); err != nil {
		return Gate{}, err
	}
	return Gate{Defer: true, Reason: "awaiting_verification"}, nil
}
