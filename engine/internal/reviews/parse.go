package reviews

import (
	"encoding/json"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"
)

// Review is one review as its page shows it. Rating is on a 1 to 5 scale
// whatever the platform uses; RatingNative is as shown.
type Review struct {
	ExternalID   string
	URL          string
	Rating       float64
	RatingNative float64
	RatingScale  float64
	Title        string
	Body         string
	Language     string
	Author       string
	Country      string
	Verified     bool
	PostedAt     time.Time
	EditedAt     *time.Time
	Reply        string
	RepliedAt    *time.Time
}

// Read is what one page of a review page shows: the platform's headline
// score (on a 1 to 5 scale) and review count, and the reviews on it. A
// number the page doesn't show is nil, never zero. More says the page
// links to an older page of reviews.
type Read struct {
	Name    string
	Score   *float64
	Count   *int64
	Reviews []Review
	More    bool
}

// Found says whether the page was a review page at all, rather than a
// login wall, a challenge or an error page.
func (r Read) Found() bool { return r.Score != nil || r.Count != nil || len(r.Reviews) > 0 }

// Limits on what is kept of a review, so one page can't fill the store.
const (
	maxTitle  = 300
	maxBody   = 10000
	maxAuthor = 200
)

// Parse reads one page of a platform's reviews. now dates Google's
// "2 weeks ago".
func Parse(platform, page string, now time.Time) Read {
	var r Read
	// fromData is a page whose own data says whether a next page exists.
	fromData := false
	switch platform {
	case Trustpilot:
		r = trustpilotNextData(page)
	case Capterra:
		r = capterraData(page)
		// Capterra draws a "Next page" button on every page, even the
		// only one, so its data decides.
		fromData = len(r.Reviews) > 0
	}
	if r.Score == nil || r.Count == nil || len(r.Reviews) == 0 {
		r = mergeRead(r, jsonLD(page))
	}
	if platform == Google {
		r = mergeRead(r, googleMaps(page, now))
	}
	if !r.More && !fromData {
		r.More = hasNextPage(page)
	}
	for i := range r.Reviews {
		x := &r.Reviews[i]
		x.Title = clip(x.Title, maxTitle)
		x.Body = clip(x.Body, maxBody)
		x.Author = clip(x.Author, maxAuthor)
		x.Reply = clip(x.Reply, maxBody)
		if x.ExternalID == "" {
			x.ExternalID = fingerprint(*x)
		}
	}
	return r
}

func mergeRead(a, b Read) Read {
	if a.Name == "" {
		a.Name = b.Name
	}
	if a.Score == nil {
		a.Score = b.Score
	}
	if a.Count == nil {
		a.Count = b.Count
	}
	if len(a.Reviews) == 0 {
		a.Reviews = b.Reviews
	}
	a.More = a.More || b.More
	return a
}

// clip trims text to at most n characters.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// fingerprint names a review that has no id of its own on its page by
// who wrote it, when and what it says.
func fingerprint(x Review) string {
	t := x.Body
	if r := []rune(t); len(r) > 80 {
		t = string(r[:80])
	}
	day := ""
	if !x.PostedAt.IsZero() {
		day = x.PostedAt.UTC().Format("2006-01-02")
	}
	return "fp:" + strings.ToLower(x.Author) + "|" + day + "|" + strings.ToLower(t)
}

var (
	ldScript   = regexp.MustCompile(`(?is)<script[^>]*type=["']application/ld\+json["'][^>]*>(.*?)</script>`)
	nextScript = regexp.MustCompile(`(?is)<script[^>]*id=["']__NEXT_DATA__["'][^>]*>(.*?)</script>`)
	nextLink   = regexp.MustCompile(`(?i)<link[^>]+rel=["']next["']|rel=["']next["'][^>]*>|aria-label=["']Next page["']|name=["']pagination-button-next["']`)
)

func hasNextPage(page string) bool { return nextLink.MatchString(page) }

// jsonLD reads schema.org data: an aggregateRating on the product or
// business, and its Review items. Trustpilot, G2 and Capterra all publish
// it for search engines.
func jsonLD(page string) Read {
	var r Read
	seen := map[string]bool{}
	for _, m := range ldScript.FindAllStringSubmatch(page, 50) {
		var v any
		if json.Unmarshal([]byte(strings.TrimSpace(m[1])), &v) != nil {
			continue
		}
		walkLD(v, &r, seen, 0)
	}
	return r
}

