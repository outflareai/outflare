package connectors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A rate limit answer (HTTP 429) from a service whose 429 is never the
// account's quota is busy: waited out, never a reason to stop for a
// person. Its Retry-After is kept. Out of credit (402) is still quota.
func TestDoJSONRateLimitedIsBusyNotQuota(t *testing.T) {
	status := http.StatusTooManyRequests
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(status)
	}))
	defer srv.Close()
	cl := &Client{HTTP: srv.Client()}
	call := func() error {
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		_, err := DoJSONRateLimited(context.Background(), cl, "Example", req, nil, 0)
		return err
	}
	err := call()
	if KindOf(err) == ErrQuota || !IsThrottled(err) {
		t.Fatalf("429: kind %s, throttled %v: want busy, not quota (%v)", KindOf(err), IsThrottled(err), err)
	}
	if wait, ok := IsBusy(err); !ok || wait != 7*time.Second {
		t.Fatalf("429: wait %s, busy %v: want 7s from Retry-After", wait, ok)
	}
	status = http.StatusPaymentRequired
	if err := call(); KindOf(err) != ErrQuota {
		t.Fatalf("402: kind %s, want quota", KindOf(err))
	}
	// DoJSON keeps 429 as quota for the services that mean it so.
	status = http.StatusTooManyRequests
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	if _, err := DoJSON(context.Background(), cl, "Example", req, nil); KindOf(err) != ErrQuota {
		t.Fatalf("DoJSON 429: kind %s, want quota", KindOf(err))
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	for v, want := range map[string]time.Duration{
		"":                              0,
		"30":                            30 * time.Second,
		"-4":                            0,
		"soon":                          0,
		"99999":                         maxRetryAfter,
		"Thu, 19 Nov 2026 12:01:00 GMT": time.Minute,
	} {
		h := http.Header{}
		if v != "" {
			h.Set("Retry-After", v)
		}
		if got := RetryAfter(h, now); got != want {
			t.Errorf("Retry-After %q = %s, want %s", v, got, want)
		}
	}
}
