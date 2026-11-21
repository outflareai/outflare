package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/blobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// memBlobs is a bucket in memory.
type memBlobs struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (b *memBlobs) Put(_ context.Context, key, _ string, body []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.m[key] = body
	return nil
}

func (b *memBlobs) Get(_ context.Context, key string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.m[key]
	if !ok {
		return nil, blobs.ErrNotFound
	}
	return v, nil
}

func (b *memBlobs) Delete(_ context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.m, key)
	return nil
}

func (b *memBlobs) len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.m)
}

func sendReplay(t *testing.T, h http.Handler, ua string, body map[string]any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/v1/site/replay", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Origin", "https://acme.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestSessionRecordingsAndHeatmaps(t *testing.T) {
	got := track.Capture(t)
	bucket := &memBlobs{m: map[string][]byte{}}
	e := freshEnvWith(t, func(o *api.Options) { o.Recordings = bucket })
	owner, ws := setupOwner(t, e.h)
	var brand string
	if err := e.pool.QueryRow(t.Context(), `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	k := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "acme.example", "origins": []string{"https://acme.example"}})
	key := k["key"].(string)

	// Off until the brand says so: nothing is stored, and the script is told.
	if set := owner.must(200, "GET", base+"/replay/settings", nil); set["recordings"] != false || set["heatmaps"] != false || set["storage"] != true || set["sample_percent"].(float64) != 100 {
		t.Fatalf("defaults: %v", set)
	}
	part := map[string]any{"key": key, "session": "sess-0001-aaaa", "anonymousId": "anon-1", "part": 0, "path": "/pricing?plan=pro", "width": 1280, "height": 720,
		"events": []any{map[string]any{"type": 4, "data": map[string]any{"href": "https://acme.example/pricing"}}, map[string]any{"type": 2}},
		"clicks": [][2]int{{50, 12}, {50, 12}, {10, 300}, {120, 1}}, "depth": 6, "page_height": 2400}
	if code, out := sendReplay(t, e.h, desktopUA, part); code != 200 || out["recording"] != false || out["heatmaps"] != false || bucket.len() != 0 {
		t.Fatalf("off: %d %v", code, out)
	}
	rec := sendSiteAs(t, e.h, "https://acme.example", key, desktopUA, false, map[string]any{"type": "page", "anonymousId": "anon-1", "messageId": "m-1"})
	if strings.Contains(rec.Body.String(), "replay") {
		t.Fatalf("a brand that records nothing says nothing: %s", rec.Body)
	}

	owner.must(400, "PATCH", base+"/replay/settings", map[string]any{"sample_percent": 0})
	owner.must(400, "PATCH", base+"/replay/settings", map[string]any{"keep_days": 400})
	if set := owner.must(200, "PATCH", base+"/replay/settings", map[string]any{"recordings": true, "heatmaps": true, "sample_percent": 50, "mask_text": true}); set["recordings"] != true || set["sample_percent"].(float64) != 50 {
		t.Fatalf("settings: %v", set)
	}
	rec = sendSiteAs(t, e.h, "https://acme.example", key, desktopUA, false, map[string]any{"type": "page", "anonymousId": "anon-1", "messageId": "m-2"})
	var answer struct {
		Replay map[string]any `json:"replay"`
	}
	json.Unmarshal(rec.Body.Bytes(), &answer)
	if answer.Replay["record"] != true || answer.Replay["heatmaps"] != true || answer.Replay["sample"].(float64) != 50 || answer.Replay["mask_text"] != true {
		t.Fatalf("the script is told what to do: %s", rec.Body)
	}

	// A visit in two parts, on two pages.
	if code, out := sendReplay(t, e.h, desktopUA, part); code != 200 || out["recording"] != true {
		t.Fatalf("part 0: %d %v", code, out)
	}
	part["part"], part["path"], part["clicks"], part["depth"] = 1, "/signup/", [][2]int{{20, 40}}, 10
	part["events"] = []any{map[string]any{"type": 3, "data": map[string]any{"source": 2}}}
	if code, out := sendReplay(t, e.h, desktopUA, part); code != 200 || out["recording"] != true {
		t.Fatalf("part 1: %d %v", code, out)
	}
	// Bots are not recorded, and a bad session id is refused.
	part["session"], part["part"] = "sess-0002-bbbb", 0
	if code, out := sendReplay(t, e.h, "Googlebot/2.1", part); code != 200 || out["recording"] != false {
		t.Fatalf("bot: %d %v", code, out)
	}
	part["session"] = "../../x"
	if code, _ := sendReplay(t, e.h, desktopUA, part); code != 400 {
		t.Fatalf("bad session: %d", code)
	}
	part["key"] = "ofk_nope"
	if code, _ := sendReplay(t, e.h, desktopUA, part); code != 401 {
		t.Fatalf("bad key: %d", code)
	}
	if bucket.len() != 2 {
		t.Fatalf("two parts stored, got %d", bucket.len())
	}

	list := owner.must(200, "GET", base+"/recordings", nil)["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("recordings: %v", list)
	}
	one := list[0].(map[string]any)
	id := one["id"].(string)
	if one["parts"].(float64) != 2 || one["device"] != "desktop" || one["browser"] != "chrome" || one["anonymous_id"] != "anon-1" || one["viewed_at"] != nil ||
		mustJSON(one["paths"]) != `["/pricing","/signup"]` || one["clicks"].(float64) != 5 {
		t.Fatalf("recording: %v", one)
	}
	if l := owner.must(200, "GET", base+"/recordings?path=/signup&device=desktop", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("by page: %v", l)
	}
	if l := owner.must(200, "GET", base+"/recordings?path=/nope", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("by page: %v", l)
	}
	owner.must(400, "GET", base+"/recordings?device=watch", nil)
	if page := owner.must(200, "GET", base+"/recordings?limit=1", nil); len(page["data"].([]any)) != 1 || page["next_cursor"] != nil {
		t.Fatalf("the last page has no next_cursor: %v", page)
	}
	owner.must(400, "GET", base+"/recordings?cursor=nope", nil)
	p0 := owner.must(200, "GET", base+"/recordings/"+id+"/parts/0", nil)
	if evs := p0["events"].([]any); len(evs) != 2 || evs[0].(map[string]any)["type"].(float64) != 4 {
		t.Fatalf("part 0: %v", p0)
	}
	owner.must(404, "GET", base+"/recordings/"+id+"/parts/2", nil)
	if v := owner.must(200, "GET", base+"/recordings/"+id, nil); v["viewed_at"] == nil {
		t.Fatalf("watching marks it: %v", v)
	}

	// Heatmaps: clicks by spot, and how far down views got.
	pages := owner.must(200, "GET", base+"/heatmaps/pages", nil)["data"].([]any)
	if len(pages) != 2 || pages[0].(map[string]any)["path"] != "/pricing" || pages[0].(map[string]any)["clicks"].(float64) != 3 {
		t.Fatalf("pages: %v", pages)
	}
	heat := owner.must(200, "GET", base+"/heatmaps?path=/pricing", nil)
	clicks := heat["clicks"].([]any)
	if heat["total_clicks"].(float64) != 3 || len(clicks) != 2 || mustJSON(clicks[0]) != `[50,120,2]` || heat["height"].(float64) != 2400 ||
		heat["views"].(float64) != 1 || heat["recording_id"] != id {
		t.Fatalf("heatmap: %v", heat)
	}
	scroll := heat["scroll"].([]any)
	if len(scroll) != 10 || scroll[5].(map[string]any)["views"].(float64) != 1 || scroll[6].(map[string]any)["views"].(float64) != 0 {
		t.Fatalf("scroll: %v", scroll)
	}
	owner.must(400, "GET", base+"/heatmaps", nil)

	// Deleting hides it at once; the sweep removes the files, then the row.
	owner.must(204, "DELETE", base+"/recordings/"+id, nil)
	owner.must(404, "GET", base+"/recordings/"+id, nil)
	if n, err := e.srv.SweepReplay(t.Context()); err != nil || n != 1 || bucket.len() != 0 {
		t.Fatalf("sweep: %d %v, %d files left", n, err, bucket.len())
	}

	// A visit whose first parts never arrived (the visitor left the page
	// before they were sent) is kept from the part that did: the missing
	// ones read as empty.
	late := map[string]any{"key": key, "session": "sess-0003-cccc", "anonymousId": "anon-3", "part": 2, "path": "/pricing", "width": 1280, "height": 720,
		"events": []any{map[string]any{"type": 2, "timestamp": 1}}}
	if code, out := sendReplay(t, e.h, desktopUA, late); code != 200 || out["recording"] != true {
		t.Fatalf("late start: %d %v", code, out)
	}
	list = owner.must(200, "GET", base+"/recordings", nil)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["parts"].(float64) != 3 {
		t.Fatalf("late start list: %v", list)
	}
	lateID := list[0].(map[string]any)["id"].(string)
	if p := owner.must(200, "GET", base+"/recordings/"+lateID+"/parts/0", nil); len(p["events"].([]any)) != 0 {
		t.Fatalf("missing part: %v", p)
	}
	if p := owner.must(200, "GET", base+"/recordings/"+lateID+"/parts/2", nil); len(p["events"].([]any)) != 1 {
		t.Fatalf("late part: %v", p)
	}

	// Another workspace's brand has none of it.
	owner.must(404, "GET", "/v1/brands/00000000-0000-4000-8000-000000000000/recordings", nil)

	names := map[string]bool{}
	for _, ev := range got() {
		names[ev.Name] = true
	}
	for _, n := range []string{"replay_settings.updated", "recording.deleted"} {
		if !names[n] {
			t.Fatalf("missing event %s", n)
		}
	}
}

func TestRecordingsNeedStorage(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	var brand string
	if err := e.pool.QueryRow(t.Context(), `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand + "/replay/settings"
	if set := owner.must(200, "GET", base, nil); set["storage"] != false {
		t.Fatalf("settings: %v", set)
	}
	owner.must(400, "PATCH", base, map[string]any{"recordings": true})
	// Heatmaps are counts in the database and need no bucket.
	if set := owner.must(200, "PATCH", base, map[string]any{"heatmaps": true}); set["heatmaps"] != true || set["recordings"] != false {
		t.Fatalf("heatmaps: %v", set)
	}
}

// A brand chooses which pages are recorded, how short a visit is still
// kept and how many visits a day are recorded.
func TestReplayPageRulesAndLimits(t *testing.T) {
	got := track.Capture(t)
	bucket := &memBlobs{m: map[string][]byte{}}
	e := freshEnvWith(t, func(o *api.Options) { o.Recordings = bucket })
	owner, ws := setupOwner(t, e.h)
	var brand string
	if err := e.pool.QueryRow(t.Context(), `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	key := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "acme.example", "origins": []string{"https://acme.example"}})["key"].(string)

	owner.must(400, "PATCH", base+"/replay/settings", map[string]any{"include_paths": []string{"pricing"}})
	owner.must(400, "PATCH", base+"/replay/settings", map[string]any{"exclude_paths": []string{"/a?b=1"}})
	owner.must(400, "PATCH", base+"/replay/settings", map[string]any{"min_seconds": 601})
	owner.must(400, "PATCH", base+"/replay/settings", map[string]any{"daily_limit": -1})
	// A shape is x's and separators, never a key and never short.
	owner.must(400, "PATCH", base+"/replay/settings", map[string]any{"hide_shapes": []string{"ab12-cd34-ef56"}})
	owner.must(400, "PATCH", base+"/replay/settings", map[string]any{"hide_shapes": []string{"xxxx-xxx"}})
	if got := owner.must(200, "PATCH", base+"/replay/settings", map[string]any{"hide_shapes": []string{"xxxx-xxxx-xxxx", " xxxx-xxxx-xxxx "}})["hide_shapes"].([]any); len(got) != 1 || got[0] != "xxxx-xxxx-xxxx" {
		t.Fatalf("shapes: %v", got)
	}
	set := owner.must(200, "PATCH", base+"/replay/settings", map[string]any{"recordings": true, "heatmaps": true,
		"include_paths": []string{"/pricing/", "/blog/*", "/pricing"}, "exclude_paths": []string{"/blog/private*"}, "daily_limit": 1})
	if inc := set["include_paths"].([]any); len(inc) != 2 || inc[0] != "/pricing" || set["exclude_paths"].([]any)[0] != "/blog/private*" || set["daily_limit"].(float64) != 1 || set["min_seconds"].(float64) != 0 {
		t.Fatalf("settings: %v", set)
	}
	rec := sendSiteAs(t, e.h, "https://acme.example", key, desktopUA, false, map[string]any{"type": "page", "anonymousId": "anon-1", "messageId": "m-1"})
	var answer struct {
		Replay struct{ Include, Exclude, Hide []string } `json:"replay"`
	}
	json.Unmarshal(rec.Body.Bytes(), &answer)
	if len(answer.Replay.Include) != 2 || len(answer.Replay.Exclude) != 1 || len(answer.Replay.Hide) != 1 {
		t.Fatalf("the script is told the pages: %s", rec.Body)
	}

	part := func(session, path string) map[string]any {
		return map[string]any{"key": key, "session": session, "anonymousId": "anon-1", "part": 0, "path": path, "width": 1280, "height": 720,
			"events": []any{map[string]any{"type": 2}}, "clicks": [][2]int{{50, 12}}, "depth": 5, "page_height": 2000}
	}
	// A page that isn't listed, and one left out: nothing kept, the visit carries on.
	for _, p := range []string{"/about", "/blog/private-notes"} {
		if code, out := sendReplay(t, e.h, desktopUA, part("sess-0001-aaaa", p)); code != 200 || out["recording"] != true || bucket.len() != 0 {
			t.Fatalf("%s: %d %v, %d stored", p, code, out, bucket.len())
		}
	}
	if code, out := sendReplay(t, e.h, desktopUA, part("sess-0001-aaaa", "/blog/launch")); code != 200 || out["recording"] != true || bucket.len() != 1 {
		t.Fatalf("a listed page: %d %v", code, out)
	}
	pages := owner.must(200, "GET", base+"/heatmaps/pages", nil)["data"].([]any)
	if len(pages) != 1 || pages[0].(map[string]any)["path"] != "/blog/launch" {
		t.Fatalf("only the listed page has a heatmap: %v", pages)
	}
	// The day's one recording is taken: a second visit isn't recorded, the first carries on.
	if code, out := sendReplay(t, e.h, desktopUA, part("sess-0002-bbbb", "/pricing")); code != 200 || out["recording"] != false || bucket.len() != 1 {
		t.Fatalf("over the day's limit: %d %v", code, out)
	}
	more := part("sess-0001-aaaa", "/pricing")
	more["part"] = 1
	if code, out := sendReplay(t, e.h, desktopUA, more); code != 200 || out["recording"] != true || bucket.len() != 2 {
		t.Fatalf("the visit already recording carries on: %d %v", code, out)
	}

	// A visit shorter than the brand keeps is hidden once it has ended, then deleted.
	owner.must(200, "PATCH", base+"/replay/settings", map[string]any{"min_seconds": 10})
	if list := owner.must(200, "GET", base+"/recordings", nil)["data"].([]any); len(list) != 1 {
		t.Fatalf("a visit still under way is listed: %v", list)
	}
	if _, err := e.pool.Exec(t.Context(), `UPDATE session_recordings SET started_at = now() - interval '2 hours', last_at = now() - interval '2 hours' + interval '3 seconds' WHERE brand_id = $1`, brand); err != nil {
		t.Fatal(err)
	}
	if list := owner.must(200, "GET", base+"/recordings", nil)["data"].([]any); len(list) != 0 {
		t.Fatalf("a short visit that ended is hidden: %v", list)
	}
	if n, err := e.srv.SweepReplay(t.Context()); err != nil || n != 1 || bucket.len() != 0 {
		t.Fatalf("sweep: %d %v, %d files left", n, err, bucket.len())
	}
	changed := false
	for _, ev := range got() {
		changed = changed || ev.Name == "replay_settings.updated"
	}
	if !changed {
		t.Fatal("missing event replay_settings.updated")
	}
}

// Many recordings deleted at once, and heatmaps cleared to start again.
func TestBulkDeleteRecordingsAndClearHeatmaps(t *testing.T) {
	got := track.Capture(t)
	bucket := &memBlobs{m: map[string][]byte{}}
	e := freshEnvWith(t, func(o *api.Options) { o.Recordings = bucket })
	owner, ws := setupOwner(t, e.h)
	var brand string
	if err := e.pool.QueryRow(t.Context(), `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	key := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "acme.example", "origins": []string{"https://acme.example"}})["key"].(string)
	owner.must(200, "PATCH", base+"/replay/settings", map[string]any{"recordings": true, "heatmaps": true})
	for i, s := range []string{"sess-0001-aaaa", "sess-0002-bbbb", "sess-0003-cccc"} {
		part := map[string]any{"key": key, "session": s, "anonymousId": "anon-1", "part": 0, "path": []string{"/pricing", "/signup", "/pricing"}[i], "width": 1280, "height": 720,
			"events": []any{map[string]any{"type": 2}}, "clicks": [][2]int{{50, 12}}, "depth": 5, "page_height": 2000}
		if code, out := sendReplay(t, e.h, desktopUA, part); code != 200 || out["recording"] != true {
			t.Fatalf("part: %d %v", code, out)
		}
	}
	if _, err := e.pool.Exec(t.Context(), `UPDATE session_recordings SET started_at = now() - interval '10 days', last_at = now() - interval '10 days' WHERE brand_id = $1 AND session_id = 'sess-0001-aaaa'`, brand); err != nil {
		t.Fatal(err)
	}
	list := owner.must(200, "GET", base+"/recordings", nil)["data"].([]any)
	if len(list) != 3 {
		t.Fatalf("three recordings: %v", list)
	}
	newest := list[0].(map[string]any)["id"].(string)

	owner.must(400, "POST", base+"/recordings/delete", map[string]any{})
	owner.must(400, "POST", base+"/recordings/delete", map[string]any{"ids": []string{newest}, "older_than_days": 7})
	owner.must(400, "POST", base+"/recordings/delete", map[string]any{"ids": []string{"nope"}})
	owner.must(400, "POST", base+"/recordings/delete", map[string]any{"older_than_days": 91})
	if out := owner.must(200, "POST", base+"/recordings/delete", map[string]any{"older_than_days": 7}); out["deleted"].(float64) != 1 {
		t.Fatalf("older than a week: %v", out)
	}
	if out := owner.must(200, "POST", base+"/recordings/delete", map[string]any{"ids": []string{newest, "00000000-0000-4000-8000-000000000000"}}); out["deleted"].(float64) != 1 {
		t.Fatalf("by id: %v", out)
	}
	if list := owner.must(200, "GET", base+"/recordings", nil)["data"].([]any); len(list) != 1 {
		t.Fatalf("one left: %v", list)
	}
	if n, err := e.srv.SweepReplay(t.Context()); err != nil || n != 2 || bucket.len() != 1 {
		t.Fatalf("sweep: %d %v, %d files left", n, err, bucket.len())
	}
	if out := owner.must(200, "POST", base+"/recordings/delete", map[string]any{"older_than_days": 0}); out["deleted"].(float64) != 1 {
		t.Fatalf("every recording: %v", out)
	}

	// One page's heatmap, then every page's.
	owner.must(400, "POST", base+"/heatmaps/clear", map[string]any{"path": "pricing"})
	owner.must(200, "POST", base+"/heatmaps/clear", map[string]any{"path": "/pricing/"})
	if pages := owner.must(200, "GET", base+"/heatmaps/pages", nil)["data"].([]any); len(pages) != 1 || pages[0].(map[string]any)["path"] != "/signup" {
		t.Fatalf("the other page keeps its heatmap: %v", pages)
	}
	owner.must(200, "POST", base+"/heatmaps/clear", map[string]any{})
	if pages := owner.must(200, "GET", base+"/heatmaps/pages", nil)["data"].([]any); len(pages) != 0 {
		t.Fatalf("every heatmap cleared: %v", pages)
	}

	names := map[string]bool{}
	for _, ev := range got() {
		names[ev.Name] = true
	}
	for _, n := range []string{"recordings.bulk_deleted", "heatmaps.cleared"} {
		if !names[n] {
			t.Fatalf("missing event %s", n)
		}
	}
}

