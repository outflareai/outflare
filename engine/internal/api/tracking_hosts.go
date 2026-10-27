package api

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// A workspace's tracking domain (links.acme-com.example) points at this server so
// click, open and unsubscribe links carry the customer's own domain. On
// that host the server answers only those links: the rest of the app,
// sign-in above all, never appears under a customer's domain.

// trackingPaths are what a tracking domain serves.
var trackingPaths = []string{"/c/", "/o/", "/u/"}

const (
	trackingHostTTL = time.Minute
	// Hosts remembered at most; the Host header is the sender's to choose.
	trackingHostMax = 1000
)

type trackingHostCache struct {
	mu    sync.Mutex
	hosts map[string]trackingHostEntry
}

type trackingHostEntry struct {
	tracking bool
	until    time.Time
}

func requestHost(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

func (s *Server) trackingHostsOnly(next http.Handler) http.Handler {
	own := ""
	if i := strings.Index(s.publicURL, "://"); i >= 0 {
		own = requestHost(&http.Request{Host: strings.SplitN(s.publicURL[i+3:], "/", 2)[0]})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := requestHost(r)
		if s.pool == nil || host == "" || host == own || r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		for _, p := range trackingPaths {
			if strings.HasPrefix(r.URL.Path, p) {
				next.ServeHTTP(w, r)
				return
			}
		}
		tracking, err := s.isTrackingHost(r, host)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if tracking {
			writeError(w, r, http.StatusNotFound, "not_found", "this address only serves email links")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isTrackingHost reports whether host is any workspace's tracking domain,
// verified or not, remembering the answer for a minute.
func (s *Server) isTrackingHost(r *http.Request, host string) (bool, error) {
	c := &s.trackingHosts
	now := time.Now()
	c.mu.Lock()
	e, ok := c.hosts[host]
	c.mu.Unlock()
	if ok && now.Before(e.until) {
		return e.tracking, nil
	}
	var tracking bool
	if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM tracking_domains WHERE domain = $1)`, host).Scan(&tracking); err != nil {
		return false, err
	}
	c.mu.Lock()
	if c.hosts == nil || len(c.hosts) >= trackingHostMax {
		c.hosts = map[string]trackingHostEntry{}
	}
	c.hosts[host] = trackingHostEntry{tracking: tracking, until: now.Add(trackingHostTTL)}
	c.mu.Unlock()
	return tracking, nil
}
