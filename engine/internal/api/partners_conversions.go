package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/partners"
)

// Partner conversions: the workspace's own backend reports a sign-up (a
// sign-up), a sale or a refund, server to server, the way it would call
// Tolt. The partner is found from what the conversion carries (a promo
// code, the ref code, the click id from the short link), then from a
// referral already bound to the customer, then from the customer's own
// site visits that carried a ref inside the program's window. A browser
// visit only proposes a partner; the conversion binds it.
//
// A conversion that names no partner, or whose partner can't be credited,
// is answered with credited false and a reason, never an error, so a
// backend can report every sale and let Outflare pick the referred ones.

// conversionInput is a conversion as the API, a postback or an import
// reports it.
type conversionInput struct {
	Type        string `json:"type"`
	EventName   string `json:"event_name"`
	Recurring   bool   `json:"recurring"`
	Program     string `json:"program"`
	Ref         string `json:"ref"`
	ClickID     string `json:"click_id"`
	PromoCode   string `json:"promo_code"`
	CustomerID  string `json:"customer_id"`
	Email       string `json:"email"`
	RecordID    string `json:"record_id"`
	ExternalID  string `json:"external_id"`
	RefundOf    string `json:"refund_of"`
	AmountCents *int64 `json:"amount_cents"`
	Currency    string `json:"currency"`
	ProductID   string `json:"product_id"`
	SubID       string `json:"sub_id"`
	// AnonymousID is the website visitor's id (outflare.anonymousId()):
	// their visits with a partner's ref credit the partner, as the
	// customer's own visits do once the product identifies them.
	AnonymousID string  `json:"anonymous_id"`
	OccurredAt  *string `json:"occurred_at"`
	at          time.Time
}

func (in *conversionInput) check(now time.Time) error {
	in.Type = strings.ToLower(strings.TrimSpace(in.Type))
	if in.Type == "lead" {
		in.Type = "signup"
	}
	in.EventName = strings.TrimSpace(in.EventName)
	in.Currency = strings.ToUpper(strings.TrimSpace(in.Currency))
	in.Ref, in.PromoCode = strings.TrimSpace(in.Ref), strings.TrimSpace(in.PromoCode)
	in.CustomerID, in.ExternalID, in.RefundOf = strings.TrimSpace(in.CustomerID), strings.TrimSpace(in.ExternalID), strings.TrimSpace(in.RefundOf)
	switch {
	case !slices.Contains([]string{"signup", "sale", "refund", "custom"}, in.Type):
		return errInvalid("type is signup (or lead), sale, refund or custom")
	case in.Type == "custom" && (!conversionEventRE.MatchString(in.EventName) || slices.Contains(reservedConversionEvents, in.EventName)):
		return errInvalid("event_name is required for a custom conversion: lowercase letters, digits, dots and underscores, such as trial_started, and not one of signup, sale, refund, signed_up, payment.succeeded or payment.refunded")
	case in.Type != "custom" && in.EventName != "":
		return errInvalid("event_name is only for a custom conversion")
	case in.Type == "sale" && in.AmountCents == nil:
		return errInvalid("amount_cents is required for a sale, in the smallest unit of the currency (4900 is $49.00)")
	case in.AmountCents != nil && *in.AmountCents < 0:
		return errInvalid("amount_cents can't be negative; report a refund with type refund")
	case in.Type == "refund" && in.RefundOf == "":
		return errInvalid("refund_of is required for a refund: the external_id of the sale it refunds")
	case in.Type != "refund" && in.CustomerID == "" && in.Email == "" && in.RecordID == "":
		return errInvalid("customer_id, email or record_id is required, so the customer can be found again for later sales and refunds")
	case in.Currency != "" && !currencyRE.MatchString(in.Currency):
		return errInvalid("currency is a three-letter code such as USD")
	case in.Ref != "" && !partnerCodeRE.MatchString(in.Ref), in.PromoCode != "" && !partnerCodeRE.MatchString(in.PromoCode):
		return errInvalid("ref and promo_code are partner codes: letters, digits, hyphens and underscores")
	case in.ClickID != "" && !validUUID(in.ClickID):
		return errInvalid("click_id is the ref_click value from a partner link")
	case in.RecordID != "" && !validUUID(in.RecordID):
		return errInvalid("record_id is a record's id")
	case in.AnonymousID != "" && !siteCookieRe.MatchString(in.AnonymousID):
		return errInvalid("anonymous_id is the visitor's id from the website script (outflare.anonymousId())")
	case len(in.CustomerID) > 200 || len(in.ExternalID) > 200 || len(in.RefundOf) > 200 || len(in.ProductID) > 200 || len(in.SubID) > 100:
		return errInvalid("customer_id, external_id, refund_of and product_id are at most 200 characters, sub_id 100")
	}
	in.at = now
	if in.OccurredAt != nil && *in.OccurredAt != "" {
		t, err := time.Parse(time.RFC3339, *in.OccurredAt)
		if err != nil {
			secs, err2 := strconv.ParseInt(*in.OccurredAt, 10, 64)
			if err2 != nil {
				return errInvalid("occurred_at is an RFC 3339 time or Unix seconds")
			}
			t = time.Unix(secs, 0)
		}
		if t.After(now.Add(time.Hour)) || t.Before(now.AddDate(-5, 0, 0)) {
			return errInvalid("occurred_at must be within the last five years and not in the future")
		}
		in.at = t
	}
	return nil
}

// reservedConversionEvents are names a custom conversion can't take: the
// types' own names and the events they write.
var reservedConversionEvents = []string{"signup", "lead", "sale", "refund", "custom", "signed_up", "payment.succeeded", "payment.refunded"}

