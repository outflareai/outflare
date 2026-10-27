// Package logos serves each connector's logo as an SVG: the company's own
// mark where we ship one (svg/, sources in SOURCES.md), otherwise a
// monogram drawn from the connector's name, so every connector has one.
//
// A logo can have a dark-theme variant, svg/{id}.dark.svg, for marks that
// are black and would vanish on a dark background.
//
// Connectors added as manifests keep their logo beside the manifest:
// integrations/connectors/{id}/logo.svg (and logo.dark.svg).
package logos

import (
	"embed"
	"fmt"
	"hash/fnv"
	"io/fs"
	"strings"
	"unicode"

	"github.com/outflareai/outflare/integrations"
)

//go:embed svg/*.svg
var files embed.FS

// Connectors that share another connector's mark.
var aliases = map[string]string{
	"posthog_cloud":            "posthog",
	"hubspot_private_app":      "hubspot",
	"cloudflare_web_analytics": "cloudflare",
	"vercel_analytics":         "vercel",
	"vercel_ai_gateway":        "vercel",
	"zoom_scheduler":           "zoom",
	"cheapverifier":            "cheapinboxes",
}

// Get returns the logo for a connector and whether it is the company's
// own mark (false: a monogram). dark picks the dark-theme variant.
func Get(id, name string, dark bool) ([]byte, bool) {
	file := id
	if a, ok := aliases[id]; ok {
		file = a
	}
	if dark {
		if b, err := files.ReadFile("svg/" + file + ".dark.svg"); err == nil {
			return b, true
		}
	}
	if b, err := files.ReadFile("svg/" + file + ".svg"); err == nil {
		return b, true
	}
	if !strings.ContainsAny(file, "./") {
		dir := "connectors/" + file + "/"
		if dark {
			if b, err := fs.ReadFile(integrations.FS, dir+"logo.dark.svg"); err == nil {
				return b, true
			}
		}
		if b, err := fs.ReadFile(integrations.FS, dir+"logo.svg"); err == nil {
			return b, true
		}
	}
	if id == "builtin" {
		return []byte(builtin), false
	}
	return Monogram(id, name), false
}

// Has reports whether the connector has its company's own mark.
func Has(id string) bool {
	_, ok := Get(id, "", false)
	return ok
}

// builtin is the free built-in checks: a check mark, no company.
const builtin = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="14" fill="#525252"/><path d="M20 33l8 8 16-18" fill="none" stroke="#fff" stroke-width="5" stroke-linecap="round" stroke-linejoin="round"/></svg>`

// Muted tones that hold white text in both themes.
var tones = []string{"#4f46e5", "#0f766e", "#b45309", "#be123c", "#7c3aed", "#0369a1", "#4d7c0f", "#a21caf", "#c2410c", "#334155"}

// Monogram draws a connector's initials on a rounded square, the same
// size as a company mark. The tone comes from the id, so it never changes.
func Monogram(id, name string) []byte {
	h := fnv.New32a()
	h.Write([]byte(id))
	tone := tones[h.Sum32()%uint32(len(tones))]
	letters := Initials(name)
	if letters == "" {
		letters = Initials(id)
	}
	size := 30
	if len([]rune(letters)) > 1 {
		size = 25
	}
	return []byte(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="14" fill="%s"/><text x="32" y="32" dy=".35em" text-anchor="middle" fill="#fff" font-family="Geist, ui-sans-serif, system-ui, -apple-system, 'Segoe UI', sans-serif" font-size="%d" font-weight="600" letter-spacing="-.5">%s</text></svg>`, tone, size, letters))
}

// Initials: the first letter of up to two words, ignoring anything in
// brackets ("Salesforce (sandbox)" is "S", "People Data Labs" "PD").
// One word with capitals inside gives its first two ("EmailBison" is "EB").
func Initials(name string) string {
	if i := strings.IndexByte(name, '('); i >= 0 {
		name = name[:i]
	}
	words := strings.FieldsFunc(name, func(r rune) bool { return unicode.IsSpace(r) || r == '-' || r == '_' })
	var out []rune
	if len(words) == 1 {
		for i, r := range []rune(words[0]) {
			if i == 0 || unicode.IsUpper(r) {
				out = append(out, unicode.ToUpper(r))
			}
		}
	} else {
		for _, w := range words {
			for _, r := range w {
				if unicode.IsLetter(r) || unicode.IsDigit(r) {
					out = append(out, unicode.ToUpper(r))
					break
				}
			}
		}
	}
	if len(out) > 2 {
		out = out[:2]
	}
	return string(out)
}
