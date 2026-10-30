package api

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"

	"github.com/outflareai/outflare/engine/internal/metrics"
)

// The public help centre: server-rendered pages, so search engines and AI
// crawlers read every article, with a little script only for copying an
// article for an AI assistant. It shows only published items marked for
// the help centre (visibility public) of its brand or shared by every
// brand. Article text is the workspace's Markdown, rendered with raw HTML
// left out and unsafe links dropped; pages run under a strict content
// security policy. Reading counts views and feedback per day (counts
// only), and searches that find nothing join the knowledge's unanswered
// questions.

//go:embed help_centre.html
var helpCentreHTML string

var helpTemplate = template.Must(template.New("help").Funcs(template.FuncMap{
	"icon": helpIcon,
	"pair": func(a, b any) []any { return []any{a, b} },
	"date": func(t time.Time) string { return t.Format("2 January 2006") },
	"plural": func(n int, one, many string) string {
		if n == 1 {
			return "1 " + one
		}
		return strconv.Itoa(n) + " " + many
	},
	"social": func(k string) template.HTML { return helpSocialIcons[k] },
	"socialName": func(k string) string {
		return map[string]string{"x": "X", "linkedin": "LinkedIn", "youtube": "YouTube", "facebook": "Facebook", "instagram": "Instagram", "github": "GitHub"}[k]
	},
}).Parse(helpCentreHTML))

var helpMarkdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
)

func (s *Server) routesHelpCentreSite(mux *http.ServeMux) {
	mux.HandleFunc("GET /h/{slug}/{$}", s.helpPage("home"))
	mux.HandleFunc("GET /h/{slug}/search", s.helpPage("search"))
	mux.HandleFunc("GET /h/{slug}/collections/{ref}", s.helpPage("collection"))
	mux.HandleFunc("GET /h/{slug}/articles/{ref}", s.helpPage("article"))
	mux.HandleFunc("POST /h/{slug}/articles/{ref}/feedback", s.helpFeedback)
	mux.HandleFunc("GET /h/{slug}/sitemap.xml", s.helpSitemap)
	mux.HandleFunc("GET /h/{slug}/robots.txt", s.helpRobots)
	mux.HandleFunc("GET /h/{slug}/llms.txt", s.helpLLMs)
}

// helpSite is one help centre being served.
type helpSite struct {
	ws      string
	centre  helpCentreView
	set     helpSettings
	base    string // the path its pages start at: /h/<slug>/, or / on its own domain
	preview bool
	nonce   string
	// What the page rendered: the article read, and whether a search
	// found nothing, for the day's counts.
	read        *string
	searchEmpty bool
}

func (s *Server) helpSiteFor(ws string, v helpCentreView, base string, preview bool) *helpSite {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return &helpSite{ws: ws, centre: v, set: v.Settings, base: base, preview: preview, nonce: base64.RawStdEncoding.EncodeToString(b)}
}

type ctxHelpHostKey struct{}

// onHelpHost reports whether the request came to a help centre's own
// domain, where its pages live at / rather than /h/<slug>/.
func onHelpHost(r *http.Request) bool {
	v, _ := r.Context().Value(ctxHelpHostKey{}).(bool)
	return v
}

// helpSiteFrom finds the help centre a public request is for: on, by its
// slug. ok is false (and a 404 written) for an unknown or switched-off one.
func (s *Server) helpSiteFrom(w http.ResponseWriter, r *http.Request) (*helpSite, bool) {
	slug := r.PathValue("slug")
	if !subdomainRE.MatchString(slug) {
		http.NotFound(w, r)
		return nil, false
	}
	var ws string
	err := s.pool.QueryRow(r.Context(), `SELECT workspace_id::text FROM help_centres WHERE slug = $1 AND enabled`, slug).Scan(&ws)
	if errors.Is(err, pgx.ErrNoRows) {
		metrics.HelpCentrePage("unknown", "not_found")
		http.NotFound(w, r)
		return nil, false
	}
	if err != nil {
		metrics.HelpCentrePage("unknown", "error")
		s.internal(w, r, err)
		return nil, false
	}
	v, err := s.scanHelpCentre(s.pool.QueryRow(r.Context(), helpCentreSelect+` WHERE h.workspace_id = $1 AND h.slug = $2`, ws, slug))
	if err != nil {
		metrics.HelpCentrePage("unknown", "error")
		s.internal(w, r, err)
		return nil, false
	}
	base := "/h/" + slug + "/"
	if onHelpHost(r) {
		base = "/"
	}
	return s.helpSiteFor(ws, v, base, false), true
}

// helpBot reports whether a request looks like a crawler or link preview,
// whose reads aren't counted as views.
func helpBot(r *http.Request) bool {
	ua := strings.ToLower(r.UserAgent())
	if ua == "" {
		return true
	}
	for _, b := range []string{"bot", "crawl", "spider", "slurp", "preview", "fetch", "monitor", "curl", "wget", "python", "headless"} {
		if strings.Contains(ua, b) {
			return true
		}
	}
	return false
}

