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
	"math"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/verifiers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/mailbox"
	"github.com/outflareai/outflare/engine/internal/outcomes"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/usagelimits"
)

// JobVerify verifies a batch of addresses in one workspace.
const JobVerify = "verify.emails"

// MaxBatch is the most addresses one job verifies.
const MaxBatch = 200

// RetryDelay is how long addresses the verifier could not answer wait
// before they are asked again. Without it a verifier that answers nobody
// (down, or too busy) is asked again at once, batch after batch, and the
// retries keep it busy.
const RetryDelay = 5 * time.Minute

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
	return enqueueAt(ctx, q, ws, emails, force, time.Time{})
}

// enqueueAt is Enqueue for batches that start at a given time (now when
// zero).
func enqueueAt(ctx context.Context, q jobs.Querier, ws string, emails []string, force bool, at time.Time) ([]int64, error) {
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
			Payload: payload{Emails: clean[:n], Force: force}, RunAt: at, IdempotencyKey: key, MaxAttempts: 5})
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
//
// A chosen verifier whose first check can be unavailable
// (connectors.FallbackVerifier, such as a gateway) is given the
// workspace's other connected verifier, when it has one, to ask instead;
// each address that one answers is booked to its own connection.
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

	// A paid service checks at most what its usage limits allow (one
	// credit an address); the rest get the free checks.
	var limited []string
	if opened != nil {
		left, capped, err := usagelimits.Allowance(ctx, s.Pool, ws, opened.ID, "credit")
		if err != nil {
			return nil, err
		}
		if n := int(math.Floor(left + 1e-9)); capped && n < len(emails) {
			emails, limited = emails[:n], emails[n:]
		}
	}

	// A verifier whose first check can be unavailable gets the workspace's
	// other verifier to ask instead, when it has one.
	var second *secondVerifier
	if opened != nil {
		if _, ok := v.(connectors.FallbackVerifier); ok {
			if second, err = s.secondVerifier(ctx, ws, opened.ID, opened.Connector.Definition().ID); err != nil {
				return nil, err
			}
		}
	}
	// A verifier that can use several accounts gets the workspace's other
	// connections to the same connector: a call the chosen one has no room
	// for, or refuses, is made on the next. accounts[0] is the chosen one.
	var accounts []*connectors.Opened
	var across connectors.AccountsVerifier
	if opened != nil {
		if av, ok := v.(connectors.AccountsVerifier); ok {
			if accounts, err = s.sameConnector(ctx, ws, opened); err != nil {
				return nil, err
			}
			if len(accounts) > 1 {
				across = av
			}
		}
	}
	creds := make([]connectors.Creds, len(accounts))
	for i, o := range accounts {
		creds[i] = o.Creds
	}
	// One address gets 40 seconds, or what the verifier says it needs.
	perAddress := 40 * time.Second
	if sv, ok := v.(connectors.SlowVerifier); ok && opened != nil && sv.VerifyWait() > perAddress {
		perAddress = sv.VerifyWait()
	}

	conc := s.Concurrency
	if conc <= 0 {
		conc = 5
	}
	results := make([]Result, len(emails))
	errs := make([]error, len(emails))
	// answered is the account (accounts) that took each address's check.
	answered := make([]int, len(emails))
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
			cctx, cancel := context.WithTimeout(ctx, perAddress)
			defer cancel()
			var r connectors.Verification
			var err error
			if across != nil {
				var fallback func(context.Context, string) (connectors.Verification, error)
				if second != nil {
					fallback = second.verify
				}
				r, answered[i], err = across.VerifyAcross(cctx, opened.Client, creds, e, fallback)
				answered[i] = min(max(answered[i], 0), len(accounts)-1)
			} else if opened != nil && second != nil {
				r, err = v.(connectors.FallbackVerifier).VerifyWith(cctx, opened.Client, opened.Creds, e, second.verify)
			} else if opened != nil {
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
			if across != nil && answered[i] > 0 {
				// Another of the workspace's accounts took the check.
				res.IntegrationID = &accounts[answered[i]].ID
			}
			results[i] = res
		}(i, e)
	}
	wg.Wait()

	var keep []Result
	if opened != nil {
		_ = s.Store.Checked(ctx, s.Pool, ws, opened.ID, &opened.Account, stopped)
		if across == nil {
			if err := s.book(ctx, ws, opened, errs, results); err != nil {
				return nil, err
			}
		}
	}
	if across != nil {
		// Each check is booked to the account that took it.
		took := make([]int, len(accounts))
		cost := make([]float64, len(accounts))
		for i, err := range errs {
			if err == nil {
				took[answered[i]]++
				cost[answered[i]] += results[i].CostMicros
			}
		}
		for k, o := range accounts {
			if err := s.bookChecks(ctx, ws, o, took[k], cost[k]); err != nil {
				return nil, err
			}
			if k > 0 && took[k] > 0 {
				track.Emit(ctx, track.Event{Name: "email.verifier_handed_over", WorkspaceID: ws, ActorType: "system", Surface: "job",
					Props: map[string]any{"connector": o.Connector.Definition().ID, "integration_id": o.ID, "from_integration_id": opened.ID, "addresses": took[k]}})
			}
		}
	}
	if second != nil {
		if err := second.settle(ctx); err != nil {
			return nil, err
		}
	}
	for _, e := range limited {
		r, err := s.Builtin.Verify(ctx, nil, nil, e)
		if err != nil {
			continue // checked again with the next batch that asks
		}
		r.Email = strings.ToLower(e)
		r.Reason = strings.TrimSpace(r.Reason + " (built-in fallback: usage limit reached)")
		keep = append(keep, Result{Verification: r, Provider: verifiers.BuiltinID, CheckedAt: time.Now()})
	}
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
		if _, err := enqueueAt(ctx, s.Pool, ws, retry, force, time.Now().Add(RetryDelay)); err != nil {
			return keep, err
		}
		if s.Log != nil {
			s.Log.Warn("verification will retry", "workspace", ws, "count", len(retry), "err", lastErr)
		}
	}
	return keep, nil
}

