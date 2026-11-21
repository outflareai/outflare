package media

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// ElevenLabs reads a script out in a voice, on the workspace's own
// ElevenLabs API key. One call makes the recording and answers with it:
// POST /v1/text-to-speech/{voice_id}
// (https://elevenlabs.io/docs/api-reference/text-to-speech/convert).
type ElevenLabs struct{}

const (
	// ElevenLabsID is the connector's id.
	ElevenLabsID   = "elevenlabs"
	elevenName     = "ElevenLabs"
	elevenAPI      = "https://api.elevenlabs.io"
	elevenVoiceMax = 100 // voices listed
)

// elevenOptions are the settings of the speech call a request may set.
var elevenOptions = []string{"output_format", "language_code", "voice_settings", "seed", "previous_text", "next_text", "apply_text_normalization"}

func (ElevenLabs) Definition() c.Definition {
	return c.Definition{
		ID: ElevenLabsID, Name: elevenName, Category: c.CategoryMedia,
		Summary: "Record voice-overs with your own ElevenLabs account, in the voices it holds.",
		Description: "Sends a script to the ElevenLabs voice and model you name and keeps the recording with the brand's files. " +
			"Connecting reads your plan and how many characters are left; nothing is recorded or spent. " +
			"Every recording is billed to your own ElevenLabs plan by the character. Your key is encrypted and only ever sent to ElevenLabs.",
		Website: "https://elevenlabs.io", DocsURL: "https://elevenlabs.io/docs", KeyURL: "https://elevenlabs.io/app/settings/api-keys",
		Auth: c.AuthAPIKey,
		Fields: []c.Field{{Name: "api_key", Label: "API key", Secret: true,
			Help: "ElevenLabs → Settings → API keys. Give it access to Text to Speech, Speech to Text (for word timings in talking videos) and Voices (read), and User (read) so Outflare can show what is left."}},
		Capabilities: []string{c.CapMediaVoice, c.CapTranscribe},
		Pricing:      "Per character on your ElevenLabs plan, by model",
	}
}

func (ElevenLabs) request(cl *c.Client, creds c.Creds, method, path string, body any) (*http.Request, error) {
	req, err := jsonRequest(elevenName, method, cl.Base(ElevenLabsID, elevenAPI)+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", creds["api_key"])
	return req, nil
}

// Test reads the account's plan, which spends nothing. A key that is
// accepted but may not read the plan still connects.
func (e ElevenLabs) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	if strings.TrimSpace(creds["api_key"]) == "" {
		return c.Account{}, c.Errf(c.ErrInvalid, "%s needs an API key", elevenName)
	}
	req, err := e.request(cl, creds, http.MethodGet, "/v1/user/subscription", nil)
	if err != nil {
		return c.Account{}, err
	}
	status, _, raw, err := send(ctx, cl, elevenName, req, 1<<20, 0)
	if err != nil {
		return c.Account{}, err
	}
	if status < 200 || status > 299 {
		if code, _ := elevenDetail(raw); code == "missing_permissions" {
			return c.Account{Name: elevenName}, nil
		}
		return c.Account{}, c.HTTPError(elevenName, status, string(raw))
	}
	var sub struct {
		Tier  string  `json:"tier"`
		Used  float64 `json:"character_count"`
		Limit float64 `json:"character_limit"`
	}
	_ = json.Unmarshal(raw, &sub)
	acct := c.Account{Name: elevenName, Details: map[string]string{}}
	if sub.Tier != "" {
		acct.Details["plan"] = sub.Tier
	}
	if sub.Limit > 0 {
		// Characters left in this billing period.
		left := max(sub.Limit-sub.Used, 0)
		acct.Credits = &left
	}
	return acct, nil
}

func (ElevenLabs) MediaKinds() []string { return []string{c.MediaVoice} }

