package api

import (
	"encoding/csv"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const maxProofExport = 10000

// text cells hold what people or other systems typed, so they get the spreadsheet formula guard.
type proofColumn struct {
	header string
	text   bool
	value  func(p *partnerRewardProof) string
}

func csvTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func csvTimeP(t *time.Time) string {
	if t == nil {
		return ""
	}
	return csvTime(*t)
}

func csvStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func csvRate(r *rewardRuleView) string {
	switch {
	case r == nil:
		return ""
	case r.PercentBP != nil:
		return strconv.FormatFloat(float64(*r.PercentBP)/100, 'f', -1, 64) + "%"
	case r.AmountCents != nil:
		return decimal(*r.AmountCents)
	}
	return ""
}

func cell[T any](link *T, f func(*T) string) string {
	if link == nil {
		return ""
	}
	return f(link)
}

func (p *partnerRewardProof) click() *proofClick {
	if p.Credit == nil {
		return nil
	}
	return p.Credit.Click
}

func (p *partnerRewardProof) visit() *proofVisit {
	if p.Credit == nil {
		return nil
	}
	return p.Credit.Visit
}

var proofColumns = []proofColumn{
	{"reward_id", false, func(p *partnerRewardProof) string { return p.Reward.ID }},
	{"reward_kind", false, func(p *partnerRewardProof) string { return p.Reward.Kind }},
	{"reward_status", false, func(p *partnerRewardProof) string { return p.Reward.Status }},
	{"amount", false, func(p *partnerRewardProof) string { return decimal(p.Reward.AmountCents) }},
	{"net_amount", false, func(p *partnerRewardProof) string { return decimal(p.Reward.NetCents) }},
	{"currency", false, func(p *partnerRewardProof) string { return p.Reward.Currency }},
	{"made_at", false, func(p *partnerRewardProof) string { return csvTime(p.Reward.CreatedAt) }},
	{"reverses_id", false, func(p *partnerRewardProof) string { return csvStr(p.Reward.ReversesID) }},
	{"partner_id", false, func(p *partnerRewardProof) string { return p.Partner.ID }},
	{"partner_code", true, func(p *partnerRewardProof) string { return p.Partner.Code }},
	{"partner_name", true, func(p *partnerRewardProof) string { return p.Partner.Name }},
	{"credited_via", false, func(p *partnerRewardProof) string {
		return cell(p.Credit, func(c *proofCredit) string { return c.Via })
	}},
	{"credited_at", false, func(p *partnerRewardProof) string {
		return cell(p.Credit, func(c *proofCredit) string { return csvTime(c.At) })
	}},
	{"ref", true, func(p *partnerRewardProof) string {
		return cell(p.Credit, func(c *proofCredit) string { return c.Ref })
	}},
	{"sub_id", true, func(p *partnerRewardProof) string {
		return cell(p.Credit, func(c *proofCredit) string { return c.SubID })
	}},
	{"promo_code", true, func(p *partnerRewardProof) string {
		return cell(p.Credit, func(c *proofCredit) string { return c.PromoCode })
	}},
	{"click_at", false, func(p *partnerRewardProof) string {
		return cell(p.click(), func(c *proofClick) string { return csvTime(c.At) })
	}},
	{"click_sub_id", true, func(p *partnerRewardProof) string {
		return cell(p.click(), func(c *proofClick) string { return c.SubID })
	}},
	{"click_country", false, func(p *partnerRewardProof) string {
		return cell(p.click(), func(c *proofClick) string { return c.Country })
	}},
	{"visit_at", false, func(p *partnerRewardProof) string {
		return cell(p.visit(), func(v *proofVisit) string { return csvTime(v.At) })
	}},
	{"visit_landing_url", true, func(p *partnerRewardProof) string {
		return cell(p.visit(), func(v *proofVisit) string { return v.LandingURL })
	}},
	{"visit_country", false, func(p *partnerRewardProof) string {
		return cell(p.visit(), func(v *proofVisit) string { return v.Country })
	}},
	{"visit_recorded", false, func(p *partnerRewardProof) string {
		return cell(p.visit(), func(v *proofVisit) string { return strconv.FormatBool(v.Recorded) })
	}},
	{"customer_record_id", false, func(p *partnerRewardProof) string {
		return cell(p.Customer, func(c *proofCustomer) string { return c.RecordID })
	}},
	{"customer_name", true, func(p *partnerRewardProof) string {
		return cell(p.Customer, func(c *proofCustomer) string { return c.Name })
	}},
	{"customer_email", true, func(p *partnerRewardProof) string {
		return cell(p.Customer, func(c *proofCustomer) string { return c.Email })
	}},
	{"customer_id", true, func(p *partnerRewardProof) string {
		return cell(p.Customer, func(c *proofCustomer) string { return c.CustomerID })
	}},
	{"conversion_id", false, func(p *partnerRewardProof) string {
		return cell(p.Conversion, func(c *proofConversion) string { return c.ID })
	}},
	{"conversion_type", false, func(p *partnerRewardProof) string {
		return cell(p.Conversion, func(c *proofConversion) string { return c.Type })
	}},
	{"sale_amount", false, func(p *partnerRewardProof) string {
		return cell(p.Conversion, func(c *proofConversion) string { return decimal(c.AmountCents) })
	}},
	{"sale_currency", false, func(p *partnerRewardProof) string {
		return cell(p.Conversion, func(c *proofConversion) string { return c.Currency })
	}},
	{"external_id", true, func(p *partnerRewardProof) string {
		return cell(p.Conversion, func(c *proofConversion) string { return c.ExternalID })
	}},
	{"transaction_id", false, func(p *partnerRewardProof) string {
		return cell(p.Conversion, func(c *proofConversion) string { return csvStr(c.TransactionID) })
	}},
	{"payment_source", true, func(p *partnerRewardProof) string {
		return cell(p.Conversion, func(c *proofConversion) string { return c.PaymentSource })
	}},
	{"conversion_source", false, func(p *partnerRewardProof) string {
		return cell(p.Conversion, func(c *proofConversion) string { return c.Source })
	}},
	{"occurred_at", false, func(p *partnerRewardProof) string {
		return cell(p.Conversion, func(c *proofConversion) string { return csvTime(c.OccurredAt) })
	}},
	{"rule_name", true, func(p *partnerRewardProof) string {
		return cell(p.Rule, func(r *rewardRuleView) string { return r.Name })
	}},
	{"rule_rate", false, func(p *partnerRewardProof) string { return csvRate(p.Rule) }},
	{"rule_recurrence", false, func(p *partnerRewardProof) string {
		return cell(p.Rule, func(r *rewardRuleView) string { return r.Recurrence })
	}},
	{"rule_kept", false, func(p *partnerRewardProof) string {
		return cell(p.Rule, func(r *rewardRuleView) string { return strconv.FormatBool(r.Kept) })
	}},
	{"decided_status", false, func(p *partnerRewardProof) string {
		return cell(p.Decision, func(d *rewardDecision) string { return d.Status })
	}},
	{"decided_by", false, func(p *partnerRewardProof) string {
		return cell(p.Decision, func(d *rewardDecision) string { return d.By })
	}},
	{"decided_actor", true, func(p *partnerRewardProof) string {
		return cell(p.Decision, func(d *rewardDecision) string { return d.ActorName })
	}},
	{"decided_at", false, func(p *partnerRewardProof) string {
		return cell(p.Decision, func(d *rewardDecision) string { return csvTimeP(d.At) })
	}},
	{"decided_note", true, func(p *partnerRewardProof) string {
		return cell(p.Decision, func(d *rewardDecision) string { return d.Note })
	}},
	{"payout_id", false, func(p *partnerRewardProof) string { return cell(p.Payout, func(o *proofPayout) string { return o.ID }) }},
	{"payout_status", false, func(p *partnerRewardProof) string {
		return cell(p.Payout, func(o *proofPayout) string { return o.Status })
	}},
	{"payout_paid_at", false, func(p *partnerRewardProof) string {
		return cell(p.Payout, func(o *proofPayout) string { return csvTimeP(o.PaidAt) })
	}},
	{"payout_method", true, func(p *partnerRewardProof) string {
		return cell(p.Payout, func(o *proofPayout) string { return o.Method })
	}},
	{"payout_reference", true, func(p *partnerRewardProof) string {
		return cell(p.Payout, func(o *proofPayout) string { return o.Reference })
	}},
	{"imported_from", true, func(p *partnerRewardProof) string {
		return cell(p.Imported, func(i *proofImported) string { return i.Connector })
	}},
	{"source_partner_id", true, func(p *partnerRewardProof) string {
		return cell(p.Imported, func(i *proofImported) string { return i.PartnerID })
	}},
	{"source_customer_id", true, func(p *partnerRewardProof) string {
		return cell(p.Imported, func(i *proofImported) string { return i.CustomerID })
	}},
	{"source_commission_id", true, func(p *partnerRewardProof) string {
		return cell(p.Imported, func(i *proofImported) string { return i.CommissionID })
	}},
	{"source_transaction_id", true, func(p *partnerRewardProof) string {
		return cell(p.Imported, func(i *proofImported) string { return i.TransactionID })
	}},
	{"source_payout_id", true, func(p *partnerRewardProof) string {
		return cell(p.Imported, func(i *proofImported) string { return i.PayoutID })
	}},
	{"gaps", true, func(p *partnerRewardProof) string {
		out := make([]string, len(p.Gaps))
		for i, g := range p.Gaps {
			out[i] = g.Step + ": " + g.Message
		}
		return strings.Join(out, "; ")
	}},
}

func (s *Server) exportPartnerProofs(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	payoutID, partnerID, format := q.Get("payout_id"), q.Get("partner_id"), q.Get("format")
	if format == "" {
		format = "csv"
	}
	bad := func(msg string) { writeError(w, r, http.StatusBadRequest, "invalid_request", msg) }
	var from, to *time.Time
	for _, t := range []struct {
		name string
		at   **time.Time
	}{{"from", &from}, {"to", &to}} {
		if v := q.Get(t.name); v != "" {
			at, err := time.Parse(time.RFC3339, v)
			if err != nil {
				bad(t.name + " is a time such as 2026-01-31T00:00:00Z")
				return
			}
			*t.at = &at
		}
	}
	switch {
	case format != "csv" && format != "json":
		bad("format is csv or json")
		return
	case (payoutID == "") == (partnerID == ""):
		bad("say whose proof: payout_id, or partner_id with from and to")
		return
	case (payoutID != "" && !validUUID(payoutID)) || (partnerID != "" && !validUUID(partnerID)):
		bad("payout_id and partner_id are ids")
		return
	case payoutID != "" && (from != nil || to != nil):
		bad("from and to go with partner_id; a payout's proof is all its rewards")
		return
	case from != nil && to != nil && !from.Before(*to):
		bad("from is before to")
		return
	}
	ctx := r.Context()
	kind, owner := "payout", `SELECT 1 FROM partner_payouts o JOIN partner_memberships m ON m.id = o.membership_id JOIN partner_programs p ON p.id = m.program_id
		WHERE o.workspace_id = $1 AND o.id::text = $2 AND p.brand_id = $3::uuid`
	id := payoutID
	if partnerID != "" {
		kind, owner, id = "partner", `SELECT 1 FROM partner_memberships m JOIN partner_programs p ON p.id = m.program_id
			WHERE m.workspace_id = $1 AND m.id::text = $2 AND p.brand_id = $3::uuid`, partnerID
	}
	var one int
	if err := s.pool.QueryRow(ctx, owner, a.WorkspaceID, id, a.Brand).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", kind+" not found")
		return
	} else if err != nil {
		s.internal(w, r, err)
		return
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM partner_rewards w WHERE w.workspace_id = $1
		AND ($2 = '' OR w.payout_id::text = $2) AND ($3 = '' OR w.membership_id::text = $3)
		AND ($4::timestamptz IS NULL OR w.created_at >= $4) AND ($5::timestamptz IS NULL OR w.created_at < $5) LIMIT $6) x`,
		a.WorkspaceID, payoutID, partnerID, from, to, maxProofExport+1).Scan(&n); err != nil {
		s.internal(w, r, err)
		return
	}
	if n > maxProofExport {
		bad("the proof holds at most 10,000 rewards; pick a shorter time range with from and to")
		return
	}
	proofs, err := rewardProofs(ctx, s.pool, a.WorkspaceID, proofFilter{brand: a.Brand, payoutID: payoutID, partnerID: partnerID, from: from, to: to,
		limit: maxProofExport})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	data := map[string]any{"filter": kind, "format": format, "rows": len(proofs)}
	if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "partner_proofs.exported", "workspace", a.WorkspaceID,
			map[string]any{"filter": kind, "format": format, "rows": len(proofs), kind + "_id": id}, data)
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	if proofs == nil {
		proofs = []partnerRewardProof{}
	}
	if format == "json" {
		writeJSON(w, http.StatusOK, map[string]any{"data": proofs})
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="outflare-reward-proofs-`+kind+`-`+id+`.csv"`)
	cw := csv.NewWriter(w)
	row := make([]string, len(proofColumns))
	for i, c := range proofColumns {
		row[i] = c.header
	}
	cw.Write(row) //nolint:errcheck
	for i := range proofs {
		for j, c := range proofColumns {
			v := c.value(&proofs[i])
			if c.text {
				v = csvSafe(v)
			}
			row[j] = v
		}
		cw.Write(row) //nolint:errcheck
	}
	cw.Flush()
}
