// Package utm is the one tagging scheme for links a workspace shares, so a
// visit can be credited to a channel, source and campaign however it
// arrives. Every module that makes links (sequences, broadcasts, social
// posts, partner links, ad final URLs) and the campaign link builder use
// it, so the tags read the same everywhere.
//
// A campaign fixes channel, source, medium and the utm_campaign slug, and
// has a short code that links carry as ofc. The code names the campaign
// exactly even when someone edits or drops the UTM tags, so attribution
// prefers it.
package utm

import (
	"context"
	"crypto/rand"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Channel is a marketing channel. Spend entries use the same ids, so cost
// and credit line up per channel.
type Channel struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Medium is the utm_medium every link in the channel gets.
	Medium string `json:"medium"`
	// Sources are the usual utm_source values, first the most common. Any
	// other source is allowed.
	Sources []string `json:"sources"`
}

// Channels a campaign can be in, in display order. Direct traffic has no
// links, so it is not one.
var Channels = []Channel{
	{ID: "paid_ads", Label: "Paid ads", Medium: "cpc", Sources: []string{"google", "meta", "linkedin", "microsoft", "reddit", "x", "tiktok",
		"snapchat", "pinterest", "taboola", "outbrain", "quora", "apple_search_ads", "amazon_ads"}},
	{ID: "organic_social", Label: "Organic social", Medium: "social", Sources: []string{"linkedin", "x", "facebook", "instagram", "youtube", "tiktok", "threads", "bluesky"}},
	{ID: "reddit", Label: "Reddit", Medium: "social", Sources: []string{"reddit"}},
	{ID: "outbound_email", Label: "Outbound email", Medium: "email", Sources: []string{"outflare", "emailbison", "instantly", "smartlead"}},
	{ID: "lifecycle_email", Label: "Lifecycle email", Medium: "email", Sources: []string{"outflare", "newsletter"}},
	{ID: "affiliates", Label: "Affiliates", Medium: "affiliate", Sources: []string{"partner"}},
	{ID: "referral", Label: "Referral", Medium: "referral", Sources: []string{"newsletter", "podcast", "producthunt", "event", "qr"}},
	{ID: "seo", Label: "SEO", Medium: "organic", Sources: []string{"google", "bing"}},
	{ID: "ai_answers", Label: "AI answers", Medium: "ai", Sources: []string{"chatgpt", "perplexity", "gemini", "claude", "copilot"}},
}

// ChannelByID returns the channel with id.
func ChannelByID(id string) (Channel, bool) {
	for _, c := range Channels {
		if c.ID == id {
			return c, true
		}
	}
	return Channel{}, false
}

// aliases are spellings people use for the same source.
var aliases = map[string]string{
	"fb": "facebook", "facebook.com": "facebook", "m.facebook.com": "facebook", "l.facebook.com": "facebook",
	"ig": "instagram", "instagram.com": "instagram",
	"twitter": "x", "twitter.com": "x", "x.com": "x", "t.co": "x",
	"li": "linkedin", "linkedin.com": "linkedin", "lnkd.in": "linkedin",
	"yt": "youtube", "youtube.com": "youtube",
	"reddit.com": "reddit", "old.reddit.com": "reddit",
	"google-ads": "google", "googleads": "google", "adwords": "google", "google.com": "google",
	"bing-ads": "microsoft", "microsoft-ads": "microsoft", "bing.com": "bing",
	"facebook-ads": "meta", "meta-ads": "meta",
	"tiktok-ads": "tiktok", "snap": "snapchat", "snapchat-ads": "snapchat", "pinterest-ads": "pinterest",
	"apple-search-ads": "apple_search_ads", "asa": "apple_search_ads", "amazon-ads": "amazon_ads",
	"chat.openai.com": "chatgpt", "chatgpt.com": "chatgpt", "openai": "chatgpt", "perplexity.ai": "perplexity",
}

