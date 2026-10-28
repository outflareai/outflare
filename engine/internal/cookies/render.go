package cookies

import (
	"fmt"
	"html"
	"strings"
	"time"
)

// What each category is, as a policy explains it. The consent banner's
// own words for the categories say the same.
var categoryText = map[string][2]string{
	Necessary:   {"Necessary", "Needed for the site to work, such as keeping you signed in and remembering your cookie choices. They can't be turned off."},
	Analytics:   {"Analytics", "Help us understand how the site is used, so we can improve it. Used only if you allow them."},
	Marketing:   {"Marketing", "Used to show and measure ads, here and on other sites. Used only if you allow them."},
	Preferences: {"Preferences", "Remember settings and power features such as chat. Used only if you allow them."},
}

var kindText = map[string]string{
	KindCookie: "Cookie", KindLocalStorage: "Local storage", KindSessionStorage: "Session storage",
	KindScript: "Script", KindIframe: "Embed", KindPixel: "Pixel",
}

// Version is a published policy with its number and date.
type Version struct {
	Policy
	Version   int
	Published time.Time
}

func (v Version) intro() string {
	return fmt.Sprintf("This page lists the cookies, browser storage and third-party services %s uses, why, and for how long. "+
		"Version %d, updated %s. It is kept up to date automatically from what the site actually uses.",
		v.Site, v.Version, v.Published.UTC().Format("2 January 2006"))
}

func (v Version) title() string { return "Cookie policy for " + v.Site }

func itemPurpose(vd PolicyVendor, it PolicyItem) string {
	if it.Purpose != "" {
		return it.Purpose
	}
	return vd.Purpose
}

func itemRetention(it PolicyItem) string {
	switch {
	case it.Retention != "":
		return it.Retention
	case it.Kind == KindSessionStorage:
		return "Session"
	case it.Kind == KindScript || it.Kind == KindIframe || it.Kind == KindPixel:
		return "-"
	}
	return "Until cleared"
}

func provider(v PolicyVendor) string {
	if v.Company != "" && v.Company != v.Name {
		return v.Name + " (" + v.Company + ")"
	}
	return v.Name
}

// cell escapes text for a Markdown table cell.
func cell(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ", "\r", "").Replace(s)
}

// Markdown is the policy as Markdown.
func (v Version) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n%s\n", v.title(), v.intro())
	for _, c := range v.Categories {
		if len(c.Vendors) == 0 {
			continue
		}
		t := categoryText[c.ID]
		fmt.Fprintf(&b, "\n## %s\n\n%s\n\n| Name | Provider | Type | Purpose | Kept for |\n|---|---|---|---|---|\n", t[0], t[1])
		for _, vd := range c.Vendors {
			for _, it := range vd.Items {
				fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s |\n", cell(it.Name), cell(provider(vd)), kindText[it.Kind], cell(itemPurpose(vd, it)), cell(itemRetention(it)))
			}
		}
		for _, vd := range c.Vendors {
			if vd.PrivacyURL != "" {
				fmt.Fprintf(&b, "\n%s's privacy policy: %s", vd.Name, vd.PrivacyURL)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Text is the policy as plain text.
func (v Version) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n%s\n", strings.ToUpper(v.title()), v.intro())
	for _, c := range v.Categories {
		if len(c.Vendors) == 0 {
			continue
		}
		t := categoryText[c.ID]
		fmt.Fprintf(&b, "\n%s\n%s\n", strings.ToUpper(t[0]), t[1])
		for _, vd := range c.Vendors {
			fmt.Fprintf(&b, "\n%s", provider(vd))
			if vd.PrivacyURL != "" {
				fmt.Fprintf(&b, " (privacy policy: %s)", vd.PrivacyURL)
			}
			b.WriteString("\n")
			for _, it := range vd.Items {
				kept := ""
				if r := itemRetention(it); r != "-" {
					kept = ", kept for " + strings.ToLower(r)
				}
				fmt.Fprintf(&b, "- %s (%s%s): %s\n", it.Name, strings.ToLower(kindText[it.Kind]), kept, itemPurpose(vd, it))
			}
		}
	}
	return b.String()
}

// HTML is the policy as an HTML fragment for a page, with a button that
// opens the site's consent banner (the website script handles it).
func (v Version) HTML() string {
	e := html.EscapeString
	var b strings.Builder
	fmt.Fprintf(&b, `<div class="of-cookie-policy"><p>%s</p>`, e(v.intro()))
	b.WriteString(`<p><button type="button" class="of-cookie-policy-settings" data-outflare-consent-open>Change your cookie choices</button></p>`)
	for _, c := range v.Categories {
		if len(c.Vendors) == 0 {
			continue
		}
		t := categoryText[c.ID]
		fmt.Fprintf(&b, `<h2>%s</h2><p>%s</p><table><thead><tr><th>Name</th><th>Provider</th><th>Type</th><th>Purpose</th><th>Kept for</th></tr></thead><tbody>`, e(t[0]), e(t[1]))
		for _, vd := range c.Vendors {
			prov := e(provider(vd))
			if vd.PrivacyURL != "" {
				prov = fmt.Sprintf(`<a href="%s" rel="noopener" target="_blank">%s</a>`, e(vd.PrivacyURL), prov)
			}
			for _, it := range vd.Items {
				fmt.Fprintf(&b, `<tr><td><code>%s</code></td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
					e(it.Name), prov, e(kindText[it.Kind]), e(itemPurpose(vd, it)), e(itemRetention(it)))
			}
		}
		b.WriteString(`</tbody></table>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// Page is the policy as a whole HTML page, for the hosted policy.
func (v Version) Page() string {
	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">` +
		`<title>` + html.EscapeString(v.title()) + `</title><meta name="robots" content="noindex"><style>` +
		`body{font:15px/1.55 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;color:#1f2937;background:#fff;max-width:960px;margin:0 auto;padding:32px 16px}` +
		`h1{font-size:26px}h2{font-size:19px;margin-top:32px}table{width:100%;border-collapse:collapse;font-size:14px;display:block;overflow-x:auto}` +
		`th,td{text-align:left;vertical-align:top;padding:8px;border-bottom:1px solid #e5e7eb}th{font-weight:600}code{font-size:13px}` +
		`.of-cookie-policy-settings{display:none}a{color:inherit}` +
		`@media (prefers-color-scheme:dark){body{color:#e5e7eb;background:#111827}th,td{border-color:#374151}}` +
		`</style></head><body><h1>` + html.EscapeString(v.title()) + `</h1>` + v.HTML() + `</body></html>`
}
