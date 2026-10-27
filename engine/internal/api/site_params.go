package api

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// A page's query string is kept whole, so reports can filter on any
// parameter, except what could sign someone in or name them. Those are
// dropped before anything is stored, whatever the site's settings say.

const (
	// maxExcludedParams a site can list, each at most maxParamName long.
	maxExcludedParams = 50
	maxParamName      = 100
)

var (
	// Parameter names, lowercased with everything but letters and digits
	// taken out, that are always dropped.
	sensitiveParams = []string{
		"code", "authcode", "oauthcode", "verificationcode", "otp", "pin", "state", "nonce", "ticket",
		"auth", "authorization", "jwt", "sig", "signature", "hash", "key", "apikey", "accesskey",
		"pass", "pwd", "passwd", "password", "secret", "clientsecret", "sid", "session", "sessionid",
		"email", "mail", "phone", "tel", "ssn",
	}
	// And any name containing one of these.
	sensitiveParts = []string{"token", "password", "passwd", "secret", "session", "email", "signature", "credential", "apikey", "accesskey", "oauth"}
	// Outflare's own, taken off the address by the script.
	ownParams = []string{"_ofa", "ofvisit"}

	emailValue = regexp.MustCompile(`[^@\s/?#]+@[^@\s/?#]+\.[A-Za-z]{2,}`)
	jwtValue   = regexp.MustCompile(`^eyJ[\w-]+\.[\w-]+\.[\w-]*$`)
	paramName  = regexp.MustCompile(`^[A-Za-z0-9_.\-\[\]]+\*?$`)
)

// sensitiveParam reports whether a parameter is always dropped.
func sensitiveParam(name string) bool {
	n := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, strings.ToLower(name))
	if slices.Contains(sensitiveParams, n) || slices.Contains(ownParams, strings.ToLower(name)) {
		return true
	}
	for _, p := range sensitiveParts {
		if strings.Contains(n, p) {
			return true
		}
	}
	return false
}

// excludedParam reports whether the site listed a parameter to leave out:
// by its exact name, any case, or by a prefix ending in * (fb_*).
func excludedParam(name string, excluded []string) bool {
	name = strings.ToLower(name)
	for _, e := range excluded {
		e = strings.ToLower(e)
		if p, ok := strings.CutSuffix(e, "*"); ok && strings.HasPrefix(name, p) || name == e {
			return true
		}
	}
	return false
}

// keptParam reports whether a parameter of a page's address is kept: not
// sensitive, not left out by the site, and with no value that is an email
// address or a signed token.
func keptParam(name string, values []string, excluded []string) bool {
	if name == "" || sensitiveParam(name) || excludedParam(name, excluded) {
		return false
	}
	for _, v := range values {
		if emailValue.MatchString(v) || jwtValue.MatchString(v) {
			return false
		}
	}
	return true
}

// cleanQuery keeps the parameters keptParam allows, sorted by name.
func cleanQuery(raw string, excluded []string) string {
	q, err := url.ParseQuery(strings.TrimPrefix(raw, "?"))
	if err != nil {
		return ""
	}
	for k, v := range q {
		if !keptParam(k, v, excluded) {
			delete(q, k)
		}
	}
	return q.Encode()
}

// cleanExcludedParams checks the names a site leaves out.
func cleanExcludedParams(in []string) ([]string, error) {
	out := []string{}
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if len(p) > maxParamName || !paramName.MatchString(p) {
			return nil, errInvalid("excluded_params are query parameter names such as plan or fb_* (a trailing * leaves out every name that starts with it)")
		}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	if len(out) > maxExcludedParams {
		return nil, errInvalid("a site can leave out at most 50 query parameters")
	}
	return out, nil
}