func walkLD(v any, r *Read, seen map[string]bool, depth int) {
	if depth > 8 {
		return
	}
	switch t := v.(type) {
	case []any:
		for _, x := range t {
			walkLD(x, r, seen, depth+1)
		}
	case map[string]any:
		if hasType(t, "Review") {
			if x, ok := ldReview(t); ok {
				key := x.ExternalID
				if key == "" {
					key = fingerprint(x)
				}
				if !seen[key] {
					seen[key] = true
					r.Reviews = append(r.Reviews, x)
				}
			}
			return
		}
		if agg, ok := t["aggregateRating"].(map[string]any); ok && r.Score == nil {
			best := num(agg["bestRating"])
			if s := num(agg["ratingValue"]); s != nil {
				v := toFive(*s, best)
				r.Score = &v
			}
			if c := num(agg["reviewCount"]); c != nil {
				n := int64(*c)
				r.Count = &n
			} else if c := num(agg["ratingCount"]); c != nil {
				n := int64(*c)
				r.Count = &n
			}
			if n, ok := t["name"].(string); ok && r.Name == "" {
				r.Name = clip(html.UnescapeString(n), 300)
			}
		}
		for k, x := range t {
			if k == "aggregateRating" {
				continue
			}
			walkLD(x, r, seen, depth+1)
		}
	}
}

