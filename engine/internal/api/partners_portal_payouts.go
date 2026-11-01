package api

import (
	"github.com/jackc/pgx/v5"
	"net/http"
)

// A partner requests the same due payout the brand can create. This allocates
// existing eligible ledger entries; it never holds or transfers money.
func (s *Server) portalRequestPayout(w http.ResponseWriter, r *http.Request, c *portalCaller) {
	if c.readOnly || c.partner.Status != "active" {
		writeError(w, r, http.StatusForbidden, "forbidden", "only an active partner can request payment")
		return
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "partner-payout:"+c.program.ws); err != nil {
			return err
		}
		var details bool
		if err := tx.QueryRow(ctx, `SELECT payout_method IS NOT NULL FROM partner_memberships WHERE workspace_id=$1 AND program_id=$2 AND id=$3`, c.program.ws, c.program.id, c.partner.ID).Scan(&details); err != nil {
			return err
		}
		if !details {
			return errInvalid("add your payout details in Settings before requesting a payout")
		}

		rows, err := tx.Query(ctx, `SELECT w.id::text,w.amount_cents FROM partner_rewards w JOIN partner_programs p ON p.id=w.program_id WHERE w.workspace_id=$1 AND w.program_id=$2 AND w.membership_id=$3 AND w.status='approved' AND w.payout_id IS NULL AND w.side='partner' AND w.kind<>'action' AND `+partnerPayoutTermDue+` AND NOT `+heldAtSource+` ORDER BY w.id FOR UPDATE OF w`, c.program.ws, c.program.id, c.partner.ID)
		if err != nil {
			return err
		}
		var ids []string
		var total int64
		for rows.Next() {
			var id string
			var amount int64
			if err := rows.Scan(&id, &amount); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
			total += amount
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		if total <= 0 || total < c.program.minPayout {
			return errInvalid("no payable balance meets the minimum yet; payment terms and reward holds may still apply")
		}
		if err := tx.QueryRow(ctx, `INSERT INTO partner_payouts(workspace_id,membership_id,amount_cents,currency) VALUES($1,$2,$3,$4) RETURNING id::text`, c.program.ws, c.partner.ID, total, c.program.currency).Scan(&id); err != nil {
			return err
		}

		tag, err := tx.Exec(ctx, `UPDATE partner_rewards SET payout_id=$4,updated_at=now() WHERE workspace_id=$1 AND program_id=$2 AND membership_id=$3 AND id=ANY($5::uuid[])`, c.program.ws, c.program.id, c.partner.ID, id, ids)

		if err != nil {
			return err
		}
		return partnerRecord(ctx, tx, nil, c.program.ws, "partner_payout.created", "partner_payout", id, map[string]any{"program_id": c.program.id, "partner_id": c.partner.ID, "amount_cents": total, "currency": c.program.currency, "rewards": tag.RowsAffected(), "requested_by_partner": true})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "status": "due"})
}