// book puts the paid checks in the spend ledger, one credit an address,
// so they count toward the connection's usage limits, with what the
// provider said they cost when it did.
func (s *Service) book(ctx context.Context, ws string, o *connectors.Opened, errs []error, results []Result) error {
	n, cost := 0, 0.0
	for i, err := range errs {
		if err == nil {
			n++
			cost += results[i].CostMicros
		}
	}
	return s.bookChecks(ctx, ws, o, n, cost)
}

// bookChecks books n checks on o. micros is what the provider said they
// cost, in millionths of a dollar: 0 when it said nothing, and the credits
// are then priced with the workspace's spend rates.
func (s *Service) bookChecks(ctx context.Context, ws string, o *connectors.Opened, n int, micros float64) error {
	if n == 0 {
		return nil
	}
	units := float64(n)
	id := o.ID
	e := outcomes.Entry{
		WorkspaceID: ws, Category: outcomes.CategoryVerification, Provider: o.Connector.Definition().ID,
		IntegrationID: &id, Units: &units, UnitName: "credit", OccurredAt: time.Now(), Source: outcomes.SourceAuto,
		Note: fmt.Sprintf("Verified %d addresses, one credit each (estimated)", n),
	}
	if m := int64(math.Round(micros)); m > 0 {
		e.AmountMicros = &m
		e.Note = fmt.Sprintf("Verified %d addresses, at the cost the provider reported", n)
	}
	_, _, err := outcomes.Record(ctx, s.Pool, e)
	return err
}

// secondVerifier is the workspace's other connected verifier, asked when
// the chosen one's first check is unavailable
// (connectors.FallbackVerifier). It is opened on first use and counts the
// addresses it answered, which are booked to its own connection.
type secondVerifier struct {
	s      *Service
	ws, id string

	once sync.Once
	o    *connectors.Opened
	err  error

	mu       sync.Mutex
	answered int
	refused  error // a refused key or an empty balance: not asked again
}

// secondVerifier finds the workspace's oldest connected verifier other
// than the chosen one that serves the brand the work is for, or nil when
// there is none. Only the workspace's own connections.
func (s *Service) secondVerifier(ctx context.Context, ws, chosen, connector string) (*secondVerifier, error) {
	if s.Store == nil || s.Store.Registry == nil {
		return nil, nil
	}
	list, err := s.Store.ListCapable(ctx, s.Pool, ws, outcomes.BrandFrom(ctx), connectors.CapVerify)
	if err != nil {
		return nil, err
	}
	for _, i := range list {
		// Another connection to the chosen verifier's own connector is the
		// same service, asked by the verifier itself (sameConnector): it
		// is no second opinion when the first has no room.
		if i.ID == chosen || i.Connector == connector || i.Connector == verifiers.BuiltinID {
			continue
		}
		if c, ok := s.Store.Registry.Get(i.Connector); ok {
			if _, ok := c.(connectors.Verifier); ok {
				return &secondVerifier{s: s, ws: ws, id: i.ID}, nil
			}
		}
	}
	return nil, nil
}

