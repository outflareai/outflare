package linkedin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

func TestPublish(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rest/posts" || r.Header.Get("Authorization") != "Bearer li-access" ||
			r.Header.Get("LinkedIn-Version") == "" || r.Header.Get("X-Restli-Protocol-Version") != "2.0.0" {
			w.WriteHeader(400)
			return
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		w.Header().Set("x-restli-id", "urn:li:share:7001")
		w.WriteHeader(201)
	}))
	defer srv.Close()
	cl := &c.Client{HTTP: srv.Client(), BaseURLs: map[string]string{ID: srv.URL}}
	res, err := LinkedIn{}.Publish(context.Background(), cl, c.Creds{"access_token": "li-access"},
		c.SocialPostInput{Text: "We shipped (a thing) #today", Account: c.SocialAccount{ExternalID: "urn:li:organization:42", Kind: c.SocialPage}})
	if err != nil {
		t.Fatal(err)
	}
	if got["author"] != "urn:li:organization:42" || got["commentary"] != `We shipped \(a thing\) \#today` || got["visibility"] != "PUBLIC" ||
		got["lifecycleState"] != "PUBLISHED" {
		t.Fatalf("sent %v", got)
	}
	if res.ExternalID != "urn:li:share:7001" || res.URL != "https://www.linkedin.com/feed/update/urn:li:share:7001/" {
		t.Fatalf("got %+v", res)
	}
	if _, err := (LinkedIn{}).Publish(context.Background(), cl, c.Creds{}, c.SocialPostInput{Text: "hi"}); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("without a sign-in: %v", err)
	}
}
