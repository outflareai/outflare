package smartlead_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/smartlead"
)

func TestBlock(t *testing.T) {
	var posted string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/leads/add-domain-block-list" || r.URL.Query().Get("api_key") != "key" {
			w.WriteHeader(404)
			return
		}
		b, _ := io.ReadAll(r.Body)
		posted = string(b)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	cl := &c.Client{BaseURLs: map[string]string{smartlead.ID: srv.URL}}
	if err := (smartlead.Smartlead{}).Block(context.Background(), cl, c.Creds{"api_key": "key"}, []c.Blocked{{Value: "bo@acme.test"}, {Value: "rival.example", Domain: true}}); err != nil {
		t.Fatal(err)
	}
	if posted != `{"domain_block_list":["bo@acme.test","rival.example"]}` {
		t.Fatalf("posted %s", posted)
	}
}
