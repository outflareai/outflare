package journeys

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/netguard"
	"github.com/outflareai/outflare/engine/internal/secrets"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/webhooks"
)

// Calls are the steps that reach outside Outflare: a webhook to any URL,
// a message to Slack. The run's transaction only starts one: it holds the
// run (next_at) and queues JobCall. The job calls without holding a
// transaction, tries again on errors that can pass, and then records the
// outcome and moves the run on as any step does. A run whose call never
// comes back gives up on it after callHold.

// JobCall makes one call step's call.
const JobCall = "journey.call"

// callHold is how long a run waits for its step's call.
const callHold = 3 * time.Hour

// callResult is what a call did: done or failed, with a line saying how;
// retry when another try could work (it fails when no tries are left).
type callResult struct {
	outcome, detail string
	retry           bool
	// code is the error code for the event when it didn't work.
	code string
	// saved is what save_as keeps, for a step that saves.
	saved any
}

// call is one call step being made for one person.
type call struct {
	s    *Service
	ws   string
	j    *journey
	r    run
	st   *Step
	data map[string]any // person, event, vars and run, for templates
	// id is the call's id: the same on every try, so a receiver can tell
	// a retry (webhook-id and Idempotency-Key).
	id string
}

func failed(code, detail string) callResult {
	return callResult{outcome: "failed", code: code, detail: detail}
}

func retry(code, detail string) callResult {
	return callResult{outcome: "failed", retry: true, code: code, detail: detail}
}

// startCall holds the run on a call step and queues its call, plus a
// run job for when the hold runs out.
func (s *Service) startCall(ctx context.Context, tx pgx.Tx, ws string, r run, st *Step, a *actionDef) error {
	at := time.Now().Add(callHold)
	if _, err := tx.Exec(ctx, `UPDATE journey_runs SET next_at = $2, updated_at = now() WHERE id = $1`, r.id, at); err != nil {
		return err
	}
	if err := logStep(ctx, tx, ws, r, st, "calling", nil, ""); err != nil {
		return err
	}
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobCall, WorkspaceID: &ws, MaxAttempts: a.attempts,
		Payload:        map[string]any{"run_id": r.id, "step": st.ID, "taken": r.taken},
		IdempotencyKey: "journey-call:" + r.id + ":" + strconv.Itoa(r.taken) + ":" + stamp}); err != nil {
		return err
	}
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobRun, WorkspaceID: &ws, Payload: map[string]string{"run_id": r.id}, RunAt: at,
		IdempotencyKey: "journey-run:" + r.id + ":hold:" + strconv.Itoa(r.taken) + ":" + stamp})
	return err
}

