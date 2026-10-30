package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The reward ledger. An entry's amount never changes: a refund or a
// correction is a new entry (kind adjustment) that names the one it
// corrects. Entries are held until hold_until, then approved (on their own
// when the program says so, else by a person), then paid through a payout
// (cash, fees, adjustments) or fulfilled by the workspace's own system
// (actions, which the partner_reward.approved webhook delivers).
//
// Approved cash and fees for partners are a marketing cost, so each one is
// also a spend entry (channel affiliates) and the CMO view sees it.

type rewardEntry struct {
	program, membership, side, recipient, kind string
	conversion, rule, reverses                 *string
	amount                                     int64
	currency, action, note                     string
	actionData                                 map[string]any
	hold                                       time.Time
	// autoApprove is the flow's approval; nil follows the program.
	autoApprove *bool
}

func insertPartnerReward(ctx context.Context, tx pgx.Tx, ws string, e rewardEntry) (string, error) {
	if e.actionData == nil {
		e.actionData = map[string]any{}
	}
	data, err := json.Marshal(e.actionData)
	if err != nil {
		return "", err
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO partner_rewards (workspace_id, program_id, membership_id, side, recipient_id, kind, conversion_id, rule_id,
		reverses_id, amount_cents, currency, action, action_data, hold_until, note, auto_approve)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, GREATEST(now(), $14), $15, $16) RETURNING id::text`,
		ws, e.program, e.membership, e.side, e.recipient, e.kind, e.conversion, e.rule, e.reverses, e.amount, e.currency, e.action,
		string(data), e.hold, e.note, e.autoApprove).Scan(&id)
	if err == nil {
		metrics.PartnerRewards(e.kind, "created", 1)
	}
	return id, err
}

func loadReward(ctx context.Context, q pgx.Tx, ws, id string) (partnerRewardView, error) {
	if !validUUID(id) {
		return partnerRewardView{}, errNotFound
	}
	v, err := scanPartnerReward(q.QueryRow(ctx, partnerRewardSelect+` WHERE w.workspace_id = $1 AND w.id = $2 FOR UPDATE`, ws, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return v, errNotFound
	}
	return v, err
}

// rewardAuditData is a reward's webhook data.
func rewardAuditData(ctx context.Context, tx pgx.Tx, w partnerRewardView) (map[string]any, error) {
	var kind string
	if err := tx.QueryRow(ctx, `SELECT kind FROM partner_programs WHERE id = $1`, w.ProgramID).Scan(&kind); err != nil {
		return nil, err
	}
	d := map[string]any{"program_id": w.ProgramID, "program_kind": kind, "partner_id": w.PartnerID, "side": w.Side, "kind": w.Kind,
		"recipient_id": w.RecipientID, "amount_cents": w.AmountCents, "currency": w.Currency, "status": w.Status, "hold_until": w.HoldUntil}
	if w.ReversesID != nil {
		d["reverses_id"] = *w.ReversesID
	}
	if w.ConversionID != nil {
		if err := addConversionData(ctx, tx, d, *w.ConversionID); err != nil {
			return nil, err
		}
	}
	if w.Action != "" {
		d["action"], d["action_data"] = w.Action, w.ActionData
		// The recipient's own user id, so the workspace's system knows whom to grant.
		uid, err := recordUserID(ctx, tx, w.RecipientID)
		if err != nil {
			return nil, err
		}
		d["recipient_customer_id"] = uid
	}
	return d, nil
}

// addConversionData adds to a reward's webhook data the conversion that
// earned it, so a system that handled another platform's commission
// webhooks finds the same facts on the reward: the sale's amount
// (revenue_cents), the id the backend sent with it (external_id, such as
// a charge id), and the referred customer (record_id, and customer_id,
// the backend's own user id for them, "" when it never sent one).
func addConversionData(ctx context.Context, tx pgx.Tx, d map[string]any, conv string) error {
	var typ, external, record string
	var amount int64
	if err := tx.QueryRow(ctx, `SELECT type, external_id, amount_cents, record_id::text FROM partner_conversions WHERE id = $1`, conv).
		Scan(&typ, &external, &amount, &record); err != nil {
		return err
	}
	uid, err := recordUserID(ctx, tx, record)
	if err != nil {
		return err
	}
	d["conversion_id"], d["conversion_type"], d["external_id"], d["revenue_cents"], d["record_id"], d["customer_id"] = conv, typ, external, amount, record, uid
	return nil
}

// recordUserID is the backend's own user id for a customer record, or "".
func recordUserID(ctx context.Context, tx pgx.Tx, record string) (string, error) {
	var uid string
	err := tx.QueryRow(ctx, `SELECT value FROM record_identities WHERE record_id = $1 AND namespace = 'user_id' AND valid_to IS NULL
		ORDER BY verified DESC, valid_from LIMIT 1`, record).Scan(&uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return uid, err
}

// approvePartnerReward approves a pending entry (by is auto, refund or
// manual) and logs cash for the partner as a cost.
func approvePartnerReward(ctx context.Context, tx pgx.Tx, p *principal, ws, id, by string) error {
	w, err := loadReward(ctx, tx, ws, id)
	if err != nil {
		return err
	}
	if w.Status != "pending" {
		return errInvalid("only a pending reward can be approved; this one is " + w.Status)
	}
	if _, err := tx.Exec(ctx, `UPDATE partner_rewards SET status = 'approved', approved_at = now(), updated_at = now() WHERE id = $1`, id); err != nil {
		return err
	}
	w.Status = "approved"
	if err := logRewardCost(ctx, tx, ws, w); err != nil {
		return err
	}
	d, err := rewardAuditData(ctx, tx, w)
	if err != nil {
		return err
	}
	d["by"] = by
	metrics.PartnerRewards(w.Kind, "approved", 1)
	return partnerRecord(ctx, tx, p, ws, "partner_reward.approved", "partner_reward", id, d)
}

// logRewardCost keeps the cost ledger in step with an approved entry:
// cash and fees add a spend entry, less any refund adjustments already
// approved against them; a negative adjustment lowers the entry it
// corrects (never below zero). Either may be approved first.
func logRewardCost(ctx context.Context, tx pgx.Tx, ws string, w partnerRewardView) error {
	if w.Side != "partner" || w.Kind == "action" || w.AmountCents == 0 {
		return nil
	}
	if w.AmountCents < 0 {
		if w.ReversesID == nil {
			return nil
		}
		_, err := tx.Exec(ctx, `UPDATE spend_entries SET amount_cents = GREATEST(0, amount_cents + $3) WHERE workspace_id = $1 AND ref = $2`,
			ws, "partner_reward:"+*w.ReversesID, w.AmountCents)
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO spend_entries (workspace_id, category, provider, amount_cents, currency, occurred_at, source, ref, note, channel, brand_id)
		SELECT $1, 'people', 'partners', GREATEST(0, $2 + COALESCE((SELECT sum(a.amount_cents) FROM partner_rewards a
			WHERE a.reverses_id = $7::uuid AND a.status IN ('approved', 'paid')), 0)),
			$3, now(), 'auto', $4, 'Partner ' || $5 || ' (' || p.name || ')', 'affiliates', p.brand_id
		FROM partner_programs p WHERE p.id = $6`,
		ws, w.AmountCents, w.Currency, "partner_reward:"+w.ID, map[string]string{"cash": "reward", "fee": "fee", "adjustment": "adjustment"}[w.Kind], w.ProgramID, w.ID)
	return err
}

