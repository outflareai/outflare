package connectors

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A call that may take long waits as long as it asks, even when the
// client's own timeout is shorter; other calls keep the client's.
func TestDoWithinWaitsLonger(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
	}))
	defer srv.Close()
	cl := &Client{HTTP: &http.Client{Timeout: 50 * time.Millisecond}}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	if _, err := cl.Do(req); err == nil {
		t.Fatal("the client's own timeout should apply")
	}
	req, _ = http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := cl.DoWithin(req, time.Second)
	if err != nil {
		t.Fatalf("a longer wait was cut short: %v", err)
	}
	resp.Body.Close()
}
