package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/paidads"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Cost entered by hand (plans/ad-tracker.md, B6): what a tracker campaign
// cost over a period, for networks whose cost Outflare can't read. An
// update names the campaign, the days (in the brand's reporting time zone)
// and the amount; a job shares it out evenly over the campaign's clicks in
// those days, to the millionth, so the tracker report's total for the
// period is the amount entered and every breakdown has cost. The cost goes
// where a network's own reported cost goes (network_cost_micros), which the
// report reads before the click's own {cost}. Never both: a campaign whose
// cost comes from a cost model, or whose network reports its cost for
// those days, is refused.

const (
	jobCostUpdate = "tracker.cost_update"
	// costUpdateMaxDays is the longest period one update covers, and
	// costUpdateMax the most updates one call takes (a CSV's rows).
	costUpdateMaxDays = 31
	costUpdateMax     = 500
)

func (s *Server) routesCostUpdates(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/tracker-cost-updates", s.onBrand(auth.ScopeAnalyticsRead, s.listCostUpdates))
	mux.HandleFunc("POST /v1/brands/{brand}/tracker-cost-updates", s.onBrand(auth.ScopeAnalyticsManage, s.createCostUpdates))
}

type costUpdateView struct {
	ID         string     `json:"id"`
	CampaignID string     `json:"campaign_id"`
	From       string     `json:"from"`
	To         string     `json:"to"`
	Mode       string     `json:"mode"`
	Amount     string     `json:"amount"`
	Status     string     `json:"status"`
	Clicks     int64      `json:"clicks"`
	Error      string     `json:"error"`
	CreatedAt  time.Time  `json:"created_at"`
	AppliedAt  *time.Time `json:"applied_at"`
}

type costUpdateInput struct {
	CampaignID string `json:"campaign_id"`
	From       string `json:"from"`
	To         string `json:"to"`
	Amount     any    `json:"amount"`
	Mode       string `json:"mode"`
}

// errCostConflict is an update the campaign's present state refuses.
type errCostConflict string

func (e errCostConflict) Error() string { return string(e) }

// createCostUpdates queues the updates of one call, all or none.
func (s *Server) createCostUpdates(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var in struct {
		Updates []costUpdateInput `json:"updates"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Updates) == 0 || len(in.Updates) > costUpdateMax {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "updates has 1 to "+strconv.Itoa(costUpdateMax)+" updates")
		return
	}
	loc, err := s.brandZone(ctx, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	type update struct {
		costUpdateInput
		from, to time.Time
		micros   int64
	}
	ups := make([]update, len(in.Updates))
	for i, u := range in.Updates {
		at := "updates[" + strconv.Itoa(i) + "]"
		up := update{costUpdateInput: u}
		var ferr, terr error
		up.from, ferr = time.Parse(time.DateOnly, u.From)
		up.to, terr = time.Parse(time.DateOnly, u.To)
		var bad string
		switch {
		case !validUUID(u.CampaignID):
			bad = at + ".campaign_id is a tracker campaign's id"
		case ferr != nil || terr != nil:
			bad = at + ".from and .to are days, such as 2026-10-01"
		case up.to.Before(up.from):
			bad = at + ".to is on or after .from"
		case up.to.After(today):
			bad = at + ".to is today or earlier, in the brand's time zone"
		case int(up.to.Sub(up.from).Hours()/24)+1 > costUpdateMaxDays:
			bad = at + " covers at most " + strconv.Itoa(costUpdateMaxDays) + " days; split a longer period"
		case u.Mode != "" && u.Mode != "total" && u.Mode != "clear":
			bad = at + ".mode is total or clear"
		case u.Mode == "clear" && u.Amount != nil:
			bad = at + ".amount isn't sent with mode clear"
		}
		if bad != "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", bad)
			return
		}
		if up.Mode == "" {
			up.Mode = "total"
		}
		if up.Mode == "total" {
			if up.micros, err = payoutMicros(at+".amount", u.Amount); err != nil {
				writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
				return
			}
		}
		// Two amounts for one campaign's day in one call have no order to
		// apply them in, so the call names each day once.
		for j, other := range ups[:i] {
			if other.CampaignID == up.CampaignID && !up.from.After(other.to) && !other.from.After(up.to) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", at+" and updates["+strconv.Itoa(j)+"] are for the same campaign and share days; send one amount for a day")
				return
			}
		}
		ups[i] = up
	}
	out := make([]costUpdateView, 0, len(ups))
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for i, up := range ups {
			at := "updates[" + strconv.Itoa(i) + "]"
			var model string
			var source *string
			err := tx.QueryRow(ctx, `SELECT cost_model, traffic_source_id::text FROM tracking_campaigns WHERE workspace_id = $1 AND brand_id = $2 AND id = $3`,
				a.WorkspaceID, a.Brand, up.CampaignID).Scan(&model, &source)
			if errors.Is(err, pgx.ErrNoRows) {
				return errInvalid(at + ".campaign_id isn't one of the brand's tracker campaigns")
			}
			if err != nil {
				return err
			}
			if model != "token" {
				return errCostConflict(at + ": the campaign's cost comes from its cost model (" + model + "); set the model to token to enter its cost by hand")
			}
			if source != nil {
				var reported bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tracker_network_costs WHERE workspace_id = $1 AND source_id = $2 AND day BETWEEN $3 AND $4)`,
					a.WorkspaceID, *source, up.From, up.To).Scan(&reported); err != nil {
					return err
				}
				if reported {
					return errCostConflict(at + ": the campaign's network reports its cost for these days, and Outflare reads it from there")
				}
			}
			starts, ends := paidads.DayStart(up.from, loc), paidads.DayStart(up.to.AddDate(0, 0, 1), loc)
			v := costUpdateView{CampaignID: up.CampaignID, From: up.From, To: up.To, Mode: up.Mode, Amount: formatMicros(up.micros), Status: "queued"}
			var actor *string
			if a.ActorID != "" {
				actor = &a.ActorID
			}
			if err := tx.QueryRow(ctx, `INSERT INTO tracker_cost_updates (workspace_id, brand_id, campaign_id, from_day, to_day, starts_at, ends_at, mode, amount_micros, created_by)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id::text, created_at`,
				a.WorkspaceID, a.Brand, up.CampaignID, up.From, up.To, starts, ends, up.Mode, up.micros, actor).Scan(&v.ID, &v.CreatedAt); err != nil {
				return err
			}
			if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobCostUpdate, WorkspaceID: &a.WorkspaceID, Payload: map[string]string{"update_id": v.ID},
				MaxAttempts: 5, IdempotencyKey: "cost-update:" + v.ID}); err != nil {
				return err
			}
			if err := recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "tracking_campaign.cost_updated", "tracking_campaign", up.CampaignID,
				map[string]any{"brand_id": a.Brand, "update_id": v.ID, "from": up.From, "to": up.To, "mode": up.Mode},
				map[string]any{"mode": up.Mode, "days": int(up.to.Sub(up.from).Hours()/24) + 1, "updates": len(ups)}); err != nil {
				return err
			}
			out = append(out, v)
		}
		return nil
	})
	var conflict errCostConflict
	if errors.As(err, &conflict) {
		writeError(w, r, http.StatusConflict, "conflict", string(conflict))
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	for range out {
		metrics.TrackerCostUpdate("queued")
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "data": out})
}

