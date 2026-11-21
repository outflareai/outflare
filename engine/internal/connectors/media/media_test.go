package media

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const testKey = "test-not-a-real-key"

var creds = c.Creds{"api_key": testKey}

// fake stands in for all three providers on one server: it records each
// call with its body and answers with what the test set.
type fake struct {
	mu     sync.Mutex
	calls  []string
	bodies []map[string]any
	answer func(w http.ResponseWriter, r *http.Request, n int)
}

func newFake(t *testing.T) (*fake, *c.Client) {
	t.Helper()
	f := &fake{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.calls = append(f.calls, r.Method+" "+r.URL.RequestURI())
		f.bodies = append(f.bodies, body)
		if strings.HasPrefix(r.URL.Path, "/files/") {
			if r.Header.Get("Authorization") != "" || r.Header.Get("xi-api-key") != "" {
				t.Error("the API key went to the file host")
			}
		} else if r.Header.Get("Authorization") != "Bearer "+testKey && r.Header.Get("Authorization") != "Key "+testKey && r.Header.Get("xi-api-key") != testKey {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"detail":"Invalid credentials"}`)
			return
		}
		f.answer(w, r, len(f.calls))
	}))
	t.Cleanup(srv.Close)
	return f, &c.Client{HTTP: srv.Client(), BaseURLs: map[string]string{"openai": srv.URL + "/v1", FalID: srv.URL, FalID + ".files": srv.URL, ElevenLabsID: srv.URL}}
}

func kind(err error) string {
	if err == nil {
		return ""
	}
	return c.KindOf(err)
}

func TestOpenAIPicture(t *testing.T) {
	f, cl := newFake(t)
	picture := []byte("\x89PNG not really a picture")
	f.answer = func(w http.ResponseWriter, r *http.Request, _ int) {
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"b64_json": base64.StdEncoding.EncodeToString(picture)}},
			"output_format": "webp", "usage": map[string]any{"input_tokens": 12, "output_tokens": 4000, "total_tokens": 4012}})
	}
	q, err := c.MediaRequest{Kind: "image", Model: " img-model-test ", Prompt: " A red fox ", Options: map[string]any{"size": "1024x1536", "quality": "low"}}.Normal()
	if err != nil {
		t.Fatal(err)
	}
	run, err := OpenAI{}.StartMedia(context.Background(), cl, creds, q)
	if err != nil {
		t.Fatal(err)
	}
	if !run.Done || string(run.Data) != string(picture) || run.ContentType != "image/webp" || run.Usage["output_tokens"] != 4000 {
		t.Fatalf("run: %+v", run)
	}
	if f.calls[0] != "POST /v1/images/generations" {
		t.Fatalf("calls: %v", f.calls)
	}
	if b := f.bodies[0]; b["model"] != "img-model-test" || b["prompt"] != "A red fox" || b["n"] != float64(1) || b["size"] != "1024x1536" || b["quality"] != "low" {
		t.Fatalf("body: %v", b)
	}

	// An option the call doesn't have is refused before anything is sent.
	q.Options = map[string]any{"n": 4}
	if _, err := (OpenAI{}).StartMedia(context.Background(), cl, creds, q); kind(err) != c.ErrInvalid || len(f.calls) != 1 {
		t.Fatalf("unknown option: %v, calls %v", err, f.calls)
	}
	q.Options = nil

	// What OpenAI refuses comes back by kind, in words.
	f.answer = func(w http.ResponseWriter, r *http.Request, _ int) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"Your request was rejected by the safety system.","type":"image_generation_user_error","code":"moderation_blocked"}}`)
	}
	if _, err := (OpenAI{}).StartMedia(context.Background(), cl, creds, q); kind(err) != c.ErrInvalid || !strings.Contains(err.Error(), "safety rules") {
		t.Fatalf("moderation: %v", err)
	}
	f.answer = func(w http.ResponseWriter, r *http.Request, _ int) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"You exceeded your current quota","code":"insufficient_quota"}}`)
	}
	if _, err := (OpenAI{}).StartMedia(context.Background(), cl, creds, q); kind(err) != c.ErrQuota {
		t.Fatalf("quota: %v", err)
	}
	if _, err := (OpenAI{}).StartMedia(context.Background(), cl, c.Creds{"api_key": "wrong"}, q); kind(err) != c.ErrAuth {
		t.Fatalf("wrong key: %v", err)
	}
}

func TestFalVideo(t *testing.T) {
	f, cl := newFake(t)
	const run = "019a1b2c-3d4e-7f80-9a1b-2c3d4e5f6a7b"
	video := []byte("....ftypisom not really a video")
	done := false
	f.answer = func(w http.ResponseWriter, r *http.Request, _ int) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/acme/render/v2/text-to-video":
			json.NewEncoder(w).Encode(map[string]any{"status": "IN_QUEUE", "request_id": run})
		case r.URL.Path == "/acme/render/requests/"+run+"/status" && !done:
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]any{"status": "IN_PROGRESS", "request_id": run})
		case r.URL.Path == "/acme/render/requests/"+run+"/status":
			json.NewEncoder(w).Encode(map[string]any{"status": "COMPLETED", "request_id": run})
		case r.URL.Path == "/acme/render/requests/"+run:
			w.Header().Set("X-Fal-Billable-Units", "5")
			json.NewEncoder(w).Encode(map[string]any{"seed": 7, "video": map[string]any{"url": "https://v3.fal.media/files/a/out.mp4", "content_type": "video/mp4"}})
		case r.URL.Path == "/files/a/out.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			w.Write(video)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"status":"NOT_FOUND"}`)
		}
	}
	ctx := context.Background()
	q, err := c.MediaRequest{Kind: "video", Model: "acme/render/v2/text-to-video", Prompt: "A fox runs", Options: map[string]any{"duration": 5, "prompt": "ignored"}}.Normal()
	if err != nil {
		t.Fatal(err)
	}
	started, err := Fal{}.StartMedia(ctx, cl, creds, q)
	if err != nil || started.Done || started.RunID != run {
		t.Fatalf("start: %+v %v", started, err)
	}
	if b := f.bodies[0]; b["prompt"] != "A fox runs" || b["duration"] != float64(5) {
		t.Fatalf("body: %v", b)
	}
	// Still rendering: nothing yet, and nothing started again.
	if got, err := (Fal{}).FetchMedia(ctx, cl, creds, q, run); err != nil || got.Done || got.Failure != "" {
		t.Fatalf("while rendering: %+v %v", got, err)
	}
	done = true
	got, err := Fal{}.FetchMedia(ctx, cl, creds, q, run)
	if err != nil || !got.Done || string(got.Data) != string(video) || got.ContentType != "video/mp4" || got.Usage["billable_units"] != 5 {
		t.Fatalf("done: %+v %v", got, err)
	}
	for _, call := range f.calls[1:] {
		if strings.HasPrefix(call, "POST") {
			t.Fatalf("the render was started again: %v", f.calls)
		}
	}

	// A run that failed says why, once and for all.
	f.answer = func(w http.ResponseWriter, r *http.Request, _ int) {
		if strings.HasSuffix(r.URL.Path, "/status") {
			json.NewEncoder(w).Encode(map[string]any{"status": "COMPLETED"})
			return
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
		io.WriteString(w, `{"detail":[{"loc":["body","duration"],"msg":"duration must be 5 or 10","type":"value_error"}]}`)
	}
	if got, err := (Fal{}).FetchMedia(ctx, cl, creds, q, run); err != nil || got.Done || !strings.Contains(got.Failure, "duration must be 5 or 10") {
		t.Fatalf("failed run: %+v %v", got, err)
	}
	// A file anywhere but fal's own file host is never fetched.
	f.answer = func(w http.ResponseWriter, r *http.Request, _ int) {
		if strings.HasSuffix(r.URL.Path, "/status") {
			json.NewEncoder(w).Encode(map[string]any{"status": "COMPLETED"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"video": map[string]any{"url": "https://files.example/a.mp4"}})
	}
	before := len(f.calls)
	if _, err := (Fal{}).FetchMedia(ctx, cl, creds, q, run); kind(err) != c.ErrUnavailable || len(f.calls) != before+2 {
		t.Fatalf("foreign file: %v, calls %v", err, f.calls[before:])
	}
	// A model id that isn't an endpoint, and a wrong key.
	q.Model = "../admin"
	if _, err := (Fal{}).StartMedia(ctx, cl, creds, q); kind(err) != c.ErrInvalid {
		t.Fatalf("bad model: %v", err)
	}
	q.Model = "acme/render"
	if _, err := (Fal{}).StartMedia(ctx, cl, c.Creds{"api_key": "wrong"}, q); kind(err) != c.ErrAuth {
		t.Fatalf("wrong key: %v", err)
	}
}

func TestFalKeyCheck(t *testing.T) {
	f, cl := newFake(t)
	f.answer = func(w http.ResponseWriter, r *http.Request, _ int) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"status":"NOT_FOUND"}`)
	}
	if acct, err := (Fal{}).Test(context.Background(), cl, creds); err != nil || acct.Name != "fal.ai" {
		t.Fatalf("good key: %+v %v", acct, err)
	}
	if len(f.calls) != 1 || !strings.HasPrefix(f.calls[0], "GET ") {
		t.Fatalf("the key check must only read: %v", f.calls)
	}
	if _, err := (Fal{}).Test(context.Background(), cl, c.Creds{"api_key": "wrong"}); kind(err) != c.ErrAuth {
		t.Fatalf("wrong key: %v", err)
	}
}

func TestElevenLabsVoice(t *testing.T) {
	f, cl := newFake(t)
	audio := []byte("ID3 not really a recording")
	f.answer = func(w http.ResponseWriter, r *http.Request, _ int) {
		switch r.URL.Path {
		case "/v1/user/subscription":
			json.NewEncoder(w).Encode(map[string]any{"tier": "creator", "character_count": 1500, "character_limit": 100000})
		case "/v2/voices":
			json.NewEncoder(w).Encode(map[string]any{"voices": []map[string]any{{"voice_id": "voice_test_1", "name": "Ana", "category": "cloned"}}})
		default:
			w.Header().Set("Content-Type", "audio/mpeg")
			w.Write(audio)
		}
	}
	ctx := context.Background()
	acct, err := ElevenLabs{}.Test(ctx, cl, creds)
	if err != nil || acct.Details["plan"] != "creator" || acct.Credits == nil || *acct.Credits != 98500 {
		t.Fatalf("account: %+v %v", acct, err)
	}
	voices, err := ElevenLabs{}.Voices(ctx, cl, creds)
	if err != nil || len(voices) != 1 || voices[0].ID != "voice_test_1" {
		t.Fatalf("voices: %v %v", voices, err)
	}
	q, err := c.MediaRequest{Kind: "voice", Model: "voice-model-test", Voice: "voice_test_1", Prompt: "Hello there.",
		Options: map[string]any{"output_format": "mp3_44100_128", "voice_settings": map[string]any{"speed": 1.1}}}.Normal()
	if err != nil {
		t.Fatal(err)
	}
	run, err := ElevenLabs{}.StartMedia(ctx, cl, creds, q)
	if err != nil || !run.Done || string(run.Data) != string(audio) || run.Usage["characters"] != 12 {
		t.Fatalf("run: %+v %v", run, err)
	}
	last := len(f.calls) - 1
	if f.calls[last] != "POST /v1/text-to-speech/voice_test_1?output_format=mp3_44100_128" {
		t.Fatalf("call: %v", f.calls[last])
	}
	if b := f.bodies[last]; b["text"] != "Hello there." || b["model_id"] != "voice-model-test" || b["voice_settings"] == nil || b["output_format"] != nil {
		t.Fatalf("body: %v", b)
	}

	// A voice needs a voice; raw audio formats are refused.
	if _, err := (c.MediaRequest{Kind: "voice", Model: "m", Prompt: "Hi"}).Normal(); kind(err) != c.ErrInvalid {
		t.Fatalf("no voice: %v", err)
	}
	q.Options = map[string]any{"output_format": "pcm_16000"}
	if _, err := (ElevenLabs{}).StartMedia(ctx, cl, creds, q); kind(err) != c.ErrInvalid {
		t.Fatalf("raw format: %v", err)
	}
	q.Options = nil
	f.answer = func(w http.ResponseWriter, r *http.Request, _ int) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"detail":{"status":"quota_exceeded","message":"This request exceeds your quota."}}`)
	}
	if _, err := (ElevenLabs{}).StartMedia(ctx, cl, creds, q); kind(err) != c.ErrQuota {
		t.Fatalf("quota: %v", err)
	}
	// A key that is accepted but may not read the plan still connects.
	f.answer = func(w http.ResponseWriter, r *http.Request, _ int) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"detail":{"status":"missing_permissions","message":"The API key is missing the permission user_read."}}`)
	}
	if _, err := (ElevenLabs{}).Test(ctx, cl, creds); err != nil {
		t.Fatalf("restricted key: %v", err)
	}
	if _, err := (ElevenLabs{}).Test(ctx, cl, c.Creds{"api_key": "wrong"}); kind(err) != c.ErrAuth {
		t.Fatalf("wrong key: %v", err)
	}
}

func TestProviders(t *testing.T) {
	for kind, ids := range Providers {
		for _, id := range ids {
			g, ok := For(id)
			if !ok {
				t.Fatalf("%s has no generator", id)
			}
			found := false
			for _, k := range g.MediaKinds() {
				found = found || k == kind
			}
			if !found {
				t.Fatalf("%s is listed for %s but doesn't make it", id, kind)
			}
		}
	}
	if _, ok := For("claude"); ok {
		t.Fatal("claude makes no media")
	}
}