func (s *Server) helpPage(page string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		if !s.helpIPLimit.allow(w, r, networkKey(clientIP(r))) {
			metrics.HelpCentrePage(page, "limited")
			return
		}
		site, ok := s.helpSiteFrom(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		ref := r.PathValue("ref")
		if page == "article" && strings.HasSuffix(ref, ".md") {
			s.helpArticleMarkdown(w, r, site, strings.TrimSuffix(ref, ".md"))
			return
		}
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		body, status, err := s.renderHelpPage(ctx, site, page, ref, q, r.URL.Query().Get("thanks") == "1")
		if err != nil {
			metrics.HelpCentrePage(page, "error")
			s.internal(w, r, err)
			return
		}
		if status == http.StatusOK && !helpBot(r) {
			s.countHelpView(ctx, site, page, q, r)
		}
		writeHelpHeaders(w, site, status)
		w.WriteHeader(status)
		_, _ = w.Write(body)
		outcome := "ok"
		if status == http.StatusNotFound {
			outcome = "not_found"
		}
		metrics.HelpCentrePage(page, outcome)
		metrics.HelpCentreRender(time.Since(start))
	}
}

func writeHelpHeaders(w http.ResponseWriter, site *helpSite, status int) {
	nonce := site.nonce
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Content-Security-Policy", "default-src 'self'; img-src https: data:; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src https://fonts.gstatic.com; script-src 'self' 'nonce-"+nonce+"'; frame-ancestors 'self'; base-uri 'none'; form-action 'self'")
	if !site.set.Indexing || status != http.StatusOK {
		h.Set("X-Robots-Tag", "noindex")
	}
}

// helpFolder is a folder shown on the help centre, with what it holds.
type helpFolder struct {
	ID, Name, Description, Icon, Ref string
	ParentID                         *string
	Count                            int
	Children                         []*helpFolder
}

// helpArticle is an item shown on the help centre.
type helpArticle struct {
	ID, Kind, Title, Excerpt, Ref, Body string
	FolderID                            *string
	Owner                               string
	Updated                             time.Time
	Views                               int
	Answer                              template.HTML
}

// helpContent is everything a help centre shows, read once per page.
type helpContent struct {
	folders  []*helpFolder
	byFolder map[string]*helpFolder
	articles []*helpArticle
	byID     map[string]*helpArticle
}

// helpRef is how a folder or article appears in its address: the start of
// its id, then its name as words.
func helpRef(id, name string) string {
	slug := helpSlug(name)
	if len(slug) > 60 {
		slug = strings.Trim(slug[:60], "-")
	}
	return strings.ReplaceAll(id, "-", "")[:8] + "-" + slug
}

func (s *Server) helpContent(ctx context.Context, site *helpSite) (*helpContent, error) {
	brand := site.centre.BrandID
	rows, err := s.pool.Query(ctx, `
		SELECT c.id::text, c.name, c.description, c.icon, c.parent_id::text FROM knowledge_collections c
		WHERE c.workspace_id = $1 AND ($2::uuid IS NULL OR c.brand_id IS NULL OR c.brand_id = $2::uuid)
		ORDER BY c.position, c.name`, site.ws, brand)
	if err != nil {
		return nil, err
	}
	c := &helpContent{byFolder: map[string]*helpFolder{}, byID: map[string]*helpArticle{}}
	all, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*helpFolder, error) {
		f := &helpFolder{}
		err := row.Scan(&f.ID, &f.Name, &f.Description, &f.Icon, &f.ParentID)
		f.Ref = helpRef(f.ID, f.Name)
		return f, err
	})
	if err != nil {
		return nil, err
	}
	for _, f := range all {
		c.byFolder[f.ID] = f
	}
	rows, err = s.pool.Query(ctx, `
		SELECT i.id::text, i.kind, i.title, i.body, i.collection_id::text, COALESCE(u.name, ''), i.updated_at,
		       COALESCE((SELECT sum(d.views) FROM help_article_days d WHERE d.item_id = i.id AND d.day > current_date - 30), 0)::int
		FROM knowledge_items i LEFT JOIN users u ON u.actor_id = i.owner_id
		WHERE i.workspace_id = $1 AND i.deleted_at IS NULL AND i.status = 'published' AND i.visibility = 'public'
		  AND ($2::uuid IS NULL OR i.brand_id IS NULL OR i.brand_id = $2::uuid)
		ORDER BY i.title`, site.ws, brand)
	if err != nil {
		return nil, err
	}
	c.articles, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (*helpArticle, error) {
		a := &helpArticle{}
		err := row.Scan(&a.ID, &a.Kind, &a.Title, &a.Body, &a.FolderID, &a.Owner, &a.Updated, &a.Views)
		a.Ref = helpRef(a.ID, a.Title)
		a.Excerpt = helpExcerpt(a.Body)
		return a, err
	})
	if err != nil {
		return nil, err
	}
	for _, a := range c.articles {
		c.byID[a.ID] = a
		if a.FolderID != nil && c.byFolder[*a.FolderID] == nil {
			a.FolderID = nil // a folder of another brand
		}
		for f := folderOf(c, a.FolderID); f != nil; f = folderOf(c, f.ParentID) {
			f.Count++
		}
	}
	// Folders with nothing to read are left out.
	for _, f := range all {
		if f.Count == 0 {
			continue
		}
		if p := folderOf(c, f.ParentID); p != nil {
			p.Children = append(p.Children, f)
		} else {
			c.folders = append(c.folders, f)
		}
	}
	return c, nil
}

