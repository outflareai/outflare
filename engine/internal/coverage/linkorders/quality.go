package linkorders

import (
	"fmt"
	"math"
)

// Whether a catalog site is worth a link. The rules are the link-building
// team's base minimum:
//
//   - DR 30 or more, and 5,000 or more organic visits a month.
//   - No more than 45% of its search traffic on its top page, nor on its
//     home page; skip it too when its top three pages take 80% or more (all
//     of it on one to three pages: often a doorway or one viral post).
//   - No more than 0.3 linked domains for every referring domain (linked ÷
//     referring): more gives away links like a link farm, not a
//     publication.
//   - A top page with 35% to 45% is close to the limit: worth a look.
//
// The link-farm grade (referring ÷ linked domains: under 1 a link farm, 2
// or more normal, 5 or more premium) is shown beside it. Where the traffic
// sits comes from an Ahrefs check (or a file that has it); the link ratio
// also reads the intermediary's outgoing domains.
const (
	MinDomainRating  = 30
	MinTraffic       = 5000
	HomeShareSkip    = 0.45
	Top3ShareSkip    = 0.80
	TopPageShareSkip = 0.45
	TopPageShareLook = 0.35
	OutInSkip        = 0.30
)

// Quality is the verdict on a site and the numbers behind it. Shares are
// 0 to 1 of the site's search traffic; nil when not known.
type Quality struct {
	// Verdict is skip, check, ok, or unknown when nothing is known.
	Verdict      string   `json:"verdict"`
	Reasons      []string `json:"reasons"`
	HomeShare    *float64 `json:"home_share"`
	TopPageShare *float64 `json:"top_page_share"`
	Top3Share    *float64 `json:"top3_share"`
	// OutIn is linked ÷ referring domains; RDLD its inverse.
	OutIn *float64 `json:"out_in"`
	RDLD  *float64 `json:"rd_ld"`
	// Grade of RDLD: link_farm (under 1), weak (1 to 2), normal (2 to 5),
	// premium (5 or more).
	Grade string `json:"grade"`
}

func share(part *int64, total *int64) *float64 {
	if part == nil || total == nil || *total <= 0 {
		return nil
	}
	v := math.Min(1, float64(*part)/float64(*total))
	v = math.Round(v*1000) / 1000
	return &v
}

func pct(v float64) string { return fmt.Sprintf("%.0f%%", v*100) }

// Assess judges one catalog site from its stored numbers.
func Assess(d Donor) Quality {
	q := Quality{Verdict: "unknown", Reasons: []string{}}
	m := d.Metrics
	q.HomeShare = share(m.HomeTraffic, d.Traffic)
	q.TopPageShare = share(m.TopPageTraffic, d.Traffic)
	q.Top3Share = share(m.Top3Traffic, d.Traffic)
	linked := m.LinkedDomains
	if linked == nil {
		linked = m.OutgoingDomainsFollowed
	}
	if rd := m.ReferringDomains; rd != nil && linked != nil {
		if *rd > 0 {
			v := math.Round(float64(*linked)/float64(*rd)*100) / 100
			q.OutIn = &v
		}
		if *linked > 0 {
			v := math.Round(float64(*rd)/float64(*linked)*100) / 100
			q.RDLD = &v
			switch {
			case v < 1:
				q.Grade = "link_farm"
			case v < 2:
				q.Grade = "weak"
			case v < 5:
				q.Grade = "normal"
			default:
				q.Grade = "premium"
			}
		} else if *rd > 0 {
			q.Grade = "premium"
		}
	}
	skip, look := false, false
	if d.DomainRating != nil && *d.DomainRating < MinDomainRating {
		skip = true
		q.Reasons = append(q.Reasons, fmt.Sprintf("DR %d, under %d", *d.DomainRating, MinDomainRating))
	}
	if d.Traffic != nil && *d.Traffic < MinTraffic {
		skip = true
		q.Reasons = append(q.Reasons, fmt.Sprintf("%d organic visits a month, under %d", *d.Traffic, MinTraffic))
	}
	if s := q.HomeShare; s != nil && *s > HomeShareSkip {
		skip = true
		q.Reasons = append(q.Reasons, "home page has "+pct(*s)+" of the search traffic")
	}
	if s := q.Top3Share; s != nil && *s >= Top3ShareSkip {
		skip = true
		q.Reasons = append(q.Reasons, "top 3 pages have "+pct(*s)+" of the search traffic")
	}
	if s := q.TopPageShare; s != nil {
		switch {
		case *s > TopPageShareSkip:
			skip = true
			q.Reasons = append(q.Reasons, "one page has "+pct(*s)+" of the search traffic")
		case *s >= TopPageShareLook:
			look = true
			q.Reasons = append(q.Reasons, "top page has "+pct(*s)+" of the search traffic")
		}
	}
	if q.OutIn != nil && *q.OutIn > OutInSkip {
		skip = true
		q.Reasons = append(q.Reasons, fmt.Sprintf("links out to %.2f domains per referring domain (over %.1f)", *q.OutIn, OutInSkip))
	}
	switch {
	case skip:
		q.Verdict = "skip"
	case look:
		q.Verdict = "check"
	case q.OutIn != nil || q.TopPageShare != nil || q.HomeShare != nil || q.Top3Share != nil || d.DomainRating != nil || d.Traffic != nil:
		q.Verdict = "ok"
	}
	return q
}
