// Package guardsync keeps a copy of each contact guard's whole list (a
// product's account feed, a shared blocklist's exports) in the workspace's
// opt-out list, so a company is checked against the copy without a call,
// and whoever joins the list is stopped like anyone else on it: a sequence
// Outflare sends checks the opt-out list before every email, and the
// suppression syncs write it to connected sequencers' blocklists.
//
// Each guard connection whose connector can read its list
// (connectors.GuardLister) is synced by a job on its own schedule. A feed
// is read from where the last sync stopped, so only what is new is read;
// its first sync reads it all, in bounded runs that pick up from their
// cursor. A snapshot is read whole each time and compared with the copy:
// what left it is removed, and only entries this sync added are ever
// removed. Entries carry the source <connector>_sync, the reason the
// guard's list means (customer, or blocklist) and the connection's id.
//
// The live checks are unchanged: before anyone is added to a campaign,
// the guard itself is asked, since the copy is only as new as its last
// sync.
package guardsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/classify"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/leads"
	"github.com/outflareai/outflare/engine/internal/track"
)

// JobSync syncs one guard connection's list.
const JobSync = "guard_lists.sync"

// Limits of one sync, as variables so tests can change them.
var (
	// RunBudget is how long one sync of a feed reads before it stops and
	// queues the next, and FeedPages the most pages it reads.
	RunBudget = 4 * time.Minute
	FeedPages = 400
	// Lease is how long a sync holds its connection's row: a second sync
	// waits for it, or for it to run out if the process died.
	Lease = 15 * time.Minute
	// RetryMin is the least a failed sync waits before the next try.
	RetryMin = time.Minute
	// FreshFactor: a copy is used in place of the guard while its last
	// success is within this many intervals.
	FreshFactor = 2
)

// Service syncs guard lists.
type Service struct {
	Pool  *pgxpool.Pool
	Store *connectors.Store
	Log   *slog.Logger
	// Now is overridable in tests.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Register handles the sync job, and syncs a guard straight away when it
// is connected.
func (s *Service) Register(q *jobs.Queue) {
	q.Handle(JobSync, s.job)
	s.Store.OnConnect = append(s.Store.OnConnect, func(ctx context.Context, tx pgx.Tx, i connectors.Integration) error {
		if _, ok := lister(s.Store.Registry, i.Connector); !ok {
			return nil
		}
		return Request(ctx, tx, i.WorkspaceID, i.ID, 0)
	})
}

// lister is a connector that can read its guard list.
func lister(reg *connectors.Registry, connector string) (connectors.GuardLister, bool) {
	c, ok := reg.Get(connector)
	if !ok {
		return nil, false
	}
	l, ok := c.(connectors.GuardLister)
	return l, ok
}

// Listers are the ids of the connectors that can read their guard list.
func Listers(reg *connectors.Registry) []string {
	var ids []string
	for _, d := range reg.All() {
		if _, ok := lister(reg, d.ID); ok {
			ids = append(ids, d.ID)
		}
	}
	return ids
}

// Info is how a connector's list is read, and ok whether it can be.
func Info(reg *connectors.Registry, connector string) (connectors.GuardListInfo, bool) {
	l, ok := lister(reg, connector)
	if !ok {
		return connectors.GuardListInfo{}, false
	}
	return l.GuardList(), true
}

// Source is the opt-out list's source for a connector's list entries.
func Source(connector string) string { return connector + "_sync" }

// SuppressionReason is the opt-out list reason for what a guard's list
// means: customer, or blocklist.
func SuppressionReason(guardReason string) string {
	if guardReason == connectors.GuardCustomer {
		return "customer"
	}
	return "blocklist"
}

type payload struct {
	WorkspaceID   string `json:"workspace_id"`
	IntegrationID string `json:"integration_id"`
	// Continue is a feed's next run, straight after one that stopped
	// before its end.
	Continue bool `json:"continue,omitempty"`
}

// Request queues a sync of one guard connection after delay. Requests in
// the same minute share one run.
func Request(ctx context.Context, q jobs.Querier, ws, integrationID string, delay time.Duration) error {
	at := time.Now().Add(delay)
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{
		Kind: JobSync, WorkspaceID: &ws, Payload: payload{WorkspaceID: ws, IntegrationID: integrationID}, RunAt: at, MaxAttempts: 1,
		IdempotencyKey: integrationID + ":" + strconv.FormatInt(at.Truncate(time.Minute).Unix(), 10),
	})
	return err
}

