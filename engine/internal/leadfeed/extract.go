// Package leadfeed reads the posts of a Slack lead channel. Extract is rule
// based: no database, no network, no AI. The text is data, never instructions.
package leadfeed

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	maxFields     = 40
	maxLabelLen   = 80
	maxValueLen   = 2000
	maxPhones     = 20
	minPhoneDigit = 9
	maxPhoneDigit = 15
)

// Own describes what belongs to the workspace and brand, so its addresses are
// not taken for the person who wrote in.
type Own struct {
	Members map[string]string // lowercased member email -> user id
	Domains []string          // the brand's own domains, lowercased
}

// Field is one "Label: value" line.
type Field struct{ Label, Value string }

// Extracted is what a post holds.
type Extracted struct {
	Emails  []string // outside addresses, lowercased, in order, no duplicates
	OwnerID string   // user id of a member whose address follows an owner label; "" when none
	Links   []string // http(s) links that are not Slack's own and not on an own domain
	CRMLink string   // first link whose host is a known outside CRM (see crmHosts); "" when none
	Phones  []string
	Fields  []Field // "Label: value" lines, at most 40, label <= 80 chars, value <= 2000 chars
}

// crmHosts are the app hosts of CRM vendors. An entry starting with "." matches
// any subdomain of it.
var crmHosts = []string{"app.attio.com", "app.hubspot.com", ".salesforce.com", ".pipedrive.com"}

var slackHosts = []string{"slack.com", "slack-edge.com", "slack-files.com", "slack-redir.net"}

var (
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)+`)
	linkRe  = regexp.MustCompile(`https?://[^\s<>|"'\)\]]+`)
	phoneRe = regexp.MustCompile(`\+?\d[ \d().\-]{6,}\d`)
	ownerRe = regexp.MustCompile(`(?i)\b(?:contact owner|account owner|assigned to|owner)\b`)
)

// Hash is the hex sha256 of a post's text.
func Hash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// Extract reads emails, links, phone numbers and "Label: value" fields.
func Extract(text string, own Own) Extracted {
	var out Extracted
	text = strings.ToValidUTF8(text, "")
	seenEmail := map[string]bool{}
	seenLink := map[string]bool{}
	seenPhone := map[string]bool{}

	for _, line := range strings.Split(text, "\n") {
		// Owner: a member's address after an owner label on this line.
		if out.OwnerID == "" {
			if loc := ownerRe.FindStringIndex(line); loc != nil {
				for _, e := range emailRe.FindAllString(line[loc[1]:], -1) {
					if id, ok := memberID(e, own); ok {
						out.OwnerID = id
						break
					}
				}
			}
		}
		for _, e := range emailRe.FindAllString(line, -1) {
			e = strings.ToLower(e)
			if seenEmail[e] || isOwn(e, own) {
				continue
			}
			seenEmail[e] = true
			out.Emails = append(out.Emails, e)
		}
		for _, l := range linkRe.FindAllString(line, -1) {
			l = strings.TrimRight(l, ".,;:!?*_")
			host := linkHost(l)
			if host == "" || seenLink[l] || isSlackHost(host) || onDomain(host, own.Domains) {
				continue
			}
			seenLink[l] = true
			if out.CRMLink == "" && isCRMHost(host) {
				out.CRMLink = l
			}
			if !isCRMHost(host) {
				out.Links = append(out.Links, l)
			}
		}
		if len(out.Phones) < maxPhones {
			bare := linkRe.ReplaceAllString(emailRe.ReplaceAllString(line, " "), " ")
			for _, p := range phoneRe.FindAllString(bare, -1) {
				p = strings.TrimSpace(p)
				if n := digits(p); n < minPhoneDigit || n > maxPhoneDigit || seenPhone[p] {
					continue
				}
				seenPhone[p] = true
				out.Phones = append(out.Phones, p)
				if len(out.Phones) == maxPhones {
					break
				}
			}
		}
		if len(out.Fields) < maxFields {
			if f, ok := parseField(line); ok {
				out.Fields = append(out.Fields, f)
			}
		}
	}
	return out
}

func parseField(line string) (Field, bool) {
	line = strings.TrimLeft(line, " \t•·–—-*>")
	i := strings.IndexByte(line, ':')
	if i <= 0 {
		return Field{}, false
	}
	label := strings.Trim(strings.TrimSpace(line[:i]), "*_ ")
	value := strings.Trim(strings.TrimSpace(line[i+1:]), "*_ ")
	if label == "" || value == "" || utf8.RuneCountInString(label) > maxLabelLen ||
		strings.ContainsAny(label, "<>/@|") || strings.HasPrefix(value, "//") {
		return Field{}, false
	}
	return Field{Label: label, Value: truncate(value, maxValueLen)}, true
}

// truncate cuts s to at most n bytes without splitting a character.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func digits(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			n++
		}
	}
	return n
}

func isOwn(email string, own Own) bool {
	if _, ok := memberID(email, own); ok {
		return true
	}
	return onDomain(email[strings.LastIndexByte(email, '@')+1:], own.Domains)
}

// onDomain reports whether host is one of the domains or a subdomain of one.
func onDomain(host string, domains []string) bool {
	host = strings.ToLower(host)
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" && (host == d || strings.HasSuffix(host, "."+d)) {
			return true
		}
	}
	return false
}

func linkHost(l string) string {
	u, err := url.Parse(l)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func isSlackHost(host string) bool { return onDomain(host, slackHosts) }

func isCRMHost(host string) bool {
	for _, h := range crmHosts {
		if host == h || (strings.HasPrefix(h, ".") && strings.HasSuffix(host, h)) {
			return true
		}
	}
	return false
}

// memberID finds a workspace member by address, ignoring case and a +tag in
// the local part (lee+x@host is lee@host).
func memberID(email string, own Own) (string, bool) {
	email = strings.ToLower(email)
	if id, ok := own.Members[email]; ok {
		return id, true
	}
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return "", false
	}
	if plus := strings.IndexByte(email[:at], '+'); plus > 0 {
		id, ok := own.Members[email[:plus]+email[at:]]
		return id, ok
	}
	return "", false
}
