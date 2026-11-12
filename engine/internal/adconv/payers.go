package adconv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The payers list: a Customer Match list of everyone who paid, which
// Google's new customer acquisition goal reads as the existing customers
// and campaigns can exclude. A job refreshes it once a day from the
// brand's payment.succeeded events: people who may be added (consent as
// for conversions, for ad data and ad personalisation) go in with their
// hashed emails and phones; people who withdrew consent, or whose record
// was merged away or archived, are taken out. An erased person's
// identifiers are gone with them, so they leave the list when their
// membership expires (Google keeps a member at most 540 days, and the
// list asks for that).

const (
	// JobPayers refreshes one account's payers list.
	JobPayers = "ads.payers_refresh"
	// payersEvery is how often a list is refreshed.
	payersEvery = 23 * time.Hour
	// payersBatch is how many people are read from the database at once.
	payersBatch = 5000
)

type payersPayload struct {
	Account string `json:"ad_account_id"`
}

// EnqueuePayers asks for a refresh of one account's payers list; key makes
// the request idempotent (the day, or a reason).
func EnqueuePayers(ctx context.Context, q jobs.Querier, ws, accountID, key string) (int64, error) {
	return jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobPayers, WorkspaceID: &ws, Payload: payersPayload{Account: accountID},
		IdempotencyKey: "payers:" + accountID + ":" + key, MaxAttempts: 3})
}

// queuePayers asks for a refresh of every payers list that is due, and
// records how many people the lists hold.
func (s *Sender) queuePayers(ctx context.Context) error {
	rows, err := s.Pool.Query(ctx, `SELECT a.id, a.workspace_id FROM ad_accounts a
		WHERE a.status = 'active' AND a.feed->'payers_list'->>'id' <> ''
		  AND (NULLIF(a.feed->'payers_list'->>'refreshed_at', '') IS NULL
		       OR (a.feed->'payers_list'->>'refreshed_at')::timestamptz < now() - $1::interval)`, fmt.Sprintf("%d seconds", int(payersEvery.Seconds())))
	if err != nil {
		return err
	}
	type due struct{ acct, ws string }
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.acct, &d.ws); err != nil {
			rows.Close()
			return err
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	day := s.now().UTC().Format("2006-01-02")
	for _, d := range list {
		if _, err := EnqueuePayers(ctx, s.Pool, d.ws, d.acct, day); err != nil {
			return err
		}
	}
	var members int64
	if err := s.Pool.QueryRow(ctx, `SELECT COALESCE(sum((a.feed->'payers_list'->>'members')::bigint), 0) FROM ad_accounts a
		WHERE a.status = 'active' AND (a.feed->'payers_list'->>'members') ~ '^[0-9]+$'`).Scan(&members); err != nil {
		return err
	}
	metrics.AdsPayersListMembers(members)
	return nil
}

func (s *Sender) payersJob(ctx context.Context, job jobs.Job) error {
	var p payersPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return nil
	}
	ws := *job.WorkspaceID
	res, err := s.RefreshPayers(ctx, ws, p.Account)
	ev := track.Event{Name: "ad_payers.refreshed", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"ad_account_id": p.Account, "connector": res.Connector, "members": res.Members, "removed": res.Removed, "skipped": res.Skipped}}
	var db dbError
	switch {
	case err == nil:
		metrics.AdsFeedBatch("payers_list", "sent")
		track.Emit(ctx, ev)
		s.checkHealth(ctx, ws, p.Account)
		return nil
	case errors.Is(err, connectors.ErrNotFound):
		return nil
	case errors.As(err, &db):
		track.Fail(ctx, s.Log, ev, "internal", err)
		return err
	}
	metrics.AdsFeedBatch("payers_list", "failed")
	kind := connectors.KindOf(err)
	ev.Outcome = track.Failure
	if kind == connectors.ErrUnavailable {
		ev.Outcome = track.Error
	}
	track.Fail(ctx, s.Log, ev, "connector_"+kind, err)
	if kind == connectors.ErrUnavailable || kind == connectors.ErrQuota {
		return err
	}
	return nil
}

// PayersResult counts what one refresh did.
type PayersResult struct {
	Connector string `json:"connector"`
	// Members is how many people are on the list now; Removed how many
	// were taken out; Skipped how many payers could not go in (no
	// consent, nothing to match on).
	Members int `json:"members"`
	Removed int `json:"removed"`
	Skipped int `json:"skipped"`
}

type payer struct {
	id     string
	brand  string
	member connectors.AudienceMember
	gone   bool // merged away, archived or deleted: taken out
}

