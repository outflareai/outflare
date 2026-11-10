package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The Rewards list, a reward's proof and the proof export all read rewards through rewardRows, so they never disagree.

type rewardSale struct {
	ConversionID string    `json:"conversion_id"`
	Type         string    `json:"type"`
	ExternalID   string    `json:"external_id"`
	AmountCents  int64     `json:"amount_cents"`
	Currency     string    `json:"currency"`
	OccurredAt   time.Time `json:"occurred_at"`
}

type rewardRuleView struct {
	ID          *string `json:"id"`
	Name        string  `json:"name"`
	Trigger     string  `json:"trigger"`
	Kind        string  `json:"kind"`
	PercentBP   *int    `json:"percent_bp"`
	AmountCents *int64  `json:"amount_cents"`
	Recurrence  string  `json:"recurrence"`
	Months      *int    `json:"months"`
	Kept        bool    `json:"kept"`
}

type rewardDecision struct {
	Status    string     `json:"status"`
	By        string     `json:"by"`
	ActorType string     `json:"actor_type"`
	ActorName string     `json:"actor_name"`
	At        *time.Time `json:"at"`
	Note      string     `json:"note"`
	logged    bool
}

type partnerRewardRow struct {
	partnerRewardView
	PartnerCode      string          `json:"partner_code"`
	PartnerName      string          `json:"partner_name"`
	CustomerRecordID *string         `json:"customer_record_id"`
	CustomerName     string          `json:"customer_name"`
	CustomerEmail    string          `json:"customer_email"`
	Sale             *rewardSale     `json:"sale"`
	Rule             *rewardRuleView `json:"rule"`
	Source           string          `json:"source"`
	ImportedFrom     string          `json:"imported_from"`
	HeldAtSource     bool            `json:"held_at_source"`
	Decision         *rewardDecision `json:"decision"`
	ReversedBy       []string        `json:"reversed_by"`
	NetCents         int64           `json:"net_cents"`
	PayoutStatus     *string         `json:"payout_status"`
}

// The inner query picks the page, so the joins after it run for the page only.
const rewardRowSelect = `SELECT w.id::text, w.program_id::text, w.membership_id::text, w.side, w.recipient_id::text, w.kind,
	w.conversion_id::text, w.rule_id::text, w.reverses_id::text, w.amount_cents, w.currency, w.action, w.action_data, w.status,
	w.hold_until, w.payout_id::text, w.note, w.approved_at, w.created_at,
	m.code, COALESCE(mr.name, ''), v.record_id::text, COALESCE(c.name, ''), %s,
	v.id::text, v.type, v.external_id, v.amount_cents, v.currency, v.occurred_at,
	w.rule, rr.id::text, rr.name, rr.trigger, rr.kind, rr.percent_bp, rr.amount_cents, rr.recurrence, rr.months,
	COALESCE(w.imported_from, ''), %s, COALESCE(rv.ids, '{}'), w.amount_cents + COALESCE(rv.net, 0), o.status
	FROM (SELECT w.*, imp.connector AS imported_from FROM partner_rewards w JOIN partner_memberships m ON m.id = w.membership_id
		JOIN partner_programs p ON p.id = w.program_id LEFT JOIN partner_conversions v ON v.id = w.conversion_id
		LEFT JOIN LATERAL (SELECT x.connector FROM partner_import_refs x WHERE x.local_id = w.id AND x.kind = 'commission' LIMIT 1) imp ON true
		WHERE w.workspace_id = $1 AND (%s) ORDER BY %s LIMIT %d) w
	JOIN partner_memberships m ON m.id = w.membership_id
	LEFT JOIN records mr ON mr.id = m.record_id
	LEFT JOIN partner_conversions v ON v.id = w.conversion_id
	LEFT JOIN records c ON c.id = v.record_id
	LEFT JOIN partner_reward_rules rr ON rr.id = w.rule_id AND w.rule IS NULL
	LEFT JOIN partner_payouts o ON o.id = w.payout_id
	LEFT JOIN LATERAL (SELECT array_agg(a.id::text ORDER BY a.created_at, a.id) AS ids,
		sum(a.amount_cents) FILTER (WHERE a.status IN ('approved', 'paid')) AS net
		FROM partner_rewards a WHERE a.reverses_id = w.id) rv ON true
	ORDER BY %s`

