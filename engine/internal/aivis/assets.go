package aivis

import (
	"context"
	"strings"

	"github.com/outflareai/outflare/engine/internal/jobs"
)

// Assistant assets: the images and files an assistant shows from its own
// servers (ChatGPT's images.openai.com) are links in its answer, not pages
// anyone can be placed on. They are kept out of citations, pages read,
// placements and opportunities.

// assetHosts are the hosts, and the parents of hosts, that serve an
// assistant's own images and files.
var assetHosts = []string{"images.openai.com", "oaiusercontent.com", "gstatic.com"}

// AssetHost reports whether host serves an assistant's own images or files.
func AssetHost(host string) bool {
	host = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(host)), "www.")
	for _, h := range assetHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// NotAssetSQL is SQL that is true when the host column col isn't an
// assistant's own image or file host.
func NotAssetSQL(col string) string {
	parts := make([]string, 0, len(assetHosts))
	for _, h := range assetHosts {
		parts = append(parts, col+" <> '"+h+"' AND "+col+" NOT LIKE '%."+h+"'")
	}
	return "(" + strings.Join(parts, " AND ") + ")"
}

// JobPageChecks reads pages AI answers cite, to see whether each links to
// or only names the brand and its competitors. Package api handles it.
const JobPageChecks = "ai_visibility.page_checks"

// PageChecksPayload is a page checks job: one page when URL is set, else
// the brand's most cited pages that weren't checked lately.
type PageChecksPayload struct {
	BrandID string `json:"brand_id"`
	URL     string `json:"url,omitempty"`
}

// EnqueuePageChecks queues a page checks job, once per key.
func EnqueuePageChecks(ctx context.Context, q jobs.Querier, ws, brand, pageURL, key string) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobPageChecks, WorkspaceID: &ws, Payload: PageChecksPayload{BrandID: brand, URL: pageURL},
		IdempotencyKey: JobPageChecks + ":" + brand + ":" + key, MaxAttempts: 3})
	return err
}
