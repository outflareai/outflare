// Package engain connects Engain, a Reddit marketing service that posts
// comments, replies and threads from its own pool of Reddit accounts, with
// the workspace's own API key.
package engain

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// ID is the connector id.
const ID = "engain"

// api is Engain's REST API. Every endpoint sits under /api/v1 and takes the
// key in the X-API-Key header.
const api = "https://api.engain.io/api/v1"

// retries is how often a rate-limited call is tried again, and maxWait the
// longest it waits before each (Engain says how long in Retry-After).
var (
	retries = 2
	maxWait = 10 * time.Second
)

// All returns the Engain connector.
func All() []c.Connector { return []c.Connector{Engain{}} }

// Engain is the connector. Connecting checks the key and reads the
// account's projects and credits; nothing is posted or spent.
type Engain struct{}

func (Engain) Definition() c.Definition {
	return c.Definition{
		ID: ID, Name: "Engain", Category: c.CategorySocial,
		Summary: "Run Reddit campaigns through Engain's accounts: comments, replies, posts and threads.",
		Description: "Uses your own Engain API key, so tasks and votes are billed to your Engain credits. " +
			"Connecting checks the key and shows your projects and credits left. API access needs Engain's Max plan.",
		Website: "https://engain.io", KeyURL: "https://app.engain.io",
		Auth: c.AuthAPIKey,
		Fields: []c.Field{{Name: "api_key", Label: "API key", Secret: true, Placeholder: "eng_...",
			Help: "In Engain: Settings, API."}},
		Capabilities: []string{},
		Status:       c.Available,
		Pricing:      "Engain credits: about 5 per comment or reply, 10 per post",
	}
}

// Test reads the account and its balance. Both calls are free.
func (e Engain) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	me, err := e.get(ctx, cl, creds, "the account", "/me")
	if err != nil {
		return c.Account{}, err
	}
	a := account(me)
	bal, err := e.get(ctx, cl, creds, "the balance", "/balance")
	if err != nil {
		return c.Account{}, err
	}
	if n, ok := number(bal, "credits", "balance", "remainingCredits", "remaining_credits"); ok {
		a.Credits = &n
	}
	if n, ok := number(bal, "freeComments", "free_comments"); ok {
		a.Details["free_comments"] = strconv.FormatFloat(n, 'f', -1, 64)
	}
	return a, nil
}

// account maps GET /me: the user, at the top or under "user", and the
// projects the key can reach.
func account(me map[string]any) c.Account {
	user := me
	if u, ok := me["user"].(map[string]any); ok {
		user = u
	}
	a := c.Account{Details: map[string]string{}}
	a.ID = text(user, "id", "_id", "userId")
	a.Email = text(user, "email")
	a.Name = text(user, "name", "displayName", "username")
	if a.Name == "" {
		a.Name = a.Email
	}
	if ps, ok := me["projects"].([]any); ok {
		a.Details["projects"] = strconv.Itoa(len(ps))
	}
	return a
}

// get makes one GET and decodes a JSON object, unwrapping a "data" envelope
// when there is one. what names it in errors ("the balance").
func (Engain) get(ctx context.Context, cl *c.Client, creds c.Creds, what, path string) (map[string]any, error) {
	u := cl.Base(ID, api) + path
	var resp *http.Response
	var body []byte
	for try := 0; ; try++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		req.Header.Set("X-API-Key", strings.TrimSpace(creds["api_key"]))
		req.Header.Set("Accept", "application/json")
		var err error
		if resp, err = cl.Do(req); err != nil {
			return nil, c.Errf(c.ErrUnavailable, "reading %s from Engain: could not reach Engain: %v", what, err)
		}
		body, err = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil {
			return nil, c.Errf(c.ErrUnavailable, "reading %s from Engain: %v", what, err)
		}
		if resp.StatusCode != http.StatusTooManyRequests || try >= retries {
			break
		}
		wait := time.Duration(1<<try) * time.Second
		if s, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && s >= 0 {
			wait = time.Duration(s) * time.Second
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(min(wait, maxWait)):
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		e := c.HTTPError("Engain", resp.StatusCode, string(body))
		e.Message = "reading " + what + " from Engain: " + e.Message
		return nil, e
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, c.Errf(c.ErrUnavailable, "reading %s from Engain: the answer is not a JSON object", what)
	}
	if d, ok := out["data"].(map[string]any); ok {
		return d, nil
	}
	return out, nil
}

// text returns the first of keys that holds a non-empty string.
func text(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// number returns the first of keys that holds a number, or a string that is
// one.
func number(m map[string]any, keys ...string) (float64, bool) {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			return v, true
		case string:
			if n, err := strconv.ParseFloat(v, 64); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}