func scanRewardRow(row pgx.Row) (partnerRewardRow, error) {
	var v partnerRewardRow
	var saleID, saleType, saleExternal, saleCurrency *string
	var saleAmount *int64
	var saleAt *time.Time
	var kept []byte
	var ruleID, ruleName, ruleTrigger, ruleKind, ruleRecurrence *string
	var rulePercent, ruleMonths *int
	var ruleAmount *int64
	w := &v.partnerRewardView
	err := row.Scan(&w.ID, &w.ProgramID, &w.PartnerID, &w.Side, &w.RecipientID, &w.Kind, &w.ConversionID, &w.RuleID, &w.ReversesID,
		&w.AmountCents, &w.Currency, &w.Action, &w.ActionData, &w.Status, &w.HoldUntil, &w.PayoutID, &w.Note, &w.ApprovedAt, &w.CreatedAt,
		&v.PartnerCode, &v.PartnerName, &v.CustomerRecordID, &v.CustomerName, &v.CustomerEmail,
		&saleID, &saleType, &saleExternal, &saleAmount, &saleCurrency, &saleAt,
		&kept, &ruleID, &ruleName, &ruleTrigger, &ruleKind, &rulePercent, &ruleAmount, &ruleRecurrence, &ruleMonths,
		&v.ImportedFrom, &v.HeldAtSource, &v.ReversedBy, &v.NetCents, &v.PayoutStatus)
	if err != nil {
		return v, err
	}
	v.Source = "native"
	if v.ImportedFrom != "" {
		v.Source = "imported"
	}
	if saleID != nil {
		v.Sale = &rewardSale{ConversionID: *saleID, Type: *saleType, ExternalID: *saleExternal, AmountCents: *saleAmount, Currency: *saleCurrency, OccurredAt: *saleAt}
	}
	switch {
	case kept != nil:
		var r rewardRule
		if err := json.Unmarshal(kept, &r); err != nil {
			return v, err
		}
		v.Rule = &rewardRuleView{ID: &r.ID, Name: r.Name, Trigger: r.Trigger, Kind: r.Kind, PercentBP: r.PercentBP, AmountCents: r.AmountCents,
			Recurrence: r.Recurrence, Months: r.Months, Kept: true}
	case ruleID != nil:
		v.Rule = &rewardRuleView{ID: ruleID, Name: *ruleName, Trigger: *ruleTrigger, Kind: *ruleKind, PercentBP: rulePercent, AmountCents: ruleAmount,
			Recurrence: *ruleRecurrence, Months: ruleMonths}
	}
	return v, nil
}

// where and order are on w, m, p, v and imp; $1 is the workspace and args are $2 on.
func rewardRows(ctx context.Context, q partnerQuerier, ws, where, order string, limit int, args ...any) ([]partnerRewardRow, error) {
	sql := fmt.Sprintf(rewardRowSelect, recordEmailSQL("v.record_id"), heldAtSource, where, order, limit, order)
	rows, err := q.Query(ctx, sql, append([]any{ws}, args...)...)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (partnerRewardRow, error) { return scanRewardRow(row) })
	if err != nil {
		return nil, err
	}
	return list, addRewardDecisions(ctx, q, ws, list)
}

