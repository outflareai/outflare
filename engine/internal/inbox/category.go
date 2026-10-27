package inbox

import "regexp"

// Categories are what a reply can mean, in the order the inbox lists them.
// Shipped names are a promise (API, MCP, webhooks): add, never rename.
var Categories = []string{"interested", "meeting_booked", "question", "referral",
	"not_now", "objection", "not_interested", "wrong_person", "unsubscribe", "complaint",
	"out_of_office", "auto_reply"}

// Groups sort the categories by what a person should do about them: hot
// replies want an answer now, later ones a follow-up, closed ones nothing,
// and automatic ones are mail no person wrote.
var Groups = map[string][]string{
	"hot":       {"interested", "meeting_booked", "question"},
	"later":     {"referral", "not_now", "objection"},
	"closed":    {"not_interested", "wrong_person", "unsubscribe", "complaint"},
	"automatic": {"out_of_office", "auto_reply"},
}

// GroupNames lists the groups in display order.
var GroupNames = []string{"hot", "later", "closed", "automatic"}

// GroupOf returns the group a category is in, or "".
func GroupOf(category string) string {
	for g, cs := range Groups {
		for _, c := range cs {
			if c == category {
				return g
			}
		}
	}
	return ""
}

// Expand turns a list of categories and group names into categories,
// reporting the first entry that is neither.
func Expand(names []string) ([]string, string) {
	var out []string
	add := func(c string) {
		for _, x := range out {
			if x == c {
				return
			}
		}
		out = append(out, c)
	}
	for _, n := range names {
		if cs, ok := Groups[n]; ok {
			for _, c := range cs {
				add(c)
			}
			continue
		}
		known := false
		for _, c := range Categories {
			if c == n {
				known = true
			}
		}
		if !known {
			return nil, n
		}
		add(n)
	}
	return out, ""
}

// Rules that sort a reply on arrival. They are deliberately cautious: a
// reply that matches nothing is left for a person or an agent to sort,
// which beats a wrong label. Checked in order, so "not interested" is
// caught before "interested".
var categoryRules = []struct {
	category string
	re       *regexp.Regexp
}{
	{"referral", regexp.MustCompile(`(?i)\b((reach out|speak|talk|get in touch) (to|with)|(contact|email|e-mail|try) (my colleague|our|him|her|them)\b|cc'?(ing|'d|d)\b|copied (in )?(my|our)|looping in|better person|right person (is|would be|to (speak|talk)))`)},
	{"wrong_person", regexp.MustCompile(`(?i)\b(no longer (with|at|work)|left the (company|business|organi[sz]ation)|wrong (person|contact)|not the right (person|contact)|not my (area|department|responsibility))`)},
	{"not_interested", regexp.MustCompile(`(?i)\b(not interested|no,? thanks|no thank you|not (a|the) (good |right )?fit|we('re| are) (all )?(set|good|covered)|not (looking|in the market)|pass on this|not for us|already have (a|an|someone))`)},
	{"not_now", regexp.MustCompile(`(?i)\b(not (right )?now|maybe (later|next)|(circle|reach|check|come) back (to me )?(in|next|later|after)|next (quarter|year|month)|in a (few|couple of) (weeks|months)|bad timing|not a priority (right now|at the moment|this))`)},
	{"meeting_booked", regexp.MustCompile(`(?i)\b(booked (a|in|it|the)|(just )?scheduled|sent (you )?(an? )?(calendar )?invite|see you (on|then|next|tomorrow)|accepted (the|your) invite)`)},
	{"interested", regexp.MustCompile(`(?i)\b(interested|sounds (good|great|interesting)|let'?s (chat|talk|connect|set up|book|find a time)|happy to (chat|talk|connect|jump|hop)|tell me more|send (me |over )?(some )?(more )?(info|information|details|pricing)|what times?|when are you (free|available)|i'?m free|calendly|cal\.com|book a (call|time|meeting)|open to (a )?(chat|call))`)},
	{"question", regexp.MustCompile(`\?`)},
}

var outOfOffice = regexp.MustCompile(`(?i)\b(out of (the )?office|on (annual |parental |maternity |paternity |sick )?leave|on (vacation|holiday)|away (from|until|till)|limited access to (my )?e-?mail|currently away|back (on|in the office))`)

// complaint is a reply that is angry about being emailed at all: a spam
// report, a legal threat, a demand to know where the address came from.
var complaint = regexp.MustCompile(`(?i)\b(this is spam|reported (you|this|as spam)|report(ing)? (you|this) (as|for) spam|marked (as|this as) spam|how did you get my (email|e-mail|address|details)|where did you get my (email|e-mail|address|details)|harass(ing|ment)?|my (lawyer|attorney|solicitor)|legal action|gdpr (complaint|request|violation)|violat(es|ion of) (gdpr|can-spam|the law))\b`)

// Complaint reports whether a reply complains about being emailed. A
// complaint is also an opt-out.
func Complaint(body string) bool {
	return complaint.MatchString(firstChars(NewText(body), 2000))
}

// Categorize sorts a received message by the built-in rules, or returns ""
// when they can't tell. Only the new text of a reply is read.
func Categorize(kind string, optOut bool, body string) string {
	switch {
	case kind == "reply" && Complaint(body):
		return "complaint"
	case optOut:
		return "unsubscribe"
	case kind == "auto_reply":
		if outOfOffice.MatchString(firstChars(body, 2000)) {
			return "out_of_office"
		}
		return "auto_reply"
	case kind != "reply":
		return ""
	}
	t := firstChars(NewText(body), 2000)
	for _, r := range categoryRules {
		if r.re.MatchString(t) {
			return r.category
		}
	}
	return ""
}

func firstChars(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
