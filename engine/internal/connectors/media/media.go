// Package media makes pictures, video and voice on a workspace's own
// accounts: OpenAI's image models (on the OpenAI connection the workspace
// already has for its AI model), fal.ai (many image and video models
// behind one key) and ElevenLabs (voice). Each implements
// connectors.MediaGenerator.
//
// Nothing here chooses a model: providers retire and rename models every
// few months, so the caller always names one. Request shapes follow each
// provider's own API reference, linked in its file. Base URLs can be
// pointed at a fake in tests through connectors.Client.BaseURLs.
package media

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/llm"
)

// All returns the connectors this package adds to the catalog. OpenAI is
// not one of them: its connector is the AI model one (models.Model), which
// For gives a generator for.
func All() []c.Connector { return []c.Connector{Fal{}, ElevenLabs{}} }

// For returns the generator for a connector id, or false when that
// connector makes no pictures, video or voice.
func For(connector string) (c.MediaGenerator, bool) {
	switch connector {
	case llm.OpenAI.ID:
		return OpenAI{}, true
	case FalID:
		return Fal{}, true
	case ElevenLabsID:
		return ElevenLabs{}, true
	}
	return nil, false
}

// Providers lists the connector ids that make each kind, the usual one
// first.
var Providers = map[string][]string{
	c.MediaImage: {llm.OpenAI.ID, FalID},
	c.MediaVideo: {FalID},
	c.MediaVoice: {ElevenLabsID},
}

// wait is how long one call may take. A picture or a voice-over is made
// while the request is held open, which takes up to a few minutes.
const wait = 5 * time.Minute

// mediaAnswerMax is the largest provider answer kept with a file's recipe.
const mediaAnswerMax = 64 << 10

// send runs a request and returns the status and up to limit bytes of the
// answer. Only a failure to reach the provider is an error.
func send(ctx context.Context, cl *c.Client, name string, req *http.Request, limit int64, within time.Duration) (int, http.Header, []byte, error) {
	resp, err := cl.DoWithin(req.WithContext(ctx), within)
	if err != nil {
		return 0, nil, nil, c.Errf(c.ErrUnavailable, "could not reach %s: %v", name, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return resp.StatusCode, resp.Header, nil, c.Errf(c.ErrUnavailable, "reading the %s response: %v", name, err)
	}
	if int64(len(body)) > limit {
		return resp.StatusCode, resp.Header, nil, c.Errf(c.ErrInvalid, "%s returned a file larger than %d MB", name, c.MediaMaxBytes>>20)
	}
	return resp.StatusCode, resp.Header, body, nil
}

// jsonRequest builds a request with a JSON body (nil for none).
func jsonRequest(name, method, url string, body any) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, c.Errf(c.ErrInvalid, "could not build the %s request", name)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return nil, c.Errf(c.ErrInvalid, "bad %s address: %v", name, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// short cuts a provider's message to what fits in an error.
func short(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// number reads a JSON number out of a decoded map.
func number(m map[string]any, key string) (float64, bool) {
	f, ok := m[key].(float64)
	return f, ok
}