// The WHERE repeats audit_events_partner_idx's expression and predicate so the index serves it.
func addRewardDecisions(ctx context.Context, q partnerQuerier, ws string, list []partnerRewardRow) error {
	var ids, partnerIDs []string
	for _, w := range list {
		if w.Status != "pending" {
			ids, partnerIDs = append(ids, w.ID), append(partnerIDs, w.PartnerID)
		}
	}
	type event struct {
		action, by, actorType, actorName, reason string
		at                                       time.Time
	}
	events := map[string]event{}
	if len(ids) > 0 {
		rows, err := q.Query(ctx, `SELECT DISTINCT ON (e.target_id) e.target_id, e.action, COALESCE(e.data->>'by', ''), e.actor_type,
				COALESCE(ac.display_name, ''), COALESCE(e.data->>'reason', ''), e.created_at
			FROM audit_events e LEFT JOIN actors ac ON ac.id = e.actor_id
			WHERE e.workspace_id = $1 AND e.target_type IN ('partner', 'partner_referral', 'partner_reward', 'partner_payout')
			  AND (CASE WHEN e.target_type = 'partner' THEN e.target_id ELSE e.data->>'partner_id' END) = ANY($2::text[])
			  AND e.target_type = 'partner_reward' AND e.target_id = ANY($3::text[])
			  AND e.action IN ('partner_reward.approved', 'partner_reward.rejected')
			ORDER BY e.target_id, e.id DESC`, ws, partnerIDs, ids)
		if err != nil {
			return err
		}
		var id string
		var ev event
		if _, err := pgx.ForEachRow(rows, []any{&id, &ev.action, &ev.by, &ev.actorType, &ev.actorName, &ev.reason, &ev.at}, func() error {
			events[id] = ev
			return nil
		}); err != nil {
			return err
		}
	}
	for i := range list {
		w := &list[i]
		if w.Status == "pending" {
			continue
		}
		status := "approved"
		if w.Status == "rejected" {
			status = "rejected"
		}
		if ev, ok := events[w.ID]; ok && ev.action == "partner_reward."+status {
			w.Decision = &rewardDecision{Status: status, By: ev.by, ActorType: ev.actorType, ActorName: ev.actorName, At: &ev.at, Note: ev.reason, logged: true}
			continue
		}
		// No audit event: imported, or decided before the audit log kept who did.
		d := &rewardDecision{Status: status, At: w.ApprovedAt}
		switch {
		case w.Source == "imported":
			d.By = "import"
		case status == "rejected":
			d.Note = w.Note
		}
		w.Decision = d
	}
	return nil
}

type proofPartner struct {
	ID    string `json:"id"`
	Code  string `json:"code"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type proofClick struct {
	ID           string    `json:"id"`
	At           time.Time `json:"at"`
	SubID        string    `json:"sub_id"`
	ReferrerHost string    `json:"referrer_host"`
	Country      string    `json:"country"`
	ExternalID   string    `json:"external_id"`
}

type proofVisit struct {
	ID           string    `json:"id"`
	At           time.Time `json:"at"`
	LandingURL   string    `json:"landing_url"`
	Ref          string    `json:"ref"`
	ReferrerHost string    `json:"referrer_host"`
	Country      string    `json:"country"`
	UTMSource    string    `json:"utm_source"`
	UTMMedium    string    `json:"utm_medium"`
	UTMCampaign  string    `json:"utm_campaign"`
	Recorded     bool      `json:"recorded"`
}

type proofCredit struct {
	Via       string      `json:"via"`
	At        time.Time   `json:"at"`
	Ref       string      `json:"ref"`
	SubID     string      `json:"sub_id"`
	PromoCode string      `json:"promo_code"`
	Click     *proofClick `json:"click"`
	Visit     *proofVisit `json:"visit"`
}

type proofReferral struct {
	ID          string     `json:"id"`
	Via         string     `json:"via"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	FirstSaleAt *time.Time `json:"first_sale_at"`
}

type proofCustomer struct {
	RecordID   string `json:"record_id"`
	Name       string `json:"name"`
	Email      string `json:"email"`
	CustomerID string `json:"customer_id"`
}

type proofConversion struct {
	ID            string    `json:"id"`
	Type          string    `json:"type"`
	EventName     string    `json:"event_name"`
	AmountCents   int64     `json:"amount_cents"`
	Currency      string    `json:"currency"`
	ExternalID    string    `json:"external_id"`
	TransactionID *string   `json:"transaction_id"`
	PaymentSource string    `json:"payment_source"`
	Source        string    `json:"source"`
	ProductID     string    `json:"product_id"`
	Recurring     bool      `json:"recurring"`
	RefundOf      *string   `json:"refund_of"`
	OccurredAt    time.Time `json:"occurred_at"`
}

type proofPayout struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	AmountCents int64      `json:"amount_cents"`
	Currency    string     `json:"currency"`
	CreatedAt   time.Time  `json:"created_at"`
	PaidAt      *time.Time `json:"paid_at"`
	Method      string     `json:"method"`
	Reference   string     `json:"reference"`
}

type proofImported struct {
	Connector     string `json:"connector"`
	PartnerID     string `json:"partner_id"`
	CustomerID    string `json:"customer_id"`
	CommissionID  string `json:"commission_id"`
	TransactionID string `json:"transaction_id"`
	PayoutID      string `json:"payout_id"`
}