func (e ElevenLabs) StartMedia(ctx context.Context, cl *c.Client, creds c.Creds, q c.MediaRequest) (c.MediaRun, error) {
	if q.Kind != c.MediaVoice {
		return c.MediaRun{}, c.Errf(c.ErrInvalid, "%s makes voice here, not %s", elevenName, q.Kind)
	}
	if q.Voice == "" {
		return c.MediaRun{}, c.Errf(c.ErrInvalid, "name the %s voice that reads the script", elevenName)
	}
	if len(q.Files) > 0 {
		return c.MediaRun{}, c.Errf(c.ErrInvalid, "an %s voice-over takes no reference files: the voice is the reference", elevenName)
	}
	body := map[string]any{"text": q.Prompt, "model_id": q.Model}
	query := url.Values{}
	for k, v := range q.Options {
		if !slices.Contains(elevenOptions, k) {
			return c.MediaRun{}, c.Errf(c.ErrInvalid, "%s voice has no option %q; the options are %s", elevenName, k, strings.Join(elevenOptions, ", "))
		}
		if k == "output_format" {
			s, _ := v.(string)
			// The file is kept as MP3: the other formats are raw or
			// telephone audio a browser can't play.
			if !strings.HasPrefix(s, "mp3_") {
				return c.MediaRun{}, c.Errf(c.ErrInvalid, "output_format is one of %s's MP3 formats, such as mp3_44100_128", elevenName)
			}
			query.Set(k, s)
			continue
		}
		body[k] = v
	}
	path := "/v1/text-to-speech/" + url.PathEscape(q.Voice)
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	req, err := e.request(cl, creds, http.MethodPost, path, body)
	if err != nil {
		return c.MediaRun{}, err
	}
	req.Header.Set("Accept", "audio/mpeg")
	status, head, raw, err := send(ctx, cl, elevenName, req, c.MediaMaxBytes, wait)
	if err != nil {
		return c.MediaRun{}, err
	}
	if status < 200 || status > 299 {
		return c.MediaRun{}, elevenError(status, raw)
	}
	if len(raw) == 0 {
		return c.MediaRun{}, c.Errf(c.ErrUnavailable, "%s answered without a recording", elevenName)
	}
	sent, _ := json.Marshal(map[string]any{"endpoint": "text-to-speech", "voice_id": q.Voice, "output_format": query.Get("output_format"), "input": body})
	answer, _ := json.Marshal(map[string]any{"request_id": head.Get("request-id"), "character_cost": head.Get("character-cost")})
	return c.MediaRun{Done: true, Data: raw, ContentType: head.Get("Content-Type"), Request: sent, Response: answer,
		Usage: map[string]float64{"characters": float64(utf8.RuneCountInString(q.Prompt))}}, nil
}

// FetchMedia has nothing to fetch: ElevenLabs answers with the recording.
func (ElevenLabs) FetchMedia(context.Context, *c.Client, c.Creds, c.MediaRequest, string) (c.MediaRun, error) {
	return c.MediaRun{}, c.Errf(c.ErrInvalid, "%s recordings are made in one call; there is no run to ask about", elevenName)
}

