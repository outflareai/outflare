package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// A workspace's tracking domain (links.acme-com.example) points at this server so
// click, open, unsubscribe and campaign redirect links carry the customer's own
// domain. On that host the server answers only those links (and partner short
// links): the rest of the app, sign-in above all, never appears under a
// customer's domain.

// trackingPaths are what a tracking domain serves.
var trackingPaths = []string{"/c/", "/o/", "/u/", "/r/", "/l/"}

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
	// program is the partner program whose portal the host serves: its
	// subdomain of the partners domain, or a verified domain tied to it.
	program string
	// centre is the slug of the help centre a verified domain tied to it
	// serves.
	centre string
	until  time.Time
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
		e, err := s.hostEntry(r, host)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		tracking := e.tracking
		if e.centre != "" {
			// A help centre's domain serves only the help centre, at its root.
			r2 := r.Clone(context.WithValue(r.Context(), ctxHelpHostKey{}, true))
			r2.URL.Path = "/h/" + e.centre + r.URL.Path
			r2.URL.RawPath = ""
			next.ServeHTTP(w, r2)
			return
		}
		if e.program != "" {
			// A partner portal's host serves only its portal, at its root.
			r2 := r.Clone(context.WithValue(r.Context(), ctxPortalHostKey{}, true))
			r2.URL.Path = "/p/" + e.program + r.URL.Path
			r2.URL.RawPath = ""
			next.ServeHTTP(w, r2)
			return
		}
		if tracking {
			writeError(w, r, http.StatusNotFound, "not_found", "this address only serves email links")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type ctxPortalHostKey struct{}

// onPortalHost reports whether the request came to a partner portal's own
// host, so its pages live at / rather than /p/<program>/.
func onPortalHost(r *http.Request) bool {
	v, _ := r.Context().Value(ctxPortalHostKey{}).(bool)
	return v
}

// isTrackingHost reports whether host is any workspace's tracking domain,
// verified or not, remembering the answer for a minute.
func (s *Server) isTrackingHost(r *http.Request, host string) (bool, error) {
	e, err := s.hostEntry(r, host)
	return e.tracking, err
}

// hostEntry looks up what host serves: a tracking domain (verified or
// not), a partner program's portal (open or closed: a closed one still
// serves its referral widget and short links), a help centre, or neither. The answer is kept for a
// minute.
func (s *Server) hostEntry(r *http.Request, host string) (trackingHostEntry, error) {
	c := &s.trackingHosts
	now := time.Now()
	c.mu.Lock()
	e, ok := c.hosts[host]
	c.mu.Unlock()
	if ok && now.Before(e.until) {
		return e, nil
	}
	e = trackingHostEntry{}
	var program, centre *string
	err := s.pool.QueryRow(r.Context(), `SELECT true, CASE WHEN d.status = 'verified' THEN p.id::text END, CASE WHEN d.status = 'verified' THEN h.slug END
		FROM tracking_domains d LEFT JOIN partner_programs p ON p.portal_domain_id = d.id LEFT JOIN help_centres h ON h.domain_id = d.id
		WHERE d.domain = $1`, host).Scan(&e.tracking, &program, &centre)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return e, err
	}
	if program != nil {
		e.program = *program
	}
	if centre != nil {
		e.centre = *centre
	}
	if sub, ok := strings.CutSuffix(host, "."+s.partnersDomain); ok && s.partnersDomain != "" && !e.tracking && !strings.Contains(sub, ".") {
		err := s.pool.QueryRow(r.Context(), `SELECT id::text FROM partner_programs WHERE portal_subdomain = $1`, sub).Scan(&e.program)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return e, err
		}
		// An unknown subdomain of the partners domain is never the app.
		e.tracking = true
	}
	e.until = now.Add(trackingHostTTL)
	c.mu.Lock()
	if c.hosts == nil || len(c.hosts) >= trackingHostMax {
		c.hosts = map[string]trackingHostEntry{}
	}
	c.hosts[host] = e
	c.mu.Unlock()
	return e, nil
}