func (s *Service) callJob(ctx context.Context, job jobs.Job) error {
	var in struct {
		RunID string `json:"run_id"`
		Step  string `json:"step"`
		Taken int    `json:"taken"`
	}
	if err := json.Unmarshal(job.Payload, &in); err != nil || job.WorkspaceID == nil {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	var c *call
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		r, found, err := loadRun(ctx, tx, ws, in.RunID)
		if err != nil || !found || r.step == nil || *r.step != in.Step || r.taken != in.Taken {
			return err // it moved on: exited, or gave up on the call
		}
		j, err := loadJourney(ctx, tx, ws, r.journeyID, false)
		if err != nil || j == nil {
			return err
		}
		if j.status != "active" {
			// Paused: let go of the hold, so resuming makes the call then.
			_, err := tx.Exec(ctx, `UPDATE journey_runs SET next_at = NULL, updated_at = now() WHERE id = $1`, r.id)
			return err
		}
		// The step as the version the run goes through has it.
		if err := j.pin(ctx, tx, r.version); err != nil {
			return err
		}
		st := j.step(in.Step)
		if st == nil || action(st.Kind) == nil || action(st.Kind).call == nil {
			return nil
		}
		data, err := templateData(ctx, tx, ws, r)
		if err != nil {
			return err
		}
		c = &call{s: s, ws: ws, j: j, r: r, st: st, data: data, id: r.id + "." + st.ID + "." + strconv.Itoa(r.taken)}
		return nil
	})
	if err != nil || c == nil {
		return err
	}
	started := time.Now()
	res := action(c.st.Kind).call(ctx, c)
	last := job.Attempt >= job.MaxAttempts
	ev := track.Event{Name: "journey_step.called", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"kind": c.st.Kind, "journey_id": c.j.id, "attempt": job.Attempt, "duration_ms": time.Since(started).Milliseconds()}}
	switch {
	case res.outcome == "done":
		metrics.JourneyCalled(c.st.Kind, "done", time.Since(started))
		track.Emit(ctx, ev)
	case res.retry && !last:
		metrics.JourneyCalled(c.st.Kind, "retried", time.Since(started))
		track.Fail(ctx, s.Log, ev, res.code, errors.New(res.detail))
		return fmt.Errorf("journey %s step %s: %s", c.j.id, c.st.ID, res.detail)
	default:
		metrics.JourneyCalled(c.st.Kind, "failed", time.Since(started))
		track.Fail(ctx, s.Log, ev, res.code, errors.New(res.detail))
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		r, found, err := loadRun(ctx, tx, ws, in.RunID)
		if err != nil || !found || r.step == nil || *r.step != in.Step || r.taken != in.Taken {
			return err
		}
		if res.saved != nil && c.st.SaveAs != "" && res.outcome == "done" {
			raw, _ := json.Marshal(res.saved)
			all, _ := json.Marshal(r.vars)
			if len(all)+len(raw) > maxVars {
				// A run keeps at most maxVars: past it, only the status.
				raw, _ = json.Marshal(map[string]any{"status": res.saved.(map[string]any)["status"], "body": nil, "truncated": true})
			}
			if _, err := tx.Exec(ctx, `UPDATE journey_runs SET vars = jsonb_set(vars, ARRAY[$2::text], $3::jsonb) WHERE id = $1`, r.id, c.st.SaveAs, raw); err != nil {
				return err
			}
		}
		more, err := s.moveOn(ctx, tx, ws, r, c.st, onError(c.st, taken{next: c.st.Next, outcome: res.outcome, detail: res.detail}))
		if err != nil || !more {
			return err
		}
		_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobRun, WorkspaceID: &ws, Payload: map[string]string{"run_id": r.id},
			IdempotencyKey: "journey-run:" + r.id + ":called:" + strconv.Itoa(r.taken)})
		return err
	})
}

// templateData is what {{…}} in a call step can use: the person (their
// fields, name, email and id), the run's event, what earlier steps saved,
// and the run.
func templateData(ctx context.Context, q jobs.Querier, ws string, r run) (map[string]any, error) {
	var name, address string
	var attrs []byte
	err := q.QueryRow(ctx, `SELECT r.name, COALESCE(i.value, ''), r.attributes FROM records r
		LEFT JOIN LATERAL (SELECT value FROM record_identities i WHERE i.record_id = r.id AND i.namespace = 'email' AND i.valid_to IS NULL
			ORDER BY i.verified DESC, i.valid_from, i.id LIMIT 1) i ON true
		WHERE r.workspace_id = $1 AND r.id = $2`, ws, r.recordID).Scan(&name, &address, &attrs)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var attributes map[string]any
	_ = json.Unmarshal(attrs, &attributes)
	event := r.context
	if event == nil {
		event = map[string]any{}
	}
	return map[string]any{"person": emails.PersonData(r.recordID, name, address, attributes), "event": event, "vars": r.vars,
		"run": map[string]any{"id": r.id, "journey_id": r.journeyID}}, nil
}

// ---- Webhooks ----

var (
	headerRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)
	paramRE  = regexp.MustCompile(`^[A-Za-z0-9_.\-\[\]]{1,64}$`)
	saveRE   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	// Headers Outflare sets itself.
	ownHeaders = map[string]bool{"host": true, "content-length": true, "content-type": true, "user-agent": true, "idempotency-key": true,
		"webhook-id": true, "webhook-timestamp": true, "webhook-signature": true, "transfer-encoding": true, "connection": true}
)

// Limits on what a webhook sends and keeps.
const (
	maxBody   = 16 << 10 // the body Outflare sends
	maxAnswer = 64 << 10 // the answer it reads
	maxSaved  = 16 << 10 // the answer it keeps in vars
	maxVars   = 64 << 10 // everything a run keeps
)

