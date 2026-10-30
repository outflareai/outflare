// Package engain connects Engain, a Reddit marketing service that posts
// comments, replies, posts and threads from its own pool of Reddit
// accounts and adds upvotes, with the workspace's own API key.
package engain

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
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
// Calls that create something are never retried: a lost answer could mean
// it was created.
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
		Summary: "Run Reddit campaigns through Engain's accounts: comments, replies, posts, threads and upvotes.",
		Description: "Uses your own Engain API key, so tasks and votes are billed to your Engain credits. " +
			"Connecting checks the key and shows your projects and credits left. API access needs Engain's Max plan.",
		Website: "https://engain.io", KeyURL: "https://app.engain.io",
		Auth: c.AuthAPIKey,
		Fields: []c.Field{{Name: "api_key", Label: "API key", Secret: true, Placeholder: "eng_...",
			Help: "In Engain: Settings, API."}},
		Capabilities: []string{c.CapRedditCampaigns},
		Status:       c.Available,
		Pricing:      "Engain credits: about 5 per comment or reply, 10 per post, 0.1 per upvote",
	}
}

// Test reads the account and its balance. Both calls are free.
func (e Engain) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	me, err := e.object(ctx, cl, creds, "the account", http.MethodGet, "/me", nil, nil)
	if err != nil {
		return c.Account{}, err
	}
	a := account(me)
	bal, err := e.Balance(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	a.Credits = bal.Credits
	if bal.FreeComments != nil {
		a.Details["free_comments"] = strconv.FormatFloat(*bal.FreeComments, 'f', -1, 64)
	}
	return a, nil
}

// Balance is what the workspace has left at Engain.
type Balance struct {
	Credits      *float64
	FreeComments *float64
}

// Balance reads the credits and free comments left. Free.
func (e Engain) Balance(ctx context.Context, cl *c.Client, creds c.Creds) (Balance, error) {
	m, err := e.object(ctx, cl, creds, "the balance", http.MethodGet, "/balance", nil, nil)
	if err != nil {
		return Balance{}, err
	}
	var b Balance
	if n, ok := number(m, "credits", "balance", "remainingCredits", "remaining_credits"); ok {
		b.Credits = &n
	}
	if n, ok := number(m, "freeComments", "free_comments"); ok {
		b.FreeComments = &n
	}
	return b, nil
}

// Project is an Engain project (its word for a campaign).
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Projects lists the projects the key reaches. Free.
func (e Engain) Projects(ctx context.Context, cl *c.Client, creds c.Creds) ([]Project, error) {
	me, err := e.object(ctx, cl, creds, "the account", http.MethodGet, "/me", nil, nil)
	if err != nil {
		return nil, err
	}
	ps, _ := me["projects"].([]any)
	out := []Project{}
	for _, p := range ps {
		if m, ok := p.(map[string]any); ok {
			if id := text(m, "id", "_id", "projectId"); id != "" {
				out = append(out, Project{ID: id, Name: text(m, "name", "title")})
			}
		}
	}
	return out, nil
}

// Upvote plan sent with a task: how many upvotes, how fast, from when.
type Rank struct {
	UpvotesGoal   int    `json:"upvotesGoal"`
	UpvotesPerDay int    `json:"upvotesPerDay"`
	StartingDate  string `json:"startingDate,omitempty"`
}

// TaskRequest is one comment, reply or post to create.
type TaskRequest struct {
	ProjectID string `json:"projectId"`
	// URL is the thread (comment) or the comment (reply).
	URL          string     `json:"url,omitempty"`
	SubredditURL string     `json:"subredditUrl,omitempty"`
	PostTitle    string     `json:"postTitle,omitempty"`
	Content      string     `json:"content"`
	ScheduledAt  string     `json:"scheduledAt,omitempty"`
	Rank         *Rank      `json:"rank,omitempty"`
	Labels       []string   `json:"labels,omitempty"`
	Kind         string     `json:"-"` // comment, reply or post
	Thread       *ThreadReq `json:"-"`
}