// customerEvent records a reported conversion on the customer's timeline,
// credited to a partner or not, under the names attribution reads by
// default: a sign-up is signed_up, a sale payment.succeeded (payment_id is
// its external_id), a refund payment.refunded (payment_id is the sale's),
// and a custom conversion its own event name. The sender's ids make a
// retry store it once.
func (c *conversionRun) customerEvent(ctx context.Context, rec string, amount *int64, currency string) error {
	if c.source == "billing" {
		return nil // the payment sync recorded it already
	}
	in := c.in
	props := map[string]any{"source": c.source}
	if in.ProductID != "" {
		props["product_id"] = in.ProductID
	}
	if currency == "" {
		currency = in.Currency
	}
	if amount != nil {
		props["amount_minor"] = *amount
		if currency != "" {
			props["currency"] = currency
		}
	}
	name, msg := "", ""
	switch in.Type {
	case "signup":
		name, msg = "signed_up", "conversion:signup:"+rec
		if in.ExternalID != "" {
			msg = "conversion:signup:" + in.ExternalID
		}
	case "sale":
		name = "payment.succeeded"
		props["recurring"] = in.Recurring
		if in.ExternalID != "" {
			props["payment_id"], msg = in.ExternalID, "conversion:sale:"+in.ExternalID
		}
	case "refund":
		name, props["payment_id"] = "payment.refunded", in.RefundOf
		if in.ExternalID != "" {
			msg = "conversion:refund:" + in.ExternalID
		}
	case "custom":
		name = in.EventName
		if in.ExternalID != "" {
			msg = "conversion:" + in.EventName + ":" + in.ExternalID
		}
	}
	_, err := customers.Record(ctx, c.tx, c.ws, customers.Event{RecordID: rec, Name: name, Properties: props, Source: "conversions",
		OccurredAt: in.at, MessageID: msg, RequestID: RequestID(ctx)})
	return err
}

type partnerRewardView struct {
	ID           string         `json:"id"`
	ProgramID    string         `json:"program_id"`
	PartnerID    string         `json:"partner_id"`
	Side         string         `json:"side"`
	RecipientID  string         `json:"recipient_id"`
	Kind         string         `json:"kind"`
	ConversionID *string        `json:"conversion_id"`
	RuleID       *string        `json:"rule_id"`
	ReversesID   *string        `json:"reverses_id"`
	AmountCents  int64          `json:"amount_cents"`
	Currency     string         `json:"currency"`
	Action       string         `json:"action"`
	ActionData   map[string]any `json:"action_data"`
	Status       string         `json:"status"`
	HoldUntil    time.Time      `json:"hold_until"`
	PayoutID     *string        `json:"payout_id"`
	Note         string         `json:"note"`
	ApprovedAt   *time.Time     `json:"approved_at"`
	CreatedAt    time.Time      `json:"created_at"`
}

const partnerRewardSelect = `SELECT w.id::text, w.program_id::text, w.membership_id::text, w.side, w.recipient_id::text, w.kind,
	w.conversion_id::text, w.rule_id::text, w.reverses_id::text, w.amount_cents, w.currency, w.action, w.action_data, w.status,
	w.hold_until, w.payout_id::text, w.note, w.approved_at, w.created_at FROM partner_rewards w`

func scanPartnerReward(row pgx.Row) (partnerRewardView, error) {
	var v partnerRewardView
	err := row.Scan(&v.ID, &v.ProgramID, &v.PartnerID, &v.Side, &v.RecipientID, &v.Kind, &v.ConversionID, &v.RuleID, &v.ReversesID,
		&v.AmountCents, &v.Currency, &v.Action, &v.ActionData, &v.Status, &v.HoldUntil, &v.PayoutID, &v.Note, &v.ApprovedAt, &v.CreatedAt)
	return v, err
}

type partnerConversionView struct {
	ID          string    `json:"id"`
	ProgramID   string    `json:"program_id"`
	PartnerID   string    `json:"partner_id"`
	ReferralID  string    `json:"referral_id"`
	RecordID    string    `json:"record_id"`
	Type        string    `json:"type"`
	EventName   string    `json:"event_name"`
	Recurring   bool      `json:"recurring"`
	ExternalID  string    `json:"external_id"`
	RefundOf    *string   `json:"refund_of"`
	AmountCents int64     `json:"amount_cents"`
	Currency    string    `json:"currency"`
	ProductID   string    `json:"product_id"`
	Source      string    `json:"source"`
	OccurredAt  time.Time `json:"occurred_at"`
	CreatedAt   time.Time `json:"created_at"`
}

const partnerConversionSelect = `SELECT v.id::text, v.program_id::text, f.membership_id::text, v.referral_id::text, v.record_id::text, v.type,
	v.event_name, v.recurring, v.external_id, v.refund_of::text, v.amount_cents, v.currency, v.product_id, v.source, v.occurred_at, v.created_at
	FROM partner_conversions v JOIN partner_referrals f ON f.id = v.referral_id`

func scanPartnerConversion(row pgx.Row) (partnerConversionView, error) {
	var v partnerConversionView
	err := row.Scan(&v.ID, &v.ProgramID, &v.PartnerID, &v.ReferralID, &v.RecordID, &v.Type, &v.EventName, &v.Recurring, &v.ExternalID, &v.RefundOf, &v.AmountCents,
		&v.Currency, &v.ProductID, &v.Source, &v.OccurredAt, &v.CreatedAt)
	return v, err
}

// conversionResult is what reporting a conversion did.
type conversionResult struct {
	Credited bool `json:"credited"`
	// Reason says why it wasn't credited: no_partner, partner_inactive,
	// program_inactive, self_referral, sale_not_found or currency.
	Reason     string                 `json:"reason,omitempty"`
	Duplicate  bool                   `json:"duplicate"`
	PartnerID  *string                `json:"partner_id"`
	ReferralID *string                `json:"referral_id"`
	Conversion *partnerConversionView `json:"conversion"`
	Rewards    []partnerRewardView    `json:"rewards"`
}

