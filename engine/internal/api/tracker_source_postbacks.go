package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/netguard"
	"github.com/outflareai/outflare/engine/internal/tokens"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/trafficsources"
)

// Conversions back to traffic sources (plans/ad-tracker.md, T7). A traffic
// source with a postback URL (an ad network with no conversion API, such as
// a push or native network) is told of each tracker conversion on its
// campaigns' clicks, by its send-back rules: the share of the payout sent
// and its decimals, a minimum payout, the types and statuses sent, and
// whether a change of status or payout is sent again. Each state of a
// conversion is sent once, from a job, over a client that reaches only
// public addresses and never follows a redirect; every send is kept.

const jobSourcePostback = "tracker.source_postback"

// sourcePostbackClient calls traffic sources' postback URLs. Tests swap it
// for one that reaches their local server.
var sourcePostbackClient = func() *http.Client {
	c := netguard.Client(10 * time.Second)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c
}()

// sourcePostbackRules are a traffic source's send-back rules.
type sourcePostbackRules struct {
	// Share is the percentage of the payout sent: 100 sends it all, 0
	// sends 0.
	Share int `json:"share"`
	// Decimals is how many decimals the payout sent is rounded to.
	Decimals int `json:"decimals"`
	// MinPayout is the smallest payout sent (after the share); smaller
	// conversions aren't sent.
	MinPayout string `json:"min_payout"`
	// Types are the conversion types sent; empty for every type.
	Types []string `json:"types"`
	// Statuses are the statuses sent: approved by default.
	Statuses []string `json:"statuses"`
	// Resend sends a conversion again when its status or payout changes.
	Resend bool `json:"resend"`
}

type sourcePostbackRulesInput struct {
	Share     *int      `json:"share"`
	Decimals  *int      `json:"decimals"`
	MinPayout any       `json:"min_payout"`
	Types     *[]string `json:"types"`
	Statuses  *[]string `json:"statuses"`
	Resend    *bool     `json:"resend"`
}

// apply puts in's rules over r, checking them; minMicros is r's minimum.
func (in *sourcePostbackRulesInput) apply(r *sourcePostbackRules, minMicros *int64) error {
	if in == nil {
		return nil
	}
	if in.Share != nil {
		if *in.Share < 0 || *in.Share > 1000 {
			return errInvalid("postback_rules.share is 0 to 1,000: the percentage of the payout sent")
		}
		r.Share = *in.Share
	}
	if in.Decimals != nil {
		if *in.Decimals < 0 || *in.Decimals > 6 {
			return errInvalid("postback_rules.decimals is 0 to 6")
		}
		r.Decimals = *in.Decimals
	}
	if in.MinPayout != nil {
		m, err := payoutMicros("postback_rules.min_payout", in.MinPayout)
		if err != nil {
			return err
		}
		*minMicros, r.MinPayout = m, formatMicros(m)
	}
	if in.Types != nil {
		if len(*in.Types) > 20 {
			return errInvalid("postback_rules.types has at most 20 types")
		}
		r.Types = make([]string, 0, len(*in.Types))
		for _, t := range *in.Types {
			t = strings.ToLower(strings.TrimSpace(t))
			if !conversionTypeRe.MatchString(t) {
				return errInvalid("postback_rules.types are conversion types, such as lead or sale")
			}
			r.Types = append(r.Types, t)
		}
	}
	if in.Statuses != nil {
		r.Statuses = make([]string, 0, len(*in.Statuses))
		for _, st := range *in.Statuses {
			if st != "approved" && st != "pending" && st != "declined" {
				return errInvalid("postback_rules.statuses are approved, pending or declined")
			}
			if !slices.Contains(r.Statuses, st) {
				r.Statuses = append(r.Statuses, st)
			}
		}
	}
	if in.Resend != nil {
		r.Resend = *in.Resend
	}
	return nil
}

// sentPayout is the payout a source is sent for payout: its share,
// rounded to its decimals.
func sentPayout(micros int64, share, decimals int) int64 {
	v := float64(micros) * float64(share) / 100
	unit := math.Pow(10, float64(6-decimals))
	return int64(math.Round(v/unit) * unit)
}

