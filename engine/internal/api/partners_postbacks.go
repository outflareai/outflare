package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/netguard"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Postbacks out: an affiliate who runs their own tracker gives a URL with
// {placeholders}, on the portal or through the team. Each conversion
// credited to them (and each refund of one) calls it once, from a job,
// over a client that only reaches public addresses and never follows a
// redirect. Every call is kept in partner_postback_sends.
//
// Payout export: the payouts as CSV, with each partner's payout details,
// for paying them from the workspace's own account or payout tool.

const jobPartnerPostback = "partners.postback"

// postbackPlaceholders are what a postback URL may carry.
var postbackPlaceholders = []string{"sub_id", "click_id", "conversion_id", "type", "event_name", "amount", "amount_cents",
	"currency", "commission", "commission_cents", "status", "code", "program"}

var placeholderRE = regexp.MustCompile(`\{([a-z_]+)\}`)

// partnerPostbackClient calls partners' trackers. Tests swap it for one
// that reaches their local server.
var partnerPostbackClient = func() *http.Client {
	c := netguard.Client(10 * time.Second)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c
}()

// checkPostbackURL accepts "" (none) or an https URL whose placeholders are
// all known.
func checkPostbackURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if len(raw) > 2000 {
		return errInvalid("postback_url is at most 2,000 characters")
	}
	u, err := url.Parse(placeholderRE.ReplaceAllString(raw, "x"))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errInvalid("postback_url is an https address of your own tracker, such as https://tracker.example/postback?clickid={sub_id}&payout={commission}")
	}
	for _, m := range placeholderRE.FindAllStringSubmatch(raw, -1) {
		if !slices.Contains(postbackPlaceholders, m[1]) {
			return errInvalid("postback_url: {" + m[1] + "} isn't a placeholder; use " + strings.Join(postbackPlaceholders, ", "))
		}
	}
	return nil
}

func trimPtr(p *string) *string {
	if p == nil {
		return nil
	}
	t := strings.TrimSpace(*p)
	return &t
}

// enqueuePartnerPostback queues the partner's postback for a conversion
// credited to them, in the conversion's transaction, when they have one.
func enqueuePartnerPostback(ctx context.Context, tx pgx.Tx, ws, membership, conv string) error {
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO partner_postback_sends (workspace_id, membership_id, conversion_id)
		SELECT $1, m.id, $3 FROM partner_memberships m WHERE m.id = $2 AND m.postback_url <> ''
		ON CONFLICT (conversion_id, membership_id) DO NOTHING RETURNING id::text`, ws, membership, conv).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobPartnerPostback, WorkspaceID: &ws, Payload: map[string]string{"send_id": id},
		MaxAttempts: 5, IdempotencyKey: "partner-postback:" + id})
	return err
}

// postbackURL fills in a partner's template for a conversion.
func postbackURL(tmpl string, values map[string]string) string {
	return placeholderRE.ReplaceAllStringFunc(tmpl, func(m string) string {
		return url.QueryEscape(values[m[1:len(m)-1]])
	})
}

func decimal(cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}

func (s *Server) partnerPostbackJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		SendID string `json:"send_id"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || !validUUID(p.SendID) {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	var tmpl, membership, code, program, typ, event, currency, status string
	var subID, clickID *string
	var conv string
	var amount, commission int64
	err := s.pool.QueryRow(ctx, `SELECT m.postback_url, m.id::text, m.code, p.slug, v.id::text, v.type, v.event_name, v.amount_cents, v.currency,
			COALESCE(NULLIF(f.sub_id, ''), k.sub_id), f.click_id::text,
			COALESCE((SELECT sum(w.amount_cents) FROM partner_rewards w WHERE w.conversion_id = v.id AND w.side = 'partner' AND w.kind IN ('cash', 'adjustment')), 0)::bigint,
			CASE WHEN EXISTS (SELECT 1 FROM partner_rewards w WHERE w.conversion_id = v.id AND w.side = 'partner' AND w.status = 'pending') THEN 'pending' ELSE 'approved' END
		FROM partner_postback_sends o
		JOIN partner_memberships m ON m.id = o.membership_id JOIN partner_programs p ON p.id = m.program_id
		JOIN partner_conversions v ON v.id = o.conversion_id JOIN partner_referrals f ON f.id = v.referral_id
		LEFT JOIN partner_clicks k ON k.id = f.click_id
		WHERE o.workspace_id = $1 AND o.id = $2 AND o.status = 'queued'`, ws, p.SendID).
		Scan(&tmpl, &membership, &code, &program, &conv, &typ, &event, &amount, &currency, &subID, &clickID, &commission, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // sent, given up on, or gone
	}
	if err != nil {
		return err
	}
	if tmpl == "" {
		_, err := s.pool.Exec(ctx, `UPDATE partner_postback_sends SET status = 'failed', error = 'the partner removed their postback URL' WHERE id = $1`, p.SendID)
		return err
	}
	if typ == "refund" {
		status = "reversed"
	}
	values := map[string]string{"sub_id": deref(subID), "click_id": deref(clickID), "conversion_id": conv, "type": typ, "event_name": event,
		"amount": decimal(amount), "amount_cents": strconv.FormatInt(amount, 10), "currency": currency,
		"commission": decimal(commission), "commission_cents": strconv.FormatInt(commission, 10), "status": status, "code": code, "program": program}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, postbackURL(tmpl, values), nil)
	if err != nil {
		return s.postbackDone(ctx, ws, p.SendID, membership, "failed", 0, "the postback URL isn't valid", "postback_invalid", err)
	}
	req.Header.Set("User-Agent", "Outflare-Postback/1")
	resp, err := partnerPostbackClient.Do(req)
	last := job.Attempt >= job.MaxAttempts
	if err != nil {
		if last {
			return s.postbackDone(ctx, ws, p.SendID, membership, "failed", 0, "couldn't reach the tracker", "postback_unreachable", err)
		}
		s.postbackAttempt(ctx, p.SendID, 0, "couldn't reach the tracker")
		metrics.PartnerPostback("retried")
		return err
	}
	resp.Body.Close()
	switch code := resp.StatusCode; {
	case code >= 200 && code < 300:
		return s.postbackDone(ctx, ws, p.SendID, membership, "sent", code, "", "", nil)
	case code == http.StatusTooManyRequests || code >= 500:
		msg := "the tracker answered " + strconv.Itoa(code)
		if last {
			return s.postbackDone(ctx, ws, p.SendID, membership, "failed", code, msg, "postback_unreachable", errors.New(msg))
		}
		s.postbackAttempt(ctx, p.SendID, code, msg)
		metrics.PartnerPostback("retried")
		return errors.New(msg)
	default:
		msg := "the tracker refused it with " + strconv.Itoa(code)
		return s.postbackDone(ctx, ws, p.SendID, membership, "failed", code, msg, "postback_rejected", errors.New(msg))
	}
}

