package api

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adconv"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Ad pixels the website script loads by itself. A site key with
// chatgpt_pixel on gets, with the answer to its events, the Pixel ID of
// the brand's own ChatGPT Ads connection and, for a visitor the brand's
// product has signed in, the same hashed email, phone and id Outflare
// sends with their conversions. The script loads OpenAI's pixel with
// them, so the site carries no pixel code and its developers pass no
// user data. Nothing is hashed in the browser: the script never learns
// an email address.
//
// A site key with meta_pixel or tiktok_pixel on gets the pixel the brand's
// conversions already go to (the target of its conversions for Meta Ads
// or TikTok Ads) and, for each conversion a page's own event can cause,
// the platform's name for it. The script then sends that event to the
// pixel with the id Outflare's server sends the same conversion with, so
// the platform counts it once and matches it on the browser's cookies
// too.

// The ad connectors' ids.
const (
	chatgptAdsConnector = "chatgpt_ads"
	metaAdsConnector    = "meta_ads"
	tiktokAdsConnector  = "tiktok_ads"
)

// adPixel is a pixel the brand's conversions go to, and the conversions
// a page's own event can cause, by the event's catalog name.
type adPixel struct {
	ID     string
	Events map[string]adPixelEvent
}

// adPixelEvent is how the script sends one conversion to the pixel: the
// platform's event, and where its value comes from, as the rule says.
type adPixelEvent struct {
	Type            string   `json:"type"`
	ValueProperty   string   `json:"value_property,omitempty"`
	ValueUnit       string   `json:"value_unit,omitempty"`
	Value           *float64 `json:"value,omitempty"`
	Currency        string   `json:"currency,omitempty"`
	AccountCurrency string   `json:"account_currency,omitempty"`
}

// brandPixels is what a brand's scripts can be told to load.
type brandPixels struct {
	chatgpt      string
	meta, tiktok adPixel
}

// sitePixelTTL is how long a server remembers a brand's Pixel ID between
// looks, so the script's requests don't each open the connection.
const sitePixelTTL = 30 * time.Second

var sitePixelIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

type sitePixelCache struct {
	mu sync.Mutex
	m  map[string]sitePixelCached
}

type sitePixelCached struct {
	at time.Time
	px brandPixels
}

func (c *sitePixelCache) get(brand string) (brandPixels, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	x, ok := c.m[brand]
	if !ok || time.Since(x.at) > sitePixelTTL {
		return brandPixels{}, false
	}
	return x.px, true
}

func (c *sitePixelCache) put(brand string, px brandPixels) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) > 10000 {
		c.m = map[string]sitePixelCached{}
	}
	c.m[brand] = sitePixelCached{time.Now(), px}
}

