package linkorders

import "testing"

func TestAssess(t *testing.T) {
	i := func(v int64) *int64 { return &v }
	n := func(v int) *int { return &v }
	cases := []struct {
		name    string
		d       Donor
		verdict string
		grade   string
	}{
		{"nothing known", Donor{}, "unknown", ""},
		{"a healthy site", Donor{DomainRating: n(50), Traffic: i(10000), Metrics: DonorMetrics{HomeTraffic: i(2000), TopPageTraffic: i(2000), Top3Traffic: i(4000), ReferringDomains: i(1000), LinkedDomains: i(100)}}, "ok", "premium"},
		{"home page over 45%", Donor{Traffic: i(10000), Metrics: DonorMetrics{HomeTraffic: i(4600), TopPageTraffic: i(4600), Top3Traffic: i(6000)}}, "skip", ""},
		{"all on three pages", Donor{Traffic: i(10000), Metrics: DonorMetrics{TopPageTraffic: i(4000), Top3Traffic: i(8500)}}, "skip", ""},
		{"top page over 45%", Donor{Traffic: i(10000), Metrics: DonorMetrics{TopPageTraffic: i(4600), Top3Traffic: i(7000)}}, "skip", ""},
		{"top page at 45% is allowed, close to the limit", Donor{Traffic: i(10000), Metrics: DonorMetrics{TopPageTraffic: i(4500), Top3Traffic: i(7000)}}, "check", ""},
		{"links out too much", Donor{Metrics: DonorMetrics{ReferringDomains: i(1000), OutgoingDomainsFollowed: i(400)}}, "skip", "normal"},
		{"a link farm", Donor{Metrics: DonorMetrics{ReferringDomains: i(100), OutgoingDomainsFollowed: i(500)}}, "skip", "link_farm"},
		// Ahrefs' live linked domains win over the intermediary's count.
		{"Ahrefs first", Donor{Metrics: DonorMetrics{ReferringDomains: i(1000), OutgoingDomainsFollowed: i(900), LinkedDomains: i(200)}}, "ok", "premium"},
		{"DR under 30", Donor{DomainRating: n(29), Traffic: i(50000)}, "skip", ""},
		{"under 5,000 visits", Donor{DomainRating: n(60), Traffic: i(4999)}, "skip", ""},
		{"the base minimum exactly", Donor{DomainRating: n(30), Traffic: i(5000)}, "ok", ""},
	}
	for _, c := range cases {
		q := Assess(c.d)
		if q.Verdict != c.verdict || q.Grade != c.grade {
			t.Errorf("%s: %+v", c.name, q)
		}
	}
	q := Assess(cases[2].d)
	if len(q.Reasons) == 0 || *q.HomeShare != 0.46 {
		t.Fatalf("reasons: %+v", q)
	}
}
