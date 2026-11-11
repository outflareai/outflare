package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The tracker conversion log (plans/ad-tracker.md, T5c): every postback and
// pixel call a brand's sources made, with what they sent, what Outflare
// answered and why, kept 30 days, so a brand can see why a network's
// conversion wasn't recorded. A call whose secret names no source has no
// brand to show it to and isn't kept.

// trackerCall watches one postback or pixel call.
type trackerCall struct {
	http.ResponseWriter
	src                  postbackSource
	via, method          string
	request              url.Values
	clickKey, conversion string
	outcome, reason      string
	status               int
	start                time.Time
	body                 bytes.Buffer
}

func newTrackerCall(w http.ResponseWriter, src postbackSource, via, method string) *trackerCall {
	return &trackerCall{ResponseWriter: w, src: src, via: via, method: method, start: time.Now()}
}

func (l *trackerCall) WriteHeader(code int) {
	if l.status == 0 {
		l.status = code
	}
	l.ResponseWriter.WriteHeader(code)
}

func (l *trackerCall) Write(b []byte) (int, error) {
	if l.status == 0 {
		l.status = http.StatusOK
	}
	return l.ResponseWriter.Write(b)
}

// requestMap is what the call sent: at most 40 parameters, each value cut
// to 200 characters.
func requestMap(v url.Values) map[string]string {
	out := map[string]string{}
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if len(out) == 40 {
			break
		}
		out[cutUTF8(k, 60)] = cutUTF8(v.Get(k), 200)
	}
	return out
}

// saveTrackerCall writes the call to the log after it was answered; a
// failure is logged and recorded as an event, never changing the answer.
func (s *Server) saveTrackerCall(r *http.Request, l *trackerCall) {
	ctx := context.WithoutCancel(r.Context())
	if l.outcome == "" {
		l.outcome = "error"
	}
	if l.status == 0 {
		l.status = http.StatusInternalServerError
	}
	req, _ := json.Marshal(requestMap(l.request))
	method := l.method
	if method != "POST" {
		method = "GET"
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO tracker_conversion_log (workspace_id, brand_id, source_id, conversion_id, via, method, request, click_key,
			status, outcome, reason, duration_ms, request_id)
		VALUES ($1, $2, $3, NULLIF($4, '')::uuid, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		l.src.ws, l.src.brand, l.src.id, l.conversion, l.via, method, req, cutUTF8(l.clickKey, 32), l.status, cutUTF8(l.outcome, 40),
		cutUTF8(l.reason, 300), time.Since(l.start).Milliseconds(), cutUTF8(RequestID(ctx), 100))
	if err != nil {
		track.Fail(ctx, s.log, track.Event{Name: "tracker_conversion.log_failed", WorkspaceID: l.src.ws, Surface: "system", ActorType: "system"}, "internal", err)
	}
}

// PruneTrackerConversionLog deletes logged calls older than 30 days.
func (s *Server) PruneTrackerConversionLog(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM tracker_conversion_log WHERE created_at < now() - interval '30 days'`)
	return tag.RowsAffected(), err
}

func (s *Server) routesTrackerConversionLog(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/tracker-conversion-log", s.onBrand(auth.ScopeAnalyticsRead, s.listTrackerConversionLog))
}

type trackerCallView struct {
	ID           string            `json:"id"`
	SourceID     string            `json:"source_id"`
	ConversionID *string           `json:"conversion_id"`
	Via          string            `json:"via"`
	Method       string            `json:"method"`
	Request      map[string]string `json:"request"`
	ClickKey     string            `json:"click_key"`
	Status       int               `json:"status"`
	Outcome      string            `json:"outcome"`
	Reason       string            `json:"reason"`
	DurationMS   int               `json:"duration_ms"`
	RequestID    string            `json:"request_id"`
	CreatedAt    time.Time         `json:"created_at"`
}

// listTrackerConversionLog is the log, newest first.
func (s *Server) listTrackerConversionLog(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	source, outcome, via, click := q.Get("source"), q.Get("outcome"), q.Get("via"), q.Get("click_id")
	switch {
	case source != "" && !validUUID(source):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "source is a postback source's id")
		return
	case via != "" && via != "postback" && via != "pixel":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "via is postback or pixel")
		return
	case len(outcome) > 40 || len(click) > 32:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "outcome or click_id is too long")
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
	rows, err := s.pool.Query(r.Context(), `SELECT id::text, source_id::text, conversion_id::text, via, method, request, click_key, status, outcome, reason,
			duration_ms, request_id, created_at
		FROM tracker_conversion_log WHERE workspace_id = $1 AND brand_id = $2
		  AND ($3 = '' OR source_id = $3::uuid) AND ($4 = '' OR outcome = $4) AND ($5 = '' OR via = $5) AND ($6 = '' OR click_key = lower($6))
		  AND ($7::timestamptz IS NULL OR created_at < $7)
		ORDER BY created_at DESC LIMIT $8`, a.WorkspaceID, a.Brand, source, outcome, via, click, before, limit+1)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (trackerCallView, error) {
		var v trackerCallView
		var req []byte
		err := row.Scan(&v.ID, &v.SourceID, &v.ConversionID, &v.Via, &v.Method, &req, &v.ClickKey, &v.Status, &v.Outcome, &v.Reason,
			&v.DurationMS, &v.RequestID, &v.CreatedAt)
		if err == nil {
			err = json.Unmarshal(req, &v.Request)
		}
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
		list = []trackerCallView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": nextCursor(pageBefore, next)})
}
