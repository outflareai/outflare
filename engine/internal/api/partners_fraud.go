package api

// Partner fraud review (plans/ad-tracker.md, B7d; ideas/fraud-signals.md,
// "Partner fraud"). Every credited lead or sale is checked for partner
// signals (internal/fraud, PartnerFlags) and the shared fraud reasons of
// the tracker click that brought the customer. Each one found is a flag;
// the conversion's rewards wait while a flag on it is open, even past
// their hold, until someone clears it (the rewards go on as usual) or
// confirms it (pending rewards are rejected, and the partner can be
// suspended). Nothing is rejected on its own: a flag only holds.
// Thresholds are the same for every program (the owner's decision 14).

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/fraud"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// openFlagOn is a condition on a partner_rewards row w: its conversion has
// an open fraud flag, so it waits for a person.
const openFlagOn = `EXISTS (SELECT 1 FROM partner_flags g WHERE g.conversion_id = w.conversion_id AND g.status = 'open')`

// fraudChecks runs the partner checks on a conversion just stored, records
// a flag for each signal found, and returns how many.
func (c *conversionRun) fraudChecks(ctx context.Context, m *membershipRow, referral, rec, conv string) (int, error) {
	tx := c.tx
	var f fraud.PartnerFacts

	// The same email, phone or user id on the partner and the customer.
	if m.recordID != rec {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM record_identities a JOIN record_identities b
				ON b.workspace_id = a.workspace_id AND b.namespace = a.namespace AND lower(b.value) = lower(a.value)
			WHERE a.workspace_id = $1 AND a.record_id = $2 AND b.record_id = $3 AND a.namespace IN ('email', 'phone', 'user_id')
			  AND a.valid_to IS NULL AND b.valid_to IS NULL`, c.ws, m.recordID, rec).Scan(&f.SharedIdentities); err != nil {
			return 0, err
		}
	}

	// The partner is paid to an address the customer uses. The sealed
	// payout details are opened in memory only.
	rows, err := tx.Query(ctx, `SELECT lower(value) FROM record_identities WHERE workspace_id = $1 AND record_id = $2 AND namespace = 'email' AND valid_to IS NULL`,
		c.ws, rec)
	if err != nil {
		return 0, err
	}
	emails, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	if len(emails) > 0 && c.s.box != nil {
		var sealed []byte
		if err := tx.QueryRow(ctx, `SELECT payout_method FROM partner_memberships WHERE id = $1`, m.id).Scan(&sealed); err != nil {
			return 0, err
		}
		if len(sealed) > 0 {
			if b, err := c.s.box.Open(sealed, []byte(m.id)); err == nil {
				payout := strings.ToLower(string(b))
				f.PayoutMatches = slices.ContainsFunc(emails, func(e string) bool { return strings.Contains(payout, e) })
			}
		}
	}

	// The click that brought the customer: the partner's own short link, or
	// a tracker click with its shared fraud reasons.
	var clickAt, linkClickAt *time.Time
	var ipHash []byte
	var reasons []string
	err = tx.QueryRow(ctx, `SELECT k.created_at, k.ip_hash, x.clicked_at, COALESCE(x.fraud_reasons, '{}')
		FROM partner_referrals f
		LEFT JOIN partner_clicks k ON k.id = f.click_id
		LEFT JOIN tracking_link_clicks x ON x.id = f.link_click_id
		WHERE f.id = $1`, referral).Scan(&clickAt, &ipHash, &linkClickAt, &reasons)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	f.ClickReasons = reasons
	if clickAt != nil && len(ipHash) > 0 {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM partner_clicks WHERE membership_id = $1 AND ip_hash = $2
			AND created_at BETWEEN $3::timestamptz - interval '1 day' AND $3::timestamptz + interval '1 day'`,
			m.id, ipHash, *clickAt).Scan(&f.ClicksFromAddress); err != nil {
			return 0, err
		}
		if err := tx.QueryRow(ctx, `SELECT count(DISTINCT f.id) FROM partner_referrals f JOIN partner_clicks k ON k.id = f.click_id
			WHERE f.membership_id = $1 AND k.ip_hash = $2 AND f.created_at > now() - interval '30 days'`,
			m.id, ipHash).Scan(&f.ReferralsFromAddress); err != nil {
			return 0, err
		}
	}
	if at := cmpOr(clickAt, linkClickAt); at != nil {
		d := c.in.at.Sub(*at)
		f.SinceClick = &d
	}

	// A partner whose sales are often refunded.
	if c.in.Type == "sale" {
		f.Sale = true
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE v.type = 'sale'), count(*) FILTER (WHERE v.type = 'refund')
			FROM partner_conversions v JOIN partner_referrals f ON f.id = v.referral_id
			WHERE f.membership_id = $1 AND v.occurred_at > now() - interval '90 days' AND v.id <> $2`, m.id, conv).Scan(&f.Sales, &f.Refunds); err != nil {
			return 0, err
		}
	}

	n := 0
	for _, h := range fraud.PartnerFlags(f) {
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO partner_flags (workspace_id, brand_id, program_id, membership_id, conversion_id, signal, detail)
			SELECT $1, p.brand_id, p.id, $3, $4, $5, $6 FROM partner_programs p WHERE p.id = $2 AND p.brand_id IS NOT NULL
			ON CONFLICT (conversion_id, signal) DO NOTHING RETURNING id::text`,
			c.ws, m.programID, m.id, conv, h.Signal, h.Detail).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, err
		}
		n++
		metrics.PartnerFlag(h.Signal, "raised")
		if err := c.audit(ctx, "partner_flag.raised", "partner_flag", id, map[string]any{"program_id": m.programID, "program_kind": m.kind,
			"partner_id": m.id, "conversion_id": conv, "signal": h.Signal, "detail": h.Detail}); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// cmpOr is the first time that isn't nil.
