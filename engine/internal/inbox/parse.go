// Package inbox syncs mail received by connected mailboxes over IMAP,
// matches it to the leads and sequences it answers, and acts on it: a reply
// stops the lead's sequence, an opt-out or a bounce suppresses the address.
package inbox

import (
	"bufio"
	"bytes"
	"io"
	"net/textproto"
	"regexp"
	"strings"
	"time"

	_ "github.com/emersion/go-message/charset" // decode non-UTF-8 mail
	"github.com/emersion/go-message/mail"
)

// maxBody is how much text is kept from a message.
const maxBody = 100 << 10

// Parsed is what the inbox needs from a received message.
type Parsed struct {
	MessageID  string
	InReplyTo  string
	References []string
	From       string // lower-case address
	FromName   string
	To         string
	Subject    string
	Body       string
	Date       time.Time
	AutoReply  bool
	// Bounce is set for delivery failure reports.
	Bounce *Bounce
}

// Bounce is a delivery failure report.
type Bounce struct {
	// Failed holds recipients that failed permanently (5.x.x).
	Failed []string
	// MessageIDs are ids of the original message found in the report.
	MessageIDs []string
}

// Parse reads a raw RFC 5322 message. It is lenient: a message cut short
// still yields its headers and whatever body was read.
func Parse(raw []byte) (*Parsed, error) {
	r, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil && r == nil {
		return nil, err
	}
	p := &Parsed{}
	h := r.Header
	if from, err := h.AddressList("From"); err == nil && len(from) > 0 {
		p.From, p.FromName = strings.ToLower(from[0].Address), from[0].Name
	} else {
		p.From = strings.ToLower(strings.Trim(h.Get("From"), "<> "))
	}
	if to, err := h.AddressList("To"); err == nil && len(to) > 0 {
		p.To = strings.ToLower(to[0].Address)
	}
	p.Subject, _ = h.Subject()
	p.MessageID, _ = h.MessageID()
	if ids, _ := h.MsgIDList("In-Reply-To"); len(ids) > 0 {
		p.InReplyTo = ids[0]
	}
	p.References, _ = h.MsgIDList("References")
	p.Date, _ = h.Date()
	p.AutoReply = isAutoReply(h.Header.Get, p.Subject)

	isReport := strings.HasPrefix(strings.ToLower(h.Get("Content-Type")), "multipart/report")
	var plain, html string
	for {
		part, err := r.NextPart()
		if err != nil {
			break // io.EOF, or a message cut short
		}
		ct := ""
		switch ph := part.Header.(type) {
		case *mail.InlineHeader:
			ct, _, _ = ph.ContentType()
		case *mail.AttachmentHeader:
			ct, _, _ = ph.ContentType()
		}
		b, _ := io.ReadAll(io.LimitReader(part.Body, maxBody))
		switch {
		case ct == "message/delivery-status":
			isReport = true
			bo := p.bounce()
			bo.Failed = append(bo.Failed, failedRecipients(b)...)
		case ct == "message/rfc822" || ct == "text/rfc822-headers":
			if id := headerMessageID(b); id != "" {
				bo := p.bounce()
				bo.MessageIDs = append(bo.MessageIDs, id)
			}
		case ct == "text/plain" && plain == "":
			plain = string(b)
		case ct == "text/html" && html == "":
			html = string(b)
		}
	}
	p.Body = plain
	if p.Body == "" && html != "" {
		p.Body = stripHTML(html)
	}
	p.Body = strings.TrimSpace(strings.ReplaceAll(p.Body, "\r\n", "\n"))

	// Some servers send bounces as plain text from the mailer daemon.
	if !isReport && isDaemon(p.From) && bounceSubject.MatchString(p.Subject) {
		isReport = true
		b := p.bounce()
		if hardCode.MatchString(p.Body) {
			for _, id := range msgIDInText.FindAllStringSubmatch(p.Body, -1) {
				b.MessageIDs = append(b.MessageIDs, id[1])
			}
		}
	}
	if isReport && p.Bounce == nil {
		p.Bounce = &Bounce{}
	}
	return p, nil
}

func (p *Parsed) bounce() *Bounce {
	if p.Bounce == nil {
		p.Bounce = &Bounce{}
	}
	return p.Bounce
}

