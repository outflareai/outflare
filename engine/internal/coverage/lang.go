package coverage

import (
	"net/url"
	"regexp"
	"strings"
)

// Many sites publish the same page in several languages: /fr/isp-proxies,
// fr.example.com/isp-proxies or ?lang=fr. Presence suggests the English
// page, which is the one worth pitching, and treats the translations as
// copies of it.

// languages are the language codes sites commonly put in a path or a
// subdomain. Codes that are also common words in paths (id, no, my, so)
// are left out unless they carry a region (id-id).
var languages = map[string]bool{
	"ar": true, "bg": true, "bn": true, "ca": true, "cs": true, "da": true, "de": true, "el": true, "en": true,
	"es": true, "et": true, "fa": true, "fi": true, "fr": true, "he": true, "hi": true, "hr": true, "hu": true,
	"it": true, "ja": true, "ko": true, "lt": true, "lv": true, "nb": true, "nl": true, "pl": true, "pt": true,
	"ro": true, "ru": true, "sk": true, "sl": true, "sr": true, "sv": true, "th": true, "tr": true, "uk": true,
	"vi": true, "zh": true,
}

// regional are codes that count only with a region: id-id, no-no, ms-my.
var regional = map[string]bool{"id": true, "no": true, "ms": true}

var langCode = regexp.MustCompile(`^([a-z]{2})(?:[-_]([a-z]{2}|[a-z]{4}|[0-9]{3}))?$`)

// language returns the language a path segment or subdomain names, or "".
func language(s string) string {
	m := langCode.FindStringSubmatch(strings.ToLower(s))
	if m == nil {
		return ""
	}
	if languages[m[1]] || (regional[m[1]] && m[2] != "") {
		return m[1]
	}
	return ""
}

var langParams = []string{"lang", "language", "locale", "hl", "lng"}

// EnglishURL returns the English version of a page's address, and the
// language the address names ("" when it names none). The English version
// drops the language from the first path segment, a language subdomain
// and a language query parameter, so every translation of a page has the
// same English address.
func EnglishURL(raw string) (string, string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return raw, ""
	}
	lang := ""
	labels := strings.Split(u.Hostname(), ".")
	if len(labels) >= 3 {
		if l := language(labels[0]); l != "" {
			lang = l
			host := strings.Join(labels[1:], ".")
			if p := u.Port(); p != "" {
				host += ":" + p
			}
			u.Host = host
		}
	}
	if segs := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 2); len(segs) > 0 && segs[0] != "" {
		if l := language(segs[0]); l != "" {
			lang = l
			u.Path = "/"
			if len(segs) == 2 {
				u.Path += segs[1]
			}
			u.RawPath = ""
		}
	}
	if u.RawQuery != "" {
		q := u.Query()
		for _, k := range langParams {
			if v := q.Get(k); v != "" {
				if l := language(v); l != "" {
					lang = l
					q.Del(k)
				}
			}
		}
		u.RawQuery = q.Encode()
	}
	if lang == "en" {
		lang = ""
	}
	return u.String(), lang
}