func checkWebhook(st Step) string {
	switch st.Method {
	case "", "POST", "GET", "PUT", "PATCH", "DELETE":
	default:
		return "method is POST, GET, PUT, PATCH or DELETE"
	}
	if len(st.URL) > 2000 {
		return "url is at most 2,000 characters"
	}
	if err := emails.CheckFill(st.URL); err != nil {
		return "url: " + err.Error()
	}
	// The host is fixed: only the path and query take {{…}}.
	u, err := url.Parse(st.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || strings.Contains(u.Host, "{") {
		return "url must be an absolute http(s) URL, with {{…}} only in its path or query"
	}
	if u.User != nil {
		return "url must not hold a user name or password; use auth basic"
	}
	if len(st.Headers) > 20 {
		return "at most 20 headers"
	}
	for k, v := range st.Headers {
		if !headerRE.MatchString(k) || ownHeaders[strings.ToLower(k)] {
			return "header " + k + " is not a header a step can set"
		}
		if len(v) > 2000 || strings.ContainsAny(v, "\r\n") {
			return "header " + k + ": a value is one line, up to 2,000 characters"
		}
		if err := emails.CheckFill(v); err != nil {
			return "header " + k + ": " + err.Error()
		}
	}
	if st.Body != nil {
		if st.Method == "GET" {
			return "a GET sends no body"
		}
		raw, _ := json.Marshal(st.Body)
		if len(raw) > maxBody {
			return "body is at most 16 KB"
		}
		if msg := checkBody(st.Body); msg != "" {
			return "body: " + msg
		}
	}
	if a := st.Auth; a != nil {
		switch a.Type {
		case "", "none":
		case "header":
			if !headerRE.MatchString(a.Name) || ownHeaders[strings.ToLower(a.Name)] {
				return "auth header needs name, the header the key goes in, such as Authorization"
			}
		case "basic":
			if a.Username == "" || len(a.Username) > 200 || strings.Contains(a.Username, ":") {
				return "auth basic needs username (no colon)"
			}
		case "query":
			if !paramRE.MatchString(a.Name) {
				return "auth query needs name, the URL parameter the key goes in, such as api_key"
			}
		default:
			return "auth type is none, header, basic or query"
		}
	}
	if st.SaveAs != "" && !saveRE.MatchString(st.SaveAs) {
		return "save_as is lowercase letters, digits and _, up to 40"
	}
	return ""
}

func checkBody(v any) string {
	switch t := v.(type) {
	case string:
		if err := emails.CheckFill(t); err != nil {
			return err.Error()
		}
	case map[string]any:
		for _, x := range t {
			if msg := checkBody(x); msg != "" {
				return msg
			}
		}
	case []any:
		for _, x := range t {
			if msg := checkBody(x); msg != "" {
				return msg
			}
		}
	}
	return ""
}

// NeedsSecret reports whether a step sends a key that must be set before
// its journey goes live.
func NeedsSecret(st Step) bool {
	return st.Kind == "webhook" && st.Auth != nil && st.Auth.Type != "" && st.Auth.Type != "none"
}

// fillBody fills a body's strings. A string that is only {{name}} becomes
// the value itself, so numbers, lists and objects keep their type.
func fillBody(v any, data map[string]any) (any, error) {
	switch t := v.(type) {
	case string:
		if name, ok := wholeTag(t); ok {
			got, _ := lookup(data, name)
			return got, nil
		}
		return emails.Fill(t, data, nil)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			f, err := fillBody(x, data)
			if err != nil {
				return nil, err
			}
			out[k] = f
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			f, err := fillBody(x, data)
			if err != nil {
				return nil, err
			}
			out[i] = f
		}
		return out, nil
	}
	return v, nil
}

var wholeTagRE = regexp.MustCompile(`^\{\{\s*([A-Za-z_][\w.]*)\s*\}\}$`)

func wholeTag(s string) (string, bool) {
	m := wholeTagRE.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	return m[1], true
}

