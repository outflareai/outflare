package paidads

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

// Creative results: every ad scored step by step.
//
// An ad wins or loses people at each step between being shown and a
// first payment. A video has to be watched past its opening (hook) and
// then kept watching (hold); any ad has to be clicked, the click has to
// become a sign-up, and the sign-up a first payment. Each step's rate is
// graded against the same rate of all the brand's ads on the same network
// over the same days, so a grade says where this ad is better or worse
// than the brand's own usual, not than an outside benchmark. From the
// grades and the brand's target cost per new paying customer comes one
// verdict, and the weakest step says what to fix.

// The steps, in order.
const (
	StepHook   = "hook"    // video: plays past the opening / impressions
	StepHold   = "hold"    // video: plays to the end / plays past the opening
	StepClick  = "click"   // clicks / impressions
	StepSignup = "sign_up" // sign-ups / clicks
	StepPay    = "pay"     // first payments / sign-ups
)

// How much of a step's base an ad needs before its rate is graded.
var creativeEnough = map[string]int64{StepHook: 1000, StepHold: 100, StepClick: 1000, StepSignup: 50, StepPay: 10}

// CreativeStep is one step of one ad.
type CreativeStep struct {
	ID string `json:"id"`
	// Count over Base is the rate: the people who took the step, of those
	// who could.
	Count int64    `json:"count"`
	Base  int64    `json:"base"`
	Rate  *float64 `json:"rate"`
	// Usual is the same rate over all the brand's ads on the network.
	Usual *float64 `json:"usual"`
	// Grade is A (well above usual), B (about usual), C (below), D (well
	// below), or empty while the base is too small to say.
	Grade string `json:"grade"`
}

// CreativeResult is one ad.
type CreativeResult struct {
	PerformanceRow
	CampaignName string `json:"campaign_name"`
	// What the network says the ad is and shows.
	Type         string         `json:"type"`
	Headline     string         `json:"headline"`
	Text         string         `json:"text"`
	URL          string         `json:"url"`
	ThumbnailURL string         `json:"thumbnail_url"`
	Video        bool           `json:"video"`
	Steps        []CreativeStep `json:"steps"`
	// Verdict is scale, keep, fix, stop or too_early. WeakestStep is the
	// step to fix, when there is one.
	Verdict     string `json:"verdict"`
	WeakestStep string `json:"weakest_step"`
}

// CreativeSummary counts the ads by verdict.
type CreativeSummary struct {
	Total    int `json:"total"`
	Scale    int `json:"scale"`
	Keep     int `json:"keep"`
	Fix      int `json:"fix"`
	Stop     int `json:"stop"`
	TooEarly int `json:"too_early"`
}

