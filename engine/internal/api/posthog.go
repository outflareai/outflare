package api

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// posthogProxy forwards the web app's product analytics from /ingest/ on
// this server to PostHog. Same-origin requests aren't dropped by ad
// blockers, and the browser never talks to a third party directly.
type posthogProxy struct {
	key    string
	api    *url.URL // events: https://us.i.posthog.com
	assets *url.URL // the JS library: https://us-assets.i.posthog.com
	ui     string   // where people view the data: https://us.posthog.com
	proxy  *httputil.ReverseProxy
}

func newPostHogProxy(key, host string) *posthogProxy {
	if key == "" {
		return nil
	}
	api, err := url.Parse(strings.TrimRight(host, "/"))
	if err != nil || api.Host == "" {
		return nil
	}
	assets := *api
	ui := "https://" + api.Host
	// PostHog Cloud serves its library from a sibling host and its app
	// without the "i." part. Self-hosted PostHog serves all three itself.
	if region, ok := strings.CutSuffix(api.Host, ".i.posthog.com"); ok {
		assets.Host = region + "-assets.i.posthog.com"
		ui = "https://" + region + ".posthog.com"
	}
	p := &posthogProxy{key: key, api: api, assets: &assets, ui: ui}
	p.proxy = &httputil.ReverseProxy{Rewrite: p.rewrite}
	return p
}

func (p *posthogProxy) rewrite(r *httputil.ProxyRequest) {
	target := p.api
	if strings.HasPrefix(r.In.URL.Path, "/ingest/static/") || strings.HasPrefix(r.In.URL.Path, "/ingest/array/") {
		target = p.assets
	}
	r.SetURL(target)
	r.Out.URL.Path = strings.TrimPrefix(r.In.URL.Path, "/ingest")
	r.Out.URL.RawPath = ""
	r.Out.Host = target.Host
	// PostHog needs only the visitor's address (for country); never this
	// server's session cookie.
	r.Out.Header.Del("Cookie")
	r.Out.Header.Del("Authorization")
	r.Out.Header.Set("X-Forwarded-For", ClientIP(r.In))
}

func (p *posthogProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.proxy.ServeHTTP(w, r)
}

// meta is what the web app needs to start PostHog, in /v1/meta.
func (p *posthogProxy) meta() map[string]string {
	return map[string]string{"provider": "posthog", "key": p.key, "api_host": "/ingest", "ui_host": p.ui}
}
