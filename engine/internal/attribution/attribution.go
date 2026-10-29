// Package attribution works out which channel brought each website visit
// and which visits get the credit for each conversion (a sign-up, a
// payment), under five models, so every report, channel and ad is measured
// on the same numbers. Visits are site_sessions; conversions are customer
// events the workspace counts; credits are stored for every model at once.
package attribution

import (
	"math"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/adtrack"
	"github.com/outflareai/outflare/engine/internal/utm"
)

// Models, by id. LastNonDirect is the default.
const (
	LastNonDirect = "last_non_direct"
	FirstTouch    = "first_touch"
	Linear        = "linear"
	Position      = "position"
	TimeDecay     = "time_decay"
)

// Models lists every model, the default first.
var Models = []string{LastNonDirect, FirstTouch, Linear, Position, TimeDecay}

// ValidModel reports whether m is a model id.
func ValidModel(m string) bool {
	for _, x := range Models {
		if x == m {
			return true
		}
	}
	return false
}

// Direct is the channel of a visit that arrived with nothing to go on.
const Direct = "direct"

// Defaults for a workspace that hasn't changed its settings.
const (
	DefaultLookbackDays = 30
	// Time decay halves a touch's weight for every week before the
	// conversion.
	halfLife = 7 * 24 * time.Hour
	// At most this many touches before a conversion are looked at.
	MaxTouches = 50
)

// DefaultEvents are the customer events that count as conversions until
// the workspace picks its own.
var DefaultEvents = []string{"signed_up", "payment.succeeded"}

// Visit is what a website visit arrived with.
type Visit struct {
	UTMSource, UTMMedium, UTMCampaign string
	Ref, ReferrerHost                 string
	ClickIDs                          map[string]string
	// The Outflare campaign its ofc code names, if any.
	Campaign *utm.Campaign
}

// Touch is a visit's channel, source and campaign, and what decided them.
type Touch struct {
	Channel, Source, Campaign string
	CampaignID                *string
	Method                    string // ofc, ref, click_id, utm, referrer, none
}

var paidMediums = map[string]bool{"cpc": true, "ppc": true, "paid": true, "paidsearch": true, "paid-search": true, "paid_search": true,
	"paidsocial": true, "paid-social": true, "paid_social": true, "display": true, "cpm": true, "cpv": true, "banner": true, "ads": true, "ad": true}

// PaidMedium reports whether a cleaned utm_medium means a paid ad.
func PaidMedium(m string) bool { return paidMediums[m] }

var emailMediums = map[string]bool{"email": true, "e-mail": true, "newsletter": true, "mail": true}
var socialMediums = map[string]bool{"social": true, "social-network": true, "social-media": true, "sm": true, "organic_social": true, "organic-social": true}
var affiliateMediums = map[string]bool{"affiliate": true, "affiliates": true, "partner": true, "partners": true}
var outboundSources = map[string]bool{"emailbison": true, "instantly": true, "smartlead": true, "outbound": true, "cold-email": true, "cold": true}

// Referrer hosts by kind. The first match wins, so AI assistants on a
// search engine's domain (gemini.google.com) come before search.
var aiHosts = map[string]string{"chatgpt.com": "chatgpt", "chat.openai.com": "chatgpt", "perplexity.ai": "perplexity",
	"gemini.google.com": "gemini", "bard.google.com": "gemini", "claude.ai": "claude", "copilot.microsoft.com": "copilot",
	"you.com": "you", "phind.com": "phind", "meta.ai": "meta-ai", "grok.com": "grok", "chat.deepseek.com": "deepseek"}
var searchHosts = map[string]string{"google": "google", "bing.com": "bing", "duckduckgo.com": "duckduckgo", "yahoo": "yahoo",
	"yandex": "yandex", "baidu.com": "baidu", "ecosia.org": "ecosia", "search.brave.com": "brave", "startpage.com": "startpage", "naver.com": "naver"}
var socialHosts = map[string]string{"facebook.com": "facebook", "instagram.com": "instagram", "linkedin.com": "linkedin", "lnkd.in": "linkedin",
	"t.co": "x", "x.com": "x", "twitter.com": "x", "youtube.com": "youtube", "youtu.be": "youtube", "tiktok.com": "tiktok",
	"threads.net": "threads", "bsky.app": "bluesky", "pinterest.com": "pinterest", "news.ycombinator.com": "hackernews", "quora.com": "quora"}

// Classify works out a visit's channel. own are the workspace's own hosts:
// a referrer on one of them is an internal page, not a source.
func Classify(v Visit, own []string) Touch {
	campaign := cut(utm.Value(v.UTMCampaign), 80)
	if c := v.Campaign; c != nil {
		id := c.ID
		return Touch{Channel: c.Channel, Source: c.Source, Campaign: c.Slug, CampaignID: &id, Method: "ofc"}
	}
	if strings.TrimSpace(v.Ref) != "" {
		return Touch{Channel: "affiliates", Source: "partner", Campaign: campaign, Method: "ref"}
	}
	// A click id that proves a paid click (adtrack.ClickIDs). fbclid is
	// not one: Facebook adds it to every link, paid or not, so Meta ads
	// need UTM tags.
	for _, c := range adtrack.ClickIDs {
		if c.Paid && v.ClickIDs[c.Param] != "" {
			return Touch{Channel: c.Channel, Source: c.Source, Campaign: campaign, Method: "click_id"}
		}
	}
	source := cut(utm.Source(v.UTMSource), 80)
	if medium := strings.ToLower(strings.TrimSpace(v.UTMMedium)); medium != "" || source != "" {
		ch := ""
		switch {
		case paidMediums[medium]:
			ch = "paid_ads"
		case emailMediums[medium]:
			ch = "lifecycle_email"
			if outboundSources[source] {
				ch = "outbound_email"
			}
		case socialMediums[medium]:
			ch = "organic_social"
			if source == "reddit" {
				ch = "reddit"
			}
		case affiliateMediums[medium]:
			ch = "affiliates"
		case medium == "referral":
			ch = "referral"
		case medium == "organic" || medium == "seo":
			ch = "seo"
		case medium == "ai":
			ch = "ai_answers"
		default:
			// A source with no medium we know: go by what the source is.
			if t, ok := byHost(source); ok {
				ch = t.Channel
			} else {
				ch = "referral"
			}
		}
		if source == "" {
			source = "(none)"
		}
		return Touch{Channel: ch, Source: source, Campaign: campaign, Method: "utm"}
	}
	host := cleanHost(v.ReferrerHost)
	if host != "" && !isOwn(host, own) {
		if t, ok := byHost(host); ok {
			t.Method = "referrer"
			return t
		}
		return Touch{Channel: "referral", Source: cut(host, 80), Method: "referrer"}
	}
	return Touch{Channel: Direct, Source: "(direct)", Method: "none"}
}

