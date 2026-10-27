package instantly_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/instantly"
)

func TestBlocklist(t *testing.T) {
	var posted string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v2/block-lists-entries" && r.URL.Query().Get("starting_after") == "":
			w.Write([]byte(`{"items":[{"id":"1","bl_value":"Ana@Acme.test","is_domain":false},{"id":"2","bl_value":"rival.example","is_domain":true}],"next_starting_after":"2"}`))
		case r.URL.Path == "/api/v2/block-lists-entries":
			w.Write([]byte(`{"items":[],"next_starting_after":""}`))
		case r.URL.Path == "/api/v2/block-lists-entries/bulk-create" && r.Method == http.MethodPost:
			b, _ := io.ReadAll(r.Body)
			posted = string(b)
			w.Write([]byte(`{}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	cl := &c.Client{BaseURLs: map[string]string{instantly.ID: srv.URL}}
	got, err := instantly.Instantly{}.ListBlocked(context.Background(), cl, c.Creds{"api_key": "key"})
	if err != nil || len(got) != 2 || got[0] != (c.Blocked{Value: "ana@acme.test"}) || got[1] != (c.Blocked{Value: "rival.example", Domain: true}) {
		t.Fatalf("list = %v, %v", got, err)
	}
	if err := (instantly.Instantly{}).Block(context.Background(), cl, c.Creds{"api_key": "key"}, []c.Blocked{{Value: "bo@acme.test"}, {Value: "spam.example", Domain: true}}); err != nil {
		t.Fatal(err)
	}
	if posted != `{"bl_values":["bo@acme.test","spam.example"]}` {
		t.Fatalf("posted %s", posted)
	}
}
