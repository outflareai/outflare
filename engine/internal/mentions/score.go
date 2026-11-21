package mentions

import (
	"math"
	"time"
)

// Sources are the places a mention can come from.
var Sources = []string{"youtube", "podcast", "x", "reddit", "hacker_news", "bluesky", "news", "web"}

// Score is how worth a look a mention is, 0 to 100, from what is known
// before anyone reads it: who was named (a competitor named by someone who
// never named the brand is the opening), how many people heard it, where it
// was said (a video or episode is listened to whole, a post skimmed), and
// how fresh it is. It is a first sort, not a verdict.
func Score(about, source string, reach *int64, published *time.Time, now time.Time) int {
	s := 20.0
	if about == "competitor" {
		s += 15
	}
	switch source {
	case "youtube", "podcast":
		s += 15
	case "x", "bluesky", "reddit", "hacker_news":
		s += 8
	}
	if reach != nil && *reach > 0 {
		// 1,000 is about 15 points, 100,000 about 25, a million 30.
		s += math.Min(30, 5*math.Log10(float64(*reach)))
	}
	if published != nil {
		days := now.Sub(*published).Hours() / 24
		switch {
		case days <= 7:
			s += 20
		case days <= 30:
			s += 12
		case days <= 180:
			s += 5
		}
	}
	return int(math.Round(math.Max(0, math.Min(100, s))))
}