// rejectReward rejects a pending entry, with the reason.
func rejectReward(ctx context.Context, tx pgx.Tx, p *principal, ws string, w partnerRewardView, note string) error {
	if w.Status != "pending" {
		return errInvalid("only a pending reward can be rejected; reverse an approved one with an adjustment")
	}
	if _, err := tx.Exec(ctx, `UPDATE partner_rewards SET status = 'rejected', note = $2, updated_at = now() WHERE id = $1`, w.ID, cutUTF8(note, 2000)); err != nil {
		return err
	}
	w.Status = "rejected"
	d, err := rewardAuditData(ctx, tx, w)
	if err != nil {
		return err
	}
	d["reason"] = note
	metrics.PartnerRewards(w.Kind, "rejected", 1)
	return partnerRecord(ctx, tx, p, ws, "partner_reward.rejected", "partner_reward", w.ID, d)
}

func (s *Server) listPartnerRewards(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	status, kind, partner, payout, program := q.Get("status"), q.Get("kind"), q.Get("partner_id"), q.Get("payout_id"), strings.ToLower(q.Get("program"))
	switch {
	case status != "" && !slices.Contains([]string{"pending", "approved", "rejected", "paid", "fulfilled"}, status):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is pending, approved, rejected, paid or fulfilled")
		return
	case kind != "" && !slices.Contains([]string{"cash", "fee", "action", "adjustment"}, kind):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is cash, fee, action or adjustment")
		return
	case (partner != "" && !validUUID(partner)) || (payout != "" && !validUUID(payout)):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "partner_id and payout_id are ids")
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
	args := []any{a.WorkspaceID, status, kind, partner, payout, program, before, limit + 1}
	rows, err := s.pool.Query(r.Context(), partnerRewardSelect+` JOIN partner_programs p ON p.id = w.program_id
		WHERE w.workspace_id = $1 AND ($2 = '' OR w.status = $2) AND ($3 = '' OR w.kind = $3) AND ($4 = '' OR w.membership_id::text = $4)
		AND ($5 = '' OR w.payout_id::text = $5) AND ($6 = '' OR p.id::text = $6 OR p.slug = $6) AND ($7::timestamptz IS NULL OR w.created_at < $7)`+sel.sql("p.brand_id", &args)+`
		ORDER BY w.created_at DESC LIMIT $8`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (partnerRewardView, error) { return scanPartnerReward(row) })
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
		list = []partnerRewardView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

// rewardChange runs change on one reward in a transaction and answers it.
func (s *Server) rewardChange(w http.ResponseWriter, r *http.Request, change func(ctx context.Context, tx pgx.Tx, a *access, v partnerRewardView) error) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	id := r.PathValue("id")
	var out partnerRewardView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		v, err := loadReward(ctx, tx, a.WorkspaceID, id)
		if err != nil {
			return err
		}
		brand, err := partnerProgramBrand(ctx, tx, a.WorkspaceID, v.ProgramID)
		if err != nil {
			return err
		}
		if err := partnerBrandAccess(r, a, brand); err != nil {
			return err
		}
		if err := change(ctx, tx, a, v); err != nil {
			return err
		}
		out, err = loadReward(ctx, tx, a.WorkspaceID, id)
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// approvePartnerReward approves a reward now, even before its hold ends.
func (s *Server) approvePartnerReward(w http.ResponseWriter, r *http.Request) {
	if !decode(w, r, &struct{}{}) {
		return
	}
	s.rewardChange(w, r, func(ctx context.Context, tx pgx.Tx, a *access, v partnerRewardView) error {
		return approvePartnerReward(ctx, tx, &a.principal, a.WorkspaceID, v.ID, "manual")
	})
}

func (s *Server) rejectPartnerReward(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Note string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	s.rewardChange(w, r, func(ctx context.Context, tx pgx.Tx, a *access, v partnerRewardView) error {
		return rejectReward(ctx, tx, &a.principal, a.WorkspaceID, v, strings.TrimSpace(in.Note))
	})
}

// fulfilPartnerReward is the workspace's system saying it carried out an
// approved action (it granted the credit).
func (s *Server) fulfilPartnerReward(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Note string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	s.rewardChange(w, r, func(ctx context.Context, tx pgx.Tx, a *access, v partnerRewardView) error {
		if v.Action == "" || v.Status != "approved" {
			return errInvalid("only an approved action reward can be fulfilled; this one is a " + v.Kind + " reward, " + v.Status)
		}
		if _, err := tx.Exec(ctx, `UPDATE partner_rewards SET status = 'fulfilled', note = CASE WHEN $2 = '' THEN note ELSE $2 END, updated_at = now() WHERE id = $1`,
			v.ID, cutUTF8(strings.TrimSpace(in.Note), 2000)); err != nil {
			return err
		}
		v.Status = "fulfilled"
		d, err := rewardAuditData(ctx, tx, v)
		if err != nil {
			return err
		}
		metrics.PartnerRewards(v.Kind, "fulfilled", 1)
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner_reward.fulfilled", "partner_reward", v.ID, d)
	})
}

type ledgerInput struct {
	AmountCents *int64 `json:"amount_cents"`
	Note        string `json:"note"`
}

// addPartnerFee logs a fixed fee (an influencer's fee for a post or a
// month), approved at once and counted as a cost.
func (s *Server) addPartnerFee(w http.ResponseWriter, r *http.Request) {
	s.addLedgerEntry(w, r, "fee")
}

// addPartnerAdjustment adds a correction: positive adds to what the
// partner is owed, negative takes from it. It nets in the next payout.
func (s *Server) addPartnerAdjustment(w http.ResponseWriter, r *http.Request) {
	s.addLedgerEntry(w, r, "adjustment")
}

func (s *Server) addLedgerEntry(w http.ResponseWriter, r *http.Request, kind string) {
	a := accessFrom(r.Context())
	cur, ok := s.findPartner(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var in ledgerInput
	if !decode(w, r, &in) {
		return
	}
	note := strings.TrimSpace(in.Note)
	switch {
	case in.AmountCents == nil || *in.AmountCents == 0:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "amount_cents is required, in the program's currency's smallest unit")
		return
	case kind == "fee" && *in.AmountCents < 0:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a fee is positive; take money back with an adjustment")
		return
	case *in.AmountCents > 1e11 || *in.AmountCents < -1e11:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "amount_cents is too large")
		return
	case kind == "adjustment" && note == "":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "note is required: say why, for the partner and for whoever reviews it")
		return
	case len(note) > 2000:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "note is at most 2,000 characters")
		return
	}
	ctx := r.Context()
	var out partnerRewardView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		id, err := insertPartnerReward(ctx, tx, a.WorkspaceID, rewardEntry{program: cur.ProgramID, membership: cur.ID, side: "partner",
			recipient: cur.RecordID, kind: kind, amount: *in.AmountCents, currency: cur.Currency, note: note, hold: time.Now()})
		if err != nil {
			return err
		}
		v, err := loadReward(ctx, tx, a.WorkspaceID, id)
		if err != nil {
			return err
		}
		d, err := rewardAuditData(ctx, tx, v)
		if err != nil {
			return err
		}
		if err := partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner_reward.created", "partner_reward", id, d); err != nil {
			return err
		}
		if err := approvePartnerReward(ctx, tx, &a.principal, a.WorkspaceID, id, "manual"); err != nil {
			return err
		}
		out, err = loadReward(ctx, tx, a.WorkspaceID, id)
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// ---- Payouts ----

