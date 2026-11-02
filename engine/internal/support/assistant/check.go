// Package assistant holds the support assistant's rules that don't need a
// model: when it must not answer at all, and what a reply it drafted must
// look like before anyone sees it. They are plain code so no customer's
// message can talk its way around them.
package assistant

import (
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Channel is where the customer wrote: chat or email.
type Channel string

const (
	Chat  Channel = "chat"
	Email Channel = "email"
)

// Words a reply may use at most, per channel: a chat line is read at a
// glance, an email can carry a few steps.
var maxWords = map[Channel]int{Chat: 70, Email: 140}

// Before is what a check before the AI sees of the customer's message.
type Before struct {
	Body      string
	From      string // the sender's address, if any
	AutoReply bool   // an out-of-office, a bounce or another automatic answer
	// AIReplies is how many times the assistant already answered in this
	// conversation; TeamReplied is whether a person has answered since the
	// assistant last did.
	AIReplies   int
	TeamReplied bool
}

// The most times the assistant answers in one conversation before it
// leaves the rest to a person.
const MaxRepliesPerConversation = 3

var (
	noReplyRe = regexp.MustCompile(`(?i)^(no-?reply|do-?not-?reply|mailer-daemon|postmaster|bounces?)[@+.-]`)
	humanRe   = regexp.MustCompile(`(?i)\b(human|real person|a person|someone (real|from your team)|talk to (a|an|your)|speak (to|with) (a|an|your|someone)|representative|live agent|support agent|manager)\b`)
	// Topics a person always handles: money disputes, legal, security and
	// personal data requests.
	handoffRe = regexp.MustCompile(`(?i)\b(chargeback|dispute[ds]?|fraud|scam|lawyer|attorney|legal action|sue|lawsuit|court|gdpr|data (deletion|erasure|request)|delete (all )?my (data|account)|right to be forgotten|hack(ed|er)?|breach|stolen|compromised|unauthori[sz]ed (charge|access))\b`)
)

// Stop says whether the assistant must not answer this message, and why:
// a short code for the report. An empty reason means it may go on.
func Stop(b Before) (reason string, handoff bool) {
	from := strings.ToLower(strings.TrimSpace(b.From))
	switch {
	case b.AutoReply:
		return "auto_reply", false
	case from != "" && noReplyRe.MatchString(from):
		return "no_reply_sender", false
	case strings.TrimSpace(b.Body) == "":
		return "empty", false
	case b.TeamReplied:
		return "team_handling", false
	case b.AIReplies >= MaxRepliesPerConversation:
		return "too_many_replies", true
	case humanRe.MatchString(b.Body):
		return "asked_for_person", true
	case handoffRe.MatchString(b.Body):
		return "sensitive_topic", true
	}
	return "", false
}

// Phrases that make a reply read like a machine wrote it, or pad it.
var bannedPhrases = []string{
	"great question", "good question", "i hope this helps", "hope this helps", "i understand your frustration",
	"i completely understand", "i apologize for any inconvenience", "we apologize for any inconvenience",
	"sorry for any inconvenience", "thank you for reaching out", "thanks for reaching out", "thank you for contacting",
	"i'd be happy to help", "i would be happy to help", "i'm happy to help", "happy to assist", "feel free to",
	"don't hesitate to", "do not hesitate to", "as an ai", "as a language model", "i'm just an ai", "certainly!",
	"absolutely!", "delve", "rest assured", "please be advised", "at your earliest convenience",
}

var (
	urlRe   = regexp.MustCompile(`https?://[^\s)>\]]+`)
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	phoneRe = regexp.MustCompile(`\+?\d[\d\s().-]{7,}\d`)
	headRe  = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s`)
)

// Style is what a drafted reply is checked against.
type Style struct {
	Channel Channel
	// Hosts a link may point to: the help centre and the brand's own
	// domains (a host and its subdomains).
	Hosts []string
	// Sources is the text of the articles it cited: any address or number
	// in the reply must come from there.
	Sources string
	// Name is how the assistant signs, if anywhere; the reply mustn't sign
	// itself as anyone else.
	Name string
}

// Lint lists what is wrong with a reply, in words the model can act on
// when asked to rewrite it. Nothing wrong is an empty list.
func Lint(reply string, s Style) []string {
	var issues []string
	text := strings.TrimSpace(reply)
	low := strings.ToLower(text)
	if text == "" {
		return []string{"the reply is empty"}
	}
	if n := len(strings.Fields(text)); n > maxWords[s.Channel] {
		issues = append(issues, "it is "+strconv.Itoa(n)+" words; keep it under "+strconv.Itoa(maxWords[s.Channel]))
	}
	for _, p := range bannedPhrases {
		if strings.Contains(low, p) {
			issues = append(issues, `drop "`+p+`"`)
		}
	}
	if strings.ContainsAny(text, "—–") {
		issues = append(issues, "no dashes like — or –; use a comma, a colon or a full stop")
	}
	if headRe.MatchString(text) {
		issues = append(issues, "no headings; write it as a message")
	}
	if strings.Contains(text, "**") {
		issues = append(issues, "no bold text")
	}
	links := urlRe.FindAllString(text, -1)
	if len(links) > 1 {
		issues = append(issues, "at most one link")
	}
	for _, l := range links {
		if !allowedLink(l, s.Hosts) {
			issues = append(issues, "a link to "+host(l)+" isn't one of the business's own pages")
		}
	}
	src := strings.ToLower(s.Sources)
	for _, e := range emailRe.FindAllString(text, -1) {
		if !strings.Contains(src, strings.ToLower(e)) {
			issues = append(issues, "the address "+e+" isn't in the articles")
		}
	}
	for _, p := range phoneRe.FindAllString(text, -1) {
		// A phone number has at least 9 digits; dates and prices don't.
		if d := digits(p); len(d) >= 9 && !strings.Contains(digits(src), d) {
			issues = append(issues, "the number "+strings.TrimSpace(p)+" isn't in the articles")
		}
	}
	if utf8.RuneCountInString(text) > 2000 {
		issues = append(issues, "far too long")
	}
	return issues
}

func allowedLink(raw string, hosts []string) bool {
	h := host(raw)
	if h == "" {
		return false
	}
	return slices.ContainsFunc(hosts, func(a string) bool {
		a = strings.ToLower(strings.TrimPrefix(a, "www."))
		return a != "" && (h == a || strings.HasSuffix(h, "."+a))
	})
}

func host(raw string) string {
	u, err := url.Parse(strings.TrimRight(raw, ".,;:!?"))
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
