package connectors

import "strings"

// challengeMarks are what bot checks and access walls put on the page
// they show instead of the one asked for (Cloudflare, DataDome,
// PerimeterX, Imperva and the sites' own).
var challengeMarks = []string{
	"<title>just a moment", "cf-challenge", "cf_chl_", "challenges.cloudflare.com", "attention required! | cloudflare",
	"captcha-delivery.com", "datadome", "px-captcha", "perimeterx", "_incapsula_resource", "access denied</title>",
	"<title>access denied", "verify you are human", "are you a robot", "unusual traffic from your computer",
}

// Blocked says whether a page a PageFetcher read, that showed nothing a
// parser could use, is a bot check or an access wall rather than the page
// asked for: the site refused the read, so trying again (in a browser,
// later) can work, and the parser isn't to blame. Ask only about a page a
// parser found nothing on: a real page can load a bot check's script too.

func (p FetchedPage) Blocked() bool {
	if p.Status == 403 || p.Status == 429 || p.Status == 503 {
		return true
	}
	head := p.HTML
	if len(head) > 200_000 {
		head = head[:200_000]
	}
	head = strings.ToLower(head)
	for _, m := range challengeMarks {
		if strings.Contains(head, m) {
			return true
		}
	}
	return false
}
