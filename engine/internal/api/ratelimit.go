package api

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/outflareai/outflare/engine/internal/metrics"
)

// rateLimiter caps requests per API token per minute, in fixed windows,
// and reports where a caller stands in the IETF RateLimit and
// RateLimit-Policy headers on every answer, so clients can slow down
// before they are refused. Counts are per server process.
// sessionLimitFactor is how many times a token's limit a signed-in browser
// session may use: the web app makes many calls a page.
const sessionLimitFactor = 4

type rateLimiter struct {
	name   string // the limit's name in outflare_http_rate_limited_total
	who    string // what is counted, in the 429's message; "token" when empty
	limit  int    // requests per window; 0 turns limiting off
	window time.Duration
	mu     sync.Mutex
	start  time.Time
	counts map[string]int
}

// over reports whether key has used up its requests in this window,
// without counting one.
func (l *rateLimiter) over(key string) bool {
	if l == nil || l.limit <= 0 {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.counts != nil && time.Since(l.start) < l.window && l.counts[key] >= l.limit
}

// hit counts one for key and reports whether it is still within the
// limit, without answering or setting headers: for quiet limits on what
// a public page records.
func (l *rateLimiter) hit(key string) bool {
	if l == nil || l.limit <= 0 {
		return true
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.counts == nil || now.Sub(l.start) >= l.window {
		l.start, l.counts = now.Truncate(l.window), map[string]int{}
	}
	l.counts[key]++
	return l.counts[key] <= l.limit
}

// allow counts a request for key and sets the headers. It reports false,
// having answered 429, when the caller is over its limit.
func (l *rateLimiter) allow(w http.ResponseWriter, r *http.Request, key string) bool {
	if l == nil || l.limit <= 0 {
		return true
	}
	now := time.Now()
	l.mu.Lock()
	if l.counts == nil || now.Sub(l.start) >= l.window {
		l.start, l.counts = now.Truncate(l.window), map[string]int{}
	}
	l.counts[key]++
	n := l.counts[key]
	reset := max(1, int(l.start.Add(l.window).Sub(now).Seconds()+0.999))
	l.mu.Unlock()

	who := l.who
	if who == "" {
		who = "token"
	}
	h := w.Header()
	h.Set("RateLimit-Policy", fmt.Sprintf(`"%s";q=%d;w=%d`, who, l.limit, int(l.window.Seconds())))
	h.Set("RateLimit", fmt.Sprintf(`"%s";r=%d;t=%d`, who, max(0, l.limit-n), reset))
	if n <= l.limit {
		return true
	}
	h.Set("Retry-After", strconv.Itoa(reset))
	metrics.RateLimited(l.name)
	writeError(w, r, http.StatusTooManyRequests, "rate_limited",
		fmt.Sprintf("this %s made more than %d requests in a minute; wait %d seconds (Retry-After) and slow down", who, l.limit, reset))
	return false
}