// membershipRow is what crediting needs to know about a partner.
type membershipRow struct {
	id, programID, recordID, status, programStatus, currency, kind, model string
	groupID, brandID                                                      *string
	windowDays, holdDays                                                  int
	autoApprove, codesWin                                                 bool
}

const membershipCols = `m.id::text, m.program_id::text, m.record_id::text, m.status, p.status, p.currency, p.kind, p.credit_model,
	m.group_id::text, p.brand_id::text, p.credit_window_days, p.hold_days, p.auto_approve, p.codes_win
	FROM partner_memberships m JOIN partner_programs p ON p.id = m.program_id`

func scanMembership(row pgx.Row) (membershipRow, error) {
	var m membershipRow
	err := row.Scan(&m.id, &m.programID, &m.recordID, &m.status, &m.programStatus, &m.currency, &m.kind, &m.model,
		&m.groupID, &m.brandID, &m.windowDays, &m.holdDays, &m.autoApprove, &m.codesWin)
	return m, err
}

func optMembership(row pgx.Row) (*membershipRow, error) {
	m, err := scanMembership(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// conversionRun is one conversion being recorded in a transaction.
type conversionRun struct {
	s       *Server
	tx      pgx.Tx
	p       *principal
	ws      string
	in      conversionInput
	source  string
	program string // the program's id when the caller named one
	res     conversionResult
}

func (c *conversionRun) audit(ctx context.Context, action, targetType, id string, data map[string]any) error {
	return partnerRecord(ctx, c.tx, c.p, c.ws, action, targetType, id, data)
}

// recordPartnerConversion records a conversion and the rewards it earns.
func (s *Server) recordPartnerConversion(ctx context.Context, ws string, p *principal, in conversionInput, source string) (conversionResult, error) {
	c := &conversionRun{s: s, p: p, ws: ws, in: in, source: source, res: conversionResult{Rewards: []partnerRewardView{}}}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		c.tx = tx
		c.res = conversionResult{Rewards: []partnerRewardView{}}
		if in.Program != "" {
			err := tx.QueryRow(ctx, `SELECT id::text FROM partner_programs WHERE workspace_id = $1 AND (id::text = lower($2) OR slug = lower($2))`,
				ws, in.Program).Scan(&c.program)
			if errors.Is(err, pgx.ErrNoRows) {
				return errInvalid("program: no such partner program")
			}
			if err != nil {
				return err
			}
		}
		if in.Type == "refund" {
			return c.refund(ctx)
		}
		return c.leadOrSale(ctx)
	})
	// The same conversion sent twice at once: the second sees the first's.
	if pgErr := (*pgconn.PgError)(nil); errors.As(err, &pgErr) && pgErr.Code == "23505" {
		err = errConflict
	}
	outcome := "credited"
	switch {
	case err != nil && errors.As(err, new(errInvalid)):
		outcome = "rejected"
	case err != nil:
		outcome = "error"
	case c.res.Duplicate:
		outcome = "duplicate"
	case !c.res.Credited:
		outcome = "not_credited"
	}
	metrics.PartnerConversion(in.Type, source, outcome)
	return c.res, err
}

// customer finds or makes the customer's record from the ids the
// conversion carries. A customer id is the workspace's own user id, held
// verified: it came from the workspace's own server.
func (c *conversionRun) customer(ctx context.Context) (string, error) {
	in, tx, ws := c.in, c.tx, c.ws
	if in.RecordID != "" {
		var rec string
		err := tx.QueryRow(ctx, `SELECT COALESCE(merged_into, id)::text FROM records WHERE workspace_id = $1 AND id = $2`, ws, in.RecordID).Scan(&rec)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errInvalid("record_id: no such record in this workspace")
		}
		return rec, err
	}
	email := ""
	if in.Email != "" {
		e, err := normalizeEmail(in.Email)
		if err != nil || e == "" {
			return "", errInvalid("email is not a valid address")
		}
		email = e
	}
	var rec string
	var err error
	if in.CustomerID != "" {
		id, ok, err := customers.OwnerOf(ctx, tx, ws, "user_id", in.CustomerID)
		if err != nil {
			return "", err
		}
		if ok {
			rec = id
		} else if email != "" {
			if rec, err = customers.EnsurePerson(ctx, tx, ws, "email", email, false, RequestID(ctx)); err != nil {
				return "", err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO record_identities (workspace_id, record_id, namespace, value, verified, verified_at, source)
				VALUES ($1, $2, 'user_id', $3, true, now(), 'partners') ON CONFLICT DO NOTHING`, ws, rec, in.CustomerID); err != nil {
				return "", err
			}
		} else if rec, err = customers.EnsurePerson(ctx, tx, ws, "user_id", in.CustomerID, true, RequestID(ctx)); err != nil {
			return "", err
		}
	} else if rec, err = customers.EnsurePerson(ctx, tx, ws, "email", email, false, RequestID(ctx)); err != nil {
		return "", err
	}
	if email != "" && in.CustomerID != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO record_identities (workspace_id, record_id, namespace, value, verified, source)
			VALUES ($1, $2, 'email', $3, false, 'partners') ON CONFLICT DO NOTHING`, ws, rec, email); err != nil {
			return "", err
		}
		if _, err := flagDuplicates(ctx, tx, ws, rec, "email", email); err != nil {
			return "", err
		}
	}
	return rec, nil
}