type partnerPayoutView struct {
	ID          string     `json:"id"`
	PartnerID   string     `json:"partner_id"`
	PartnerName string     `json:"partner_name"`
	ProgramID   string     `json:"program_id"`
	AmountCents int64      `json:"amount_cents"`
	Currency    string     `json:"currency"`
	Status      string     `json:"status"`
	Note        string     `json:"note"`
	Reference   string     `json:"reference"`
	Rewards     int        `json:"rewards"`
	PaidAt      *time.Time `json:"paid_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

const partnerPayoutSelect = `SELECT o.id::text, o.membership_id::text, COALESCE(NULLIF(r.name, ''), m.code), m.program_id::text, o.amount_cents, o.currency, o.status, o.note, o.reference,
	(SELECT count(*) FROM partner_rewards w WHERE w.payout_id = o.id)::int, o.paid_at, o.created_at
	FROM partner_payouts o JOIN partner_memberships m ON m.id = o.membership_id JOIN partner_programs p ON p.id = m.program_id
	LEFT JOIN records r ON r.id = m.record_id`

func (s *Server) listPartnerPayouts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	status, partner, program := q.Get("status"), q.Get("partner_id"), strings.ToLower(q.Get("program"))
	if status != "" && !slices.Contains([]string{"due", "paid", "canceled"}, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is due, paid or canceled")
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
	args := []any{a.WorkspaceID, status, partner, before, limit + 1, program}
	rows, err := s.pool.Query(r.Context(), partnerPayoutSelect+` WHERE o.workspace_id = $1 AND ($2 = '' OR o.status = $2)
		AND ($3 = '' OR o.membership_id::text = $3) AND ($4::timestamptz IS NULL OR o.created_at < $4)
		AND ($6 = '' OR p.id::text = $6 OR p.slug = $6)`+sel.sql("p.brand_id", &args)+` ORDER BY o.created_at DESC LIMIT $5`,
		args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[partnerPayoutView])
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
		list = []partnerPayoutView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

// createPartnerPayouts makes a payout for each partner whose approved
// balance is at least their program's minimum: one partner (partner_id),
// one program, or all. Each payout gathers the approved, unpaid cash, fees
// and adjustments; the partner_payout.created webhook says it is due.
// Outflare sends no money: pay from your own account, then mark it paid.
func (s *Server) createPartnerPayouts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		PartnerID string `json:"partner_id"`
		Program   string `json:"program"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.PartnerID != "" && !validUUID(in.PartnerID) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "partner_id is a partner's id")
		return
	}
	ctx := r.Context()
	made := []string{}
	waiting := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT m.id::text, p.id::text, p.currency, p.min_payout_cents, sum(w.amount_cents)::bigint,
			m.source = 'import' AND m.payout_method IS NULL
			FROM partner_rewards w JOIN partner_memberships m ON m.id = w.membership_id JOIN partner_programs p ON p.id = m.program_id
			WHERE w.workspace_id = $1 AND w.status = 'approved' AND w.payout_id IS NULL AND w.side = 'partner' AND w.kind <> 'action'
			  AND NOT `+heldAtSource+` AND ($2 = '' OR m.id::text = $2) AND ($3 = '' OR p.id::text = lower($3) OR p.slug = lower($3))
			  AND ($4::uuid[] IS NULL OR p.brand_id = ANY($4))
			GROUP BY m.id, p.id ORDER BY m.id`, a.WorkspaceID, in.PartnerID, in.Program, a.Brands)
		if err != nil {
			return err
		}
		type due struct {
			membership, program, currency string
			min, total                    int64
			noDetails                     bool
		}
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
			var d due
			return d, row.Scan(&d.membership, &d.program, &d.currency, &d.min, &d.total, &d.noDetails)
		})
		if err != nil {
			return err
		}
		for _, d := range list {
			if d.total <= 0 || d.total < d.min {
				continue
			}
			// An imported partner whose platform didn't give their payout
			// details (Tolt's v2 has none) waits until they add them on the
			// portal; their balance stays and is paid in a later payout.
			if d.noDetails {
				waiting++
				continue
			}
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO partner_payouts (workspace_id, membership_id, amount_cents, currency, created_by)
				VALUES ($1, $2, $3, $4, $5) RETURNING id::text`, a.WorkspaceID, d.membership, d.total, d.currency, nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `UPDATE partner_rewards w SET payout_id = $2, updated_at = now() WHERE w.membership_id = $1 AND w.status = 'approved'
				AND w.payout_id IS NULL AND w.side = 'partner' AND w.kind <> 'action' AND NOT `+heldAtSource, d.membership, id)
			if err != nil {
				return err
			}
			if err := partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner_payout.created", "partner_payout", id, map[string]any{
				"partner_id": d.membership, "program_id": d.program, "amount_cents": d.total, "currency": d.currency,
				"rewards": tag.RowsAffected()}); err != nil {
				return err
			}
			if err := enqueuePartnerNotify(ctx, tx, a.WorkspaceID, partnerNotify{Kind: "payout_due", PartnerID: d.membership, Ref: id}); err != nil {
				return err
			}
			made = append(made, id)
		}
		return nil
	})
	if s.allocError(w, r, err) {
		return
	}
	rows, err := s.pool.Query(ctx, partnerPayoutSelect+` WHERE o.workspace_id = $1 AND o.id = ANY($2::uuid[]) ORDER BY o.created_at`, a.WorkspaceID, made)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[partnerPayoutView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []partnerPayoutView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "waiting_for_details": waiting})
}

