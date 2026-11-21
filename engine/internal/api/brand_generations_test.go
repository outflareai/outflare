package api_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/track"
)

const (
	mediaKey       = "test-not-a-real-key"
	openAIMediaKey = "sk-test-not-a-real-key"
)

// fakeMedia stands in for OpenAI's image API, fal.ai's queue and file
// host, and ElevenLabs. It counts what was started, so a test can tell a
// render was never started twice.
type fakeMedia struct {
	t         *testing.T
	mu        sync.Mutex
	started   map[string]int // pictures, renders, recordings
	rendering bool           // the fal render is still going
	refuse    bool           // OpenAI's safety rules refuse the prompt
	prompt    string
	// For talking videos: what fal.ai was sent per model, what the voice
	// was asked to read, what speech to text hears, and a model that
	// refuses.
	sent      map[string][]map[string]any
	read      []string
	heard     []string
	refuseFal bool
	runs      int
}

const falRun = "019a1b2c-3d4e-7f80-9a1b-2c3d4e5f6a7b"

func (f *fakeMedia) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, p := r.Header, r.URL.Path
	file := strings.HasPrefix(p, "/files/") || strings.HasPrefix(p, "/put/")
	if !file && h.Get("Authorization") != "Bearer "+openAIMediaKey && h.Get("Authorization") != "Key "+mediaKey && h.Get("xi-api-key") != mediaKey {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"detail":"Invalid credentials"}`)
		return
	}
	var body map[string]any
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case p == "/storage/upload/initiate": // fal.ai takes a reference file
		f.started["uploads"]++
		n := strconv.Itoa(f.started["uploads"])
		io.WriteString(w, `{"file_url":"https://v3.fal.media/files/up/`+n+`","upload_url":"https://upload.fal.media/put/`+n+`"}`)
	case r.Method == "PUT" && strings.HasPrefix(p, "/put/"):
		f.started["puts"]++
	case r.Method == "POST" && (p == "/acme/talk/reference-to-video" || p == "/acme/upscale/video"):
		if f.refuseFal {
			w.WriteHeader(http.StatusUnprocessableEntity)
			io.WriteString(w, `{"detail":[{"msg":"audio is shorter than 1.8 seconds"}]}`)
			return
		}
		f.runs++
		if f.sent == nil {
			f.sent = map[string][]map[string]any{}
		}
		f.sent[p] = append(f.sent[p], body)
		json.NewEncoder(w).Encode(map[string]any{"status": "IN_QUEUE", "request_id": fmt.Sprintf("019a1b2c-3d4e-7f80-9a1b-%012d", f.runs)})
	case strings.HasPrefix(p, "/acme/talk/requests/") || strings.HasPrefix(p, "/acme/upscale/requests/"):
		if strings.HasSuffix(p, "/status") {
			io.WriteString(w, `{"status":"COMPLETED"}`)
			return
		}
		w.Header().Set("X-Fal-Billable-Units", "20")
		io.WriteString(w, `{"video":{"url":"https://v3.fal.media/files/b/`+p[strings.LastIndex(p, "/")+1:]+`.mp4"},"seed":7}`)
	case strings.HasPrefix(p, "/files/b/"):
		w.Header().Set("Content-Type", "video/mp4")
		w.Write(append([]byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2"), []byte(p)...))
	case p == "/v1/speech-to-text":
		f.started["transcripts"]++
		words := []map[string]any{}
		for i, word := range f.heard {
			words = append(words, map[string]any{"text": word, "start": float64(i), "end": float64(i) + 0.6, "type": "word"},
				map[string]any{"text": " ", "start": float64(i) + 0.6, "end": float64(i) + 1, "type": "spacing"})
		}
		json.NewEncoder(w).Encode(map[string]any{"language_code": "eng", "text": strings.Join(f.heard, " "), "words": words})
	case p == "/images/edits":
		f.started["edits"]++
		png, _ := base64.StdEncoding.DecodeString(testPNG(f.t, 320, 320, 77))
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"b64_json": base64.StdEncoding.EncodeToString(png)}}, "output_format": "png"})
	case p == "/models": // OpenAI's key check
		io.WriteString(w, `{"data":[{"id":"gpt-test"}]}`)
	case p == "/images/generations":
		if f.refuse {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"Rejected by the safety system.","code":"moderation_blocked"}}`)
			return
		}
		f.started["pictures"]++
		f.prompt, _ = body["prompt"].(string)
		png, _ := base64.StdEncoding.DecodeString(testPNG(f.t, 640, 320, 42))
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"b64_json": base64.StdEncoding.EncodeToString(png)}},
			"output_format": "png", "usage": map[string]any{"input_tokens": 20, "output_tokens": 3000, "total_tokens": 3020}})
	case r.Method == "POST" && p == "/acme/render/text-to-video":
		f.started["renders"]++
		json.NewEncoder(w).Encode(map[string]any{"status": "IN_QUEUE", "request_id": falRun})
	case p == "/acme/render/requests/"+falRun+"/status":
		if f.rendering {
			w.WriteHeader(http.StatusAccepted)
			io.WriteString(w, `{"status":"IN_PROGRESS"}`)
			return
		}
		io.WriteString(w, `{"status":"COMPLETED"}`)
	case p == "/acme/render/requests/"+falRun:
		w.Header().Set("X-Fal-Billable-Units", "5")
		io.WriteString(w, `{"video":{"url":"https://v3.fal.media/files/a/out.mp4","content_type":"video/mp4"}}`)
	case p == "/files/a/out.mp4":
		w.Header().Set("Content-Type", "video/mp4")
		w.Write(append([]byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2"), bytes.Repeat([]byte{7}, 64)...))
	case p == "/v1/user/subscription":
		io.WriteString(w, `{"tier":"creator","character_count":10,"character_limit":1000}`)
	case p == "/v2/voices":
		io.WriteString(w, `{"voices":[{"voice_id":"voice_test_1","name":"Ana","category":"cloned"}]}`)
	case p == "/v1/text-to-speech/voice_test_1":
		f.started["recordings"]++
		text, _ := body["text"].(string)
		f.read = append(f.read, text)
		w.Header().Set("Content-Type", "audio/mpeg")
		// An MP3 with no ID3 tag: it starts straight with a frame. Each
		// recording differs, as a real one would.
		w.Write(append(append([]byte{0xFF, 0xFB, 0x90, 0x64}, bytes.Repeat([]byte{0}, 400)...), byte(f.started["recordings"])))
	default:
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"status":"NOT_FOUND"}`)
	}
}

// Pictures, video and voice are made on the brand's own accounts, kept
// with its files, and never without the connection.
func TestBrandGenerations(t *testing.T) {
	got := track.Capture(t)
	bucket := &memBlobs{m: map[string][]byte{}}
	e := freshEnvWith(t, func(o *api.Options) { o.Files = bucket })
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	fake := &fakeMedia{t: t, started: map[string]int{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	for _, id := range []string{"openai", "fal", "fal.files", "fal.rest", "elevenlabs"} {
		e.integrations.Client.BaseURLs[id] = srv.URL
	}
	base := "/v1/brands/" + brand + "/generations"
	picture := map[string]any{"kind": "image", "model": "img-model-test", "prompt": "A red fox on a laptop", "options": map[string]any{"size": "1536x1024"}, "name": "Fox ad"}

	// No account, nothing made: the answer names what to connect.
	if b := owner.must(409, "POST", base, picture); b["error"].(map[string]any)["code"] != "connector_needed" {
		t.Fatalf("without a connection: %v", b)
	}
	providers := owner.must(200, "GET", base+"/providers", nil)["data"].([]any)
	if len(providers) != 3 || providers[0].(map[string]any)["ready"] != false {
		t.Fatalf("providers before connecting: %v", providers)
	}

	connect := func(connector string) {
		key := mediaKey
		if connector == "openai" {
			key = openAIMediaKey
		}
		owner.must(201, "POST", "/v1/workspaces/"+ws+"/integrations", map[string]any{"connector": connector, "credentials": map[string]any{"api_key": key}})
	}
	connect("openai")
	connect("fal")
	connect("elevenlabs")
	for _, p := range owner.must(200, "GET", base+"/providers", nil)["data"].([]any) {
		if p.(map[string]any)["ready"] != true {
			t.Fatalf("providers after connecting: %v", p)
		}
	}

	// What isn't accepted, before anything is queued.
	owner.must(400, "POST", base, map[string]any{"kind": "banner", "model": "m", "prompt": "x"})
	owner.must(400, "POST", base, map[string]any{"kind": "image", "prompt": "x"})
	owner.must(400, "POST", base, map[string]any{"kind": "image", "model": "m", "prompt": " "})
	owner.must(400, "POST", base, map[string]any{"kind": "voice", "model": "m", "prompt": "Hello"})
	owner.must(400, "POST", base, map[string]any{"kind": "image", "model": "m", "prompt": "x", "provider": "elevenlabs"})
	owner.must(400, "POST", base, map[string]any{"kind": "image", "model": "m", "prompt": "x", "asset_kind": "font"})

	// A picture: queued, made in one call, kept as a brand file.
	g := owner.must(201, "POST", base, picture)
	id := g["id"].(string)
	if g["status"] != "queued" || g["provider"] != "openai" || g["asset_id"] != nil || fake.started["pictures"] != 0 {
		t.Fatalf("queued: %v", g)
	}
	if err := e.srv.RunGeneration(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	g = owner.must(200, "GET", base+"/"+id, nil)
	if g["status"] != "done" || g["asset_id"] == nil || g["usage"].(map[string]any)["output_tokens"] != float64(3000) || fake.prompt != "A red fox on a laptop" {
		t.Fatalf("done: %v", g)
	}
	asset := owner.must(200, "GET", "/v1/brands/"+brand+"/assets/"+g["asset_id"].(string), nil)
	if asset["source"] != "generated" || asset["name"] != "Fox ad" || asset["kind"] != "other" || asset["content_type"] != "image/png" || asset["width"] != float64(640) || asset["usable"] != true {
		t.Fatalf("asset: %v", asset)
	}
	if code, _, body := owner.download("/v1/brands/" + brand + "/assets/" + g["asset_id"].(string) + "/file"); code != 200 {
		t.Fatalf("file: %d", code)
	} else if cfg, _, err := image.DecodeConfig(bytes.NewReader(body)); err != nil || cfg.Width != 640 {
		t.Fatalf("file body: %v %v", cfg, err)
	}
	// Running it again does nothing: it is done.
	if err := e.srv.RunGeneration(ctx, ws, id); err != nil || fake.started["pictures"] != 1 {
		t.Fatalf("run again: %v, pictures %d", err, fake.started["pictures"])
	}

	// A prompt the provider refuses fails in words, and is not retried.
	fake.refuse = true
	refused := owner.must(201, "POST", base, picture)["id"].(string)
	if err := e.srv.RunGeneration(ctx, ws, refused); err != nil {
		t.Fatal(err)
	}
	if g = owner.must(200, "GET", base+"/"+refused, nil); g["status"] != "failed" || g["error_code"] != "connector_invalid" || !strings.Contains(g["error"].(string), "safety rules") {
		t.Fatalf("refused: %v", g)
	}
	fake.refuse = false

	// A video renders for a while: it is asked about, never started twice.
	fake.rendering = true
	video := owner.must(201, "POST", base, map[string]any{"kind": "video", "model": "acme/render/text-to-video", "prompt": "The fox runs", "options": map[string]any{"duration": 5}, "asset_kind": "example_ad"})["id"].(string)
	for range 3 {
		if err := e.srv.RunGeneration(ctx, ws, video); err != nil {
			t.Fatal(err)
		}
	}
	if g = owner.must(200, "GET", base+"/"+video, nil); g["status"] != "running" || fake.started["renders"] != 1 {
		t.Fatalf("rendering: %v, renders %d", g, fake.started["renders"])
	}
	var checks int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE workspace_id = $1 AND kind = 'asset.generate' AND payload->>'generation_id' = $2 AND run_at > now()`, ws, video).Scan(&checks)
	if checks == 0 {
		t.Fatal("no later look is queued while the video renders")
	}
	fake.rendering = false
	if err := e.srv.RunGeneration(ctx, ws, video); err != nil {
		t.Fatal(err)
	}
	g = owner.must(200, "GET", base+"/"+video, nil)
	if g["status"] != "done" || fake.started["renders"] != 1 || g["usage"].(map[string]any)["billable_units"] != float64(5) {
		t.Fatalf("video done: %v, renders %d", g, fake.started["renders"])
	}
	if asset = owner.must(200, "GET", "/v1/brands/"+brand+"/assets/"+g["asset_id"].(string), nil); asset["family"] != "video" || asset["kind"] != "example_ad" {
		t.Fatalf("video asset: %v", asset)
	}

	// A step that died after sending is never sent again.
	lost := owner.must(201, "POST", base, picture)["id"].(string)
	if _, err := e.pool.Exec(ctx, `UPDATE brand_generations SET status = 'running', started_at = now() WHERE id = $1`, lost); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.RunGeneration(ctx, ws, lost); err != nil {
		t.Fatal(err)
	}
	if g = owner.must(200, "GET", base+"/"+lost, nil); g["status"] != "failed" || g["error_code"] != "generation_interrupted" || fake.started["pictures"] != 1 {
		t.Fatalf("interrupted: %v, pictures %d", g, fake.started["pictures"])
	}

	// A voice-over, in a voice the account has.
	voices := owner.must(200, "GET", base+"/voices", nil)["data"].([]any)
	if len(voices) != 1 || voices[0].(map[string]any)["id"] != "voice_test_1" {
		t.Fatalf("voices: %v", voices)
	}
	voice := owner.must(201, "POST", base, map[string]any{"kind": "voice", "model": "voice-model-test", "voice": "voice_test_1", "prompt": "Meet Northpeak."})["id"].(string)
	if err := e.srv.RunGeneration(ctx, ws, voice); err != nil {
		t.Fatal(err)
	}
	g = owner.must(200, "GET", base+"/"+voice, nil)
	if g["status"] != "done" || g["usage"].(map[string]any)["characters"] != float64(15) {
		t.Fatalf("voice: %v", g)
	}
	if asset = owner.must(200, "GET", "/v1/brands/"+brand+"/assets/"+g["asset_id"].(string), nil); asset["content_type"] != "audio/mpeg" || asset["family"] != "audio" {
		t.Fatalf("voice asset: %v", asset)
	}

	// The recipe of a file: what was sent and what came back, found from
	// the file, and used again with one thing changed.
	g = owner.must(200, "GET", base+"/"+id, nil)
	if req, _ := g["request"].(map[string]any); req == nil || req["endpoint"] != "images/generations" || g["response"] == nil || g["recipe_of"] != nil {
		t.Fatalf("recipe: %v", g)
	}
	if list := owner.must(200, "GET", base+"?asset_id="+g["asset_id"].(string), nil)["data"].([]any); len(list) != 1 || list[0].(map[string]any)["id"] != id {
		t.Fatalf("recipe by file: %v", list)
	}
	fox := g["asset_id"].(string)
	again := owner.must(201, "POST", base+"/"+id+"/reuse", map[string]any{"prompt": "A red fox on a phone"})
	if again["recipe_of"] != id || again["model"] != "img-model-test" || again["prompt"] != "A red fox on a phone" || again["name"] != "Fox ad" || again["options"].(map[string]any)["size"] != "1536x1024" {
		t.Fatalf("reused: %v", again)
	}
	owner.must(404, "POST", base+"/"+voice+"x/reuse", map[string]any{})

	// A picture made from another as its reference goes to the edit call.
	owner.must(400, "POST", base, map[string]any{"kind": "image", "model": "m", "prompt": "x", "references": []any{map[string]any{"asset_id": voice}}})
	owner.must(400, "POST", base, map[string]any{"kind": "image", "model": "m", "prompt": "x", "references": []any{map[string]any{"asset_id": fox, "as": "Not An Input"}}})
	owner.must(400, "POST", base, map[string]any{"kind": "voice", "model": "m", "voice": "voice_test_1", "prompt": "x", "references": []any{map[string]any{"asset_id": fox}}})
	same := owner.must(201, "POST", base, map[string]any{"kind": "image", "model": "img-model-test", "prompt": "The same fox, in a kitchen", "references": []any{map[string]any{"asset_id": fox}}})
	if refs := same["references"].([]any); len(refs) != 1 || refs[0].(map[string]any)["as"] != "image_urls" {
		t.Fatalf("references: %v", same)
	}
	if err := e.srv.RunGeneration(ctx, ws, same["id"].(string)); err != nil {
		t.Fatal(err)
	}
	if g = owner.must(200, "GET", base+"/"+same["id"].(string), nil); g["status"] != "done" || fake.started["edits"] != 1 || g["request"].(map[string]any)["endpoint"] != "images/edits" {
		t.Fatalf("picture from a reference: %v, edits %d", g, fake.started["edits"])
	}
	// A reference that is gone ends the request before anything is sent.
	gone := owner.must(201, "POST", base, map[string]any{"kind": "image", "model": "img-model-test", "prompt": "Again", "references": []any{map[string]any{"asset_id": fox}}})["id"].(string)
	owner.must(200, "POST", "/v1/brands/"+brand+"/assets/"+fox+"/archive", nil)
	if err := e.srv.RunGeneration(ctx, ws, gone); err != nil {
		t.Fatal(err)
	}
	if g = owner.must(200, "GET", base+"/"+gone, nil); g["status"] != "failed" || g["error_code"] != "reference_missing" || fake.started["edits"] != 1 {
		t.Fatalf("reference gone: %v", g)
	}

	// Lists, and another brand sees none of it.
	if list := owner.must(200, "GET", base+"?status=done", nil)["data"].([]any); len(list) != 4 {
		t.Fatalf("done list: %d", len(list))
	}
	if list := owner.must(200, "GET", base+"?kind=video", nil)["data"].([]any); len(list) != 1 {
		t.Fatalf("video list: %d", len(list))
	}
	owner.must(400, "GET", base+"?kind=banner", nil)
	other := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Summitly"})["id"].(string)
	if list := owner.must(200, "GET", "/v1/brands/"+other+"/generations", nil)["data"].([]any); len(list) != 0 {
		t.Fatalf("other brand's list: %v", list)
	}
	owner.must(404, "GET", "/v1/brands/"+other+"/generations/"+id, nil)

	want := map[string]int{"generation.requested": 8, "generation.completed": 4, "generation.failed": 3}
	seen := map[string]int{}
	for _, ev := range got() {
		if _, ok := want[ev.Name]; ok {
			seen[ev.Name]++
			for k := range ev.Props {
				if k == "prompt" || k == "name" || k == "error" {
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
