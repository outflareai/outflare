package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/affiliatenetworks"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/fraud"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/tracking"
)

// Tracker conversions by click id (plans/ad-tracker.md, T5a). A brand's
// postback source (an affiliate network, an offer's tracker, its backend)
// calls a secret URL on the brand's own tracking domain when a conversion
// happens, naming the redirect click it came from (the click key the
// redirect added as ofclk, passed on as the network's sub id). Each call
// writes the click ledger (tracker_conversions), which needs no person: a
// conversion on a third-party offer has none. When the click's visit knew
// the person, it also goes on their timeline.

const (
	maxPostbackSources = 100
	maxPostbackParams  = 20
)

var (
	conversionTypeRe  = regexp.MustCompile(`^[a-z0-9_.-]{1,40}$`)
	duplicatePolicies = []string{"ignore", "new", "update", "add"}
)

func (s *Server) routesTrackerConversions(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeAnalyticsManage
	mux.HandleFunc("GET /t/{token}", s.trackerPostback)
	mux.HandleFunc("POST /t/{token}", s.trackerPostback)
	mux.HandleFunc("GET /v1/brands/{brand}/postback-sources", s.onBrand(read, s.listPostbackSources))
	mux.HandleFunc("POST /v1/brands/{brand}/postback-sources", s.onBrand(manage, s.createPostbackSource))
	mux.HandleFunc("GET /v1/brands/{brand}/postback-sources/{id}", s.onBrand(read, s.getPostbackSource))
	mux.HandleFunc("PATCH /v1/brands/{brand}/postback-sources/{id}", s.onBrand(manage, s.updatePostbackSource))
	mux.HandleFunc("POST /v1/brands/{brand}/postback-sources/{id}/rotate", s.onBrand(manage, s.rotatePostbackSource))
	mux.HandleFunc("GET /v1/brands/{brand}/tracker-conversions", s.onBrand(read, s.listTrackerConversions))
}

// The postback.

type postbackSource struct {
	id, ws, brand, currency, policy string
	expiryDays                      int
	allow                           []netip.Prefix
}

