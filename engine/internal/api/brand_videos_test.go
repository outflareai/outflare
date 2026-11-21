package api_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/videofx"
)

// fakeVideoTool stands in for ffmpeg: it answers with small files of the
// right type and remembers what it was asked.
type fakeVideoTool struct {
	t     *testing.T
	calls []string
	n     int
}

func (f *fakeVideoTool) mp4(what string) []byte {
	f.n++
	return append([]byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2"), []byte(fmt.Sprintf("%s-%d", what, f.n))...)
}

func (f *fakeVideoTool) Probe(context.Context, []byte) (videofx.Info, error) {
	return videofx.Info{Seconds: 2.5, Width: 320, Height: 568, Video: true, Audio: true}, nil
}

func (f *fakeVideoTool) Tighten(_ context.Context, audio []byte, gap float64) ([]byte, error) {
	f.calls = append(f.calls, fmt.Sprintf("tighten %.1f", gap))
	return append(append([]byte{}, audio...), 't'), nil
}

func (f *fakeVideoTool) Finish(_ context.Context, _ []byte, look videofx.Look) ([]byte, error) {
	f.calls = append(f.calls, fmt.Sprintf("finish phone=%v captions=%d", look.Phone, len(look.Captions)))
	return f.mp4("finished"), nil
}

func (f *fakeVideoTool) Cut(_ context.Context, _ []byte, from, to float64) ([]byte, error) {
	f.calls = append(f.calls, fmt.Sprintf("cut %.1f-%.1f", from, to))
	return f.mp4("cut"), nil
}

func (f *fakeVideoTool) Splice(_ context.Context, _, _ []byte, from, to float64) ([]byte, error) {
	f.calls = append(f.calls, fmt.Sprintf("splice %.1f-%.1f", from, to))
	return f.mp4("spliced"), nil
}

func (f *fakeVideoTool) Frame(_ context.Context, _ []byte, at float64) ([]byte, error) {
	f.calls = append(f.calls, fmt.Sprintf("frame %.1f", at))
	f.n++
	png, _ := base64.StdEncoding.DecodeString(testPNG(f.t, 32, 56, uint8(f.n)))
	return png, nil
}

func (f *fakeVideoTool) called(prefix string) int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// makeVideo runs a video's stages and the generations they wait for
// until it is no longer being made.
func makeVideo(t *testing.T, e *env, owner *client, ws, path string) map[string]any {
	t.Helper()
	ctx := context.Background()
	id := path[strings.LastIndex(path, "/")+1:]
	for range 30 {
		if err := e.srv.AdvanceVideo(ctx, ws, id); err != nil {
			t.Fatal(err)
		}
		v := owner.must(200, "GET", path, nil)
		if v["status"] != "working" {
			return v
		}
		rows, err := e.pool.Query(ctx, `SELECT id::text FROM brand_generations WHERE workspace_id = $1 AND status IN ('queued', 'running')
			AND id IN (SELECT generation_id FROM brand_video_steps WHERE video_id = $2)`, ws, id)
		if err != nil {
			t.Fatal(err)
		}
		var gens []string
		for rows.Next() {
			var g string
			rows.Scan(&g)
			gens = append(gens, g)
		}
		rows.Close()
		for _, g := range gens {
			// Twice: once to send it, once to fetch what fal.ai made.
			for range 2 {
				if err := e.srv.RunGeneration(ctx, ws, g); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	t.Fatal("the video was never finished")
	return nil
}

func texts(v map[string]any) string {
	var out []string
	for _, w := range v["words"].([]any) {
		out = append(out, w.(map[string]any)["text"].(string))
	}
	return strings.Join(out, " ")
}

// A talking video is made stage by stage on the brand's own accounts,
// keeps every stage's file and recipe, and is edited by its words.
func TestBrandVideos(t *testing.T) {
	got := track.Capture(t)
	bucket := &memBlobs{m: map[string][]byte{}}
	tool := &fakeVideoTool{t: t}
	e := freshEnvWith(t, func(o *api.Options) { o.Files, o.Video = bucket, tool })
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	fake := &fakeMedia{t: t, started: map[string]int{}, heard: strings.Fields("Meet Northpeak the fastest way too ship")}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	for _, id := range []string{"fal", "fal.files", "fal.rest", "elevenlabs"} {
		e.integrations.Client.BaseURLs[id] = srv.URL
	}
	base := "/v1/brands/" + brand + "/videos"
	scene := owner.must(201, "POST", "/v1/brands/"+brand+"/assets", map[string]any{"kind": "other", "filename": "kitchen.png", "data": testPNG(t, 320, 568, 30)})["id"].(string)
	settings := map[string]any{
		"voice":   map[string]any{"voice": "voice_test_1", "model": "voice-model-test"},
		"words":   map[string]any{"model": "words-model-test"},
		"video":   map[string]any{"model": "acme/talk/reference-to-video", "options": map[string]any{"resolution": "480p"}},
		"upscale": map[string]any{"model": "acme/upscale/video", "options": map[string]any{"target_resolution": "1080p"}},
		"finish":  map[string]any{"tighten": true, "phone_look": true, "captions": true},
	}
	ask := map[string]any{"name": "Kitchen ad", "script": "Meet Northpeak, the fastest way to ship.", "direction": "A woman in a kitchen talks to her phone camera.",
		"scene_asset_id": scene, "settings": settings}

	// No accounts, nothing made.
	if b := owner.must(409, "POST", base, ask); b["error"].(map[string]any)["code"] != "connector_needed" {
		t.Fatalf("without connections: %v", b)
	}
	for _, c := range []string{"fal", "elevenlabs"} {
		owner.must(201, "POST", "/v1/workspaces/"+ws+"/integrations", map[string]any{"connector": c, "credentials": map[string]any{"api_key": mediaKey}})
	}
	// What isn't accepted.
	owner.must(400, "POST", base, map[string]any{"script": " ", "settings": settings})
	owner.must(400, "POST", base, map[string]any{"script": "Hello", "settings": map[string]any{"voice": map[string]any{"model": "m"}}})
	owner.must(400, "POST", base, map[string]any{"script": "Hello", "scene_asset_id": "00000000-0000-4000-8000-000000000000", "settings": settings})

	v := owner.must(201, "POST", base, ask)
	path := base + "/" + v["id"].(string)
	if v["status"] != "working" || v["stage"] != "voice" || fake.started["recordings"] != 0 {
		t.Fatalf("asked: %v", v)
	}
	if list := owner.must(200, "GET", base, nil); len(list["data"].([]any)) != 1 || list["finishing"] != true {
		t.Fatalf("list: %v", list)
	}
	v = makeVideo(t, e, owner, ws, path)
	if v["status"] != "ready" || v["stage"] != "done" || v["final_asset_id"] == nil || v["final_asset_id"] == v["base_asset_id"] || v["base_asset_id"] == v["render_asset_id"] {
		t.Fatalf("made: %v", v)
	}
	if texts(v) != "Meet Northpeak the fastest way too ship" || v["seconds"] != 6.6 {
		t.Fatalf("words: %v", v["words"])
	}
	// The voice read "too" where the script has "to": a note says where.
	if notes := v["notes"].([]any); len(notes) != 1 || !strings.Contains(notes[0].(string), `"too"`) || !strings.Contains(notes[0].(string), "5.0 seconds") {
		t.Fatalf("notes: %v", notes)
	}
	var stages []string
	steps := map[string]map[string]any{}
	for _, s := range v["steps"].([]any) {
		st := s.(map[string]any)
		stages = append(stages, st["stage"].(string)+":"+st["status"].(string))
		steps[st["stage"].(string)] = st
	}
	if strings.Join(stages, " ") != "voice:done tighten:done words:done render:done upscale:done finish:done" {
		t.Fatalf("steps: %v", stages)
	}
	// The render got the scene and the tightened voice-over, and the
	// script; the upscale got the render and no prompt.
	render := fake.sent["/acme/talk/reference-to-video"][0]
	if len(render["image_urls"].([]any)) != 1 || len(render["audio_urls"].([]any)) != 1 || render["resolution"] != "480p" ||
		!strings.Contains(render["prompt"].(string), "A woman in a kitchen") || !strings.Contains(render["prompt"].(string), "fastest way to ship") {
		t.Fatalf("render request: %v", render)
	}
	if up := fake.sent["/acme/upscale/video"][0]; up["video_url"] == nil || up["prompt"] != nil || up["target_resolution"] != "1080p" {
		t.Fatalf("upscale request: %v", up)
	}
	if fake.started["recordings"] != 1 || fake.started["transcripts"] != 1 || fake.started["uploads"] != 3 || fake.started["puts"] != 3 ||
		tool.called("tighten") != 1 || tool.called("finish phone=true captions=") != 1 {
		t.Fatalf("calls: %v, tool %v", fake.started, tool.calls)
	}
	// Each stage's generation keeps its recipe.
	gen := owner.must(200, "GET", "/v1/brands/"+brand+"/generations/"+steps["render"]["generation_id"].(string), nil)
	if len(gen["references"].([]any)) != 2 || gen["request"].(map[string]any)["endpoint"] != "acme/talk/reference-to-video" || gen["response"].(map[string]any)["seed"] != float64(7) {
		t.Fatalf("render recipe: %v", gen)
	}
	final := v["final_asset_id"]

	// Remove a word: cut out, captions redone, nothing rendered again.
	owner.must(400, "POST", path+"/edits", map[string]any{"action": "remove", "from": 0, "to": 6})
	owner.must(400, "POST", path+"/edits", map[string]any{"action": "remove", "from": 3, "to": 9})
	owner.must(400, "POST", path+"/edits", map[string]any{"action": "polish", "from": 0, "to": 0})
	if v = owner.must(200, "POST", path+"/edits", map[string]any{"action": "remove", "from": 2, "to": 2}); v["status"] != "working" || v["stage"] != "edit_cut" || v["round"] != float64(1) {
		t.Fatalf("remove asked: %v", v)
	}
	owner.must(409, "POST", path+"/edits", map[string]any{"action": "remove", "from": 0, "to": 0})
	owner.must(409, "POST", path+"/fix/keep", nil)
	// The fix is made but waits: the video is as it was until a person
	// keeps it, and the fix is there to look at as a version.
	v = makeVideo(t, e, owner, ws, path)
	if v["status"] != "ready" || texts(v) != "Meet Northpeak the fastest way too ship" || v["final_asset_id"] != final || v["edit"] != nil || v["waiting_version"] != float64(2) ||
		tool.called("cut 1.8-2.8") != 1 || len(fake.sent["/acme/talk/reference-to-video"]) != 1 {
		t.Fatalf("removed, waiting: %v, tool %v", v, tool.calls)
	}
	versions := v["versions"].([]any)
	if w := versions[0].(map[string]any); len(versions) != 2 || w["state"] != "waiting" || texts(w) != "Meet Northpeak fastest way too ship" || w["final_asset_id"] == final ||
		w["edit"].(map[string]any)["action"] != "remove" || versions[1].(map[string]any)["state"] != "current" {
		t.Fatalf("versions: %v", versions)
	}
	// Nothing else is changed while a fix waits.
	owner.must(409, "POST", path+"/edits", map[string]any{"action": "remove", "from": 0, "to": 0})
	owner.must(409, "POST", path+"/versions/1/restore", nil)
	// Yes: the fix is the video, and the one before is kept.
	v = owner.must(200, "POST", path+"/fix/keep", nil)
	if texts(v) != "Meet Northpeak fastest way too ship" || v["final_asset_id"] == final || v["waiting_version"] != nil ||
		v["versions"].([]any)[0].(map[string]any)["state"] != "current" || v["versions"].([]any)[1].(map[string]any)["state"] != "before" {
		t.Fatalf("kept: %v", v)
	}
	owner.must(409, "POST", path+"/fix/keep", nil)
	owner.must(409, "POST", path+"/fix/go-back", map[string]any{})
	// Later words moved up by the second that was cut.
	if w := v["words"].([]any)[2].(map[string]any); w["text"] != "fastest" || w["start"] != float64(2) {
		t.Fatalf("moved words: %v", v["words"])
	}

	// Redo a word the voice read wrong: read again as spelled, only that
	// stretch rendered from the frame it starts on, and stitched in.
	fake.heard = strings.Fields("way two ship")
	if v = owner.must(200, "POST", path+"/edits", map[string]any{"action": "redo", "from": 4, "to": 4, "text": "to", "say": "tuh", "note": "She says too, not to"}); v["stage"] != "edit_voice" {
		t.Fatalf("redo asked: %v", v)
	}
	// One word is too short to render: it took in its neighbours.
	if ed := v["edit"].(map[string]any); ed["first_word"] != float64(3) || ed["last_word"] != float64(5) {
		t.Fatalf("widened: %v", ed)
	}
	v = makeVideo(t, e, owner, ws, path)
	if v["status"] != "ready" || v["error"] != nil || texts(v) != "Meet Northpeak fastest way too ship" || v["round"] != float64(2) || v["waiting_version"] != float64(3) {
		t.Fatalf("redone, waiting: %v", v)
	}
	if last := fake.read[len(fake.read)-1]; last != "way tuh ship" {
		t.Fatalf("the voice was asked to read %q", last)
	}
	part := fake.sent["/acme/talk/reference-to-video"][1]
	if len(part["image_urls"].([]any)) != 2 || !strings.Contains(part["prompt"].(string), "way to ship") || !strings.Contains(part["prompt"].(string), "She says too, not to") ||
		len(fake.sent["/acme/upscale/video"]) != 2 || tool.called("frame 2.8") != 1 || tool.called("splice 2.8-") != 1 || tool.called("finish") != 3 {
		t.Fatalf("redo requests: %v, tool %v", part, tool.calls)
	}
	// No: the video stays as it was, the fix is kept with why, and the
	// same words are made again, told what was wrong.
	owner.must(400, "POST", path+"/fix/go-back", map[string]any{"reason": strings.Repeat("x", 201)})
	v = owner.must(200, "POST", path+"/fix/go-back", map[string]any{"reason": "Looks different from the rest", "note": "Her head jumps at the cut", "try_again": true, "say": "tu"})
	if v["status"] != "working" || v["round"] != float64(3) || v["waiting_version"] != nil || v["edit"].(map[string]any)["say"] != "tu" {
		t.Fatalf("gone back, trying again: %v", v)
	}
	if gone := v["versions"].([]any)[0].(map[string]any); gone["state"] != "gone_back" || gone["reason"] != "Looks different from the rest" || gone["note"] != "Her head jumps at the cut" {
		t.Fatalf("turned down version: %v", gone)
	}
	v = makeVideo(t, e, owner, ws, path)
	if again := fake.sent["/acme/talk/reference-to-video"][2]; v["waiting_version"] != float64(4) || !strings.Contains(again["prompt"].(string), "Her head jumps at the cut") ||
		fake.read[len(fake.read)-1] != "way tu ship" {
		t.Fatalf("made again: %v, %v", v, again)
	}
	// Yes, and remember how to say it: every later voice-over gets it.
	v = owner.must(200, "POST", path+"/fix/keep", map[string]any{"remember": true})
	if texts(v) != "Meet Northpeak fastest way to ship" || len(v["versions"].([]any)) != 4 {
		t.Fatalf("kept the second try: %v", v)
	}
	lessons := owner.must(200, "GET", "/v1/brands/"+brand+"/creative-lessons", nil)["data"].([]any)
	if l := lessons[0].(map[string]any); len(lessons) != 1 || l["kind"] != "say" || l["word"] != "to" || l["say"] != "tu" || l["source_id"] != v["id"] {
		t.Fatalf("lessons: %v", lessons)
	}
	// A rule goes into every later prompt.
	owner.must(400, "POST", "/v1/brands/"+brand+"/creative-lessons", map[string]any{"kind": "rule"})
	owner.must(400, "POST", "/v1/brands/"+brand+"/creative-lessons", map[string]any{"kind": "say", "word": "x"})
	rule := owner.must(201, "POST", "/v1/brands/"+brand+"/creative-lessons", map[string]any{"kind": "rule", "rule": "No logos on clothing", "applies_to": "video", "source_type": "video", "source_id": v["id"]})
	if rule["kind"] != "rule" || rule["applies_to"] != "video" {
		t.Fatalf("rule: %v", rule)
	}

	// An edit the provider refuses leaves the video as it was.
	final = v["final_asset_id"]
	fake.refuseFal = true
	owner.must(200, "POST", path+"/edits", map[string]any{"action": "redo", "from": 0, "to": 0})
	v = makeVideo(t, e, owner, ws, path)
	if v["status"] != "ready" || v["final_asset_id"] != final || v["error_code"] != "connector_invalid" || !strings.Contains(v["error"].(string), "The edit was not made") || v["edit"] != nil ||
		v["waiting_version"] != nil || texts(v) != "Meet Northpeak fastest way to ship" {
		t.Fatalf("refused edit: %v", v)
	}
	fake.refuseFal = false

	// Go back to the first version: the one it replaces is kept too.
	owner.must(404, "POST", path+"/versions/9/restore", nil)
	owner.must(409, "POST", path+"/versions/4/restore", nil)
	if v = owner.must(200, "POST", path+"/versions/1/restore", nil); texts(v) != "Meet Northpeak the fastest way too ship" || len(v["versions"].([]any)) != 4 {
		t.Fatalf("first version again: %v", v)
	}

	// Like it, say why, and take it back.
	owner.must(400, "POST", path+"/rating", map[string]any{"rating": "great"})
	if v = owner.must(200, "POST", path+"/rating", map[string]any{"rating": "liked", "reason": "The kitchen feels real"}); v["rating"] != "liked" || v["rating_reason"] != "The kitchen feels real" || v["rated_at"] == nil {
		t.Fatalf("liked: %v", v)
	}
	owner.must(200, "POST", path+"/rating", map[string]any{"rating": "not_this", "reason": "Too slow"})
	if v = owner.must(200, "POST", path+"/rating", map[string]any{"rating": ""}); v["rating"] != "" || v["rated_at"] != nil {
		t.Fatalf("rating taken back: %v", v)
	}

	// More of what works: the same video with one thing changed.
	owner.must(400, "POST", path+"/variations", map[string]any{"change": "music", "values": []string{"x"}})
	owner.must(400, "POST", path+"/variations", map[string]any{"change": "first_line", "values": []string{}})
	owner.must(400, "POST", path+"/variations", map[string]any{"change": "scene", "values": []string{"00000000-0000-4000-8000-000000000000"}})
	vars := owner.must(201, "POST", path+"/variations", map[string]any{"change": "first_line", "values": []string{"Tired of slow releases?", "Ship twice as fast."}})["data"].([]any)
	first := vars[0].(map[string]any)
	if len(vars) != 2 || first["variation_of"] != v["id"] || first["status"] != "working" || first["variation_change"].(map[string]any)["change"] != "first_line" ||
		vars[1].(map[string]any)["script"] != "Ship twice as fast." || first["name"] != "Kitchen ad (variation 1)" {
		t.Fatalf("variations: %v", vars)
	}
	// A variation of a variation joins the same family.
	voice := owner.must(201, "POST", base+"/"+first["id"].(string)+"/variations", map[string]any{"change": "voice", "values": []string{"voice_test_2"}})["data"].([]any)[0].(map[string]any)
	if voice["variation_of"] != v["id"] || voice["settings"].(map[string]any)["voice"].(map[string]any)["voice"] != "voice_test_2" || voice["script"] != first["script"] {
		t.Fatalf("variation of a variation: %v", voice)
	}
	if family := owner.must(200, "GET", base+"?family="+v["id"].(string), nil)["data"].([]any); len(family) != 4 {
		t.Fatalf("family: %d", len(family))
	}
	if v = owner.must(200, "GET", path, nil); v["variations"] != float64(3) {
		t.Fatalf("variations counted: %v", v["variations"])
	}
	owner.must(400, "GET", base+"?family=x", nil)
	for _, made := range append(vars, voice) {
		owner.must(204, "DELETE", base+"/"+made.(map[string]any)["id"].(string), nil)
	}

	// One history holds all of it, newest first, with what was said.
	var kinds []string
	said := map[string]string{}
	for _, h := range owner.must(200, "GET", path+"/history", nil)["data"].([]any) {
		line := h.(map[string]any)
		kinds = append(kinds, line["kind"].(string))
		said[line["kind"].(string)] += line["said"].(string) + "|"
		if line["kind"] == "liked" && line["actor_id"] == nil {
			t.Fatalf("history line without its person: %v", line)
		}
	}
	if strings.Join(kinds, " ") != "variation_made rating_cleared not_this liked version_restored fix_failed fix_asked fix_kept fix_ready fix_asked gone_back fix_ready fix_asked fix_kept fix_ready fix_asked made" {
		t.Fatalf("history: %v", kinds)
	}
	if !strings.Contains(said["gone_back"], "Her head jumps") || !strings.Contains(said["fix_asked"], "She says too") || !strings.Contains(said["liked"], "kitchen feels real") {
		t.Fatalf("what was said: %v", said)
	}
	owner.must(404, "GET", base+"/00000000-0000-4000-8000-000000000000/history", nil)

	// A new video the provider refuses fails, and can be tried again
	// without paying for the voice twice.
	fake.refuseFal = true
	bad := owner.must(201, "POST", base, ask)
	badPath := base + "/" + bad["id"].(string)
	fake.heard = strings.Fields("Meet Northpeak the fastest way to ship")
	if v = makeVideo(t, e, owner, ws, badPath); v["status"] != "failed" || v["stage"] != "render" || v["error_code"] != "connector_invalid" {
		t.Fatalf("refused video: %v", v)
	}
	owner.must(409, "POST", badPath+"/edits", map[string]any{"action": "remove", "from": 0, "to": 0})
	fake.refuseFal = false
	recordings := fake.started["recordings"]
	owner.must(200, "POST", badPath+"/retry", nil)
	if v = makeVideo(t, e, owner, ws, badPath); v["status"] != "ready" || fake.started["recordings"] != recordings || len(v["notes"].([]any)) != 0 {
		t.Fatalf("tried again: %v", v)
	}
	owner.must(409, "POST", badPath+"/retry", nil)
	// What the brand taught went into it: the word as it is said, and the rule.
	if last := fake.sent["/acme/talk/reference-to-video"]; !strings.Contains(last[len(last)-1]["prompt"].(string), "No logos on clothing") ||
		!strings.Contains(strings.Join(fake.read, "|"), "fastest way tu ship") {
		t.Fatalf("lessons not used: %v, read %v", last[len(last)-1]["prompt"], fake.read)
	}
	owner.must(204, "DELETE", "/v1/brands/"+brand+"/creative-lessons/"+rule["id"].(string), nil)
	owner.must(404, "DELETE", "/v1/brands/"+brand+"/creative-lessons/"+rule["id"].(string), nil)
	if left := owner.must(200, "GET", "/v1/brands/"+brand+"/creative-lessons", nil)["data"].([]any); len(left) != 1 {
		t.Fatalf("lessons left: %v", left)
	}

	// Rename and delete; another brand sees nothing.
	if v = owner.must(200, "PATCH", badPath, map[string]any{"name": "Second take"}); v["name"] != "Second take" {
		t.Fatalf("renamed: %v", v)
	}
	owner.must(204, "DELETE", badPath, nil)
	owner.must(404, "GET", badPath, nil)
	other := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Summitly"})["id"].(string)
	owner.must(404, "GET", "/v1/brands/"+other+"/videos/"+path[strings.LastIndex(path, "/")+1:], nil)
	if list := owner.must(200, "GET", "/v1/brands/"+other+"/videos", nil)["data"].([]any); len(list) != 0 {
		t.Fatalf("other brand's videos: %v", list)
	}
	owner.must(400, "GET", base+"?status=waiting", nil)

	want := map[string]int{"video.requested": 5, "video.completed": 5, "video.edit_requested": 4, "video.edit_failed": 1, "video.failed": 1, "video.retried": 1, "video.renamed": 1, "video.deleted": 4,
		"video.fix_kept": 2, "video.fix_discarded": 1, "video.version_restored": 1, "video.rated": 3, "video.variations_requested": 2, "creative_lesson.added": 2, "creative_lesson.removed": 1}
	seen := map[string]int{}
	for _, ev := range got() {
		if _, ok := want[ev.Name]; ok {
			seen[ev.Name]++
			for k := range ev.Props {
				if k == "script" || k == "name" || k == "error" || k == "text" || k == "reason" || k == "note" || k == "value" || k == "rule" || k == "word" {
					t.Fatalf("%s carries %s", ev.Name, k)
				}
			}
		}
	}
	for name, n := range want {
		if seen[name] != n {
			t.Fatalf("%s events: %d, want %d (all: %v)", name, seen[name], n, seen)
		}
	}
}

// Without ffmpeg a video is still made, as the model made it, and says
// what was left out; it can't be edited.
func TestBrandVideosWithoutFFmpeg(t *testing.T) {
	bucket := &memBlobs{m: map[string][]byte{}}
	e := freshEnvWith(t, func(o *api.Options) { o.Files = bucket })
	owner, ws := setupOwner(t, e.h)
	var brand string
	if err := e.pool.QueryRow(t.Context(), `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	fake := &fakeMedia{t: t, started: map[string]int{}, heard: strings.Fields("Meet Northpeak")}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	for _, id := range []string{"fal", "fal.files", "fal.rest", "elevenlabs"} {
		e.integrations.Client.BaseURLs[id] = srv.URL
		if !strings.Contains(id, ".") {
			owner.must(201, "POST", "/v1/workspaces/"+ws+"/integrations", map[string]any{"connector": id, "credentials": map[string]any{"api_key": mediaKey}})
		}
	}
	base := "/v1/brands/" + brand + "/videos"
	v := owner.must(201, "POST", base, map[string]any{"script": "Meet Northpeak.", "settings": map[string]any{
		"voice": map[string]any{"voice": "voice_test_1", "model": "voice-model-test"}, "words": map[string]any{"model": "words-model-test"},
		"video": map[string]any{"model": "acme/talk/reference-to-video"}, "finish": map[string]any{"tighten": true, "phone_look": true, "captions": true}}})
	path := base + "/" + v["id"].(string)
	v = makeVideo(t, e, owner, ws, path)
	if v["status"] != "ready" || v["final_asset_id"] != v["render_asset_id"] || len(v["notes"].([]any)) != 2 {
		t.Fatalf("made without ffmpeg: %v", v)
	}
	// No scene picture: only the voice-over went along.
	if r := fake.sent["/acme/talk/reference-to-video"][0]; r["image_urls"] != nil || len(r["audio_urls"].([]any)) != 1 {
		t.Fatalf("render request: %v", r)
	}
	if b := owner.must(409, "POST", path+"/edits", map[string]any{"action": "remove", "from": 0, "to": 0}); b["error"].(map[string]any)["code"] != "not_configured" {
		t.Fatalf("edit without ffmpeg: %v", b)
	}
	if list := owner.must(200, "GET", base, nil); list["finishing"] != false {
		t.Fatalf("list: %v", list)
	}
}