var (
	bounceSubject = regexp.MustCompile(`(?i)undeliver|delivery (status|failure|has failed)|returned mail|failure notice|mail delivery failed`)
	hardCode      = regexp.MustCompile(`\b5\.\d{1,3}\.\d{1,3}\b|\b55\d\b`)
	msgIDInText   = regexp.MustCompile(`(?i)message-id:\s*<([^>\s]+)>`)
	autoSubject   = regexp.MustCompile(`(?i)^(auto(matic)?[ -]?(reply|response)|out of (the )?office|ooo\b|away from|on leave|i am (currently )?(away|out))`)
)

func isDaemon(from string) bool {
	local, _, _ := strings.Cut(from, "@")
	return local == "mailer-daemon" || local == "postmaster"
}

// isAutoReply follows RFC 3834 (Auto-Submitted) and the headers common
// mail systems add to vacation replies.
func isAutoReply(get func(string) string, subject string) bool {
	if v := strings.ToLower(get("Auto-Submitted")); v != "" && v != "no" {
		return true
	}
	if get("X-Autoreply") != "" || get("X-Autorespond") != "" || get("X-Auto-Response-Suppress") != "" && autoSubject.MatchString(subject) {
		return true
	}
	if p := strings.ToLower(get("Precedence")); p == "auto_reply" {
		return true
	}
	return autoSubject.MatchString(subject)
}

// failedRecipients reads a message/delivery-status part (RFC 3464) and
// returns recipients whose delivery failed permanently.
func failedRecipients(b []byte) []string {
	var out []string
	tr := textproto.NewReader(bufio.NewReader(bytes.NewReader(append(bytes.TrimLeft(b, "\r\n"), '\n', '\n'))))
	// The first block is per-message; the rest are per-recipient.
	if _, err := tr.ReadMIMEHeader(); err != nil {
		return nil
	}
	for {
		h, err := tr.ReadMIMEHeader()
		if len(h) > 0 {
			action := strings.ToLower(strings.TrimSpace(h.Get("Action")))
			status := strings.TrimSpace(h.Get("Status"))
			rcpt := h.Get("Final-Recipient")
			if rcpt == "" {
				rcpt = h.Get("Original-Recipient")
			}
			if _, addr, ok := strings.Cut(rcpt, ";"); ok {
				rcpt = addr
			}
			rcpt = strings.ToLower(strings.Trim(strings.TrimSpace(rcpt), "<>"))
			if action == "failed" && strings.HasPrefix(status, "5") && strings.Contains(rcpt, "@") {
				out = append(out, rcpt)
			}
		}
		if err != nil {
			return out
		}
	}
}

func headerMessageID(b []byte) string {
	tr := textproto.NewReader(bufio.NewReader(bytes.NewReader(b)))
	h, _ := tr.ReadMIMEHeader()
	return strings.Trim(strings.TrimSpace(h.Get("Message-Id")), "<>")
}

var (
	tags    = regexp.MustCompile(`(?s)<(script|style)[^>]*>.*?</(script|style)>|<[^>]+>`)
	breaks  = regexp.MustCompile(`(?i)<(br|/p|/div|/tr|/li)[^>]*>`)
	spaces  = regexp.MustCompile(`[ \t]+`)
	entites = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'")
)

func stripHTML(s string) string {
	s = breaks.ReplaceAllString(s, "\n")
	s = tags.ReplaceAllString(s, "")
	s = entites.Replace(s)
	return spaces.ReplaceAllString(s, " ")
}

// NewText is the part of a reply the person wrote, without the quoted
// message below it.
func NewText(body string) string {
	var b strings.Builder
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, ">") {
			continue
		}
		if quoteHeader.MatchString(t) || t == "-----Original Message-----" || strings.HasPrefix(t, "From: ") && b.Len() > 0 {
			break
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

var quoteHeader = regexp.MustCompile(`(?i)^on .{4,200} wrote:?$|^le .{4,200} a écrit|^am .{4,200} schrieb`)

var optOut = regexp.MustCompile(`(?im)\b(unsubscribe|remove me from (your|this|the|these|all|my) (mailing )?(list|lists|emails|database|sequence)|remove me\W*$|take me off|opt(ed)? out|stop (emailing|e-mailing|contacting|sending)|do not (contact|email|e-mail)|don'?t (contact|email|e-mail)|no more emails)\b`)

// OptOut reports whether a reply asks not to be contacted again. Only the
// new text is read, so a quoted unsubscribe link does not count.
func OptOut(body string) bool {
	t := NewText(body)
	if len(t) > 2000 {
		t = t[:2000]
	}
	return optOut.MatchString(t)
}