// named finds the partner a conversion names itself: a promo code, a ref
// code or a click id. A promo code beats a link when its program says so.
func (c *conversionRun) named(ctx context.Context) (m *membershipRow, via, clickID string, err error) {
	in, tx, ws := c.in, c.tx, c.ws
	var promo, ref, click *membershipRow
	if in.PromoCode != "" {
		if promo, err = optMembership(tx.QueryRow(ctx, `SELECT `+membershipCols+`
			WHERE m.workspace_id = $1 AND (m.id IN (SELECT membership_id FROM partner_promo_codes WHERE workspace_id = $1 AND lower(code) = lower($2))
			OR lower(m.code) = lower($2))`, ws, in.PromoCode)); err != nil {
			return
		}
	}
	if in.Ref != "" {
		if ref, err = optMembership(tx.QueryRow(ctx, `SELECT `+membershipCols+` WHERE m.workspace_id = $1 AND lower(m.code) = lower($2)`, ws, in.Ref)); err != nil {
			return
		}
	}
	if in.ClickID != "" {
		// A click counts inside its program's credit window.
		if click, err = optMembership(tx.QueryRow(ctx, `SELECT `+membershipCols+` JOIN partner_clicks k ON k.membership_id = m.id
			WHERE m.workspace_id = $1 AND k.id = $2 AND k.created_at >= $3::timestamptz - make_interval(days => p.credit_window_days)`,
			ws, in.ClickID, c.in.at)); err != nil {
			return
		}
	}
	inProgram := func(x *membershipRow) bool { return x != nil && (c.program == "" || x.programID == c.program) }
	order := []struct {
		m   *membershipRow
		via string
	}{{ref, "ref"}, {click, "click"}, {promo, "promo_code"}}
	if inProgram(promo) && promo.codesWin {
		order = append(order[2:], order[:2]...)
	}
	for _, o := range order {
		if inProgram(o.m) {
			if o.via == "click" {
				clickID = in.ClickID
			}
			return o.m, o.via, clickID, nil
		}
	}
	return nil, "", "", nil
}

