package imports

import (
	"html"
	"regexp"
	"strings"
)

// Variables other tools use, mapped to Outflare's names. Anything else in
// {{...}} is kept as is and read as a custom field.
var variables = map[string]string{
	"firstname": "first_name", "first_name": "first_name",
	"lastname": "last_name", "last_name": "last_name",
	"companyname": "company", "company_name": "company", "company": "company",
	"jobtitle": "title", "job_title": "title", "title": "title",
	"website": "website", "phone": "phone", "phone_number": "phone",
	"linkedin": "linkedin_url", "linkedin_profile": "linkedin_url",
	"sendingaccountfirstname": "sender_name", "sender_name": "sender_name", "sender_first_name": "sender_name",
	"unsubscribe": "unsubscribe_url", "unsubscribe_link": "unsubscribe_url", "unsubscribe_url": "unsubscribe_url",
}

var (
	varRe    = regexp.MustCompile(`\{\{\s*([^{}|]+?)\s*(\|[^{}]*)?\}\}`)
	paraRe   = regexp.MustCompile(`(?i)<\s*/p\s*>`)
	breakRe  = regexp.MustCompile(`(?i)<\s*(br|/div|/li|/h[1-6])\s*/?>`)
	tagRe    = regexp.MustCompile(`<[^>]*>`)
	blanksRe = regexp.MustCompile(`\n{3,}`)
	spinRe   = regexp.MustCompile(`(?i)\{\{\s*random\s*\|`)
)

// Convert turns another tool's step text into Outflare's plain-text
// template, and says what it could not carry over.
func Convert(s string) (string, []string) {
	var warn []string
	if spinRe.MatchString(s) || strings.Contains(s, "{%") {
		warn = append(warn, "uses spintax or logic Outflare does not run; check the text before starting")
	}
	if strings.Contains(s, "<") {
		s = paraRe.ReplaceAllString(s, "\n\n")
		s = breakRe.ReplaceAllString(s, "\n")
		s = tagRe.ReplaceAllString(s, "")
		s = html.UnescapeString(s)
	}
	s = varRe.ReplaceAllStringFunc(s, func(m string) string {
		p := varRe.FindStringSubmatch(m)
		name, fallback := p[1], p[2]
		key := strings.ToLower(strings.ReplaceAll(name, " ", ""))
		if spinRe.MatchString(m) {
			return m
		}
		if key == "accountsignature" || key == "signature" {
			return ""
		}
		if v, ok := variables[key]; ok {
			name = v
		}
		return "{{" + name + fallback + "}}"
	})
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, " ", " ")
	s = blanksRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s), warn
}
