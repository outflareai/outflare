package api

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// gaIDRE is a Google Analytics 4 measurement id.
var gaIDRE = regexp.MustCompile(`^G-[A-Z0-9]{4,15}$`)

// siteOriginOK reports whether site key k takes requests from origin: one
// of its origins, or the partner portal that uses it for its analytics
// (on the portal's own host, or at /p/<program>/ on this server), so a
// portal keeps working when its address changes.
func (s *Server) siteOriginOK(r *http.Request, k siteKeyRow, origin string) bool {
	if originAllowed(k.origins, origin) {
		return true
	}
	u, err := url.Parse(strings.ToLower(strings.TrimSpace(origin)))
	if err != nil || u.Hostname() == "" || s.pool == nil {
		return false
	}
	ctx := r.Context()
	own := ""
	if p, err := url.Parse(s.publicURL); err == nil {
		own = strings.ToLower(p.Hostname())
	}
	program := ""
	if host := strings.TrimSuffix(u.Hostname(), "."); host != own {
		e, err := s.hostEntry(r, host)
		if err != nil || e.program == "" {
			return false
		}
		program = e.program
	}
	var ok bool
	err = s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM partner_programs WHERE workspace_id = $1 AND ($2 = '' OR id::text = $2)
		AND portal_enabled AND portal->>'site_key_id' = $3)`, k.workspaceID, program, k.id).Scan(&ok)
	return err == nil && ok
}

// portalAnalytics is what the portal page loads for the workspace's own
// analytics: its website script with the chosen site key and, held back
// by that script until the visitor allows analytics, Google Analytics.
func (s *Server) portalAnalytics(r *http.Request, program string) (head, scriptSrc, connectSrc string) {
	var key, ga string
	err := s.pool.QueryRow(r.Context(), `SELECT k.key, COALESCE(p.portal->>'ga_measurement_id', '')
		FROM partner_programs p JOIN site_keys k ON k.id::text = p.portal->>'site_key_id' AND k.workspace_id = p.workspace_id AND k.revoked_at IS NULL
		WHERE p.id::text = $1`, program).Scan(&key, &ga)
	if err != nil {
		return "", "", ""
	}
	base := strings.TrimRight(s.publicURL, "/")
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		origin := u.Scheme + "://" + u.Host
		scriptSrc, connectSrc = " "+origin, " "+origin
	}
	head = `<script async src="` + html.EscapeString(base) + `/site.js" data-key="` + html.EscapeString(key) + `"></script>`
	if gaIDRE.MatchString(ga) {
		head += `<script type="text/plain" data-category="analytics" async src="https://www.googletagmanager.com/gtag/js?id=` + ga + `"></script>` +
			`<script type="text/plain" data-category="analytics">window.dataLayer=window.dataLayer||[];function gtag(){dataLayer.push(arguments);}gtag("js",new Date());gtag("config","` + ga + `");</script>`
		scriptSrc += " https://www.googletagmanager.com"
		connectSrc += " https://*.google-analytics.com https://*.analytics.google.com https://www.googletagmanager.com"
	}
	return head, scriptSrc, connectSrc
}