// CreativeResults reads the brand's ads that ran in the range with their
// steps, grades and verdicts, most spend first. p.Level is ignored.
func CreativeResults(ctx context.Context, q Querier, p PerformanceQuery) ([]CreativeResult, CreativeSummary, error) {
	var sum CreativeSummary
	p.Level, p.Model = LevelAd, ""
	rows, err := Performance(ctx, q, p)
	if err != nil {
		return nil, sum, err
	}
	type extra struct {
		details        map[string]string
		campaign       string
		views, through int64
	}
	more := map[string]extra{}
	xr, err := q.Query(ctx, `
		SELECT e.id::text, e.details, COALESCE(c.name, ''),
		       COALESCE(sum(m.video_views), 0)::bigint, COALESCE(sum(m.video_thruplays), 0)::bigint
		FROM ad_entities e
		LEFT JOIN ad_entities g ON g.id = e.parent_id
		LEFT JOIN ad_entities c ON c.id = COALESCE(g.parent_id, g.id)
		LEFT JOIN ad_metrics_daily m ON m.ad_entity_id = e.id AND m.day BETWEEN $2::date AND $3::date
		WHERE e.workspace_id = $1 AND e.level = 'ad'
		GROUP BY e.id, c.name`, p.WorkspaceID, p.From.UTC().Format(time.DateOnly), p.To.UTC().Format(time.DateOnly))
	if err != nil {
		return nil, sum, err
	}
	defer xr.Close()
	for xr.Next() {
		var id string
		var raw []byte
		var x extra
		if err := xr.Scan(&id, &raw, &x.campaign, &x.views, &x.through); err != nil {
			return nil, sum, err
		}
		var any map[string]any
		_ = json.Unmarshal(raw, &any)
		x.details = map[string]string{}
		for k, v := range any {
			if s, ok := v.(string); ok {
				x.details[k] = s
			}
		}
		more[id] = x
	}
	if err := xr.Err(); err != nil {
		return nil, sum, err
	}

	out := make([]CreativeResult, 0, len(rows))
	// The usual rate of each step on each network: all its ads together.
	type pool struct{ count, base int64 }
	usual := map[string]*pool{}
	add := func(connector, step string, count, base int64) {
		k := connector + " " + step
		if usual[k] == nil {
			usual[k] = &pool{}
		}
		usual[k].count += count
		usual[k].base += base
	}
	for _, r := range rows {
		if r.Status == "removed" && r.SpendMicros == 0 || r.SpendMicros == 0 && r.Impressions == 0 {
			continue
		}
		x := more[r.ID]
		c := CreativeResult{PerformanceRow: r, CampaignName: x.campaign, Type: x.details["type"], Headline: x.details["headline"], Text: x.details["text"],
			URL: x.details["url"], ThumbnailURL: x.details["thumbnail_url"]}
		c.Video = x.views > 0 || strings.Contains(strings.ToLower(c.Type), "video")
		if c.Video {
			c.Steps = append(c.Steps, CreativeStep{ID: StepHook, Count: x.views, Base: r.Impressions}, CreativeStep{ID: StepHold, Count: x.through, Base: x.views})
		}
		c.Steps = append(c.Steps, CreativeStep{ID: StepClick, Count: r.Clicks, Base: r.Impressions},
			CreativeStep{ID: StepSignup, Count: int64(r.Signups), Base: r.Clicks}, CreativeStep{ID: StepPay, Count: int64(r.NewPayers), Base: int64(r.Signups)})
		for _, s := range c.Steps {
			add(r.Connector, s.ID, s.Count, s.Base)
		}
		out = append(out, c)
	}
	for i := range out {
		c := &out[i]
		worst := 0.0
		for j := range c.Steps {
			s := &c.Steps[j]
			if u := usual[c.Connector+" "+s.ID]; u != nil && u.base > 0 {
				v := float64(u.count) / float64(u.base)
				s.Usual = &v
			}
			if s.Base > 0 {
				v := float64(s.Count) / float64(s.Base)
				s.Rate = &v
			}
			if s.Base < creativeEnough[s.ID] || s.Usual == nil || *s.Usual == 0 {
				continue
			}
			switch ratio := *s.Rate / *s.Usual; {
			case ratio >= 1.3:
				s.Grade = "A"
			case ratio >= 0.9:
				s.Grade = "B"
			case ratio >= 0.6:
				s.Grade = "C"
			default:
				s.Grade = "D"
			}
			// The weakest step is the first of the lowest grade.
			if g := float64(s.Grade[0]); g > worst && s.Grade >= "C" {
				worst, c.WeakestStep = g, s.ID
			}
		}
		c.Verdict = creativeVerdict(c, p.Econ)
		if c.Verdict != "fix" && c.Verdict != "stop" {
			c.WeakestStep = ""
		}
		sum.Total++
		switch c.Verdict {
		case "scale":
			sum.Scale++
		case "keep":
			sum.Keep++
		case "fix":
			sum.Fix++
		case "stop":
			sum.Stop++
		default:
			sum.TooEarly++
		}
	}
	slices.SortStableFunc(out, func(a, b CreativeResult) int {
		switch {
		case a.SpendMicros > b.SpendMicros:
			return -1
		case a.SpendMicros < b.SpendMicros:
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out, sum, nil
}

// creativeVerdict is the one word for an ad. With a target cost per new
// paying customer it is judged in money first; the grades say whether
// there is something to fix.
func creativeVerdict(c *CreativeResult, econ Economics) string {
	graded, low := 0, false
	for _, s := range c.Steps {
		if s.Grade != "" {
			graded++
		}
		if s.Grade == "D" {
			low = true
		}
	}
	target := int64(0)
	if econ.TargetCostPerPayerMicros != nil {
		target = *econ.TargetCostPerPayerMicros
	}
	if target > 0 {
		cost := c.CostPerNewPayerMicros
		switch {
		case c.NewPayers == 0 && c.SpendMicros >= 2*target:
			return "stop"
		case cost != nil && *cost > target*3/2 && c.NewPayers >= 2:
			return "stop"
		case c.SpendMicros < 2*target && c.NewPayers < 2:
			if graded < 2 {
				return "too_early"
			}
		case cost != nil && *cost <= target && c.NewPayers >= 2 && !low:
			return "scale"
		}
	} else if graded < 2 {
		return "too_early"
	}
	switch {
	case c.WeakestStep != "":
		return "fix"
	case graded == 0:
		return "too_early"
	}
	return "keep"
}