// sameConnector is the chosen verifier's connection followed by the
// workspace's other connected connections to the same connector that serve
// the brand the work is for, oldest first. Only the workspace's own
// connections; one that cannot be opened or is at its usage limit is left
// out.
func (s *Service) sameConnector(ctx context.Context, ws string, chosen *connectors.Opened) ([]*connectors.Opened, error) {
	out := []*connectors.Opened{chosen}
	connector := chosen.Connector.Definition().ID
	list, err := s.Store.ListCapable(ctx, s.Pool, ws, outcomes.BrandFrom(ctx), connectors.CapVerify)
	if err != nil {
		return nil, err
	}
	for _, i := range list {
		if i.ID == chosen.ID || i.Connector != connector {
			continue
		}
		if err := usagelimits.Check(ctx, s.Pool, ws, i.ID, "credit", 1); err != nil {
			var le *usagelimits.Error
			if errors.As(err, &le) {
				continue
			}
			return nil, err
		}
		o, err := s.Store.Open(ctx, s.Pool, ws, i.ID)
		if err != nil {
			continue
		}
		out = append(out, o)
	}
	return out, nil
}

func (f *secondVerifier) event(name string, props map[string]any) track.Event {
	p := map[string]any{"integration_id": f.id}
	if f.o != nil {
		p["connector"] = f.o.Connector.Definition().ID
	}
	for k, v := range props {
		p[k] = v
	}
	return track.Event{Name: name, WorkspaceID: f.ws, ActorType: "system", Surface: "job", Props: p}
}

// verify checks one address with the second verifier. An error means it
// gave no answer, and the chosen verifier goes on without it.
func (f *secondVerifier) verify(ctx context.Context, email string) (connectors.Verification, error) {
	fail := func(code string, err error, outcome string) (connectors.Verification, error) {
		ev := f.event("email.fallback_verifier_failed", map[string]any{"kind": strings.TrimPrefix(code, "connector_")})
		ev.Outcome = outcome
		track.Fail(ctx, f.s.Log, ev, code, err)
		return connectors.Verification{}, err
	}
	f.mu.Lock()
	refused := f.refused
	f.mu.Unlock()
	if refused != nil {
		return connectors.Verification{}, refused
	}
	f.once.Do(func() { f.o, f.err = f.s.Store.Open(ctx, f.s.Pool, f.ws, f.id) })
	if f.err != nil {
		if errors.Is(f.err, connectors.ErrNotFound) {
			return connectors.Verification{}, f.err // disconnected meanwhile
		}
		return fail("connector_"+connectors.KindOf(f.err), f.err, track.Failure)
	}
	v, ok := f.o.Connector.(connectors.Verifier)
	if !ok {
		return connectors.Verification{}, connectors.Errf(connectors.ErrInvalid, "this connection cannot verify emails")
	}
	if err := usagelimits.Check(ctx, f.s.Pool, f.ws, f.id, "credit", 1); err != nil {
		var le *usagelimits.Error
		if errors.As(err, &le) {
			return fail(le.Code(), err, track.Failure)
		}
		return fail("internal", err, track.Error)
	}
	started := time.Now()
	r, err := v.Verify(ctx, f.o.Client, f.o.Creds, email)
	if err != nil {
		kind := connectors.KindOf(err)
		outcome := track.Error
		if kind == connectors.ErrAuth || kind == connectors.ErrQuota {
			outcome = track.Failure
			f.mu.Lock()
			f.refused = err
			f.mu.Unlock()
		}
		return fail("connector_"+kind, err, outcome)
	}
	f.mu.Lock()
	f.answered++
	f.mu.Unlock()
	track.Emit(ctx, f.event("email.fallback_verifier_asked", map[string]any{
		"status": r.Status, "catch_all": r.CatchAll, "duration_ms": time.Since(started).Milliseconds()}))
	return r, nil
}

// settle records what the second verifier's calls came to: the error on
// its connection when it refused the key or ran out, and one credit an
// address it answered in its own spend ledger.
func (f *secondVerifier) settle(ctx context.Context) error {
	if f.o == nil {
		return nil
	}
	f.mu.Lock()
	n, refused := f.answered, f.refused
	f.mu.Unlock()
	if n > 0 || refused != nil {
		_ = f.s.Store.Checked(ctx, f.s.Pool, f.ws, f.id, &f.o.Account, refused)
	}
	return f.s.bookChecks(ctx, f.ws, f.o, n, 0)
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
