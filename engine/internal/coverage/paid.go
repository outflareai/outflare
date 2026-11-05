package coverage

import (
	"net/url"
	"strings"
)

// PaidEvidence is why a page looks paid for: the signs in its address,
// its title or the link it holds. A competitor's placement shows "Likely
// paid" only with at least one; otherwise its arrangement is unknown,
// because nobody outside the deal can know for sure.
//
// pageURL and title describe the page; linkURL is where its link points
// (a competitor's page), when a backlinks read found one. The evidence is
// short fixed labels, never page text.
func PaidEvidence(pageURL, title, linkURL string, sponsoredRel bool) []string {
	out := []string{}
	add := func(s string) {
		for _, e := range out {
			if e == s {
				return
			}
		}
		out = append(out, s)
	}
	if sponsoredRel {
		add("link_sponsored")
	}
	if u, err := url.Parse(strings.TrimSpace(pageURL)); err == nil {
		path := strings.ToLower(u.Path)
		for _, word := range []string{"sponsored", "advertorial", "partner-content", "paid-post", "paid-content", "promoted", "brandvoice", "brand-studio", "sponsor-content"} {
			if strings.Contains(path, word) {
				add("address_" + strings.ReplaceAll(word, "-", "_"))
			}
		}
		if medium := strings.ToLower(u.Query().Get("utm_medium")); paidMedium(medium) {
			add("address_utm_" + medium)
		}
	}
	t := strings.ToLower(title)
	for _, word := range []string{"sponsored", "advertorial", "paid post", "partner content", "promoted"} {
		if strings.Contains(t, word) {
			add("title_" + strings.ReplaceAll(word, " ", "_"))
		}
	}
	if u, err := url.Parse(strings.TrimSpace(linkURL)); err == nil && u.Hostname() != "" {
		q := u.Query()
		for _, key := range []string{"aff", "affiliate", "aff_id", "affid", "via", "fpr", "ref_id", "partner", "irclickid", "clickid"} {
			if q.Get(key) != "" {
				add("link_affiliate")
				break
			}
		}
		if medium := strings.ToLower(q.Get("utm_medium")); paidMedium(medium) {
			add("link_utm_" + medium)
		}
	}
	return out
}

func paidMedium(m string) bool {
	switch m {
	case "affiliate", "affiliates", "cpc", "ppc", "paid", "sponsored", "sponsorship", "display", "advertorial", "partner":
		return true
	}
	return false
}
