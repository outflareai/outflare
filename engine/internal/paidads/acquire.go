package paidads

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/adconv"
	"github.com/outflareai/outflare/engine/internal/adtrack"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The ledger job (plan.md "The ledger job"). Every minute, for each
// workspace with an ad account, it reads the customer events that arrived
// since its cursor (a few minutes ago, so the visit and the identify call
// that tie them to a person have landed): sign-ups, payments and refunds
// with a record. For each it finds the person's last visit within the
// brand's acquisition window that carried a paid click, resolves the
// campaign, ad group and ad, and writes the acquisition (the first time)
// and the revenue row (every payment and refund). Events are read in the
// order they arrived, not happened, so a payment a billing connector
// synced late still counts. On a workspace's first run it reads 90 days.

const (
	// BackfillDays is how far back the first run of a workspace reads.
	BackfillDays = 90
	eventBatch   = 500
	// zeroUUID is where a cursor starts within its timestamp.
	zeroUUID = "00000000-0000-0000-0000-000000000000"
)

// Ledger runs the job.
type Ledger struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	// Settle is how long after an event arrives it is read; zero is five
	// minutes.
	Settle time.Duration
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Result is what a run did.
type Result struct {
	// Events read, acquisitions made (sign-ups and first payers with a paid
	// click), revenue rows written.
	Events, Acquisitions, Revenue int
}

func (l *Ledger) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Ledger) settle() time.Duration {
	if l.Settle == 0 {
		return 5 * time.Minute
	}
	return l.Settle
}