// enqueueSourcePostback queues a conversion's postback to its click's
// traffic source, inside tx, when the source's rules send its present
// state: a status and type it sends, a payout at least its minimum, and,
// once one was sent, a source that sends changes.
func enqueueSourcePostback(ctx context.Context, tx pgx.Tx, ws, conv string) error {
	var source, url, status, typ string
	var share, decimals int
	var minMicros, micros int64
	var types, statuses []string
	var resend, sent bool
	err := tx.QueryRow(ctx, `SELECT ts.id::text, ts.postback_url, ts.postback_share, ts.postback_decimals, ts.postback_min_micros, ts.postback_types,
			ts.postback_statuses, ts.postback_resend, c.status, c.type, c.payout_micros,
			EXISTS (SELECT 1 FROM tracker_source_postbacks p WHERE p.conversion_id = c.id)
		FROM tracker_conversions c
		JOIN tracking_campaigns k ON k.id = c.campaign_id
		JOIN tracker_traffic_sources ts ON ts.id = k.traffic_source_id AND ts.workspace_id = c.workspace_id AND ts.status = 'active'
		WHERE c.workspace_id = $1 AND c.id = $2`, ws, conv).
		Scan(&source, &url, &share, &decimals, &minMicros, &types, &statuses, &resend, &status, &typ, &micros, &sent)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil // no campaign, or its campaign has no active traffic source
	case err != nil:
		return err
	case url == "" || strings.Contains(url, tokens.Placeholder):
		return nil
	case !slices.Contains(statuses, status), len(types) > 0 && !slices.Contains(types, typ):
		metrics.TrackerSourcePostback("filtered")
		return nil
	case sent && !resend:
		return nil
	}
	payout := sentPayout(micros, share, decimals)
	if payout < minMicros {
		metrics.TrackerSourcePostback("filtered")
		return nil
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO tracker_source_postbacks (workspace_id, brand_id, source_id, conversion_id, state, payout_micros)
		SELECT c.workspace_id, c.brand_id, $3, c.id, $4, $5 FROM tracker_conversions c WHERE c.id = $2 AND c.workspace_id = $1
		ON CONFLICT (conversion_id, state) DO NOTHING RETURNING id::text`,
		ws, conv, source, status+":"+typ+":"+strconv.FormatInt(micros, 10), payout).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // this state was sent before
	}
	if err != nil {
		return err
	}
	metrics.TrackerSourcePostback("queued")
	_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobSourcePostback, WorkspaceID: &ws, Payload: map[string]string{"send_id": id},
		MaxAttempts: 5, IdempotencyKey: "source-postback:" + id})
	return err
}

// sourcePostbackJob calls a traffic source's postback URL for one queued
// send.
func (s *Server) sourcePostbackJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		SendID string `json:"send_id"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || !validUUID(p.SendID) {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	var tmpl, method, source, key, txid, typ, status, currency, campaign, campaignCode string
	var sourceClickID, country *string
	var fields []byte
	var payout int64
	var decimals int
	err := s.pool.QueryRow(ctx, `SELECT ts.postback_url, ts.postback_method, ts.id::text, ts.postback_decimals, o.payout_micros,
			c.click_key, c.transaction_id, c.type, c.status, c.currency, COALESCE(k.slug, ''), COALESCE(k.code, ''),
			x.source_click_id, x.country, COALESCE(x.fields, '{}')
		FROM tracker_source_postbacks o
		JOIN tracker_traffic_sources ts ON ts.id = o.source_id
		JOIN tracker_conversions c ON c.id = o.conversion_id
		LEFT JOIN tracking_campaigns k ON k.id = c.campaign_id
		LEFT JOIN tracking_link_clicks x ON x.id = c.click_id
		WHERE o.workspace_id = $1 AND o.id = $2 AND o.status = 'queued'`, ws, p.SendID).
		Scan(&tmpl, &method, &source, &decimals, &payout, &key, &txid, &typ, &status, &currency, &campaign, &campaignCode, &sourceClickID, &country, &fields)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // sent, given up on, or gone
	}
	if err != nil {
		return err
	}
	if tmpl == "" {
		return s.sourcePostbackDone(ctx, ws, p.SendID, source, "failed", 0, "", "the traffic source has no postback URL any more", "postback_invalid", nil)
	}
	values := tokens.Values{"click_id": key, "source_click_id": deref(sourceClickID), "payout": strconv.FormatFloat(float64(payout)/1e6, 'f', decimals, 64),
		"currency": currency, "transaction_id": txid, "conversion_type": typ, "status": status, "campaign": campaign, "campaign_code": campaignCode,
		"country": deref(country), "random": strconv.FormatInt(rand.Int64N(1_000_000_000), 10)}
	var f map[string]string
	if json.Unmarshal(fields, &f) == nil {
		for n, v := range f {
			values["field"+n] = v
		}
	}
	target := tokens.ExpandKnown(tmpl, values, trafficsources.PostbackTokens)
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return s.sourcePostbackDone(ctx, ws, p.SendID, source, "failed", 0, target, "the postback URL isn't valid", "postback_invalid", err)
	}
	req.Header.Set("User-Agent", "Outflare-Postback/1")
	resp, err := sourcePostbackClient.Do(req)
	last := job.Attempt >= job.MaxAttempts
	if err != nil {
		if last {
			return s.sourcePostbackDone(ctx, ws, p.SendID, source, "failed", 0, target, "couldn't reach the network", "postback_unreachable", err)
		}
		s.sourcePostbackAttempt(ctx, p.SendID, 0, target, "couldn't reach the network")
		metrics.TrackerSourcePostback("retried")
		return err
	}
	resp.Body.Close()
	switch code := resp.StatusCode; {
	case code >= 200 && code < 300:
		return s.sourcePostbackDone(ctx, ws, p.SendID, source, "sent", code, target, "", "", nil)
	case code == http.StatusTooManyRequests || code >= 500:
		msg := "the network answered " + strconv.Itoa(code)
		if last {
			return s.sourcePostbackDone(ctx, ws, p.SendID, source, "failed", code, target, msg, "postback_unreachable", errors.New(msg))
		}
		s.sourcePostbackAttempt(ctx, p.SendID, code, target, msg)
		metrics.TrackerSourcePostback("retried")
		return errors.New(msg)
	default:
		msg := "the network refused it with " + strconv.Itoa(code)
		return s.sourcePostbackDone(ctx, ws, p.SendID, source, "failed", code, target, msg, "postback_rejected", errors.New(msg))
	}
}

