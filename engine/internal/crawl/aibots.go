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
}

// AIBots are the AI crawlers checked, search and user agents first. Tokens
// are a promise: add new ones, never remove one.
var AIBots = []AIBot{
	{"OAI-SearchBot", "OpenAI", "search"},
	{"ChatGPT-User", "OpenAI", "user"},
	{"Claude-SearchBot", "Anthropic", "search"},
	{"Claude-User", "Anthropic", "user"},
	{"PerplexityBot", "Perplexity", "search"},
	{"Perplexity-User", "Perplexity", "user"},
	{"DuckAssistBot", "DuckDuckGo", "search"},
	{"MistralAI-User", "Mistral", "user"},
	{"Amazonbot", "Amazon", "search"},
	{"GPTBot", "OpenAI", "training"},
	{"ClaudeBot", "Anthropic", "training"},
	{"Google-Extended", "Google", "training"},
	{"Applebot-Extended", "Apple", "training"},
	{"Meta-ExternalAgent", "Meta", "training"},
	{"CCBot", "Common Crawl", "training"},
	{"Bytespider", "ByteDance", "training"},
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
