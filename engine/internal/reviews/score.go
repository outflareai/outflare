package reviews

import (
	"math"
	"time"
)

// Outflare's review score: one number out of 100 for a brand (or one
// page), from every review's stars, weighted by how new it is, and pulled
// toward the middle while there are few reviews. Everything here works
// from review dates, so the score has a history from the first read.

const (
	// HalfLife is how long until a review counts half as much.
	HalfLife = 180 * 24 * time.Hour
	// PriorStars and PriorWeight are the starting point a small number of
	// reviews is pulled toward: 10 reviews at 3.5 stars.
	PriorStars  = 3.5
	PriorWeight = 10.0
)

// Rated is one review's stars (1 to 5) and when it was posted.
type Rated struct {
	Stars float64
	At    time.Time
}

// Score is Outflare's review score at a moment: out of 100, the weighted
// average it comes from, and how many reviews it counts.
type Score struct {
	Score   int     `json:"score"`
	Average float64 `json:"average"`
	Reviews int     `json:"reviews"`
}

// ScoreAt is the review score as it stood at asOf, counting only reviews
// posted by then. ok is false with no reviews.
func ScoreAt(list []Rated, asOf time.Time, halfLife time.Duration) (Score, bool) {
	if halfLife <= 0 {
		halfLife = HalfLife
	}
	sum, weight, n := PriorStars*PriorWeight, PriorWeight, 0
	for _, r := range list {
		if r.At.After(asOf) || r.Stars < 1 {
			continue
		}
		age := asOf.Sub(r.At)
		w := math.Pow(0.5, age.Hours()/halfLife.Hours())
		sum += w * r.Stars
		weight += w
		n++
	}
	if n == 0 {
		return Score{}, false
	}
	avg := sum / weight
	return Score{Score: int(math.Round((avg - 1) / 4 * 100)), Average: math.Round(avg*100) / 100, Reviews: n}, true
}

// Momentum states, from best to worst.
const (
	Growing    = "growing"
	Improving  = "improving"
	Steady     = "steady"
	Stagnating = "stagnating"
	Slipping   = "slipping"
	// TooFew means there aren't enough reviews to say yet.
	TooFew = "too_few"
)

// Momentum is where reviews are heading: the score now against 28 days
// ago, and the pace of new reviews in the last 28 days against the 28
// before.
type Momentum struct {
	State string `json:"state"`
	// Score now, and its change over 28 days.
	Score       int `json:"score"`
	ScoreChange int `json:"score_change"`
	// New reviews in the last 28 days and the 28 before, and the change
	// in pace as a share (-0.31 is 31% slower); null with none before.
	New         int      `json:"new_reviews"`
	NewBefore   int      `json:"new_reviews_before"`
	PaceChange  *float64 `json:"pace_change"`
	PerWeek     float64  `json:"per_week"`
	TotalCounts int      `json:"reviews"`
}

// MomentumAt works out momentum at asOf.
//
//   - Slipping: the score fell 2 points or more.
//   - Growing: the score held or rose, and the pace is up 15% or more.
//   - Improving: the score rose 2 points or more.
//   - Stagnating: the pace fell 30% or more, or under 8 reviews in 8 weeks.
//   - Steady: none of these.
//
// With fewer than 5 reviews ever, it is too_few.
func MomentumAt(list []Rated, asOf time.Time, halfLife time.Duration) Momentum {
	now, ok := ScoreAt(list, asOf, halfLife)
	m := Momentum{State: TooFew, Score: now.Score, TotalCounts: now.Reviews}
	if !ok || now.Reviews < 5 {
		return m
	}
	before, _ := ScoreAt(list, asOf.Add(-28*24*time.Hour), halfLife)
	m.ScoreChange = now.Score - before.Score
	from, mid := asOf.Add(-28*24*time.Hour), asOf.Add(-56*24*time.Hour)
	for _, r := range list {
		switch {
		case r.At.After(asOf):
		case r.At.After(from):
			m.New++
		case r.At.After(mid):
			m.NewBefore++
		}
	}
	m.PerWeek = math.Round(float64(m.New)/4*10) / 10
	if m.NewBefore > 0 {
		p := math.Round((float64(m.New)/float64(m.NewBefore)-1)*100) / 100
		m.PaceChange = &p
	}
	pace := 0.0
	if m.PaceChange != nil {
		pace = *m.PaceChange
	} else if m.New > 0 {
		pace = 1
	}
	switch {
	case m.ScoreChange <= -2:
		m.State = Slipping
	case m.ScoreChange > -2 && pace >= 0.15 && m.New >= 2:
		m.State = Growing
	case m.ScoreChange >= 2:
		m.State = Improving
	case pace <= -0.30 || m.New+m.NewBefore < 8:
		m.State = Stagnating
	default:
		m.State = Steady
	}
	return m
}

// Sentiment from stars alone: 4 and 5 are positive, 3 neutral, 1 and 2
// negative. The workspace's own AI can read the text as well.
func Sentiment(stars float64) string {
	switch {
	case stars >= 3.75:
		return "positive"
	case stars >= 2.75:
		return "neutral"
	}
	return "negative"
}