func folderOf(c *helpContent, id *string) *helpFolder {
	if id == nil {
		return nil
	}
	return c.byFolder[*id]
}

// findRef finds a folder or article by the id start in its address.
func findRef[T any](ref string, list map[string]T) (T, bool) {
	var zero T
	prefix, _, _ := strings.Cut(ref, "-")
	if len(prefix) != 8 {
		return zero, false
	}
	for id, v := range list {
		if strings.ReplaceAll(id, "-", "")[:8] == prefix {
			return v, true
		}
	}
	return zero, false
}

// helpPageData is what the template gets.
type helpPageData struct {
	Page, Base, Title, Description, Canonical, Nonce, Lang, Query string
	Preview                                                       bool
	Set                                                           helpSettings
	Name                                                          string
	Style                                                         template.CSS
	FontURL                                                       string
	Folders                                                       []*helpFolder
	Loose                                                         []*helpArticle
	Popular, FAQ, Results, Related                                []*helpArticle
	Folder                                                        *helpFolder
	Crumbs                                                        []*helpFolder
	Articles, Snippets                                            []*helpArticle
	Article                                                       *helpArticle
	Body                                                          template.HTML
	TOC                                                           []helpHeading
	Thanks                                                        bool
	Social                                                        []string
	Contact                                                       bool
}

type helpHeading struct {
	ID, Text string
	Level    int
}

// renderHelpPage renders one page: home, collection, article or search.
// A ref or search that finds nothing renders the not-found page with 404.
func (s *Server) renderHelpPage(ctx context.Context, site *helpSite, page, ref, q string, thanks bool) ([]byte, int, error) {
	c, err := s.helpContent(ctx, site)
	if err != nil {
		return nil, 0, err
	}
	set := site.set
	name := set.Title
	if name == "" {
		name = site.centre.Name
	}
	d := &helpPageData{Page: page, Base: site.base, Preview: site.preview, Nonce: site.nonce, Set: set, Name: name, Lang: set.Language, Title: name,
		Style: helpStyle(set), FontURL: helpFontURL(set.Font), Folders: c.folders, Query: q,
		Contact: set.ContactURL != ""}
	for _, k := range helpSocials {
		if set.Social[k] != "" {
			d.Social = append(d.Social, k)
		}
	}
	if !site.preview {
		d.Canonical = strings.TrimSuffix(site.centre.URL, "/")
	}
	status := http.StatusOK
	switch page {
	case "home":
		d.Description = set.HeroSubtitle
		for _, a := range c.articles {
			if a.FolderID == nil && a.Kind == "article" {
				d.Loose = append(d.Loose, a)
			}
		}
		pop := slices.Clone(c.articles)
		slices.SortStableFunc(pop, func(x, y *helpArticle) int { return y.Views - x.Views })
		for _, a := range pop {
			if a.Kind == "article" && len(d.Popular) < 6 && (a.Views > 0 || site.preview) {
				d.Popular = append(d.Popular, a)
			}
			if a.Kind == "snippet" && len(d.FAQ) < 8 {
				d.FAQ = append(d.FAQ, a)
			}
		}
		for _, a := range d.FAQ {
			a.Answer = helpRender(a.Body, nil)
		}
		if d.Canonical != "" {
			d.Canonical += "/"
		}
	case "collection":
		f, ok := findRef(ref, c.byFolder)
		if (!ok || f.Count == 0) && site.preview && ref == "" && len(c.folders) > 0 {
			f, ok = c.folders[0], true
		}
		if !ok || f.Count == 0 {
			return s.helpNotFound(d)
		}
		d.Folder, d.Title, d.Description = f, f.Name+" | "+name, f.Description
		for p := folderOf(c, f.ParentID); p != nil; p = folderOf(c, p.ParentID) {
			d.Crumbs = append([]*helpFolder{p}, d.Crumbs...)
		}
		for _, a := range c.articles {
			if a.FolderID != nil && *a.FolderID == f.ID {
				if a.Kind == "snippet" {
					a.Answer = helpRender(a.Body, nil)
					d.Snippets = append(d.Snippets, a)
				} else {
					d.Articles = append(d.Articles, a)
				}
			}
		}
		if d.Canonical != "" {
			d.Canonical += "/collections/" + f.Ref
		}
	case "article":
		a, ok := findRef(ref, c.byID)
		if !ok && site.preview && ref == "" {
			for _, x := range c.articles {
				if x.Kind == "article" {
					a, ok = x, true
					break
				}
			}
		}
		if !ok {
			return s.helpNotFound(d)
		}
		d.Article, d.Title, d.Description = a, a.Title+" | "+name, truncate(a.Excerpt, 160)
		site.read = &a.ID
		d.Thanks = thanks
		var toc []helpHeading
		d.Body = helpRender(a.Body, &toc)
		if set.TableOfContents && len(toc) >= 2 {
			d.TOC = toc
		}
		for f := folderOf(c, a.FolderID); f != nil; f = folderOf(c, f.ParentID) {
			d.Crumbs = append([]*helpFolder{f}, d.Crumbs...)
		}
		if set.Related {
			for _, x := range c.articles {
				if x != a && x.Kind == "article" && sameBrand(x.FolderID, a.FolderID) && len(d.Related) < 5 {
					d.Related = append(d.Related, x)
				}
			}
		}
		if d.Canonical != "" {
			d.Canonical += "/articles/" + a.Ref
		}
	case "search":
		d.Title = "Search | " + name
		d.Results, err = s.helpSearch(ctx, site, c, q)
		if err != nil {
			return nil, 0, err
		}
		site.searchEmpty = q != "" && len(d.Results) == 0
		d.Canonical = ""
	}
	var buf bytes.Buffer
	if err := helpTemplate.ExecuteTemplate(&buf, "page", d); err != nil {
		return nil, 0, err
	}
	return buf.Bytes(), status, nil
}

