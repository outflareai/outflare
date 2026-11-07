package crawl

import (
	"context"
	"net/url"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// AI crawlability: the crawlers AI products send, and what robots.txt
// lets each of them read. A crawl works this out from the robots.txt it
// already read, so it costs no extra request; it also checks /llms.txt.

// AIBot is an AI product's crawler, by its robots.txt token.
type AIBot struct {
	Token  string `json:"token"`
	Vendor string `json:"vendor"`
	// Purpose: search (finds pages AI answers cite), user (fetches a page
	// a person asked about) or training (collects pages to train models).
	Purpose string `json:"purpose"`
	// Feeds names the AI answers the crawler reads pages for, in plain
	// words, such as "Google AI Overviews and AI Mode".
	Feeds string `json:"feeds"`
	// MayIgnoreRobots: its company says it may not follow robots.txt,
	// because a person asked for the page. Blocking it there may not work.
	MayIgnoreRobots bool `json:"may_ignore_robots"`
	// SearchEngine marks a search engine's own crawler (Googlebot,
	// Bingbot): AI answers are built from its index, but most of its
	// visits are for ordinary search, so request logs don't count it as
	// an AI crawler.
	SearchEngine bool `json:"search_engine"`
}

// AIBots are the AI crawlers checked, search and user agents first, each
// as its company documents it. Tokens are a promise: add new ones, never
// remove one.
var AIBots = []AIBot{
	{Token: "Googlebot", Vendor: "Google", Purpose: "search", Feeds: "Google AI Overviews and AI Mode (Google-Extended doesn't control these)", SearchEngine: true},
	{Token: "Bingbot", Vendor: "Microsoft", Purpose: "search", Feeds: "Microsoft Copilot and Bing's AI answers, and other assistants that search Bing", SearchEngine: true},
	{Token: "OAI-SearchBot", Vendor: "OpenAI", Purpose: "search", Feeds: "ChatGPT search"},
	{Token: "ChatGPT-User", Vendor: "OpenAI", Purpose: "user", Feeds: "ChatGPT, when a person asks about a page", MayIgnoreRobots: true},
	{Token: "Claude-SearchBot", Vendor: "Anthropic", Purpose: "search", Feeds: "Claude's web search"},
	{Token: "Claude-User", Vendor: "Anthropic", Purpose: "user", Feeds: "Claude, when a person asks about a page"},
	{Token: "PerplexityBot", Vendor: "Perplexity", Purpose: "search", Feeds: "Perplexity answers"},
	{Token: "Perplexity-User", Vendor: "Perplexity", Purpose: "user", Feeds: "Perplexity, when a person asks about a page", MayIgnoreRobots: true},
	{Token: "DuckAssistBot", Vendor: "DuckDuckGo", Purpose: "search", Feeds: "DuckDuckGo's AI-assisted answers"},
	{Token: "MistralAI-User", Vendor: "Mistral", Purpose: "user", Feeds: "Mistral's assistant, when a person asks about a page"},
	{Token: "Amazonbot", Vendor: "Amazon", Purpose: "training", Feeds: "Amazon's products and AI models"},
	{Token: "GPTBot", Vendor: "OpenAI", Purpose: "training", Feeds: "Training OpenAI's models"},
	{Token: "ClaudeBot", Vendor: "Anthropic", Purpose: "training", Feeds: "Training Anthropic's models"},
	{Token: "Google-Extended", Vendor: "Google", Purpose: "training", Feeds: "Training Gemini, and grounding in Gemini apps"},
	{Token: "Applebot-Extended", Vendor: "Apple", Purpose: "training", Feeds: "Training Apple's models"},
	{Token: "Meta-ExternalAgent", Vendor: "Meta", Purpose: "training", Feeds: "Training Meta's models"},
	{Token: "CCBot", Vendor: "Common Crawl", Purpose: "training", Feeds: "Common Crawl's open dataset, used to train many models"},
	{Token: "Bytespider", Vendor: "ByteDance", Purpose: "training", Feeds: "Training ByteDance's models"},
	// Added later, grouped as above.
	{Token: "Applebot", Vendor: "Apple", Purpose: "search", Feeds: "Siri, Spotlight and Safari answers"},
	{Token: "meta-webindexer", Vendor: "Meta", Purpose: "search", Feeds: "Meta AI's answers and citations"},
	{Token: "Amzn-SearchBot", Vendor: "Amazon", Purpose: "search", Feeds: "Alexa and Amazon's search"},
	{Token: "MistralAI-Index", Vendor: "Mistral", Purpose: "search", Feeds: "Mistral's search"},
	{Token: "Meta-ExternalFetcher", Vendor: "Meta", Purpose: "user", Feeds: "Meta AI, when a person asks about a page", MayIgnoreRobots: true},
	{Token: "Amzn-User", Vendor: "Amazon", Purpose: "user", Feeds: "Alexa, when a person asks something live", MayIgnoreRobots: true},
	{Token: "MistralAI-Training", Vendor: "Mistral", Purpose: "training", Feeds: "Training Mistral's models"},
}

// aiAccess returns, per page id, the search and user AI crawlers that
// robots.txt keeps from the page, and the per-crawler summary.
func aiAccess(pages []AuditPage, robots map[string]robotsFile, start string) (map[int64][]string, []aiBotRow) {
	parsed := map[string]map[string]*Robots{} // host -> token -> rules
	rulesFor := func(host, token string) *Robots {
		byToken, ok := parsed[host]
		if !ok {
			byToken = map[string]*Robots{}
			parsed[host] = byToken
		}
		rb, ok := byToken[token]
		if !ok {
			f, known := robots[host]
			if !known {
				rb = AllowAll() // a host the crawl never read robots.txt for
			} else {
				rb = robotsFrom(f.status, f.body, token)
			}
			byToken[token] = rb
		}
		return rb
	}
	blocked := map[int64][]string{}
	rows := make([]aiBotRow, 0, len(AIBots))
	for _, b := range AIBots {
		row := aiBotRow{AIBot: b, RootAllowed: true}
		if u, err := url.Parse(start); err == nil {
			row.RootAllowed = rulesFor(strings.ToLower(u.Host), b.Token).Allowed(pathOf(u))
		}
		for i := range pages {
			p := &pages[i]
			if !p.html() {
				continue
			}
			u, err := url.Parse(p.URL)
			if err != nil {
				continue
			}
			row.Pages++
			if !rulesFor(strings.ToLower(u.Host), b.Token).Allowed(pathOf(u)) {
				row.Blocked++
				if b.Purpose != "training" {
					blocked[p.ID] = append(blocked[p.ID], b.Token)
				}
			}
		}
		rows = append(rows, row)
	}
	for id := range blocked {
		sort.Strings(blocked[id])
	}
	return blocked, rows
}

func pathOf(u *url.URL) string {
	p := u.EscapedPath()
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return p
}

type robotsFile struct {
	status int
	body   string
}

type aiBotRow struct {
	AIBot
	RootAllowed bool
	Pages       int
	Blocked     int
}

func loadRobotsFiles(ctx context.Context, tx pgx.Tx, runID string) (map[string]robotsFile, error) {
	rows, err := tx.Query(ctx, `SELECT host, status, body FROM crawl_robots WHERE run_id = $1`, runID)
	if err != nil {
		return nil, err
	}
	out := map[string]robotsFile{}
	var host string
	var f robotsFile
	_, err = pgx.ForEachRow(rows, []any{&host, &f.status, &f.body}, func() error {
		out[host] = f
		return nil
	})
	return out, err
}

// maxLLMSTxtBytes is how much of an llms.txt is read: only its size and
// whether it is there matter.
const maxLLMSTxtBytes = 256 << 10

// UsageSignals are the lines of a robots.txt that state how content may
// be used once read, rather than what may be crawled: Cloudflare's
// Content-Signal (search=yes, ai-train=no, ai-input=yes) and the IETF AI
// Preferences draft's Content-Usage. Crawlers that support them treat
// them as the site's wishes, such as "find me in AI search, don't train
// on me". At most maxUsageSignals, each clipped.
func UsageSignals(body string) []string {
	var out []string
	seen := map[string]bool{}
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "content-signal", "content-usage":
			l := clip(strings.TrimSpace(k)+": "+strings.TrimSpace(v), 300)
			if !seen[l] && len(out) < maxUsageSignals {
				seen[l] = true
				out = append(out, l)
			}
		}
	}
	return out
}

const maxUsageSignals = 5