func TestRecordingSitesAndPeople(t *testing.T) {
	bucket := &memBlobs{m: map[string][]byte{}}
	e := freshEnvWith(t, func(o *api.Options) { o.Recordings = bucket })
	owner, ws := setupOwner(t, e.h)
	var brand string
	if err := e.pool.QueryRow(t.Context(), `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	key := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "acme.example", "origins": []string{"https://acme.example", "https://app.acme.example"}})["key"].(string)
	owner.must(200, "PATCH", base+"/replay/settings", map[string]any{"recordings": true})
	send := func(origin, session, anon string) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"key": key, "session": session, "anonymousId": anon, "part": 0, "path": "/", "width": 1280, "height": 720, "events": []any{map[string]any{"type": 2}}})
		req := httptest.NewRequest("POST", "/v1/site/replay", strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
		req.Header.Set("User-Agent", desktopUA)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("part from %s: %d %s", origin, rec.Code, rec.Body)
		}
	}
	send("https://acme.example", "sess-site-0001", "anon-site")
	send("https://app.acme.example", "sess-app-0001", "anon-app")
	// The app's visitor is a known person.
	if _, err := e.pool.Exec(t.Context(), `WITH p AS (INSERT INTO records (brand_id, workspace_id, record_type, name) VALUES ($2, $1, 'person', 'Ana Example') RETURNING id)
		INSERT INTO site_visitors (brand_id, workspace_id, anonymous_id, record_id, identified_by, first_seen_at, last_seen_at) SELECT $2, $1, 'anon-app', id, 'product', now(), now() FROM p`, ws, brand); err != nil {
		t.Fatal(err)
	}

	all := owner.must(200, "GET", base+"/recordings", nil)
	if sites := all["sites"].([]any); len(sites) != 2 || sites[0] != "acme.example" || sites[1] != "app.acme.example" {
		t.Fatalf("both websites are listed: %v", sites)
	}
	if len(all["data"].([]any)) != 2 {
		t.Fatalf("two recordings: %v", all["data"])
	}
	one := func(query string) map[string]any {
		t.Helper()
		list := owner.must(200, "GET", base+"/recordings?"+query, nil)["data"].([]any)
		if len(list) != 1 {
			t.Fatalf("%s: one recording, got %v", query, list)
		}
		return list[0].(map[string]any)
	}
	if r := one("host=app.acme.example"); r["host"] != "app.acme.example" || r["record_name"] != "Ana Example" || r["record_id"] == nil {
		t.Fatalf("the app's recording names its person: %v", r)
	}
	if r := one("visitor=known"); r["host"] != "app.acme.example" {
		t.Fatalf("known people: %v", r)
	}
	if r := one("visitor=anonymous"); r["host"] != "acme.example" || r["record_name"] != nil {
		t.Fatalf("anonymous visitors: %v", r)
	}
	owner.must(400, "GET", base+"/recordings?visitor=everyone", nil)
}

func TestHeatmapsTakeTheirOwnPicture(t *testing.T) {
	// No storage for recordings, and recordings off: heatmaps still get
	// the page behind them.
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	var brand string
	if err := e.pool.QueryRow(t.Context(), `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	key := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "acme.example", "origins": []string{"https://acme.example"}})["key"].(string)
	owner.must(200, "PATCH", base+"/replay/settings", map[string]any{"heatmaps": true})
	heat := map[string]any{"key": key, "path": "/pricing", "width": 1280, "height": 720}

	// A page view without a click asks for nothing.
	heat["depth"], heat["page_height"] = 5, 2000
	if code, out := sendReplay(t, e.h, desktopUA, heat); code != 200 || out["snapshot"] != nil {
		t.Fatalf("no click, no picture asked for: %d %v", code, out)
	}
	delete(heat, "depth")
	delete(heat, "page_height")
	heat["clicks"] = [][2]int{{50, 12}}
	if code, out := sendReplay(t, e.h, desktopUA, heat); code != 200 || out["snapshot"] != true {
		t.Fatalf("a clicked page with no picture asks for one: %d %v", code, out)
	}
	owner.must(404, "GET", base+"/heatmaps/snapshot?path=/pricing", nil)
	if h := owner.must(200, "GET", base+"/heatmaps?path=/pricing", nil); h["snapshot_at"] != nil {
		t.Fatalf("no picture yet: %v", h["snapshot_at"])
	}

	// Anything but the recorder's two events is not kept.
	delete(heat, "clicks")
	heat["snapshot"] = []any{map[string]any{"type": 3}, map[string]any{"type": 2}}
	sendReplay(t, e.h, desktopUA, heat)
	owner.must(404, "GET", base+"/heatmaps/snapshot?path=/pricing", nil)

	heat["snapshot"] = []any{
		map[string]any{"type": 4, "timestamp": 1, "data": map[string]any{"href": "https://acme.example/pricing", "width": 1280, "height": 720}},
		map[string]any{"type": 2, "timestamp": 2, "data": map[string]any{"node": map[string]any{"type": 0, "childNodes": []any{}, "id": 1}}},
	}
	if code, out := sendReplay(t, e.h, desktopUA, heat); code != 200 || out["snapshot"] != nil {
		t.Fatalf("the picture is kept: %d %v", code, out)
	}
	snap := owner.must(200, "GET", base+"/heatmaps/snapshot?path=/pricing", nil)
	if events := snap["events"].([]any); len(events) != 2 || snap["width"].(float64) != 1280 || snap["device"] != "desktop" {
		t.Fatalf("the picture: %v", snap)
	}
	if h := owner.must(200, "GET", base+"/heatmaps?path=/pricing", nil); h["snapshot_at"] == nil || h["recording_id"] != nil {
		t.Fatalf("the heatmap says it has a picture: %v", h)
	}
	// A fresh picture isn't asked for again, a phone's page is its own, and an old one is replaced.
	delete(heat, "snapshot")
	heat["clicks"] = [][2]int{{50, 12}}
	if _, out := sendReplay(t, e.h, desktopUA, heat); out["snapshot"] != nil {
		t.Fatalf("a fresh picture is enough: %v", out)
	}
	owner.must(404, "GET", base+"/heatmaps/snapshot?path=/pricing&device=mobile", nil)
	if _, err := e.pool.Exec(t.Context(), `UPDATE heatmap_snapshots SET taken_at = now() - interval '8 days' WHERE brand_id = $1`, brand); err != nil {
		t.Fatal(err)
	}
	if _, out := sendReplay(t, e.h, desktopUA, heat); out["snapshot"] != true {
		t.Fatalf("a week-old picture is asked for again: %v", out)
	}

	// Clearing the page's heatmap deletes its picture too.
	owner.must(200, "POST", base+"/heatmaps/clear", map[string]any{"path": "/pricing"})
	owner.must(404, "GET", base+"/heatmaps/snapshot?path=/pricing", nil)
}