func onePayout(ctx context.Context, tx pgx.Tx, sql string, args ...any) (partnerPayoutView, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return partnerPayoutView{}, err
	}
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[partnerPayoutView])
}

// payoutChange runs change on a due payout in a transaction and answers it.
func (s *Server) payoutChange(w http.ResponseWriter, r *http.Request, change func(ctx context.Context, tx pgx.Tx, a *access, v partnerPayoutView) error) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "payout not found")
		return
	}
	var out partnerPayoutView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		v, err := onePayout(ctx, tx, partnerPayoutSelect+` WHERE o.workspace_id = $1 AND o.id = $2 FOR UPDATE OF o`, a.WorkspaceID, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		brand, err := partnerProgramBrand(ctx, tx, a.WorkspaceID, v.ProgramID)
		if err != nil {
			return err
		}
		if err := partnerBrandAccess(r, a, brand); err != nil {
			return err
		}
		if v.Status != "due" {
			return errInvalid("the payout is " + v.Status + "; only a due payout can change")
		}
		if err := change(ctx, tx, a, v); err != nil {
			return err
		}
		out, err = onePayout(ctx, tx, partnerPayoutSelect+` WHERE o.id = $1`, id)
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// markPartnerPayoutPaid records that the workspace paid a payout from its
// own account, with a note and the payment's reference.
func (s *Server) markPartnerPayoutPaid(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Note      string     `json:"note"`
		Reference string     `json:"reference"`
		PaidAt    *time.Time `json:"paid_at"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Note) > 2000 || len(in.Reference) > 200 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "note is at most 2,000 characters and reference 200")
		return
	}
	paid := time.Now()
	if in.PaidAt != nil {
		if in.PaidAt.After(paid.Add(time.Hour)) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "paid_at can't be in the future")
			return
		}
		paid = *in.PaidAt
	}
	s.payoutChange(w, r, func(ctx context.Context, tx pgx.Tx, a *access, v partnerPayoutView) error {
		if _, err := tx.Exec(ctx, `UPDATE partner_payouts SET status = 'paid', note = $2, reference = $3, paid_at = $4, updated_at = now() WHERE id = $1`,
			v.ID, strings.TrimSpace(in.Note), strings.TrimSpace(in.Reference), paid); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `UPDATE partner_rewards SET status = 'paid', updated_at = now() WHERE payout_id = $1 RETURNING id::text, kind`, v.ID)
		if err != nil {
			return err
		}
		type paidReward struct{ ID, Kind string }
		rewards, err := pgx.CollectRows(rows, pgx.RowToStructByPos[paidReward])
		if err != nil {
			return err
		}
		ids := make([]string, len(rewards))
		for i, w := range rewards {
			ids[i] = w.ID
			metrics.PartnerRewards(w.Kind, "paid", 1)
		}
		if err := enqueuePartnerNotify(ctx, tx, a.WorkspaceID, partnerNotify{Kind: "payout_paid", PartnerID: v.PartnerID, Ref: v.ID}); err != nil {
			return err
		}
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner_payout.paid", "partner_payout", v.ID, map[string]any{
			"partner_id": v.PartnerID, "program_id": v.ProgramID, "amount_cents": v.AmountCents, "currency": v.Currency,
			"reference": strings.TrimSpace(in.Reference), "has_note": in.Note != "", "reward_ids": ids})
	})
}

// cancelPartnerPayout puts a due payout's rewards back into the balance.
func (s *Server) cancelPartnerPayout(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Note string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	s.payoutChange(w, r, func(ctx context.Context, tx pgx.Tx, a *access, v partnerPayoutView) error {
		if _, err := tx.Exec(ctx, `UPDATE partner_payouts SET status = 'canceled', note = $2, updated_at = now() WHERE id = $1`,
			v.ID, cutUTF8(strings.TrimSpace(in.Note), 2000)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE partner_rewards SET payout_id = NULL, updated_at = now() WHERE payout_id = $1`, v.ID); err != nil {
			return err
		}
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner_payout.canceled", "partner_payout", v.ID, map[string]any{
			"partner_id": v.PartnerID, "program_id": v.ProgramID, "amount_cents": v.AmountCents, "currency": v.Currency})
	})
}

