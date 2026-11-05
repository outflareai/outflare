package coverage

import (
	"net/url"
	"strings"
)

// Classify guesses a found page's placement type from its address alone:
// a YouTube video, a forum thread, an influencer's post, a review site, an
// MCP directory, an app store listing, a comparison or a roundup. It
// returns "" when the address says nothing, so a person picks the type.
// It never reads the page.
func Classify(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	path := strings.ToLower(u.Path)
	on := func(domains ...string) bool {
		for _, d := range domains {
			if host == d || strings.HasSuffix(host, "."+d) {
				return true
			}
		}
		return false
	}
	switch {
	case on("youtube.com", "youtu.be"):
		return "youtube"
	case on("smithery.ai", "mcp.so", "pulsemcp.com", "mcpservers.org", "mcpmarket.com", "mcp.directory", "mcpserverfinder.com", "cursor.directory"),
		on("glama.ai") && strings.HasPrefix(path, "/mcp"),
		on("github.com") && strings.Contains(path, "awesome-mcp"):
		return "mcp_directory"
	case on("apps.apple.com", "play.google.com", "chromewebstore.google.com", "chrome.google.com", "addons.mozilla.org", "marketplace.atlassian.com", "appexchange.salesforce.com", "apps.shopify.com", "marketplace.zoom.us", "slack.com") && (!on("slack.com") || strings.HasPrefix(path, "/apps")),
		on("zapier.com") && strings.HasPrefix(path, "/apps"),
		on("appsource.microsoft.com", "workspace.google.com") && (on("appsource.microsoft.com") || strings.HasPrefix(path, "/marketplace")):
		return "app_listing"
	case on("g2.com", "capterra.com", "trustpilot.com", "trustradius.com", "getapp.com", "softwareadvice.com", "gartner.com") && (!on("gartner.com") || strings.Contains(path, "/reviews")),
		on("producthunt.com") && strings.Contains(path, "/reviews"):
		return "review"
	case on("producthunt.com", "alternativeto.net", "saashub.com", "crunchbase.com", "theresanaiforthat.com", "futurepedia.io"):
		return "directory"
	case on("reddit.com", "news.ycombinator.com", "quora.com", "stackoverflow.com", "stackexchange.com", "indiehackers.com", "discord.com", "community.openai.com"),
		strings.HasPrefix(host, "forum.") || strings.HasPrefix(host, "community.") || strings.HasPrefix(host, "discuss."):
		return "forum"
	case on("instagram.com", "tiktok.com", "x.com", "twitter.com", "threads.net", "facebook.com", "bsky.app"),
		on("linkedin.com") && (strings.HasPrefix(path, "/posts") || strings.HasPrefix(path, "/feed") || strings.HasPrefix(path, "/pulse")):
		return "influencer"
	case on("vimeo.com", "loom.com", "twitch.tv"):
		return "video"
	case on("podcasts.apple.com", "open.spotify.com") || strings.Contains(path, "/podcast"):
		return "podcast"
	case on("substack.com") || strings.Contains(path, "/newsletter"):
		return "newsletter"
	case on("medium.com", "dev.to", "hashnode.dev", "hackernoon.com"):
		return "blog"
	}
	switch {
	case strings.Contains(path, "-vs-") || strings.Contains(path, "/vs/") || strings.Contains(path, "-versus-") || strings.Contains(path, "/compare"):
		return "comparison"
	case strings.Contains(path, "alternatives") || strings.Contains(path, "/best-") || strings.Contains(path, "/top-"):
		return "roundup"
	case strings.Contains(path, "/review"):
		return "review"
	case strings.Contains(path, "/case-stud"):
		return "case_study"
	case strings.Contains(path, "/interview"):
		return "interview"
	case strings.Contains(path, "/integrations/") || strings.Contains(path, "/integration/"):
		return "integration"
	case strings.Contains(path, "/partners/"):
		return "partner"
	case strings.Contains(path, "/blog/") || strings.HasPrefix(host, "blog."):
		return "blog"
	case strings.Contains(path, "/news/") || strings.Contains(path, "/press/"):
		return "news"
	}
	return ""
}
