package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/track"
)

// conversionLog watches one inbound conversion call (the API or a postback
// URL) and keeps what it sent and what Outflare answered, so the team can
// see on Partners → Log why a sale wasn't credited.
type conversionLog struct {
	http.ResponseWriter
	source  string
	program string // the postback's program
	in      *conversionInput
	res     *conversionResult
	status  int
	body    bytes.Buffer
	start   time.Time
}

func newConversionLog(w http.ResponseWriter, source string) *conversionLog {
	return &conversionLog{ResponseWriter: w, source: source, start: time.Now()}
}

func (l *conversionLog) WriteHeader(code int) {
	if l.status == 0 {
		l.status = code
	}
	l.ResponseWriter.WriteHeader(code)
}

func (l *conversionLog) Write(b []byte) (int, error) {
	if l.status == 0 {
		l.status = http.StatusOK
	}
	if l.body.Len() < 32<<10 {
		l.body.Write(b)
	}
	return l.ResponseWriter.Write(b)
}

// emailHash is how the log finds a customer by email without keeping it.
func emailHash(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return ""
	}
	h := sha256.Sum256([]byte(email))
	return hex.EncodeToString(h[:])
}

// logRequest is what the call sent, without empty fields and without the
// email, which only says that one was sent.
func logRequest(in *conversionInput) map[string]any {
	out := map[string]any{}
	if in == nil {
		return out
	}
	c := *in
	email := c.Email != ""
	c.Email = ""
	b, _ := json.Marshal(c)
	_ = json.Unmarshal(b, &out)
	for k, v := range out {
		if v == nil || v == "" || v == false {
			delete(out, k)
		}
	}
	if email {
		out["email"] = "(sent)"
	}
	return out
}