// visited finds the partner by the customer's own site visits with a ref
// code, each program by its own window and first or last rule. When
// several programs could take it, the latest credited visit wins.
func (c *conversionRun) visited(ctx context.Context, rec string) (*membershipRow, error) {
	rows, err := c.tx.Query(ctx, `SELECT s.started_at, m.id::text, m.program_id::text, p.credit_window_days, p.credit_model
		FROM site_sessions s
		JOIN partner_memberships m ON m.workspace_id = s.workspace_id AND lower(m.code) = lower(s.ref)
		JOIN partner_programs p ON p.id = m.program_id
		WHERE s.workspace_id = $1 AND (s.record_id = $2 OR ($5 <> '' AND s.anonymous_id = $5)) AND s.ref <> '' AND ($3 = '' OR m.program_id::text = $3)
		  AND s.started_at >= $4::timestamptz - interval '366 days' AND s.started_at <= $4::timestamptz + interval '1 hour'`,
		c.ws, rec, c.program, c.in.at, c.in.AnonymousID)
	if err != nil {
		return nil, err
	}
	type prog struct {
		window int
		model  string
		visits []partners.Visit
	}
	progs := map[string]*prog{}
	for rows.Next() {
		var at time.Time
		var mid, pid, model string
		var window int
		if err := rows.Scan(&at, &mid, &pid, &window, &model); err != nil {
			rows.Close()
			return nil, err
		}
		if progs[pid] == nil {
			progs[pid] = &prog{window: window, model: model}
		}
		progs[pid].visits = append(progs[pid].visits, partners.Visit{At: at, MembershipID: mid})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var best partners.Visit
	found := false
	for _, p := range progs {
		if v, ok := partners.Credit(p.visits, c.in.at, p.window, p.model); ok && (!found || v.At.After(best.At)) {
			best, found = v, true
		}
	}
	if !found {
		return nil, nil
	}
	return optMembership(c.tx.QueryRow(ctx, `SELECT `+membershipCols+` WHERE m.id = $1`, best.MembershipID))
}

type referralRow struct {
	id, membershipID string
	firstSaleAt      *time.Time
}

// existingReferral is the customer's referral in a program ("" is any
// program, the newest first).
func (c *conversionRun) existingReferral(ctx context.Context, rec, program string) (*referralRow, error) {
	var f referralRow
	err := c.tx.QueryRow(ctx, `SELECT id::text, membership_id::text, first_sale_at FROM partner_referrals
		WHERE workspace_id = $1 AND record_id = $2 AND ($3 = '' OR program_id::text = $3) ORDER BY created_at DESC LIMIT 1 FOR UPDATE`,
		c.ws, rec, program).Scan(&f.id, &f.membershipID, &f.firstSaleAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (c *conversionRun) leadOrSale(ctx context.Context) error {
	in, tx := c.in, c.tx
	rec, err := c.customer(ctx)
	if err != nil {
		return err
	}
	if err := c.customerEvent(ctx, rec, in.AmountCents, ""); err != nil {
		return err
	}
	m, via, clickID, err := c.named(ctx)
	if err != nil {
		return err
	}
	var f *referralRow
	program := c.program
	if m != nil {
		program = m.programID
	}
	// The first partner bound in a program keeps the customer.
	if f, err = c.existingReferral(ctx, rec, program); err != nil {
		return err
	}
	if f != nil {
		if m, err = optMembership(tx.QueryRow(ctx, `SELECT `+membershipCols+` WHERE m.id = $1`, f.membershipID)); err != nil {
			return err
		}
	} else if m == nil {
		if m, err = c.visited(ctx, rec); err != nil {
			return err
		}
		via = "visit"
	}
	switch {
	case m == nil:
		c.res.Reason = "no_partner"
		return nil
	case m.recordID == rec:
		c.res.Reason = "self_referral"
		return nil
	case m.programStatus != "active":
		c.res.Reason = "program_inactive"
		return nil
	case m.status != "active":
		c.res.Reason = "partner_inactive"
		return nil
	case in.Currency != "" && in.Currency != m.currency && in.Type == "sale":
		c.res.Reason = "currency"
		return nil
	}
	c.res.PartnerID = &m.id
	if f == nil {
		f = &referralRow{membershipID: m.id}
		var click *string
		if clickID != "" {
			click = &clickID
		}
		err := tx.QueryRow(ctx, `INSERT INTO partner_referrals (workspace_id, program_id, membership_id, record_id, via, click_id, sub_id, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, LEAST(now(), $8)) ON CONFLICT (program_id, record_id) DO NOTHING RETURNING id::text`,
			c.ws, m.programID, m.id, rec, via, click, cutUTF8(in.SubID, 100), in.at).Scan(&f.id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errConflict // another conversion bound them first; the caller retries
		}
		if err != nil {
			return err
		}
		if _, err := customers.Record(ctx, tx, c.ws, customers.Event{RecordID: rec, Name: "partner.referred", Source: "partners",
			Properties: map[string]any{"program_id": m.programID, "program_kind": m.kind, "partner_id": m.id, "via": via},
			MessageID:  "partner.referred:" + f.id, OccurredAt: in.at, RequestID: RequestID(ctx)}); err != nil {
			return err
		}
		if err := c.audit(ctx, "partner_referral.created", "partner_referral", f.id, map[string]any{"program_id": m.programID,
			"partner_id": m.id, "record_id": rec, "via": via, "source": c.source}); err != nil {
			return err
		}
	}
	c.res.ReferralID = &f.id

	// A retry of the same conversion is stored once.
	var dup string
	if in.ExternalID != "" {
		err = tx.QueryRow(ctx, `SELECT id::text FROM partner_conversions WHERE program_id = $1 AND type = $2 AND external_id = $3`,
			m.programID, in.Type, in.ExternalID).Scan(&dup)
	} else if in.Type == "signup" {
		err = tx.QueryRow(ctx, `SELECT id::text FROM partner_conversions WHERE referral_id = $1 AND type = 'signup' LIMIT 1`, f.id).Scan(&dup)
	} else {
		err = pgx.ErrNoRows
	}
	if err == nil {
		return c.duplicate(ctx, dup)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var prior int
	if in.Type == "sale" {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM partner_conversions v JOIN partner_referrals f ON f.id = v.referral_id
			WHERE f.membership_id = $1 AND v.type = 'sale'`, m.id).Scan(&prior); err != nil {
			return err
		}
	}
	amount := int64(0)
	if in.AmountCents != nil {
		amount = *in.AmountCents
	}
	var conv string
	if err := tx.QueryRow(ctx, `INSERT INTO partner_conversions (workspace_id, program_id, referral_id, record_id, type, event_name, recurring,
		external_id, amount_cents, currency, product_id, source, occurred_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) RETURNING id::text`,
		c.ws, m.programID, f.id, rec, in.Type, in.EventName, in.Recurring, in.ExternalID, amount, m.currency, in.ProductID, c.source, in.at).Scan(&conv); err != nil {
		return err
	}
	if in.Type == "sale" {
		if _, err := tx.Exec(ctx, `UPDATE partner_referrals SET status = 'customer', first_sale_at = LEAST(COALESCE(first_sale_at, $2), $2) WHERE id = $1`,
			f.id, in.at); err != nil {
			return err
		}
	}
	if err := c.audit(ctx, "partner_conversion.created", "partner_conversion", conv, map[string]any{"program_id": m.programID,
		"partner_id": m.id, "referral_id": f.id, "record_id": rec, "type": in.Type, "event_name": in.EventName, "recurring": in.Recurring,
		"amount_cents": amount, "currency": m.currency,
		"product_id": in.ProductID, "source": c.source}); err != nil {
		return err
	}
	rules, err := partnerRules(ctx, tx, c.ws, m.programID)
	if err != nil {
		return err
	}
	trigger := in.Type
	if in.Type == "custom" {
		trigger = in.EventName
	}
	earned := partners.Rewards(rules, partners.Conversion{Trigger: trigger, AmountCents: amount, ProductID: in.ProductID,
		PartnerID: m.id, GroupID: m.groupID, PriorSales: prior, FirstSaleAt: f.firstSaleAt, At: in.at})
	if c.source == "browser" {
		// Anyone can call outflare.signup(): it makes the lead, and pays
		// only when the program trusts it to.
		var pay bool
		if err := tx.QueryRow(ctx, `SELECT pay_browser_signups FROM partner_programs WHERE id = $1`, m.programID).Scan(&pay); err != nil {
			return err
		}
		if !pay {
			earned = nil
		}
	}
	hold := in.at.AddDate(0, 0, m.holdDays)
	for _, e := range earned {
		recipient := m.recordID
		if e.Side == "referred" {
			recipient = rec
		}
		ruleID := e.Rule.ID
		id, err := insertPartnerReward(ctx, tx, c.ws, rewardEntry{program: m.programID, membership: m.id, side: e.Side, recipient: recipient,
			kind: e.Kind, conversion: &conv, rule: &ruleID, amount: e.AmountCents, currency: m.currency, action: e.Rule.Action,
			actionData: e.Rule.ActionData, hold: hold})
		if err != nil {
			return err
		}
		d, err := rewardData(ctx, tx, m, e.Side, e.Kind, recipient, conv, e.AmountCents, e.Rule.Action, e.Rule.ActionData, hold)
		if err != nil {
			return err
		}
		if err := c.audit(ctx, "partner_reward.created", "partner_reward", id, d); err != nil {
			return err
		}
		if m.autoApprove && !hold.After(time.Now()) {
			if err := approvePartnerReward(ctx, tx, c.p, c.ws, id, "auto"); err != nil {
				return err
			}
		}
	}
	if slices.ContainsFunc(earned, func(e partners.Earned) bool { return e.Side == "partner" }) {
		if err := enqueuePartnerNotify(ctx, tx, c.ws, partnerNotify{Kind: "conversion", PartnerID: m.id, Ref: conv}); err != nil {
			return err
		}
	}
	if err := enqueuePartnerPostback(ctx, tx, c.ws, m.id, conv); err != nil {
		return err
	}
	return c.finish(ctx, conv)
}

// finish loads what the conversion stored.
func (c *conversionRun) finish(ctx context.Context, conv string) error {
	v, err := scanPartnerConversion(c.tx.QueryRow(ctx, partnerConversionSelect+` WHERE v.id = $1`, conv))
	if err != nil {
		return err
	}
	c.res.Credited, c.res.Conversion = true, &v
	c.res.PartnerID, c.res.ReferralID = &v.PartnerID, &v.ReferralID
	rows, err := c.tx.Query(ctx, partnerRewardSelect+` WHERE w.conversion_id = $1 ORDER BY w.created_at, w.id`, conv)
	if err != nil {
		return err
	}
	c.res.Rewards, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (partnerRewardView, error) { return scanPartnerReward(row) })
	if c.res.Rewards == nil {
		c.res.Rewards = []partnerRewardView{}
	}
	return err
}

func (c *conversionRun) duplicate(ctx context.Context, conv string) error {
	c.res.Duplicate = true
	return c.finish(ctx, conv)
}

// rewardData is a new reward's webhook data, with the conversion that
// earned it.
func rewardData(ctx context.Context, tx pgx.Tx, m *membershipRow, side, kind, recipient, conv string, amount int64, action string,
	actionData map[string]any, hold time.Time) (map[string]any, error) {
	d := map[string]any{"program_id": m.programID, "program_kind": m.kind, "partner_id": m.id, "side": side, "kind": kind,
		"recipient_id": recipient, "amount_cents": amount, "currency": m.currency, "hold_until": hold}
	if conv != "" {
		if err := addConversionData(ctx, tx, d, conv); err != nil {
			return nil, err
		}
	}
	if action != "" {
		d["action"], d["action_data"] = action, actionData
	}
	return d, nil
}

// refund records a refund of a sale. Rewards from the sale still on hold
// are rejected (or, for a partial refund, reduced by a pending
// adjustment); approved and paid cash rewards get a negative adjustment
// that the next payout nets; approved or fulfilled actions get a
// reversal entry, so the workspace's system can take back what it gave.
func (c *conversionRun) refund(ctx context.Context) error {
	in, tx := c.in, c.tx
	var sale partnerConversionView
	sale, err := scanPartnerConversion(tx.QueryRow(ctx, partnerConversionSelect+`
		WHERE v.workspace_id = $1 AND v.type = 'sale' AND v.external_id = $2 AND ($3 = '' OR v.program_id::text = $3)
		ORDER BY v.created_at DESC LIMIT 1 FOR UPDATE OF v`, c.ws, in.RefundOf, c.program))
	if errors.Is(err, pgx.ErrNoRows) {
		c.res.Reason = "sale_not_found"
		// Not a referred sale, but still a refund for attribution.
		if in.CustomerID == "" && in.Email == "" && in.RecordID == "" {
			return nil
		}
		rec, err := c.customer(ctx)
		if err != nil {
			return err
		}
		return c.customerEvent(ctx, rec, in.AmountCents, "")
	}
	if err != nil {
		return err
	}
	c.res.PartnerID, c.res.ReferralID = &sale.PartnerID, &sale.ReferralID
	if in.ExternalID != "" {
		var dup string
		err := tx.QueryRow(ctx, `SELECT id::text FROM partner_conversions WHERE program_id = $1 AND type = 'refund' AND external_id = $2`,
			sale.ProgramID, in.ExternalID).Scan(&dup)
		if err == nil {
			return c.duplicate(ctx, dup)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	var already int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(amount_cents), 0)::bigint FROM partner_conversions WHERE refund_of = $1`, sale.ID).Scan(&already); err != nil {
		return err
	}
	left := sale.AmountCents - already
	amount := left
	if in.AmountCents != nil && *in.AmountCents < left {
		amount = *in.AmountCents
	}
	if amount < 0 {
		amount = 0
	}
	if err := c.customerEvent(ctx, sale.RecordID, &amount, sale.Currency); err != nil {
		return err
	}
	m, err := optMembership(tx.QueryRow(ctx, `SELECT `+membershipCols+` WHERE m.id = $1`, sale.PartnerID))
	if err != nil || m == nil {
		return err
	}
	var conv string
	if err := tx.QueryRow(ctx, `INSERT INTO partner_conversions (workspace_id, program_id, referral_id, record_id, type, external_id, refund_of,
		amount_cents, currency, product_id, source, occurred_at) VALUES ($1, $2, $3, $4, 'refund', $5, $6, $7, $8, $9, $10, $11) RETURNING id::text`,
		c.ws, sale.ProgramID, sale.ReferralID, sale.RecordID, in.ExternalID, sale.ID, amount, sale.Currency, sale.ProductID, c.source, in.at).Scan(&conv); err != nil {
		return err
	}
	if err := c.audit(ctx, "partner_conversion.created", "partner_conversion", conv, map[string]any{"program_id": sale.ProgramID,
		"partner_id": sale.PartnerID, "referral_id": sale.ReferralID, "record_id": sale.RecordID, "type": "refund", "amount_cents": amount,
		"currency": sale.Currency, "refund_of": sale.ID, "source": c.source}); err != nil {
		return err
	}
	full := amount >= left
	rows, err := tx.Query(ctx, partnerRewardSelect+` WHERE w.conversion_id = $1 AND w.reverses_id IS NULL ORDER BY w.created_at FOR UPDATE`, sale.ID)
	if err != nil {
		return err
	}
	rewards, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (partnerRewardView, error) { return scanPartnerReward(row) })
	if err != nil {
		return err
	}
	for _, w := range rewards {
		cash := w.Kind == "cash"
		share := partners.Share(w.AmountCents, amount, sale.AmountCents)
		switch {
		case w.Status == "rejected":
		case w.Status == "pending" && full && already == 0:
			if err := rejectReward(ctx, tx, c.p, c.ws, w, "refunded"); err != nil {
				return err
			}
		case cash && share > 0:
			// Held rewards get a held adjustment; the rest net at once.
			status := "approved"
			if w.Status == "pending" {
				status = "pending"
			}
			if err := c.reverse(ctx, m, w, conv, -share, status, "refund"); err != nil {
				return err
			}
		case w.Kind == "action" && full && w.Status != "pending":
			if err := c.reverse(ctx, m, w, conv, 0, "approved", "refund"); err != nil {
				return err
			}
		}
	}
	if full {
		if _, err := tx.Exec(ctx, `UPDATE partner_referrals f SET status = 'refunded' WHERE f.id = $1 AND NOT EXISTS (
			SELECT 1 FROM partner_conversions s WHERE s.referral_id = f.id AND s.type = 'sale'
			AND s.amount_cents > COALESCE((SELECT sum(r.amount_cents) FROM partner_conversions r WHERE r.refund_of = s.id), 0))`, sale.ReferralID); err != nil {
			return err
		}
	}
	if err := enqueuePartnerPostback(ctx, tx, c.ws, m.id, conv); err != nil {
		return err
	}
	return c.finish(ctx, conv)
}

// reverse adds a ledger entry that corrects w: amount (negative or 0) of
// kind adjustment, keeping an action's name and data so the workspace's
// system knows what to take back.
func (c *conversionRun) reverse(ctx context.Context, m *membershipRow, w partnerRewardView, conv string, amount int64, status, why string) error {
	id, err := insertPartnerReward(ctx, c.tx, c.ws, rewardEntry{program: w.ProgramID, membership: w.PartnerID, side: w.Side, recipient: w.RecipientID,
		kind: "adjustment", conversion: &conv, reverses: &w.ID, amount: amount, currency: w.Currency, action: w.Action, actionData: w.ActionData,
		hold: w.HoldUntil, note: why})
	if err != nil {
		return err
	}
	d, err := rewardData(ctx, c.tx, m, w.Side, "adjustment", w.RecipientID, conv, amount, w.Action, w.ActionData, w.HoldUntil)
	if err != nil {
		return err
	}
	d["reverses_id"], d["reason"] = w.ID, why
	if err := c.audit(ctx, "partner_reward.reversed", "partner_reward", id, d); err != nil {
		return err
	}
	metrics.PartnerRewards("adjustment", "reversed", 1)
	if status == "approved" {
		return approvePartnerReward(ctx, c.tx, c.p, c.ws, id, "refund")
	}
	return nil
}

// ---- The conversion API and postbacks ----

func (s *Server) createPartnerConversion(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in conversionInput
	if !decode(w, r, &in) {
		return
	}
	s.servePartnerConversion(w, r, a.WorkspaceID, &a.principal, in, "api")
}

func (s *Server) servePartnerConversion(w http.ResponseWriter, r *http.Request, ws string, p *principal, in conversionInput, source string) {
	if err := in.check(time.Now()); err != nil {
		metrics.PartnerConversion(in.Type, source, "rejected")
		s.allocError(w, r, err)
		return
	}
	res, err := s.recordPartnerConversion(r.Context(), ws, p, in, source)
	if errors.Is(err, errConflict) {
		// Two conversions for the same customer or charge raced; the retry
		// sees the first's referral, or answers duplicate.
		res, err = s.recordPartnerConversion(r.Context(), ws, p, in, source)
	}
	if s.allocError(w, r, err) {
		return
	}
	status := http.StatusOK
	if res.Credited && !res.Duplicate {
		status = http.StatusCreated
	}
	writeJSON(w, status, res)
}

// partnerPostback takes a conversion by URL, for partners' trackers and
// backends that can only call a URL. The secret in the path names the
// program. Parameters come from the query string, a form or a JSON body,
// with the names affiliate networks use as aliases. amount is in the
// currency's main unit (49.90); amount_cents in its smallest.
func (s *Server) partnerPostback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token := r.PathValue("token")
	var ws, program string
	err := pgx.ErrNoRows
	if strings.HasPrefix(token, auth.PrefixPostback) {
		err = s.pool.QueryRow(ctx, `SELECT workspace_id::text, id::text FROM partner_programs WHERE postback_hash = $1`, auth.HashToken(token)).Scan(&ws, &program)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		metrics.PartnerConversion("unknown", "postback", "rejected")
		writeError(w, r, http.StatusNotFound, "not_found", "this postback URL is not valid; make a new one on the program")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	noteWorkspace(r, ws)
	vals := r.URL.Query()
	if r.Method == http.MethodPost {
		body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "could not read the body")
			return
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			var m map[string]any
			if err := json.Unmarshal(body, &m); err != nil {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "the body is not a JSON object")
				return
			}
			for k, v := range m {
				switch t := v.(type) {
				case string:
					vals.Set(k, t)
				case float64:
					vals.Set(k, strconv.FormatFloat(t, 'f', -1, 64))
				}
			}
		} else if form, err := url.ParseQuery(string(body)); err == nil {
			for k, v := range form {
				vals[k] = v
			}
		}
	}
	in, err := postbackInput(vals)
	if s.allocError(w, r, err) {
		metrics.PartnerConversion("unknown", "postback", "rejected")
		return
	}
	in.Program = program
	s.servePartnerConversion(w, r, ws, nil, in, "postback")
}

