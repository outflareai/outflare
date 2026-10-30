package engain

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

func fake(t *testing.T, routes map[string]string, status int) *c.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "eng_test" {
			t.Errorf("%s: key header = %q", r.URL.Path, r.Header.Get("X-API-Key"))
		}
		if r.Method != http.MethodGet {
			t.Errorf("%s: method %s, want GET only", r.URL.Path, r.Method)
		}
		body, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &c.Client{BaseURLs: map[string]string{ID: srv.URL}}
}

func TestTest(t *testing.T) {
	for _, tc := range []struct {
		name, me, balance      string
		wantName, wantProjects string
		credits                float64
		free                   string
	}{
		{"nested", `{"user": {"id": "u1", "email": "ana@acme.example", "name": "Ana"}, "projects": [{"id": "p1"}, {"id": "p2"}]}`,
			`{"credits": 120.5, "freeComments": 3}`, "Ana", "2", 120.5, "3"},
		{"enveloped", `{"data": {"email": "ana@acme.example", "projects": []}}`,
			`{"data": {"balance": "40"}}`, "ana@acme.example", "0", 40, ""},
	} {
		cl := fake(t, map[string]string{"/me": tc.me, "/balance": tc.balance}, http.StatusOK)
		a, err := Engain{}.Test(context.Background(), cl, c.Creds{"api_key": " eng_test "})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if a.Name != tc.wantName || a.Details["projects"] != tc.wantProjects || a.Details["free_comments"] != tc.free {
			t.Errorf("%s: account %+v", tc.name, a)
		}
		if a.Credits == nil || *a.Credits != tc.credits {
			t.Errorf("%s: credits %v, want %v", tc.name, a.Credits, tc.credits)
		}
	}
}

func TestTestRefusedKey(t *testing.T) {
	cl := fake(t, map[string]string{"/me": `{"error": "invalid api key"}`}, http.StatusUnauthorized)
	_, err := Engain{}.Test(context.Background(), cl, c.Creds{"api_key": "eng_test"})
	if c.KindOf(err) != c.ErrAuth {
		t.Errorf("err = %v, want an auth error", err)
	}
}

func TestDefinition(t *testing.T) {
	d := Engain{}.Definition()
	if d.ID != ID || d.Category != c.CategorySocial || len(d.Fields) != 1 || !d.Fields[0].Secret {
		t.Errorf("definition %+v", d)
	}
}

func TestItemsAndIDs(t *testing.T) {
	if got := ids([]byte(`{"data": {"id": "t1"}}`)); len(got) != 1 || got[0] != "t1" {
		t.Errorf("enveloped id = %v", got)
	}
	if got := ids([]byte(`{"parent": {"id": "x"}, "tasks": [{"id": "a"}, "b"]}`)); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("thread ids = %v", got)
	}
	list := objects([]byte(`{"data": {"tasks": [{"_id": "t1", "status": "Published", "publishedUrl": "https://www.reddit.com/r/x/comments/abc/t/def/",
		"account": {"username": "u/ana"}, "metrics": {"upvotes": 7, "replyCount": "2"}}]}}`), "tasks")
	if len(list) != 1 {
		t.Fatalf("objects = %v", list)
	}
	it := item(list[0])
	if it.ID != "t1" || it.Status != "published" || it.Author != "ana" || it.URL == "" || it.Upvotes == nil || *it.Upvotes != 7 ||
		it.Replies == nil || *it.Replies != 2 || len(it.Raw) == 0 {
		t.Errorf("item %+v", it)
	}
}

func TestCreateTaskNeverRetries(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/tasks/comment" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	cl := &c.Client{BaseURLs: map[string]string{ID: srv.URL}}
	_, err := Engain{}.CreateTask(context.Background(), cl, c.Creds{"api_key": "eng_test"},
		TaskRequest{Kind: "comment", ProjectID: "p1", URL: "https://www.reddit.com/r/x/comments/abc/t/", Content: "hi"})
	if c.KindOf(err) != c.ErrQuota || calls != 1 {
		t.Errorf("err %v after %d calls, want one quota error", err, calls)
	}
}
