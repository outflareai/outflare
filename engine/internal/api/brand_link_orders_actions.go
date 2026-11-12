package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/coverage/linkorders"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

func (s *Server) listLinkOrders(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws, brand, _, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	limit, ok := pageLimit(w, r, 50, 200)
	if !ok {
		return
	}
	cur, ok := linkOrderCursor(w, r, s.pool, "coverage_link_orders", ws, brand)
	if !ok {
		return
	}
	f := linkorders.OrderListFilter{
		Status: strings.TrimSpace(r.URL.Query().Get("status")),
		Q:      strings.TrimSpace(r.URL.Query().Get("q")),
	}
	if v := r.URL.Query().Get("donor_profile_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "donor_profile_id must be a UUID")
			return
		}
		f.DonorProfileID = &id
	}
	list, err := linkorders.ListOrders(r.Context(), s.pool, ws, brand, f, limit+1, cur)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *string
	if len(list) > limit {
		id := list[limit-1].ID.String()
		next = &id
		list = list[:limit]
	}
	if list == nil {
		list = []linkorders.Order{}
	}
	writeLinkOrderList(w, list, next)
}

func (s *Server) getLinkOrder(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws, brand, _, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	id, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	o, err := linkorders.GetOrder(r.Context(), s.pool, ws, brand, id)
	if err != nil {
		s.writeLinkOrderError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) createLinkOrder(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws, brand, actor, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var in struct {
		DonorProfileID      string  `json:"donor_profile_id"`
		TargetURL           string  `json:"target_url"`
		TargetID            *string `json:"target_id"`
		TargetKind          string  `json:"target_kind"`
		TargetChoice        string  `json:"target_choice"`
		AnchorPrimary       string  `json:"anchor_primary"`
		AnchorPrimarySource string  `json:"anchor_primary_source"`
		AnchorSecondary     string  `json:"anchor_secondary"`
		AnchorSecondaryType string  `json:"anchor_secondary_type"`
		SlotType            string  `json:"slot_type"`
		RelRequested        string  `json:"rel_requested"`
		GuaranteeDays       *int    `json:"guarantee_days"`
		Notes               *string `json:"notes"`
		OwnerUserID         *string `json:"owner_user_id"`
		ReplacesOrderID     *string `json:"replaces_order_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	donorID, err := uuid.Parse(in.DonorProfileID)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "donor_profile_id is required")
		return
	}
	req := linkorders.CreateOrderInput{
		DonorProfileID: donorID,
		TargetURL:      strings.TrimSpace(in.TargetURL),
		TargetKind:     in.TargetKind,
		TargetChoice:   in.TargetChoice,
		Anchors: linkorders.Anchors{
			Primary: in.AnchorPrimary, PrimarySource: in.AnchorPrimarySource,
			Secondary: in.AnchorSecondary, SecondaryType: in.AnchorSecondaryType,
		},
		SlotType:      in.SlotType,
		RelRequested:  in.RelRequested,
		GuaranteeDays: in.GuaranteeDays,
		Notes:         in.Notes,
	}
	if in.TargetID != nil && *in.TargetID != "" {
		id, err := uuid.Parse(*in.TargetID)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "target_id must be a UUID")
			return
		}
		req.TargetID = &id
	}
	if in.OwnerUserID != nil && *in.OwnerUserID != "" {
		id, err := uuid.Parse(*in.OwnerUserID)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "owner_user_id must be a UUID")
			return
		}
		req.OwnerUserID = &id
	}
	if in.ReplacesOrderID != nil && *in.ReplacesOrderID != "" {
		id, err := uuid.Parse(*in.ReplacesOrderID)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "replaces_order_id must be a UUID")
			return
		}
		req.ReplacesOrderID = &id
	}
	var out *linkorders.Order
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		o, err := linkorders.CreateOrder(r.Context(), tx, ws, brand, actor, req)
		if err != nil {
			return err
		}
		if err := recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "link_orders.created", "brand", a.Brand, map[string]any{"brand_id": a.Brand, "status": o.Status, "target_choice": o.TargetChoice, "target_kind": o.TargetKind, "anchor_primary_source": o.AnchorPrimarySource, "anchor_secondary_type": o.AnchorSecondaryType}, map[string]any{"brand_id": a.Brand, "target_choice": o.TargetChoice, "target_kind": o.TargetKind, "anchor_primary_source": o.AnchorPrimarySource, "anchor_secondary_type": o.AnchorSecondaryType}); err != nil {
			return err
		}
		out = o
		return nil
	})
	if err != nil {
		s.writeLinkOrderError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) updateLinkOrder(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws, brand, _, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	id, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	var raw map[string]any
	if !decode(w, r, &raw) {
		return
	}
	var in linkorders.PatchOrderInput
	if v, ok := raw["donor_profile_id"]; ok {
		s, _ := v.(string)
		pid, err := uuid.Parse(s)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "donor_profile_id must be a UUID")
			return
		}
		in.DonorProfileID = &pid
	}
	setStr := func(key string, dest ***string) {
		if v, ok := raw[key]; ok {
			if v == nil {
				var n *string
				*dest = &n
				return
			}
			s, _ := v.(string)
			*dest = ptrTo(&s)
		}
	}
	setStr("target_url", &in.TargetURL)
	if v, ok := raw["target_id"]; ok {
		if v == nil {
			var n *uuid.UUID
			in.TargetID = &n
		} else {
			s, _ := v.(string)
			tid, err := uuid.Parse(s)
			if err != nil {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "target_id must be a UUID or null")
				return
			}
			in.TargetID = ptrTo(&tid)
		}
	}
	for key, dest := range map[string]**string{"target_kind": &in.TargetKind, "target_choice": &in.TargetChoice} {
		if v, ok := raw[key]; ok {
			s, isStr := v.(string)
			if v != nil && !isStr {
				writeError(w, r, http.StatusBadRequest, "invalid_request", key+" must be a string")
				return
			}
			*dest = &s
		}
	}
	anchorKeys := []string{"anchor_primary", "anchor_primary_source", "anchor_secondary", "anchor_secondary_type"}
	for _, key := range anchorKeys {
		if _, ok := raw[key]; !ok {
			continue
		}
		// Anchors change together: the stored values fill what is not sent.
		cur, err := linkorders.GetOrder(r.Context(), s.pool, ws, brand, id)
		if err != nil {
			s.writeLinkOrderError(w, r, err)
			return
		}
		an := linkorders.Anchors{Primary: cur.AnchorPrimary, PrimarySource: cur.AnchorPrimarySource, Secondary: cur.AnchorSecondary, SecondaryType: cur.AnchorSecondaryType}
		for _, k := range anchorKeys {
			v, ok := raw[k]
			if !ok {
				continue
			}
			str, isStr := v.(string)
			if v != nil && !isStr {
				writeError(w, r, http.StatusBadRequest, "invalid_request", k+" must be a string or null")
				return
			}
			switch k {
			case "anchor_primary":
				an.Primary = str
				if _, set := raw["anchor_primary_source"]; !set {
					an.PrimarySource = ""
				}
			case "anchor_primary_source":
				an.PrimarySource = str
			case "anchor_secondary":
				an.Secondary = str
			case "anchor_secondary_type":
				an.SecondaryType = str
			}
		}
		in.Anchors = &an
		break
	}
	setStr("slot_type", &in.SlotType)
	setStr("rel_requested", &in.RelRequested)
	setStr("notes", &in.Notes)
	if v, ok := raw["guarantee_days"]; ok {
		if v == nil {
			var n *int
			in.GuaranteeDays = &n
		} else {
			n := int(v.(float64))
			in.GuaranteeDays = ptrTo(&n)
		}
	}
	if v, ok := raw["owner_user_id"]; ok {
		if v == nil {
			var n *uuid.UUID
			in.OwnerUserID = &n
		} else {
			s, _ := v.(string)
			id, err := uuid.Parse(s)
			if err != nil {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "owner_user_id must be a UUID")
				return
			}
			in.OwnerUserID = ptrTo(&id)
		}
	}
	var out *linkorders.Order
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		o, err := linkorders.PatchOrder(r.Context(), tx, ws, brand, id, in)
		if err != nil {
			return err
		}
		out = o
		return nil
	})
	if err != nil {
		s.writeLinkOrderError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listLinkOrderHistory(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws, brand, _, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	id, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	list, err := linkorders.ListEvents(r.Context(), s.pool, ws, brand, id)
	if err != nil {
		s.writeLinkOrderError(w, r, err)
		return
	}
	if list == nil {
		list = []linkorders.Event{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) getLinkOrderSummary(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws, brand, _, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v, err := linkorders.Summarize(r.Context(), s.pool, ws, brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) getLinkOrderBudget(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws, brand, _, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v, err := linkorders.CurrentBudgets(r.Context(), s.pool, ws, brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) quoteLinkOrder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AmountMinor int64  `json:"amount_minor"`
		Currency    string `json:"currency"`
	}
	if !decode(w, r, &in) {
		return
	}
	s.mutateLinkOrder(w, r, "link_orders.quoted", false, false, func(tx pgx.Tx, ws, brand, id, actor uuid.UUID) (*linkorders.Order, error) {
		return linkorders.Quote(r.Context(), tx, ws, brand, id, actor, in.AmountMinor, strings.ToUpper(in.Currency))
	})
}

func (s *Server) negotiateLinkOrder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Notes *string `json:"notes"`
	}
	if r.ContentLength != 0 && r.Header.Get("Content-Type") != "" {
		if !decode(w, r, &in) {
			return
		}
	}
	s.mutateLinkOrder(w, r, "link_orders.negotiating", false, false, func(tx pgx.Tx, ws, brand, id, actor uuid.UUID) (*linkorders.Order, error) {
		return linkorders.Negotiate(r.Context(), tx, ws, brand, id, actor, in.Notes)
	})
}

func (s *Server) approveLinkOrder(w http.ResponseWriter, r *http.Request) {
	s.mutateLinkOrder(w, r, "link_order.approved", true, false, func(tx pgx.Tx, ws, brand, id, actor uuid.UUID) (*linkorders.Order, error) {
		return linkorders.Approve(r.Context(), tx, ws, brand, id, actor)
	})
}

func (s *Server) publishLinkOrder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PublishedURL string `json:"published_url"`
		ProofURL     string `json:"proof_url"`
	}
	if !decode(w, r, &in) {
		return
	}
	s.mutateLinkOrder(w, r, "link_order.published", false, false, func(tx pgx.Tx, ws, brand, id, actor uuid.UUID) (*linkorders.Order, error) {
		o, err := linkorders.Publish(r.Context(), tx, ws, brand, id, actor, strings.TrimSpace(in.PublishedURL), strings.TrimSpace(in.ProofURL))
		if err != nil || o == nil || o.PlacementID == nil {
			return o, err
		}
		a := accessFrom(r.Context())
		_, err = jobs.Enqueue(r.Context(), tx, jobs.NewJob{
			Kind:           jobCoverageCheck,
			WorkspaceID:    &a.WorkspaceID,
			Payload:        map[string]string{"placement_id": o.PlacementID.String(), "brand_id": a.Brand, "source": "link_orders"},
			IdempotencyKey: "coverage.check:" + o.PlacementID.String() + ":link_orders:publish",
			MaxAttempts:    3,
		})
		return o, err
	})
}

func (s *Server) checkLinkOrder(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws, brand, _, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	id, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	o, err := linkorders.GetOrder(r.Context(), s.pool, ws, brand, id)
	if err != nil {
		s.writeLinkOrderError(w, r, err)
		return
	}
	if o.PlacementID == nil {
		writeError(w, r, http.StatusConflict, "conflict", "publish the order first")
		return
	}
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		_, err := jobs.Enqueue(r.Context(), tx, jobs.NewJob{
			Kind:           jobCoverageCheck,
			WorkspaceID:    &a.WorkspaceID,
			Payload:        map[string]string{"placement_id": o.PlacementID.String(), "brand_id": a.Brand, "source": "link_orders"},
			IdempotencyKey: "coverage.check:" + o.PlacementID.String() + ":link_orders:" + time.Now().UTC().Format(time.RFC3339Nano),
			MaxAttempts:    3,
		})
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "placement_id": o.PlacementID.String()})
}

func (s *Server) payLinkOrder(w http.ResponseWriter, r *http.Request) {
	s.mutateLinkOrder(w, r, "link_order.paid", true, true, func(tx pgx.Tx, ws, brand, id, actor uuid.UUID) (*linkorders.Order, error) {
		return linkorders.Pay(r.Context(), tx, ws, brand, id, actor)
	})
}

func (s *Server) verifyLinkOrder(w http.ResponseWriter, r *http.Request) {
	s.mutateLinkOrder(w, r, "link_order.verified", false, false, func(tx pgx.Tx, ws, brand, id, actor uuid.UUID) (*linkorders.Order, error) {
		return linkorders.Verify(r.Context(), tx, ws, brand, id, actor)
	})
}

func (s *Server) loseLinkOrder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Notes *string `json:"notes"`
	}
	if r.ContentLength != 0 && r.Header.Get("Content-Type") != "" {
		if !decode(w, r, &in) {
			return
		}
	}
	s.mutateLinkOrder(w, r, "link_order.lost", false, false, func(tx pgx.Tx, ws, brand, id, actor uuid.UUID) (*linkorders.Order, error) {
		return linkorders.MarkLost(r.Context(), tx, ws, brand, id, actor, in.Notes)
	})
}

func (s *Server) replaceLinkOrder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DonorProfileID string  `json:"donor_profile_id"`
		Notes          *string `json:"notes"`
	}
	if !decode(w, r, &in) {
		return
	}
	donorID, err := uuid.Parse(in.DonorProfileID)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "donor_profile_id is required")
		return
	}
	s.mutateLinkOrder(w, r, "link_order.replacement_opened", false, false, func(tx pgx.Tx, ws, brand, id, actor uuid.UUID) (*linkorders.Order, error) {
		return linkorders.Replace(r.Context(), tx, ws, brand, id, actor, donorID, in.Notes)
	})
}

func (s *Server) creditLinkOrder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason *string `json:"reason"`
	}
	if r.ContentLength != 0 && r.Header.Get("Content-Type") != "" {
		if !decode(w, r, &in) {
			return
		}
	}
	s.mutateLinkOrder(w, r, "link_orders.credited", true, true, func(tx pgx.Tx, ws, brand, id, actor uuid.UUID) (*linkorders.Order, error) {
		return linkorders.Credit(r.Context(), tx, ws, brand, id, actor, in.Reason)
	})
}

func (s *Server) mutateLinkOrder(w http.ResponseWriter, r *http.Request, action string, approver, finance bool, fn func(tx pgx.Tx, ws, brand, id, actor uuid.UUID) (*linkorders.Order, error)) {
	a := accessFrom(r.Context())
	if approver && !s.requireLinkOrderApprover(w, r, a, finance) {
		return
	}
	ws, brand, actor, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	id, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	var out *linkorders.Order
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		o, err := fn(tx, ws, brand, id, actor)
		if err != nil {
			return err
		}
		data := map[string]any{"brand_id": a.Brand, "status": o.Status}
		if o.Currency != nil && o.DonorAmountMinor != nil {
			data["currency"] = *o.Currency
			data["amount_minor"] = *o.DonorAmountMinor
		}
		if err := recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, action, "brand", a.Brand, data, data); err != nil {
			return err
		}
		out = o
		return nil
	})
	if err != nil {
		s.writeLinkOrderError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