// carryOn queues a feed's next run now.
func carryOn(ctx context.Context, q jobs.Querier, ws, integrationID string) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{
		Kind: JobSync, WorkspaceID: &ws, Payload: payload{WorkspaceID: ws, IntegrationID: integrationID, Continue: true}, MaxAttempts: 1,
		IdempotencyKey: integrationID + ":more:" + strconv.FormatInt(time.Now().UnixNano(), 10),
	})
	return err
}

// Loop queues a sync for every guard connection that is due.
func (s *Service) Loop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := s.QueueDue(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.Log, track.Event{Name: "guard_list.sync_failed", ActorType: "system", Surface: "system",
				Props: map[string]any{"stage": "queue"}}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// QueueDue queues a sync for every enabled guard connection whose last
// try is older than its interval and that is not waiting after a failure.
func (s *Service) QueueDue(ctx context.Context) error {
	ids := Listers(s.Store.Registry)
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT i.id::text, i.workspace_id::text, i.connector, g.interval_seconds, g.attempted_at
		FROM integrations i LEFT JOIN guard_list_syncs g ON g.integration_id = i.id
		WHERE i.connector = ANY($1) AND COALESCE(g.enabled, true)
		  AND (g.retry_at IS NULL OR g.retry_at <= $2)
		  AND (g.running_until IS NULL OR g.running_until < $2)`, ids, s.now())
	if err != nil {
		return err
	}
	type due struct {
		id, ws, connector string
		every             *int
		attempted         *time.Time
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (due, error) {
		var d due
		return d, r.Scan(&d.id, &d.ws, &d.connector, &d.every, &d.attempted)
	})
	if err != nil {
		return err
	}
	now := s.now()
	for _, d := range list {
		info, _ := Info(s.Store.Registry, d.connector)
		every := Interval(info, d.every)
		if d.attempted != nil && now.Sub(*d.attempted) < every {
			continue
		}
		if err := Request(ctx, s.Pool, d.ws, d.id, 0); err != nil {
			return err
		}
	}
	return nil
}

// Interval is how often a list is read: the connection's own setting, or
// the connector's default.
func Interval(info connectors.GuardListInfo, seconds *int) time.Duration {
	if seconds != nil && *seconds > 0 {
		return time.Duration(*seconds) * time.Second
	}
	if info.Every > 0 {
		return info.Every
	}
	return 30 * time.Minute
}

func (s *Service) job(ctx context.Context, job jobs.Job) error {
	var p payload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	if !p.Continue {
		// A sync queued twice runs once: one that ran within half its
		// interval is enough.
		var every *int
		var attempted *time.Time
		var connector string
		err := s.Pool.QueryRow(ctx, `SELECT g.interval_seconds, g.attempted_at, i.connector FROM integrations i
			LEFT JOIN guard_list_syncs g ON g.integration_id = i.id WHERE i.id = $1 AND i.workspace_id = $2`, p.IntegrationID, p.WorkspaceID).
			Scan(&every, &attempted, &connector)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		info, _ := Info(s.Store.Registry, connector)
		if attempted != nil && s.now().Sub(*attempted) < Interval(info, every)/2 {
			return nil
		}
	}
	res, err := s.Sync(ctx, p.WorkspaceID, p.IntegrationID)
	if errors.Is(err, connectors.ErrNotFound) || errors.Is(err, ErrNotLister) || errors.Is(err, ErrBusy) || errors.Is(err, ErrOff) {
		return nil
	}
	if err == nil && res.More {
		// A feed with more to read carries on straight away.
		return carryOn(ctx, s.Pool, p.WorkspaceID, p.IntegrationID)
	}
	// A failure is on the sync's row, an event and a log line; the next
	// try waits for its time there rather than for the job's retries.
	return nil
}

var (
	// ErrNotLister is a connection whose connector can't read its list.
	ErrNotLister = errors.New("this connection's list cannot be read")
	// ErrBusy is a sync already running for the connection.
	ErrBusy = errors.New("a sync of this list is already running")
	// ErrOff is a list sync turned off.
	ErrOff = errors.New("this list sync is turned off")
)

// Result is what one sync did.
type Result struct {
	Added, Removed int
	// More is a feed that stopped before its end.
	More bool
}

// Sync reads one guard connection's list into the opt-out list.
func (s *Service) Sync(ctx context.Context, ws, integrationID string) (Result, error) {
	var res Result
	op, err := s.Store.Open(ctx, s.Pool, ws, integrationID)
	if err != nil {
		if !errors.Is(err, connectors.ErrNotFound) {
			// A connection that can't be opened (its sign-in or key can't
			// be read): nothing is read, and it says so.
			track.Fail(ctx, s.Log, track.Event{Name: "guard_list.sync_failed", WorkspaceID: ws, ActorType: "system", Surface: "job",
				Props: map[string]any{"integration_id": integrationID, "stage": "open"}}, "connector_"+connectors.KindOf(err), err)
		}
		return res, err
	}
	l, ok := op.Connector.(connectors.GuardLister)
	if !ok {
		return res, ErrNotLister
	}
	info := l.GuardList()
	started := s.now()
	var cursor *string
	var enabled bool
	err = s.Pool.QueryRow(ctx, `
		INSERT INTO guard_list_syncs (integration_id, workspace_id, running_until, attempted_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (integration_id) DO UPDATE SET running_until = $3, attempted_at = $4
			WHERE guard_list_syncs.enabled AND (guard_list_syncs.running_until IS NULL OR guard_list_syncs.running_until < $4)
		RETURNING cursor, enabled`, integrationID, ws, started.Add(Lease), started).Scan(&cursor, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		var on bool
		if s.Pool.QueryRow(ctx, `SELECT enabled FROM guard_list_syncs WHERE integration_id = $1`, integrationID).Scan(&on) == nil && !on {
			return res, ErrOff
		}
		return res, ErrBusy
	}
	if err != nil {
		return res, err
	}
	w := &writer{s: s, ws: ws, op: op, info: info, source: Source(op.Integration.Connector)}
	var stage string
	switch info.Mode {
	case connectors.GuardListSnapshot:
		stage = "snapshot"
		res, err = w.snapshot(ctx, l)
	default:
		stage = "feed"
		from := ""
		if cursor != nil {
			from = *cursor
		}
		res, err = w.feed(ctx, l, from, started)
	}
	s.finish(ctx, ws, op, info, started, res, stage, err)
	return res, err
}

// finish records how a sync went on its row, as an event and, for a
// failure, a log line, and lets go of the row.
func (s *Service) finish(ctx context.Context, ws string, op *connectors.Opened, info connectors.GuardListInfo, started time.Time, res Result, stage string, runErr error) {
	now := s.now()
	took := now.Sub(started)
	props := map[string]any{"connector": op.Integration.Connector, "integration_id": op.Integration.ID, "mode": info.Mode,
		"added": res.Added, "removed": res.Removed, "more": res.More, "duration_ms": took.Milliseconds()}
	var msg, code *string
	var retry *time.Time
	if runErr != nil {
		m := runErr.Error()
		c := "internal"
		var ge *connectors.GuardError
		if errors.As(runErr, &ge) {
			c = "connector_" + connectors.KindOf(runErr)
			props["detail"] = ge.Code
		}
		msg, code = &m, &c
		wait := RetryMin
		if g, ok := runErr.(*connectors.GuardError); ok && g.RetryAfter > wait {
			wait = g.RetryAfter
		}
		at := now.Add(wait)
		retry = &at
		props["stage"] = stage
		track.Fail(ctx, s.Log, track.Event{Name: "guard_list.sync_failed", WorkspaceID: ws, ActorType: "system", Surface: "job", Props: props}, c, runErr)
	}
	// The count of entries is read again only when something changed.
	_, err := s.Pool.Exec(ctx, `UPDATE guard_list_syncs SET running_until = NULL, last_error = $2, error_code = $3, retry_at = $4,
		succeeded_at = CASE WHEN $2::text IS NULL THEN $5::timestamptz ELSE succeeded_at END,
		backfilled_at = CASE WHEN $2::text IS NULL AND NOT $6::bool THEN COALESCE(backfilled_at, $5::timestamptz) ELSE backfilled_at END,
		last_added = $7::int, last_removed = $8::int, last_duration_ms = $9::int, added_count = added_count + $7::int, removed_count = removed_count + $8::int,
		entries = CASE WHEN $7::int + $8::int > 0 OR entries = 0 THEN (SELECT count(*) FROM suppressions x WHERE x.integration_id = $1 AND x.source = $10) ELSE entries END,
		updated_at = $5::timestamptz
		WHERE integration_id = $1`, op.Integration.ID, msg, code, retry, now, res.More, res.Added, res.Removed, int(min(took.Milliseconds(), 1<<31-1)),
		Source(op.Integration.Connector))
	if err != nil {
		track.Fail(ctx, s.Log, track.Event{Name: "guard_list.sync_failed", WorkspaceID: ws, ActorType: "system", Surface: "job",
			Props: map[string]any{"connector": op.Integration.Connector, "integration_id": op.Integration.ID, "stage": "record"}}, "internal", err)
	}
	if runErr == nil && (res.Added > 0 || res.Removed > 0 || res.More) {
		// A sync that found nothing new is on its row, not an event.
		track.Emit(ctx, track.Event{Name: "guard_list.synced", WorkspaceID: ws, ActorType: "system", Surface: "job", Outcome: track.Success, Props: props})
	}
}

// writer writes one connection's list entries to the opt-out list.
type writer struct {
	s      *Service
	ws     string
	op     *connectors.Opened
	info   connectors.GuardListInfo
	source string
}

// entries are a batch from the guard as opt-out list entries: valid
// addresses and domains only, and never a mailbox provider's domain,
// which would stop everyone with an address there.
func entries(batch []connectors.GuardListEntry) []leads.Suppression {
	intel := classify.Builtin()
	out := make([]leads.Suppression, 0, len(batch))
	seen := map[leads.Suppression]bool{}
	for _, e := range batch {
		kind, value, err := leads.ParseSuppression(e.Value)
		if err != nil || (kind == "domain") != e.Domain {
			continue
		}
		if kind == "domain" && intel.MailboxProvider(value) {
			continue
		}
		x := leads.Suppression{Kind: kind, Value: value}
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func (w *writer) added() leads.Added {
	brand := ""
	if w.op.Integration.BrandID != nil {
		brand = *w.op.Integration.BrandID
	}
	what := "customers"
	if w.info.Reason != connectors.GuardCustomer {
		what = "blocklist"
	}
	return leads.Added{Reason: SuppressionReason(w.info.Reason), Source: w.source, IntegrationID: w.op.Integration.ID, BrandID: brand,
		Note: "On the " + w.op.Integration.Name + " " + what + " list"}
}

// auditAdded records entries the sync added, in their transaction.
func (w *writer) auditAdded(ctx context.Context, tx pgx.Tx, n int) error {
	if n == 0 {
		return nil
	}
	return audit.Record(ctx, tx, audit.Event{WorkspaceID: w.ws, ActorType: "system", Action: "suppressions.added", TargetType: "suppression",
		Data: map[string]any{"added": n, "reason": SuppressionReason(w.info.Reason), "method": leads.MethodAutomated, "source": w.source,
			"integration_id": w.op.Integration.ID}})
}

// feed reads a feed from cursor: each page's entries and the cursor after
// it are kept in one transaction, so a sync that stops anywhere carries on
// from the last page kept.
func (w *writer) feed(ctx context.Context, l connectors.GuardLister, cursor string, started time.Time) (Result, error) {
	var res Result
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errBudget := errors.New("budget")
	more, err := l.ReadGuardList(ctx, w.op.Client, w.op.Creds, cursor, FeedPages, func(batch []connectors.GuardListEntry, next string) error {
		err := pgx.BeginFunc(ctx, w.s.Pool, func(tx pgx.Tx) error {
			n, err := leads.Suppress(ctx, tx, w.ws, w.added(), entries(batch))
			if err != nil {
				return err
			}
			if err := w.auditAdded(ctx, tx, n); err != nil {
				return err
			}
			res.Added += n
			_, err = tx.Exec(ctx, `UPDATE guard_list_syncs SET cursor = NULLIF($2, ''), running_until = $3 WHERE integration_id = $1`,
				w.op.Integration.ID, next, w.s.now().Add(Lease))
			return err
		})
		if err != nil {
			return err
		}
		if w.s.now().Sub(started) >= RunBudget {
			return errBudget
		}
		return nil
	})
	switch {
	case errors.Is(err, errBudget):
		res.More = true
		return res, nil
	case err != nil:
		res.More = more
		return res, err
	}
	res.More = more
	return res, nil
}

// snapshot reads the whole list into a table of this transaction, then
// adds what is new and removes this connection's entries that are no
// longer on it, all at once: a read that fails half way changes nothing.
func (w *writer) snapshot(ctx context.Context, l connectors.GuardLister) (Result, error) {
	var res Result
	err := pgx.BeginFunc(ctx, w.s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `CREATE TEMP TABLE guard_list_stage (kind text NOT NULL, value text NOT NULL) ON COMMIT DROP`); err != nil {
			return err
		}
		_, err := l.ReadGuardList(ctx, w.op.Client, w.op.Creds, "", 0, func(batch []connectors.GuardListEntry, _ string) error {
			list := entries(batch)
			rows := make([][]any, len(list))
			for i, e := range list {
				rows[i] = []any{e.Kind, e.Value}
			}
			_, err := tx.CopyFrom(ctx, pgx.Identifier{"guard_list_stage"}, []string{"kind", "value"}, pgx.CopyFromRows(rows))
			return err
		})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `CREATE INDEX ON guard_list_stage (kind, value)`); err != nil {
			return err
		}
		by := w.added()
		brand := by.BrandID
		if leads.EveryBrand(by.Reason) {
			brand = ""
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO suppressions (workspace_id, kind, value, reason, source, note, method, added_by_type, integration_id, brand_id)
			SELECT DISTINCT $1::uuid, t.kind, t.value, $2, $3, $4, $5, 'system', $6::uuid, NULLIF($7, '')::uuid FROM guard_list_stage t
			ON CONFLICT DO NOTHING`, w.ws, by.Reason, by.Source, by.Note, leads.MethodAutomated, by.IntegrationID, brand)
		if err != nil {
			return err
		}
		res.Added = int(tag.RowsAffected())
		if err := w.auditAdded(ctx, tx, res.Added); err != nil {
			return err
		}
		// Only this connection's own entries leave with the snapshot: one a
		// person or another source added stays whatever the list says.
		tag, err = tx.Exec(ctx, `
			DELETE FROM suppressions s WHERE s.integration_id = $2 AND s.workspace_id = $1 AND s.source = $3
			  AND NOT EXISTS (SELECT 1 FROM guard_list_stage t WHERE t.kind = s.kind AND t.value = s.value)`, w.ws, w.op.Integration.ID, w.source)
		if err != nil {
			return err
		}
		res.Removed = int(tag.RowsAffected())
		if res.Removed > 0 {
			if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: w.ws, ActorType: "system", Action: "suppressions.removed", TargetType: "suppression",
				Data: map[string]any{"removed": res.Removed, "source": w.source, "integration_id": w.op.Integration.ID}}); err != nil {
				return err
			}
		}
		return nil
	})
	return res, err
}