// Run ticks every interval until ctx ends.
func (l *Ledger) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := l.Tick(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, l.Log, track.Event{Name: "paidads.run_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick runs the ledger for every workspace with an ad account and reports
// how far the furthest-behind one is.
func (l *Ledger) Tick(ctx context.Context) (Result, error) {
	rows, err := l.Pool.Query(ctx, `SELECT DISTINCT a.workspace_id::text FROM ad_accounts a ORDER BY 1`)
	if err != nil {
		return Result{}, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return Result{}, err
	}
	var total Result
	for _, ws := range list {
		r, err := l.RunWorkspace(ctx, ws)
		total.Events += r.Events
		total.Acquisitions += r.Acquisitions
		total.Revenue += r.Revenue
		if err != nil && ctx.Err() == nil {
			track.Fail(ctx, l.Log, track.Event{Name: "paidads.run_failed", WorkspaceID: ws, ActorType: "system", Surface: "system"}, "internal", err)
		}
	}
	var lag *float64
	if err := l.Pool.QueryRow(ctx, `SELECT max(extract(epoch from now() - cursor_at)) FROM ad_acquisition_runs r
		WHERE cursor_at > '-infinity' AND EXISTS (SELECT 1 FROM ad_accounts a WHERE a.workspace_id = r.workspace_id)`).Scan(&lag); err != nil {
		return total, err
	}
	if lag != nil {
		metrics.PaidAdsLedgerLag(time.Duration(*lag * float64(time.Second)))
	} else {
		metrics.PaidAdsLedgerLag(0)
	}
	return total, ctx.Err()
}

// brandState is what the ledger needs per brand for one run.
type brandState struct {
	econ    Economics
	mode    string
	signups map[string]bool
}

// run holds one workspace's run. Each brand is read on its own cursor
// (ad_acquisition_runs), since brands own their data.
type run struct {
	l      *Ledger
	ws     string
	brand  string
	brands map[string]*brandState
	names  []string
	res    Result
}

// RunWorkspace reads one workspace's new events and writes its ledger,
// brand by brand, each on its own cursor.
func (l *Ledger) RunWorkspace(ctx context.Context, ws string) (Result, error) {
	r := &run{l: l, ws: ws, brands: map[string]*brandState{}}
	if err := r.loadBrands(ctx); err != nil {
		return r.res, err
	}
	ids := make([]string, 0, len(r.brands))
	for id := range r.brands {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var first error
	for _, brand := range ids {
		r.brand = brand
		if err := r.do(ctx); err != nil {
			msg := err.Error()
			if len(msg) > 500 {
				msg = msg[:500]
			}
			_, _ = l.Pool.Exec(ctx, `INSERT INTO ad_acquisition_runs (brand_id, workspace_id, last_run_at, last_error) VALUES ($1, $2, now(), $3)
				ON CONFLICT (brand_id) DO UPDATE SET last_run_at = now(), last_error = EXCLUDED.last_error`, brand, ws, msg)
			if first == nil {
				first = err
			}
		}
	}
	return r.res, first
}

func (r *run) do(ctx context.Context) error {
	l := r.l
	var at *time.Time
	var id *string
	var backfilled *time.Time
	err := l.Pool.QueryRow(ctx, `INSERT INTO ad_acquisition_runs (brand_id, workspace_id) VALUES ($1, $2) ON CONFLICT (brand_id) DO UPDATE SET last_run_at = ad_acquisition_runs.last_run_at
		RETURNING NULLIF(cursor_at, '-infinity'), cursor_id::text, backfilled_at`, r.brand, r.ws).Scan(&at, &id, &backfilled)
	if err != nil {
		return err
	}
	cur := cursor{id: zeroUUID}
	if at != nil {
		cur.at = *at
	}
	if id != nil {
		cur.id = *id
	}
	if backfilled == nil || at == nil {
		cur = cursor{at: l.now().AddDate(0, 0, -BackfillDays), id: zeroUUID}
	}
	for {
		next, fetched, err := r.batch(ctx, cur)
		if err != nil {
			return err
		}
		if fetched < eventBatch {
			break
		}
		cur = next
	}
	if backfilled == nil {
		if _, err := l.Pool.Exec(ctx, `UPDATE ad_acquisition_runs SET backfilled_at = now() WHERE brand_id = $1`, r.brand); err != nil {
			return err
		}
	}
	return nil
}

// loadBrands reads each brand's economics, consent mode and the event
// names that count as a sign-up there: signed_up, and the brand's own
// conversion events from its attribution settings (payments and orders
// aside, which are payments).
func (r *run) loadBrands(ctx context.Context) error {
	rows, err := r.l.Pool.Query(ctx, `SELECT b.id::text, COALESCE(s.conversion_events, '{}') FROM brands b
		LEFT JOIN attribution_settings s ON s.brand_id = b.id WHERE b.workspace_id = $1`, r.ws)
	if err != nil {
		return err
	}
	type brandRow struct {
		id     string
		events []string
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (brandRow, error) {
		var x brandRow
		return x, row.Scan(&x.id, &x.events)
	})
	if err != nil {
		return err
	}
	all := map[string]bool{"signed_up": true, "payment.succeeded": true, "payment.refunded": true}
	for _, b := range list {
		econ, err := Load(ctx, r.l.Pool, r.ws, b.id)
		if err != nil {
			return err
		}
		mode, err := adconv.ConsentMode(ctx, r.l.Pool, r.ws, b.id)
		if err != nil {
			return err
		}
		st := &brandState{econ: econ, mode: mode, signups: map[string]bool{"signed_up": true}}
		for _, e := range b.events {
			if e == "" || strings.HasPrefix(e, "payment.") || e == "order_completed" {
				continue
			}
			st.signups[e] = true
			all[e] = true
		}
		r.brands[b.id] = st
	}
	for n := range all {
		r.names = append(r.names, n)
	}
	return nil
}

type cursor struct {
	at time.Time
	id string
}

// event is a customer event the ledger looks at.
type event struct {
	id, record string
	anon       *string
	brand      *string
	name       string
	at         time.Time
	received   time.Time
	props      []byte
}

// batch reads and handles up to eventBatch events after cur in one
// transaction, moving the cursor past them.
func (r *run) batch(ctx context.Context, cur cursor) (next cursor, fetched int, err error) {
	next = cur
	err = pgx.BeginFunc(ctx, r.l.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT e.id::text, e.record_id::text, e.anonymous_id, COALESCE(e.brand_id, outflare_first_brand(e.workspace_id))::text, e.name, e.occurred_at, e.received_at, e.properties
			FROM customer_events e
			WHERE e.workspace_id = $1 AND e.kind = 'track' AND e.record_id IS NOT NULL AND e.name = ANY($2)
			  AND (e.received_at, e.id) > ($3, $4::uuid) AND e.received_at <= $5
			  AND COALESCE(e.brand_id, outflare_first_brand(e.workspace_id)) = $7::uuid
			ORDER BY e.received_at, e.id LIMIT $6`, r.ws, r.names, cur.at, cur.id, r.l.now().Add(-r.l.settle()), eventBatch, r.brand)
		if err != nil {
			return err
		}
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (event, error) {
			var e event
			return e, row.Scan(&e.id, &e.record, &e.anon, &e.brand, &e.name, &e.at, &e.received, &e.props)
		})
		if err != nil {
			return err
		}
		fetched = len(list)
		for _, e := range list {
			if err := r.handle(ctx, tx, e); err != nil {
				return err
			}
			next = cursor{at: e.received, id: e.id}
		}
		r.res.Events += fetched
		if fetched > 0 {
			_, err = tx.Exec(ctx, `UPDATE ad_acquisition_runs SET cursor_at = $2, cursor_id = $3, last_run_at = now(), last_error = NULL WHERE brand_id = $1`, r.brand, next.at, next.id)
		} else {
			_, err = tx.Exec(ctx, `UPDATE ad_acquisition_runs SET last_run_at = now(), last_error = NULL WHERE brand_id = $1`, r.brand)
		}
		return err
	})
	return next, fetched, err
}

// handle writes what one event means for the ledger.
func (r *run) handle(ctx context.Context, tx pgx.Tx, e event) error {
	if e.brand == nil {
		return nil // a workspace with no brand has no ads to credit
	}
	st := r.brands[*e.brand]
	if st == nil {
		return nil
	}
	var kind string
	var err error
	switch {
	case e.name == "payment.refunded":
		kind, err = "refund", r.refund(ctx, tx, st, e)
	case e.name == "payment.succeeded":
		kind, err = "payment", r.payment(ctx, tx, st, e)
	case st.signups[e.name]:
		kind, err = "signup", r.signup(ctx, tx, st, e)
	default:
		return nil
	}
	if err != nil {
		metrics.PaidAdsLedgerEvent(kind, "error")
	}
	return err
}

// click is a paid visit: the last within the window before an event.
type click struct {
	sessionID   string
	at          time.Time
	kind, id    string // the click id parameter and its value
	connector   string
	accountID   *string
	campaignID  *string // ad_entities ids
	adGroupID   *string
	adID        *string
	campaignExt string
	adGroupExt  string
	adExt       string
	keyword     string
	matchedBy   string
}

// paidParams are the click id parameters that prove a paid click.
var paidParams = func() []string {
	var out []string
	for _, c := range adtrack.ClickIDs {
		if c.Paid && c.Channel == "paid_ads" {
			out = append(out, c.Param)
		}
	}
	return out
}()

// locate finds the person's last visit within windowDays before at that
// carried a paid click: a network click id, the template's ad ids, or a
// visit classified as paid ads. The person is their record, the event's
// anonymous id (before identify) and the browsers linked to their record.
func locate(ctx context.Context, q Querier, ws, brand, record string, anon *string, at time.Time, windowDays int) (*click, error) {
	var c click
	var clickIDs, ad []byte
	var utmSource, utmCampaign, utmTerm string
	err := q.QueryRow(ctx, `
		SELECT s.id::text, s.started_at, s.click_ids, s.ad, s.utm_source, s.utm_campaign, s.utm_term
		FROM site_sessions s
		WHERE s.workspace_id = $1
		  AND (s.record_id = $2 OR ($3::text IS NOT NULL AND s.anonymous_id = $3)
		       OR s.anonymous_id IN (SELECT v.anonymous_id FROM site_visitors v WHERE v.workspace_id = $1 AND v.record_id = $2))
		  AND s.started_at <= $4 AND s.started_at > $4 - make_interval(days => $5)
		  AND (s.brand_id IS NULL OR s.brand_id = $6::uuid)
		  AND (s.channel = 'paid_ads' OR s.ad <> '{}'::jsonb OR EXISTS (SELECT 1 FROM jsonb_object_keys(s.click_ids) k WHERE k = ANY($7::text[])))
		ORDER BY s.started_at DESC LIMIT 1`, ws, record, anon, at, windowDays, brand, paidParams).
		Scan(&c.sessionID, &c.at, &clickIDs, &ad, &utmSource, &utmCampaign, &utmTerm)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids map[string]any
	_ = json.Unmarshal(clickIDs, &ids)
	var adIn map[string]any
	_ = json.Unmarshal(ad, &adIn)
	adIDs, _ := adtrack.CleanAd(adIn)
	network := ""
	for _, k := range adtrack.ClickIDs {
		if !k.Paid || k.Channel != "paid_ads" {
			continue
		}
		if v, _ := ids[k.Param].(string); v != "" {
			c.kind, c.id, network = k.Param, v, k.Network
			break
		}
	}
	if network == "" {
		network, _ = adtrack.Arrival(ids, len(adIDs), strings.ToLower(utmSource))
	}
	if n, ok := adtrack.NetworkOf(network); ok {
		c.connector = n.Connector
	}
	c.campaignExt, c.adGroupExt, c.adExt, c.keyword = adIDs["campaign"], adIDs["adset"], adIDs["ad"], utmTerm
	switch {
	case c.adGroupExt != "" || c.adExt != "":
		c.matchedBy = "template"
	case c.campaignExt != "" && network == "google" && c.id != "":
		// Google adds gad_campaignid itself with auto-tagging; the script
		// keeps it as the visit's campaign.
		c.matchedBy = "campaign_param"
	case c.campaignExt != "":
		c.matchedBy = "template"
	default:
		c.matchedBy = "utm"
	}
	// A click harvested from the network (ad_clicks, another workstream's
	// table) fills the ids a landing page with only a click id lacks.
	if c.id != "" && c.campaignExt == "" {
		if err := harvested(ctx, q, ws, brand, &c); err != nil {
			return nil, err
		}
	}
	if err := resolve(ctx, q, ws, brand, &c, utmCampaign); err != nil {
		return nil, err
	}
	return &c, nil
}

// harvested fills a click's ids from ad_clicks when that table exists
// (migration 0500, the sync workstream) and knows the click.
func harvested(ctx context.Context, q Querier, ws, brand string, c *click) error {
	var ok bool
	if err := q.QueryRow(ctx, `SELECT to_regclass('ad_clicks') IS NOT NULL`).Scan(&ok); err != nil || !ok {
		return err
	}
	err := q.QueryRow(ctx, `SELECT k.campaign_external_id, k.ad_group_external_id, k.ad_external_id, k.keyword FROM ad_clicks k JOIN ad_accounts a ON a.id = k.ad_account_id
		WHERE k.workspace_id = $1 AND k.click_id = $2 AND (a.brand_id IS NULL OR a.brand_id = $3::uuid) LIMIT 1`, ws, c.id, brand).
		Scan(&c.campaignExt, &c.adGroupExt, &c.adExt, &c.keyword)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err == nil {
		c.matchedBy = "click_id"
	}
	return err
}

// resolve maps a click's network ids to the brand's ad entities, and the
// campaign by its name from the UTM tags when the click carried no id.
func resolve(ctx context.Context, q Querier, ws, brand string, c *click, utmCampaign string) error {
	find := func(level, external, by string) (*string, *string, string, error) {
		var id, acct, connector string
		err := q.QueryRow(ctx, `SELECT e.id::text, a.id::text, a.connector FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id
			WHERE e.workspace_id = $1 AND COALESCE(e.brand_id, a.brand_id) = $2::uuid AND e.level = $3 AND `+by+` AND ($5 = '' OR a.connector = $5)
			ORDER BY a.status, e.updated_at DESC LIMIT 1`, ws, brand, level, external, c.connector).Scan(&id, &acct, &connector)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, "", nil
		}
		return &id, &acct, connector, err
	}
	var err error
	var acct *string
	var connector string
	if c.adExt != "" {
		if c.adID, acct, connector, err = find("ad", c.adExt, `e.external_id = $4`); err != nil {
			return err
		}
	}
	if c.adGroupExt != "" {
		if c.adGroupID, acct, connector, err = find("ad_group", c.adGroupExt, `e.external_id = $4`); err != nil {
			return err
		}
	}
	if c.campaignExt != "" {
		if c.campaignID, acct, connector, err = find("campaign", c.campaignExt, `e.external_id = $4`); err != nil {
			return err
		}
	} else if utmCampaign != "" && c.adGroupID == nil && c.adID == nil {
		if c.campaignID, acct, connector, err = find("campaign", utmCampaign, `lower(e.name) = lower($4)`); err != nil {
			return err
		}
	}
	// Parents fill what the click didn't name.
	for _, step := range []struct{ from, to **string }{{&c.adID, &c.adGroupID}, {&c.adGroupID, &c.campaignID}} {
		if *step.from != nil && *step.to == nil {
			var parent *string
			if err := q.QueryRow(ctx, `SELECT parent_id::text FROM ad_entities WHERE id = $1`, **step.from).Scan(&parent); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			*step.to = parent
		}
	}
	if acct != nil {
		c.accountID = acct
		if c.connector == "" {
			c.connector = connector
		}
	}
	return nil
}

// acquisition is an ad_acquisitions row's identity.
type acquisition struct {
	id string
}

func existing(ctx context.Context, q Querier, brand, record string) (*acquisition, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT id::text FROM ad_acquisitions WHERE brand_id = $1 AND record_id = $2`, brand, record).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &acquisition{id: id}, nil
}

// insert writes a new acquisition from a click, with the event that made
// it (a sign-up or a first payment).
func (r *run) insert(ctx context.Context, tx pgx.Tx, st *brandState, e event, c *click, signedUp, firstPaid *time.Time, firstMinor *int64, currency string) (string, error) {
	_, consent, _, err := adconv.Consent(ctx, tx, r.ws, &e.record, e.anon, st.mode)
	if err != nil {
		return "", err
	}
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO ad_acquisitions (workspace_id, brand_id, record_id, connector, ad_account_id, campaign_entity_id, ad_group_entity_id, ad_entity_id,
			campaign_external_id, ad_group_external_id, ad_external_id, keyword, click_id_kind, click_id, session_id, clicked_at, matched_by, window_days,
			signed_up_at, first_paid_at, first_payment_minor, currency, ad_consent)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23)
		ON CONFLICT (brand_id, record_id) DO UPDATE SET
			signed_up_at = COALESCE(ad_acquisitions.signed_up_at, EXCLUDED.signed_up_at),
			first_paid_at = COALESCE(ad_acquisitions.first_paid_at, EXCLUDED.first_paid_at),
			first_payment_minor = COALESCE(ad_acquisitions.first_payment_minor, EXCLUDED.first_payment_minor),
			currency = CASE WHEN ad_acquisitions.currency = '' THEN EXCLUDED.currency ELSE ad_acquisitions.currency END,
			updated_at = now()
		RETURNING id::text`,
		r.ws, *e.brand, e.record, c.connector, c.accountID, c.campaignID, c.adGroupID, c.adID,
		c.campaignExt, c.adGroupExt, c.adExt, cut(c.keyword, 200), c.kind, cut(c.id, 500), c.sessionID, c.at, c.matchedBy, st.econ.AcquisitionWindowDays,
		signedUp, firstPaid, firstMinor, currency, consent).Scan(&id)
	if err != nil {
		return "", err
	}
	kind := "signup"
	if firstPaid != nil {
		kind = "first_payment"
	}
	r.res.Acquisitions++
	metrics.PaidAdsAcquisition(labelConnector(c.connector), kind)
	track.Emit(ctx, track.Event{Name: "ad_acquisition.recorded", WorkspaceID: r.ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"brand_id": *e.brand, "connector": c.connector, "kind": kind, "matched_by": c.matchedBy, "click_id_kind": c.kind,
			"campaign_known": c.campaignID != nil, "ad_known": c.adID != nil}})
	return id, nil
}

// signup records a sign-up: an acquisition when the person has none yet
// and a paid click within the window brought them, or the sign-up time on
// the acquisition they have.
func (r *run) signup(ctx context.Context, tx pgx.Tx, st *brandState, e event) error {
	if acq, err := existing(ctx, tx, *e.brand, e.record); err != nil {
		return err
	} else if acq != nil {
		_, err := tx.Exec(ctx, `UPDATE ad_acquisitions SET signed_up_at = COALESCE(signed_up_at, $2), updated_at = now() WHERE id = $1`, acq.id, e.at)
		metrics.PaidAdsLedgerEvent("signup", "known")
		return err
	}
	c, err := locate(ctx, tx, r.ws, *e.brand, e.record, e.anon, e.at, st.econ.AcquisitionWindowDays)
	if err != nil {
		return err
	}
	if c == nil {
		metrics.PaidAdsLedgerEvent("signup", "no_click")
		return nil
	}
	at := e.at
	_, err = r.insert(ctx, tx, st, e, c, &at, nil, nil, "")
	if err == nil {
		metrics.PaidAdsLedgerEvent("signup", "acquired")
	}
	return err
}

// payment records a payment: a person's first ever payment makes them a
// new customer (on their acquisition, or a new one when the payment itself
// had a paid click in the window); a later one is returning revenue, with
// the campaign whose paid click was last before it within the window.
func (r *run) payment(ctx context.Context, tx pgx.Tx, st *brandState, e event) error {
	var first bool
	if err := tx.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM customer_events p
		WHERE p.workspace_id = $1 AND p.record_id = $2 AND p.kind = 'track' AND p.name = 'payment.succeeded'
		  AND (p.occurred_at, p.id) < ($3, $4::uuid)
		  AND COALESCE(p.brand_id, outflare_first_brand(p.workspace_id)) = $5::uuid)`, r.ws, e.record, e.at, e.id, *e.brand).Scan(&first); err != nil {
		return err
	}
	minor, currency := value(e.props, st.econ.Currency)
	touch, err := locate(ctx, tx, r.ws, *e.brand, e.record, e.anon, e.at, st.econ.AcquisitionWindowDays)
	if err != nil {
		return err
	}
	acq, err := existing(ctx, tx, *e.brand, e.record)
	if err != nil {
		return err
	}
	var acqID *string
	switch {
	case acq != nil && first:
		if _, err := tx.Exec(ctx, `UPDATE ad_acquisitions SET first_paid_at = COALESCE(first_paid_at, $2), first_payment_minor = COALESCE(first_payment_minor, $3),
			currency = CASE WHEN currency = '' THEN $4 ELSE currency END, updated_at = now() WHERE id = $1`, acq.id, e.at, minor, currency); err != nil {
			return err
		}
		acqID = &acq.id
	case acq != nil:
		acqID = &acq.id
	case first && touch != nil:
		at := e.at
		id, err := r.insert(ctx, tx, st, e, touch, nil, &at, &minor, currency)
		if err != nil {
			return err
		}
		acqID = &id
	}
	if acqID == nil && touch == nil {
		metrics.PaidAdsLedgerEvent("payment", "not_ads")
		return nil
	}
	bucket := "returning"
	if first {
		bucket = "new"
	}
	var touchCampaign *string
	touchConnector := ""
	if touch != nil {
		touchCampaign, touchConnector = touch.campaignID, touch.connector
	}
	return r.revenue(ctx, tx, e, acqID, touchCampaign, touchConnector, bucket, minor, currency, "payment")
}

// revenue writes one ad_revenue row, once per event.
func (r *run) revenue(ctx context.Context, tx pgx.Tx, e event, acqID, touchCampaign *string, touchConnector, bucket string, minor int64, currency, kind string) error {
	tag, err := tx.Exec(ctx, `INSERT INTO ad_revenue (event_id, workspace_id, brand_id, record_id, acquisition_id, touch_campaign_entity_id, touch_connector, bucket, value_minor, currency, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) ON CONFLICT (event_id) DO NOTHING`,
		e.id, r.ws, *e.brand, e.record, acqID, touchCampaign, touchConnector, bucket, minor, currency, e.at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		metrics.PaidAdsLedgerEvent(kind, "known")
		return nil
	}
	r.res.Revenue++
	metrics.PaidAdsLedgerEvent(kind, "recorded")
	track.Emit(ctx, track.Event{Name: "ad_revenue.recorded", WorkspaceID: r.ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"brand_id": *e.brand, "bucket": bucket, "refund": kind == "refund", "acquired": acqID != nil, "touched": touchCampaign != nil, "touch_connector": touchConnector}})
	return nil
}