// A page that changes what it shows without changing its address (the
// steps of a sign-up) gets a heatmap and a picture per screen.
func TestHeatmapScreens(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	var brand string
	if err := e.pool.QueryRow(t.Context(), `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	key := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "acme.example", "origins": []string{"https://acme.example"}})["key"].(string)
	owner.must(200, "PATCH", base+"/replay/settings", map[string]any{"heatmaps": true})
	click := func(screen string, n int) map[string]any {
		clicks := [][2]int{}
		for i := 0; i < n; i++ {
			clicks = append(clicks, [2]int{10 + i, 20})
		}
		body := map[string]any{"key": key, "path": "/onboarding", "width": 1280, "height": 720, "clicks": clicks}
		if screen != "" {
			body["screen"] = screen
		}
		code, out := sendReplay(t, e.h, desktopUA, body)
		if code != 200 {
			t.Fatalf("clicks on %q: %d %v", screen, code, out)
		}
		return out
	}
	// Clicks from before screens were named, then one screen: one heatmap.
	click("", 1)
	if out := click("  Create your\n account # ", 2); out["snapshot"] != true {
		t.Fatalf("a new screen asks for its picture: %v", out)
	}
	h := owner.must(200, "GET", base+"/heatmaps?path=/onboarding", nil)
	if h["total_clicks"].(float64) != 3 || len(h["screens"].([]any)) != 0 || h["screen"] != "Create your account" {
		t.Fatalf("a page with one screen is one heatmap: %v", h)
	}
	// A second screen: the page splits, and shows its most clicked.
	click("Choose a plan", 4)
	h = owner.must(200, "GET", base+"/heatmaps?path=/onboarding", nil)
	screens := h["screens"].([]any)
	if h["screen"] != "Choose a plan" || h["total_clicks"].(float64) != 4 || len(screens) != 3 || screens[0].(map[string]any)["screen"] != "Choose a plan" {
		t.Fatalf("the busiest screen first: %v", h)
	}
	if h := owner.must(200, "GET", base+"/heatmaps?path=/onboarding&screen=Create+your+account", nil); h["total_clicks"].(float64) != 2 {
		t.Fatalf("one screen: %v", h)
	}
	if h := owner.must(200, "GET", base+"/heatmaps?path=/onboarding&screen=", nil); h["total_clicks"].(float64) != 1 || h["screen"] != "" {
		t.Fatalf("the earlier clicks: %v", h)
	}
	// The list shows the page once.
	pages := owner.must(200, "GET", base+"/heatmaps/pages", nil)["data"].([]any)
	if len(pages) != 1 || pages[0].(map[string]any)["path"] != "/onboarding" || pages[0].(map[string]any)["clicks"].(float64) != 7 || pages[0].(map[string]any)["screens"].(float64) != 2 {
		t.Fatalf("pages: %v", pages)
	}
	// Each screen keeps its own picture.
	snap := []any{
		map[string]any{"type": 4, "timestamp": 1, "data": map[string]any{"href": "https://acme.example/onboarding", "width": 1280, "height": 720}},
		map[string]any{"type": 2, "timestamp": 2, "data": map[string]any{"node": map[string]any{"type": 0, "childNodes": []any{}, "id": 1}}},
	}
	sendReplay(t, e.h, desktopUA, map[string]any{"key": key, "path": "/onboarding", "screen": "Choose a plan", "width": 1280, "height": 720, "snapshot": snap})
	owner.must(200, "GET", base+"/heatmaps/snapshot?path=/onboarding&screen=Choose+a+plan", nil)
	owner.must(404, "GET", base+"/heatmaps/snapshot?path=/onboarding&screen=Create+your+account", nil)
	// A page that names too many screens keeps counting under itself.
	for i := 0; i < 12; i++ {
		if _, err := e.pool.Exec(t.Context(), `INSERT INTO heatmap_snapshots (workspace_id, brand_id, path, device, width, body) VALUES ($1, $2, $3, 'mobile', 390, '\x00')`, ws, brand, "/onboarding#Hello "+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	click("Hello stranger", 1)
	if h := owner.must(200, "GET", base+"/heatmaps?path=/onboarding&screen=", nil); h["total_clicks"].(float64) != 2 {
		t.Fatalf("an unknown screen of a full page counts under the page: %v", h)
	}
	// Clearing the page clears its screens.
	owner.must(200, "POST", base+"/heatmaps/clear", map[string]any{"path": "/onboarding"})
	if pages := owner.must(200, "GET", base+"/heatmaps/pages", nil)["data"].([]any); len(pages) != 0 {
		t.Fatalf("cleared: %v", pages)
	}
}