// conversionPixel is the pixel the brand's conversions for one ad
// platform go to: the target of its oldest conversion that is switched
// on, "" when it sends none. Its events are the conversions for that
// pixel that a page's own event can cause and the script can send the
// same way the server does: one event, no conditions, and a value the
// event itself carries. A conversion with conditions, a predicted value
// or a place in the feed of payments is the server's alone, so the page
// never sends one the server would hold.
func (s *Server) conversionPixel(ctx context.Context, q connectors.Querier, ws, brand, connector string) (adPixel, error) {
	var px adPixel
	if brand == "" {
		return px, nil
	}
	rows, err := q.Query(ctx, `
		SELECT r.target_id, COALESCE(d.event_name, r.event_name), r.event_type, r.value_property, r.value_unit, r.fixed_value_micros, r.currency, a.currency,
			r.feed_role = 'custom' AND NOT r.predicted_value AND NOT r.qualified_only
				AND (r.conversion_id IS NULL OR (d.filters = '[]'::jsonb AND (r.value_property <> '' OR r.fixed_value_micros IS NOT NULL)))
		FROM ad_conversion_rules r
		JOIN ad_accounts a ON a.id = r.ad_account_id AND a.status = 'active'
		LEFT JOIN conversion_definitions d ON d.id = r.conversion_id
		WHERE r.workspace_id = $1 AND r.brand_id = $2 AND a.connector = $3 AND r.enabled
		  AND (r.conversion_id IS NULL OR d.archived_at IS NULL)
		ORDER BY r.created_at, r.id`, ws, brand, connector)
	if err != nil {
		return px, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			target, name string
			ev           adPixelEvent
			fixed        *int64
			plain        bool
		)
		if err := rows.Scan(&target, &name, &ev.Type, &ev.ValueProperty, &ev.ValueUnit, &fixed, &ev.Currency, &ev.AccountCurrency, &plain); err != nil {
			return px, err
		}
		if px.ID == "" {
			if !sitePixelIDRe.MatchString(target) {
				continue
			}
			px.ID, px.Events = target, map[string]adPixelEvent{}
		}
		if _, dup := px.Events[name]; target != px.ID || !plain || dup {
			continue
		}
		if ev.Type == "" {
			// What the server sends for a rule that names no event.
			ev.Type = "Lead"
			if connector == tiktokAdsConnector {
				ev.Type = "CompleteRegistration"
			}
		}
		switch {
		case ev.ValueProperty != "":
		case fixed != nil:
			v := float64(*fixed) / 1e6
			ev.Value, ev.ValueUnit = &v, ""
		default:
			ev.ValueUnit, ev.Currency, ev.AccountCurrency = "", "", ""
		}
		px.Events[name] = ev
	}
	return px, rows.Err()
}

// brandPixelsOf reads what the brand's scripts can load, as stored: it
// calls nobody.
func (s *Server) brandPixelsOf(ctx context.Context, q connectors.Querier, ws, brand string) (brandPixels, error) {
	var px brandPixels
	var err error
	if px.chatgpt, err = s.chatgptPixelID(ctx, q, ws, brand); err != nil {
		return px, err
	}
	if px.meta, err = s.conversionPixel(ctx, q, ws, brand, metaAdsConnector); err != nil {
		return px, err
	}
	px.tiktok, err = s.conversionPixel(ctx, q, ws, brand, tiktokAdsConnector)
	return px, err
}

// chatgptPixelID is the Pixel ID on the brand's ChatGPT Ads connection,
// "" when the brand has no such connection or it names no pixel. It
// reads the connection as stored and calls nobody.
func (s *Server) chatgptPixelID(ctx context.Context, q connectors.Querier, ws, brand string) (string, error) {
	if s.integrations == nil || brand == "" {
		return "", nil
	}
	list, err := s.integrations.List(ctx, q, ws, chatgptAdsConnector)
	if err != nil {
		return "", err
	}
	for _, i := range connectors.ForBrand(list, brand) {
		_, creds, err := s.integrations.Load(ctx, q, ws, i.ID)
		if errors.Is(err, connectors.ErrNotFound) {
			continue
		}
		if err != nil {
			return "", err
		}
		if id := strings.TrimSpace(creds["pixel_id"]); sitePixelIDRe.MatchString(id) {
			return id, nil
		}
	}
	return "", nil
}

