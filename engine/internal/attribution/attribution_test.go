package attribution

import (
	"math"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/utm"
)

func TestClassify(t *testing.T) {
	own := []string{"northpeak.example"}
	camp := &utm.Campaign{ID: "c1", Slug: "spring-launch", Channel: "paid_ads", Source: "linkedin"}
	for name, c := range map[string]struct {
		v               Visit
		ch, src, method string
	}{
		"ofc wins over tags":      {Visit{Campaign: camp, UTMSource: "fb", UTMMedium: "social"}, "paid_ads", "linkedin", "ofc"},
		"partner code":            {Visit{Ref: "ana", UTMSource: "google", UTMMedium: "cpc"}, "affiliates", "partner", "ref"},
		"google click id":         {Visit{ClickIDs: map[string]string{"gclid": "x"}}, "paid_ads", "google", "click_id"},
		"snapchat click id":       {Visit{ClickIDs: map[string]string{"ScCid": "x"}}, "paid_ads", "snapchat", "click_id"},
		"impact is a partner":     {Visit{ClickIDs: map[string]string{"irclickid": "x"}}, "affiliates", "impact", "click_id"},
		"meta cookie is no click": {Visit{ClickIDs: map[string]string{"fbp": "fb.1.1.1"}}, "direct", "(direct)", "none"},
		"fbclid alone is organic": {Visit{ClickIDs: map[string]string{"fbclid": "x"}, ReferrerHost: "l.facebook.com"}, "organic_social", "facebook", "referrer"},
		"paid social tags":        {Visit{UTMSource: "Facebook", UTMMedium: "paid_social"}, "paid_ads", "facebook", "utm"},
		"sequencer email":         {Visit{UTMSource: "instantly", UTMMedium: "email"}, "outbound_email", "instantly", "utm"},
		"newsletter":              {Visit{UTMSource: "outflare", UTMMedium: "email"}, "lifecycle_email", "outflare", "utm"},
		"reddit social":           {Visit{UTMSource: "reddit", UTMMedium: "social"}, "reddit", "reddit", "utm"},
		"chatgpt adds a source":   {Visit{UTMSource: "chatgpt.com"}, "ai_answers", "chatgpt", "utm"},
		"google search":           {Visit{ReferrerHost: "www.google.co.uk"}, "seo", "google", "referrer"},
		"gemini is ai":            {Visit{ReferrerHost: "gemini.google.com"}, "ai_answers", "gemini", "referrer"},
		"perplexity":              {Visit{ReferrerHost: "www.perplexity.ai"}, "ai_answers", "perplexity", "referrer"},
		"old reddit":              {Visit{ReferrerHost: "old.reddit.com"}, "reddit", "reddit", "referrer"},
		"x short links":           {Visit{ReferrerHost: "t.co"}, "organic_social", "x", "referrer"},
		"gmail is not search":     {Visit{ReferrerHost: "mail.google.com"}, "referral", "mail.google.com", "referrer"},
		"another site":            {Visit{ReferrerHost: "blog.acme.example"}, "referral", "blog.acme.example", "referrer"},
		"own pages are direct":    {Visit{ReferrerHost: "app.northpeak.example"}, "direct", "(direct)", "none"},
		"nothing":                 {Visit{}, "direct", "(direct)", "none"},
	} {
		got := Classify(c.v, own)
		if got.Channel != c.ch || got.Source != c.src || got.Method != c.method {
			t.Errorf("%s: got %+v, want %s/%s/%s", name, got, c.ch, c.src, c.method)
		}
	}
	if got := Classify(Visit{Campaign: camp}, own); got.CampaignID == nil || *got.CampaignID != "c1" || got.Campaign != "spring-launch" {
		t.Errorf("campaign = %+v", got)
	}
}

func TestWeights(t *testing.T) {
	day := 24 * time.Hour
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	// Out of order on purpose: seo first, then direct, paid, direct.
	ts := []Point{{At: at.Add(-2 * day)}, {At: at.Add(-10 * day)}, {At: at.Add(-time.Hour), Direct: true}, {At: at.Add(-5 * day), Direct: true}}
	sum := func(w []float64) float64 {
		s := 0.0
		for _, x := range w {
			s += x
		}
		return s
	}
	for model, want := range map[string][]float64{
		LastNonDirect: {1, 0, 0, 0},
		FirstTouch:    {0, 1, 0, 0},
		Linear:        {0.5, 0.5, 0, 0},
		Position:      {0.5, 0.5, 0, 0},
	} {
		got := Weights(model, ts, at)
		for i := range want {
			if math.Abs(got[i]-want[i]) > 1e-9 {
				t.Errorf("%s = %v, want %v", model, got, want)
				break
			}
		}
	}
	td := Weights(TimeDecay, ts, at)
	if math.Abs(sum(td)-1) > 1e-9 || td[0] <= td[1] || td[2] != 0 {
		t.Errorf("time decay = %v", td)
	}
	// Three or more: 40/20/40.
	three := []Point{{At: at.Add(-3 * day)}, {At: at.Add(-2 * day)}, {At: at.Add(-day)}, {At: at.Add(-time.Hour)}}
	if w := Weights(Position, three, at); math.Abs(w[0]-0.4) > 1e-9 || math.Abs(w[1]-0.1) > 1e-9 || math.Abs(w[3]-0.4) > 1e-9 {
		t.Errorf("position = %v", w)
	}
	// Only direct visits: the last one.
	if w := Weights(LastNonDirect, []Point{{At: at.Add(-day), Direct: true}, {At: at.Add(-time.Hour), Direct: true}}, at); w[1] != 1 {
		t.Errorf("all direct = %v", w)
	}
	if Weights(Linear, nil, at) != nil {
		t.Error("no touches should give nil")
	}
}