func (s *Server) helpNotFound(d *helpPageData) ([]byte, int, error) {
	d.Page, d.Title, d.Canonical = "notfound", "Page not found | "+d.Name, ""
	var buf bytes.Buffer
	if err := helpTemplate.ExecuteTemplate(&buf, "page", d); err != nil {
		return nil, 0, err
	}
	return buf.Bytes(), http.StatusNotFound, nil
}

// helpSearch finds the help centre's articles that match q by their
// words, best first: one result per item, with the passage that matched.
// Words only: a reader's search never spends the workspace's AI budget.
func (s *Server) helpSearch(ctx context.Context, site *helpSite, c *helpContent, q string) ([]*helpArticle, error) {
	words := knowledgeWords(q)
	if words == "" {
		return nil, nil
	}
	if err := s.ensureKnowledgeChunks(ctx, site.ws); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (i.id) i.id::text, c.text, ts_rank_cd(`+knowledgeVector+`, to_tsquery('simple', $3)) AS rank
		FROM knowledge_chunks c JOIN knowledge_items i ON i.id = c.item_id
		WHERE c.workspace_id = $1 AND i.deleted_at IS NULL AND i.status = 'published' AND i.visibility = 'public'
		  AND ($2::uuid IS NULL OR i.brand_id IS NULL OR i.brand_id = $2::uuid)
		  AND `+knowledgeVector+` @@ to_tsquery('simple', $3)
		ORDER BY i.id, rank DESC`, site.ws, site.centre.BrandID, words)
	if err != nil {
		return nil, err
	}
	type hit struct {
		id, text string
		rank     float64
	}
	hits, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (hit, error) {
		var h hit
		err := row.Scan(&h.id, &h.text, &h.rank)
		return h, err
	})
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(hits, func(x, y hit) int { return int(math.Copysign(1, y.rank-x.rank)) })
	var out []*helpArticle
	for _, h := range hits {
		a := c.byID[h.id]
		if a == nil || len(out) == 20 {
			continue
		}
		cp := *a
		cp.Excerpt = helpExcerpt(h.text)
		out = append(out, &cp)
	}
	return out, nil
}

// helpExcerpt is an article's first paragraph as plain text, for lists:
// the first lines of prose, not a run of list items or table cells, cut at
// a word near 200 characters.
func helpExcerpt(md string) string {
	var para []string
	for _, l := range strings.Split(md, "\n") {
		t := strings.TrimSpace(l)
		switch {
		case t == "" && len(para) > 0:
			return helpCut(strings.Join(para, " "))
		case t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "|") || strings.HasPrefix(t, "```") || strings.HasPrefix(t, "---"):
			continue
		case listMarker.MatchString(t):
			if len(para) > 0 {
				return helpCut(strings.Join(para, " "))
			}
			continue
		}
		para = append(para, strings.TrimSpace(strings.TrimLeft(t, ">")))
	}
	if len(para) == 0 {
		return knowledgeExcerpt(md)
	}
	return helpCut(strings.Join(para, " "))
}

// helpCut is text without Markdown marks, cut at a word near 200 runes.
func helpCut(t string) string {
	t = strings.NewReplacer("**", "", "__", "", "`", "", "*", "", "_", "").Replace(t)
	t = helpLinkRE.ReplaceAllString(t, "$1")
	if utf8.RuneCountInString(t) <= 200 {
		return t
	}
	r := []rune(t)[:200]
	if i := strings.LastIndex(string(r), " "); i > 120 {
		return strings.TrimRight(string(r)[:i], ",;:") + "…"
	}
	return string(r) + "…"
}