func webhookCall(ctx context.Context, c *call) callResult {
	st := c.st
	method := st.Method
	if method == "" {
		method = http.MethodPost
	}
	raw, err := emails.Fill(st.URL, c.data, url.QueryEscape)
	if err != nil {
		return failed("invalid_request", "url: "+err.Error())
	}
	if err := webhooks.CheckURL(raw, c.s.AllowPrivate); err != nil {
		return failed("invalid_request", err.Error())
	}
	u, _ := url.Parse(raw)
	var body []byte
	if st.Body != nil && method != http.MethodGet {
		filled, err := fillBody(st.Body, c.data)
		if err != nil {
			return failed("invalid_request", "body: "+err.Error())
		}
		body, _ = json.Marshal(filled)
	}
	var key string
	if NeedsSecret(*st) {
		k, ok, err := stepSecret(ctx, c.s.Pool, c.s.Box, c.ws, c.j.id, st.ID)
		if err != nil {
			return retry("internal", "the key could not be read")
		}
		if !ok {
			return failed("not_configured", "the step's key isn't set: set it on the step")
		}
		key = k
		if st.Auth.Type == "query" {
			q := u.Query()
			q.Set(st.Auth.Name, key)
			u.RawQuery = q.Encode()
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return failed("invalid_request", "the request could not be made")
	}
	for k, v := range st.Headers {
		filled, err := emails.Fill(v, c.data, nil)
		if err != nil {
			return failed("invalid_request", "header "+k+": "+err.Error())
		}
		req.Header.Set(k, strings.NewReplacer("\r", " ", "\n", " ").Replace(filled))
	}
	switch {
	case !NeedsSecret(*st):
	case st.Auth.Type == "header":
		req.Header.Set(st.Auth.Name, key)
	case st.Auth.Type == "basic":
		req.SetBasicAuth(st.Auth.Username, key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "Outflare-Journeys/1 (+https://outflare.ai)")
	req.Header.Set("Idempotency-Key", c.id)
	signing, err := SigningSecrets(ctx, c.s.Pool, c.s.Box, c.ws)
	if err != nil {
		return retry("internal", "the signing secret could not be read")
	}
	now := time.Now()
	var sigs []string
	for _, sec := range signing {
		sig, err := webhooks.Sign(sec, c.id, now, body)
		if err != nil {
			return retry("internal", "the call could not be signed")
		}
		sigs = append(sigs, sig)
	}
	req.Header.Set("webhook-id", c.id)
	req.Header.Set("webhook-timestamp", strconv.FormatInt(now.Unix(), 10))
	req.Header.Set("webhook-signature", strings.Join(sigs, " "))

	started := time.Now()
	resp, err := c.s.httpClient().Do(req)
	if err != nil {
		// No answer: a timeout, a refused connection or a refused address.
		var dial *net.OpError
		if errors.As(err, &dial) && strings.Contains(err.Error(), "non-public") {
			return failed("invalid_request", "the URL is not on the public internet")
		}
		return retry("unavailable", "no answer: "+shortErr(err))
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	took := time.Since(started).Milliseconds()
	detail := fmt.Sprintf("%d in %d ms", resp.StatusCode, took)
	switch code := resp.StatusCode; {
	case code >= 200 && code < 300:
		return callResult{outcome: "done", detail: detail, saved: saved(code, resp.Header.Get("Content-Type"), answer)}
	case code == http.StatusRequestTimeout || code == http.StatusTooEarly || code == http.StatusTooManyRequests || code >= 500:
		return retry("unavailable", detail)
	default:
		return failed("platform_error", "the receiver answered "+detail)
	}
}

// saved is what save_as keeps of an answer: its status and body, JSON
// when it is JSON, else text, cut to maxSaved.
func saved(status int, contentType string, body []byte) map[string]any {
	out := map[string]any{"status": status, "body": nil}
	if len(body) == 0 {
		return out
	}
	var v any
	if len(body) <= maxSaved && json.Unmarshal(body, &v) == nil {
		out["body"] = v
		return out
	}
	text := string(body)
	if len(text) > maxSaved {
		text = text[:maxSaved]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
		out["truncated"] = true
	}
	out["body"] = text
	return out
}

func shortErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	s := err.Error()
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func (s *Service) httpClient() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if !s.AllowPrivate {
		dialer.Control = func(network, address string, rc syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip, err := netip.ParseAddr(host); err != nil || !netguard.PublicAddr(ip) {
				return fmt.Errorf("refusing a non-public address %s", host)
			}
			return nil
		}
	}
	return &http.Client{
		Timeout:   20 * time.Second,
		Transport: &http.Transport{Proxy: nil, DialContext: dialer.DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 20 * time.Second},
		// A redirect could point anywhere; receivers answer directly.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// ---- Keys ----

// stepSecret opens a webhook step's key.
func stepSecret(ctx context.Context, q jobs.Querier, box *secrets.Box, ws, journeyID, step string) (string, bool, error) {
	if box == nil {
		return "", false, errors.New("no secrets box")
	}
	var id string
	var sealed []byte
	err := q.QueryRow(ctx, `SELECT id::text, secret FROM journey_step_secrets WHERE workspace_id = $1 AND journey_id = $2 AND step = $3`,
		ws, journeyID, step).Scan(&id, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	plain, err := box.Open(sealed, []byte(id))
	if err != nil {
		return "", false, err
	}
	return string(plain), true, nil
}

// SetStepSecret stores a webhook step's key, sealed, in place of any
// earlier one.
func SetStepSecret(ctx context.Context, tx pgx.Tx, box *secrets.Box, ws, journeyID, step, value string, by *string) (time.Time, error) {
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO journey_step_secrets (workspace_id, journey_id, step, secret, created_by) VALUES ($1, $2, $3, '', $4)
		ON CONFLICT (journey_id, step) DO UPDATE SET created_by = EXCLUDED.created_by RETURNING id::text`, ws, journeyID, step, by).Scan(&id)
	if err != nil {
		return time.Time{}, err
	}
	var at time.Time
	err = tx.QueryRow(ctx, `UPDATE journey_step_secrets SET secret = $2, updated_at = now() WHERE id = $1 RETURNING updated_at`,
		id, box.Seal([]byte(value), []byte(id))).Scan(&at)
	return at, err
}

// SigningSecrets is the workspace's signing secret, made the first time
// it is needed, then the previous one while it still signs after a
// rotation.
func SigningSecrets(ctx context.Context, q jobs.Querier, box *secrets.Box, ws string) ([]string, error) {
	if box == nil {
		return nil, errors.New("no secrets box")
	}
	var sealed, prev []byte
	var prevUntil *time.Time
	err := q.QueryRow(ctx, `SELECT secret, previous_secret, previous_expires_at FROM journey_signing_secrets WHERE workspace_id = $1`, ws).
		Scan(&sealed, &prev, &prevUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := q.Exec(ctx, `INSERT INTO journey_signing_secrets (workspace_id, secret) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			ws, box.Seal([]byte(webhooks.NewSecret()), []byte(ws))); err != nil {
			return nil, err
		}
		return SigningSecrets(ctx, q, box, ws)
	}
	if err != nil {
		return nil, err
	}
	cur, err := box.Open(sealed, []byte(ws))
	if err != nil {
		return nil, err
	}
	out := []string{string(cur)}
	if prev != nil && prevUntil != nil && time.Now().Before(*prevUntil) {
		if p, err := box.Open(prev, []byte(ws)); err == nil {
			out = append(out, string(p))
		}
	}
	return out, nil
}

// SigningOverlap is how long the previous signing secret keeps signing
// after a rotation.
const SigningOverlap = 24 * time.Hour

// RotateSigningSecret makes a new signing secret and returns it. The old
// one signs too for SigningOverlap, unless expireNow (for a leaked one).
func RotateSigningSecret(ctx context.Context, tx pgx.Tx, box *secrets.Box, ws string, expireNow bool) (string, error) {
	if _, err := SigningSecrets(ctx, tx, box, ws); err != nil {
		return "", err
	}
	secret := webhooks.NewSecret()
	_, err := tx.Exec(ctx, `UPDATE journey_signing_secrets SET previous_secret = CASE WHEN $4 THEN NULL ELSE secret END,
		previous_expires_at = CASE WHEN $4 THEN NULL ELSE now() + $3::interval END, secret = $2, rotated_at = now() WHERE workspace_id = $1`,
		ws, box.Seal([]byte(secret), []byte(ws)), fmt.Sprintf("%d seconds", int(SigningOverlap.Seconds())), expireNow)
	return secret, err
}

// SigningSecretInfo is when the workspace's signing secret was made and
// last rotated, with the current secret.
type SigningSecretInfo struct {
	Secret    string     `json:"secret"`
	CreatedAt time.Time  `json:"created_at"`
	RotatedAt *time.Time `json:"rotated_at"`
	// PreviousExpiresAt is when the secret before the last rotation stops
	// signing; null when it no longer does.
	PreviousExpiresAt *time.Time `json:"previous_expires_at"`
}

// SigningSecret reads the workspace's signing secret, making it the first
// time.
func SigningSecret(ctx context.Context, q jobs.Querier, box *secrets.Box, ws string) (SigningSecretInfo, error) {
	list, err := SigningSecrets(ctx, q, box, ws)
	if err != nil {
		return SigningSecretInfo{}, err
	}
	info := SigningSecretInfo{Secret: list[0]}
	err = q.QueryRow(ctx, `SELECT created_at, rotated_at, CASE WHEN previous_expires_at > now() THEN previous_expires_at END
		FROM journey_signing_secrets WHERE workspace_id = $1`, ws).Scan(&info.CreatedAt, &info.RotatedAt, &info.PreviousExpiresAt)
	return info, err
}

// ---- Slack ----

func checkSlack(st Step) string {
	switch {
	case strings.TrimSpace(st.Channel) == "" || len(st.Channel) > 100:
		return "channel is the Slack channel's id"
	case strings.TrimSpace(st.Text) == "" || len(st.Text) > 3000:
		return "text is 1 to 3,000 characters"
	case len(st.Title) > 120:
		return "title is at most 120 characters"
	case st.ConnectionID != "" && !uuidRE.MatchString(st.ConnectionID):
		return "connection_id must be an id"
	}
	for _, t := range []string{st.Text, st.Title} {
		if err := emails.CheckFill(t); err != nil {
			return err.Error()
		}
	}
	return ""
}

func slackCall(ctx context.Context, c *call) callResult {
	s, st := c.s, c.st
	if s.Store == nil {
		return failed("connector_needed", "Slack can't be reached from here")
	}
	text, err := emails.Fill(st.Text, c.data, nil)
	if err != nil {
		return failed("invalid_request", err.Error())
	}
	title, _ := emails.Fill(st.Title, c.data, nil)
	if title == "" {
		title, text = text, ""
	}
	var o *connectors.Opened
	if st.ConnectionID != "" {
		o, err = s.Store.Open(ctx, s.Pool, c.ws, st.ConnectionID)
	} else {
		brand := ""
		if c.j.brandID != nil {
			brand = *c.j.brandID
		}
		o, err = s.Store.OpenCapable(ctx, s.Pool, c.ws, brand, connectors.CapNotify)
	}
	switch {
	case errors.Is(err, connectors.ErrNotFound), errors.Is(err, connectors.ErrNoCapable):
		return failed("connector_needed", "connect Slack in Settings, Connections")
	case err != nil && connectors.KindOf(err) == connectors.ErrAuth:
		return failed("connector_auth", "Slack refused the connection: connect it again")
	case err != nil:
		return retry("connector_unavailable", shortErr(err))
	}
	n, ok := o.Connector.(connectors.Notifier)
	if !ok {
		return failed("connector_needed", "that connection can't post messages")
	}
	err = n.Post(ctx, o.Client, o.Creds, st.Channel, connectors.Notice{Title: title, Text: text})
	_ = s.Store.Checked(ctx, s.Pool, c.ws, o.ID, nil, err)
	if err != nil {
		code := "connector_" + connectors.KindOf(err)
		if code == "connector_auth" || code == "connector_invalid" {
			return failed(code, shortErr(err))
		}
		return retry(code, shortErr(err))
	}
	return callResult{outcome: "done", detail: "posted"}
}
