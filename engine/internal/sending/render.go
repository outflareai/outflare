// Package sending runs sequences: it schedules due steps, renders them,
// passes every email through the policy check, rotates mailboxes and sends.
package sending

import (
	"bytes"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"regexp"
	"slices"
	"strings"
	"time"
)

var varPattern = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.]+)\s*(?:\|([^}]*))?\}\}`)

// Render fills {{name}} and {{name|fallback}} placeholders from vars. Names
// are case-insensitive and "custom." prefixes are optional. It returns the
// names that had no value and no fallback; such an email must not be sent,
// or the lead would get "Hi ,".
func Render(tmpl string, vars map[string]string) (string, []string) {
	var missing []string
	out := varPattern.ReplaceAllStringFunc(tmpl, func(m string) string {
		parts := varPattern.FindStringSubmatch(m)
		name := strings.TrimPrefix(strings.ToLower(parts[1]), "custom.")
		if v := strings.TrimSpace(vars[name]); v != "" {
			return v
		}
		if strings.Contains(m, "|") {
			return strings.TrimSpace(parts[2])
		}
		if !slices.Contains(missing, name) {
			missing = append(missing, name)
		}
		return ""
	})
	return out, missing
}

// Email is a message ready to send.
type Email struct {
	FromName, From, To string
	Subject, Body      string
	MessageID          string // without angle brackets
	InReplyTo          string // Message-ID being replied to, if any
	References         string // space-separated, with angle brackets
	UnsubscribeURL     string
	Date               time.Time
}

// Bytes renders the email as RFC 5322 plain text. Cold email is sent as
// plain text: it reads as a personal note and avoids tracking pixels.
func (e Email) Bytes() []byte {
	var b bytes.Buffer
	h := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	from := e.From
	if e.FromName != "" {
		from = fmt.Sprintf("%s <%s>", mime.QEncoding.Encode("utf-8", e.FromName), e.From)
	}
	h("From", from)
	h("To", e.To)
	h("Subject", mime.QEncoding.Encode("utf-8", e.Subject))
	h("Date", e.Date.Format(time.RFC1123Z))
	h("Message-ID", "<"+e.MessageID+">")
	if e.InReplyTo != "" {
		h("In-Reply-To", "<"+e.InReplyTo+">")
	}
	if e.References != "" {
		h("References", e.References)
	}
	if e.UnsubscribeURL != "" {
		// RFC 8058 one-click unsubscribe, required by Gmail and Yahoo for
		// bulk senders.
		h("List-Unsubscribe", "<"+e.UnsubscribeURL+">")
		h("List-Unsubscribe-Post", "List-Unsubscribe=One-Click")
	}
	h("MIME-Version", "1.0")
	h("Content-Type", `text/plain; charset="utf-8"`)
	h("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	qp.Write([]byte(strings.ReplaceAll(strings.ReplaceAll(e.Body, "\r\n", "\n"), "\n", "\r\n"))) //nolint:errcheck
	qp.Close()                                                                                   //nolint:errcheck
	return b.Bytes()
}
