package rdap

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegistered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/domain/acme.example":
			_, _ = w.Write([]byte(`{"events":[{"eventAction":"last changed","eventDate":"2024-01-02T00:00:00Z"},{"eventAction":"registration","eventDate":"2011-05-09T14:00:00Z"}]}`))
		case "/domain/nodate.example":
			_, _ = w.Write([]byte(`{"events":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := &Client{BaseURL: srv.URL}
	got, err := c.Registered(context.Background(), "www.Acme.example")
	if err != nil || got.Format("2006-01-02") != "2011-05-09" {
		t.Fatalf("registered: %v %v", got, err)
	}
	for _, host := range []string{"nodate.example", "missing.example"} {
		if _, err := c.Registered(context.Background(), host); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s: %v", host, err)
		}
	}
	if d := Domain("shop.acme.co.uk"); d != "acme.co.uk" {
		t.Fatalf("domain: %s", d)
	}
}
