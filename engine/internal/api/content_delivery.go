package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/cmspublish"
)

// Delivering content to the brand's own site (docs/content.md,
// "Delivering to your own site"). Instead of a connector per CMS, an
// approved item is sent in the content.approved webhook with everything
// the site needs to put it in place itself: where it goes, its language,
// the body as Markdown and HTML, its SEO, links in and out, images and
// why it exists. The site's own code, automation or coding agent does the
// rest under its own review, and reports the live address back
// (POST .../content/{id}/delivered), so Outflare can measure the page.

var (
	contentPlacementTypes = []string{"post", "page", "landing", "comparison", "docs", "glossary", "faq", "other"}
	contentIntents        = []string{"informational", "commercial", "transactional", "navigational"}
	contentMediaRoles     = []string{"featured", "inline", "social"}
	contentGoals          = []string{"customers", "revenue", "activation", "retention", "partners", "search", "ai_answers", "tracking", "replies"}
	// A BCP 47 language tag such as en, en-AU or pt-BR.
	languageRe = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8}){0,3}$`)
)

type contentPlacement struct {
	// post, page, landing, comparison, docs, glossary, faq or other.
	Type       string `json:"type,omitempty"`
	Collection string `json:"collection,omitempty"`
	// The address on the site, such as /blog/northpeak-vs-ridgeline.
	Path         string `json:"path,omitempty"`
	Parent       string `json:"parent,omitempty"`
	TemplateHint string `json:"template_hint,omitempty"`
	NavLabel     string `json:"nav_label,omitempty"`
}

type contentSEO struct {
	MetaTitle         string           `json:"meta_title,omitempty"`
	MetaDescription   string           `json:"meta_description,omitempty"`
	PrimaryKeyword    string           `json:"primary_keyword,omitempty"`
	SecondaryKeywords []string         `json:"secondary_keywords,omitempty"`
	Intent            string           `json:"intent,omitempty"`
	Robots            string           `json:"robots,omitempty"`
	Schema            []map[string]any `json:"schema,omitempty"`
	OGTitle           string           `json:"og_title,omitempty"`
	OGDescription     string           `json:"og_description,omitempty"`
	OGImage           string           `json:"og_image,omitempty"`
}

type contentLink struct {
	Anchor string `json:"anchor"`
	Href   string `json:"href"`
	Rel    string `json:"rel,omitempty"`
	Why    string `json:"why,omitempty"`
}

type contentLinkFrom struct {
	// An existing page on the site, its path or address.
	Page string `json:"page"`
	// The text the link goes after, quoted from the page, to find the spot.
	AfterText string `json:"after_text,omitempty"`
	Anchor    string `json:"anchor"`
}

type contentLinks struct {
	Internal     []contentLink     `json:"internal,omitempty"`
	External     []contentLink     `json:"external,omitempty"`
	AddLinksFrom []contentLinkFrom `json:"add_links_from,omitempty"`
}

type contentMedia struct {
	Role     string `json:"role,omitempty"`
	URL      string `json:"url"`
	Alt      string `json:"alt,omitempty"`
	Caption  string `json:"caption,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Filename string `json:"filename,omitempty"`
	Credit   string `json:"credit,omitempty"`
}

type contentOverlap struct {
	Page    string `json:"page"`
	Keyword string `json:"keyword,omitempty"`
	Advice  string `json:"advice,omitempty"`
}

type contentRedirect struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type contentBrief struct {
	Goal            string            `json:"goal,omitempty"`
	Opportunity     string            `json:"opportunity,omitempty"`
	Measure         string            `json:"measure,omitempty"`
	CheckBeforeLive []string          `json:"check_before_live,omitempty"`
	Overlaps        []contentOverlap  `json:"overlaps,omitempty"`
	Redirects       []contentRedirect `json:"redirects,omitempty"`
}

func short(s string, n int) bool { return utf8.RuneCountInString(s) <= n }

// sitePath is a path on the site (/blog/a) or a full http(s) address.
func sitePath(s string) bool {
	if s == "" || len(s) > 2000 || strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	if strings.HasPrefix(s, "/") {
		return !strings.HasPrefix(s, "//")
	}
	return webURL(s)
}

func webURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && len(s) <= 2000 && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

func (p *contentPlacement) validate() string {
	if p.Type != "" && !slices.Contains(contentPlacementTypes, p.Type) {
		return "placement.type is one of " + strings.Join(contentPlacementTypes, ", ")
	}
	if p.Path != "" && !sitePath(p.Path) {
		return "placement.path is a path on the site such as /blog/a-post"
	}
	if p.Parent != "" && !sitePath(p.Parent) {
		return "placement.parent is a path on the site"
	}
	if !short(p.Collection, 100) || !short(p.TemplateHint, 500) || !short(p.NavLabel, 100) {
		return "placement.collection and nav_label are at most 100 characters, template_hint 500"
	}
	return ""
}

func (s *contentSEO) validate() string {
	if !short(s.MetaTitle, 200) || !short(s.OGTitle, 200) {
		return "seo.meta_title and og_title are at most 200 characters"
	}
	if !short(s.MetaDescription, 500) || !short(s.OGDescription, 500) {
		return "seo.meta_description and og_description are at most 500 characters"
	}
	if !short(s.PrimaryKeyword, 200) || len(s.SecondaryKeywords) > 30 {
		return "seo.primary_keyword is at most 200 characters, with at most 30 secondary_keywords"
	}
	for _, k := range s.SecondaryKeywords {
		if strings.TrimSpace(k) == "" || !short(k, 200) {
			return "each of seo.secondary_keywords is 1 to 200 characters"
		}
	}
	if s.Intent != "" && !slices.Contains(contentIntents, s.Intent) {
		return "seo.intent is " + strings.Join(contentIntents, ", ")
	}
	if !short(s.Robots, 100) {
		return "seo.robots is at most 100 characters, such as index,follow"
	}
	if s.OGImage != "" && !webURL(s.OGImage) {
		return "seo.og_image is a full image address"
	}
	if len(s.Schema) > 10 {
		return "seo.schema has at most 10 JSON-LD objects"
	}
	for _, o := range s.Schema {
		if _, ok := o["@type"]; !ok {
			return "each seo.schema object is JSON-LD with an @type"
		}
	}
	return ""
}

func (l *contentLinks) validate() string {
	if len(l.Internal) > 100 || len(l.External) > 100 || len(l.AddLinksFrom) > 50 {
		return "links has at most 100 internal, 100 external and 50 add_links_from"
	}
	for _, k := range l.Internal {
		if !sitePath(k.Href) || strings.TrimSpace(k.Anchor) == "" || !short(k.Anchor, 300) || !short(k.Why, 300) || !short(k.Rel, 50) {
			return "each internal link has anchor text and an href on the site"
		}
	}
	for _, k := range l.External {
		if !webURL(k.Href) || strings.TrimSpace(k.Anchor) == "" || !short(k.Anchor, 300) || !short(k.Why, 300) || !short(k.Rel, 50) {
			return "each external link has anchor text and a full https address"
		}
	}
	for _, k := range l.AddLinksFrom {
		if !sitePath(k.Page) || strings.TrimSpace(k.Anchor) == "" || !short(k.Anchor, 300) || !short(k.AfterText, 1000) {
			return "each of links.add_links_from has an existing page and anchor text"
		}
	}
	return ""
}

func validateMedia(m []contentMedia) string {
	if len(m) > 30 {
		return "at most 30 media items"
	}
	for _, x := range m {
		if !webURL(x.URL) {
			return "each media item has a full image address"
		}
		if x.Role != "" && !slices.Contains(contentMediaRoles, x.Role) {
			return "media role is featured, inline or social"
		}
		if !short(x.Alt, 500) || !short(x.Caption, 1000) || !short(x.Filename, 200) || !short(x.Credit, 300) || x.Width < 0 || x.Height < 0 {
			return "media alt is at most 500 characters, caption 1000, filename 200 and credit 300"
		}
	}
	return ""
}

