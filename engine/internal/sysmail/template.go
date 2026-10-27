package sysmail

import (
	"bytes"
	_ "embed"
	"html/template"
	"strings"
)

// An Email is an account email in the house layout: a heading, a few short
// paragraphs, one button and a small note. Message renders it as plain text
// and as HTML that follows the website's design tokens.
type Email struct {
	To      string
	Subject string
	// Preview is the line inboxes show next to the subject.
	Preview string
	Heading string
	Body    []string
	Button  string // label for URL
	URL     string
	Note    string // small print under the button, such as who can ignore it
}

//go:embed email.html
var emailHTML string

var emailTmpl = template.Must(template.New("email").Parse(emailHTML))

// Message renders e into a Message with both a text and an HTML body.
func (e Email) Message() Message {
	var t strings.Builder
	t.WriteString(e.Heading + "\n\n")
	for _, p := range e.Body {
		t.WriteString(p + "\n\n")
	}
	if e.URL != "" {
		t.WriteString(e.Button + ":\n" + e.URL + "\n\n")
	}
	if e.Note != "" {
		t.WriteString(e.Note + "\n\n")
	}
	t.WriteString("-- \nOutflare · https://outflare.ai\n")

	var h bytes.Buffer
	if err := emailTmpl.Execute(&h, e); err != nil {
		// The template is fixed and its data is strings, so this can't
		// happen; the text body still goes out if it somehow does.
		return Message{To: e.To, Subject: e.Subject, Text: t.String()}
	}
	return Message{To: e.To, Subject: e.Subject, Text: t.String(), HTML: h.String()}
}
