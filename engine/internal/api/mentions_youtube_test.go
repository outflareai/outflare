package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// fakeYouTubeGeonode answers Geonode's search with two videos and reads
// their watch pages and captions.
type fakeYouTubeGeonode struct {
	mu       sync.Mutex
	searched []string
	read     []string
	// own: searching for Rival Corp finds a video on Rival Corp's own
	// channel.
	own bool
}

func (f *fakeYouTubeGeonode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/v1/usage/concurrency":
		json.NewEncoder(w).Encode(map[string]any{"work_concurrency_in_use": 0, "work_concurrency_limit": 5}) //nolint:errcheck
	case "/v1/search":
		f.searched = append(f.searched, body["query"].(string))
		results := []map[string]any{}
		if strings.Contains(body["query"].(string), "Northpeak") {
			results = []map[string]any{
				{"title": "Northpeak review", "url": "https://www.youtube.com/watch?v=vidAAAAAAAA", "snippet": "An honest look at Northpeak."},
				{"title": "Proxy tools in 2026", "url": "https://www.youtube.com/watch?v=vidBBBBBBBB", "snippet": "Five tools compared."},
				{"title": "A channel", "url": "https://www.youtube.com/@someone", "snippet": "Northpeak"},
			}
		}
		if f.own && strings.Contains(body["query"].(string), "Rival Corp") {
			results = []map[string]any{{"title": "Rival Corp product tour", "url": "https://www.youtube.com/watch?v=vidCCCCCCCC", "snippet": "See Rival Corp in action."}}
		}
		json.NewEncoder(w).Encode(map[string]any{"engine": "google", "results": results, "tokens_charged": 1}) //nolint:errcheck
	case "/v1/extract":
		u := body["url"].(string)
		f.read = append(f.read, u)
		var page string
		switch {
		case strings.Contains(u, "timedtext"):
			page = `<html><body><pre>{"events":[{"tStartMs":1000,"segs":[{"utf8":"hi all"}]},{"tStartMs":95000,"segs":[{"utf8":"I switched from Rival Corp &amp; never looked back"}]}]}</pre></body></html>`
		case strings.Contains(u, "vidCCCCCCCC"):
			page = `<script>var ytInitialPlayerResponse = {"videoDetails":{"videoId":"vidCCCCCCCC","title":"Rival Corp product tour","author":"Rival Corp","channelId":"UCrival","viewCount":"40","lengthSeconds":"60"},
				"microformat":{"playerMicroformatRenderer":{"ownerProfileUrl":"http://www.youtube.com/@rivalcorp"}}};</script>`
		case strings.Contains(u, "vidDDDDDDDD"):
			page = `<script>var ytInitialPlayerResponse = {"videoDetails":{"videoId":"vidDDDDDDDD","title":"Top proxy tools","author":"Cy Tech","channelId":"UCcy","viewCount":"77000","lengthSeconds":"900"},
				"microformat":{"playerMicroformatRenderer":{"ownerProfileUrl":"http://www.youtube.com/@cytech"}}};</script>`
		case strings.Contains(u, "vidBBBBBBBB"):
			page = `<script>var ytInitialPlayerResponse = {"videoDetails":{"videoId":"vidBBBBBBBB","title":"Proxy tools in 2026","author":"Ana Builds","channelId":"UCexample","viewCount":"5200","lengthSeconds":"600"},
				"microformat":{"playerMicroformatRenderer":{"publishDate":"2026-11-01","ownerProfileUrl":"http://www.youtube.com/@anabuilds"}},
				"captions":{"playerCaptionsTracklistRenderer":{"captionTracks":[{"baseUrl":"https://www.youtube.com/api/timedtext?v=vidBBBBBBBB&lang=en","languageCode":"en","kind":"asr"}]}}};</script>`
		default:
			page = `<script>var ytInitialPlayerResponse = {"videoDetails":{"videoId":"vidAAAAAAAA","title":"Northpeak review","author":"Bo Reviews","channelId":"UCother","viewCount":"900","lengthSeconds":"300"}};</script>`
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"html": page}, "metadata": map[string]any{"http_status": 200, "url": u}, "tokens_charged": 1}) //nolint:errcheck
	default:
		http.NotFound(w, r)
	}
}