var helpLinkRE = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)

// helpRender turns an article's Markdown into HTML: raw HTML left out,
// unsafe links dropped, headings given ids. toc, when given, collects the
// second- and third-level headings.
func helpRender(md string, toc *[]helpHeading) template.HTML {
	src := []byte(md)
	doc := helpMarkdown.Parser().Parse(text.NewReader(src))
	if toc != nil {
		_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
			if h, ok := n.(*ast.Heading); ok && entering && (h.Level == 2 || h.Level == 3) {
				id, _ := h.AttributeString("id")
				ids, _ := id.([]byte)
				var t bytes.Buffer
				for c := h.FirstChild(); c != nil; c = c.NextSibling() {
					if tx, ok := c.(*ast.Text); ok {
						t.Write(tx.Segment.Value(src))
					} else if c.HasChildren() {
						for cc := c.FirstChild(); cc != nil; cc = cc.NextSibling() {
							if tx, ok := cc.(*ast.Text); ok {
								t.Write(tx.Segment.Value(src))
							}
						}
					}
				}
				*toc = append(*toc, helpHeading{ID: string(ids), Text: t.String(), Level: h.Level})
			}
			return ast.WalkContinue, nil
		})
	}
	var buf bytes.Buffer
	if err := helpMarkdown.Renderer().Render(&buf, src, doc); err != nil {
		return ""
	}
	return template.HTML(buf.String()) //nolint:gosec // goldmark leaves raw HTML out and drops unsafe links (no WithUnsafe)
}

// helpStyle is the page's CSS variables from the settings.
func helpStyle(set helpSettings) template.CSS {
	header := set.HeaderColor
	if header == "" {
		header = set.Color
	}
	footer := set.FooterColor
	if footer == "" {
		footer = set.Background
	}
	radius := map[string]string{"none": "0px", "small": "4px", "medium": "10px", "large": "18px"}[set.Radius]
	font := helpFontStack(set.Font)
	css := fmt.Sprintf("--accent-text:%s;--accent:%s;--accent-ink:%s;--bg:%s;--ink:%s;--header:%s;--header-ink:%s;--footer:%s;--footer-ink:%s;--radius:%s;--font:%s;",
		readableOn(set.Color, set.Background), set.Color, inkOn(set.Color), set.Background, inkOn(set.Background), header, inkOn(header), footer, inkOn(footer), radius, font)
	return template.CSS(css) //nolint:gosec // every value is a checked colour, a fixed radius or a fixed font stack
}

// rgbOf reads a hex colour; ok is false for anything else.
func rgbOf(hex string) (r, g, b float64, ok bool) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) == 3 {
		hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
	}
	v, err := strconv.ParseUint(hex, 16, 32)
	if len(hex) != 6 || err != nil {
		return 0, 0, 0, false
	}
	return float64(v >> 16 & 0xff), float64(v >> 8 & 0xff), float64(v & 0xff), true
}

