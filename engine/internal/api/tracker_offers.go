package api

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/affiliatenetworks"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/utm"
)

// Offers (plans/ad-tracker.md, T6a). An offer is a page a brand sends
// tracker clicks to, usually on an affiliate network: its URL (with the
// click id in the network's sub id), its payout, a daily cap on clicks or
// conversions with a fallback offer, and the network it pays through (a
// postback source, which can follow one of the affiliate-network templates
// in integrations/affiliate-networks). A link's destination can be an
// offer; a conversion on a click that went to an offer gets the offer's
// payout when the postback sends none (or always, when it is fixed).

const maxOffers = 1000

func (s *Server) routesTrackerOffers(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeAnalyticsManage
	mux.HandleFunc("GET /v1/brands/{brand}/affiliate-network-templates", s.onBrand(read, s.listAffiliateNetworkTemplates))
	mux.HandleFunc("GET /v1/brands/{brand}/offers", s.onBrand(read, s.listOffers))
	mux.HandleFunc("POST /v1/brands/{brand}/offers", s.onBrand(manage, s.createOffer))
	mux.HandleFunc("GET /v1/brands/{brand}/offers/{id}", s.onBrand(read, s.getOffer))
	mux.HandleFunc("PATCH /v1/brands/{brand}/offers/{id}", s.onBrand(manage, s.updateOffer))
}

func (s *Server) listAffiliateNetworkTemplates(w http.ResponseWriter, r *http.Request) {
	list, err := affiliatenetworks.All()
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "kinds": affiliatenetworks.Kinds})
}

// The payout a conversion gets.

// offerTerms are what an offer says about its conversions.
type offerTerms struct {
	mode, currency, conversionType string
	micros                         int64
	byCountry                      map[string]int64
}

// payout is the offer's payout for a click from country.
func (o offerTerms) payout(country string) int64 {
	if p, ok := o.byCountry[country]; ok && country != "" {
		return p
	}
	return o.micros
}