// Fresh says whether the copy of a guard connection's list can be used in
// place of asking the guard: its sync is on, has read the whole list once,
// and last succeeded within FreshFactor of its intervals.
func Fresh(ctx context.Context, q jobs.Querier, reg *connectors.Registry, ws, integrationID, connector string, now time.Time) (bool, error) {
	info, ok := Info(reg, connector)
	if !ok {
		return false, nil
	}
	var enabled bool
	var every *int
	var backfilled, succeeded *time.Time
	err := q.QueryRow(ctx, `SELECT enabled, interval_seconds, backfilled_at, succeeded_at FROM guard_list_syncs
		WHERE integration_id = $1 AND workspace_id = $2`, integrationID, ws).Scan(&enabled, &every, &backfilled, &succeeded)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !enabled || backfilled == nil || succeeded == nil {
		return false, nil
	}
	return now.Sub(*succeeded) <= time.Duration(FreshFactor)*Interval(info, every), nil
}

// CompanyListed says whether a company's domain, or a domain it is under,
// is on a guard connection's copy. ok is false for a domain that can't be
// read or is a mailbox provider's: the copy says nothing about those.
func CompanyListed(ctx context.Context, q jobs.Querier, ws, integrationID, connector, domain string) (listed, ok bool, err error) {
	d, perr := classify.ParseDomain(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(domain)), "www."))
	if perr != nil || d.Registrable == "" || classify.Builtin().MailboxProvider(d.Name) || classify.Builtin().MailboxProvider(d.Registrable) {
		return false, false, nil
	}
	var chain []string
	for h := d.Name; ; {
		chain = append(chain, h)
		if h == d.Registrable {
			break
		}
		i := strings.IndexByte(h, '.')
		if i < 0 {
			break
		}
		h = h[i+1:]
	}
	err = q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM suppressions WHERE integration_id = $2 AND kind = 'domain' AND value = ANY($3)
		AND workspace_id = $1 AND source = $4)`, ws, integrationID, chain, Source(connector)).Scan(&listed)
	return listed, err == nil, err
}
