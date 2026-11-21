// Package podcasts searches podcast episodes on a workspace's own account
// with a podcast directory. Podcast Index (podcastindex.org) is an open
// directory of every public podcast; its API is free with a key and secret
// the workspace makes for itself.
package podcasts

import (
	"context"
	"crypto/sha1" //nolint:gosec // Podcast Index signs requests with SHA-1; it is their scheme, not ours.
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// All returns the connectors this package adds to the catalog.
func All() []c.Connector { return []c.Connector{PodcastIndex{}} }

// PodcastIndex searches episodes on the workspace's own Podcast Index API
// key (https://podcastindex-org.github.io/docs-api/).
type PodcastIndex struct{}

const (
	// PodcastIndexID is the connector's id.
	PodcastIndexID = "podcastindex"
	piName         = "Podcast Index"
	piAPI          = "https://api.podcastindex.org/api/1.0"
	piMax          = 60
)

func (PodcastIndex) Definition() c.Definition {
	return c.Definition{
		ID: PodcastIndexID, Name: piName, Category: c.CategorySocial,
		Summary: "Find podcast episodes that name your brand or competitors, in the open Podcast Index directory, on your own free API key.",
		Description: "Searches the titles, show notes and people of every public podcast episode for the names Mentions listens for, and reads " +
			"the transcripts shows publish in their feeds. Connecting runs one small search. Podcast Index's API is free: make a key and " +
			"secret at api.podcastindex.org. Your key is encrypted and only ever sent to Podcast Index.",
		Website: "https://podcastindex.org", DocsURL: "https://podcastindex-org.github.io/docs-api/", KeyURL: "https://api.podcastindex.org/signup",
		Auth: c.AuthAPIKey,
		Fields: []c.Field{
			{Name: "api_key", Label: "API key", Secret: true, Help: "From the email Podcast Index sends after you sign up at api.podcastindex.org."},
			{Name: "api_secret", Label: "API secret", Secret: true, Help: "Sent with the key."},
		},
		Capabilities: []string{c.CapPodcastSearch},
		Pricing:      "Free",
	}
}

// request signs a GET: the key, the time and SHA-1 of key, secret and time.
func (PodcastIndex) request(ctx context.Context, cl *c.Client, creds c.Creds, path string, q url.Values) ([]byte, error) {
	key, secret := strings.TrimSpace(creds["api_key"]), strings.TrimSpace(creds["api_secret"])
	if key == "" || secret == "" {
		return nil, c.Errf(c.ErrInvalid, "%s needs an API key and secret", piName)
	}
	u := cl.Base(PodcastIndexID, piAPI) + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, c.Errf(c.ErrInvalid, "bad %s address: %v", piName, err)
	}
	now := strconv.FormatInt(time.Now().Unix(), 10)
	sum := sha1.Sum([]byte(key + secret + now)) //nolint:gosec // their scheme
	req.Header.Set("User-Agent", "Outflare")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Auth-Date", now)
	req.Header.Set("X-Auth-Key", key)
	req.Header.Set("Authorization", hex.EncodeToString(sum[:]))
	resp, err := cl.Do(req)
	if err != nil {
		return nil, c.Errf(c.ErrUnavailable, "could not reach %s: %v", piName, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, c.Errf(c.ErrUnavailable, "reading the %s response: %v", piName, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, c.HTTPError(piName, resp.StatusCode, string(raw))
	}
	return raw, nil
}

// Test runs one small search, which costs nothing.
func (p PodcastIndex) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	if _, err := p.request(ctx, cl, creds, "/search/byterm", url.Values{"q": {"podcast"}, "max": {"1"}}); err != nil {
		return c.Account{}, err
	}
	return c.Account{Name: piName}, nil
}

type piEpisode struct {
	ID            int64  `json:"id"`
	Title         string `json:"title"`
	Link          string `json:"link"`
	Description   string `json:"description"`
	DatePublished int64  `json:"datePublished"`
	EnclosureURL  string `json:"enclosureUrl"`
	Duration      int    `json:"duration"`
	FeedID        int64  `json:"feedId"`
	FeedTitle     string `json:"feedTitle"`
	FeedImage     string `json:"feedImage"`
	Image         string `json:"image"`
	FeedURL       string `json:"feedUrl"`
	FeedAuthor    string `json:"feedAuthor"`
	TranscriptURL string `json:"transcriptUrl"`
	Transcripts   []struct {
		URL  string `json:"url"`
		Type string `json:"type"`
	} `json:"transcripts"`
}

// transcriptRank orders transcript types: timed ones first.
var transcriptRank = map[string]int{"application/json": 1, "text/vtt": 2, "application/x-subrip": 3, "application/srt": 3, "text/srt": 3, "text/html": 4, "text/plain": 5}

// SearchPodcastEpisodes finds episodes naming the term in their title,
// show notes or people (search/byperson searches all three).
func (p PodcastIndex) SearchPodcastEpisodes(ctx context.Context, cl *c.Client, creds c.Creds, q c.PodcastQuery) ([]c.PodcastEpisode, error) {
	n := q.Max
	if n <= 0 || n > piMax {
		n = piMax
	}
	raw, err := p.request(ctx, cl, creds, "/search/byperson", url.Values{"q": {q.Term}, "max": {strconv.Itoa(n)}, "fulltext": {""}})
	if err != nil {
		return nil, err
	}
	var body struct {
		Items []piEpisode `json:"items"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, c.Errf(c.ErrUnavailable, "%s answered something unexpected", piName)
	}
	out := make([]c.PodcastEpisode, 0, len(body.Items))
	for _, it := range body.Items {
		e := c.PodcastEpisode{ID: strconv.FormatInt(it.ID, 10), Title: it.Title, Description: it.Description, Link: it.Link,
			AudioURL: it.EnclosureURL, Seconds: it.Duration, ShowID: strconv.FormatInt(it.FeedID, 10), ShowTitle: it.FeedTitle,
			ShowLink: it.FeedURL, ShowImage: it.FeedImage, Author: it.FeedAuthor}
		if e.ShowImage == "" {
			e.ShowImage = it.Image
		}
		if it.DatePublished > 0 {
			t := time.Unix(it.DatePublished, 0).UTC()
			e.Published = &t
		}
		best := 99
		for _, t := range it.Transcripts {
			typ := strings.ToLower(strings.TrimSpace(t.Type))
			if r, ok := transcriptRank[typ]; ok && r < best && strings.HasPrefix(t.URL, "https://") {
				best, e.TranscriptURL, e.TranscriptType = r, t.URL, typ
			}
		}
		if e.TranscriptURL == "" && strings.HasPrefix(it.TranscriptURL, "https://") {
			e.TranscriptURL = it.TranscriptURL
		}
		out = append(out, e)
	}
	return out, nil
}
