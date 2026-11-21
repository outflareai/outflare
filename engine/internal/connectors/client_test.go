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

// The transport's own wait for response headers (netguard's 30 seconds)
// must not cut short a call that asked to wait longer (Treg holds a call
// for 60 seconds, then answers 202): its headers come after the
// transport's limit, inside the call's. Other calls keep the transport's
// limit, and the transport itself is never changed.
func TestDoWithinOutwaitsTheHeaderTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	tr := &http.Transport{ResponseHeaderTimeout: 50 * time.Millisecond}
	cl := &Client{HTTP: &http.Client{Timeout: 5 * time.Second, Transport: tr}}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	if _, err := cl.Do(req); err == nil {
		t.Fatal("a call with no wait of its own should keep the transport's header timeout")
	}
	for range 2 {
		req, _ = http.NewRequest(http.MethodGet, srv.URL, nil)
		resp, err := cl.DoWithin(req, time.Second)
		if err != nil {
			t.Fatalf("a call allowed a second was cut off by the header timeout: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	}
	if tr.ResponseHeaderTimeout != 50*time.Millisecond {
		t.Fatalf("the shared transport was changed: %v", tr.ResponseHeaderTimeout)
	}
	// A wait shorter than the header timeout still ends the call in time.
	req, _ = http.NewRequest(http.MethodGet, srv.URL, nil)
	if _, err := cl.DoWithin(req, 20*time.Millisecond); err == nil {
		t.Fatal("a call's own short wait should still apply")
	}
}
