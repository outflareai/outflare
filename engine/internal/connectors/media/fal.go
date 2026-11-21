package media

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Fal is fal.ai: many image, video and voice models behind one API key,
// each at its own endpoint (owner/app/path). A request goes on fal's
// queue and is asked about until it is done, so a video that renders for
// minutes is fetched, never started (and paid for) twice.
//
// The queue API (https://fal.ai/docs/api-reference/inference-api):
// POST queue.fal.run/{owner}/{app}/{path} answers with a request_id;
// GET .../{owner}/{app}/requests/{id}/status answers 202 while it runs and
// 200 once it ended (a failed run is COMPLETED too);
// GET .../{owner}/{app}/requests/{id} is the model's answer, or its error.
// The files in an answer are links on fal's file host.
type Fal struct{}

const (
	// FalID is the connector's id.
	FalID    = "fal"
	falName  = "fal.ai"
	falQueue = "https://queue.fal.run"
	// falFiles is fal's file host: an answer's files are fetched only from
	// it (or a host under it), and without the API key.
	falFiles = "fal.media"
	// falREST is where a reference file is handed to fal before a request
	// names it: POST /storage/upload/initiate answers with a link to PUT
	// the file to and the link models read it from (fal's own client
	// libraries upload the same way).
	falREST = "https://rest.alpha.fal.ai"
	// falKeep is how long fal keeps a reference file: a day, long enough
	// for the request that uses it.
	falKeep = 86400
	// falNoRun is a well-formed request id no account has.
	falNoRun = "00000000-0000-0000-0000-000000000000"
)