var (
	nonTag    = regexp.MustCompile(`[^a-z0-9._-]+`)
	nonSlug   = regexp.MustCompile(`[^a-z0-9]+`)
	sourceRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,59}$`)
	slugRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)
	contentRE = regexp.MustCompile(`^[a-z0-9._-]{0,80}$`)
)

// Value cleans a tag value the way every tag is stored: lowercase, with
// spaces and other characters as hyphens. "Hero CTA" becomes "hero-cta".
func Value(s string) string {
	s = strings.Trim(nonTag.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-"), "-._")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return s
}

// Source cleans a utm_source and merges known spellings, so fb,
// facebook.com and Facebook are all facebook.
func Source(s string) string {
	v := Value(strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "https://"), "www."))
	if a, ok := aliases[v]; ok {
		return a
	}
	return v
}

// Slug makes a utm_campaign from a campaign name: "Residential proxies, US"
// becomes "residential-proxies-us".
func Slug(name string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(s) > 80 {
		s = strings.Trim(s[:80], "-")
	}
	return s
}

// Problems with a campaign's tags, as messages for the person who typed them.
var (
	ErrChannel = errors.New("channel must be one of paid_ads, organic_social, reddit, outbound_email, lifecycle_email, affiliates, referral, seo or ai_answers")
	ErrSource  = errors.New("source must be 1 to 60 letters, digits, dots, hyphens or underscores, such as google or linkedin")
	ErrSlug    = errors.New("slug must be 1 to 80 lowercase letters, digits and hyphens, such as spring-launch")
	ErrContent = errors.New("content and term must be at most 80 letters, digits, dots, hyphens or underscores")
	ErrURL     = errors.New("url must be a full http or https address, such as https://example.com/pricing")
)

// CheckSource reports whether a cleaned source is valid.
func CheckSource(s string) error {
	if !sourceRE.MatchString(s) {
		return ErrSource
	}
	return nil
}

// CheckSlug reports whether a slug is valid.
func CheckSlug(s string) error {
	if !slugRE.MatchString(s) {
		return ErrSlug
	}
	return nil
}

// CheckContent reports whether a cleaned content or term value is valid.
func CheckContent(s string) error {
	if !contentRE.MatchString(s) {
		return ErrContent
	}
	return nil
}

// Destination checks a link's destination and returns it without any
// tags it already had, so a link is never tagged twice.
func Destination(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || len(raw) > 2000 {
		return "", ErrURL
	}
	u.RawQuery = untagged(u.RawQuery, false)
	return u.String(), nil
}

// untagged drops the tags this package adds from a query string and keeps
// the rest as written. A partner's ref is dropped only when a new one
// replaces it.
func untagged(raw string, dropRef bool) string {
	if raw == "" {
		return ""
	}
	kept := []string{}
	for _, part := range strings.Split(raw, "&") {
		k, _, _ := strings.Cut(part, "=")
		if k, err := url.QueryUnescape(k); err == nil && (strings.HasPrefix(k, "utm_") || k == "ofc" || dropRef && k == "ref") {
			continue
		}
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, "&")
}

// Tags are what a link carries.
type Tags struct {
	Source, Medium, Campaign, Content, Term string
	// Code is the campaign's ofc.
	Code string
	// Ref is a partner's referral code, written last as ref. Commissions
	// credit the partner by ref first; the CMO view credits by ofc.
	Ref string
}

// Tag adds tags to a destination, replacing any UTM tags it had, in a
// fixed order so the same link always reads the same. The fragment stays
// at the end.
func Tag(destination string, t Tags) (string, error) {
	u, err := url.Parse(destination)
	if err != nil || u.Host == "" {
		return "", ErrURL
	}
	q := untagged(u.RawQuery, t.Ref != "")
	add := func(k, v string) {
		if v == "" {
			return
		}
		if q != "" {
			q += "&"
		}
		q += k + "=" + url.QueryEscape(v)
	}
	add("utm_source", t.Source)
	add("utm_medium", t.Medium)
	add("utm_campaign", t.Campaign)
	add("utm_content", t.Content)
	add("utm_term", t.Term)
	add("ofc", t.Code)
	add("ref", t.Ref)
	u.RawQuery = q
	return u.String(), nil
}

// NewCode makes a campaign's ofc: ten random lowercase letters and digits.
func NewCode() string {
	const abc = "abcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = abc[int(b[i])%len(abc)]
	}
	return string(b)
}

// Campaign is a tracking campaign as modules use it.
type Campaign struct {
	ID, Slug, Code, Channel, Source, Medium string
}

// Link tags a destination with the campaign's tags and ofc.
func (c Campaign) Link(destination, content string) (string, error) {
	dest, err := Destination(destination)
	if err != nil {
		return "", err
	}
	return Tag(dest, Tags{Source: c.Source, Medium: c.Medium, Campaign: c.Slug, Content: Value(content), Code: c.Code})
}

// Spec is the campaign a module wants for one of its objects.
type Spec struct {
	Channel, Source, Name string
	BrandID               *string
	// ObjectKind and ObjectID name the module's object, such as
	// "email_broadcast" and its id. One object has one campaign. Partners
	// use "partner_program" and the membership id, since one partner can
	// promote several brands and a campaign has one.
	ObjectKind, ObjectID string
}

// Querier is a transaction or pool.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Ensure returns the campaign for a module's object, making it the first
// time. created says it was made now, so the caller records its audit
// event (tracking_campaign.created) in the same transaction. A slug that is
// taken gets the object's kind and a number added.
func Ensure(ctx context.Context, tx Querier, ws string, s Spec) (c Campaign, created bool, err error) {
	ch, ok := ChannelByID(s.Channel)
	if !ok {
		return c, false, ErrChannel
	}
	source := Source(s.Source)
	if err := CheckSource(source); err != nil {
		return c, false, err
	}
	if s.ObjectKind == "" || s.ObjectID == "" {
		return c, false, errors.New("utm: Ensure needs an object kind and id")
	}
	const sel = `SELECT id::text, slug, code, channel, source, medium FROM tracking_campaigns WHERE workspace_id = $1 AND object_kind = $2 AND object_id = $3`
	err = tx.QueryRow(ctx, sel, ws, s.ObjectKind, s.ObjectID).Scan(&c.ID, &c.Slug, &c.Code, &c.Channel, &c.Source, &c.Medium)
	if err == nil {
		return c, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return c, false, err
	}
	name := strings.TrimSpace(s.Name)
	if name == "" {
		name = s.ObjectKind + " " + s.ObjectID
	}
	if len(name) > 120 {
		name = name[:120]
	}
	base := Slug(name)
	if base == "" {
		base = Slug(s.ObjectKind)
	}
	for i := 0; i < 20; i++ {
		slug := base
		if i > 0 {
			slug = SuffixSlug(base, i+1)
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO tracking_campaigns (workspace_id, brand_id, name, slug, code, channel, source, medium, object_kind, object_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT DO NOTHING
			RETURNING id::text, slug, code, channel, source, medium`,
			ws, s.BrandID, name, slug, NewCode(), ch.ID, source, ch.Medium, s.ObjectKind, s.ObjectID).
			Scan(&c.ID, &c.Slug, &c.Code, &c.Channel, &c.Source, &c.Medium)
		if err == nil {
			return c, true, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return c, false, err
		}
		// Another transaction may have made it for the same object.
		if err := tx.QueryRow(ctx, sel, ws, s.ObjectKind, s.ObjectID).Scan(&c.ID, &c.Slug, &c.Code, &c.Channel, &c.Source, &c.Medium); err == nil {
			return c, false, nil
		}
	}
	return c, false, errors.New("utm: no free slug for " + base)
}