// refund takes a payment's revenue back: a negative row with the same
// acquisition, touch and bucket as the payment, found by its payment_id.
func (r *run) refund(ctx context.Context, tx pgx.Tx, st *brandState, e event) error {
	var props map[string]any
	_ = json.Unmarshal(e.props, &props)
	paymentID, _ := props["payment_id"].(string)
	if paymentID == "" {
		metrics.PaidAdsLedgerEvent("refund", "no_payment")
		return nil
	}
	var acqID, touchCampaign *string
	var touchConnector, bucket, currency string
	var paid int64
	err := tx.QueryRow(ctx, `SELECT r.acquisition_id::text, r.touch_campaign_entity_id::text, r.touch_connector, r.bucket, r.currency, r.value_minor
		FROM ad_revenue r JOIN customer_events o ON o.id = r.event_id
		WHERE r.workspace_id = $1 AND r.brand_id = $2 AND o.name = 'payment.succeeded' AND o.properties->>'payment_id' = $3
		ORDER BY o.occurred_at LIMIT 1`, r.ws, *e.brand, paymentID).Scan(&acqID, &touchCampaign, &touchConnector, &bucket, &currency, &paid)
	if errors.Is(err, pgx.ErrNoRows) {
		metrics.PaidAdsLedgerEvent("refund", "no_payment")
		return nil
	}
	if err != nil {
		return err
	}
	minor, cur := value(e.props, currency)
	if minor == 0 {
		minor = paid // a refund with no amount takes the whole payment back
	}
	if minor > 0 {
		minor = -minor
	}
	if cur == "" {
		cur = currency
	}
	return r.revenue(ctx, tx, e, acqID, touchCampaign, touchConnector, bucket, minor, cur, "refund")
}

// value is an event's money in minor units and its currency: a payment's
// amount_minor, or Segment-style revenue or value in major units; the
// currency from the event, else fallback.
func value(props []byte, fallback string) (int64, string) {
	var p map[string]any
	_ = json.Unmarshal(props, &p)
	cur, _ := p["currency"].(string)
	cur = strings.ToUpper(strings.TrimSpace(cur))
	if len(cur) != 3 {
		cur = strings.ToUpper(fallback)
	}
	if len(cur) != 3 {
		cur = "USD"
	}
	num := func(k string) (float64, bool) {
		switch x := p[k].(type) {
		case float64:
			return x, true
		case string:
			var f float64
			if err := json.Unmarshal([]byte(x), &f); err == nil {
				return f, true
			}
		}
		return 0, false
	}
	if f, ok := num("amount_minor"); ok {
		return int64(math.Round(f)), cur
	}
	if f, ok := num("revenue"); ok {
		return int64(math.Round(f * 100)), cur
	}
	if f, ok := num("value"); ok {
		return int64(math.Round(f * 100)), cur
	}
	return 0, cur
}

func cut(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// labelConnector keeps metric labels bounded: a catalog id or other.
func labelConnector(c string) string {
	if c == "" {
		return "other"
	}
	return c
}