func (s *Server) postbackAttempt(ctx context.Context, id string, httpStatus int, msg string) {
	var hs *int
	if httpStatus > 0 {
		hs = &httpStatus
	}
	if _, err := s.pool.Exec(ctx, `UPDATE partner_postback_sends SET attempts = attempts + 1, http_status = $2, error = $3 WHERE id = $1`, id, hs, msg); err != nil {
		s.log.WarnContext(ctx, "partners: postback attempt not stored", "err", err)
	}
}

// postbackDone stores how a postback ended; code is set for a failure.
func (s *Server) postbackDone(ctx context.Context, ws, id, membership, status string, httpStatus int, msg, code string, cause error) error {
	var hs *int
	if httpStatus > 0 {
		hs = &httpStatus
	}
	if _, err := s.pool.Exec(ctx, `UPDATE partner_postback_sends SET status = $2, attempts = attempts + 1, http_status = $3, error = $4,
		sent_at = CASE WHEN $2 = 'sent' THEN now() END WHERE id = $1`, id, status, hs, msg); err != nil {
		return err
	}
	if status == "sent" {
		metrics.PartnerPostback("sent")
		return nil
	}
	metrics.PartnerPostback(strings.TrimPrefix(code, "postback_"))
	track.Fail(ctx, s.log, track.Event{Name: "partner.postback_failed", WorkspaceID: ws, Surface: "job",
		Props: map[string]any{"partner_id": membership, "http_status": httpStatus}}, code, cause)
	return nil
}

// ---- Who sees the sends ----

type partnerPostbackSend struct {
	ID           string     `json:"id"`
	ConversionID string     `json:"conversion_id"`
	Status       string     `json:"status"`
	HTTPStatus   *int       `json:"http_status"`
	Error        string     `json:"error"`
	Attempts     int        `json:"attempts"`
	SentAt       *time.Time `json:"sent_at"`
	CreatedAt    time.Time  `json:"created_at"`
}

