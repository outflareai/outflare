package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"net/http"
	"strings"
	"time"
)

var errPartnerCouponConnection = errors.New("connect your own billing account with promotion-code write access before creating a checkout discount")

type partnerPromoCodeInput struct {
	Code   string                    `json:"code"`
	Coupon string                    `json:"coupon"`
	Rules  connectors.PromoCodeRules `json:"rules"`
}

func checkPartnerPromoRules(r *connectors.PromoCodeRules, coupon string) error {
	if r.PercentBP < 0 || r.PercentBP > 10000 || r.AmountCents < 0 || r.AmountCents > 100000000 || r.MinOrderCents < 0 || r.MinOrderCents > 100000000 || r.MaxRedemptions < 0 || r.MaxRedemptions > 1000000 || len(r.ProductIDs) > 50 {
		return errInvalid("coupon discount and limits are outside the supported range")
	}
	if (r.PercentBP > 0 && r.AmountCents > 0) || (coupon != "" && (r.PercentBP > 0 || r.AmountCents > 0)) {
		return errInvalid("choose an existing billing coupon, a percentage, or a fixed discount")
	}
	if len(coupon) > 200 {
		return errInvalid("billing coupon id is at most 200 characters")
	}
	if r.Currency == "" {
		r.Currency = "USD"
	}
	r.Currency = strings.ToUpper(r.Currency)
	if len(r.Currency) != 3 {
		return errInvalid("coupon currency has three letters")
	}
	if r.Duration == "" {
		r.Duration = "once"
	}
	if r.Duration != "once" && r.Duration != "forever" && r.Duration != "repeating" {
		return errInvalid("coupon duration is once, repeating or forever")
	}
	if r.Duration == "repeating" && (r.Months < 1 || r.Months > 120) {
		return errInvalid("repeating discount duration is 1 to 120 months")
	}
	if r.StartsAt != nil && r.EndsAt != nil && !r.StartsAt.Before(*r.EndsAt) {
		return errInvalid("coupon end must be after its start")
	}
	return nil
}
func (s *Server) storePartnerPromoCode(ctx context.Context, ws string, actor *principal, cur *partnerView, in partnerPromoCodeInput) error {
	if !partnerCodeRE.MatchString(in.Code) {
		return errInvalid("code is 1 to 64 letters, digits, hyphens or underscores")
	}
	if in.Rules.EndsAt != nil && !in.Rules.EndsAt.After(time.Now()) {
		return errInvalid("a new coupon must end in the future")
	}
	in.Coupon = strings.TrimSpace(in.Coupon)
	if in.Rules.Currency == "" {
		in.Rules.Currency = cur.Currency
	}
	if err := checkPartnerPromoRules(&in.Rules, in.Coupon); err != nil {
		return err
	}
	managed := in.Coupon != "" || in.Rules.PercentBP > 0 || in.Rules.AmountCents > 0
	var integration *string
	if managed {
		var err error
		integration, err = s.promoCodeConnection(ctx, ws, cur.ProgramID)
		if err != nil {
			return err
		}
		if integration == nil {
			return errPartnerCouponConnection
		}
	}
	raw, _ := json.Marshal(in.Rules)
	if in.Rules.PercentBP == 0 && in.Rules.AmountCents == 0 && !in.Rules.Disabled && in.Rules.StartsAt == nil && in.Rules.EndsAt == nil && in.Rules.MaxRedemptions == 0 && !in.Rules.FirstOrderOnly && in.Rules.MinOrderCents == 0 {
		raw = []byte(`{}`)
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "partner-code:"+ws); err != nil {
			return err
		}
		var taken bool
		var count int
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM partner_memberships WHERE workspace_id=$1 AND lower(code)=lower($2) AND id<>$3) OR EXISTS(SELECT 1 FROM partner_promo_codes WHERE workspace_id=$1 AND lower(code)=lower($2)),(SELECT count(*) FROM partner_promo_codes WHERE workspace_id=$1 AND membership_id=$3)`, ws, in.Code, cur.ID).Scan(&taken, &count); err != nil {
			return err
		}
		if taken {
			return errConflict
		}
		if count >= maxPartnerPromoCodes {
			return errInvalid("a partner can have at most 20 promo codes")
		}
		status := "local"
		if managed {
			status = "pending"
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO partner_promo_codes(workspace_id,membership_id,code,coupon,integration_id,sync_status,rules) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id::text`, ws, cur.ID, in.Code, in.Coupon, integration, status, raw).Scan(&id); err != nil {
			return err
		}
		if managed {
			run := time.Time{}
			if in.Rules.StartsAt != nil {
				run = *in.Rules.StartsAt
			}
			if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobPartnerPromoCode, WorkspaceID: &ws, Payload: partnerPromoJob{ID: id}, RunAt: run, MaxAttempts: 5, IdempotencyKey: "partner-promo:" + id}); err != nil {
				return err
			}
		}
		return partnerRecord(ctx, tx, actor, ws, "partner.promo_code_added", "partner", cur.ID, map[string]any{"program_id": cur.ProgramID, "promo_code_id": id})
	})
}
func (s *Server) portalCreatePromoCode(w http.ResponseWriter, r *http.Request, c *portalCaller) {
	if c.readOnly || c.partner.Status != "active" || c.program.status != "active" {
		writeError(w, r, http.StatusForbidden, "forbidden", "only an active partner can generate a code")
		return
	}
	var in struct {
		Code     string `json:"code"`
		Template string `json:"template"`
	}
	if !decode(w, r, &in) {
		return
	}
	for _, t := range c.program.settings.CouponTemplates {
		if t.ID == in.Template {
			if t.Rules.Disabled || (t.Rules.EndsAt != nil && !t.Rules.EndsAt.After(time.Now())) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "this coupon offer is no longer available")
				return
			}
			if s.partnerCouponError(w, r, s.storePartnerPromoCode(r.Context(), c.program.ws, nil, &c.partner, partnerPromoCodeInput{Code: in.Code, Coupon: t.Coupon, Rules: t.Rules})) {
				return
			}
			v, err := s.loadPartner(r.Context(), c.program.ws, c.partner.ID)
			if err != nil {
				s.internal(w, r, err)
				return
			}
			c.partner = v
			s.portalMe(w, r, c)
			return
		}
	}
	writeError(w, r, http.StatusBadRequest, "invalid_request", "choose a coupon offer enabled by the brand")
}
func (s *Server) togglePartnerPromoCode(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findPartner(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Enabled == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "enabled is required")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		var integration *string
		if err := tx.QueryRow(ctx, `UPDATE partner_promo_codes SET rules=jsonb_set(rules,'{disabled}',to_jsonb($4::boolean)),sync_status=CASE WHEN integration_id IS NULL THEN 'local' ELSE 'pending' END WHERE workspace_id=$1 AND membership_id=$2 AND lower(code)=lower($3) RETURNING id::text,integration_id::text`, a.WorkspaceID, cur.ID, r.PathValue("code"), !*in.Enabled).Scan(&id, &integration); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errNotFound
			}
			return err
		}
		if integration != nil {
			if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobPartnerPromoCode, WorkspaceID: &a.WorkspaceID, Payload: partnerPromoJob{ID: id}, MaxAttempts: 5}); err != nil {
				return err
			}
		}
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner.promo_code_updated", "partner", cur.ID, map[string]any{"program_id": cur.ProgramID, "promo_code_id": id, "enabled": *in.Enabled})
	})
	if s.allocError(w, r, err) {
		return
	}
	if v, ok := s.findPartner(w, r, a.WorkspaceID, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

func (s *Server) partnerCouponError(w http.ResponseWriter, r *http.Request, err error) bool {
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", "that code is already in use; choose another")
		return true
	}
	if errors.Is(err, errPartnerCouponConnection) {
		writeError(w, r, http.StatusConflict, "connector_needed", err.Error())
		return true
	}
	return s.allocError(w, r, err)
}