func (b *contentBrief) validate() string {
	if b.Goal != "" && !slices.Contains(contentGoals, b.Goal) {
		return "brief.goal is one of " + strings.Join(contentGoals, ", ")
	}
	if !short(b.Opportunity, 2000) || !short(b.Measure, 1000) {
		return "brief.opportunity is at most 2000 characters, measure 1000"
	}
	if len(b.CheckBeforeLive) > 20 || len(b.Overlaps) > 20 || len(b.Redirects) > 50 {
		return "brief has at most 20 check_before_live, 20 overlaps and 50 redirects"
	}
	for _, c := range b.CheckBeforeLive {
		if strings.TrimSpace(c) == "" || !short(c, 500) {
			return "each of brief.check_before_live is 1 to 500 characters"
		}
	}
	for _, o := range b.Overlaps {
		if !sitePath(o.Page) || !short(o.Keyword, 200) || !short(o.Advice, 500) {
			return "each of brief.overlaps names a page on the site"
		}
	}
	for _, r := range b.Redirects {
		if !sitePath(r.From) || !sitePath(r.To) || r.From == r.To {
			return "each of brief.redirects has a from and a different to on the site"
		}
	}
	return ""
}

// deliveryInput holds the delivery fields of a content write.
type deliveryInput struct {
	Language  *string           `json:"language"`
	Placement *contentPlacement `json:"placement"`
	SEO       *contentSEO       `json:"seo"`
	Links     *contentLinks     `json:"links"`
	Media     *[]contentMedia   `json:"media"`
	Brief     *contentBrief     `json:"brief"`
}

func (in *deliveryInput) validate() string {
	if in.Language != nil {
		*in.Language = strings.TrimSpace(*in.Language)
		if *in.Language != "" && !languageRe.MatchString(*in.Language) {
			return "language is a language tag such as en, en-AU or pt-BR"
		}
	}
	for _, msg := range []func() string{
		func() string {
			if in.Placement == nil {
				return ""
			}
			return in.Placement.validate()
		},
		func() string {
			if in.SEO == nil {
				return ""
			}
			return in.SEO.validate()
		},
		func() string {
			if in.Links == nil {
				return ""
			}
			return in.Links.validate()
		},
		func() string {
			if in.Media == nil {
				return ""
			}
			return validateMedia(*in.Media)
		},
		func() string {
			if in.Brief == nil {
				return ""
			}
			return in.Brief.validate()
		},
	} {
		if m := msg(); m != "" {
			return m
		}
	}
	return ""
}

// jsonArg marshals a delivery field for the database, or nil to keep it.
func jsonArg[T any](p *T) any {
	if p == nil {
		return nil
	}
	b, _ := json.Marshal(p)
	return string(b)
}

