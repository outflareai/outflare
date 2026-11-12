package crawl

import "strings"

// siteLocales are language (and language-region) first path segments that
// usually mean a locale, not a product page, so audit templates can be
// rolled up across them.
var siteLocales = map[string]bool{
	"ar": true, "bg": true, "bn": true, "ca": true, "cs": true, "da": true,
	"de": true, "el": true, "en": true, "es": true, "et": true, "fa": true,
	"fi": true, "fr": true, "he": true, "hi": true, "hr": true, "hu": true,
	"id": true, "it": true, "ja": true, "ko": true, "lt": true, "lv": true,
	"ms": true, "nb": true, "nl": true, "nn": true, "pl": true, "pt": true,
	"ro": true, "ru": true, "sk": true, "sl": true, "sr": true, "sv": true,
	"th": true, "tr": true, "uk": true, "vi": true, "zh": true,
}

// LocaleSegment reports whether a path or template segment is a locale.
func LocaleSegment(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if siteLocales[s] {
		return true
	}
	lang, rest, ok := strings.Cut(s, "-")
	return ok && siteLocales[lang] && len(rest) >= 2 && len(rest) <= 4
}

// FoldLocaleTemplate turns /de/products/* into /{locale}/products/* so
// the same template in every locale can be one row. ok is false when the
// first segment is not a locale.
func FoldLocaleTemplate(template string) (folded, locale string, ok bool) {
	path := strings.Trim(template, "/")
	if path == "" {
		return template, "", false
	}
	segs := strings.Split(path, "/")
	if segs[0] == "*" || segs[0] == "{locale}" || !LocaleSegment(segs[0]) {
		return template, "", false
	}
	locale = strings.ToLower(segs[0])
	segs[0] = "{locale}"
	return "/" + strings.Join(segs, "/"), locale, true
}

// LocalePrefix is the first path segment when it is a locale, with a
// leading slash, such as /de.
func LocalePrefix(locale string) string {
	if !LocaleSegment(locale) {
		return ""
	}
	return "/" + strings.ToLower(locale)
}

// IsFoldedLocaleTemplate reports whether template is one FoldLocaleTemplate
// would emit, such as /{locale}/products/*.
func IsFoldedLocaleTemplate(template string) bool {
	return strings.HasPrefix(strings.Trim(template, "/"), "{locale}") || strings.HasPrefix(template, "/{locale}/") || template == "/{locale}"
}