// Transcribe writes a recording out word by word, with when each word is
// said: POST /v1/speech-to-text, as a form
// (https://elevenlabs.io/docs/api-reference/speech-to-text/convert).
// Billed to the account by the length of the recording.
func (e ElevenLabs) Transcribe(ctx context.Context, cl *c.Client, creds c.Creds, model string, file c.MediaFile) (c.Transcript, error) {
	model = strings.TrimSpace(model)
	if model == "" || len(model) > c.MediaModelMax {
		return c.Transcript{}, c.Errf(c.ErrInvalid, "name the %s speech to text model", elevenName)
	}
	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	_ = form.WriteField("model_id", model)
	_ = form.WriteField("timestamps_granularity", "word")
	_ = form.WriteField("tag_audio_events", "false")
	name := file.Name
	if name == "" {
		name = "recording"
	}
	part, err := form.CreateFormFile("file", name)
	if err != nil {
		return c.Transcript{}, c.Errf(c.ErrInvalid, "could not build the %s request", elevenName)
	}
	_, _ = part.Write(file.Data)
	_ = form.Close()
	req, err := http.NewRequest(http.MethodPost, cl.Base(ElevenLabsID, elevenAPI)+"/v1/speech-to-text", &buf)
	if err != nil {
		return c.Transcript{}, c.Errf(c.ErrInvalid, "bad %s address: %v", elevenName, err)
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("xi-api-key", creds["api_key"])
	status, _, raw, err := send(ctx, cl, elevenName, req, 8<<20, wait)
	if err != nil {
		return c.Transcript{}, err
	}
	if status < 200 || status > 299 {
		if code, _ := elevenDetail(raw); code == "missing_permissions" {
			return c.Transcript{}, c.Errf(c.ErrAuth, "the %s API key may not turn speech into text. Give it access to Speech to Text, or create a new key.", elevenName)
		}
		return c.Transcript{}, elevenError(status, raw)
	}
	var out struct {
		Language string  `json:"language_code"`
		Text     string  `json:"text"`
		Seconds  float64 `json:"audio_duration_secs"`
		Words    []struct {
			Text  string  `json:"text"`
			Start float64 `json:"start"`
			End   float64 `json:"end"`
			Type  string  `json:"type"`
		} `json:"words"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return c.Transcript{}, c.Errf(c.ErrUnavailable, "%s returned something that is not JSON", elevenName)
	}
	t := c.Transcript{Text: out.Text, Language: out.Language, Seconds: out.Seconds, Words: []c.SpokenWord{}}
	for _, w := range out.Words {
		if w.Type != "word" || strings.TrimSpace(w.Text) == "" || w.End < w.Start {
			continue
		}
		t.Words = append(t.Words, c.SpokenWord{Text: strings.TrimSpace(w.Text), Start: w.Start, End: w.End})
		t.Seconds = max(t.Seconds, w.End)
	}
	if len(t.Words) == 0 {
		return t, c.Errf(c.ErrInvalid, "%s heard no words in the recording", elevenName)
	}
	return t, nil
}

// Voice is one voice an ElevenLabs account can use.
type Voice struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
}

// Voices lists the account's voices, up to a hundred, by name
// (GET /v2/voices).
func (e ElevenLabs) Voices(ctx context.Context, cl *c.Client, creds c.Creds) ([]Voice, error) {
	req, err := e.request(cl, creds, http.MethodGet, "/v2/voices?page_size="+strconv.Itoa(elevenVoiceMax)+"&sort=name&sort_direction=asc", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Voices []struct {
			ID       string `json:"voice_id"`
			Name     string `json:"name"`
			Category string `json:"category"`
		} `json:"voices"`
	}
	if _, err := c.DoJSON(ctx, cl, elevenName, req, &out); err != nil {
		return nil, err
	}
	list := make([]Voice, 0, len(out.Voices))
	for _, v := range out.Voices {
		if v.ID != "" {
			list = append(list, Voice{ID: v.ID, Name: short(v.Name), Category: v.Category})
		}
	}
	return list, nil
}

// elevenDetail reads ElevenLabs's error body: detail is words, a
// {status, message} pair, or a list of {msg} for a refused field.
func elevenDetail(raw []byte) (code, message string) {
	var e struct {
		Detail any `json:"detail"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return "", ""
	}
	switch d := e.Detail.(type) {
	case string:
		return "", d
	case map[string]any:
		code, _ = d["status"].(string)
		message, _ = d["message"].(string)
	case []any:
		var msgs []string
		for _, item := range d {
			if m, ok := item.(map[string]any); ok {
				if msg, ok := m["msg"].(string); ok && msg != "" {
					msgs = append(msgs, msg)
				}
			}
		}
		message = strings.Join(msgs, "; ")
	}
	return code, message
}

// elevenError explains a refused call.
func elevenError(status int, raw []byte) error {
	code, message := elevenDetail(raw)
	switch {
	case code == "quota_exceeded":
		return c.Errf(c.ErrQuota, "the %s plan has too few characters left for this script. Top it up or shorten the script.", elevenName)
	case code == "missing_permissions":
		return c.Errf(c.ErrAuth, "the %s API key may not make speech. Give it access to Text to Speech, or create a new key.", elevenName)
	case status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusTooManyRequests || status >= 500:
		return c.HTTPError(elevenName, status, string(raw))
	case message != "":
		return c.Errf(c.ErrInvalid, "%s refused the request: %s", elevenName, short(message))
	}
	return c.HTTPError(elevenName, status, string(raw))
}
