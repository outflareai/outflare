package digests

import (
	"fmt"
	"html"
	"math"
	"strings"
	"time"
)

// Render writes a digest as an email: subject, HTML and plain text.
// appBase is the workspace's address in the app (https://host/{slug});
// items link to their page under it.
func Render(workspace, appBase string, d *Digest) (subject, htmlBody, text string) {
	kind := "Weekly"
	if d.Frequency == Daily {
		kind = "Daily"
	}
	subject = fmt.Sprintf("%s digest for %s, %s", kind, workspace, span(d))

	var h, t strings.Builder
	h.WriteString(`<div style="font-family:-apple-system,Segoe UI,Helvetica,Arial,sans-serif;max-width:600px;margin:0 auto;color:#111;font-size:14px;line-height:1.5">`)
	fmt.Fprintf(&h, `<h1 style="font-size:20px;margin:0 0 4px">%s digest</h1><p style="margin:0 0 20px;color:#666">%s, %s</p>`,
		kind, html.EscapeString(workspace), html.EscapeString(span(d)))
	fmt.Fprintf(&t, "%s digest for %s, %s\n", kind, workspace, span(d))
	if len(d.Sections) == 0 {
		h.WriteString(`<p>Nothing happened in this period.</p>`)
		t.WriteString("\nNothing happened in this period.\n")
	}
	for _, s := range d.Sections {
		fmt.Fprintf(&h, `<h2 style="font-size:16px;margin:24px 0 8px">%s</h2>`, html.EscapeString(s.Title))
		fmt.Fprintf(&t, "\n%s\n", s.Title)
		if len(s.Metrics) > 0 {
			h.WriteString(`<table style="border-collapse:collapse;width:100%">`)
			for _, m := range s.Metrics {
				fmt.Fprintf(&h, `<tr><td style="padding:4px 0;border-bottom:1px solid #eee">%s</td><td style="padding:4px 0;border-bottom:1px solid #eee;text-align:right;font-weight:600">%s</td><td style="padding:4px 0 4px 12px;border-bottom:1px solid #eee;text-align:right;color:#666;width:90px">%s</td></tr>`,
					html.EscapeString(m.Label), number(m.Value), html.EscapeString(change(m)))
				fmt.Fprintf(&t, "  %s: %s", m.Label, number(m.Value))
				if c := change(m); c != "" {
					fmt.Fprintf(&t, " (%s)", c)
				}
				t.WriteString("\n")
			}
			h.WriteString(`</table>`)
		}
		if len(s.Items) > 0 {
			h.WriteString(`<ul style="padding-left:20px;margin:8px 0">`)
			for _, it := range s.Items {
				color := "#111"
				switch it.Tone {
				case "bad":
					color = "#b42318"
				case "good":
					color = "#067647"
				}
				line := html.EscapeString(it.Text)
				if it.Tab != "" {
					line = fmt.Sprintf(`<a href="%s" style="color:%s">%s</a>`, html.EscapeString(appBase+"/"+it.Tab), color, line)
				}
				fmt.Fprintf(&h, `<li style="margin:2px 0;color:%s">%s</li>`, color, line)
				fmt.Fprintf(&t, "  - %s", it.Text)
				if it.Tab != "" {
					fmt.Fprintf(&t, " (%s/%s)", appBase, it.Tab)
				}
				t.WriteString("\n")
			}
			h.WriteString(`</ul>`)
		}
	}
	settings := appBase + "/digests"
	fmt.Fprintf(&h, `<p style="margin:32px 0 0;color:#666;font-size:12px"><a href="%s" style="color:#666">Open in Outflare</a> · You get this as a member of %s. Change it or turn it off in <a href="%s" style="color:#666">Digests</a>.</p></div>`,
		html.EscapeString(appBase+"/overview"), html.EscapeString(workspace), html.EscapeString(settings))
	fmt.Fprintf(&t, "\nOpen in Outflare: %s/overview\nChange it or turn it off: %s\n", appBase, settings)
	return subject, h.String(), t.String()
}

func span(d *Digest) string {
	end := d.PeriodEnd.Add(-time.Second)
	if d.Frequency == Daily {
		return end.Format("2 Jan 2006")
	}
	return d.PeriodStart.Format("2 Jan") + " to " + end.Format("2 Jan 2006")
}

func number(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}

// change is how a metric moved from the period before: "+3", "-2" or "".
func change(m Metric) string {
	if m.Previous == nil || *m.Previous == m.Value {
		return ""
	}
	diff := m.Value - *m.Previous
	if diff > 0 {
		return "+" + number(diff)
	}
	return "-" + number(-diff)
}
