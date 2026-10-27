package sysmail

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProviders(t *testing.T) {
	var got struct {
		path, auth string
		body       map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		got.auth = r.Header.Get("Authorization") + r.Header.Get("X-Postmark-Server-Token")
		json.NewDecoder(r.Body).Decode(&got.body) //nolint:errcheck
		switch {
		case got.auth == "bad":
			http.Error(w, `{"message":"invalid token"}`, http.StatusUnauthorized)
		case got.body["to"] == "gone@example.com":
			w.Write([]byte(`{"success":true,"result":{"delivered":[],"permanent_bounces":["gone@example.com"],"queued":[]}}`)) //nolint:errcheck
		case r.URL.Path == "/accounts/acc/email/sending/send":
			w.Write([]byte(`{"success":true,"result":{"delivered":["ann@example.com"],"permanent_bounces":[],"queued":[]}}`)) //nolint:errcheck
		}
	}))
	defer srv.Close()
	m := Message{To: "ann@example.com", Subject: "Hi", Text: "Hello", HTML: "<p>Hello</p>"}
	ctx := context.Background()

	if err := (&Postmark{Token: "pm", From: "login@x.test", BaseURL: srv.URL}).Send(ctx, m); err != nil {
		t.Fatal(err)
	}
	if got.path != "/email" || got.auth != "pm" || got.body["To"] != "ann@example.com" || got.body["From"] != "login@x.test" || got.body["TextBody"] != "Hello" || got.body["HtmlBody"] != "<p>Hello</p>" {
		t.Fatalf("postmark: %+v", got)
	}
	if err := (&Resend{Key: "re", From: "login@x.test", BaseURL: srv.URL}).Send(ctx, m); err != nil {
		t.Fatal(err)
	}
	if got.path != "/emails" || got.auth != "Bearer re" || got.body["to"].([]any)[0] != "ann@example.com" || got.body["subject"] != "Hi" || got.body["html"] != "<p>Hello</p>" {
		t.Fatalf("resend: %+v", got)
	}
	if err := (&Cloudflare{Token: "cf", AccountID: "acc", From: "Outflare <login@x.test>", BaseURL: srv.URL}).Send(ctx, m); err != nil {
		t.Fatal(err)
	}
	from, _ := got.body["from"].(map[string]any)
	if got.path != "/accounts/acc/email/sending/send" || got.auth != "Bearer cf" || got.body["to"] != "ann@example.com" ||
		from["address"] != "login@x.test" || from["name"] != "Outflare" || got.body["text"] != "Hello" || got.body["html"] != "<p>Hello</p>" {
		t.Fatalf("cloudflare: %+v", got)
	}
	if err := (&Cloudflare{Token: "cf", AccountID: "acc", From: "login@x.test", BaseURL: srv.URL}).Send(ctx, Message{To: "gone@example.com"}); err == nil {
		t.Fatal("want an error for a permanent bounce")
	}
	if err := (&Postmark{Token: "bad", From: "x@x.test", BaseURL: srv.URL}).Send(ctx, m); err == nil {
		t.Fatal("want an error for a refused send")
	}
}

func TestFromEnv(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tc := range []struct {
		from, postmark, resend, cloudflare, account, want string
		fails                                             bool
	}{
		{want: "log"},
		{postmark: "pm", fails: true}, // no EMAIL_FROM
		{from: "a@x.test", postmark: "pm", want: "postmark"},
		{from: "a@x.test", resend: "re", want: "resend"},
		{from: "a@x.test", postmark: "pm", resend: "re", fails: true},
		{from: "a@x.test", cloudflare: "cf", account: "acc", want: "cloudflare"},
		{from: "a@x.test", cloudflare: "cf", fails: true}, // no CLOUDFLARE_ACCOUNT_ID
		{from: "a@x.test", resend: "re", cloudflare: "cf", account: "acc", fails: true},
	} {
		t.Setenv("EMAIL_FROM", tc.from)
		t.Setenv("POSTMARK_SERVER_TOKEN", tc.postmark)
		t.Setenv("RESEND_API_KEY", tc.resend)
		t.Setenv("CLOUDFLARE_EMAIL_API_TOKEN", tc.cloudflare)
		t.Setenv("CLOUDFLARE_ACCOUNT_ID", tc.account)
		s, err := FromEnv(log)
		if tc.fails != (err != nil) || (err == nil && s.Name() != tc.want) {
			t.Fatalf("%+v: got %v, %v", tc, s, err)
		}
	}
}