func hasType(m map[string]any, want string) bool {
	switch t := m["@type"].(type) {
	case string:
		return t == want
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

func ldReview(m map[string]any) (Review, bool) {
	var x Review
	rating, _ := m["reviewRating"].(map[string]any)
	s := num(rating["ratingValue"])
	if s == nil {
		return x, false
	}
	best := num(rating["bestRating"])
	x.RatingNative = *s
	x.RatingScale = 5
	if best != nil && *best > 0 {
		x.RatingScale = *best
	}
	x.Rating = toFive(*s, best)
	x.Title = text(m["headline"])
	if x.Title == "" {
		x.Title = text(m["name"])
	}
	x.Body = text(m["reviewBody"])
	if x.Body == "" {
		x.Body = text(m["description"])
	}
	x.Language = text(m["inLanguage"])
	switch a := m["author"].(type) {
	case map[string]any:
		x.Author = text(a["name"])
	case string:
		x.Author = text(a)
	}
	x.PostedAt = date(text(m["datePublished"]))
	if d := date(text(m["dateModified"])); !d.IsZero() && d.After(x.PostedAt) {
		x.EditedAt = &d
	}
	id := text(m["@id"])
	if id == "" {
		id = text(m["url"])
	}
	if strings.HasPrefix(id, "http") {
		x.URL = id
	}
	x.ExternalID = id
	return x, true
}

// trustpilotNextData reads the page's own data, which also has the
// business's replies, the verified label and the reviewer's country.
func trustpilotNextData(page string) Read {
	var r Read
	m := nextScript.FindStringSubmatch(page)
	if m == nil {
		return r
	}
	var doc struct {
		Props struct {
			PageProps struct {
				BusinessUnit struct {
					DisplayName     string   `json:"displayName"`
					TrustScore      *float64 `json:"trustScore"`
					NumberOfReviews *int64   `json:"numberOfReviews"`
				} `json:"businessUnit"`
				Reviews []struct {
					ID       string `json:"id"`
					Rating   float64
					Title    string
					Text     string
					Language string
					Dates    struct {
						PublishedDate string `json:"publishedDate"`
						UpdatedDate   string `json:"updatedDate"`
					}
					Consumer struct {
						DisplayName string `json:"displayName"`
						CountryCode string `json:"countryCode"`
					}
					Labels struct {
						Verification struct {
							IsVerified bool `json:"isVerified"`
						}
					}
					Reply *struct {
						Message       string `json:"message"`
						PublishedDate string `json:"publishedDate"`
					}
				} `json:"reviews"`
				Filters struct {
					Pagination struct {
						CurrentPage int `json:"currentPage"`
						TotalPages  int `json:"totalPages"`
					}
				}
			} `json:"pageProps"`
		}
	}
	if json.Unmarshal([]byte(m[1]), &doc) != nil {
		return r
	}
	p := doc.Props.PageProps
	r.Name = clip(p.BusinessUnit.DisplayName, 300)
	r.Score, r.Count = p.BusinessUnit.TrustScore, p.BusinessUnit.NumberOfReviews
	if pg := p.Filters.Pagination; pg.TotalPages > 0 {
		r.More = pg.CurrentPage < pg.TotalPages
	}
	for _, v := range p.Reviews {
		if v.ID == "" || v.Rating < 1 {
			continue
		}
		x := Review{ExternalID: v.ID, URL: "https://www.trustpilot.com/reviews/" + v.ID, Rating: v.Rating, RatingNative: v.Rating, RatingScale: 5,
			Title: v.Title, Body: v.Text, Language: v.Language, Author: v.Consumer.DisplayName, Country: v.Consumer.CountryCode,
			Verified: v.Labels.Verification.IsVerified, PostedAt: date(v.Dates.PublishedDate)}
		if d := date(v.Dates.UpdatedDate); !d.IsZero() && d.After(x.PostedAt) {
			x.EditedAt = &d
		}
		if v.Reply != nil && strings.TrimSpace(v.Reply.Message) != "" {
			x.Reply = v.Reply.Message
			if d := date(v.Reply.PublishedDate); !d.IsZero() {
				x.RepliedAt = &d
			}
		}
		r.Reviews = append(r.Reviews, x)
	}
	return r
}

var (
	nextFlight   = regexp.MustCompile(`self\.__next_f\.push\(\[1,("(?:[^"\\]|\\.)*")\]\)`)
	capterraNext = regexp.MustCompile(`href="[^"]*/reviews/\?(?:[^"]*&(?:amp;)?)?page=[0-9]+"`)
)

// capterraData reads the reviews in the data Capterra's pages carry for
// the browser (its JSON-LD has no dates or ids): each review's id, stars,
// date, text, reviewer and the vendor's reply. The reviews page and the
// product page, where Capterra sends a product with few reviews, name
// the same things differently, so both are read.
func capterraData(page string) Read {
	var r Read
	var b strings.Builder
	for _, m := range nextFlight.FindAllStringSubmatch(page, -1) {
		var chunk string
		if json.Unmarshal([]byte(m[1]), &chunk) == nil {
			b.WriteString(chunk)
		}
	}
	data := b.String()
	seen := map[string]bool{}
	const key = `"textReviews":`
	for rest := data; ; {
		i := strings.Index(rest, key)
		if i < 0 {
			break
		}
		rest = rest[i+len(key):]
		var list []map[string]any
		if json.NewDecoder(strings.NewReader(rest)).Decode(&list) != nil {
			continue
		}
		for _, v := range list {
			x, ok := capterraReview(v)
			if !ok || seen[x.ExternalID] {
				continue
			}
			seen[x.ExternalID] = true
			r.Reviews = append(r.Reviews, x)
			if len(r.Reviews) >= 200 {
				break
			}
		}
	}
	r.More = len(r.Reviews) > 0 && capterraNext.MatchString(page)
	return r
}

func capterraReview(v map[string]any) (Review, bool) {
	var x Review
	x.ExternalID = text(v["reviewId"])
	if x.ExternalID == "" {
		x.ExternalID = text(v["id"])
	}
	s := num(v["overallRating"])
	if s == nil {
		s = num(v["rating"])
	}
	if x.ExternalID == "" || s == nil || *s < 1 {
		return x, false
	}
	x.Rating, x.RatingNative, x.RatingScale = toFive(*s, nil), *s, 5
	x.Title = text(v["title"])
	x.PostedAt = date(firstText(v, "writtenOn", "date"))
	var parts []string
	if t := firstText(v, "generalComments", "content"); t != "" {
		parts = append(parts, t)
	}
	if t := firstText(v, "prosText", "pros"); t != "" {
		parts = append(parts, "Pros: "+t)
	}
	if t := firstText(v, "consText", "cons"); t != "" {
		parts = append(parts, "Cons: "+t)
	}
	x.Body = strings.Join(parts, "\n\n")
	if who, ok := v["reviewer"].(map[string]any); ok {
		x.Author = firstText(who, "fullName", "name")
		x.Verified, _ = who["isValidated"].(bool)
	}
	if rep, ok := v["vendorResponse"].(map[string]any); ok {
		x.Reply = firstText(rep, "response", "text", "message", "content")
		if d := date(firstText(rep, "date", "writtenOn", "createdAt")); x.Reply != "" && !d.IsZero() {
			x.RepliedAt = &d
		}
	}
	return x, true
}

// firstText is the first of a map's keys that holds text.
func firstText(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if t := text(m[k]); t != "" {
			return t
		}
	}
	return ""
}

var (
	gScore   = regexp.MustCompile(`(?i)aria-label="([1-5](?:[.,][0-9])?) stars?\s*"`)
	gCount   = regexp.MustCompile(`(?i)([0-9][0-9,.\s]{0,12})\s+reviews?\b`)
	gReview  = regexp.MustCompile(`(?is)data-review-id="([^"]{4,200})"(.*?)(?:data-review-id="|$)`)
	gAuthor  = regexp.MustCompile(`(?i)aria-label="Photo of ([^"]{1,200})"`)
	gStars   = regexp.MustCompile(`(?i)aria-label="\s*([1-5]) stars?\s*"`)
	gText    = regexp.MustCompile(`(?is)<span class="wiI7pd"[^>]*>(.*?)</span>`)
	gWhen    = regexp.MustCompile(`(?i)>\s*((?:a|an|[0-9]{1,3}) (?:minute|hour|day|week|month|year)s? ago)\s*<`)
	gReplyBy = regexp.MustCompile(`(?is)Response from the owner.*?<div class="wiI7pd"[^>]*>(.*?)</div>`)
	tags     = regexp.MustCompile(`(?s)<[^>]*>`)
)

// googleMaps reads a Google Maps place as the page reader renders it: its
// rating and review count, and the reviews it shows.
func googleMaps(page string, now time.Time) Read {
	var r Read
	if m := gScore.FindStringSubmatch(page); m != nil {
		if f, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", "."), 64); err == nil {
			r.Score = &f
		}
	}
	if m := gCount.FindStringSubmatch(page); m != nil {
		digits := strings.Map(func(c rune) rune {
			if c >= '0' && c <= '9' {
				return c
			}
			return -1
		}, m[1])
		if n, err := strconv.ParseInt(digits, 10, 64); err == nil {
			r.Count = &n
		}
	}
	rest := page
	for {
		m := gReview.FindStringSubmatchIndex(rest)
		if m == nil {
			break
		}
		id, block := rest[m[2]:m[3]], rest[m[4]:m[5]]
		rest = rest[m[5]:]
		var x Review
		x.ExternalID = "g:" + id
		if a := gAuthor.FindStringSubmatch(block); a != nil {
			x.Author = html.UnescapeString(a[1])
		}
		s := gStars.FindStringSubmatch(block)
		if s == nil {
			continue
		}
		x.Rating, _ = strconv.ParseFloat(s[1], 64)
		x.RatingNative, x.RatingScale = x.Rating, 5
		if t := gText.FindStringSubmatch(block); t != nil {
			x.Body = html.UnescapeString(tags.ReplaceAllString(t[1], ""))
		}
		if w := gWhen.FindStringSubmatch(block); w != nil {
			x.PostedAt = ago(w[1], now)
		}
		if rep := gReplyBy.FindStringSubmatch(block); rep != nil {
			x.Reply = html.UnescapeString(tags.ReplaceAllString(rep[1], ""))
		}
		r.Reviews = append(r.Reviews, x)
		if len(r.Reviews) >= 200 {
			break
		}
	}
	return r
}