// RefreshPayers fills an account's payers list from the brand's payments
// and records the result on the account's feed.
func (s *Sender) RefreshPayers(ctx context.Context, ws, accountID string) (PayersResult, error) {
	var res PayersResult
	var integrationID, brand string
	var acct connectors.AdAccount
	var meta []byte
	err := s.Pool.QueryRow(ctx, `SELECT integration_id, connector, external_id, name, currency, time_zone, meta, coalesce(brand_id::text, '')
		FROM ad_accounts WHERE workspace_id = $1 AND id = $2 AND status = 'active'`, ws, accountID).Scan(
		&integrationID, &res.Connector, &acct.ID, &acct.Name, &acct.Currency, &acct.TimeZone, &meta, &brand)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return res, connectors.ErrNotFound
		}
		return res, dbError{err}
	}
	_ = json.Unmarshal(meta, &acct.Meta)
	feed, err := LoadFeed(ctx, s.Pool, ws, accountID)
	if err != nil {
		return res, dbError{err}
	}
	if feed.PayersList == nil || feed.PayersList.ID == "" {
		return res, connectors.ErrNotFound
	}
	// What happened is written on the feed whatever the outcome.
	finish := func(members, removed int, requestID string, callErr error) {
		now := s.now()
		f, err := LoadFeed(ctx, s.Pool, ws, accountID)
		if err != nil || f.PayersList == nil {
			return
		}
		f.PayersList.RefreshedAt = &now
		f.PayersList.Error = ""
		if callErr != nil {
			f.PayersList.Error = trim(callErr.Error(), 300)
		} else {
			f.PayersList.Members, f.PayersList.Removed, f.PayersList.RequestID = members, removed, requestID
		}
		_ = SaveFeed(ctx, s.Pool, ws, accountID, f)
	}
	o, err := s.Store.Open(ctx, s.Pool, ws, integrationID)
	if err != nil {
		finish(0, 0, "", err)
		return res, err
	}
	feeder, ok := o.Connector.(connectors.AudienceFeeder)
	if !ok {
		err := connectors.Errf(connectors.ErrInvalid, "%s cannot fill customer lists from here", o.Connector.Definition().Name)
		finish(0, 0, "", err)
		return res, err
	}
	mode, err := ConsentMode(ctx, s.Pool, ws, brand)
	if err != nil {
		return res, dbError{err}
	}
	payers, err := s.payers(ctx, ws, brand)
	if err != nil {
		return res, dbError{err}
	}
	var add, remove []connectors.AudienceMember
	for _, p := range payers {
		if len(p.member.Emails) == 0 && len(p.member.Phones) == 0 {
			res.Skipped++
			continue
		}
		if p.gone {
			remove = append(remove, p.member)
			continue
		}
		ok, granted, _, err := AudienceConsent(ctx, s.Pool, ws, p.id, mode)
		if err != nil {
			return res, dbError{err}
		}
		if !ok {
			// Withdrawn, or needed and not given: out, in case they were
			// in before.
			remove = append(remove, p.member)
			res.Skipped++
			continue
		}
		p.member.ConsentGranted = granted
		add = append(add, p.member)
	}
	requestID := ""
	if len(add) > 0 {
		if requestID, err = feeder.IngestAudience(ctx, o.Client, o.Creds, acct, feed.PayersList.ID, connectors.AudienceAdd, add); err != nil {
			_ = s.Store.Checked(ctx, s.Pool, ws, o.ID, nil, err)
			finish(0, 0, "", err)
			return res, err
		}
	}
	if len(remove) > 0 {
		if _, err := feeder.IngestAudience(ctx, o.Client, o.Creds, acct, feed.PayersList.ID, connectors.AudienceRemove, remove); err != nil {
			finish(len(add), 0, requestID, err)
			return res, err
		}
	}
	_ = s.Store.Checked(ctx, s.Pool, ws, o.ID, nil, nil)
	res.Members, res.Removed = len(add), len(remove)
	finish(res.Members, res.Removed, requestID, nil)
	return res, nil
}

// payers lists everyone with a payment.succeeded in the brand (every
// brand's when the account has none), with their hashed identifiers and
// whether their record is gone.
func (s *Sender) payers(ctx context.Context, ws, brand string) ([]payer, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT r.id, coalesce(r.brand_id::text, ''), (r.merged_into IS NOT NULL OR r.archived_at IS NOT NULL OR r.deleted_at IS NOT NULL),
			coalesce(array_agg(i.value) FILTER (WHERE i.namespace = 'email'), '{}'), coalesce(array_agg(i.value) FILTER (WHERE i.namespace = 'phone'), '{}')
		FROM records r
		LEFT JOIN record_identities i ON i.record_id = r.id AND i.workspace_id = r.workspace_id AND i.valid_to IS NULL AND i.namespace IN ('email', 'phone')
		WHERE r.workspace_id = $1 AND r.record_type = 'person'
		  AND EXISTS (SELECT 1 FROM customer_events e WHERE e.workspace_id = r.workspace_id AND e.record_id = r.id AND e.kind = 'track' AND e.name = 'payment.succeeded'
			AND ($2 = '' OR e.brand_id::text = $2))
		GROUP BY r.id
		ORDER BY r.id LIMIT $3`, ws, brand, payersBatch*20)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []payer
	for rows.Next() {
		var p payer
		var emails, phones []string
		if err := rows.Scan(&p.id, &p.brand, &p.gone, &emails, &phones); err != nil {
			return nil, err
		}
		for _, e := range emails {
			p.member.Emails = append(p.member.Emails, connectors.HashID(e))
			p.member.GoogleEmails = append(p.member.GoogleEmails, connectors.HashGoogleEmail(e))
		}
		for _, ph := range phones {
			p.member.Phones = append(p.member.Phones, connectors.HashID(ph))
			p.member.PhoneDigits = append(p.member.PhoneDigits, connectors.HashID(strings.TrimPrefix(ph, "+")))
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