// applyOffer gives a conversion its offer's terms: the offer's payout (by
// the click's country) when the postback sends none or the offer's payout
// is fixed, and its conversion type when the postback names none.
func (s *Server) applyOffer(ctx context.Context, ws, offer, country string, in *trackerPostbackInput) error {
	var o offerTerms
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT payout_mode, payout_micros, currency, country_payouts, conversion_type FROM tracker_offers
		WHERE workspace_id = $1 AND id = $2`, ws, offer).Scan(&o.mode, &o.micros, &o.currency, &raw, &o.conversionType)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &o.byCountry); err != nil {
		return err
	}
	if o.mode == "fixed" || !in.hasPayout {
		in.payoutMicros, in.currency = o.payout(country), o.currency
	}
	if !in.hasType && o.conversionType != "" {
		in.typ = o.conversionType
	}
	return nil
}

// Offers.

type trackerOfferView struct {
	ID      string `json:"id"`
	BrandID string `json:"brand_id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	// SourceID is the postback source (the affiliate network) it pays
	// through; nil for none.
	SourceID   *string `json:"source_id"`
	PayoutMode string  `json:"payout_mode"`
	Payout     string  `json:"payout"`
	Currency   string  `json:"currency"`
	// CountryPayouts are payouts by the click's country, before Payout.
	CountryPayouts  map[string]string `json:"country_payouts"`
	ConversionType  string            `json:"conversion_type"`
	DailyCap        *int              `json:"daily_cap"`
	CapKind         string            `json:"cap_kind"`
	FallbackOfferID *string           `json:"fallback_offer_id"`
	Status          string            `json:"status"`
	// MissingClickID says the URL doesn't carry the click id and its
	// network adds none, so a postback can't name the click.
	MissingClickID bool `json:"missing_click_id"`
	// TrackingURL is the URL a click goes to, with the click id its
	// network takes added.
	TrackingURL      string `json:"tracking_url"`
	ClicksToday      int64  `json:"clicks_today"`
	ConversionsToday int64  `json:"conversions_today"`
	// CapReached says today's cap is used up, so clicks go to the
	// fallback offer, or elsewhere.
	CapReached bool `json:"cap_reached"`
	// Destinations is how many link destinations send to it.
	Destinations int       `json:"destinations"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

const offerSelect = `
	SELECT o.id::text, o.brand_id::text, o.name, o.url, o.source_id::text, o.payout_mode, o.payout_micros, o.currency, o.country_payouts,
	       o.conversion_type, o.daily_cap, o.cap_kind, o.fallback_offer_id::text, o.status, COALESCE(p.click_id_param, ''),
	       COALESCE(d.clicks, 0), COALESCE(d.conversions, 0),
	       (SELECT count(*) FROM tracking_link_destinations x WHERE x.offer_id = o.id AND x.removed_at IS NULL)::int, o.created_at, o.updated_at
	FROM tracker_offers o
	LEFT JOIN tracker_postback_sources p ON p.id = o.source_id
	LEFT JOIN tracker_offer_days d ON d.offer_id = o.id AND d.day = $3::date
	WHERE o.workspace_id = $1 AND o.brand_id = $2`

func scanOffer(row pgx.Row) (trackerOfferView, error) {
	var v trackerOfferView
	var micros int64
	var raw []byte
	var param string
	err := row.Scan(&v.ID, &v.BrandID, &v.Name, &v.URL, &v.SourceID, &v.PayoutMode, &micros, &v.Currency, &raw, &v.ConversionType, &v.DailyCap,
		&v.CapKind, &v.FallbackOfferID, &v.Status, &param, &v.ClicksToday, &v.ConversionsToday, &v.Destinations, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return v, err
	}
	v.Payout = formatMicros(micros)
	var by map[string]int64
	if err := json.Unmarshal(raw, &by); err != nil {
		return v, err
	}
	v.CountryPayouts = map[string]string{}
	for c, p := range by {
		v.CountryPayouts[c] = formatMicros(p)
	}
	v.TrackingURL = offerURL(v.URL, param)
	v.MissingClickID = !strings.Contains(v.TrackingURL, "{click_id")
	if v.DailyCap != nil {
		used := v.ConversionsToday
		if v.CapKind == "clicks" {
			used = v.ClicksToday
		}
		v.CapReached = used >= int64(*v.DailyCap)
	}
	return v, nil
}

func formatMicros(m int64) string { return strconv.FormatFloat(float64(m)/1e6, 'f', -1, 64) }

func (s *Server) listOffers(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	status := r.URL.Query().Get("status")
	switch status {
	case "":
		status = "active"
	case "active", "archived", "all":
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is active, archived or all")
		return
	}
	source := r.URL.Query().Get("source")
	if source != "" && !validUUID(source) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "source is a postback source's id")
		return
	}
	today, _ := s.brandToday(r.Context())
	rows, err := s.pool.Query(r.Context(), offerSelect+` AND ($4 = 'all' OR o.status = $4) AND ($5 = '' OR o.source_id = $5::uuid) ORDER BY lower(o.name)`,
		a.WorkspaceID, a.Brand, today, status, source)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (trackerOfferView, error) { return scanOffer(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []trackerOfferView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) writeOffer(w http.ResponseWriter, r *http.Request, status int, id string) {
	a := accessFrom(r.Context())
	var v trackerOfferView
	err := errNotFound
	if validUUID(id) {
		today, _ := s.brandToday(r.Context())
		v, err = scanOffer(s.pool.QueryRow(r.Context(), offerSelect+` AND o.id = $4`, a.WorkspaceID, a.Brand, today, id))
	}
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "offer not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, status, v)
}

func (s *Server) getOffer(w http.ResponseWriter, r *http.Request) {
	s.writeOffer(w, r, http.StatusOK, r.PathValue("id"))
}

type trackerOfferInput struct {
	Name *string `json:"name"`
	URL  *string `json:"url"`
	// SourceID is a postback source's id; "" for none.
	SourceID       *string         `json:"source_id"`
	PayoutMode     *string         `json:"payout_mode"`
	Payout         any             `json:"payout"`
	Currency       *string         `json:"currency"`
	CountryPayouts *map[string]any `json:"country_payouts"`
	ConversionType *string         `json:"conversion_type"`
	DailyCap       json.RawMessage `json:"daily_cap"`
	CapKind        *string         `json:"cap_kind"`
	Fallback       *string         `json:"fallback_offer_id"`
	Status         *string         `json:"status"`
	// What check read: the daily cap (nil left out, *nil for none), the
	// payout and the country payouts, in millionths.
	cap       **int
	payout    *int64
	byCountry *map[string]int64
}

// payoutMicros reads a payout given as a number or a string, in the
// currency's main unit.
func payoutMicros(what string, v any) (int64, error) {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case string:
		var err error
		if f, err = strconv.ParseFloat(strings.TrimSpace(x), 64); err != nil {
			return 0, errInvalid(what + " is a number in the currency's main unit, such as 12.50")
		}
	default:
		return 0, errInvalid(what + " is a number in the currency's main unit, such as 12.50")
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f >= 1e9 {
		return 0, errInvalid(what + " is 0 or more, under a billion")
	}
	return int64(math.Round(f * 1e6)), nil
}

func (in *trackerOfferInput) check() error {
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len(n) > 120 {
			return errInvalid("name is 1 to 120 characters")
		}
		in.Name = &n
	}
	if in.URL != nil {
		u := strings.TrimSpace(*in.URL)
		if err := checkDestinationTokens("url", u); err != nil {
			return err
		}
		dest, err := utm.Destination(u)
		if err != nil {
			return errInvalid("url: " + err.Error())
		}
		in.URL = &dest
	}
	if in.SourceID != nil && *in.SourceID != "" && !validUUID(*in.SourceID) {
		return errInvalid("source_id is one of the brand's postback sources, or \"\" for none")
	}
	if in.PayoutMode != nil && *in.PayoutMode != "postback" && *in.PayoutMode != "fixed" {
		return errInvalid("payout_mode is postback (what the postback says, else the offer's payout) or fixed (always the offer's payout)")
	}
	if in.Payout != nil {
		p, err := payoutMicros("payout", in.Payout)
		if err != nil {
			return err
		}
		in.payout = &p
	}
	if in.Currency != nil {
		c := strings.ToUpper(strings.TrimSpace(*in.Currency))
		if !currencyCodeRe.MatchString(c) {
			return errInvalid("currency is a three-letter code such as USD")
		}
		in.Currency = &c
	}
	if in.CountryPayouts != nil {
		if len(*in.CountryPayouts) > 250 {
			return errInvalid("country_payouts has at most 250 countries")
		}
		by := map[string]int64{}
		for c, v := range *in.CountryPayouts {
			cc := strings.ToUpper(strings.TrimSpace(c))
			if !countryRe.MatchString(cc) {
				return errInvalid("country_payouts is keyed by two-letter country codes, such as US")
			}
			p, err := payoutMicros("country_payouts."+cc, v)
			if err != nil {
				return err
			}
			by[cc] = p
		}
		in.byCountry = &by
	}
	if in.ConversionType != nil {
		t := strings.ToLower(strings.TrimSpace(*in.ConversionType))
		if t != "" && !conversionTypeRe.MatchString(t) {
			return errInvalid("conversion_type is 1 to 40 lowercase letters, digits, dots, dashes or underscores, such as lead or sale; \"\" leaves the postback's")
		}
		in.ConversionType = &t
	}
	if len(in.DailyCap) > 0 {
		var c *int
		if err := json.Unmarshal(in.DailyCap, &c); err != nil || (c != nil && (*c < 0 || *c > 100_000_000)) {
			return errInvalid("daily_cap is 0 (stopped) or more, or null for no cap")
		}
		in.cap = &c
	}
	if in.CapKind != nil && *in.CapKind != "clicks" && *in.CapKind != "conversions" {
		return errInvalid("cap_kind is clicks or conversions")
	}
	if in.Fallback != nil && *in.Fallback != "" && !validUUID(*in.Fallback) {
		return errInvalid("fallback_offer_id is another of the brand's offers, or \"\" for none")
	}
	if in.Status != nil && *in.Status != "active" && *in.Status != "archived" {
		return errInvalid("status is active or archived")
	}
	return nil
}

// checkOfferLinks checks an offer's network and fallback are the brand's,
// inside tx; id is the offer's own ("" for a new one).
func checkOfferLinks(ctx context.Context, tx pgx.Tx, ws, brand, id string, in *trackerOfferInput) error {
	if in.SourceID != nil && *in.SourceID != "" {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tracker_postback_sources WHERE workspace_id = $1 AND brand_id = $2 AND id = $3)`,
			ws, brand, *in.SourceID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errInvalid("source_id isn't one of the brand's postback sources")
		}
	}
	if in.Fallback != nil && *in.Fallback != "" {
		if *in.Fallback == id {
			return errInvalid("fallback_offer_id is another offer; an offer can't be its own fallback")
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tracker_offers WHERE workspace_id = $1 AND brand_id = $2 AND id = $3 AND status = 'active')`,
			ws, brand, *in.Fallback).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errInvalid("fallback_offer_id isn't one of the brand's active offers")
		}
	}
	return nil
}

func (s *Server) createOffer(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in trackerOfferInput
	if !decode(w, r, &in) {
		return
	}
	switch {
	case in.Name == nil || in.URL == nil:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name and url are required")
		return
	case in.Status != nil:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a new offer is active; archive it with PATCH")
		return
	}
	if s.allocError(w, r, in.check()) {
		return
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tracker_offers WHERE workspace_id = $1 AND brand_id = $2`, a.WorkspaceID, a.Brand).Scan(&n); err != nil {
			return err
		}
		if n >= maxOffers {
			return errInvalid("a brand has at most 1,000 offers; archive old ones")
		}
		if err := checkOfferLinks(ctx, tx, a.WorkspaceID, a.Brand, "", &in); err != nil {
			return err
		}
		var cap *int
		if in.cap != nil {
			cap = *in.cap
		}
		by := map[string]int64{}
		if in.byCountry != nil {
			by = *in.byCountry
		}
		byJSON, err := json.Marshal(by)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO tracker_offers (workspace_id, brand_id, source_id, name, url, payout_mode, payout_micros, currency,
				country_payouts, conversion_type, daily_cap, cap_kind, fallback_offer_id, created_by)
			VALUES ($1, $2, NULLIF($3, '')::uuid, $4, $5, COALESCE($6, 'postback'), COALESCE($7, 0), COALESCE($8, 'USD'), $9, COALESCE($10, ''), $11,
				COALESCE($12, 'conversions'), NULLIF($13, '')::uuid, $14) RETURNING id::text`,
			a.WorkspaceID, a.Brand, deref(in.SourceID), *in.Name, *in.URL, in.PayoutMode, in.payout, in.Currency, byJSON, in.ConversionType, cap,
			in.CapKind, deref(in.Fallback), nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "tracker_offer.created", "tracker_offer", id,
			map[string]any{"brand_id": a.Brand, "source_id": nullIfEmpty(deref(in.SourceID)), "payout_mode": deref(in.PayoutMode),
				"countries": len(by), "capped": cap != nil, "fallback": deref(in.Fallback) != ""})
	})
	if s.offerSaveError(w, r, err) {
		return
	}
	s.writeOffer(w, r, http.StatusCreated, id)
}

func (s *Server) offerSaveError(w http.ResponseWriter, r *http.Request, err error) bool {
	var pe *pgconn.PgError
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "offer not found")
		return true
	case errors.As(err, &pe) && pe.Code == "23505":
		writeError(w, r, http.StatusConflict, "conflict", "the brand already has an offer with that name")
		return true
	}
	return s.allocError(w, r, err)
}

// updateOffer changes an offer. A new URL goes to every destination that
// sends to it at once; archiving it is refused while one does.
func (s *Server) updateOffer(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var in trackerOfferInput
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
			var used int
			err := tx.QueryRow(ctx, `SELECT status, (SELECT count(*) FROM tracking_link_destinations d WHERE d.offer_id = o.id AND d.removed_at IS NULL)
				FROM tracker_offers o WHERE workspace_id = $1 AND brand_id = $2 AND id = $3 FOR UPDATE`, a.WorkspaceID, a.Brand, id).Scan(&was, &used)
			if errors.Is(err, pgx.ErrNoRows) {
				return errNotFound
			}
			if err != nil {
				return err
			}
			if in.Status != nil && *in.Status == "archived" && was != "archived" && used > 0 {
				return errInvalid("the offer is a destination of " + strconv.Itoa(used) + " link(s); take it off them before archiving it")
			}
			if err := checkOfferLinks(ctx, tx, a.WorkspaceID, a.Brand, id, &in); err != nil {
				return err
			}
			var byJSON []byte
			if in.byCountry != nil {
				if byJSON, err = json.Marshal(*in.byCountry); err != nil {
					return err
				}
			}
			setCap := in.cap != nil
			var cap *int
			if setCap {
				cap = *in.cap
			}
			if _, err := tx.Exec(ctx, `UPDATE tracker_offers SET name = COALESCE($3, name), url = COALESCE($4, url),
					source_id = CASE WHEN $5::text IS NULL THEN source_id ELSE NULLIF($5, '')::uuid END,
					payout_mode = COALESCE($6, payout_mode), payout_micros = COALESCE($7, payout_micros), currency = COALESCE($8, currency),
					country_payouts = COALESCE($9, country_payouts), conversion_type = COALESCE($10, conversion_type),
					daily_cap = CASE WHEN $11 THEN $12 ELSE daily_cap END, cap_kind = COALESCE($13, cap_kind),
					fallback_offer_id = CASE WHEN $14::text IS NULL THEN fallback_offer_id ELSE NULLIF($14, '')::uuid END,
					status = COALESCE($15, status), updated_at = now()
				WHERE workspace_id = $1 AND id = $2`,
				a.WorkspaceID, id, in.Name, in.URL, in.SourceID, in.PayoutMode, in.payout, in.Currency, byJSON, in.ConversionType,
				setCap, cap, in.CapKind, in.Fallback, in.Status); err != nil {
				return err
			}
			// Destinations keep the offer's URL, so the version before
			// still sends their clicks there.
			if in.URL != nil {
				if _, err := tx.Exec(ctx, `UPDATE tracking_link_destinations SET url = $3, updated_at = now() WHERE workspace_id = $1 AND offer_id = $2`,
					a.WorkspaceID, id, *in.URL); err != nil {
					return err
				}
			}
			action := "tracker_offer.updated"
			if in.Status != nil && *in.Status != was {
				action = map[string]string{"archived": "tracker_offer.archived", "active": "tracker_offer.restored"}[*in.Status]
			}
			return record(ctx, tx, &a.principal, a.WorkspaceID, action, "tracker_offer", id,
				map[string]any{"brand_id": a.Brand, "destinations": used, "url": in.URL != nil, "countries": in.byCountry != nil})
		})
	}
	if s.offerSaveError(w, r, err) {
		return
	}
	// Its URL, cap or fallback may route clicks differently now.
	s.redirects.clear()
	s.writeOffer(w, r, http.StatusOK, id)
}