// listCostUpdates lists the brand's cost updates, newest first.
func (s *Server) listCostUpdates(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	campaign, status := q.Get("campaign"), q.Get("status")
	if campaign != "" && !validUUID(campaign) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "campaign is a tracker campaign's id")
		return
	}
	if status != "" && status != "queued" && status != "applied" && status != "failed" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is queued, applied or failed")
		return
	}
	limit, ok := pageLimit(w, r, 50, 200)
	if !ok {
		return
	}
	// One call's updates share a created_at, so the id breaks the tie.
	before, beforeID, ok := beforeTimeID(w, r)
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT id::text, campaign_id::text, from_day::text, to_day::text, mode, amount_micros, status, clicks, error, created_at, applied_at
		FROM tracker_cost_updates WHERE workspace_id = $1 AND brand_id = $2 AND ($3 = '' OR campaign_id = NULLIF($3, '')::uuid) AND ($4 = '' OR status = $4)
		  AND ($5::timestamptz IS NULL OR (created_at, id) < ($5, $7::uuid))
		ORDER BY created_at DESC, id DESC LIMIT $6`, a.WorkspaceID, a.Brand, campaign, status, before, limit+1, cmp.Or(beforeID, firstUUID))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (costUpdateView, error) {
		var v costUpdateView
		var micros int64
		err := row.Scan(&v.ID, &v.CampaignID, &v.From, &v.To, &v.Mode, &micros, &v.Status, &v.Clicks, &v.Error, &v.CreatedAt, &v.AppliedAt)
		v.Amount = formatMicros(micros)
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next any
	if len(list) > limit {
		next, list = timeIDCursor(list[limit-1].CreatedAt, list[limit-1].ID), list[:limit]
	}
	if list == nil {
		list = []costUpdateView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": next})
}

// costUpdateJob applies one queued update. It counts the campaign's clicks
// in the period and writes each one's share, a day at a time, in one
// transaction on one snapshot, so the shares add up to the amount exactly
// whatever arrives meanwhile. Every click counts, likely bots too: the
// network charged for them, as with a network's own reported cost.
func (s *Server) costUpdateJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		UpdateID string `json:"update_id"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || !validUUID(p.UpdateID) {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	outcome, mode := "", ""
	var clicks int64
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		outcome, clicks = "", 0
		var campaign string
		var starts, ends, created time.Time
		var amount int64
		err := tx.QueryRow(ctx, `SELECT campaign_id::text, starts_at, ends_at, mode, amount_micros, created_at FROM tracker_cost_updates
			WHERE workspace_id = $1 AND id = $2 AND status = 'queued' FOR UPDATE`, ws, p.UpdateID).Scan(&campaign, &starts, &ends, &mode, &amount, &created)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // applied, failed or gone
		}
		if err != nil {
			return err
		}
		// A later update for the same days replaces an earlier one, in
		// whatever order their jobs run: this one steps aside when a
		// newer one is already on those clicks. (Two running at once
		// can't both write the same clicks on this isolation level: the
		// second fails, is retried, and lands here.)
		var replaced bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tracker_cost_updates n WHERE n.workspace_id = $1 AND n.campaign_id = $2 AND n.id <> $3
				AND n.status = 'applied' AND n.starts_at < $5 AND n.ends_at > $4 AND (n.created_at, n.id) > ($6, $3::uuid))`,
			ws, campaign, p.UpdateID, starts, ends, created).Scan(&replaced); err != nil {
			return err
		}
		if replaced {
			outcome = "replaced"
			_, err := tx.Exec(ctx, `UPDATE tracker_cost_updates SET status = 'failed', error = 'a later update for the same days replaced this one before it was applied' WHERE id = $1`, p.UpdateID)
			return err
		}
		const mine = `k.workspace_id = $1 AND k.link_id IN (SELECT l.id FROM tracking_links l WHERE l.workspace_id = $1 AND l.campaign_id = $2)
			AND k.clicked_at >= $3 AND k.clicked_at < $4`
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tracking_link_clicks k WHERE `+mine, ws, campaign, starts, ends).Scan(&clicks); err != nil {
			return err
		}
		if mode == "total" && clicks == 0 {
			outcome = "no_clicks"
			_, err := tx.Exec(ctx, `UPDATE tracker_cost_updates SET status = 'failed', error = 'the campaign has no clicks in these days to carry the cost' WHERE id = $1`, p.UpdateID)
			return err
		}
		var base, extra int64
		if clicks > 0 {
			base, extra = amount/clicks, amount%clicks
		}
		// A day at a time, oldest first; the millionths left over by the
		// division go one each to the first clicks.
		for a := starts; a.Before(ends); a = a.Add(24 * time.Hour) {
			b := a.Add(24 * time.Hour)
			if b.After(ends) {
				b = ends
			}
			if mode == "clear" {
				if _, err := tx.Exec(ctx, `UPDATE tracking_link_clicks k SET network_cost_micros = NULL WHERE `+mine+` AND k.network_cost_micros IS NOT NULL`, ws, campaign, a, b); err != nil {
					return err
				}
				continue
			}
			tag, err := tx.Exec(ctx, `WITH c AS (SELECT k.id, row_number() OVER (ORDER BY k.id) AS rn FROM tracking_link_clicks k WHERE `+mine+`)
				UPDATE tracking_link_clicks x SET network_cost_micros = $5::bigint + CASE WHEN c.rn <= $6::bigint THEN 1 ELSE 0 END
				FROM c WHERE x.id = c.id AND x.workspace_id = $1`, ws, campaign, a, b, base, extra)
			if err != nil {
				return err
			}
			extra = max(0, extra-tag.RowsAffected())
		}
		outcome = "applied"
		_, err = tx.Exec(ctx, `UPDATE tracker_cost_updates SET status = 'applied', clicks = $2, error = '', applied_at = now() WHERE id = $1`, p.UpdateID, clicks)
		return err
	})
	if err != nil {
		if job.Attempt < job.MaxAttempts {
			metrics.TrackerCostUpdate("retried")
			return err
		}
		// The last try: say so on the update, so it doesn't stay queued.
		if _, uerr := s.pool.Exec(ctx, `UPDATE tracker_cost_updates SET status = 'failed', error = 'Outflare couldn''t apply it; enter it again' WHERE workspace_id = $1 AND id = $2 AND status = 'queued'`,
			ws, p.UpdateID); uerr != nil {
			return uerr
		}
		metrics.TrackerCostUpdate("error")
		track.Fail(ctx, s.log, track.Event{Name: "tracking_campaign.cost_update_failed", WorkspaceID: ws, Surface: "job", Props: map[string]any{"update_id": p.UpdateID}}, "internal", err)
		return nil
	}
	switch outcome {
	case "applied":
		metrics.TrackerCostUpdate("applied")
		track.Emit(ctx, track.Event{Name: "tracking_campaign.cost_update_applied", WorkspaceID: ws, Surface: "job",
			Props: map[string]any{"update_id": p.UpdateID, "mode": mode, "clicks": clicks}})
	case "replaced":
		metrics.TrackerCostUpdate("replaced")
	case "no_clicks":
		metrics.TrackerCostUpdate("no_clicks")
		track.Fail(ctx, s.log, track.Event{Name: "tracking_campaign.cost_update_failed", WorkspaceID: ws, Surface: "job", Outcome: track.Failure,
			Props: map[string]any{"update_id": p.UpdateID}}, "conflict", nil)
	}
	return nil
}
