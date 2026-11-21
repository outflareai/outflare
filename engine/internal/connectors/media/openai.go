package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"slices"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/llm"
)

// OpenAI makes pictures with OpenAI's image models, on the API key of the
// workspace's OpenAI connection. One call makes one picture and answers
// with it, base64-encoded:
// POST /v1/images/generations
// (https://developers.openai.com/api/reference/resources/images/methods/generate).
type OpenAI struct{}

const openAIName = "OpenAI"

// openAIOptions are the settings of the image call a request may set. The
// rest (how many pictures, streaming) are fixed here.
var openAIOptions = []string{"size", "quality", "background", "output_format", "output_compression", "moderation", "complexity", "input_fidelity"}

func (OpenAI) MediaKinds() []string { return []string{c.MediaImage} }

func (OpenAI) StartMedia(ctx context.Context, cl *c.Client, creds c.Creds, q c.MediaRequest) (c.MediaRun, error) {
	if q.Kind != c.MediaImage {
		return c.MediaRun{}, c.Errf(c.ErrInvalid, "%s makes pictures here, not %s", openAIName, q.Kind)
	}
	body := map[string]any{}
	for k, v := range q.Options {
		if !slices.Contains(openAIOptions, k) {
			return c.MediaRun{}, c.Errf(c.ErrInvalid, "%s pictures have no option %q; the options are %s", openAIName, k, strings.Join(openAIOptions, ", "))
		}
		body[k] = v
	}
	body["model"], body["prompt"], body["n"] = q.Model, q.Prompt, 1
	var req *http.Request
	var err error
	sent := map[string]any{"endpoint": "images/generations", "input": body}
	if len(q.Files) > 0 {
		// Reference pictures go to the edit call, which makes a new
		// picture from them and the prompt: POST /v1/images/edits, as a
		// form (https://developers.openai.com/api/reference/resources/images/methods/edit).
		var names []string
		for _, f := range q.Files {
			if !strings.HasPrefix(f.ContentType, "image/") {
				return c.MediaRun{}, c.Errf(c.ErrInvalid, "%s takes pictures as references, not %s", openAIName, f.ContentType)
			}
			names = append(names, f.Name)
		}
		if req, err = openAIForm(cl, body, q.Files); err != nil {
			return c.MediaRun{}, err
		}
		sent = map[string]any{"endpoint": "images/edits", "input": body, "images": names}
	} else if req, err = jsonRequest(openAIName, http.MethodPost, cl.Base(llm.OpenAI.ID, llm.OpenAI.BaseURL)+"/images/generations", body); err != nil {
		return c.MediaRun{}, err
	}
	req.Header.Set("Authorization", "Bearer "+creds["api_key"])
	// Base64 is a third larger than the picture it carries.
	status, _, raw, err := send(ctx, cl, openAIName, req, c.MediaMaxBytes/3*4+(1<<20), wait)
	if err != nil {
		return c.MediaRun{}, err
	}
	if status < 200 || status > 299 {
		return c.MediaRun{}, openAIError(status, raw)
	}
	var out struct {
		Data []struct {
			B64 string `json:"b64_json"`
		} `json:"data"`
		OutputFormat string         `json:"output_format"`
		Usage        map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Data) == 0 || out.Data[0].B64 == "" {
		return c.MediaRun{}, c.Errf(c.ErrUnavailable, "%s answered without a picture", openAIName)
	}
	data, err := base64.StdEncoding.DecodeString(out.Data[0].B64)
	if err != nil || len(data) == 0 {
		return c.MediaRun{}, c.Errf(c.ErrUnavailable, "%s answered with a picture that can't be read", openAIName)
	}
	run := c.MediaRun{Done: true, Data: data, ContentType: "image/" + out.OutputFormat, Usage: map[string]float64{}}
	run.Request, _ = json.Marshal(sent)
	run.Response, _ = json.Marshal(map[string]any{"output_format": out.OutputFormat, "usage": out.Usage})
	if out.OutputFormat == "" {
		run.ContentType = "image/png"
	}
	for _, k := range []string{"input_tokens", "output_tokens", "total_tokens"} {
		if f, ok := number(out.Usage, k); ok {
			run.Usage[k] = f
		}
	}
	return run, nil
}

// openAIForm builds the edit call: the settings as form fields and each
// reference picture as an image[] file.
func openAIForm(cl *c.Client, body map[string]any, files []c.MediaFile) (*http.Request, error) {
	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	for k, v := range body {
		s, ok := v.(string)
		if !ok {
			raw, _ := json.Marshal(v)
			s = string(raw)
		}
		_ = form.WriteField(k, s)
	}
	for i, f := range files {
		name := f.Name
		if name == "" {
			name = "reference-" + strconv.Itoa(i+1)
		}
		head := textproto.MIMEHeader{}
		head.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "image[]", "filename": name}))
		head.Set("Content-Type", f.ContentType)
		part, err := form.CreatePart(head)
		if err != nil {
			return nil, c.Errf(c.ErrInvalid, "could not build the %s request", openAIName)
		}
		_, _ = part.Write(f.Data)
	}
	_ = form.Close()
	req, err := http.NewRequest(http.MethodPost, cl.Base(llm.OpenAI.ID, llm.OpenAI.BaseURL)+"/images/edits", &buf)
	if err != nil {
		return nil, c.Errf(c.ErrInvalid, "bad %s address: %v", openAIName, err)
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// FetchMedia has nothing to fetch: OpenAI answers with the picture.
func (OpenAI) FetchMedia(context.Context, *c.Client, c.Creds, c.MediaRequest, string) (c.MediaRun, error) {
	return c.MediaRun{}, c.Errf(c.ErrInvalid, "%s pictures are made in one call; there is no run to ask about", openAIName)
}

// openAIError explains a refused call. A request OpenAI's rules or the
// model refuse (400) is invalid, with OpenAI's own words; the rest follow
// the status.
func openAIError(status int, raw []byte) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	if status == http.StatusBadRequest || status == http.StatusNotFound || status == http.StatusUnprocessableEntity {
		if code, _ := e.Error.Code.(string); code == "moderation_blocked" {
			return c.Errf(c.ErrInvalid, "%s's safety rules refused this picture. Change the prompt and try again.", openAIName)
		}
		if e.Error.Message != "" {
			return c.Errf(c.ErrInvalid, "%s refused the request: %s", openAIName, short(e.Error.Message))
		}
	}
	return c.HTTPError(openAIName, status, string(raw))
}
