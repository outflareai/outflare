package api

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// Conversions in by pixel, script and upload (plans/ad-tracker.md, T5b).
//
// A pixel or script on a thank-you page calls the source's URL from the
// browser, with the network's own sub id macro as cid, when a network can't
// send a server postback. An upload (JSON through the API, or a CSV file)
// records conversions in bulk; a row without a click id updates the status
// or payout of a conversion already recorded with its transaction id, as
// networks report approvals and reversals days later.

const (
	maxUploadRows    = 1000
	maxCSVUploadRows = 10000
)

// gif is a transparent 1x1 GIF.
var gif = []byte{0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00, 0x01, 0x00, 0x80, 0x00, 0x00, 0x00, 0x00, 0x00,
	0xff, 0xff, 0xff, 0x21, 0xf9, 0x04, 0x01, 0x00, 0x00, 0x00, 0x00, 0x2c, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00,
	0x00, 0x02, 0x02, 0x44, 0x01, 0x00, 0x3b}

func (s *Server) routesTrackerUploads(mux *http.ServeMux) {
	manage := auth.ScopeAnalyticsManage
	mux.HandleFunc("GET /t/{token}/pixel", s.trackerPixel)
	mux.HandleFunc("GET /t/{token}/pixel.js", s.trackerPixelScript)
	mux.HandleFunc("POST /v1/brands/{brand}/tracker-conversions", s.onBrand(manage, s.uploadTrackerConversions))
	mux.HandleFunc("POST /v1/brands/{brand}/tracker-conversions/csv", s.onBrand(manage, s.uploadTrackerConversionsCSV))
}

// trackerPixel records a conversion from a browser. It always answers with
// the pixel, so a page never shows a broken image; X-Outflare-Outcome says
// what happened. A source with an IP allowlist takes no pixels: browsers
// call from anywhere.
func (s *Server) trackerPixel(w http.ResponseWriter, r *http.Request) {
	outcome, l := s.pixelConversion(r)
	if l != nil {
		l.outcome = outcome
		l.status = http.StatusOK
		defer s.saveTrackerCall(r, l)
	}
	metrics.TrackerConversionRow("pixel", outcome)
	w.Header().Set("Content-Type", "image/gif")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Outflare-Outcome", outcome)
	_, _ = w.Write(gif)
}

// pixelConversion records a pixel's conversion; the call to log is nil
// when its secret names no source.
func (s *Server) pixelConversion(r *http.Request) (string, *trackerCall) {
	ctx := r.Context()
	src, err := s.findPostbackSource(ctx, r.PathValue("token"))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "unknown_source", nil
	case err != nil:
		s.log.Error("tracker pixel", "err", err)
		return "error", nil
	}
	noteWorkspace(r, src.ws)
	l := newTrackerCall(nil, src, "pixel", "GET")
	l.request = r.URL.Query()
	if _, own := s.ownDomain(r, src.ws); !own {
		l.reason = "pixels answer on the brand's own tracking domain"
		return "wrong_domain", l
	}
	if len(src.allow) > 0 {
		l.reason = "a source with an IP allowlist takes no pixels"
		return "ip_refused", l
	}
	if !s.postbackLimit.hit(src.id) {
		return "rate_limited", l
	}
	in, err := readTrackerPostback(r.URL.Query(), src.currency)
	l.clickKey = in.clickKey
	if err != nil {
		l.reason = err.Error()
		return "invalid", l
	}
	res, err := s.recordTrackerConversion(ctx, nil, src, in)
	switch {
	case errors.Is(err, errUnknownClick):
		l.reason = "no click of this brand has that click id"
		return "unknown_click", l
	case errors.Is(err, errExpiredClick):
		l.reason = "the click is older than the source's click expiry"
		return "expired_click", l
	case err != nil:
		s.log.Error("tracker pixel", "err", err, "request_id", RequestID(ctx))
		return "error", l
	}
	l.conversion = res.ConversionID
	return res.Outcome, l
}