// ---- Approving rewards when their hold ends ----

// partnerApproveBatch is how many rewards one round approves.
const partnerApproveBatch = 200

// RunPartnerRewards approves held rewards whose hold has ended, in programs
// that approve on their own, until ctx ends.
func (s *Server) RunPartnerRewards(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if err := s.approveDuePartnerRewards(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.log, track.Event{Name: "partner_reward.approve_failed", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// approveDuePartnerRewards approves one batch of due rewards. Each reward
// commits in its own transaction, so one failure doesn't hold the rest.
func (s *Server) approveDuePartnerRewards(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT w.workspace_id::text, w.id::text, w.hold_until FROM partner_rewards w
		JOIN partner_programs p ON p.id = w.program_id JOIN partner_memberships m ON m.id = w.membership_id
		WHERE w.status = 'pending' AND w.hold_until <= now() AND COALESCE(w.auto_approve, p.auto_approve) AND m.status = 'active' AND NOT `+heldAtSource+`
		ORDER BY w.hold_until LIMIT $1`, partnerApproveBatch)
	if err != nil {
		return err
	}
	type due struct {
		ws, id string
		hold   time.Time
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
		var d due
		return d, row.Scan(&d.ws, &d.id, &d.hold)
	})
	if err != nil {
		return err
	}
	if len(list) == 0 {
		metrics.PartnerRewardsOverdue(0)
		return nil
	}
	metrics.PartnerRewardsOverdue(time.Since(list[0].hold))
	var first error
	for _, d := range list {
		err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			err := approvePartnerReward(ctx, tx, nil, d.ws, d.id, "auto")
			if errors.As(err, new(errInvalid)) {
				return nil // approved or rejected by someone else meanwhile
			}
			return err
		})
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}
