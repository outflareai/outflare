package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// fakeWordPress answers the REST API for the user "ana" with the
// application password "app pass": posts it is given keep their state so
// a read-back shows them.
type fakeWordPress struct {
	mu    sync.Mutex
	posts map[string]map[string]any
	next  int
	// refuse makes every write fail as WordPress does for a user without
	// the right capability.
	refuse bool
}

func (f *fakeWordPress) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Basic YW5hOmFwcHBhc3M=" { // ana:apppass
		w.WriteHeader(401)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts == nil {
		f.posts = map[string]map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/wp-json/wp/v2/users/me":
		json.NewEncoder(w).Encode(map[string]any{"id": 3, "name": "Ana", "roles": []string{"editor"}, "capabilities": map[string]bool{"edit_posts": true}})
	case strings.HasPrefix(r.URL.Path, "/wp-json/wp/v2/posts") || strings.HasPrefix(r.URL.Path, "/wp-json/wp/v2/pages"):
		id := strings.Trim(strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/wp-json/wp/v2/posts"), "/wp-json/wp/v2/pages"), "/")
		if r.Method == http.MethodGet {
			p, ok := f.posts[id]
			if !ok {
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode(p)
			return
		}
		if f.refuse {
			w.WriteHeader(403)
			w.Write([]byte(`{"code":"rest_cannot_create","message":"Sorry, you are not allowed to create posts as this user."}`))
			return
		}
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		if id == "" {
			f.next++
			id = "10" + itoa(f.next)
			f.posts[id] = map[string]any{"id": atoi(id)}
		}
		p := f.posts[id]
		if p == nil {
			w.WriteHeader(404)
			return
		}
		for k, v := range in {
			p[k] = v
		}
		slug, _ := p["slug"].(string)
		if slug == "" {
			slug = "post-" + id
			p["slug"] = slug
		}
		p["link"] = "https://www.example.com/" + slug + "/"
		json.NewEncoder(w).Encode(p)
	default:
		w.WriteHeader(404)
	}
}

// wpFake and ghostFake are the CMS fakes freshEnv wires, so a test can
// read what they were given.
var wpFake, ghostFake = &fakeWordPress{}, &fakeGhost{}

func itoa(n int) string { return jsonNumber(n) }

func jsonNumber(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func atoi(s string) int {
	var n int
	json.Unmarshal([]byte(s), &n)
	return n
}

// fakeGhost answers the Admin API for any signed token (the signature is
// the connector's; the fake checks the scheme and the version header).
type fakeGhost struct {
	mu    sync.Mutex
	posts map[string]map[string]any
	next  int
}

func (f *fakeGhost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Ghost ") || r.Header.Get("Accept-Version") != "v5.0" {
		w.WriteHeader(401)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts == nil {
		f.posts = map[string]map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/ghost/api/admin/site/":
		json.NewEncoder(w).Encode(map[string]any{"site": map[string]any{"title": "Northpeak blog", "url": "https://blog.example.com/", "version": "5.100"}})
	case strings.HasPrefix(r.URL.Path, "/ghost/api/admin/posts/"):
		id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/ghost/api/admin/posts/"), "/")
		if r.Method == http.MethodGet {
			p, ok := f.posts[id]
			if !ok {
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"posts": []any{p}})
			return
		}
		var in struct {
			Posts []map[string]any `json:"posts"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if len(in.Posts) != 1 {
			w.WriteHeader(422)
			return
		}
		if id == "" {
			f.next++
			id = "6" + jsonNumber(f.next)
			f.posts[id] = map[string]any{"id": id, "updated_at": "2026-11-02T10:00:00.000Z"}
		} else if f.posts[id] == nil {
			w.WriteHeader(404)
			return
		} else if in.Posts[0]["updated_at"] != f.posts[id]["updated_at"] {
			w.WriteHeader(409)
			w.Write([]byte(`{"errors":[{"type":"UpdateCollisionError","message":"Saving failed! Someone else is editing this post."}]}`))
			return
		}
		p := f.posts[id]
		for k, v := range in.Posts[0] {
			p[k] = v
		}
		slug, _ := p["slug"].(string)
		if slug == "" {
			slug = "post-" + id
			p["slug"] = slug
		}
		p["url"] = "https://blog.example.com/" + slug + "/"
		p["updated_at"] = "2026-11-02T10:0" + jsonNumber(f.next%10) + ":00.000Z"
		json.NewEncoder(w).Encode(map[string]any{"posts": []any{p}})
	default:
		w.WriteHeader(404)
	}
}

// Content is written for a brand, approved, and published to its own CMS
// as a draft there or live, by a job that makes the post, reads it back
// and records its address; an edit to a live item goes back to approved
// and publishing again updates the same post. A CMS that refuses a write
// fails the publication with its reason.
func TestContentPublishing(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	path := "/v1/brands/" + brand + "/content"
	apply := func(res map[string]any) {
		t.Helper()
		if err := e.content.Apply(ctx, ws, res["publish"].(map[string]any)["id"].(string), false); err != nil {
			t.Fatal(err)
		}
	}

	// No CMS yet: writing works, publishing says what to connect.
	item := owner.must(201, "POST", path, map[string]any{"title": "Why comparison pages convert", "body": "# Hello\n\nSome **bold** text and <script>alert(1)</script> raw HTML.", "tags": []string{"seo"}})
	id := item["id"].(string)
	if item["status"] != "draft" || item["target"] != "posts" {
		t.Fatalf("item: %v", item)
	}
	st, res := owner.do("POST", path+"/"+id+"/publish", map[string]any{})
	if st != 409 || res["error"].(map[string]any)["code"] != "connector_needed" {
		t.Fatalf("publish without a CMS: %d %v", st, res)
	}

	wp := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "wordpress", "credentials": map[string]string{"site_url": "https://www.example.com", "username": "ana", "api_key": "app pass"}})
	if wp["status"] != "connected" {
		t.Fatalf("wordpress: %v", wp)
	}
	// Live needs approval; a draft on the CMS doesn't.
	st, res = owner.do("POST", path+"/"+id+"/publish", map[string]any{"live": true})
	if st != 409 {
		t.Fatalf("live before approval: %d %v", st, res)
	}
	res = owner.must(202, "POST", path+"/"+id+"/publish", map[string]any{})
	if res["publish"].(map[string]any)["kind"] != "create" || res["publish"].(map[string]any)["status"] != "queued" {
		t.Fatalf("queued: %v", res)
	}
	st, _ = owner.do("POST", path+"/"+id+"/publish", map[string]any{})
	if st != 400 {
		t.Fatalf("a second queued publication: %d", st)
	}
	apply(res)
	got := owner.must(200, "GET", path+"/"+id, nil)
	if got["status"] != "draft" || got["cms_status"] != "draft" || got["external_id"] == "" || !strings.HasPrefix(got["url"].(string), "https://www.example.com/") {
		t.Fatalf("after a draft publication: %v", got)
	}
	pubs := got["publishes"].([]any)
	if p := pubs[0].(map[string]any); p["status"] != "applied" || p["verified"] != true {
		t.Fatalf("publication: %v", p)
	}
	// The fake received rendered HTML with the script left out.
	wpFake.mu.Lock()
	var body string
	for _, p := range wpFake.posts {
		body, _ = p["content"].(string)
	}
	wpFake.mu.Unlock()
	if !strings.Contains(body, "<strong>bold</strong>") || strings.Contains(body, "<script>") {
		t.Fatalf("rendered body: %q", body)
	}

	// Approved, then live: the same post is updated and the item is published.
	owner.must(200, "PATCH", path+"/"+id, map[string]any{"status": "approved"})
	res = owner.must(202, "POST", path+"/"+id+"/publish", map[string]any{"live": true})
	if res["publish"].(map[string]any)["kind"] != "update" {
		t.Fatalf("second publication should update: %v", res)
	}
	apply(res)
	got = owner.must(200, "GET", path+"/"+id, nil)
	if got["status"] != "published" || got["cms_status"] != "published" || got["published_at"] == nil {
		t.Fatalf("after going live: %v", got)
	}
	if l := owner.must(200, "GET", path+"?status=published&published_since=2026-01-01T00:00:00Z", nil)["data"].([]any); len(l) != 1 || l[0].(map[string]any)["body"] != nil {
		t.Fatalf("published list: %v", l)
	}
	// An edit to a live item goes back to approved until published again.
	if u := owner.must(200, "PATCH", path+"/"+id, map[string]any{"body": "# Hello again"}); u["status"] != "approved" {
		t.Fatalf("edited live item: %v", u)
	}

	// A CMS that refuses the write fails the publication with its reason.
	wpFake.mu.Lock()
	wpFake.refuse = true
	wpFake.mu.Unlock()
	other := owner.must(201, "POST", path, map[string]any{"title": "A page", "target": "pages", "body": "About us", "status": "approved"})
	res = owner.must(202, "POST", path+"/"+other["id"].(string)+"/publish", map[string]any{"live": true})
	apply(res)
	got = owner.must(200, "GET", path+"/"+other["id"].(string), nil)
	if p := got["publishes"].([]any)[0].(map[string]any); p["status"] != "failed" || !strings.Contains(p["error"].(string), "refused") || got["status"] != "approved" {
		t.Fatalf("refused publication: %v %v", p, got["status"])
	}
	wpFake.mu.Lock()
	wpFake.refuse = false
	wpFake.mu.Unlock()

	// Ghost: a second CMS, chosen by id; tags and the excerpt go with the post.
	gh := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "ghost", "credentials": map[string]string{"site_url": "https://blog.example.com", "api_key": "6543210fedcba:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}})
	ghostItem := owner.must(201, "POST", path, map[string]any{"title": "On Ghost", "body": "Hi", "excerpt": "A short one", "tags": []string{"news"}, "status": "approved"})
	res = owner.must(202, "POST", path+"/"+ghostItem["id"].(string)+"/publish", map[string]any{"integration_id": gh["id"], "live": true})
	apply(res)
	got = owner.must(200, "GET", path+"/"+ghostItem["id"].(string), nil)
	if got["status"] != "published" || !strings.HasPrefix(got["url"].(string), "https://blog.example.com/") || got["integration_id"] != gh["id"] {
		t.Fatalf("ghost item: %v", got)
	}
	ghostFake.mu.Lock()
	var tags any
	for _, p := range ghostFake.posts {
		tags = p["tags"]
	}
	ghostFake.mu.Unlock()
	if !strings.Contains(jsonText(tags), "news") {
		t.Fatalf("ghost tags: %v", tags)
	}

	// Kept to its brand.
	otherBrand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	owner.must(404, "GET", "/v1/brands/"+otherBrand+"/content/"+id, nil)
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// An agent's publication is held for a person like any other change, and
// runs once approved; its draft stays an item until then.
func TestContentPublishingByAnAgent(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	bb := brandBase(t, e, ws)
	brand := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	path := "/v1/brands/" + brand + "/content"
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "wordpress", "credentials": map[string]string{"site_url": "https://www.example.com", "username": "ana", "api_key": "app pass"}})
	agent, _ := gatedAgent(t, owner, base, "Content agent", "content.manage")
	st, res := agent.do("POST", path, map[string]any{"title": "Drafted by the agent", "body": "Text"})
	if st != 202 {
		t.Fatalf("agent write: %d %v", st, res)
	}
	held := owner.must(200, "GET", bb+"/actions?status=held", nil)["data"].([]any)
	if len(held) != 1 {
		t.Fatalf("held: %v", held)
	}
	owner.must(200, "POST", bb+"/actions/"+held[0].(map[string]any)["id"].(string)+"/approve", map[string]any{"note": "ok"})
	items := owner.must(200, "GET", path, nil)["data"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["status"] != "draft" {
		t.Fatalf("items after approval: %v", items)
	}
	id := items[0].(map[string]any)["id"].(string)
	st, res = agent.do("POST", path+"/"+id+"/publish", map[string]any{})
	if st != 202 || res["queued"] == true {
		t.Fatalf("agent publish should be held: %d %v", st, res)
	}
	held = owner.must(200, "GET", bb+"/actions?status=held", nil)["data"].([]any)
	owner.must(200, "POST", bb+"/actions/"+held[0].(map[string]any)["id"].(string)+"/approve", map[string]any{"note": "ok"})
	got := owner.must(200, "GET", path+"/"+id, nil)
	pubs := got["publishes"].([]any)
	if len(pubs) != 1 || pubs[0].(map[string]any)["status"] != "queued" {
		t.Fatalf("publication after approval: %v", pubs)
	}
	if err := e.content.Apply(ctx, ws, pubs[0].(map[string]any)["id"].(string), false); err != nil {
		t.Fatal(err)
	}
	if got = owner.must(200, "GET", path+"/"+id, nil); got["cms_status"] != "draft" || got["url"] == "" {
		t.Fatalf("after the agent's publication: %v", got)
	}
}