// contentDelivery builds the content.approved webhook's data from the
// item as it is in tx, after the approval.
func contentDelivery(ctx context.Context, tx pgx.Tx, workspaceID, brandID, id string) (map[string]any, error) {
	var (
		title, slug, target, body, excerpt, canonical, language string
		tags                                                    []string
		placement, seo, links, media, brief                     []byte
		revision                                                int
		approvedBy                                              *string
		approvedAt                                              *time.Time
		brandName, brandHandle, website                         string
	)
	err := tx.QueryRow(ctx, `SELECT i.title, i.slug, i.target, i.body, i.excerpt, i.canonical_url, i.language, i.tags,
			i.placement, i.seo, i.links, i.media, i.brief, i.revision, apv.display_name, i.approved_at, b.name, b.handle, b.website
		FROM content_items i JOIN brands b ON b.id = i.brand_id LEFT JOIN actors apv ON apv.id = i.approved_by
		WHERE i.id = $1 AND i.brand_id = $2 AND i.workspace_id = $3`, id, brandID, workspaceID).
		Scan(&title, &slug, &target, &body, &excerpt, &canonical, &language, &tags, &placement, &seo, &links, &media, &brief, &revision, &approvedBy, &approvedAt, &brandName, &brandHandle, &website)
	if err != nil {
		return nil, err
	}
	html, err := cmspublish.HTML(body)
	if err != nil {
		return nil, err
	}
	// Words, not Markdown marks such as ## or -.
	words := 0
	for _, f := range strings.Fields(body) {
		if strings.IndexFunc(f, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0 {
			words++
		}
	}
	content := map[string]any{"title": title, "excerpt": excerpt, "markdown": body, "html": html, "tags": tags,
		"word_count": words, "reading_minutes": max(1, (words+219)/220)}
	out := map[string]any{
		"content_id": id, "revision": revision, "target": target, "slug": slug,
		"brand":   map[string]any{"id": brandID, "name": brandName, "handle": brandHandle, "website": website},
		"content": content,
		"review":  map[string]any{"approved_by": approvedBy, "approved_at": approvedAt},
		// The receiver reports the live address here, with a token that
		// has content.manage.
		"callback": map[string]any{"method": "POST", "path": "/v1/brands/" + brandID + "/content/" + id + "/delivered", "scope": "content.manage"},
	}
	if language != "" {
		out["language"] = language
	}
	var seoMap map[string]any
	_ = json.Unmarshal(seo, &seoMap)
	if canonical != "" {
		if seoMap == nil {
			seoMap = map[string]any{}
		}
		seoMap["canonical"] = canonical
	}
	if len(seoMap) > 0 {
		out["seo"] = seoMap
	}
	for k, raw := range map[string][]byte{"placement": placement, "links": links, "media": media, "brief": brief} {
		var v any
		if json.Unmarshal(raw, &v) == nil {
			switch t := v.(type) {
			case map[string]any:
				if len(t) > 0 {
					out[k] = t
				}
			case []any:
				if len(t) > 0 {
					out[k] = t
				}
			}
		}
	}
	return out, nil
}

// deliveredContent records that the brand's own site put the item live
// at an address: the item becomes published there, so Outflare can
// measure the page. The site's code, automation or coding agent calls it
// after the content.approved webhook.
func (s *Server) deliveredContent(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "content not found")
		return
	}
	var req struct {
		URL      string `json:"url"`
		Revision *int   `json:"revision"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if !webURL(req.URL) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "url is the page's full address, starting with https://")
		return
	}
	ctx := r.Context()
	var conflict string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		var revision int
		err := tx.QueryRow(ctx, `SELECT status, revision FROM content_items WHERE id = $1 AND brand_id = $2 AND workspace_id = $3 FOR UPDATE`, id, a.Brand, a.WorkspaceID).Scan(&status, &revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if status != "approved" && status != "published" {
			conflict = "only an approved item is delivered; approve it first (status: approved)"
			return nil
		}
		if req.Revision != nil && *req.Revision != revision {
			conflict = "that revision is out of date; the item was approved again since, so deliver the latest one"
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE content_items SET url = $2, status = 'published', published_at = COALESCE(published_at, now()), updated_at = now() WHERE id = $1`, id, req.URL); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "content.delivered", "content", id,
			map[string]any{"brand_id": a.Brand, "url": req.URL, "revision": revision},
			map[string]any{"brand_id": a.Brand, "revision": revision})
	})
	if conflict != "" {
		writeError(w, r, http.StatusConflict, "conflict", conflict)
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	s.writeContent(w, r, a, id, http.StatusOK)
}

// approveContent records a person's approval of the item in tx: its
// revision counts up, and the content.approved webhook carries the whole
// item (contentDelivery) to the brand's endpoints.
func (s *Server) approveContent(ctx context.Context, tx pgx.Tx, a *access, id string, data map[string]any) error {
	var revision int
	if err := tx.QueryRow(ctx, `UPDATE content_items SET revision = revision + 1, approved_by = $2, approved_at = now() WHERE id = $1 RETURNING revision`,
		id, actorRef(a)).Scan(&revision); err != nil {
		return err
	}
	hook, err := contentDelivery(ctx, tx, a.WorkspaceID, a.Brand, id)
	if err != nil {
		return err
	}
	data["revision"] = revision
	return recordWithWebhook(ctx, tx, &a.principal, a.WorkspaceID, "content.approved", "content", id, data,
		map[string]any{"brand_id": a.Brand, "revision": revision}, hook)
}
