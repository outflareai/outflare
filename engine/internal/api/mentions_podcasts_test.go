package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/track"
)

// fakePodcastIndex answers Podcast Index's episode search, and serves the
// transcripts shows publish.
type fakePodcastIndex struct {
	mu       sync.Mutex
	searched []string
	auth     []string
	base     string
}

func (f *fakePodcastIndex) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/transcripts/ep1.vtt":
		w.Header().Set("Content-Type", "text/vtt")
		w.Write([]byte("WEBVTT\n\n00:00:01.000 --> 00:00:03.000\nWelcome.\n\n00:12:05.000 --> 00:12:09.000\nWe tried Rival Corp before this.\n")) //nolint:errcheck
	case "/search/byterm":
		f.auth = append(f.auth, r.Header.Get("X-Auth-Key"))
		json.NewEncoder(w).Encode(map[string]any{"status": "true", "feeds": []any{}}) //nolint:errcheck
	case "/search/byperson":
		q := r.URL.Query().Get("q")
		f.searched = append(f.searched, q)
		f.auth = append(f.auth, r.Header.Get("X-Auth-Key"))
		items := []map[string]any{}
		if q == "Northpeak" {
			items = []map[string]any{
				{"id": 101, "title": "Scaling with Northpeak", "link": "https://pods.example/ep1", "description": "<p>We talk proxies with the <b>Northpeak</b> team.</p>",
					"datePublished": 1763000000, "enclosureUrl": "https://pods.example/ep1.mp3", "duration": 3600, "feedId": 9, "feedTitle": "Builders Talk",
					"feedUrl": "https://pods.example/feed.xml", "feedAuthor": "Cy Example",
					"transcripts": []map[string]any{{"url": f.base + "/transcripts/ep1.vtt", "type": "text/vtt"}}},
				{"id": 102, "title": "Northpeak weekly #4", "description": "News from Northpeak.", "feedId": 10, "feedTitle": "Northpeak Weekly",
					"feedUrl": "https://northpeak.example/podcast.xml", "feedAuthor": "Northpeak"},
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "true", "items": items}) //nolint:errcheck
	default:
		http.NotFound(w, r)
	}
}

// With podcasts on, a scan searches Podcast Index for episodes naming the
// brand and each tracked competitor on the workspace's own key, keeps the
// mentions in their titles and show notes, skips the brand's own show, and
// reads a transcript the show publishes, so a competitor named only in
// what was said is kept with its second.
func TestBrandMentionsPodcasts(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	fake := &fakePodcastIndex{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	// Transcripts are read only over https.
	tls := httptest.NewTLSServer(fake)
	t.Cleanup(tls.Close)
	fake.base = tls.URL
	e.integrations.Client.BaseURLs["podcastindex"] = srv.URL
	t.Cleanup(api.SetMentionTranscriptHTTP(tls.Client()))

	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	root := "/v1/brands/" + brand + "/mentions"
	var site, rival string
	if err := e.pool.QueryRow(ctx, `INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'northpeak.example', 'https://northpeak.example/', 'tok') RETURNING id::text`, ws, brand).Scan(&site); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, status) VALUES ($1, $2, $3, 'rivalcorp.example', 'Rival Corp', 'tracked') RETURNING id::text`, ws, site, brand).Scan(&rival); err != nil {
		t.Fatal(err)
	}
	o := owner.must(200, "PATCH", root+"/settings", map[string]any{"enabled": true, "sources": []string{"podcasts"}})
	var pod map[string]any
	for _, s := range o["sources"].([]any) {
		if s := s.(map[string]any); s["id"] == "podcasts" {
			pod = s
		}
	}
	if pod == nil || pod["on"] != true || pod["ready"] != false || pod["setup_tab"] != "connectors" {
		t.Fatalf("podcasts source: %v", pod)
	}
	if n, err := e.srv.ScanBrandMentions(ctx, ws, brand); err != nil || n != 0 {
		t.Fatalf("scan without a connection kept %d, %v", n, err)
	}
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "podcastindex",
		"credentials": map[string]string{"api_key": "pi-test-not-a-real-key", "api_secret": "pi-test-not-a-real-secret"}})
	n, err := e.srv.ScanBrandMentions(ctx, ws, brand)
	if err != nil || n != 1 {
		t.Fatalf("scan kept %d, %v", n, err)
	}
	if strings.Join(fake.searched, "|") != "Northpeak|northpeak.example|Rival Corp|rivalcorp.example" {
		t.Fatalf("searched: %v", fake.searched)
	}
	for _, k := range fake.auth {
		if k != "pi-test-not-a-real-key" {
			t.Fatalf("auth key: %v", fake.auth)
		}
	}
	var own, queued int
	if err := e.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM mention_episodes WHERE brand_id = $1 AND own_about = 'brand'),
		(SELECT count(*) FROM jobs WHERE kind = 'mentions.episode')`, brand).Scan(&own, &queued); err != nil || own != 1 || queued != 1 {
		t.Fatalf("own %d, queued %d, %v", own, queued, err)
	}
	var id string
	if err := e.pool.QueryRow(ctx, `SELECT id::text FROM mention_episodes WHERE brand_id = $1 AND transcript_status = 'waiting'`, brand).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.ReadMentionEpisode(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	list := owner.must(200, "GET", root+"?source=podcast", nil)["data"].([]any)
	if len(list) != 2 {
		t.Fatalf("mentions: %v", list)
	}
	for _, m := range list {
		m := m.(map[string]any)
		switch m["about"] {
		case "brand":
			if !strings.Contains(m["quote"].(string), "Northpeak team") || m["author"].(map[string]any)["name"] != "Builders Talk" || m["url"] != "https://pods.example/ep1" {
				t.Fatalf("show notes mention: %v", m)
			}
		default:
			if m["competitor"].(map[string]any)["id"] != rival || m["at_seconds"] != 725.0 || !strings.Contains(m["quote"].(string), "Rival Corp before") {
				t.Fatalf("spoken mention: %v", m)
			}
		}
	}
	// Scanning again keeps nothing twice and the own show stays out.
	if n, err := e.srv.ScanBrandMentions(ctx, ws, brand); err != nil || n != 0 {
		t.Fatalf("second scan kept %d, %v", n, err)
	}
	count := map[string]int{}
	for _, ev := range events() {
		count[ev.Name]++
	}
	if count["mention_episode.read"] != 1 {
		t.Fatalf("events: %v", count)
	}
}