// With YouTube on, a scan searches the web for videos naming the brand and
// each tracked competitor through the workspace's own Geonode, keeps the
// mentions in their titles, and queues reading each new video; reading a
// video's captions keeps what was said, with the second, and tells who
// made it. Without a connection YouTube says what to connect.
func TestBrandMentionsYouTube(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	fake := &fakeYouTubeGeonode{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	e.integrations.Client.BaseURLs["geonode"] = srv.URL

	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	root := "/v1/brands/" + brand + "/mentions"
	var site, rival string
	if err := e.pool.QueryRow(ctx, `INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'northpeak.example', 'https://northpeak.example/', 'tok') RETURNING id::text`, ws, brand).Scan(&site); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, status) VALUES ($1, $2, $3, 'rivalcorp.example', 'Rival Corp', 'tracked') RETURNING id::text`, ws, site, brand).Scan(&rival); err != nil {
		t.Fatal(err)
	}

	// YouTube is off by default and needs a web search connection.
	o := owner.must(200, "PATCH", root+"/settings", map[string]any{"enabled": true, "sources": []string{"youtube"}})
	var yt map[string]any
	for _, s := range o["sources"].([]any) {
		if s := s.(map[string]any); s["id"] == "youtube" {
			yt = s
		}
	}
	if yt == nil || yt["on"] != true || yt["ready"] != false || yt["setup_tab"] != "connectors" {
		t.Fatalf("youtube source: %v", yt)
	}
	if n, err := e.srv.ScanBrandMentions(ctx, ws, brand); err != nil || n != 0 {
		t.Fatalf("scan without a connection kept %d, %v", n, err)
	}
	if o := owner.must(200, "GET", root+"/overview", nil); o["last_error_code"] != "connector_needed" {
		t.Fatalf("no connection: %v", o["last_error_code"])
	}

	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "geonode", "credentials": map[string]string{"api_key": "gn-test-not-a-real-key"}})
	n, err := e.srv.ScanBrandMentions(ctx, ws, brand)
	if err != nil || n != 1 {
		t.Fatalf("scan kept %d, %v", n, err)
	}
	if len(fake.searched) != 2 || fake.searched[0] != `site:youtube.com "Northpeak"` || fake.searched[1] != `site:youtube.com "Rival Corp"` {
		t.Fatalf("searched: %v", fake.searched)
	}
	if o := owner.must(200, "GET", root+"/overview", nil); o["last_error_code"] != nil {
		t.Fatalf("error kept: %v", o["last_error_code"])
	}
	var videos, queued int
	if err := e.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM mention_videos WHERE brand_id = $1),
		(SELECT count(*) FROM jobs WHERE kind = 'mentions.video')`, brand).Scan(&videos, &queued); err != nil || videos != 2 || queued != 2 {
		t.Fatalf("videos %d, queued %d, %v", videos, queued, err)
	}

	// Reading the videos.
	rows, err := e.pool.Query(ctx, `SELECT id::text FROM mention_videos WHERE brand_id = $1 ORDER BY video_id`, brand)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id) //nolint:errcheck
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if err := e.srv.ReadMentionVideo(ctx, ws, id); err != nil {
			t.Fatal(err)
		}
	}
	list := owner.must(200, "GET", root, nil)["data"].([]any)
	if len(list) != 2 {
		t.Fatalf("mentions: %v", list)
	}
	var title, spoken map[string]any
	for _, m := range list {
		m := m.(map[string]any)
		if m["about"] == "brand" {
			title = m
		} else {
			spoken = m
		}
	}
	// The title mention learned who made the video.
	if title["source"] != "youtube" || title["author"].(map[string]any)["name"] != "Bo Reviews" || title["reach"] != 900.0 || title["at_seconds"] != nil {
		t.Fatalf("title mention: %v", title)
	}
	// The competitor was named only in what was said, 95 seconds in.
	if spoken["competitor"].(map[string]any)["id"] != rival || spoken["at_seconds"] != 95.0 || !strings.Contains(spoken["quote"].(string), "Rival Corp & never") ||
		!strings.HasSuffix(spoken["url"].(string), "&t=95s") || spoken["author"].(map[string]any)["url"] != "https://www.youtube.com/@anabuilds" {
		t.Fatalf("spoken mention: %v", spoken)
	}
	var status string
	var captions int
	if err := e.pool.QueryRow(ctx, `SELECT captions_status, jsonb_array_length(captions) FROM mention_videos WHERE brand_id = $1 AND video_id = 'vidBBBBBBBB'`, brand).Scan(&status, &captions); err != nil || status != "read" || captions != 2 {
		t.Fatalf("captions %s %d, %v", status, captions, err)
	}
	// Reading again does nothing; the spend was booked on the brand.
	if err := e.srv.ReadMentionVideo(ctx, ws, ids[0]); err != nil || len(fake.read) != 3 {
		t.Fatalf("read again: %v %v", fake.read, err)
	}
	var spend int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM spend_entries WHERE workspace_id = $1 AND brand_id = $2`, ws, brand).Scan(&spend); err != nil || spend != 5 {
		t.Fatalf("spend entries %d, %v", spend, err)
	}
	count := map[string]int{}
	for _, ev := range events() {
		count[ev.Name]++
	}
	if count["mention_video.read"] != 2 || count["brand_mentions.scanned"] != 2 {
		t.Fatalf("events: %v", count)
	}
}

// A video on a competitor's own channel isn't a mention: reading it removes
// the mention kept from its title, suggests the channel on the Competitors
// page and later scans skip it. A video Presence found is read too, so its
// mention learns the channel and the views.
func TestBrandMentionsSkipOwnChannels(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	fake := &fakeYouTubeGeonode{own: true}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	e.integrations.Client.BaseURLs["geonode"] = srv.URL
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	root := "/v1/brands/" + brand + "/mentions"
	var site, rival string
	if err := e.pool.QueryRow(ctx, `INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'northpeak.example', 'https://northpeak.example/', 'tok') RETURNING id::text`, ws, brand).Scan(&site); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, status) VALUES ($1, $2, $3, 'rivalcorp.example', 'Rival Corp', 'tracked') RETURNING id::text`, ws, site, brand).Scan(&rival); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO coverage_competitor_placements (workspace_id, brand_id, competitor_id, url, host, title, kind)
		VALUES ($1, $2, $3, 'https://www.youtube.com/watch?v=vidDDDDDDDD', 'youtube.com', 'Top proxy tools - YouTube', '')`, ws, brand, rival); err != nil {
		t.Fatal(err)
	}
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "geonode", "credentials": map[string]string{"api_key": "gn-test-not-a-real-key"}})
	owner.must(200, "PATCH", root+"/settings", map[string]any{"enabled": true, "sources": []string{"youtube", "web"}})
	// The brand's title mention, Rival Corp's own video and Presence's video.
	if n, err := e.srv.ScanBrandMentions(ctx, ws, brand); err != nil || n != 3 {
		t.Fatalf("scan kept %d, %v", n, err)
	}
	read := func() {
		t.Helper()
		rows, err := e.pool.Query(ctx, `SELECT id::text FROM mention_videos WHERE brand_id = $1 AND captions_status = 'waiting'`, brand)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for rows.Next() {
			var id string
			rows.Scan(&id) //nolint:errcheck
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			if err := e.srv.ReadMentionVideo(ctx, ws, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	read()
	list := owner.must(200, "GET", root, nil)["data"].([]any)
	for _, m := range list {
		if m.(map[string]any)["external_id"] == "vidCCCCCCCC" {
			t.Fatalf("own video kept: %v", m)
		}
	}
	var presence map[string]any
	for _, m := range list {
		if m := m.(map[string]any); m["external_id"] == "vidDDDDDDDD" {
			presence = m
		}
	}
	if presence == nil || presence["author"].(map[string]any)["name"] != "Cy Tech" || presence["reach"] != 77000.0 || presence["title"] != "Top proxy tools" {
		t.Fatalf("presence video: %v", list)
	}
	var links string
	if err := e.pool.QueryRow(ctx, `SELECT links::text FROM seo_competitors WHERE id = $1`, rival).Scan(&links); err != nil ||
		!strings.Contains(links, `"url": "https://www.youtube.com/@rivalcorp"`) || !strings.Contains(links, `"source": "mentions"`) || !strings.Contains(links, `"state": "suggested"`) {
		t.Fatalf("competitor links: %s, %v", links, err)
	}
	// Later scans skip it.
	if n, err := e.srv.ScanBrandMentions(ctx, ws, brand); err != nil || n != 0 {
		t.Fatalf("second scan kept %d, %v", n, err)
	}
}
