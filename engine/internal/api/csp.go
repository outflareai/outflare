package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The web app's script policy. The app's pages are sent with a
// Content-Security-Policy-Report-Only header that allows scripts only from
// the server itself, plus the inline scripts in the page as it was built
// (by hash). Nothing is blocked yet: a browser that sees any other script
// run, such as one injected through a bug in a page, reports it to
// POST /v1/csp-reports, where it is counted and logged. Once the reports
// are quiet the same policy can be enforced.

// cspReportPath is where browsers send what the policy would have blocked.
const cspReportPath = "/v1/csp-reports"

// cspReportMax is the largest report body read. Browsers send one small
// JSON document per violation, or a short batch.
const cspReportMax = 32 << 10

var inlineScript = regexp.MustCompile(`(?is)<script(\s[^>]*)?>(.*?)</script>`)

// scriptPolicy is the report-only policy for one built page: scripts from
// this server, and each inline script the page holds, by its hash.
func scriptPolicy(page []byte) string {
	var b strings.Builder
	b.WriteString("script-src 'self'")
	for _, m := range inlineScript.FindAllSubmatch(page, -1) {
		// A script with a src has no inline body to allow.
		if bytes.Contains(bytes.ToLower(m[1]), []byte("src=")) || len(bytes.TrimSpace(m[2])) == 0 {
			continue
		}
		sum := sha256.Sum256(m[2])
		b.WriteString(" 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'")
	}
	b.WriteString("; object-src 'none'; base-uri 'self'; report-uri " + cspReportPath)
	return b.String()
}

// cspReport is one violation, in either shape browsers send: the older
// {"csp-report": {...}} of report-uri, or an entry of the Reporting API's
// batch ({"type": "csp-violation", "body": {...}}).
type cspReport struct {
	Directive string
	Blocked   string
}

func parseCSPReports(body []byte) []cspReport {
	var old struct {
		Report *struct {
			Effective string `json:"effective-directive"`
			Violated  string `json:"violated-directive"`
			Blocked   string `json:"blocked-uri"`
		} `json:"csp-report"`
	}
	if json.Unmarshal(body, &old) == nil && old.Report != nil {
		d := old.Report.Effective
		if d == "" {
			d = old.Report.Violated
		}
		return []cspReport{{Directive: d, Blocked: old.Report.Blocked}}
	}
	var batch []struct {
		Type string `json:"type"`
		Body struct {
			Effective string `json:"effectiveDirective"`
			Blocked   string `json:"blockedURL"`
		} `json:"body"`
	}
	if json.Unmarshal(body, &batch) != nil {
		return nil
	}
	var out []cspReport
	for _, r := range batch {
		if r.Type == "csp-violation" {
			out = append(out, cspReport{Directive: r.Body.Effective, Blocked: r.Body.Blocked})
		}
	}
	return out
}

// cspDirective keeps the directive to the ones the policy has, so the
// metric's label stays a short list whatever a browser sends.
func cspDirective(d string) string {
	d, _, _ = strings.Cut(strings.TrimSpace(d), " ")
	switch d {
	case "script-src", "script-src-elem", "script-src-attr", "object-src", "base-uri":
		return d
	}
	return "other"
}

// cspSource says what kind of thing was blocked, and for a script loaded
// from another site, its host (for the log only). Browser extensions add
// scripts to every page; they are counted apart so they don't hide the rest.
func cspSource(blocked string, self string) (kind, host string) {
	switch b := strings.ToLower(strings.TrimSpace(blocked)); {
	case b == "inline":
		return "inline", ""
	case b == "eval" || b == "wasm-eval":
		return "eval", ""
	case b == "data" || strings.HasPrefix(b, "data:"):
		return "data", ""
	case b == "blob" || strings.HasPrefix(b, "blob:"):
		return "blob", ""
	case strings.Contains(b, "extension"):
		return "extension", ""
	case b == "" || b == "self":
		return "other", ""
	}
	u, err := url.Parse(blocked)
	if err != nil || u.Host == "" {
		return "other", ""
	}
	if self != "" && strings.EqualFold(u.Host, self) {
		return "self", ""
	}
	return "external", strings.ToLower(u.Host)
}

// reportCSP takes what a browser says the app's script policy would have
// blocked. Public and unauthenticated, as browsers send it without the
// session; each network may send 60 a minute.
func (s *Server) reportCSP(w http.ResponseWriter, r *http.Request) {
	if !s.cspReportLimit.allow(w, r, networkKey(clientIP(r))) {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, cspReportMax))
	if err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "the report is too large")
		return
	}
	reports := parseCSPReports(body)
	if len(reports) == 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send a Content Security Policy violation report")
		return
	}
	if len(reports) > 20 {
		reports = reports[:20]
	}
	ctx := r.Context()
	for _, rep := range reports {
		directive := cspDirective(rep.Directive)
		kind, host := cspSource(rep.Blocked, r.Host)
		metrics.CSPReport(directive, kind)
		// An extension's script is the visitor's own doing, not the app's.
		if kind == "extension" {
			continue
		}
		s.log.WarnContext(ctx, "the web app's script policy would have blocked something",
			"directive", directive, "source", kind, "host", host, "request_id", RequestID(ctx))
		track.Emit(ctx, track.Event{Name: "ui.script_policy_violated", Surface: "web", Outcome: track.Failure, ErrorCode: "script_policy",
			RequestID: RequestID(ctx), Props: map[string]any{"directive": directive, "source": kind}})
	}
	w.WriteHeader(http.StatusNoContent)
}