// ThreadReq is a thread group: a parent task and replies from up to ten
// different pool accounts.
type ThreadReq struct {
	ProjectID  string         `json:"projectId"`
	ParentTask map[string]any `json:"parentTask"`
	ChildTasks []ThreadChild  `json:"childTasks,omitempty"`
	Labels     []string       `json:"labels,omitempty"`
}

// ThreadChild is one reply in a thread group.
type ThreadChild struct {
	ID            string         `json:"id"`
	ParentID      *string        `json:"parentId"`
	Type          string         `json:"type"`
	PosterAccount int            `json:"posterAccount"`
	Content       string         `json:"content"`
	ThreadOrder   int            `json:"threadOrder"`
	ScheduleDelay map[string]any `json:"scheduleDelay"`
}

// Created is what Engain answered when it took a task or order: its id(s)
// and the whole answer, kept so the shape can be read later.
type Created struct {
	IDs []string
	Raw json.RawMessage
}

// CreateTask creates a comment, reply or post task, or a thread group.
// It spends credits and is never retried.
func (e Engain) CreateTask(ctx context.Context, cl *c.Client, creds c.Creds, t TaskRequest) (Created, error) {
	var path string
	var body any = t
	switch t.Kind {
	case "comment":
		path = "/tasks/comment"
	case "reply":
		path = "/tasks/reply"
	case "post":
		path = "/tasks/post"
	case "thread":
		path, body = "/tasks/thread", t.Thread
	default:
		return Created{}, c.Errf(c.ErrInvalid, "Engain has no %q task", t.Kind)
	}
	raw, err := e.call(ctx, cl, creds, "a "+t.Kind+" task", http.MethodPost, path, nil, body)
	if err != nil {
		return Created{}, err
	}
	return Created{IDs: ids(raw), Raw: raw}, nil
}

// VoteOrder is an upvote order on a Reddit post or comment.
type VoteOrder struct {
	ProjectID   string   `json:"projectId"`
	URL         string   `json:"url"`
	Target      string   `json:"target"` // post or comment
	Quantity    int      `json:"quantity"`
	VotesPerDay int      `json:"votesPerDay,omitempty"`
	ScheduledAt string   `json:"scheduledAt,omitempty"`
	Labels      []string `json:"labels,omitempty"`
}

// CreateUpvoteOrder orders upvotes. It spends credits and is never retried.
func (e Engain) CreateUpvoteOrder(ctx context.Context, cl *c.Client, creds c.Creds, o VoteOrder) (Created, error) {
	raw, err := e.call(ctx, cl, creds, "an upvote order", http.MethodPost, "/orders/upvote", nil, o)
	if err != nil {
		return Created{}, err
	}
	return Created{IDs: ids(raw), Raw: raw}, nil
}

// Item is a task or order as Engain reports it now. Fields Engain may name
// in more than one way are read by each name; Raw keeps the whole object.
type Item struct {
	ID     string
	Status string
	// URL is where the content went live on Reddit, once it has.
	URL       string
	Author    string
	Upvotes   *float64
	Replies   *float64
	Delivered *float64
	Credits   *float64
	Raw       json.RawMessage
}

// Tasks reads up to 100 tasks by id, with live metrics when refresh is set
// (one at a time then, since only the single read refreshes). Free.
func (e Engain) Tasks(ctx context.Context, cl *c.Client, creds c.Creds, project string, taskIDs []string) ([]Item, error) {
	return e.bulk(ctx, cl, creds, "tasks", "/tasks/bulk", project, taskIDs)
}

// Orders reads up to 100 vote orders by id. Free.
func (e Engain) Orders(ctx context.Context, cl *c.Client, creds c.Creds, project string, orderIDs []string) ([]Item, error) {
	return e.bulk(ctx, cl, creds, "orders", "/orders/bulk", project, orderIDs)
}

