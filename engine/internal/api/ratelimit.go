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
type rateLimiter struct {
	name   string // the limit's name in outflare_http_rate_limited_total
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

	h := w.Header()
	h.Set("RateLimit-Policy", fmt.Sprintf(`"token";q=%d;w=%d`, l.limit, int(l.window.Seconds())))
	h.Set("RateLimit", fmt.Sprintf(`"token";r=%d;t=%d`, max(0, l.limit-n), reset))
	if n <= l.limit {
		return true
	}
	h.Set("Retry-After", strconv.Itoa(reset))
	metrics.RateLimited(l.name)
	writeError(w, r, http.StatusTooManyRequests, "rate_limited",
		fmt.Sprintf("this token made more than %d requests in a minute; wait %d seconds (Retry-After) and slow down", l.limit, reset))
	return false
}