// postbackInput reads a postback's parameters, accepting the usual aliases.
func postbackInput(v url.Values) (conversionInput, error) {
	get := func(names ...string) string {
		for _, n := range names {
			if x := strings.TrimSpace(v.Get(n)); x != "" {
				return x
			}
		}
		return ""
	}
	in := conversionInput{
		Type:       get("type", "event", "goal"),
		EventName:  get("event_name"),
		Recurring:  slices.Contains([]string{"1", "true", "yes"}, strings.ToLower(get("recurring"))),
		Ref:        get("ref", "ref_code", "partner_code"),
		ClickID:    get("click_id", "ref_click", "clickid"),
		PromoCode:  get("promo_code", "coupon", "coupon_code"),
		CustomerID: get("customer_id", "user_id"),
		Email:      get("email"),
		ExternalID: get("external_id", "order_id", "transaction_id", "charge_id", "txid"),
		RefundOf:   get("refund_of"),
		Currency:   get("currency"),
		ProductID:  get("product_id", "product"),
		SubID:      get("sub_id", "sub", "subid"),
	}
	if t := get("occurred_at", "timestamp"); t != "" {
		in.OccurredAt = &t
	}
	if c := get("amount_cents"); c != "" {
		n, err := strconv.ParseInt(c, 10, 64)
		if err != nil {
			return in, errInvalid("amount_cents is a whole number")
		}
		in.AmountCents = &n
	} else if a := get("amount", "payout", "sale_amount", "revenue"); a != "" {
		f, err := strconv.ParseFloat(a, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 1e12 {
			return in, errInvalid("amount is a number in the currency's main unit, such as 49.90")
		}
		n := int64(math.Round(f * 100))
		in.AmountCents = &n
	}
	return in, nil
}

func (s *Server) listPartnerConversions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	typ, partner, program := q.Get("type"), q.Get("partner_id"), strings.ToLower(q.Get("program"))
	if typ != "" && !slices.Contains([]string{"signup", "sale", "refund", "custom"}, typ) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "type is signup, sale, refund or custom")
		return
	}
	if partner != "" && !validUUID(partner) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "partner_id is a partner's id")
		return
	}
	limit, ok := pageLimit(w, r, 100, 500)
	if !ok {
		return
	}
	before, ok := beforeTime(w, r)
	if !ok {
		return
	}
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, typ, partner, program, before, limit + 1}
	rows, err := s.pool.Query(r.Context(), partnerConversionSelect+`
		JOIN partner_programs p ON p.id = v.program_id
		WHERE v.workspace_id = $1 AND ($2 = '' OR v.type = $2) AND ($3 = '' OR f.membership_id::text = $3)
		  AND ($4 = '' OR p.id::text = $4 OR p.slug = $4) AND ($5::timestamptz IS NULL OR v.created_at < $5)`+sel.sql("p.brand_id", &args)+`
		ORDER BY v.created_at DESC LIMIT $6`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (partnerConversionView, error) { return scanPartnerConversion(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *time.Time
	if len(list) > limit {
		list = list[:limit]
		next = &list[limit-1].CreatedAt
	}
	if list == nil {
		list = []partnerConversionView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

type partnerReferralView struct {
	ID          string     `json:"id"`
	ProgramID   string     `json:"program_id"`
	PartnerID   string     `json:"partner_id"`
	RecordID    string     `json:"record_id"`
	Via         string     `json:"via"`
	SubID       string     `json:"sub_id"`
	Status      string     `json:"status"`
	FirstSaleAt *time.Time `json:"first_sale_at"`
	SalesCents  int64      `json:"sales_cents"`
	CreatedAt   time.Time  `json:"created_at"`
}

func (s *Server) listPartnerReferrals(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	partner, record, status := q.Get("partner_id"), q.Get("record_id"), q.Get("status")
	if (partner != "" && !validUUID(partner)) || (record != "" && !validUUID(record)) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "partner_id and record_id are ids")
		return
	}
	if status != "" && !slices.Contains([]string{"lead", "customer", "refunded", "rejected"}, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is lead, customer, refunded or rejected")
		return
	}
	limit, ok := pageLimit(w, r, 100, 500)
	if !ok {
		return
	}
	before, ok := beforeTime(w, r)
	if !ok {
		return
	}
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, partner, record, status, before, limit + 1}
	rows, err := s.pool.Query(r.Context(), `SELECT f.id::text, f.program_id::text, f.membership_id::text, f.record_id::text, f.via, f.sub_id,
		f.status, f.first_sale_at, COALESCE((SELECT sum(CASE WHEN v.type = 'sale' THEN v.amount_cents ELSE -v.amount_cents END)
		FROM partner_conversions v WHERE v.referral_id = f.id AND v.type IN ('sale', 'refund')), 0)::bigint, f.created_at
		FROM partner_referrals f JOIN partner_programs p ON p.id = f.program_id
		WHERE f.workspace_id = $1 AND ($2 = '' OR f.membership_id::text = $2) AND ($3 = '' OR f.record_id::text = $3)
		AND ($4 = '' OR f.status = $4) AND ($5::timestamptz IS NULL OR f.created_at < $5)`+sel.sql("p.brand_id", &args)+` ORDER BY f.created_at DESC LIMIT $6`,
		args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[partnerReferralView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *time.Time
	if len(list) > limit {
		list = list[:limit]
		next = &list[limit-1].CreatedAt
	}
	if list == nil {
		list = []partnerReferralView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}