// Task reads one task with refreshed upvote and reply counts. Free.
func (e Engain) Task(ctx context.Context, cl *c.Client, creds c.Creds, id string) (Item, error) {
	raw, err := e.call(ctx, cl, creds, "a task", http.MethodGet, "/tasks/"+url.PathEscape(id), url.Values{"refreshMetrics": {"true"}}, nil)
	if err != nil {
		return Item{}, err
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return Item{}, c.Errf(c.ErrUnavailable, "reading a task from Engain: the answer is not a JSON object")
	}
	if d, ok := m["data"].(map[string]any); ok {
		m = d
	}
	return item(m), nil
}

func (e Engain) bulk(ctx context.Context, cl *c.Client, creds c.Creds, what, path, project string, list []string) ([]Item, error) {
	if len(list) == 0 {
		return nil, nil
	}
	q := url.Values{"ids": {strings.Join(list, ",")}}
	if project != "" {
		q.Set("projectId", project)
	}
	raw, err := e.call(ctx, cl, creds, what, http.MethodGet, path, q, nil)
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, m := range objects(raw, what) {
		out = append(out, item(m))
	}
	return out, nil
}

// RuleCheck preflights up to 25 drafts against one subreddit; body is sent
// as given and the answer handed back as is. Free, 5 a minute.
func (e Engain) RuleCheck(ctx context.Context, cl *c.Client, creds c.Creds, body map[string]any) (json.RawMessage, error) {
	return e.call(ctx, cl, creds, "a rule check", http.MethodPost, "/rule-check/batch", nil, body)
}

// SubredditStats reads Engain's historical removal rates for a subreddit.
// Free.
func (e Engain) SubredditStats(ctx context.Context, cl *c.Client, creds c.Creds, subreddit string) (json.RawMessage, error) {
	return e.call(ctx, cl, creds, "subreddit stats", http.MethodGet, "/subreddit-stats", url.Values{"subreddit": {subreddit}}, nil)
}