// trackerPostback records a conversion. Public: the secret in the path is
// the authority, and it answers only on the brand's own tracking domain.
func (s *Server) trackerPostback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	src, err := s.findPostbackSource(ctx, r.PathValue("token"))
	if errors.Is(err, pgx.ErrNoRows) {
		metrics.TrackerPostback("unknown_source")
		writeError(w, r, http.StatusNotFound, "not_found", "this postback URL is not valid; copy it again from the source")
		return
	}
	if err != nil {
		metrics.TrackerPostback("error")
		s.internal(w, r, err)
		return
	}
	noteWorkspace(r, src.ws)
	// From here every call is in the source's conversion log.
	l := newTrackerCall(w, src, "postback", r.Method)
	w = l
	defer s.saveTrackerCall(r, l)
	fail := func(outcome string, status int, code, msg string) {
		l.outcome, l.reason = outcome, msg
		metrics.TrackerPostback(outcome)
		writeError(w, r, status, code, msg)
	}
	if _, own := s.ownDomain(r, src.ws); !own {
		fail("wrong_domain", http.StatusNotFound, "not_found", "postbacks answer on the brand's own tracking domain; copy the postback URL again")
		return
	}
	if !s.postbackLimit.hit(src.id) {
		fail("rate_limited", http.StatusTooManyRequests, "rate_limited", "too many postbacks for this source this minute; send them more slowly")
		return
	}
	if len(src.allow) > 0 {
		ip, err := parseClientAddr(clientIP(r))
		ok := false
		for _, p := range src.allow {
			ok = ok || (err == nil && p.Contains(ip.Unmap()))
		}
		if !ok {
			fail("ip_refused", http.StatusForbidden, "forbidden", "this address isn't on the source's IP allowlist")
			return
		}
	}
	vals, err := postbackValues(r)
	if err != nil {
		fail("invalid", http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	l.request = vals
	in, err := readTrackerPostback(vals, src.currency)
	l.clickKey = in.clickKey
	if err != nil {
		fail("invalid", http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	res, err := s.recordTrackerConversion(ctx, nil, src, in)
	switch {
	case errors.Is(err, errUnknownClick):
		fail("unknown_click", http.StatusNotFound, "not_found", "no click of this brand has that click id; pass the ofclk the redirect added")
		return
	case errors.Is(err, errExpiredClick):
		fail("expired_click", http.StatusNotFound, "not_found", "the click is older than the source's click expiry ("+strconv.Itoa(src.expiryDays)+" days)")
		return
	case err != nil:
		l.outcome = "error"
		metrics.TrackerPostback("error")
		s.internal(w, r, err)
		return
	}
	l.outcome, l.conversion = res.Outcome, res.ConversionID
	metrics.TrackerPostback(res.Outcome)
	writeJSON(w, http.StatusOK, res)
}

// findPostbackSource finds an active source by its secret; pgx.ErrNoRows
// for none.
func (s *Server) findPostbackSource(ctx context.Context, token string) (postbackSource, error) {
	var src postbackSource
	var allow []string
	if !strings.HasPrefix(token, auth.PrefixPostback) {
		return src, pgx.ErrNoRows
	}
	err := s.pool.QueryRow(ctx, `SELECT id::text, workspace_id::text, brand_id::text, currency, duplicate_policy, click_expiry_days, ip_allowlist
		FROM tracker_postback_sources WHERE token_hash = $1 AND status = 'active'`, auth.HashToken(token)).
		Scan(&src.id, &src.ws, &src.brand, &src.currency, &src.policy, &src.expiryDays, &allow)
	for _, a := range allow {
		if p, err := parsePrefix(a); err == nil {
			src.allow = append(src.allow, p)
		}
	}
	return src, err
}

// postbackValues reads a postback's parameters from its query and, for a
// POST, its form or JSON body.
func postbackValues(r *http.Request) (url.Values, error) {
	vals := r.URL.Query()
	if r.Method != http.MethodPost {
		return vals, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		return nil, errors.New("could not read the body")
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			return nil, errors.New("the body is not a JSON object")
		}
		for k, v := range m {
			switch t := v.(type) {
			case string:
				vals.Set(k, t)
			case float64:
				vals.Set(k, strconv.FormatFloat(t, 'f', -1, 64))
			}
		}
		return vals, nil
	}
	if form, err := url.ParseQuery(string(body)); err == nil {
		for k, v := range form {
			vals[k] = v
		}
	}
	return vals, nil
}

type trackerPostbackInput struct {
	clickKey, txid, typ, status, currency string
	payoutMicros                          int64
	params                                map[string]string
	at                                    time.Time
	// Which of status and payout the caller gave, for updating a
	// conversion by its transaction id alone.
	hasStatus, hasPayout bool
	// hasType says the caller named the conversion type.
	hasType bool
}

// readTrackerPostback reads a postback, accepting the names Voluum,
// RedTrack and ClickFlare postbacks use.
func readTrackerPostback(v url.Values, currency string) (trackerPostbackInput, error) {
	return readConversion(v, currency, false)
}

// readConversion reads a conversion's fields. An upload may leave out the
// click id when its transaction id names a conversion already recorded
// (orphan).
func readConversion(v url.Values, currency string, orphan bool) (trackerPostbackInput, error) {
	get := func(names ...string) string {
		for _, n := range names {
			if x := strings.TrimSpace(v.Get(n)); x != "" {
				return x
			}
		}
		return ""
	}
	in := trackerPostbackInput{
		clickKey: strings.ToLower(get("cid", "clickid", "click_id", "ofclk", "subid", "sub_id")),
		txid:     get("txid", "transaction_id", "orderid", "order_id", "tid"),
		typ:      strings.ToLower(get("type", "et", "ct", "event")),
		status:   strings.ToLower(get("status")),
		currency: strings.ToUpper(get("currency", "payout_currency", "cur")),
		params:   map[string]string{},
		at:       time.Now().UTC(),
	}
	switch {
	case in.clickKey == "" && orphan && in.txid != "":
	case in.clickKey == "":
		return in, errInvalid("cid is the click id: the ofclk the redirect added, passed on as the network's sub id")
	case !clickKeyRe.MatchString(in.clickKey):
		return in, errInvalid("cid isn't a click id of Outflare's: it is 16 letters and digits, the ofclk the redirect added")
	}
	if len(in.txid) > 200 {
		return in, errInvalid("txid is at most 200 characters")
	}
	in.hasType = in.typ != ""
	if in.typ == "" {
		in.typ = "conversion"
	}
	if !conversionTypeRe.MatchString(in.typ) {
		return in, errInvalid("type is 1 to 40 lowercase letters, digits, dots, dashes or underscores, such as lead or sale")
	}
	in.hasStatus = in.status != ""
	switch in.status {
	case "", "approved", "approve", "a", "1", "confirmed":
		in.status = "approved"
	case "pending", "p", "hold":
		in.status = "pending"
	case "declined", "decline", "rejected", "reject", "denied", "d", "0":
		in.status = "declined"
	default:
		return in, errInvalid("status is approved, pending or declined")
	}
	if in.currency == "" {
		in.currency = currency
	}
	if !currencyCodeRe.MatchString(in.currency) {
		return in, errInvalid("currency is a three-letter code such as USD")
	}
	if p := get("payout", "sum", "revenue", "amount"); p != "" {
		f, err := strconv.ParseFloat(p, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f >= 1e9 {
			return in, errInvalid("payout is a number in the currency's main unit, such as 12.50")
		}
		in.payoutMicros, in.hasPayout = int64(math.Round(f*1e6)), true
	}
	if t := get("converted_at", "date"); t != "" {
		var at time.Time
		var err error
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", time.DateOnly} {
			if at, err = time.Parse(layout, t); err == nil {
				break
			}
		}
		if err != nil || at.After(time.Now().Add(time.Hour)) {
			return in, errInvalid("converted_at is a time such as 2026-11-10T09:30:00Z, and not in the future")
		}
		in.at = at.UTC()
	}
	for i := 1; i <= maxPostbackParams; i++ {
		n := strconv.Itoa(i)
		if x := get("p"+n, "param"+n); x != "" {
			in.params[n] = cutUTF8(x, maxFieldLen)
		}
	}
	return in, nil
}

var (
	errUnknownClick = errors.New("unknown click")
	errExpiredClick = errors.New("expired click")
)

type trackerConversionResult struct {
	// Outcome is accepted, duplicate (ignored: the transaction id was seen
	// before) or updated (a repeated transaction id updated the conversion
	// or added to its payout).
	Outcome      string `json:"outcome"`
	ConversionID string `json:"conversion_id"`
}

type postbackClick struct {
	id                             int64
	key, brand, country            string
	campaign, link, session, offer *string
	at                             time.Time
	// fraud are the click's fraud reasons, which its conversions inherit.
	fraud []string
}

// findPostbackClick finds the click a postback names, writing this
// server's waiting clicks first if it isn't written yet (a conversion can
// come seconds after its click).
func (s *Server) findPostbackClick(ctx context.Context, ws, key string) (postbackClick, error) {
	var c postbackClick
	lookup := func() error {
		return s.pool.QueryRow(ctx, `SELECT k.id, k.click_key, COALESCE(k.brand_id::text, ''), l.campaign_id::text, k.link_id::text, k.session_id::text, k.clicked_at,
				k.offer_id::text, k.country, k.fraud_reasons
			FROM tracking_link_clicks k LEFT JOIN tracking_links l ON l.id = k.link_id
			WHERE k.workspace_id = $1 AND k.click_key = $2`, ws, key).Scan(&c.id, &c.key, &c.brand, &c.campaign, &c.link, &c.session, &c.at, &c.offer, &c.country, &c.fraud)
	}
	err := lookup()
	if errors.Is(err, pgx.ErrNoRows) {
		if ferr := s.FlushClicks(ctx); ferr == nil {
			err = lookup()
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return c, errUnknownClick
	}
	return c, err
}

// recordTrackerConversion writes a postback's conversion, following the
// source's duplicate policy for a transaction id seen before.
func (s *Server) recordTrackerConversion(ctx context.Context, p *principal, src postbackSource, in trackerPostbackInput) (trackerConversionResult, error) {
	var res trackerConversionResult
	click, err := s.findPostbackClick(ctx, src.ws, in.clickKey)
	if err != nil {
		return res, err
	}
	if click.brand != src.brand {
		return res, errUnknownClick
	}
	if in.at.Sub(click.at) > time.Duration(src.expiryDays)*24*time.Hour {
		return res, errExpiredClick
	}
	// The click's offer sets the payout the postback leaves out (or every
	// payout, when it is fixed) and the conversion type.
	if click.offer != nil {
		if err := s.applyOffer(ctx, src.ws, *click.offer, click.country, &in); err != nil {
			return res, err
		}
	}
	txid := in.txid
	if txid == "" {
		txid = click.key + ":" + in.typ
	}
	var offerTotal int64 = -1
	var offerDay string
	// First-party fraud signals (B7a): the click's, and a conversion too
	// soon after it. Flagged, never refused.
	reasons := fraud.ConversionReasons(click.fraud, click.at, in.at)
	params, err := json.Marshal(in.params)
	if err != nil {
		return res, err
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// One postback at a time for a transaction id, so a repeat sent
		// twice at once follows the policy once.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, src.id+"|"+txid); err != nil {
			return err
		}
		var prev string
		err := tx.QueryRow(ctx, `SELECT id::text FROM tracker_conversions WHERE source_id = $1 AND transaction_id = $2 ORDER BY created_at DESC LIMIT 1`,
			src.id, txid).Scan(&prev)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if prev != "" && src.policy != "new" {
			res.ConversionID = prev
			switch src.policy {
			case "ignore":
				res.Outcome = "duplicate"
				_, err := tx.Exec(ctx, `UPDATE tracker_conversions SET postbacks = postbacks + 1 WHERE id = $1`, prev)
				return err
			case "update":
				_, err = tx.Exec(ctx, `UPDATE tracker_conversions SET status = $2, payout_micros = $3, currency = $4, params = $5, type = $6,
					postbacks = postbacks + 1, updated_at = now() WHERE id = $1`, prev, in.status, in.payoutMicros, in.currency, params, in.typ)
			case "add":
				_, err = tx.Exec(ctx, `UPDATE tracker_conversions SET payout_micros = payout_micros + $2, postbacks = postbacks + 1, updated_at = now()
					WHERE id = $1`, prev, in.payoutMicros)
			}
			if err != nil {
				return err
			}
			res.Outcome = "updated"
			if err := enqueueSourcePostback(ctx, tx, src.ws, prev); err != nil {
				return err
			}
			hook, err := conversionWebhook(ctx, tx, prev)
			if err != nil {
				return err
			}
			return recordWithWebhook(ctx, tx, p, src.ws, "tracker_conversion.updated", "tracker_conversion", prev,
				map[string]any{"source_id": src.id, "policy": src.policy, "status": in.status, "type": in.typ}, nil, hook)
		}
		var rec *string
		if click.session != nil {
			if err := tx.QueryRow(ctx, `SELECT record_id::text FROM site_sessions WHERE workspace_id = $1 AND id = $2`, src.ws, *click.session).Scan(&rec); err != nil &&
				!errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO tracker_conversions (workspace_id, brand_id, source_id, click_id, click_key, campaign_id, link_id, clicked_at,
				transaction_id, type, status, payout_micros, currency, params, record_id, converted_at, offer_id, fraud_reasons)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, (SELECT id FROM tracker_offers WHERE id = $17::uuid), $18)
			RETURNING id::text`,
			src.ws, src.brand, src.id, click.id, click.key, click.campaign, click.link, click.at, txid, in.typ, in.status, in.payoutMicros, in.currency,
			params, rec, in.at, click.offer, reasons).Scan(&res.ConversionID); err != nil {
			return err
		}
		res.Outcome = "accepted"
		// A conversion counts against its offer's daily cap, unless declined.
		// It counts on the brand's day (B4a), as the offer's clicks do.
		if click.offer != nil && in.status != "declined" {
			var tz string
			if err := tx.QueryRow(ctx, `SELECT timezone FROM brands WHERE id = $1`, src.brand).Scan(&tz); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			offerDay = capDay(in.at, zone(tz))
			err := tx.QueryRow(ctx, `INSERT INTO tracker_offer_days (offer_id, workspace_id, brand_id, day, conversions)
				SELECT id, workspace_id, brand_id, $2::date, 1 FROM tracker_offers WHERE id = $1
				ON CONFLICT (offer_id, day) DO UPDATE SET conversions = tracker_offer_days.conversions + 1
				RETURNING conversions`, *click.offer, offerDay).Scan(&offerTotal)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if rec != nil {
			props := map[string]any{"type": in.typ, "status": in.status, "payout_minor": in.payoutMicros / 10_000, "currency": in.currency,
				"source_id": src.id, "conversion_id": res.ConversionID}
			if click.campaign != nil {
				props["campaign_id"] = *click.campaign
			}
			if _, err := customers.Record(ctx, tx, src.ws, customers.Event{RecordID: *rec, Name: "tracker.converted", Properties: props,
				Source: "tracker", OccurredAt: in.at, MessageID: "tracker:" + res.ConversionID, RequestID: RequestID(ctx)}); err != nil {
				return err
			}
		}
		// A converting click whose address its brand's IP lookups kept is
		// looked up now (B7c); the postback below waits for it.
		if _, err := tx.Exec(ctx, `UPDATE tracker_click_addresses SET look_up = true, next_at = now()
			WHERE workspace_id = $1 AND click_key = $2 AND NOT look_up`, src.ws, click.key); err != nil {
			return err
		}
		if err := enqueueSourcePostback(ctx, tx, src.ws, res.ConversionID); err != nil {
			return err
		}
		hook, err := conversionWebhook(ctx, tx, res.ConversionID)
		if err != nil {
			return err
		}
		return recordWithWebhook(ctx, tx, p, src.ws, "tracker_conversion.created", "tracker_conversion", res.ConversionID,
			map[string]any{"source_id": src.id, "campaign_id": click.campaign, "offer_id": click.offer, "type": in.typ, "status": in.status,
				"person": rec != nil}, nil, hook)
	})
	if err == nil && res.Outcome == "accepted" && slices.Contains(reasons, fraud.FastConversion) {
		metrics.TrackerFraudFlag("conversion", fraud.FastConversion)
	}
	if err == nil && offerTotal >= 0 {
		s.caps.seed(conversionCapKey(*click.offer), offerDay, offerTotal)
	}
	return res, err
}

// conversionWebhook is what the tracker_conversion webhooks deliver: the
// conversion as it is now.
func conversionWebhook(ctx context.Context, tx pgx.Tx, id string) (map[string]any, error) {
	var source, key, txid, typ, status, currency string
	var campaign, link, rec, offer *string
	var reasons []string
	var micros int64
	var postbacks int
	var at, clicked time.Time
	if err := tx.QueryRow(ctx, `SELECT source_id::text, click_key, campaign_id::text, link_id::text, transaction_id, type, status, payout_micros, currency,
			record_id::text, postbacks, converted_at, clicked_at, offer_id::text, fraud_reasons FROM tracker_conversions WHERE id = $1`, id).
		Scan(&source, &key, &campaign, &link, &txid, &typ, &status, &micros, &currency, &rec, &postbacks, &at, &clicked, &offer, &reasons); err != nil {
		return nil, err
	}
	return map[string]any{"conversion_id": id, "source_id": source, "click_key": key, "campaign_id": campaign, "link_id": link,
		"transaction_id": txid, "type": typ, "status": status, "payout": strconv.FormatFloat(float64(micros)/1e6, 'f', -1, 64), "currency": currency,
		"record_id": rec, "postbacks": postbacks, "converted_at": at, "clicked_at": clicked, "offer_id": offer, "fraud_reasons": reasons}, nil
}

// Postback sources.

type postbackSourceView struct {
	ID              string   `json:"id"`
	BrandID         string   `json:"brand_id"`
	Name            string   `json:"name"`
	TokenHint       string   `json:"token_hint"`
	IPAllowlist     []string `json:"ip_allowlist"`
	DuplicatePolicy string   `json:"duplicate_policy"`
	ClickExpiryDays int      `json:"click_expiry_days"`
	Currency        string   `json:"currency"`
	Status          string   `json:"status"`
	// Network is the affiliate-network template it follows ("" for a
	// custom source), and ClickIDParam the parameter in its offer links
	// that carries the click id, added to its offers' URLs ("" for none).
	Network      string `json:"network"`
	ClickIDParam string `json:"click_id_param"`
	// PostbackQuery is the postback URL's query, in the network's macros:
	// add it to the postback URL the source was given.
	PostbackQuery string `json:"postback_query"`
	// Conversions is how many conversions it has recorded.
	Conversions int       `json:"conversions"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	// PostbackURL is the URL to give the network, with the secret: shown
	// when the source is made or its secret rotated, never again.
	PostbackURL *string `json:"postback_url,omitempty"`
	// NeedsDomain says the brand has no verified tracking domain, where
	// postbacks answer.
	NeedsDomain bool `json:"needs_domain"`
}

const postbackSourceSelect = `
	SELECT p.id::text, p.brand_id::text, p.name, p.token_hint, p.ip_allowlist, p.duplicate_policy, p.click_expiry_days, p.currency, p.status,
	       p.network, p.click_id_param, (SELECT count(*) FROM tracker_conversions c WHERE c.source_id = p.id)::int, p.created_at, p.updated_at
	FROM tracker_postback_sources p WHERE p.workspace_id = $1 AND p.brand_id = $2`

func scanPostbackSource(row pgx.Row) (postbackSourceView, error) {
	var v postbackSourceView
	err := row.Scan(&v.ID, &v.BrandID, &v.Name, &v.TokenHint, &v.IPAllowlist, &v.DuplicatePolicy, &v.ClickExpiryDays, &v.Currency, &v.Status,
		&v.Network, &v.ClickIDParam, &v.Conversions, &v.CreatedAt, &v.UpdatedAt)
	var tpl *affiliatenetworks.Template
	if t, ok := affiliatenetworks.Get(v.Network); ok {
		tpl = &t
	}
	v.PostbackQuery = tpl.Query()
	if v.IPAllowlist == nil {
		v.IPAllowlist = []string{}
	}
	return v, err
}

func (s *Server) listPostbackSources(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), postbackSourceSelect+` ORDER BY lower(p.name)`, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (postbackSourceView, error) { return scanPostbackSource(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []postbackSourceView{}
	}
	needs, err := s.brandNeedsDomain(r.Context(), a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for i := range list {
		list[i].NeedsDomain = needs
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) brandNeedsDomain(ctx context.Context, ws, brand string) (bool, error) {
	has, err := s.brandHasOwnDomain(ctx, ws, &brand)
	return !has, err
}

func (s *Server) writePostbackSource(w http.ResponseWriter, r *http.Request, status int, id, token string) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	v, err := scanPostbackSource(s.pool.QueryRow(ctx, postbackSourceSelect+` AND p.id = $3`, a.WorkspaceID, a.Brand, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "postback source not found")
		return
	}
	if err == nil {
		v.NeedsDomain, err = s.brandNeedsDomain(ctx, a.WorkspaceID, a.Brand)
	}
	if err == nil && token != "" {
		var base string
		base, err = tracking.BaseURL(ctx, s.pool, a.WorkspaceID, &a.Brand, s.publicURL)
		u := base + "/t/" + token + "?" + v.PostbackQuery
		v.PostbackURL = &u
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, status, v)
}

func (s *Server) getPostbackSource(w http.ResponseWriter, r *http.Request) {
	if !validUUID(r.PathValue("id")) {
		writeError(w, r, http.StatusNotFound, "not_found", "postback source not found")
		return
	}
	s.writePostbackSource(w, r, http.StatusOK, r.PathValue("id"), "")
}

type postbackSourceInput struct {
	Name            *string   `json:"name"`
	IPAllowlist     *[]string `json:"ip_allowlist"`
	DuplicatePolicy *string   `json:"duplicate_policy"`
	ClickExpiryDays *int      `json:"click_expiry_days"`
	Currency        *string   `json:"currency"`
	Status          *string   `json:"status"`
	Network         *string   `json:"network"`
	ClickIDParam    *string   `json:"click_id_param"`
}

func (in *postbackSourceInput) check() error {
	if in.Network != nil && *in.Network != "" {
		t, ok := affiliatenetworks.Get(*in.Network)
		if !ok {
			return errInvalid("network is an affiliate-network template's id (GET .../affiliate-network-templates), or \"\" for a custom source")
		}
		// A source made from a template takes its click id parameter,
		// unless it names its own.
		if in.ClickIDParam == nil {
			in.ClickIDParam = &t.ClickIDParam
		}
	}
	if in.ClickIDParam != nil {
		p := strings.TrimSpace(*in.ClickIDParam)
		if p != "" {
			if err := affiliatenetworks.CheckParam("click_id_param", p); err != nil {
				return errInvalid(err.Error())
			}
		}
		in.ClickIDParam = &p
	}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len(n) > 120 {
			return errInvalid("name is 1 to 120 characters")
		}
		in.Name = &n
	}
	if in.IPAllowlist != nil {
		if len(*in.IPAllowlist) > 50 {
			return errInvalid("ip_allowlist has at most 50 addresses or ranges")
		}
		for i, a := range *in.IPAllowlist {
			a = strings.TrimSpace(a)
			if _, err := parsePrefix(a); err != nil {
				return errInvalid("ip_allowlist[" + strconv.Itoa(i) + "] is an IP address or range, such as 203.0.113.0/24")
			}
			(*in.IPAllowlist)[i] = a
		}
	}
	if in.DuplicatePolicy != nil && !containsStr(duplicatePolicies, *in.DuplicatePolicy) {
		return errInvalid("duplicate_policy is ignore, new, update or add")
	}
	if in.ClickExpiryDays != nil && (*in.ClickExpiryDays < 1 || *in.ClickExpiryDays > 365) {
		return errInvalid("click_expiry_days is 1 to 365")
	}
	if in.Currency != nil {
		c := strings.ToUpper(strings.TrimSpace(*in.Currency))
		if !currencyCodeRe.MatchString(c) {
			return errInvalid("currency is a three-letter code such as USD")
		}
		in.Currency = &c
	}
	if in.Status != nil && *in.Status != "active" && *in.Status != "archived" {
		return errInvalid("status is active or archived")
	}
	return nil
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (s *Server) createPostbackSource(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in postbackSourceInput
	if !decode(w, r, &in) {
		return
	}
	if in.Name == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required, such as the network's name")
		return
	}
	if in.Status != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a new source is active; archive it with PATCH")
		return
	}
	if s.allocError(w, r, in.check()) {
		return
	}
	ctx := r.Context()
	secret, hash := auth.NewToken(auth.PrefixPostback)
	hint := secret[:len(auth.PrefixPostback)+4] + "…"
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tracker_postback_sources WHERE workspace_id = $1 AND brand_id = $2`, a.WorkspaceID, a.Brand).Scan(&n); err != nil {
			return err
		}
		if n >= maxPostbackSources {
			return errInvalid("a brand has at most 100 postback sources; archive old ones")
		}
		if err := tx.QueryRow(ctx, `INSERT INTO tracker_postback_sources (workspace_id, brand_id, name, token_hash, token_hint, ip_allowlist,
				duplicate_policy, click_expiry_days, currency, created_by, network, click_id_param)
			VALUES ($1, $2, $3, $4, $5, COALESCE($6, '{}'::text[]), COALESCE($7, 'ignore'), COALESCE($8, 30), COALESCE($9, 'USD'), $10,
				COALESCE($11, ''), COALESCE($12, '')) RETURNING id::text`,
			a.WorkspaceID, a.Brand, *in.Name, hash, hint, in.IPAllowlist, in.DuplicatePolicy, in.ClickExpiryDays, in.Currency, nullIfEmpty(a.ActorID),
			in.Network, in.ClickIDParam).Scan(&id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "postback_source.created", "postback_source", id,
			map[string]any{"brand_id": a.Brand, "policy": deref(in.DuplicatePolicy), "allowlist": in.IPAllowlist != nil && len(*in.IPAllowlist) > 0,
				"network": deref(in.Network)})
	})
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		writeError(w, r, http.StatusConflict, "conflict", "the brand already has a postback source with that name")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	s.writePostbackSource(w, r, http.StatusCreated, id, secret)
}

func (s *Server) updatePostbackSource(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var in postbackSourceInput
	if !decode(w, r, &in) {
		return
	}
	if s.allocError(w, r, in.check()) {
		return
	}
	ctx := r.Context()
	err := errNotFound
	if validUUID(id) {
		err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			var was string
			err := tx.QueryRow(ctx, `SELECT status FROM tracker_postback_sources WHERE workspace_id = $1 AND brand_id = $2 AND id = $3 FOR UPDATE`,
				a.WorkspaceID, a.Brand, id).Scan(&was)
			if errors.Is(err, pgx.ErrNoRows) {
				return errNotFound
			}
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE tracker_postback_sources SET name = COALESCE($3, name), ip_allowlist = COALESCE($4, ip_allowlist),
					duplicate_policy = COALESCE($5, duplicate_policy), click_expiry_days = COALESCE($6, click_expiry_days),
					currency = COALESCE($7, currency), status = COALESCE($8, status), network = COALESCE($9, network),
					click_id_param = COALESCE($10, click_id_param), updated_at = now()
				WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, id, in.Name, in.IPAllowlist, in.DuplicatePolicy, in.ClickExpiryDays, in.Currency, in.Status,
				in.Network, in.ClickIDParam); err != nil {
				return err
			}
			action := "postback_source.updated"
			if in.Status != nil && *in.Status != was {
				action = map[string]string{"archived": "postback_source.archived", "active": "postback_source.restored"}[*in.Status]
			}
			return record(ctx, tx, &a.principal, a.WorkspaceID, action, "postback_source", id, map[string]any{"brand_id": a.Brand})
		})
	}
	var pe *pgconn.PgError
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "postback source not found")
		return
	case errors.As(err, &pe) && pe.Code == "23505":
		writeError(w, r, http.StatusConflict, "conflict", "the brand already has a postback source with that name")
		return
	case s.allocError(w, r, err):
		return
	}
	// Its click id parameter goes on its offers' URLs.
	s.redirects.clear()
	s.writePostbackSource(w, r, http.StatusOK, id, "")
}

// rotatePostbackSource gives a source a new secret; the old postback URL
// stops working at once.
func (s *Server) rotatePostbackSource(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	ctx := r.Context()
	secret, hash := auth.NewToken(auth.PrefixPostback)
	hint := secret[:len(auth.PrefixPostback)+4] + "…"
	err := errNotFound
	if validUUID(id) {
		err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `UPDATE tracker_postback_sources SET token_hash = $4, token_hint = $5, updated_at = now()
				WHERE workspace_id = $1 AND brand_id = $2 AND id = $3`, a.WorkspaceID, a.Brand, id, hash, hint)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return errNotFound
			}
			return record(ctx, tx, &a.principal, a.WorkspaceID, "postback_source.rotated", "postback_source", id, map[string]any{"brand_id": a.Brand})
		})
	}
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "postback source not found")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	s.writePostbackSource(w, r, http.StatusOK, id, secret)
}

// The ledger.

type trackerConversionView struct {
	ID         string  `json:"id"`
	SourceID   string  `json:"source_id"`
	ClickKey   string  `json:"click_key"`
	CampaignID *string `json:"campaign_id"`
	LinkID     *string `json:"link_id"`
	OfferID    *string `json:"offer_id"`
	// FraudReasons are why it is suspect (its click's, and fast_conversion);
	// empty when it isn't.
	FraudReasons  []string          `json:"fraud_reasons"`
	ClickedAt     time.Time         `json:"clicked_at"`
	TransactionID string            `json:"transaction_id"`
	Type          string            `json:"type"`
	Status        string            `json:"status"`
	Payout        string            `json:"payout"`
	Currency      string            `json:"currency"`
	Params        map[string]string `json:"params"`
	RecordID      *string           `json:"record_id"`
	Postbacks     int               `json:"postbacks"`
	ConvertedAt   time.Time         `json:"converted_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

// listTrackerConversions lists the ledger, newest first, 100 a page.
func (s *Server) listTrackerConversions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	source, campaign, status, before, offer, suspect := q.Get("source"), q.Get("campaign"), q.Get("status"), q.Get("before"), q.Get("offer"), q.Get("suspect")
	switch {
	case suspect != "" && suspect != "true" && suspect != "false":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "suspect is true (only flagged conversions) or false (only unflagged ones)")
		return
	case offer != "" && !validUUID(offer):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "offer is an offer's id")
		return
	case source != "" && !validUUID(source):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "source is a postback source's id")
		return
	case campaign != "" && !validUUID(campaign):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "campaign is a campaign's id")
		return
	case status != "" && status != "approved" && status != "pending" && status != "declined":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is approved, pending or declined")
		return
	}
	var cursor *time.Time
	if before != "" {
		t, err := time.Parse(time.RFC3339Nano, before)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "before is a time such as 2026-11-10T09:00:00Z, the converted_at of the last one you have")
			return
		}
		cursor = &t
	}
	rows, err := s.pool.Query(r.Context(), `SELECT id::text, source_id::text, click_key, campaign_id::text, link_id::text, offer_id::text, fraud_reasons, clicked_at,
			transaction_id, type, status, payout_micros, currency, params, record_id::text, postbacks, converted_at, updated_at
		FROM tracker_conversions WHERE workspace_id = $1 AND brand_id = $2
		  AND ($3 = '' OR source_id = $3::uuid) AND ($4 = '' OR campaign_id = $4::uuid) AND ($5 = '' OR status = $5)
		  AND ($6::timestamptz IS NULL OR converted_at < $6) AND ($7 = '' OR offer_id = $7::uuid)
		  AND ($8 = '' OR (cardinality(fraud_reasons) > 0) = ($8 = 'true'))
		ORDER BY converted_at DESC LIMIT 100`, a.WorkspaceID, a.Brand, source, campaign, status, cursor, offer, suspect)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (trackerConversionView, error) {
		var v trackerConversionView
		var micros int64
		var params []byte
		err := row.Scan(&v.ID, &v.SourceID, &v.ClickKey, &v.CampaignID, &v.LinkID, &v.OfferID, &v.FraudReasons, &v.ClickedAt, &v.TransactionID, &v.Type, &v.Status,
			&micros, &v.Currency, &params, &v.RecordID, &v.Postbacks, &v.ConvertedAt, &v.UpdatedAt)
		v.Payout = strconv.FormatFloat(float64(micros)/1e6, 'f', -1, 64)
		if err == nil {
			err = json.Unmarshal(params, &v.Params)
		}
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []trackerConversionView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}