type proofGap struct {
	Step    string `json:"step"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

type partnerRewardProof struct {
	Reward     partnerRewardRow `json:"reward"`
	Partner    proofPartner     `json:"partner"`
	Credit     *proofCredit     `json:"credit"`
	Referral   *proofReferral   `json:"referral"`
	Customer   *proofCustomer   `json:"customer"`
	Conversion *proofConversion `json:"conversion"`
	Rule       *rewardRuleView  `json:"rule"`
	Decision   *rewardDecision  `json:"decision"`
	Payout     *proofPayout     `json:"payout"`
	Imported   *proofImported   `json:"imported"`
	Gaps       []proofGap       `json:"gaps"`
	// visitStored: the referral kept its visit, even if that visit was deleted since.
	visitStored bool
}

type proofFilter struct {
	brand                         string
	rewardID, payoutID, partnerID string
	from, to                      *time.Time
	limit                         int
}

// Three queries whatever the number of rewards: the rewards, who decided them, and the rest of the chain.
func rewardProofs(ctx context.Context, q partnerQuerier, ws string, f proofFilter) ([]partnerRewardProof, error) {
	limit := max(f.limit, 1)
	rows, err := rewardRows(ctx, q, ws, `p.brand_id = $2::uuid AND ($3 = '' OR w.id::text = $3) AND ($4 = '' OR w.payout_id::text = $4)
		AND ($5 = '' OR w.membership_id::text = $5) AND ($6::timestamptz IS NULL OR w.created_at >= $6) AND ($7::timestamptz IS NULL OR w.created_at < $7)`,
		"w.created_at, w.id", limit, f.brand, f.rewardID, f.payoutID, f.partnerID, f.from, f.to)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	ids := make([]string, len(rows))
	for i, w := range rows {
		ids[i] = w.ID
	}
	links, err := q.Query(ctx, `SELECT w.id::text, `+recordEmailSQL("m.record_id")+`,
			f.id::text, f.via, f.status, f.created_at, f.first_sale_at, COALESCE(NULLIF(f.sub_id, ''), k.sub_id, ''), f.promo_code, f.session_id IS NOT NULL,
			COALESCE(k.id::text, kc.click_key), COALESCE(k.created_at, kc.clicked_at), COALESCE(k.sub_id, kc.sub_id), COALESCE(k.referrer_host, kc.referrer_host),
			COALESCE(k.external_id, ''),
			s.id::text, s.started_at, s.landing_url, s.ref, s.referrer_host, s.country, s.utm_source, s.utm_medium, s.utm_campaign, s.recorded,
			COALESCE(uid.value, ''), v.event_name, v.payment_source, v.source, v.product_id, v.recurring, v.refund_of::text, t.id::text,
			o.id::text, o.amount_cents, o.currency, o.created_at, o.paid_at, o.reference, COALESCE(m.payout_type, ''),
			COALESCE(x.partner, ''), COALESCE(x.customer, ''), COALESCE(x.commission, ''), COALESCE(x.payout, '')
		FROM partner_rewards w JOIN partner_memberships m ON m.id = w.membership_id JOIN partner_programs p ON p.id = w.program_id
		LEFT JOIN partner_conversions v ON v.id = w.conversion_id
		LEFT JOIN partner_referrals f ON f.id = v.referral_id
		LEFT JOIN partner_clicks k ON k.id = f.click_id
		LEFT JOIN tracking_link_clicks kc ON kc.id = f.link_click_id
		LEFT JOIN LATERAL (
			SELECT s.id, s.started_at, s.landing_url, s.ref, s.referrer_host, s.country, s.utm_source, s.utm_medium, s.utm_campaign, true AS recorded
			FROM site_sessions s WHERE s.id = f.session_id AND s.workspace_id = w.workspace_id
			UNION ALL
			(SELECT s.id, s.started_at, s.landing_url, s.ref, s.referrer_host, s.country, s.utm_source, s.utm_medium, s.utm_campaign, false
			FROM site_sessions s WHERE f.via = 'visit' AND f.session_id IS NULL AND s.workspace_id = w.workspace_id AND s.record_id = f.record_id
			  AND lower(s.ref) = lower(m.code) AND (s.brand_id IS NULL OR s.brand_id = m.brand_id) AND s.started_at <= f.created_at
			ORDER BY s.started_at DESC LIMIT 1)) s ON true
		LEFT JOIN LATERAL (SELECT i.value FROM record_identities i WHERE i.record_id = v.record_id AND i.namespace = 'user_id' AND i.valid_to IS NULL
			ORDER BY i.verified DESC, i.valid_from LIMIT 1) uid ON true
		LEFT JOIN LATERAL (SELECT t.id FROM transactions t WHERE t.record_id = v.record_id AND t.workspace_id = w.workspace_id AND t.brand_id = w.brand_id AND t.source_id = v.external_id AND t.kind = CASE WHEN v.type = 'refund' THEN 'refund' ELSE 'payment' END ORDER BY t.created_at LIMIT 1) t ON true
		LEFT JOIN partner_payouts o ON o.id = w.payout_id
		LEFT JOIN LATERAL (SELECT max(x.external_id) FILTER (WHERE x.kind = 'partner') AS partner, max(x.external_id) FILTER (WHERE x.kind = 'customer') AS customer,
				max(x.external_id) FILTER (WHERE x.kind = 'commission') AS commission, max(x.external_id) FILTER (WHERE x.kind = 'payout') AS payout
			FROM partner_import_refs x WHERE x.program_id = w.program_id AND (x.kind, x.local_id) IN (('partner', w.membership_id), ('customer', f.id),
				('commission', w.id), ('payout', w.payout_id))) x ON true
		WHERE w.workspace_id = $1 AND w.id = ANY($2::uuid[])`, ws, ids)
	if err != nil {
		return nil, err
	}
	type linkRow struct {
		partnerEmail                                                             string
		referralID, via, referralStatus                                          *string
		referralAt, firstSaleAt                                                  *time.Time
		subID, promo                                                             *string
		visitStored                                                              bool
		clickID                                                                  *string
		clickAt                                                                  *time.Time
		clickSub, clickHost, clickExternal                                       *string
		visitID                                                                  *string
		visitAt                                                                  *time.Time
		landing, visitRef, visitHost, visitCountry, utmSource, utmMedium, utmCmp *string
		visitRecorded                                                            *bool
		userID                                                                   string
		eventName, paymentSource, convSource, productID                          *string
		recurring                                                                *bool
		refundOf, transactionID                                                  *string
		payoutID                                                                 *string
		payoutAmount                                                             *int64
		payoutCurrency                                                           *string
		payoutAt, paidAt                                                         *time.Time
		reference                                                                *string
		method                                                                   string
		srcPartner, srcCustomer, srcCommission, srcPayout                        string
	}
	byID := map[string]linkRow{}
	var id string
	var l linkRow
	if _, err := pgx.ForEachRow(links, []any{&id, &l.partnerEmail,
		&l.referralID, &l.via, &l.referralStatus, &l.referralAt, &l.firstSaleAt, &l.subID, &l.promo, &l.visitStored,
		&l.clickID, &l.clickAt, &l.clickSub, &l.clickHost, &l.clickExternal,
		&l.visitID, &l.visitAt, &l.landing, &l.visitRef, &l.visitHost, &l.visitCountry, &l.utmSource, &l.utmMedium, &l.utmCmp, &l.visitRecorded,
		&l.userID, &l.eventName, &l.paymentSource, &l.convSource, &l.productID, &l.recurring, &l.refundOf, &l.transactionID,
		&l.payoutID, &l.payoutAmount, &l.payoutCurrency, &l.payoutAt, &l.paidAt, &l.reference, &l.method,
		&l.srcPartner, &l.srcCustomer, &l.srcCommission, &l.srcPayout}, func() error {
		byID[id] = l
		return nil
	}); err != nil {
		return nil, err
	}
	out := make([]partnerRewardProof, len(rows))
	for i, w := range rows {
		l := byID[w.ID]
		p := partnerRewardProof{Reward: w, Rule: w.Rule, Decision: w.Decision, visitStored: l.visitStored,
			Partner: proofPartner{ID: w.PartnerID, Code: w.PartnerCode, Name: w.PartnerName, Email: l.partnerEmail}}
		if l.referralID != nil {
			p.Referral = &proofReferral{ID: *l.referralID, Via: *l.via, Status: *l.referralStatus, CreatedAt: *l.referralAt, FirstSaleAt: l.firstSaleAt}
			c := &proofCredit{Via: *l.via, At: *l.referralAt, SubID: *l.subID, PromoCode: *l.promo}
			if l.clickID != nil {
				c.Click = &proofClick{ID: *l.clickID, At: *l.clickAt, SubID: *l.clickSub, ReferrerHost: *l.clickHost, ExternalID: *l.clickExternal}
			}
			if l.visitID != nil {
				c.Visit = &proofVisit{ID: *l.visitID, At: *l.visitAt, LandingURL: *l.landing, Ref: *l.visitRef, ReferrerHost: *l.visitHost,
					Country: *l.visitCountry, UTMSource: *l.utmSource, UTMMedium: *l.utmMedium, UTMCampaign: *l.utmCmp, Recorded: *l.visitRecorded}
			}
			switch {
			case c.Via == "ref":
				c.Ref = w.PartnerCode
			case c.Visit != nil:
				c.Ref = c.Visit.Ref
			}
			p.Credit = c
		}
		if w.CustomerRecordID != nil {
			p.Customer = &proofCustomer{RecordID: *w.CustomerRecordID, Name: w.CustomerName, Email: w.CustomerEmail, CustomerID: l.userID}
		}
		if s := w.Sale; s != nil {
			p.Conversion = &proofConversion{ID: s.ConversionID, Type: s.Type, EventName: *l.eventName, AmountCents: s.AmountCents, Currency: s.Currency,
				ExternalID: s.ExternalID, TransactionID: l.transactionID, PaymentSource: *l.paymentSource, Source: *l.convSource, ProductID: *l.productID,
				Recurring: *l.recurring, RefundOf: l.refundOf, OccurredAt: s.OccurredAt}
		}
		if l.payoutID != nil {
			p.Payout = &proofPayout{ID: *l.payoutID, Status: *w.PayoutStatus, AmountCents: *l.payoutAmount, Currency: *l.payoutCurrency, CreatedAt: *l.payoutAt,
				PaidAt: l.paidAt, Method: l.method, Reference: *l.reference}
		}
		if w.Source == "imported" {
			p.Imported = &proofImported{Connector: w.ImportedFrom, PartnerID: l.srcPartner, CustomerID: l.srcCustomer, CommissionID: l.srcCommission,
				PayoutID: l.srcPayout}
			if s := w.Sale; s != nil && p.Conversion.Source == "import" {
				p.Imported.TransactionID = strings.TrimPrefix(s.ExternalID, w.ImportedFrom+":")
			}
		}
		p.Gaps = proofGaps(&p)
		out[i] = p
	}
	return out, nil
}

// A step's rules run in order; the first that holds is the step's one gap.
type gapRule struct {
	reason  string
	holds   func(p *partnerRewardProof) bool
	message string
}

func (p *partnerRewardProof) imported() bool { return p.Reward.Source == "imported" }

func (p *partnerRewardProof) byHand() bool {
	return p.Reward.Kind == "fee" || p.Reward.Kind == "adjustment" && p.Reward.ReversesID == nil
}

func (p *partnerRewardProof) via(v string) bool { return p.Credit != nil && p.Credit.Via == v }

// noSale says why a reward's sale, and so its customer, referral and credit, has no record.
func noSale(missing func(p *partnerRewardProof) bool) []gapRule {
	return []gapRule{
		{"imported", func(p *partnerRewardProof) bool { return missing(p) && p.imported() },
			"Imported without the sale it came from, so there is none on record."},
		{"no_sale", func(p *partnerRewardProof) bool { return missing(p) && p.byHand() },
			"A fee or an adjustment made by hand: no sale earned it, so no customer was credited."},
		{"customer_deleted", missing,
			"The customer was deleted, and their referral and sales with them."},
	}
}

var proofGapSteps = []struct {
	step  string
	rules []gapRule
}{
	{"credit", append(noSale(func(p *partnerRewardProof) bool { return p.Credit == nil }),
		gapRule{"imported", func(p *partnerRewardProof) bool { return p.via("import") },
			"Credited on the platform the program was imported from, which kept no click or visit."},
		gapRule{"credited_manually", func(p *partnerRewardProof) bool { return p.via("manual") },
			"The partner submitted the customer as a lead, so there is no click or visit."},
		gapRule{"credited_by_ref", func(p *partnerRewardProof) bool { return p.via("ref") },
			"Your backend sent the partner's ref code with the customer, so there is no click or visit on record."},
		gapRule{"credited_by_promo_code", func(p *partnerRewardProof) bool { return p.via("promo_code") },
			"Credited by a promo code, so there is no click or visit."},
		gapRule{"no_click", func(p *partnerRewardProof) bool { return p.via("click") && p.Credit.Click == nil },
			"The click that credited the partner is no longer on record."},
		gapRule{"visit_deleted", func(p *partnerRewardProof) bool { return p.via("visit") && p.Credit.Visit == nil && p.visitStored },
			"The website visit that credited the partner has since been deleted."},
		gapRule{"visit_not_kept", func(p *partnerRewardProof) bool {
			return p.via("visit") && p.Credit.Visit != nil && !p.Credit.Visit.Recorded
		},
			"Outflare didn't keep the visit when it credited the partner; this is the customer's latest visit with the partner's ref before then."},
		gapRule{"no_visit", func(p *partnerRewardProof) bool { return p.via("visit") && p.Credit.Visit == nil },
			"No website visit by the customer with the partner's ref is on record."},
	)},
	{"referral", noSale(func(p *partnerRewardProof) bool { return p.Referral == nil })},
	{"customer", noSale(func(p *partnerRewardProof) bool { return p.Customer == nil })},
	{"conversion", noSale(func(p *partnerRewardProof) bool { return p.Conversion == nil })},
	{"rule", []gapRule{
		{"imported", func(p *partnerRewardProof) bool { return p.Rule == nil && p.imported() },
			"Imported from another platform, which priced it."},
		{"no_rule", func(p *partnerRewardProof) bool { return p.Rule == nil && (p.byHand() || p.Reward.ReversesID != nil) },
			"A fee, an adjustment or a reversal: no rule priced it."},
		{"rule_replaced", func(p *partnerRewardProof) bool { return p.Rule == nil },
			"The program's rules were saved again before Outflare kept a copy of the rule that priced it."},
	}},
	{"decision", []gapRule{
		{"pending", func(p *partnerRewardProof) bool { return p.Decision == nil },
			"Not approved or rejected yet."},
		{"decided_before_history", func(p *partnerRewardProof) bool { return !p.Decision.logged && p.Decision.By != "import" },
			"Decided before Outflare kept who approves and rejects rewards."},
	}},
	{"payout", []gapRule{
		{"not_in_payout", func(p *partnerRewardProof) bool {
			return p.Payout == nil && (p.Reward.Kind == "action" || p.Reward.Side == "referred")
		}, "Actions and rewards for the customer are not paid through payouts."},
		{"pending", func(p *partnerRewardProof) bool { return p.Payout == nil && p.Reward.Status == "pending" },
			"Not approved yet, so not in a payout."},
		{"not_in_payout", func(p *partnerRewardProof) bool { return p.Payout == nil && p.Reward.Status == "rejected" },
			"Rejected, so it is never paid."},
		{"imported", func(p *partnerRewardProof) bool { return p.Payout == nil && p.Reward.HeldAtSource },
			"The platform the program was imported from pays it until the program switches over."},
		{"not_paid_yet", func(p *partnerRewardProof) bool { return p.Payout == nil && p.Reward.Status == "approved" },
			"Approved; the next payout gathers it."},
		{"not_in_payout", func(p *partnerRewardProof) bool { return p.Payout == nil },
			"Marked paid, but the payout it was in is no longer on record."},
	}},
}

func proofGaps(p *partnerRewardProof) []proofGap {
	gaps := []proofGap{}
	for _, s := range proofGapSteps {
		for _, g := range s.rules {
			if g.holds(p) {
				gaps = append(gaps, proofGap{Step: s.step, Reason: g.reason, Message: g.message})
				break
			}
		}
	}
	return gaps
}

func (s *Server) getPartnerRewardProof(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "reward not found")
		return
	}
	list, err := rewardProofs(r.Context(), s.pool, a.WorkspaceID, proofFilter{brand: a.Brand, rewardID: id})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if len(list) == 0 {
		writeError(w, r, http.StatusNotFound, "not_found", "reward not found")
		return
	}
	writeJSON(w, http.StatusOK, list[0])
}