// byHost names the channel of a known referrer host (or a source written
// as one, such as utm_source=chatgpt.com).
func byHost(host string) (Touch, bool) {
	host = cleanHost(host)
	if host == "" {
		return Touch{}, false
	}
	match := func(m map[string]string) (string, bool) {
		for k, name := range m {
			if host == k || strings.HasSuffix(host, "."+k) || name == host {
				return name, true
			}
			// google.com, google.co.uk, yahoo.co.jp: the label before the
			// suffix.
			if !strings.Contains(k, ".") && (strings.HasPrefix(host, k+".") || strings.Contains(host, "."+k+".")) {
				return name, true
			}
		}
		return "", false
	}
	if strings.HasPrefix(host, "mail.") || strings.HasPrefix(host, "outlook.") {
		return Touch{}, false // a webmail inbox: who sent it is unknown
	}
	if n, ok := match(aiHosts); ok {
		return Touch{Channel: "ai_answers", Source: n}, true
	}
	if host == "reddit" || host == "reddit.com" || strings.HasSuffix(host, ".reddit.com") || host == "redd.it" {
		return Touch{Channel: "reddit", Source: "reddit"}, true
	}
	if n, ok := match(searchHosts); ok {
		return Touch{Channel: "seo", Source: n}, true
	}
	if n, ok := match(socialHosts); ok {
		return Touch{Channel: "organic_social", Source: n}, true
	}
	return Touch{}, false
}

func cleanHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if strings.Contains(h, "://") {
		if u, err := url.Parse(h); err == nil {
			h = u.Hostname()
		}
	}
	h = strings.TrimSuffix(h, ".")
	for _, p := range []string{"www.", "m.", "l.", "lm.", "old.", "mobile."} {
		h = strings.TrimPrefix(h, p)
	}
	return h
}

func isOwn(host string, own []string) bool {
	for _, d := range own {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

func cut(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Point is one touch in time, for the models.
type Point struct {
	At     time.Time
	Direct bool
	// Channel is the touch's channel, for models that leave channels out
	// or give them their own window.
	Channel string
}

// Weights shares one conversion at time at between touches, under a
// built-in model with no window. See Spec.Weights.
func Weights(model string, touches []Point, at time.Time) []float64 {
	return Spec{Base: model}.Weights(touches, at)
}

// Weights shares one conversion at time at between touches. It returns the
// share of each touch by index, zero for none; the shares add up to 1.
// Touches the spec leaves out (an excluded channel, or older than its
// window) get nothing. With no touch left it returns nil, and the
// conversion goes to direct.
func (s Spec) Weights(touches []Point, at time.Time) []float64 {
	n := len(touches)
	if n == 0 {
		return nil
	}
	order := make([]int, 0, n)
	for i, t := range touches {
		if s.counts(t, at) {
			order = append(order, i)
		}
	}
	if len(order) == 0 {
		return nil
	}
	sort.SliceStable(order, func(a, b int) bool { return touches[order[a]].At.Before(touches[order[b]].At) })
	// Every model but last-click style ones ignores direct visits when
	// there is anything else, as GA4 does.
	cands := []int{}
	for _, i := range order {
		if !touches[i].Direct {
			cands = append(cands, i)
		}
	}
	if len(cands) == 0 {
		cands = order
	}
	w := make([]float64, n)
	switch s.Base {
	case FirstTouch:
		w[cands[0]] = 1
	case Linear:
		for _, i := range cands {
			w[i] = 1 / float64(len(cands))
		}
	case Position:
		first, last := s.positionWeights()
		switch k := len(cands); k {
		case 1:
			w[cands[0]] = 1
		case 2:
			// Two touches share what the middle would have had.
			w[cands[0]], w[cands[1]] = first/(first+last), last/(first+last)
		default:
			w[cands[0]], w[cands[k-1]] = first, last
			for _, i := range cands[1 : k-1] {
				w[i] = (1 - first - last) / float64(k-2)
			}
		}
	case TimeDecay:
		half := halfLife
		if s.HalfLifeDays > 0 {
			half = time.Duration(s.HalfLifeDays * float64(24*time.Hour))
		}
		total := 0.0
		for _, i := range cands {
			age := at.Sub(touches[i].At)
			if age < 0 {
				age = 0
			}
			w[i] = math.Pow(0.5, float64(age)/float64(half))
			total += w[i]
		}
		for _, i := range cands {
			w[i] /= total
		}
	default: // LastNonDirect
		w[cands[len(cands)-1]] = 1
	}
	return w
}