// Opportunities lists threads Engain found for a project: kind is
// new_opportunities or seo_opportunities. Free.
func (e Engain) Opportunities(ctx context.Context, cl *c.Client, creds c.Creds, project, kind, cursor string, n int) (json.RawMessage, error) {
	q := url.Values{"type": {kind}}
	if project != "" {
		q.Set("projectId", project)
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if n > 0 {
		q.Set("numItems", strconv.Itoa(n))
	}
	return e.call(ctx, cl, creds, "opportunities", http.MethodGet, "/opportunities", q, nil)
}

// CreditTransactions lists the credit ledger, newest first. Free.
func (e Engain) CreditTransactions(ctx context.Context, cl *c.Client, creds c.Creds, project, cursor string, after time.Time) (json.RawMessage, error) {
	q := url.Values{"numItems": {"100"}}
	if project != "" {
		q.Set("projectId", project)
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if !after.IsZero() {
		q.Set("createdAfter", after.UTC().Format(time.RFC3339))
	}
	return e.call(ctx, cl, creds, "credit transactions", http.MethodGet, "/credit-transactions", q, nil)
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

// item maps a task or order object.
func item(m map[string]any) Item {
	it := Item{
		ID:     text(m, "id", "_id", "taskId", "orderId"),
		Status: strings.ToLower(text(m, "status", "state")),
		URL: text(m, "publishedUrl", "redditUrl", "commentUrl", "postUrl", "permalink", "resultUrl", "liveUrl",
			"published_url", "reddit_url"),
		Author: strings.TrimPrefix(text(m, "redditUsername", "username", "account", "accountName", "author", "posterUsername"), "u/"),
	}
	if a, ok := m["account"].(map[string]any); ok && it.Author == "" {
		it.Author = strings.TrimPrefix(text(a, "username", "name"), "u/")
	}
	if met, ok := m["metrics"].(map[string]any); ok {
		for k, v := range met {
			if _, dup := m[k]; !dup {
				m[k] = v
			}
		}
	}
	if n, ok := number(m, "upvotes", "score", "upvoteCount"); ok {
		it.Upvotes = &n
	}
	if n, ok := number(m, "replies", "replyCount", "numReplies", "comments"); ok {
		it.Replies = &n
	}
	if n, ok := number(m, "delivered", "deliveredVotes", "votesDelivered", "completed"); ok {
		it.Delivered = &n
	}
	if n, ok := number(m, "credits", "cost", "creditsSpent", "price"); ok {
		it.Credits = &n
	}
	it.Raw, _ = json.Marshal(m)
	return it
}

// ids finds the id or ids of what a create call made: an object, a data
// envelope, a list, or a thread group's tasks.
func ids(raw json.RawMessage) []string {
	var out []string
	for _, m := range objects(raw, "") {
		if id := text(m, "id", "_id", "taskId", "orderId"); id != "" {
			out = append(out, id)
		}
		for _, k := range []string{"tasks", "childTasks", "taskIds"} {
			switch v := m[k].(type) {
			case []any:
				for _, x := range v {
					switch t := x.(type) {
					case string:
						out = append(out, t)
					case map[string]any:
						if id := text(t, "id", "_id", "taskId"); id != "" {
							out = append(out, id)
						}
					}
				}
			}
		}
	}
	return out
}

// objects reads the objects in an answer: a list, an object, or either
// under "data", "items" or what (e.g. "tasks").
func objects(raw json.RawMessage, what string) []map[string]any {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	for i := 0; i < 3; i++ {
		m, ok := v.(map[string]any)
		if !ok {
			break
		}
		next, found := any(nil), false
		for _, k := range []string{"data", "items", what, "results"} {
			if k == "" {
				continue
			}
			if x, ok := m[k]; ok {
				if _, isList := x.([]any); isList || k == "data" {
					next, found = x, true
					break
				}
			}
		}
		if !found {
			break
		}
		v = next
	}
	switch t := v.(type) {
	case []any:
		var out []map[string]any
		for _, x := range t {
			if m, ok := x.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	case map[string]any:
		return []map[string]any{t}
	}
	return nil
}

// object makes one call and decodes a JSON object, unwrapping a "data"
// envelope when there is one.
func (e Engain) object(ctx context.Context, cl *c.Client, creds c.Creds, what, method, path string, q url.Values, body any) (map[string]any, error) {
	raw, err := e.call(ctx, cl, creds, what, method, path, q, body)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, c.Errf(c.ErrUnavailable, "reading %s from Engain: the answer is not a JSON object", what)
	}
	if d, ok := out["data"].(map[string]any); ok {
		return d, nil
	}
	return out, nil
}

// call makes one request. GETs that Engain rate-limits are tried again
// after the wait it asks for; anything else is tried once. what names the
// call in errors ("the balance").
func (Engain) call(ctx context.Context, cl *c.Client, creds c.Creds, what, method, path string, q url.Values, body any) (json.RawMessage, error) {
	u := cl.Base(ID, api) + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, c.Errf(c.ErrInvalid, "sending %s to Engain: %v", what, err)
		}
	}
	var resp *http.Response
	var out []byte
	for try := 0; ; try++ {
		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, _ := http.NewRequestWithContext(ctx, method, u, rd)
		req.Header.Set("X-API-Key", strings.TrimSpace(creds["api_key"]))
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		var err error
		if resp, err = cl.Do(req); err != nil {
			return nil, c.Errf(c.ErrUnavailable, "%s with Engain: could not reach Engain: %v", what, err)
		}
		out, err = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil {
			return nil, c.Errf(c.ErrUnavailable, "%s with Engain: %v", what, err)
		}
		if resp.StatusCode != http.StatusTooManyRequests || method != http.MethodGet || try >= retries {
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
		e := c.HTTPError("Engain", resp.StatusCode, string(out))
		e.Message = what + ": " + e.Message
		return nil, e
	}
	if !json.Valid(out) {
		return nil, c.Errf(c.ErrUnavailable, "%s with Engain: the answer is not JSON", what)
	}
	return out, nil
}

// text returns the first of keys that holds a non-empty string (or a
// number, written out).
func text(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
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