// save writes the call to the log. It runs after the answer is written and
// never changes it: a failure is logged and recorded as an event.
func (s *Server) saveConversionLog(r *http.Request, ws string, l *conversionLog) {
	ctx := context.WithoutCancel(r.Context())
	// A call that names no program is logged under the brand whose route
	// it came in on, so that brand's log shows it.
	var routeBrand *string
	if a := accessFrom(r.Context()); a != nil && a.Brand != "" {
		routeBrand = &a.Brand
	}
	outcome, reason := "rejected", ""
	var program, record, conversion *string
	if l.program != "" {
		program = &l.program
	}
	switch {
	case l.status >= 500:
		outcome = "error"
	case l.res != nil:
		outcome, reason = "credited", l.res.Reason
		switch {
		case l.res.Duplicate:
			outcome = "duplicate"
		case !l.res.Credited:
			outcome = "not_credited"
		}
		if l.res.programID != "" {
			program = &l.res.programID
		}
		if l.res.recordID != "" {
			record = &l.res.recordID
		}
		if l.res.Conversion != nil {
			conversion = &l.res.Conversion.ID
			if program == nil {
				program = &l.res.Conversion.ProgramID
			}
		}
	}
	var resp map[string]any
	if json.Unmarshal(l.body.Bytes(), &resp) != nil {
		resp = map[string]any{}
	}
	if c, ok := resp["conversion"].(map[string]any); ok {
		// The log keeps no one's name or email, like the request above.
		delete(c, "customer_name")
		delete(c, "customer_email")
		delete(c, "partner_name")
	}
	if outcome == "rejected" || outcome == "error" {
		if e, ok := resp["error"].(map[string]any); ok {
			reason, _ = e["code"].(string)
		}
	}
	typ, email := "", ""
	if l.in != nil {
		typ, email = cutUTF8(l.in.Type, 40), l.in.Email
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO partner_conversion_log (workspace_id, brand_id, program_id, record_id, conversion_id, source, type,
			request, email_hash, status, outcome, reason, response, duration_ms, request_id)
		VALUES ($1, COALESCE((SELECT brand_id FROM partner_programs WHERE workspace_id = $1 AND id = $2), $15::uuid), $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		ws, program, record, conversion, l.source, typ, logRequest(l.in), emailHash(email), l.status, outcome, cutUTF8(reason, 60), resp,
		time.Since(l.start).Milliseconds(), cutUTF8(RequestID(ctx), 100), routeBrand)
	if err != nil {
		track.Fail(ctx, s.log, track.Event{Name: "partner_conversion.log_failed", WorkspaceID: ws, Surface: "system", ActorType: "system"}, "internal", err)
	}
}

type partnerConversionLogView struct {
	ID           string         `json:"id"`
	ProgramID    *string        `json:"program_id"`
	RecordID     *string        `json:"record_id"`
	ConversionID *string        `json:"conversion_id"`
	Source       string         `json:"source"`
	Type         string         `json:"type"`
	Request      map[string]any `json:"request"`
	Status       int            `json:"status"`
	Outcome      string         `json:"outcome"`
	Reason       string         `json:"reason"`
	Response     map[string]any `json:"response"`
	DurationMS   int            `json:"duration_ms"`
	RequestID    string         `json:"request_id"`
	CreatedAt    time.Time      `json:"created_at"`
}

var conversionLogOutcomes = []string{"credited", "not_credited", "duplicate", "rejected", "error"}

// listPartnerConversionLog is the inbound conversion log, newest first.
// q finds a call by the external id, customer id, ref, promo code or email
// it sent, or by its request id.
func (s *Server) listPartnerConversionLog(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	program, source, outcome, search := strings.ToLower(q.Get("program")), q.Get("source"), q.Get("outcome"), strings.TrimSpace(q.Get("q"))
	switch {
	case source != "" && source != "api" && source != "postback":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "source is api or postback")
		return
	case outcome != "" && !slices.Contains(conversionLogOutcomes, outcome):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "outcome is credited, not_credited, duplicate, rejected or error")
		return
	case len(search) > 200:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "q is at most 200 characters")
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
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	hash := ""
	if strings.Contains(search, "@") {
		hash = emailHash(search)
	}
	args := []any{a.WorkspaceID, program, source, outcome, search, hash, before, limit + 1}
	rows, err := s.pool.Query(r.Context(), `SELECT l.id::text, l.program_id::text, l.record_id::text, l.conversion_id::text, l.source, l.type,
			l.request, l.status, l.outcome, l.reason, l.response, l.duration_ms, l.request_id, l.created_at
		FROM partner_conversion_log l LEFT JOIN partner_programs p ON p.id = l.program_id
		WHERE l.workspace_id = $1 AND ($2 = '' OR p.id::text = $2 OR p.slug = $2) AND ($3 = '' OR l.source = $3) AND ($4 = '' OR l.outcome = $4)
		  AND ($5 = '' OR l.request->>'external_id' = $5 OR l.request->>'customer_id' = $5 OR l.request->>'refund_of' = $5
		       OR lower(l.request->>'ref') = lower($5) OR lower(l.request->>'promo_code') = lower($5) OR l.request_id = $5 OR ($6 <> '' AND l.email_hash = $6))
		  AND ($7::timestamptz IS NULL OR l.created_at < $7)`+sel.sql("l.brand_id", &args)+`
		ORDER BY l.created_at DESC LIMIT $8`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (partnerConversionLogView, error) {
		var v partnerConversionLogView
		err := row.Scan(&v.ID, &v.ProgramID, &v.RecordID, &v.ConversionID, &v.Source, &v.Type, &v.Request, &v.Status, &v.Outcome,
			&v.Reason, &v.Response, &v.DurationMS, &v.RequestID, &v.CreatedAt)
		return v, err
	})
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
		list = []partnerConversionLogView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": nextCursor(pageBefore, next)})
}

// PrunePartnerConversionLog deletes logged conversion calls older than 30
// days.
func (s *Server) PrunePartnerConversionLog(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM partner_conversion_log WHERE created_at < now() - interval '30 days'`)
	return tag.RowsAffected(), err
}
