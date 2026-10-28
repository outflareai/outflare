package x

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
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost || r.URL.Path != "/2/tweets" || r.Header.Get("Authorization") != "Bearer x-access" {
			w.WriteHeader(400)
			return
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		w.WriteHeader(201)
		io.WriteString(w, `{"data": {"id": "1890", "text": "We shipped a thing."}}`)
	}))
	defer srv.Close()
	cl := &c.Client{HTTP: srv.Client(), BaseURLs: map[string]string{ID: srv.URL}}
	res, err := X{}.Publish(context.Background(), cl, c.Creds{"access_token": "x-access"},
		c.SocialPostInput{Text: "We shipped a thing.", Account: c.SocialAccount{Handle: "northpeak"}})
	if err != nil {
		t.Fatal(err)
	}
	if got["text"] != "We shipped a thing." || res.ExternalID != "1890" || res.URL != "https://x.com/northpeak/status/1890" {
		t.Fatalf("got %v, %+v", got, res)
	}
	if _, err := (X{}).Publish(context.Background(), cl, c.Creds{}, c.SocialPostInput{Text: "hi"}); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("without a sign-in: %v", err)
	}
}
