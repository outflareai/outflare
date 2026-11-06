package connectors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// routeAll sends every request, whatever its host, to h.
type routeAll struct{ h http.Handler }

func (r routeAll) RoundTrip(req *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	r.h.ServeHTTP(w, req)
	res := w.Result()
	res.Request = req
	return res, nil
}

func TestGoogleRedirects(t *testing.T) {
	if !IsGoogleRedirect("https://google.com/goto?url=CAES123") || !IsGoogleRedirect("https://www.google.co.uk/url?q=https://acme.example/") {
		t.Fatal("redirects not recognised")
	}
	if IsGoogleRedirect("https://acme.example/goto?url=x") || IsGoogleRedirect("https://google.com/search?q=acme") {
		t.Fatal("false redirect")
	}
	if got := UnwrapGoogleRedirect("https://www.google.com/url?q=https://acme.example/review&sa=U"); got != "https://acme.example/review" {
		t.Fatalf("unwrap: %q", got)
	}
	c := &Client{HTTP: &http.Client{Transport: routeAll{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("url") {
		case "LOC":
			w.Header().Set("Location", "https://reviews.example/acme-review")
			w.WriteHeader(http.StatusFound)
		case "META":
			_, _ = w.Write([]byte(`<html><head><meta http-equiv="refresh" content="0;url='https://blog.example/acme?a=1&amp;b=2'"></head></html>`))
		default:
			_, _ = w.Write([]byte(`<html>nothing</html>`))
		}
	})}}}
	got := ResolveGoogleRedirects(context.Background(), c, []string{
		"https://acme.example/page",
		"https://google.com/goto?url=LOC",
		"https://google.com/goto?url=META",
		"https://google.com/goto?url=NONE",
	})
	want := []string{"https://acme.example/page", "https://reviews.example/acme-review", "https://blog.example/acme?a=1&b=2", ""}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("resolved %q", got)
	}
}