func (s *Server) postbackSends(ctx context.Context, ws, membership string) ([]partnerPostbackSend, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text, conversion_id::text, status, http_status, error, attempts, sent_at, created_at
		FROM partner_postback_sends WHERE workspace_id = $1 AND membership_id = $2 ORDER BY created_at DESC LIMIT 50`, ws, membership)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[partnerPostbackSend])
	if list == nil {
		list = []partnerPostbackSend{}
	}
	return list, err
}

func (s *Server) routesPartnerPostbacks(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/partners/{id}/postbacks", s.inBrand(auth.ScopePartnersRead, s.listPartnerPostbacks))
	mux.HandleFunc("GET /v1/workspaces/{ws}/partner-payouts/export", s.inBrand(auth.ScopePartnersManage, s.exportPartnerPayouts))
	mux.HandleFunc("GET /p/{program}/api/postbacks", s.inPortal(s.portalPostbacks))
}

func (s *Server) listPartnerPostbacks(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findPartner(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	list, err := s.postbackSends(r.Context(), a.WorkspaceID, cur.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) portalPostbacks(w http.ResponseWriter, r *http.Request, c *portalCaller) {
	list, err := s.postbackSends(r.Context(), c.program.ws, c.partner.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// exportPartnerPayouts writes payouts as CSV (due ones unless status says
// otherwise), with each partner's payout details, so they can be paid
// from the workspace's own account or uploaded to a payout tool.
func (s *Server) exportPartnerPayouts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	status, program := q.Get("status"), strings.ToLower(q.Get("program"))
	if status == "" {
		status = "due"
	}
	if !slices.Contains([]string{"due", "paid", "canceled", "all"}, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is due, paid, canceled or all")
		return
	}
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	args := []any{a.WorkspaceID, status, program}
	rows, err := s.pool.Query(ctx, `SELECT o.id::text, p.slug, m.id::text, m.code, COALESCE(rc.name, ''),
			COALESCE((SELECT i.value FROM record_identities i WHERE i.record_id = m.record_id AND i.namespace = 'email' AND i.valid_to IS NULL
				ORDER BY i.verified DESC, i.valid_from LIMIT 1), ''),
			m.payout_method, o.amount_cents, o.currency, o.status, o.reference, o.created_at, o.paid_at,
			COALESCE(m.payout_type, ''), COALESCE(m.payout_token, '')
		FROM partner_payouts o JOIN partner_memberships m ON m.id = o.membership_id JOIN partner_programs p ON p.id = m.program_id
		LEFT JOIN records rc ON rc.id = m.record_id
		WHERE o.workspace_id = $1 AND ($2 = 'all' OR o.status = $2) AND ($3 = '' OR p.id::text = $3 OR p.slug = $3)`+sel.sql("p.brand_id", &args)+`
		ORDER BY o.created_at LIMIT 10000`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	defer rows.Close()
	header := []string{"payout_id", "program", "partner_id", "partner_code", "partner_name", "partner_email", "payout_method", "amount",
		"currency", "status", "reference", "created_at", "paid_at", "payout_type", "payout_token"}
	var out [][]string
	for rows.Next() {
		var id, prog, member, code, name, email, cur, st, ref, typ, token string
		var sealed []byte
		var cents int64
		var created time.Time
		var paid *time.Time
		if err := rows.Scan(&id, &prog, &member, &code, &name, &email, &sealed, &cents, &cur, &st, &ref, &created, &paid, &typ, &token); err != nil {
			s.internal(w, r, err)
			return
		}
		method := ""
		if len(sealed) > 0 {
			if b, err := s.box.Open(sealed, []byte(member)); err == nil {
				method = string(b)
			}
		}
		paidAt := ""
		if paid != nil {
			paidAt = paid.UTC().Format(time.RFC3339)
		}
		out = append(out, []string{id, prog, member, code, name, email, method, decimal(cents), cur, st, ref, created.UTC().Format(time.RFC3339), paidAt, typ, token})
	}
	if err := rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	// Payout details leave Outflare in this file: record who took it.
	if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return record(ctx, tx, &a.principal, a.WorkspaceID, "partner_payouts.exported", "workspace", a.WorkspaceID,
			map[string]any{"status": status, "rows": len(out)})
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="outflare-partner-payouts-`+status+`.csv"`)
	cw := csv.NewWriter(w)
	cw.Write(header) //nolint:errcheck
	for _, row := range out {
		for i, v := range row {
			row[i] = csvSafe(v)
		}
		cw.Write(row) //nolint:errcheck
	}
	cw.Flush()
}