// ago turns "3 weeks ago" into a date, to the day.
func ago(s string, now time.Time) time.Time {
	f := strings.Fields(strings.ToLower(s))
	if len(f) < 2 {
		return time.Time{}
	}
	n := 1
	if v, err := strconv.Atoi(f[0]); err == nil {
		n = v
	}
	d := now.UTC().Truncate(24 * time.Hour)
	switch strings.TrimSuffix(f[1], "s") {
	case "minute", "hour":
		return d
	case "day":
		return d.AddDate(0, 0, -n)
	case "week":
		return d.AddDate(0, 0, -7*n)
	case "month":
		return d.AddDate(0, -n, 0)
	case "year":
		return d.AddDate(-n, 0, 0)
	}
	return time.Time{}
}

// toFive puts a rating on a 1 to 5 scale.
func toFive(v float64, best *float64) float64 {
	if best != nil && *best > 0 && *best != 5 {
		v = v / *best * 5
	}
	return min(max(v, 0), 5)
}

func num(v any) *float64 {
	switch t := v.(type) {
	case float64:
		return &t
	case string:
		f, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(t), ",", "."), 64)
		if err == nil {
			return &f
		}
	}
	return nil
}

func text(v any) string {
	s, _ := v.(string)
	s = html.UnescapeString(s)
	if strings.Contains(s, "<") {
		if doc, err := xhtml.Parse(strings.NewReader(s)); err == nil {
			var b strings.Builder
			var walk func(n *xhtml.Node)
			walk = func(n *xhtml.Node) {
				if n.Type == xhtml.TextNode {
					b.WriteString(n.Data)
				}
				if n.Type == xhtml.ElementNode && (n.Data == "br" || n.Data == "p") {
					b.WriteString("\n")
				}
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
			}
			walk(doc)
			s = b.String()
		}
	}
	return strings.TrimSpace(s)
}

// date reads the dates review pages use: RFC 3339, a bare day, or
// Capterra's "September 5, 2026".
func date(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02", "January 2, 2006", "Jan 2, 2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
