package api

import (
	"html/template"
	"net/http"

	"github.com/outflareai/outflare/engine/internal/emails"
)

// The preference page behind a lifecycle email's unsubscribe link: one
// button for the email's own topic, a box per topic, and all marketing
// email off. What people choose is a consent on their customer record.

var preferencesTmpl = template.Must(template.New("p").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Email preferences</title>
<style>body{font:16px/1.5 system-ui,sans-serif;max-width:32rem;margin:10vh auto;padding:0 1rem;color:#222}
button{font:inherit;padding:.5rem 1rem;border-radius:.4rem;border:1px solid #222;background:#222;color:#fff;cursor:pointer}
button.quiet{background:transparent;color:inherit}
label{display:flex;gap:.6rem;align-items:flex-start;padding:.6rem 0;border-top:1px solid #8883}
label small{display:block;opacity:.7}
fieldset{border:0;padding:0;margin:1.5rem 0}
.note{opacity:.75;font-size:.9rem}
@media(prefers-color-scheme:dark){body{background:#111;color:#eee}button{background:#eee;color:#111;border-color:#eee}button.quiet{color:#eee}}</style>
</head><body>
{{if .Invalid}}<h1>Link not recognised</h1><p>This link is not valid. Reply to the email and ask to be removed, and the sender will take you off their list.</p>
{{else}}
{{if .Saved}}<p role="status"><strong>{{.Saved}}</strong> {{if .P.AllWithdrawn}}You won't get any more marketing email from {{.P.Sender}}.{{else}}Your choices are below.{{end}}</p>{{end}}
<h1>Email preferences</h1>
<p class="note">From {{.P.Sender}}, for {{.P.Address}}</p>
{{if and .P.TopicName (not .Saved)}}<form method="post"><input type="hidden" name="action" value="topic">
<p>You got this email because you're subscribed to <strong>{{.P.TopicName}}</strong>.</p>
<button type="submit">Unsubscribe from {{.P.TopicName}}</button></form>{{end}}
<form method="post"><input type="hidden" name="action" value="save">
{{if .P.Topics}}<fieldset><legend>Choose what you get</legend>
{{range .P.Topics}}<label><input type="checkbox" name="topic" value="{{.Key}}"{{if .Subscribed}} checked{{end}}><span>{{.Name}}{{if .Description}}<small>{{.Description}}</small>{{end}}</span></label>
{{end}}</fieldset>{{end}}
<label><input type="checkbox" name="all" value="off"{{if .P.AllWithdrawn}} checked{{end}}><span>Unsubscribe from all marketing email<small>You'll still get emails you need, such as receipts and password resets.</small></span></label>
<p><button type="submit" class="quiet">Save preferences</button></p></form>
{{end}}
</body></html>`))

// saved is the message after a change, or "".
func (s *Server) preferencesPage(w http.ResponseWriter, r *http.Request, emailID string, saved string) {
	p, err := s.emails.Preferences(r.Context(), emailID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if p == nil {
		w.WriteHeader(http.StatusNotFound)
	}
	preferencesTmpl.Execute(w, map[string]any{"Invalid": p == nil, "P": p, "Saved": saved}) //nolint:errcheck
}

// savePreferences handles the preference page's forms and one-click
// unsubscribes (RFC 8058) from mail apps, which withdraw from the email's
// topic, or from all lifecycle email when it has none.
func (s *Server) savePreferences(w http.ResponseWriter, r *http.Request, emailID string) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "the form could not be read")
		return
	}
	var err error
	saved := "Saved."
	switch r.PostForm.Get("action") {
	case "save":
		c := emails.PreferenceChange{Subscribed: map[string]bool{}, All: r.PostForm.Get("all") == "off", Source: "preference_center", RequestID: RequestID(ctx)}
		p, perr := s.emails.Preferences(ctx, emailID)
		if perr != nil || p == nil {
			err = perr
			break
		}
		ticked := map[string]bool{}
		for _, k := range r.PostForm["topic"] {
			ticked[k] = true
		}
		for _, t := range p.Topics {
			c.Subscribed[t.Key] = ticked[t.Key]
		}
		err = s.emails.SetPreferences(ctx, emailID, c)
	default:
		// "topic" from the page's button, or a mail app's one-click POST.
		err = s.emails.Unsubscribe(ctx, emailID, RequestID(ctx))
		saved = "You're unsubscribed."
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.preferencesPage(w, r, emailID, saved)
}
