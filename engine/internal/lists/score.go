package lists

import (
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Score rates how well a person fits an ideal customer profile, 0 to 100,
// and says why. Only the profile's filled-in dimensions count, weighted by
// how much they usually matter: title, then seniority, then company size,
// industry and location. It returns nil when the profile is empty.
//
// Scoring is rules, not a model: the same person always gets the same
// score, and the reasons say exactly which parts matched.
func Score(icp c.PeopleFilter, p c.Person) (*int, []string) {
	type dim struct {
		weight  int
		want    []string
		matched bool
		label   string
	}
	sen := p.Seniority
	if sen == "" {
		sen = InferSeniority(p.Title)
	}
	dims := []dim{
		{35, icp.Titles, anyIn(p.Title, icp.Titles), "title"},
		{20, icp.Seniorities, containsFold(icp.Seniorities, sen), "seniority"},
		{15, icp.EmployeeRanges, inRanges(p.Company.Employees, icp.EmployeeRanges), "company size"},
		{15, icp.Industries, anyIn(p.Company.Industry, icp.Industries), "industry"},
		{15, append(append([]string{}, icp.Locations...), icp.CompanyLocations...),
			anyIn(p.Location(), icp.Locations) || anyIn(p.Company.Location, icp.CompanyLocations) ||
				(len(icp.Locations) == 0 && anyIn(p.Location(), icp.CompanyLocations)), "location"},
	}
	total, got := 0, 0
	reasons := []string{}
	for _, d := range dims {
		if len(d.want) == 0 {
			continue
		}
		total += d.weight
		if d.matched {
			got += d.weight
			reasons = append(reasons, "+ "+d.label)
		} else {
			reasons = append(reasons, "- "+d.label)
		}
	}
	if total == 0 {
		return nil, nil
	}
	s := (got*100 + total/2) / total
	return &s, reasons
}

// anyIn reports whether any wanted term appears in s, ignoring case.
func anyIn(s string, want []string) bool {
	s = strings.ToLower(s)
	if s == "" {
		return false
	}
	for _, w := range want {
		if w = strings.ToLower(strings.TrimSpace(w)); w != "" && strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if s != "" && strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func inRanges(n int, ranges []string) bool {
	if n <= 0 {
		return false
	}
	for _, r := range ranges {
		lo, hi, ok := parseRange(r)
		if ok && n >= lo && (hi == 0 || n <= hi) {
			return true
		}
	}
	return false
}

// InferSeniority guesses a shared seniority word from a job title, for
// providers that do not send one.
func InferSeniority(title string) string {
	t := " " + strings.ToLower(title) + " "
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(t, w) {
				return true
			}
		}
		return false
	}
	switch {
	case has("founder"):
		return "founder"
	case has("owner"):
		return "owner"
	case has(" chief ", " ceo", " cto", " cfo", " coo", " cmo", " cro", " cio", " president"):
		return "c_suite"
	case has("partner"):
		return "partner"
	case has(" vp", "vice president"):
		return "vp"
	case has("head of", " head "):
		return "head"
	case has("director"):
		return "director"
	case has("manager", " lead "):
		return "manager"
	case has("senior", " sr"):
		return "senior"
	case has("intern"):
		return "intern"
	}
	return ""
}