var (
	falModelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(/[A-Za-z0-9][A-Za-z0-9._-]*){1,7}$`)
	falRunRe   = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)
)

func (Fal) Definition() c.Definition {
	return c.Definition{
		ID: FalID, Name: falName, Category: c.CategoryMedia,
		Summary: "Make video and pictures with your own fal.ai account: one key reaches many models, such as Seedance, Kling and Veo.",
		Description: "Sends a prompt and the model's settings to the fal.ai model you name, waits on fal's queue while it renders and keeps the file with the brand's files. " +
			"A render that takes minutes is asked about, never started twice. Every request is billed to your own fal.ai balance at that model's price. " +
			"Your key is encrypted and only ever sent to fal.ai.",
		Website: "https://fal.ai", DocsURL: "https://fal.ai/docs", KeyURL: "https://fal.ai/dashboard/keys",
		Auth: c.AuthAPIKey,
		Fields: []c.Field{{Name: "api_key", Label: "API key", Secret: true,
			Help: "fal.ai dashboard → Keys → Add key, with the API preset. A key made only for Outflare is safest."}},
		Capabilities: []string{c.CapMediaVideo, c.CapMediaImage},
		Pricing:      "Per picture or per second of video on your fal.ai balance, by model",
	}
}

func (Fal) request(cl *c.Client, creds c.Creds, method, path string, body any) (*http.Request, error) {
	req, err := jsonRequest(falName, method, cl.Base(FalID, falQueue)+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Key "+creds["api_key"])
	return req, nil
}

// Test asks about a request that doesn't exist, which spends nothing: fal
// answers 401 or 403 to a key it doesn't accept and 404 to one it does.
func (f Fal) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	if strings.TrimSpace(creds["api_key"]) == "" {
		return c.Account{}, c.Errf(c.ErrInvalid, "%s needs an API key", falName)
	}
	req, err := f.request(cl, creds, http.MethodGet, "/fal-ai/flux/requests/"+falNoRun+"/status", nil)
	if err != nil {
		return c.Account{}, err
	}
	if status, err := c.DoJSON(ctx, cl, falName, req, nil); err != nil && status != http.StatusNotFound {
		return c.Account{}, err
	}
	return c.Account{Name: falName}, nil
}

func (Fal) MediaKinds() []string { return []string{c.MediaVideo, c.MediaImage} }

// falApp is the owner/app part of a model id: where its requests live.
func falApp(model string) string {
	parts := strings.SplitN(model, "/", 3)
	return parts[0] + "/" + parts[1]
}

func (f Fal) StartMedia(ctx context.Context, cl *c.Client, creds c.Creds, q c.MediaRequest) (c.MediaRun, error) {
	if q.Kind != c.MediaVideo && q.Kind != c.MediaImage {
		return c.MediaRun{}, c.Errf(c.ErrInvalid, "%s makes video and pictures here, not %s", falName, q.Kind)
	}
	if !falModelRe.MatchString(q.Model) {
		return c.MediaRun{}, c.Errf(c.ErrInvalid, "a %s model is its endpoint, written owner/app or owner/app/path as on the model's page", falName)
	}
	// The model's own settings, with the prompt on top.
	body := map[string]any{}
	for k, v := range q.Options {
		body[k] = v
	}
	if q.Prompt != "" {
		body["prompt"] = q.Prompt
	}
	for _, file := range q.Files {
		link, err := f.upload(ctx, cl, creds, file)
		if err != nil {
			return c.MediaRun{}, err
		}
		if !strings.HasSuffix(file.As, "s") {
			body[file.As] = link
			continue
		}
		list, _ := body[file.As].([]any)
		body[file.As] = append(list, link)
	}
	req, err := f.request(cl, creds, http.MethodPost, "/"+q.Model, body)
	if err != nil {
		return c.MediaRun{}, err
	}
	status, _, raw, err := send(ctx, cl, falName, req, 1<<20, 0)
	if err != nil {
		return c.MediaRun{}, err
	}
	if status < 200 || status > 299 {
		return c.MediaRun{}, falError(status, raw)
	}
	sent, _ := json.Marshal(map[string]any{"endpoint": q.Model, "input": body})
	var out struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || !falRunRe.MatchString(out.RequestID) {
		return c.MediaRun{}, c.Errf(c.ErrUnavailable, "%s took the request without saying which one it is", falName)
	}
	return c.MediaRun{RunID: out.RequestID, Request: sent}, nil
}

// upload hands a reference file to fal and returns the link a model reads
// it from. The key goes to fal's API only, never to the link the file is
// put to.
func (f Fal) upload(ctx context.Context, cl *c.Client, creds c.Creds, file c.MediaFile) (string, error) {
	name := file.Name
	if name == "" {
		name = "reference"
	}
	req, err := jsonRequest(falName, http.MethodPost, cl.Base(FalID+".rest", falREST)+"/storage/upload/initiate?storage_type=fal-cdn-v3",
		map[string]any{"file_name": name, "content_type": file.ContentType})
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Key "+creds["api_key"])
	req.Header.Set("X-Fal-Object-Lifecycle", `{"expiration_duration_seconds":`+strconv.Itoa(falKeep)+`}`)
	status, _, raw, err := send(ctx, cl, falName, req, 1<<20, 0)
	if err != nil {
		return "", err
	}
	if status < 200 || status > 299 {
		return "", falError(status, raw)
	}
	var out struct {
		FileURL   string `json:"file_url"`
		UploadURL string `json:"upload_url"`
	}
	_ = json.Unmarshal(raw, &out)
	up, err := url.Parse(out.UploadURL)
	if err != nil || up.Scheme != "https" || up.User != nil || !strings.HasPrefix(out.FileURL, "https://") {
		return "", c.Errf(c.ErrUnavailable, "%s did not say where to put the reference file", falName)
	}
	target := out.UploadURL
	// Tests point the upload at a fake.
	if base := cl.Base(FalID+".files", ""); base != "" {
		target = base + up.RequestURI()
	}
	put, err := http.NewRequest(http.MethodPut, target, bytes.NewReader(file.Data))
	if err != nil {
		return "", c.Errf(c.ErrUnavailable, "%s did not say where to put the reference file", falName)
	}
	put.Header.Set("Content-Type", file.ContentType)
	if status, _, _, err = send(ctx, cl, falName, put, 1<<20, wait); err != nil {
		return "", err
	}
	if status < 200 || status > 299 {
		return "", c.Errf(c.ErrUnavailable, "the reference file could not be handed to %s (HTTP %d)", falName, status)
	}
	return out.FileURL, nil
}

func (f Fal) FetchMedia(ctx context.Context, cl *c.Client, creds c.Creds, q c.MediaRequest, runID string) (c.MediaRun, error) {
	if !falModelRe.MatchString(q.Model) || !falRunRe.MatchString(runID) {
		return c.MediaRun{}, c.Errf(c.ErrInvalid, "not a %s request", falName)
	}
	base := "/" + falApp(q.Model) + "/requests/" + runID
	req, err := f.request(cl, creds, http.MethodGet, base+"/status", nil)
	if err != nil {
		return c.MediaRun{}, err
	}
	status, _, raw, err := send(ctx, cl, falName, req, 1<<20, 0)
	if err != nil {
		return c.MediaRun{}, err
	}
	switch {
	case status == http.StatusAccepted:
		return c.MediaRun{RunID: runID}, nil
	case status == http.StatusNotFound:
		return c.MediaRun{RunID: runID, Failure: falName + " no longer has this request: it keeps a result for a short time only. Start it again."}, nil
	case status != http.StatusOK:
		return c.MediaRun{}, falError(status, raw)
	}
	var st struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &st); err != nil || st.Status != "COMPLETED" {
		return c.MediaRun{RunID: runID}, nil
	}

	// It ended. The result is the model's answer, or why it failed.
	if req, err = f.request(cl, creds, http.MethodGet, base, nil); err != nil {
		return c.MediaRun{}, err
	}
	status, head, raw, err := send(ctx, cl, falName, req, 8<<20, 0)
	if err != nil {
		return c.MediaRun{}, err
	}
	switch {
	case status == http.StatusBadRequest && strings.Contains(string(raw), "still in progress"):
		return c.MediaRun{RunID: runID}, nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusTooManyRequests:
		return c.MediaRun{}, falError(status, raw)
	case status < 200 || status > 299:
		// The run itself failed: fal has already retried what it could.
		return c.MediaRun{RunID: runID, Failure: falFailure(status, raw)}, nil
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return c.MediaRun{}, c.Errf(c.ErrUnavailable, "%s returned something that is not JSON", falName)
	}
	link := falFile(out, q.Kind)
	if link == "" {
		return c.MediaRun{RunID: runID, Failure: "the " + falName + " model finished without a file. Check that it is a model that makes a " + q.Kind + "."}, nil
	}
	data, ct, err := f.download(ctx, cl, link)
	if err != nil {
		return c.MediaRun{}, err
	}
	run := c.MediaRun{RunID: runID, Done: true, Data: data, ContentType: ct, Usage: map[string]float64{}}
	if len(raw) <= mediaAnswerMax {
		run.Response = raw
	}
	if units, err := strconv.ParseFloat(head.Get("X-Fal-Billable-Units"), 64); err == nil {
		run.Usage["billable_units"] = units
	}
	return run, nil
}

// falKeys are where models put the file of each kind, likeliest first.
var falKeys = map[string][]string{
	c.MediaVideo: {"video", "videos"},
	c.MediaImage: {"images", "image"},
}

// falFile finds the file's link in a model's answer: under the usual key
// for the kind, else the first file anywhere in it.
func falFile(out any, kind string) string {
	if m, ok := out.(map[string]any); ok {
		for _, k := range falKeys[kind] {
			if u := falLink(m[k], 0); u != "" {
				return u
			}
		}
	}
	return falLink(out, 0)
}

// falLink is the first {"url": "https://..."} in v.
func falLink(v any, depth int) string {
	if depth > 6 {
		return ""
	}
	switch t := v.(type) {
	case map[string]any:
		if u, ok := t["url"].(string); ok && strings.HasPrefix(u, "https://") {
			return u
		}
		for _, k := range sortedKeys(t) {
			if u := falLink(t[k], depth+1); u != "" {
				return u
			}
		}
	case []any:
		for _, e := range t {
			if u := falLink(e, depth+1); u != "" {
				return u
			}
		}
	}
	return ""
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Small maps: insertion sort keeps the walk the same every time.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// download fetches a finished file from fal's file host. The link came
// from a model's answer, so it is followed only to that host, and the API
// key never goes with it.
func (Fal) download(ctx context.Context, cl *c.Client, link string) ([]byte, string, error) {
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return nil, "", c.Errf(c.ErrUnavailable, "%s returned a file link that can't be followed", falName)
	}
	host := strings.ToLower(u.Hostname())
	if host != falFiles && !strings.HasSuffix(host, "."+falFiles) {
		return nil, "", c.Errf(c.ErrUnavailable, "%s returned a file outside its own file host", falName)
	}
	// Tests point the file host at a fake.
	if base := cl.Base(FalID+".files", ""); base != "" {
		link = base + u.RequestURI()
	}
	req, err := http.NewRequest(http.MethodGet, link, nil)
	if err != nil {
		return nil, "", c.Errf(c.ErrUnavailable, "%s returned a file link that can't be followed", falName)
	}
	status, head, data, err := send(ctx, cl, falName, req, c.MediaMaxBytes, wait)
	if err != nil {
		return nil, "", err
	}
	if status != http.StatusOK || len(data) == 0 {
		return nil, "", c.Errf(c.ErrUnavailable, "the file %s made could not be fetched (HTTP %d)", falName, status)
	}
	return data, head.Get("Content-Type"), nil
}

// falDetail reads the words out of fal's error bodies: detail is a string,
// or a list of {msg} for a model that refused its input.
func falDetail(raw []byte) string {
	var e struct {
		Detail any `json:"detail"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return ""
	}
	switch d := e.Detail.(type) {
	case string:
		return d
	case []any:
		var msgs []string
		for _, item := range d {
			if m, ok := item.(map[string]any); ok {
				if msg, ok := m["msg"].(string); ok && msg != "" {
					msgs = append(msgs, msg)
				}
			}
		}
		return strings.Join(msgs, "; ")
	}
	return ""
}

// falError explains a refused call.
func falError(status int, raw []byte) error {
	detail := falDetail(raw)
	if detail != "" && (status == http.StatusBadRequest || status == http.StatusNotFound || status == http.StatusUnprocessableEntity) {
		return c.Errf(c.ErrInvalid, "%s refused the request: %s", falName, short(detail))
	}
	if status == http.StatusNotFound {
		return c.Errf(c.ErrInvalid, "%s has no such model. Check the endpoint on the model's page.", falName)
	}
	return c.HTTPError(falName, status, string(raw))
}

// falFailure says why a run ended without a file.
func falFailure(status int, raw []byte) string {
	detail := falDetail(raw)
	switch {
	case status == http.StatusUnprocessableEntity && detail != "":
		return "the " + falName + " model refused the request: " + short(detail)
	case status == 499:
		return "the " + falName + " request was cancelled"
	case detail != "":
		return falName + " could not finish the request: " + short(detail)
	}
	return falName + " could not finish the request (HTTP " + strconv.Itoa(status) + ")"
}