// trackerPixelScript loads the pixel with the script's own query, for pages
// that take a script tag rather than an image.
func (s *Server) trackerPixelScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, `(function(){var s=document.currentScript;if(!s)return;var u=s.src.replace(/\/pixel\.js(\?|$)/,"/pixel$1");`+
		`var i=new Image();i.referrerPolicy="no-referrer";i.src=u;})();`)
}

// Uploads.

type conversionUploadRow struct {
	Row          int    `json:"row"`
	Outcome      string `json:"outcome"`
	ConversionID string `json:"conversion_id,omitempty"`
	Error        string `json:"error,omitempty"`
}

type conversionUploadResult struct {
	Accepted  int                   `json:"accepted"`
	Updated   int                   `json:"updated"`
	Duplicate int                   `json:"duplicate"`
	Failed    int                   `json:"failed"`
	Rows      []conversionUploadRow `json:"rows"`
}

// uploadSource finds the brand's active source an upload names.
func (s *Server) uploadSource(ctx context.Context, ws, brand, id string) (postbackSource, error) {
	src := postbackSource{id: id}
	if !validUUID(id) {
		return src, errInvalid("source is the id of one of the brand's postback sources")
	}
	err := s.pool.QueryRow(ctx, `SELECT workspace_id::text, brand_id::text, currency, duplicate_policy, click_expiry_days
		FROM tracker_postback_sources WHERE workspace_id = $1 AND brand_id = $2 AND id = $3 AND status = 'active'`, ws, brand, id).
		Scan(&src.ws, &src.brand, &src.currency, &src.policy, &src.expiryDays)
	if errors.Is(err, pgx.ErrNoRows) {
		return src, errInvalid("source isn't one of the brand's active postback sources")
	}
	return src, err
}

// uploadRows records each row, row by row, and the upload as a whole.
func (s *Server) uploadRows(ctx context.Context, a *access, src postbackSource, via string, rows []url.Values) (conversionUploadResult, error) {
	out := conversionUploadResult{Rows: []conversionUploadRow{}}
	for i, v := range rows {
		row := conversionUploadRow{Row: i + 1}
		in, err := readConversion(v, src.currency, true)
		var inv errInvalid
		switch {
		case errors.As(err, &inv):
			row.Outcome, row.Error, err = "failed", string(inv), nil
		case err != nil:
			return out, err
		case in.clickKey == "":
			row.ConversionID, row.Outcome, err = s.updateConversionByTx(ctx, &a.principal, src, in)
			if errors.As(err, &inv) {
				row.Outcome, row.Error, err = "failed", string(inv), nil
			}
		default:
			var res trackerConversionResult
			res, err = s.recordTrackerConversion(ctx, &a.principal, src, in)
			row.ConversionID, row.Outcome = res.ConversionID, res.Outcome
			switch {
			case errors.Is(err, errUnknownClick):
				row.Outcome, row.Error, err = "failed", "no click of this brand has that click id", nil
			case errors.Is(err, errExpiredClick):
				row.Outcome, row.Error, err = "failed", "the click is older than the source's click expiry", nil
			}
		}
		if err != nil {
			return out, err
		}
		metrics.TrackerConversionRow(via, row.Outcome)
		switch row.Outcome {
		case "accepted":
			out.Accepted++
		case "updated":
			out.Updated++
		case "duplicate":
			out.Duplicate++
		default:
			out.Failed++
		}
		out.Rows = append(out.Rows, row)
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return record(ctx, tx, &a.principal, a.WorkspaceID, "tracker_conversion.uploaded", "postback_source", src.id,
			map[string]any{"via": via, "rows": len(rows), "accepted": out.Accepted, "updated": out.Updated, "duplicate": out.Duplicate, "failed": out.Failed})
	})
	return out, err
}