// SuffixSlug adds -n to a slug, keeping it within the length limit.
func SuffixSlug(base string, n int) string {
	suffix := "-" + strconv.Itoa(n)
	if len(base)+len(suffix) > 80 {
		base = strings.Trim(base[:80-len(suffix)], "-")
	}
	return base + suffix
}

// Hosts are the hosts of a brand's website and domains, lowercase and
// without www, for OwnLink.
func Hosts(websites ...string) []string {
	out := []string{}
	for _, w := range websites {
		w = strings.ToLower(strings.TrimSpace(w))
		if w == "" {
			continue
		}
		if !strings.Contains(w, "://") {
			w = "https://" + w
		}
		u, err := url.Parse(w)
		if err != nil || u.Hostname() == "" {
			continue
		}
		h := strings.TrimPrefix(u.Hostname(), "www.")
		if !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	return out
}

// OwnLink tags link with the campaign when it points at one of hosts (or
// a subdomain) and has no utm_campaign of its own. Any other link, such as
// one to another company's site or one tagged by hand, comes back as is.
func (c Campaign) OwnLink(link string, hosts []string) string {
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return link
	}
	if u.Query().Get("utm_campaign") != "" {
		return link
	}
	h := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	own := false
	for _, d := range hosts {
		if h == d || strings.HasSuffix(h, "."+d) {
			own = true
			break
		}
	}
	if !own {
		return link
	}
	tagged, err := c.Link(link, "")
	if err != nil {
		return link
	}
	return tagged
}

// BrandHosts are the Hosts of a brand's website and domains, or of every
// brand in the workspace when brandID is nil, for OwnLink.
func BrandHosts(ctx context.Context, q Querier, ws string, brandID *string) ([]string, error) {
	var sites []string
	err := q.QueryRow(ctx, `
		SELECT coalesce(array_agg(site), '{}') FROM brands b, unnest(array_prepend(b.website, b.domains)) site
		WHERE b.workspace_id = $1 AND ($2::uuid IS NULL OR b.id = $2::uuid)`, ws, brandID).Scan(&sites)
	if err != nil {
		return nil, err
	}
	return Hosts(sites...), nil
}