func luminance(r, g, b float64) float64 {
	lin := func(c float64) float64 {
		x := c / 255
		if x <= 0.03928 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

// readableOn is the accent as text on the background: the accent itself
// when it reads well enough (contrast 4.5 or more), else the accent
// darkened (or, on a dark page, lightened) until it does, so a pale brand
// colour still gives readable links and icons.
func readableOn(accent, bg string) string {
	r, g, b, ok := rgbOf(accent)
	br, bgg, bb, ok2 := rgbOf(bg)
	if !ok || !ok2 {
		return accent
	}
	lb := luminance(br, bgg, bb)
	toward := 0.0
	if lb < 0.2 {
		toward = 255
	}
	for i := 0; i <= 20; i++ {
		t := float64(i) / 20
		cr, cg, cb := r+(toward-r)*t, g+(toward-g)*t, b+(toward-b)*t
		l := luminance(cr, cg, cb)
		if (math.Max(l, lb)+0.05)/(math.Min(l, lb)+0.05) >= 4.5 {
			return fmt.Sprintf("#%02x%02x%02x", int(cr+0.5), int(cg+0.5), int(cb+0.5))
		}
	}
	return inkOn(bg)
}

// inkOn is black or white, whichever reads better on the colour.
func inkOn(hex string) string {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) == 3 {
		hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
	}
	if len(hex) != 6 {
		return "#111827"
	}
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return "#111827"
	}
	lin := func(c uint64) float64 {
		x := float64(c) / 255
		if x <= 0.03928 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	l := 0.2126*lin(v>>16&0xff) + 0.7152*lin(v>>8&0xff) + 0.0722*lin(v&0xff)
	if l > 0.4 {
		return "#111827"
	}
	return "#ffffff"
}

var helpFontNames = map[string]string{
	"inter": "Inter", "roboto": "Roboto", "open-sans": "Open Sans", "lato": "Lato", "source-sans": "Source Sans 3",
	"ibm-plex-sans": "IBM Plex Sans", "dm-sans": "DM Sans", "nunito": "Nunito", "merriweather": "Merriweather", "lora": "Lora",
}

func helpFontStack(font string) string {
	const system = `ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif`
	name, ok := helpFontNames[font]
	if !ok {
		return system
	}
	if font == "merriweather" || font == "lora" {
		return `"` + name + `", Georgia, serif`
	}
	return `"` + name + `", ` + system
}

func helpFontURL(font string) string {
	name, ok := helpFontNames[font]
	if !ok {
		return ""
	}
	return "https://fonts.googleapis.com/css2?family=" + url.QueryEscape(name) + ":wght@400;500;600;700&display=swap"
}

// countHelpView adds a view to the day's counts: the help centre's for
// every page, an article's for an article, a search's for a search (and,
// when it found nothing, the question joins the unanswered ones).
func (s *Server) countHelpView(ctx context.Context, site *helpSite, page, q string, r *http.Request) {
	search, empty := 0, 0
	itemID, noResults := site.read, site.searchEmpty
	if page == "search" && q != "" {
		search = 1
	}
	if noResults {
		empty = 1
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO help_centre_days (workspace_id, centre_id, day, views, searches, empty_searches) VALUES ($1, $2, current_date, 1, $3, $4)
			ON CONFLICT (centre_id, day) DO UPDATE SET views = help_centre_days.views + 1,
				searches = help_centre_days.searches + $3, empty_searches = help_centre_days.empty_searches + $4`,
			site.ws, site.centre.ID, search, empty); err != nil {
			return err
		}
		if itemID != nil {
			if _, err := tx.Exec(ctx, `
				INSERT INTO help_article_days (workspace_id, item_id, day, views) VALUES ($1, $2, current_date, 1)
				ON CONFLICT (item_id, day) DO UPDATE SET views = help_article_days.views + 1`, site.ws, *itemID); err != nil {
				return err
			}
		}
		// A search with no answer is a question worth writing: kept like
		// one asked of the knowledge, a few a day per reader at most.
		if noResults && len(strings.Fields(q)) >= 2 && utf8.RuneCountInString(q) <= 300 && s.helpWriteLimit.hit(networkKey(clientIP(r))) {
			if _, err := tx.Exec(ctx, `INSERT INTO knowledge_questions (workspace_id, brand_id, question, audience, surface, answered)
				VALUES ($1, $2, $3, 'customers', 'help_centre', false)`, site.ws, site.centre.BrandID, q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		metrics.HelpCentrePage(page, "count_failed")
		s.log.Error("help centre view not counted", "err", err, "request_id", RequestID(r.Context()))
	}
}

// helpFeedback takes a reader's answer to "Did this answer your
// question?" as a plain form post, then shows the article again.
func (s *Server) helpFeedback(w http.ResponseWriter, r *http.Request) {
	site, ok := s.helpSiteFrom(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	c, err := s.helpContent(ctx, site)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	a, ok := findRef(r.PathValue("ref"), c.byID)
	if !ok || !site.set.Feedback {
		http.NotFound(w, r)
		return
	}
	back := site.base + "articles/" + a.Ref
	key := networkKey(clientIP(r))
	if r.ParseForm() != nil || helpBot(r) || !s.helpWriteLimit.hit(key) {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	helpful := r.PostForm.Get("helpful") == "yes"
	up, down := 0, 1
	if helpful {
		up, down = 1, 0
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO help_article_days (workspace_id, item_id, day, helpful, unhelpful) VALUES ($1, $2, current_date, $3, $4)
		ON CONFLICT (item_id, day) DO UPDATE SET helpful = help_article_days.helpful + $3, unhelpful = help_article_days.unhelpful + $4`,
		site.ws, a.ID, up, down); err != nil {
		metrics.HelpCentrePage("feedback", "error")
		s.internal(w, r, err)
		return
	}
	metrics.HelpCentrePage("feedback", "ok")
	http.Redirect(w, r, back+"?thanks=1#feedback", http.StatusSeeOther)
}

// helpArticleMarkdown serves an article as Markdown, for AI assistants and
// the "Copy for AI" button.
func (s *Server) helpArticleMarkdown(w http.ResponseWriter, r *http.Request, site *helpSite, ref string) {
	c, err := s.helpContent(r.Context(), site)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	a, ok := findRef(ref, c.byID)
	if !ok {
		metrics.HelpCentrePage("markdown", "not_found")
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if !site.set.Indexing {
		w.Header().Set("X-Robots-Tag", "noindex")
	}
	fmt.Fprintf(w, "# %s\n\n%s\n", a.Title, strings.TrimSpace(a.Body))
	metrics.HelpCentrePage("markdown", "ok")
}

func (s *Server) helpSitemap(w http.ResponseWriter, r *http.Request) {
	site, ok := s.helpSiteFrom(w, r)
	if !ok {
		return
	}
	if !site.set.Indexing {
		http.NotFound(w, r)
		return
	}
	c, err := s.helpContent(r.Context(), site)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	root := strings.TrimSuffix(site.centre.URL, "/")
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	loc := func(u string, mod *time.Time) {
		b.WriteString("  <url><loc>" + template.HTMLEscapeString(u) + "</loc>")
		if mod != nil {
			b.WriteString("<lastmod>" + mod.UTC().Format("2006-01-02") + "</lastmod>")
		}
		b.WriteString("</url>\n")
	}
	loc(root+"/", nil)
	for _, f := range c.byFolder {
		if f.Count > 0 {
			loc(root+"/collections/"+f.Ref, nil)
		}
	}
	for _, a := range c.articles {
		if a.Kind == "article" {
			loc(root+"/articles/"+a.Ref, &a.Updated)
		}
	}
	b.WriteString("</urlset>\n")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
	metrics.HelpCentrePage("sitemap", "ok")
}

func (s *Server) helpRobots(w http.ResponseWriter, r *http.Request) {
	site, ok := s.helpSiteFrom(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if !site.set.Indexing {
		_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n"))
		return
	}
	root := strings.TrimSuffix(site.centre.URL, "/")
	fmt.Fprintf(w, "User-agent: *\nAllow: /\nSitemap: %s/sitemap.xml\n", root)
}

// helpLLMs is llms.txt: the help centre's articles as a list an AI
// assistant can follow, each linking to its Markdown.
func (s *Server) helpLLMs(w http.ResponseWriter, r *http.Request) {
	site, ok := s.helpSiteFrom(w, r)
	if !ok {
		return
	}
	c, err := s.helpContent(r.Context(), site)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	root := strings.TrimSuffix(site.centre.URL, "/")
	name := site.set.Title
	if name == "" {
		name = site.centre.Name
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", name)
	if site.set.HeroSubtitle != "" {
		fmt.Fprintf(&b, "> %s\n\n", site.set.HeroSubtitle)
	}
	var walk func(fs []*helpFolder, depth int)
	list := func(folder *string) {
		for _, a := range c.articles {
			if sameBrand(a.FolderID, folder) {
				fmt.Fprintf(&b, "- [%s](%s/articles/%s.md)", a.Title, root, a.Ref)
				if a.Excerpt != "" {
					fmt.Fprintf(&b, ": %s", truncate(a.Excerpt, 140))
				}
				b.WriteString("\n")
			}
		}
	}
	walk = func(fs []*helpFolder, depth int) {
		for _, f := range fs {
			fmt.Fprintf(&b, "\n%s %s\n\n", strings.Repeat("#", min(depth+2, 6)), f.Name)
			id := f.ID
			list(&id)
			walk(f.Children, depth+1)
		}
	}
	walk(c.folders, 0)
	b.WriteString("\n## Other\n\n")
	list(nil)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
	metrics.HelpCentrePage("llms", "ok")
}

// helpIcons are the icons a folder can have on the help centre.
var helpIcons = map[string]string{
	"book":     `<path d="M4 19.5v-15A2.5 2.5 0 0 1 6.5 2H20v20H6.5a2.5 2.5 0 0 1 0-5H20"/>`,
	"rocket":   `<path d="M4.5 16.5c-1.5 1.26-2 5-2 5s3.74-.5 5-2c.71-.84.7-2.13-.09-2.91a2.18 2.18 0 0 0-2.91-.09z"/><path d="m12 15-3-3a22 22 0 0 1 2-3.95A12.88 12.88 0 0 1 22 2c0 2.72-.78 7.5-6 11a22.35 22.35 0 0 1-4 2z"/><path d="M9 12H4s.55-3.03 2-4c1.62-1.08 5 0 5 0"/><path d="M12 15v5s3.03-.55 4-2c1.08-1.62 0-5 0-5"/>`,
	"card":     `<rect width="20" height="14" x="2" y="5" rx="2"/><path d="M2 10h20"/>`,
	"shield":   `<path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"/>`,
	"settings": `<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/>`,
	"users":    `<path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M22 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/>`,
	"chat":     `<path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/>`,
	"bolt":     `<path d="M13 2 3 14h9l-1 8 10-12h-9l1-8z"/>`,
	"star":     `<path d="m12 2 3.09 6.26L22 9.27l-5 4.87 1.18 6.88L12 17.77l-6.18 3.25L7 14.14 2 9.27l6.91-1.01L12 2z"/>`,
	"globe":    `<circle cx="12" cy="12" r="10"/><path d="M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20"/><path d="M2 12h20"/>`,
	"help":     `<circle cx="12" cy="12" r="10"/><path d="M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3"/><path d="M12 17h.01"/>`,
	"tag":      `<path d="M12 2H2v10l9.29 9.29c.94.94 2.48.94 3.42 0l6.58-6.58c.94-.94.94-2.48 0-3.42L12 2Z"/><path d="M7 7h.01"/>`,
	"box":      `<path d="M21 8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16Z"/><path d="m3.3 7 8.7 5 8.7-5"/><path d="M12 22V12"/>`,
	"chart":    `<path d="M3 3v18h18"/><path d="M7 16v-4"/><path d="M11 16V8"/><path d="M15 16v-6"/><path d="M19 16V5"/>`,
	"plug":     `<path d="M12 22v-5"/><path d="M9 8V2"/><path d="M15 8V2"/><path d="M18 8v5a4 4 0 0 1-4 4h-4a4 4 0 0 1-4-4V8Z"/>`,
	"lock":     `<rect width="18" height="11" x="3" y="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/>`,
}

// helpIconNames are the folder icons, in the order the editor offers them.
var helpIconNames = []string{"book", "rocket", "card", "shield", "settings", "users", "chat", "bolt", "star", "globe", "help", "tag", "box", "chart", "plug", "lock"}

func helpIcon(name string) template.HTML {
	p, ok := helpIcons[name]
	if !ok {
		p = helpIcons["book"]
	}
	return template.HTML(`<svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">` + p + `</svg>`) //nolint:gosec // fixed markup
}

var helpSocialIcons = map[string]template.HTML{
	"x":         `<svg viewBox="0 0 24 24" width="18" height="18" fill="currentColor" aria-hidden="true"><path d="M18.9 2H22l-7.6 8.7L23 22h-6.8l-5.3-6.9L4.8 22H1.7l8.1-9.3L1 2h7l4.8 6.3L18.9 2Zm-1.2 18h1.9L7.4 3.9H5.4L17.7 20Z"/></svg>`,
	"linkedin":  `<svg viewBox="0 0 24 24" width="18" height="18" fill="currentColor" aria-hidden="true"><path d="M20.45 20.45h-3.55v-5.57c0-1.33-.03-3.04-1.85-3.04-1.86 0-2.14 1.45-2.14 2.94v5.67H9.36V9h3.41v1.56h.05c.48-.9 1.64-1.85 3.37-1.85 3.6 0 4.27 2.37 4.27 5.46v6.28ZM5.34 7.43a2.06 2.06 0 1 1 0-4.12 2.06 2.06 0 0 1 0 4.12ZM7.12 20.45H3.56V9h3.56v11.45ZM22.22 0H1.77C.79 0 0 .77 0 1.73v20.54C0 23.23.79 24 1.77 24h20.45c.98 0 1.78-.77 1.78-1.73V1.73C24 .77 23.2 0 22.22 0Z"/></svg>`,
	"youtube":   `<svg viewBox="0 0 24 24" width="18" height="18" fill="currentColor" aria-hidden="true"><path d="M23.5 6.2a3 3 0 0 0-2.1-2.1C19.5 3.6 12 3.6 12 3.6s-7.5 0-9.4.5A3 3 0 0 0 .5 6.2 31 31 0 0 0 0 12a31 31 0 0 0 .5 5.8 3 3 0 0 0 2.1 2.1c1.9.5 9.4.5 9.4.5s7.5 0 9.4-.5a3 3 0 0 0 2.1-2.1A31 31 0 0 0 24 12a31 31 0 0 0-.5-5.8ZM9.6 15.6V8.4l6.3 3.6-6.3 3.6Z"/></svg>`,
	"facebook":  `<svg viewBox="0 0 24 24" width="18" height="18" fill="currentColor" aria-hidden="true"><path d="M24 12.07C24 5.4 18.63 0 12 0S0 5.4 0 12.07C0 18.1 4.39 23.1 10.13 24v-8.44H7.08v-3.49h3.04V9.41c0-3.02 1.8-4.7 4.54-4.7 1.31 0 2.68.24 2.68.24v2.97h-1.5c-1.5 0-1.96.93-1.96 1.89v2.26h3.33l-.53 3.49h-2.8V24C19.62 23.1 24 18.1 24 12.07Z"/></svg>`,
	"instagram": `<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><rect x="2" y="2" width="20" height="20" rx="5"/><circle cx="12" cy="12" r="4"/><circle cx="17.5" cy="6.5" r=".5" fill="currentColor"/></svg>`,
	"github":    `<svg viewBox="0 0 24 24" width="18" height="18" fill="currentColor" aria-hidden="true"><path d="M12 .3a12 12 0 0 0-3.8 23.4c.6.1.8-.3.8-.6v-2.2c-3.3.7-4-1.6-4-1.6-.6-1.4-1.4-1.8-1.4-1.8-1.1-.7.1-.7.1-.7 1.2.1 1.8 1.2 1.8 1.2 1.1 1.8 2.8 1.3 3.5 1 .1-.8.4-1.3.8-1.6-2.7-.3-5.5-1.3-5.5-6 0-1.2.5-2.3 1.2-3.1-.1-.4-.5-1.6.1-3.2 0 0 1-.3 3.3 1.2a11.5 11.5 0 0 1 6 0C17.3 4.7 18.3 5 18.3 5c.7 1.6.2 2.8.1 3.2.8.8 1.2 1.9 1.2 3.1 0 4.7-2.8 5.7-5.5 6 .4.4.8 1.1.8 2.2v3.3c0 .3.2.7.8.6A12 12 0 0 0 12 .3Z"/></svg>`,
}