func cmpOr(ts ...*time.Time) *time.Time {
	for _, t := range ts {
		if t != nil {
			return t
		}
	}
	return nil
}

// ---- The review queue ----

type partnerFlagView struct {
	ID           string         `json:"id"`
	ProgramID    string         `json:"program_id"`
	PartnerID    string         `json:"partner_id"`
	PartnerCode  string         `json:"partner_code"`
	ConversionID string         `json:"conversion_id"`
	Type         string         `json:"conversion_type"`
	AmountCents  int64          `json:"amount_cents"`
	Currency     string         `json:"currency"`
	Signal       string         `json:"signal"`
	Detail       map[string]any `json:"detail"`
	Status       string         `json:"status"`
	Note         string         `json:"note"`
	// HeldCents is the partner's pending cash on the conversion, waiting
	// for this review.
	HeldCents  int64      `json:"held_cents"`
	ResolvedAt *time.Time `json:"resolved_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

const partnerFlagSelect = `SELECT f.id::text, f.program_id::text, f.membership_id::text, m.code, f.conversion_id::text, v.type, v.amount_cents, v.currency,
	f.signal, f.detail, f.status, f.note,
	(SELECT coalesce(sum(w.amount_cents), 0)::bigint FROM partner_rewards w WHERE w.conversion_id = f.conversion_id AND w.side = 'partner'
		AND w.status = 'pending' AND w.kind IN ('cash', 'adjustment')),
	f.resolved_at, f.created_at
	FROM partner_flags f JOIN partner_memberships m ON m.id = f.membership_id JOIN partner_conversions v ON v.id = f.conversion_id
	JOIN partner_programs p ON p.id = f.program_id`

func scanPartnerFlag(row pgx.Row) (partnerFlagView, error) {
	var v partnerFlagView
	err := row.Scan(&v.ID, &v.ProgramID, &v.PartnerID, &v.PartnerCode, &v.ConversionID, &v.Type, &v.AmountCents, &v.Currency,
		&v.Signal, &v.Detail, &v.Status, &v.Note, &v.HeldCents, &v.ResolvedAt, &v.CreatedAt)
	return v, err
}

func (s *Server) routesPartnerFlags(mux *http.ServeMux) {
	read, manage := auth.ScopePartnersRead, auth.ScopePartnersManage
	mux.HandleFunc("GET /v1/brands/{brand}/partner-flags", s.onBrand(read, s.listPartnerFlags))
	mux.HandleFunc("POST /v1/brands/{brand}/partner-flags/{id}/clear", s.onBrand(manage, s.clearPartnerFlag))
	mux.HandleFunc("POST /v1/brands/{brand}/partner-flags/{id}/confirm", s.onBrand(manage, s.confirmPartnerFlag))
}

func (s *Server) listPartnerFlags(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	status, partner, program := q.Get("status"), q.Get("partner_id"), strings.ToLower(q.Get("program"))
	if status != "" && !slices.Contains([]string{"open", "cleared", "confirmed"}, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is open, cleared or confirmed")
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
	rows, err := s.pool.Query(r.Context(), partnerFlagSelect+` WHERE f.workspace_id = $1 AND f.brand_id = $2 AND ($3 = '' OR f.status = $3)
		AND ($4 = '' OR f.membership_id::text = $4) AND ($5 = '' OR p.id::text = $5 OR p.slug = $5)
		AND ($6::timestamptz IS NULL OR f.created_at < $6) ORDER BY f.created_at DESC LIMIT $7`,
		a.WorkspaceID, a.Brand, status, partner, program, before, limit+1)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (partnerFlagView, error) { return scanPartnerFlag(row) })
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
		list = []partnerFlagView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": nextCursor(pageBefore, next)})
}

// flagChange resolves an open flag in a transaction and answers it.
func (s *Server) flagChange(w http.ResponseWriter, r *http.Request, in any, change func(ctx context.Context, tx pgx.Tx, a *access, v partnerFlagView) error) {
	a := accessFrom(r.Context())
	if !decode(w, r, in) {
		return
	}
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "flag not found")
		return
	}
	ctx := r.Context()
	var out partnerFlagView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		v, err := scanPartnerFlag(tx.QueryRow(ctx, partnerFlagSelect+` WHERE f.workspace_id = $1 AND f.brand_id = $2 AND f.id = $3 FOR UPDATE OF f`,
			a.WorkspaceID, a.Brand, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if v.Status != "open" {
			return errInvalid("the flag was " + v.Status + " already")
		}
		if err := change(ctx, tx, a, v); err != nil {
			return err
		}
		out, err = scanPartnerFlag(tx.QueryRow(ctx, partnerFlagSelect+` WHERE f.id = $1`, id))
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type flagNote struct {
	Note string `json:"note"`
}

// clearPartnerFlag says the signal was nothing: the conversion's rewards go
// on as usual (approved when their hold ends, in a program that approves
// on its own) once no other flag on it is open.
func (s *Server) clearPartnerFlag(w http.ResponseWriter, r *http.Request) {
	var in flagNote
	s.flagChange(w, r, &in, func(ctx context.Context, tx pgx.Tx, a *access, v partnerFlagView) error {
		if _, err := tx.Exec(ctx, `UPDATE partner_flags SET status = 'cleared', note = $2, resolved_by = $3, resolved_at = now() WHERE id = $1`,
			v.ID, cutUTF8(strings.TrimSpace(in.Note), 2000), nullIfEmpty(a.ActorID)); err != nil {
			return err
		}
		metrics.PartnerFlag(v.Signal, "cleared")
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner_flag.cleared", "partner_flag", v.ID, map[string]any{
			"program_id": v.ProgramID, "partner_id": v.PartnerID, "conversion_id": v.ConversionID, "signal": v.Signal, "has_note": in.Note != ""})
	})
}

// confirmPartnerFlag says the conversion was fraud: every open flag on it is
// confirmed, its pending rewards (for both sides) are rejected, and the
// partner is suspended when asked. Rewards already approved stay; correct
// them with an adjustment.
func (s *Server) confirmPartnerFlag(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Note           string `json:"note"`
		SuspendPartner bool   `json:"suspend_partner"`
	}
	s.flagChange(w, r, &in, func(ctx context.Context, tx pgx.Tx, a *access, v partnerFlagView) error {
		note := strings.TrimSpace(in.Note)
		if _, err := tx.Exec(ctx, `UPDATE partner_flags SET status = 'confirmed', note = CASE WHEN id = $2 THEN $3 ELSE note END,
			resolved_by = $4, resolved_at = now() WHERE conversion_id = $1 AND status = 'open'`,
			v.ConversionID, v.ID, cutUTF8(note, 2000), nullIfEmpty(a.ActorID)); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text FROM partner_rewards WHERE workspace_id = $1 AND conversion_id = $2 AND status = 'pending'`,
			a.WorkspaceID, v.ConversionID)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		reason := "Fraud confirmed (" + v.Signal + ")"
		if note != "" {
			reason += ": " + note
		}
		for _, id := range ids {
			rw, err := loadReward(ctx, tx, a.WorkspaceID, id)
			if err != nil {
				return err
			}
			if err := rejectReward(ctx, tx, &a.principal, a.WorkspaceID, rw, reason, "fraud_review"); err != nil {
				return err
			}
		}
		if in.SuspendPartner {
			tag, err := tx.Exec(ctx, `UPDATE partner_memberships SET status = 'suspended', updated_at = now() WHERE id = $1 AND status <> 'suspended'`, v.PartnerID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() > 0 {
				if err := partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner.updated", "partner", v.PartnerID,
					map[string]any{"program_id": v.ProgramID, "status": "suspended", "by": "fraud_review"}); err != nil {
					return err
				}
			}
		}
		metrics.PartnerFlag(v.Signal, "confirmed")
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner_flag.confirmed", "partner_flag", v.ID, map[string]any{
			"program_id": v.ProgramID, "partner_id": v.PartnerID, "conversion_id": v.ConversionID, "signal": v.Signal,
			"rejected": len(ids), "suspended": in.SuspendPartner, "has_note": note != ""})
	})
}
