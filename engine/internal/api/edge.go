package api

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/outflareai/outflare/engine/internal/tracking"
)

// An edge in front of the server can serve workspaces' tracking domains
// when the server's own host only answers to names it knows (an edge proxy
// in front of a host that routes by name, holding the domains'
// certificates). The edge forwards each request
// to the server's own address and says which tracking domain and visitor
// address it saw, signed with the shared EdgeSecret. Without the secret,
// or with a wrong one, the headers are dropped and ignored.
const (
	edgeSecretHeader = "X-Outflare-Edge-Secret"
	edgeHostHeader   = "X-Outflare-Edge-Host"
	edgeIPHeader     = "X-Outflare-Edge-IP"
)

func (s *Server) fromEdge(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secret, host, ip := r.Header.Get(edgeSecretHeader), r.Header.Get(edgeHostHeader), r.Header.Get(edgeIPHeader)
		r.Header.Del(edgeSecretHeader)
		r.Header.Del(edgeHostHeader)
		r.Header.Del(edgeIPHeader)
		if s.edgeSecret == "" || secret == "" || subtle.ConstantTimeCompare([]byte(secret), []byte(s.edgeSecret)) != 1 {
			next.ServeHTTP(w, r)
			return
		}
		domain, ok := tracking.ValidDomain(host)
		addr, err := netip.ParseAddr(strings.TrimSpace(ip))
		if !ok || err != nil || s.pool == nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "the edge sent no usable domain or visitor address")
			return
		}
		// The edge serves tracking domains and nothing else.
		if t, err := s.isTrackingHost(r, domain); err != nil {
			s.internal(w, r, err)
			return
		} else if !t {
			writeError(w, r, http.StatusNotFound, "not_found", "this address only serves email links")
			return
		}
		r.Host = domain
		r.RemoteAddr = net.JoinHostPort(addr.Unmap().String(), "0")
		if clientIPHeader != "" {
			r.Header.Set(clientIPHeader, addr.Unmap().String())
		}
		next.ServeHTTP(w, r)
	})
}