// updateConversionByTx changes the status or payout of the source's latest
// conversion with the row's transaction id.
func (s *Server) updateConversionByTx(ctx context.Context, p *principal, src postbackSource, in trackerPostbackInput) (string, string, error) {
	if !in.hasStatus && !in.hasPayout {
		return "", "", errInvalid("a row without a click id updates a conversion: give its status or payout")
	}
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `UPDATE tracker_conversions SET status = CASE WHEN $3 THEN $4 ELSE status END,
				payout_micros = CASE WHEN $5 THEN $6 ELSE payout_micros END, updated_at = now()
			WHERE id = (SELECT id FROM tracker_conversions WHERE source_id = $1 AND transaction_id = $2 ORDER BY created_at DESC LIMIT 1)
			RETURNING id::text`, src.id, in.txid, in.hasStatus, in.status, in.hasPayout, in.payoutMicros).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errInvalid("no click id, and no conversion from this source has that transaction id to update")
		}
		if err != nil {
			return err
		}
		if err := enqueueSourcePostback(ctx, tx, src.ws, id); err != nil {
			return err
		}
		hook, err := conversionWebhook(ctx, tx, id)
		if err != nil {
			return err
		}
		return recordWithWebhook(ctx, tx, p, src.ws, "tracker_conversion.updated", "tracker_conversion", id,
			map[string]any{"source_id": src.id, "policy": "upload", "status": in.status}, nil, hook)
	})
	return id, "updated", err
}

// uploadTrackerConversions records up to 1,000 conversions sent as JSON.
func (s *Server) uploadTrackerConversions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Source      string           `json:"source"`
		Conversions []map[string]any `json:"conversions"`
	}
	if !decodeLarge(w, r, &in, 4<<20) {
		return
	}
	if len(in.Conversions) == 0 || len(in.Conversions) > maxUploadRows {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "conversions has 1 to 1,000 rows; send a CSV for more")
		return
	}
	src, err := s.uploadSource(r.Context(), a.WorkspaceID, a.Brand, in.Source)
	if s.allocError(w, r, err) {
		return
	}
	rows := make([]url.Values, len(in.Conversions))
	for i, c := range in.Conversions {
		v := url.Values{}
		for k, x := range c {
			switch t := x.(type) {
			case string:
				v.Set(k, t)
			case float64:
				v.Set(k, strconv.FormatFloat(t, 'f', -1, 64))
			case map[string]any:
				if k == "params" {
					for pk, pv := range t {
						if str, ok := pv.(string); ok {
							v.Set("p"+strings.TrimPrefix(pk, "p"), str)
						}
					}
				}
			}
		}
		rows[i] = v
	}
	res, err := s.uploadRows(r.Context(), a, src, "api", rows)
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// uploadTrackerConversionsCSV records up to 10,000 conversions from a CSV
// file whose first row names the columns (click_id, transaction_id, payout,
// currency, type, status, converted_at, p1 to p20).
func (s *Server) uploadTrackerConversionsCSV(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	src, err := s.uploadSource(r.Context(), a.WorkspaceID, a.Brand, r.URL.Query().Get("source"))
	if s.allocError(w, r, err) {
		return
	}
	cr := csv.NewReader(io.LimitReader(r.Body, 8<<20))
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "the body is a CSV file whose first row names the columns, such as click_id,transaction_id,payout,status")
		return
	}
	for i := range header {
		header[i] = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(header[i], "\ufeff")))
	}
	var rows []url.Values
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("row %d isn't valid CSV", len(rows)+2))
			return
		}
		if len(rows) == maxCSVUploadRows {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "a CSV upload has at most 10,000 rows; split it")
			return
		}
		v := url.Values{}
		for i, cell := range rec {
			if i < len(header) && header[i] != "" {
				v.Set(header[i], cell)
			}
		}
		rows = append(rows, v)
	}
	if len(rows) == 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "the CSV file has no rows under its header")
		return
	}
	res, err := s.uploadRows(r.Context(), a, src, "csv", rows)
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, res)
}