// sitePixelsFor is what a key's script is told about ad pixels, nil when
// it loads none.
func (s *Server) sitePixelsFor(ctx context.Context, k siteKeyRow, anon string) map[string]any {
	if !(k.chatgptPixel || k.metaPixel || k.tiktokPixel) || k.brandID == nil {
		return nil
	}
	px, ok := s.pixelCache.get(*k.brandID)
	if !ok {
		var err error
		if px, err = s.brandPixelsOf(ctx, s.pool, k.workspaceID, *k.brandID); err != nil {
			// The events were taken; the pixel waits for the next answer.
			s.log.Warn("site pixel: reading the brand's pixels", "error", err, "request_id", RequestID(ctx))
			return nil
		}
		s.pixelCache.put(*k.brandID, px)
	}
	out := map[string]any{}
	if k.chatgptPixel && px.chatgpt != "" {
		out["chatgpt"] = map[string]any{"id": px.chatgpt}
	}
	if k.metaPixel && px.meta.ID != "" {
		out["meta"] = map[string]any{"id": px.meta.ID, "events": px.meta.Events}
	}
	if k.tiktokPixel && px.tiktok.ID != "" {
		out["tiktok"] = map[string]any{"id": px.tiktok.ID, "events": px.tiktok.Events}
	}
	if len(out) == 0 {
		return nil
	}
	user, err := s.sitePixelUser(ctx, k.workspaceID, *k.brandID, anon)
	if err != nil {
		s.log.Warn("site pixel: reading the visitor", "error", err, "request_id", RequestID(ctx))
		return out
	}
	if user == nil {
		return out
	}
	// Each platform's own names for the same hashes the server sends it.
	if c, ok := out["chatgpt"].(map[string]any); ok {
		u := map[string]string{"external_id_sha256": user.id}
		if user.email != "" {
			u["email_sha256"] = user.email
		}
		if user.phoneBare != "" {
			u["phone_number_sha256"] = user.phoneBare
		}
		c["user"] = u
	}
	if c, ok := out["meta"].(map[string]any); ok {
		u := map[string]string{"external_id": user.id}
		if user.email != "" {
			u["em"] = user.email
		}
		if user.phoneDigits != "" {
			u["ph"] = user.phoneDigits
		}
		c["user"] = u
	}
	if c, ok := out["tiktok"].(map[string]any); ok {
		u := map[string]string{"external_id": user.id}
		if user.email != "" {
			u["email"] = user.email
		}
		if user.phone != "" {
			u["phone_number"] = user.phone
		}
		c["user"] = u
	}
	return out
}

// sitePixelPerson is a signed-in visitor as the pixels know them: the
// SHA-256 of their record id, email and phone number. The phone is
// hashed three ways, as each platform's server sends ask for it: with
// the plus (TikTok), digits only (Meta), and digits without leading
// zeroes (OpenAI).
type sitePixelPerson struct {
	id, email, phone, phoneDigits, phoneBare string
}

// sitePixelUser is the hashed user data for a browser, nil unless the
// brand's own product signed the visitor in on it (an identify the
// site's server signed or sent) and their ad data may be sent. A browser
// that only clicked a link in an email, or whose company was looked up,
// gets none: the email's reader may not be the person it was sent to.
func (s *Server) sitePixelUser(ctx context.Context, ws, brand, anon string) (*sitePixelPerson, error) {
	if anon == "" {
		return nil, nil
	}
	var record string
	err := s.pool.QueryRow(ctx, `SELECT record_id::text FROM site_visitors
		WHERE workspace_id = $1 AND anonymous_id = $2 AND identified_by = 'product' AND record_id IS NOT NULL`, ws, anon).Scan(&record)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	mode, err := adconv.ConsentMode(ctx, s.pool, ws, brand)
	if err != nil {
		return nil, err
	}
	send, _, _, err := adconv.Consent(ctx, s.pool, ws, &record, &anon, mode)
	if err != nil || !send {
		return nil, err
	}
	user := &sitePixelPerson{id: connectors.HashID(record)}
	rows, err := s.pool.Query(ctx, `SELECT namespace, value FROM record_identities
		WHERE workspace_id = $1 AND record_id = $2 AND valid_to IS NULL AND namespace IN ('email', 'phone')
		ORDER BY verified DESC, created_at DESC LIMIT 6`, ws, record)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ns, v string
		if err := rows.Scan(&ns, &v); err != nil {
			return nil, err
		}
		switch {
		case ns == "email" && user.email == "":
			user.email = connectors.HashID(v)
		case ns == "phone" && user.phone == "":
			digits := strings.TrimPrefix(strings.TrimSpace(v), "+")
			if bare := strings.TrimLeft(digits, "0"); bare != "" {
				user.phone, user.phoneDigits, user.phoneBare = connectors.HashID(v), connectors.HashID(digits), connectors.HashID(bare)
			}
		}
	}
	return user, rows.Err()
}
