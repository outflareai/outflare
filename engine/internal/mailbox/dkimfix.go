package mailbox

import (
	"errors"
	"regexp"
	"strings"
)

// The DKIM records a domain on Google Workspace or Microsoft 365 needs.
// Only the mail host can make the key, so the person copies what its
// admin shows (Google: the TXT value; Microsoft: the two CNAME targets)
// and Outflare checks its shape before adding it.

var (
	dkimKey    = regexp.MustCompile(`^v=DKIM1;\s*k=rsa;\s*p=([A-Za-z0-9+/]+={0,2})$`)
	dkimTarget = regexp.MustCompile(`^selector[12]-[a-z0-9-]+\._domainkey\.[a-z0-9-]+(\.[a-z0-9-]+)+$`)
)

// DKIMRecords gives the records for the values the person copied from
// their mail host's admin, for the host's own selectors.
func DKIMRecords(host string, values []string) ([]DNSRecord, error) {
	switch host {
	case ProviderGoogle:
		if len(values) != 1 {
			return nil, errors.New("paste the one TXT value Google Admin shows for google._domainkey")
		}
		v := strings.Join(strings.Fields(strings.ReplaceAll(strings.Trim(strings.TrimSpace(values[0]), `"`), `" "`, "")), " ")
		m := dkimKey.FindStringSubmatch(v)
		if m == nil {
			return nil, errors.New("that isn't a DKIM key: Google Admin shows a value that starts v=DKIM1; k=rsa; p=")
		}
		return []DNSRecord{{Type: "TXT", Host: "google._domainkey", Value: "v=DKIM1; k=rsa; p=" + m[1]}}, nil
	case ProviderMicrosoft:
		if len(values) != 2 {
			return nil, errors.New("paste both CNAME targets Microsoft shows, for selector1 and selector2")
		}
		out := make([]DNSRecord, 2)
		for i, v := range values {
			v = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(v)), ".")
			if !dkimTarget.MatchString(v) || !strings.HasPrefix(v, "selector"+string(rune('1'+i))+"-") {
				return nil, errors.New("that isn't the target for selector" + string(rune('1'+i)) + ": Microsoft shows a host like selector" + string(rune('1'+i)) + "-example-com._domainkey.example.onmicrosoft.com")
			}
			out[i] = DNSRecord{Type: "CNAME", Host: "selector" + string(rune('1'+i)) + "._domainkey", Value: v}
		}
		return out, nil
	}
	return nil, errors.New("only Google Workspace and Microsoft 365 DKIM keys can be added here; add the record your mail host gives you by hand")
}
