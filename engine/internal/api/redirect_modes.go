package api

import (
	"context"
	"encoding/base64"
	"errors"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/tracking"
)

// Redirect modes and own domains (plans/ad-tracker.md, T4c). A campaign's
// redirect links send people on with a 302 (the default, and fastest), a
// 307, a meta refresh or a double meta refresh. The meta refreshes leave
// the referrer out: the destination sees no referring site, the second
// even for browsers that ignore the referrer policy, since the page they
// leave is this server's.
//
// Tracker features (destinations, rule-based paths and the modes other than
// 302) work only on the workspace's own verified tracking domains, the
// owner's decision 8: one customer's traffic never shares a domain's
// reputation with another's. On the shared domain a click goes to the link's
// own page with a 302, so printed links keep working.

var redirectModes = []string{"302", "307", "meta", "double_meta"}

// hopTTL is how long a double meta refresh's middle step stays valid.
const hopTTL = 2 * time.Minute

// ownDomain reports whether r arrived on one of ws's verified tracking
// domains, and what that host's entry is.
func (s *Server) ownDomain(r *http.Request, ws string) (trackingHostEntry, bool) {
	if s.pool == nil {
		return trackingHostEntry{}, false
	}
	e, err := s.hostEntry(r, requestHost(r))
	if err != nil {
		return e, false
	}
	return e, e.verified && e.workspace == ws
}

// sendOn sends the person to target in mode.
func (s *Server) sendOn(w http.ResponseWriter, r *http.Request, target, mode string) {
	w.Header().Set("Cache-Control", "no-store")
	switch mode {
	case "307":
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
	case "meta":
		metaRefresh(w, target)
	case "double_meta":
		if s.box == nil {
			metaRefresh(w, target)
			return
		}
		exp := strconv.FormatInt(time.Now().Add(hopTTL).Unix(), 10)
		u := base64.RawURLEncoding.EncodeToString([]byte(target))
		sig := s.box.Sign("redirect_hop", exp+"|"+u)
		metaRefresh(w, "/l/go?u="+u+"&e="+exp+"&s="+url.QueryEscape(sig))
	default:
		http.Redirect(w, r, target, http.StatusFound)
	}
}

// metaRefresh answers with a page that moves on at once and sends no
// referrer.
func metaRefresh(w http.ResponseWriter, target string) {
	t := html.EscapeString(target)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8"><meta name="referrer" content="no-referrer">` +
		`<meta http-equiv="refresh" content="0;url=` + t + `"><title>Redirecting</title></head>` +
		`<body><a href="` + t + `" rel="noreferrer">Continue</a></body></html>`))
}

// redirectHop is a double meta refresh's second step. It sends on only to
// a target this server signed in the last two minutes, so it can't be used
// to send people anywhere else.
func (s *Server) redirectHop(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	u, exp, sig := q.Get("u"), q.Get("e"), q.Get("s")
	n, err := strconv.ParseInt(exp, 10, 64)
	raw, derr := base64.RawURLEncoding.DecodeString(u)
	if s.box == nil || err != nil || derr != nil || time.Now().Unix() > n || !s.box.Verify("redirect_hop", exp+"|"+u, sig) {
		http.Error(w, "This link has expired or was never valid.", http.StatusNotFound)
		return
	}
	target := string(raw)
	if p, err := url.Parse(target); err != nil || (p.Scheme != "https" && p.Scheme != "http") {
		http.Error(w, "This link has expired or was never valid.", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	metaRefresh(w, target)
}

// unknownCode answers a redirect code that doesn't exist: the tracking
// domain's fallback page if it has one, else a not found page.
func (s *Server) unknownCode(w http.ResponseWriter, r *http.Request) {
	if e, err := s.hostEntry(r, requestHost(r)); err == nil && e.verified && e.fallback != "" {
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, e.fallback, http.StatusFound)
		return
	}
	http.Error(w, "This link has expired or was never valid.", http.StatusNotFound)
}

// brandHasOwnDomain reports whether a brand has a verified tracking domain
// its tracker links can run on.
func (s *Server) brandHasOwnDomain(ctx context.Context, ws string, brand *string) (bool, error) {
	base, err := tracking.BaseURL(ctx, s.pool, ws, brand, s.publicURL)
	return base != strings.TrimRight(s.publicURL, "/"), err
}

// API: a tracking domain's fallback page.

func (s *Server) routesRedirectModes(mux *http.ServeMux) {
	mux.HandleFunc("GET /l/go", s.redirectHop)
	mux.HandleFunc("PUT /v1/brands/{brand}/tracking-domains/{id}/fallback", s.onBrand(auth.ScopeMailboxesManage, s.putTrackingDomainFallback))
}

func (s *Server) putTrackingDomainFallback(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var in struct {
		URL string `json:"url"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.URL = strings.TrimSpace(in.URL)
	if in.URL != "" {
		u, err := url.Parse(in.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || len(in.URL) > 2000 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "url is a full address such as https://northpeak.example/, or '' for none")
			return
		}
	}
	ctx := r.Context()
	err := errNotFound
	if validUUID(id) {
		err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `UPDATE tracking_domains SET fallback_url = $4 WHERE workspace_id = $1 AND id = $2 AND brand_id = $3`,
				a.WorkspaceID, id, a.Brand, in.URL)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return errNotFound
			}
			return record(ctx, tx, &a.principal, a.WorkspaceID, "tracking_domain.fallback_set", "tracking_domain", id,
				map[string]any{"brand_id": a.Brand, "fallback": in.URL != ""})
		})
	}
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "tracking domain not found on this brand")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.forgetHosts()
	list, err := s.trackingDomains(ctx, a.WorkspaceID, &id)
	if err != nil || len(list) == 0 {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list[0])
}