func (s *Server) sourcePostbackAttempt(ctx context.Context, id string, httpStatus int, url, msg string) {
	var hs *int
	if httpStatus > 0 {
		hs = &httpStatus
	}
	if _, err := s.pool.Exec(ctx, `UPDATE tracker_source_postbacks SET attempts = attempts + 1, http_status = $2, url = $3, error = $4 WHERE id = $1`,
		id, hs, cutUTF8(url, 2000), msg); err != nil {
		s.log.WarnContext(ctx, "tracker: source postback attempt not stored", "err", err)
	}
}

// sourcePostbackDone stores how a send ended; code is set for a failure.
func (s *Server) sourcePostbackDone(ctx context.Context, ws, id, source, status string, httpStatus int, url, msg, code string, cause error) error {
	var hs *int
	if httpStatus > 0 {
		hs = &httpStatus
	}
	if _, err := s.pool.Exec(ctx, `UPDATE tracker_source_postbacks SET status = $2, attempts = attempts + 1, http_status = $3, url = $4, error = $5,
		sent_at = CASE WHEN $2 = 'sent' THEN now() END WHERE id = $1`, id, status, hs, cutUTF8(url, 2000), msg); err != nil {
		return err
	}
	if status == "sent" {
		metrics.TrackerSourcePostback("sent")
		return nil
	}
	metrics.TrackerSourcePostback(strings.TrimPrefix(code, "postback_"))
	track.Fail(ctx, s.log, track.Event{Name: "traffic_source.postback_failed", WorkspaceID: ws, Surface: "job",
		Props: map[string]any{"traffic_source_id": source, "http_status": httpStatus}}, code, cause)
	return nil
}

// The sends.

func (s *Server) routesSourcePostbacks(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/traffic-sources/{id}/postbacks", s.onBrand(auth.ScopeAnalyticsRead, s.listSourcePostbacks))
}

type sourcePostbackView struct {
	ID           string     `json:"id"`
	ConversionID string     `json:"conversion_id"`
	Status       string     `json:"status"`
	Payout       string     `json:"payout"`
	URL          string     `json:"url"`
	HTTPStatus   *int       `json:"http_status"`
	Error        string     `json:"error"`
	Attempts     int        `json:"attempts"`
	SentAt       *time.Time `json:"sent_at"`
	CreatedAt    time.Time  `json:"created_at"`
}

// listSourcePostbacks lists a traffic source's sends, newest first.
func (s *Server) listSourcePostbacks(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	if _, err := s.findTrafficSource(ctx, a.WorkspaceID, a.Brand, r.PathValue("id")); errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "traffic source not found")
		return
	} else if err != nil {
		s.internal(w, r, err)
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && status != "queued" && status != "sent" && status != "failed" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is queued, sent or failed")
		return
	}
	limit, ok := pageLimit(w, r, 50, 200)
	if !ok {
		return
	}
	before, ok := beforeTime(w, r)
	if !ok {
		return
	}
	rows, err := s.pool.Query(ctx, `SELECT id::text, conversion_id::text, status, payout_micros, url, http_status, error, attempts, sent_at, created_at
		FROM tracker_source_postbacks WHERE workspace_id = $1 AND brand_id = $2 AND source_id = $3 AND ($4 = '' OR status = $4)
		  AND ($5::timestamptz IS NULL OR created_at < $5)
		ORDER BY created_at DESC LIMIT $6`, a.WorkspaceID, a.Brand, r.PathValue("id"), status, before, limit+1)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (sourcePostbackView, error) {
		var v sourcePostbackView
		var micros int64
		err := row.Scan(&v.ID, &v.ConversionID, &v.Status, &micros, &v.URL, &v.HTTPStatus, &v.Error, &v.Attempts, &v.SentAt, &v.CreatedAt)
		v.Payout = formatMicros(micros)
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *time.Time
	if len(list) > limit {
		next, list = &list[limit-1].CreatedAt, list[:limit]
	}
	if list == nil {
		list = []sourcePostbackView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": nextCursor(pageBefore, next)})
}
