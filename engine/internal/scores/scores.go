// Package scores keeps a brand's scores on its records: a number from 0
// to 100 on every company or person, such as a health score or a lead
// score.
//
// A score starts from a base and adds or takes points for each factor
// whose conditions hold on the record, so the score explains itself:
// "usage fell by a third: -20". Its value lives on the record under
// attributes.scores.<key>, with <key>_band (high, medium or low), where
// rules, journeys, smart lists and Query read it like any other field.
//
// A record is scored again when one of the fields its factors test
// changes (a metric moving counts), once a day, since a factor can
// compare a date with today, and quietly, every record at once, when a
// score is added or changed: that pass sets nothing off, like every fill
// from what is already stored.
//
// The API handlers in engine/internal/api make and change scores; this
// package checks them and scores records in jobs.
package scores

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/outflareai/outflare/engine/internal/journeys"
)

// The bands a score falls in.
const (
	High   = "high"
	Medium = "medium"
	Low    = "low"
)

const (
	// MaxFactors is the most factors a score has.
	MaxFactors = 30
	// MaxConditions is the most conditions a factor has.
	MaxConditions = 5
	// MaxPerBrand is the most active scores a brand keeps.
	MaxPerBrand = 20
)

// KeyRE is what a score's key looks like.
var KeyRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// The kinds of number a score is.
const (
	Measured  = "measured"
	AI        = "ai"
	Predicted = "predicted"
)

const (
	// MaxParts is the most scores a headline score is made of.
	MaxParts = 6
	// HistoryDays is how long a record's score history is kept.
	HistoryDays = 730
)

// Factor adds Points (or takes them, when negative) when its conditions
// hold on the record: all of them, or any when Match is any.
type Factor struct {
	Label      string               `json:"label"`
	Conditions []journeys.Condition `json:"conditions"`
	Match      string               `json:"match"`
	Points     int                  `json:"points"`
}

// Part is one of the scores a headline score is the weighted average of.
type Part struct {
	Key    string `json:"key"`
	Weight int    `json:"weight"`
}

// Definition is a score as the engine reads it. It has factors, or parts
// (a headline score made of the brand's other scores), never both.
type Definition struct {
	ID          string   `json:"id"`
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Description string   `json:"description"`
	RecordType  string   `json:"record_type"`
	Base        int      `json:"base"`
	Factors     []Factor `json:"factors"`
	HighFrom    int      `json:"high_from"`
	LowBelow    int      `json:"low_below"`
	Version     int      `json:"version"`
	// Nature says what kind of number this is: measured from what
	// happened, read by the brand's AI, or a prediction.
	Nature string `json:"nature"`
	Parts  []Part `json:"parts"`
	// HigherIsBetter is false for a score such as churn risk, where the
	// high band is the one to worry about.
	HigherIsBetter bool `json:"higher_is_better"`
	// Needs is a field that must be set for a record to get this score, or
	// "": without it the record has no value, which is not a low one.
	Needs string `json:"needs"`
}

// Columns are a definition's columns, in Scan's order.
const Columns = `id::text, key, label, description, record_type, base, factors, high_from, low_below, version, nature, parts, higher_is_better, needs`

var needsRE = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){0,3}$`)

// Check reports what is wrong with a score on its own, or "". CheckSet
// checks it against the brand's other scores.
func Check(d Definition) string {
	switch {
	case !KeyRE.MatchString(d.Key) || strings.HasSuffix(d.Key, "_band"):
		return "key is lowercase letters, digits and underscores, such as health (and can't end in _band)"
	case strings.TrimSpace(d.Label) == "" || len(d.Label) > 80:
		return "label is 1 to 80 characters"
	case len(d.Description) > 500:
		return "description is at most 500 characters"
	case d.RecordType != "person" && d.RecordType != "account" && d.RecordType != "deal":
		return "record_type is account (companies), person or deal"
	case d.Nature != Measured && d.Nature != AI && d.Nature != Predicted:
		return "nature is measured (worked out from what happened), ai (read from conversations) or predicted (an estimate of what will happen)"
	case d.Base < 0 || d.Base > 100:
		return "base is 0 to 100"
	case d.HighFrom < 1 || d.HighFrom > 100 || d.LowBelow < 0 || d.LowBelow > 99 || d.LowBelow >= d.HighFrom:
		return "low_below is under high_from, and both are between 0 and 100"
	case d.Needs != "" && (!needsRE.MatchString(d.Needs) || len(d.Needs) > 80):
		return "needs is a field on the record, such as sentiment.value"
	case len(d.Factors) > 0 && len(d.Parts) > 0:
		return "a score has factors, or parts (a headline score made of other scores), not both"
	case len(d.Factors) == 0 && len(d.Parts) == 0:
		return "a score needs at least one factor: conditions and the points they add or take"
	case len(d.Factors) > MaxFactors:
		return fmt.Sprintf("a score has at most %d factors", MaxFactors)
	case len(d.Parts) == 1 || len(d.Parts) > MaxParts:
		return fmt.Sprintf("a headline score is made of 2 to %d scores", MaxParts)
	}
	seen := map[string]bool{}
	for i, p := range d.Parts {
		at := fmt.Sprintf("part %d: ", i+1)
		switch {
		case !KeyRE.MatchString(p.Key) || p.Key == d.Key:
			return at + "key is the key of another of the brand's scores"
		case seen[p.Key]:
			return at + "each score is a part once"
		case p.Weight < 1 || p.Weight > 100:
			return at + "weight is 1 to 100"
		}
		seen[p.Key] = true
	}
	for i, f := range d.Factors {
		at := fmt.Sprintf("factor %d: ", i+1)
		switch {
		case strings.TrimSpace(f.Label) == "" || len(f.Label) > 120:
			return at + "label is 1 to 120 characters, such as Usage fell by a third"
		case f.Points == 0 || f.Points < -100 || f.Points > 100:
			return at + "points are -100 to 100, and not 0"
		case f.Match != "all" && f.Match != "any":
			return at + "match is all or any"
		case len(f.Conditions) == 0 || len(f.Conditions) > MaxConditions:
			return at + fmt.Sprintf("a factor has 1 to %d conditions", MaxConditions)
		}
		for _, c := range f.Conditions {
			if c.Attribute == "" || c.Property != "" || c.Event != "" || c.Email != "" || c.Var != "" || c.Step != "" {
				return at + "conditions test attribute, a field on the record such as metrics.usage_7d_change"
			}
			top, rest, _ := strings.Cut(c.Attribute, ".")
			if (top == "account" || top == "deal") && d.RecordType != "deal" {
				return at + "only a deal's score reads " + top + ".: the deal's own facts and its company's fields"
			}
			if top == "scores" {
				if key := strings.TrimSuffix(rest, "_band"); !KeyRE.MatchString(key) || key == d.Key {
					return at + "to read another score, test scores.<key> or scores.<key>_band"
				}
			}
			if msg := journeys.CheckCondition(c); msg != "" {
				return at + msg
			}
		}
	}
	return ""
}

// reads are the keys of the scores d's factors test on the same record,
// and whether any factor tests what the AI read.
func (d Definition) reads() (keys []string, sentiment bool) {
	for _, f := range d.Factors {
		for _, c := range f.Conditions {
			top, rest, _ := strings.Cut(c.Attribute, ".")
			if top == "account" {
				top, rest, _ = strings.Cut(rest, ".")
				if top == "scores" {
					continue // The company's score: its own business.
				}
			}
			switch top {
			case "sentiment":
				sentiment = true
			case "scores":
				if key := strings.TrimSuffix(rest, "_band"); !slices.Contains(keys, key) {
					keys = append(keys, key)
				}
			}
		}
	}
	return keys, sentiment
}

// ReadsSentiment is whether a factor tests what the brand's AI read from
// conversations.
func (d Definition) ReadsSentiment() bool {
	_, sentiment := d.reads()
	return sentiment
}

// Uses are the keys of the scores d is made of or reads.
func (d Definition) Uses() []string {
	keys, _ := d.reads()
	for _, p := range d.Parts {
		if !slices.Contains(keys, p.Key) {
			keys = append(keys, p.Key)
		}
	}
	return keys
}

// stage is when a score is worked out: plain scores first, then headline
// scores, then scores that read other scores.
func (d Definition) stage() int {
	if len(d.Parts) > 0 {
		return 1
	}
	if keys, _ := d.reads(); len(keys) > 0 {
		return 2
	}
	return 0
}

// CheckSet reports what is wrong between d and the brand's other active
// scores for its record type, or "".
func CheckSet(d Definition, others []Definition) string {
	byKey := map[string]Definition{}
	for _, o := range others {
		if o.Key != d.Key && (o.ID != d.ID || d.ID == "") {
			byKey[o.Key] = o
		}
	}
	if msg := checkUses(d, byKey); msg != "" {
		return msg
	}
	// The scores that use d still work with it as it is now.
	withD := map[string]Definition{d.Key: d}
	for k, o := range byKey {
		withD[k] = o
	}
	for _, o := range byKey {
		if slices.Contains(o.Uses(), d.Key) {
			if msg := checkUses(o, withD); msg != "" {
				return o.Label + " uses this score: " + msg
			}
		}
	}
	return ""
}

func checkUses(d Definition, byKey map[string]Definition) string {
	for _, p := range d.Parts {
		o, ok := byKey[p.Key]
		switch {
		case !ok:
			return "there is no " + d.RecordType + " score with the key " + p.Key
		case o.stage() != 0:
			return o.Label + " is itself made of or reads other scores, so it can't be a part"
		case d.Nature == Measured && o.Nature != Measured:
			return o.Label + " isn't measured, so a score made of it isn't either: mark this one AI-read or predicted"
		}
	}
	keys, sentiment := d.reads()
	if sentiment && d.Nature == Measured {
		return "this score reads sentiment, which the brand's AI reads from conversations: mark it AI-read or predicted"
	}
	for _, key := range keys {
		o, ok := byKey[key]
		switch {
		case !ok:
			return "there is no " + d.RecordType + " score with the key " + key
		case o.stage() == 2:
			return o.Label + " reads other scores itself, so a score can't read it"
		case d.Nature == Measured && o.Nature != Measured:
			return o.Label + " isn't measured, so a score that reads it isn't either: mark this one AI-read or predicted"
		}
	}
	return ""
}

// WatchFields are the record fields a score's factors test: each
// condition's top-level field. Other scores aren't watched (every score of
// a record is worked out together), nor is a deal's company.
func WatchFields(d Definition) []string {
	out := []string{}
	add := func(top string) {
		if top != "" && top != "scores" && top != "account" && top != "deal" && !slices.Contains(out, top) {
			out = append(out, top)
		}
	}
	for _, f := range d.Factors {
		for _, c := range f.Conditions {
			top, _, _ := strings.Cut(c.Attribute, ".")
			add(top)
		}
	}
	if d.Needs != "" {
		top, _, _ := strings.Cut(d.Needs, ".")
		add(top)
	}
	return out
}

// Score is a record's value for d from its fields, its band, and which
// factors hold, in d.Factors' order. A headline score is worked out by
// All, with the scores it is made of.
func (d Definition) Score(attributes map[string]any) (int, string, []bool) {
	v := d.Base
	held := make([]bool, len(d.Factors))
	for i, f := range d.Factors {
		if journeys.Holds(attributes, nil, f.Conditions, f.Match) {
			held[i] = true
			v += f.Points
		}
	}
	v = min(100, max(0, v))
	return v, d.Band(v), held
}

// Result is one score of a record.
type Result struct {
	Value int
	Band  string
	// Held is which factors hold, in the definition's order.
	Held []bool
}

// All works out every score in defs on a record's fields (its View), in
// order: plain scores, headline scores, then scores that read scores. A
// score whose Needs field isn't set, or a headline score with no part
// that has a value, is left out.
func All(defs []Definition, view map[string]any) map[string]Result {
	out := map[string]Result{}
	fields := map[string]any{}
	for k, v := range view {
		fields[k] = v
	}
	// As a record stores them, so factors read them the same way.
	now := map[string]any{}
	fields["scores"] = now
	for stage := 0; stage <= 2; stage++ {
		for _, d := range defs {
			if d.stage() != stage {
				continue
			}
			if d.Needs != "" {
				if v, ok := journeys.Lookup(fields, d.Needs); !ok || v == "" {
					continue
				}
			}
			var r Result
			if len(d.Parts) > 0 {
				sum, weight := 0, 0
				for _, p := range d.Parts {
					if part, ok := out[p.Key]; ok {
						sum += part.Value * p.Weight
						weight += p.Weight
					}
				}
				if weight == 0 {
					continue
				}
				r.Value = (sum + weight/2) / weight
				r.Band = d.Band(r.Value)
			} else {
				r.Value, r.Band, r.Held = d.Score(fields)
			}
			out[d.Key] = r
			now[d.Key] = float64(r.Value)
			now[d.Key+"_band"] = r.Band
		}
	}
	return out
}

// Band is the band a value falls in.
func (d Definition) Band(v int) string {
	switch {
	case v >= d.HighFrom:
		return High
	case v < d.LowBelow:
		return Low
	}
	return Medium
}

func cond(attribute, op string, value any) []journeys.Condition {
	return []journeys.Condition{{Attribute: attribute, Op: op, Value: value}}
}

func exists(attribute string) []journeys.Condition {
	return []journeys.Condition{{Attribute: attribute, Op: "exists"}}
}

// Starters are scores a brand can start from and change: they read the
// standard metrics (docs/metrics.md) and fields every brand has. Health
// comes first and lead second.
var Starters = []Definition{
	{Key: "health", Label: "Health score", RecordType: "account", Base: 60, HighFrom: 70, LowBelow: 40, Nature: Measured, HigherIsBetter: true,
		Description: "How well a customer is doing, from how much they use the product and whether that is going up or down.",
		Factors: []Factor{
			{Label: "Used the product in the last 7 days", Match: "all", Points: 20, Conditions: cond("metrics.usage_7d", "gt", 0)},
			{Label: "Usage is up by a fifth or more on the week before", Match: "all", Points: 10, Conditions: cond("metrics.usage_7d_change", "gt", 19)},
			{Label: "Paid in the last 90 days", Match: "all", Points: 10, Conditions: cond("metrics.payments_90d", "gt", 0)},
			{Label: "Usage fell by a third or more on the week before", Match: "all", Points: -20, Conditions: cond("metrics.usage_7d_change", "lt", -29)},
			{Label: "No usage in the last 7 days", Match: "any", Points: -25, Conditions: []journeys.Condition{
				{Attribute: "metrics.usage_7d", Op: "not_exists"}, {Attribute: "metrics.usage_7d", Op: "lt", Value: 1}}},
			{Label: "Not seen for 30 days", Match: "all", Points: -15, Conditions: cond("metrics.last_seen", "lt", "-30d")},
			{Label: "Had a refund in the last 90 days", Match: "all", Points: -10, Conditions: cond("metrics.refunds_90d", "gt", 0)},
		}},
	{Key: "lead", Label: "Lead score", RecordType: "person", Base: 20, HighFrom: 60, LowBelow: 30, Nature: Measured, HigherIsBetter: true,
		Description: "How ready someone is to talk to sales, from what they have done with the product so far.",
		Factors: []Factor{
			{Label: "Used the product in the last 7 days", Match: "all", Points: 25, Conditions: cond("metrics.usage_7d", "gt", 0)},
			{Label: "Used the product on 5 or more of the last 30 days", Match: "all", Points: 20, Conditions: cond("metrics.active_days_30d", "gt", 4)},
			{Label: "Usage is up by a fifth or more on the week before", Match: "all", Points: 10, Conditions: cond("metrics.usage_7d_change", "gt", 19)},
			{Label: "Has a job title", Match: "all", Points: 10, Conditions: exists("job_title")},
			{Label: "Has paid before", Match: "all", Points: 15, Conditions: cond("metrics.payments_90d", "gt", 0)},
			{Label: "Not seen for 30 days", Match: "all", Points: -20, Conditions: cond("metrics.last_seen", "lt", "-30d")},
		}},
	{Key: "usage", Label: "Usage trend", RecordType: "account", Base: 50, HighFrom: 70, LowBelow: 40, Nature: Measured, HigherIsBetter: true,
		Description: "Whether a customer uses the product, and whether that is going up or down against their own week before.",
		Factors: []Factor{
			{Label: "Used the product in the last 7 days", Match: "all", Points: 25, Conditions: cond("metrics.usage_7d", "gt", 0)},
			{Label: "Usage is up by a fifth or more on the week before", Match: "all", Points: 25, Conditions: cond("metrics.usage_7d_change", "gt", 19)},
			{Label: "Usage fell by a third or more on the week before", Match: "all", Points: -25, Conditions: cond("metrics.usage_7d_change", "lt", -29)},
			{Label: "No usage in the last 7 days", Match: "any", Points: -30, Conditions: []journeys.Condition{
				{Attribute: "metrics.usage_7d", Op: "not_exists"}, {Attribute: "metrics.usage_7d", Op: "lt", Value: 1}}},
			{Label: "Not seen for 30 days", Match: "all", Points: -20, Conditions: cond("metrics.last_seen", "lt", "-30d")},
		}},
	{Key: "billing", Label: "Billing", RecordType: "account", Base: 50, HighFrom: 70, LowBelow: 40, Nature: Measured, HigherIsBetter: true,
		Description: "Whether a customer pays, and whether anything is wrong with what they pay: from your payment connection.",
		Factors: []Factor{
			{Label: "Paid in the last 90 days", Match: "all", Points: 25, Conditions: cond("metrics.payments_90d", "gt", 0)},
			{Label: "Has an active subscription", Match: "all", Points: 20, Conditions: cond("billing_status", "eq", "active")},
			{Label: "A payment is past due", Match: "all", Points: -35, Conditions: cond("billing_status", "eq", "past_due")},
			{Label: "Canceled their subscription", Match: "all", Points: -40, Conditions: cond("billing_status", "eq", "canceled")},
			{Label: "Had a refund in the last 90 days", Match: "all", Points: -15, Conditions: cond("metrics.refunds_90d", "gt", 0)},
		}},
	{Key: "fit", Label: "Fit", RecordType: "account", Base: 40, HighFrom: 70, LowBelow: 40, Nature: Measured, HigherIsBetter: true,
		Description: "How close a company is to the customers you sell to best. Change the factors to your own industries, sizes and countries.",
		Factors: []Factor{
			{Label: "Their industry is known", Match: "all", Points: 15, Conditions: exists("industry")},
			{Label: "Their size is known", Match: "all", Points: 15, Conditions: exists("company_size")},
			{Label: "Their country is known", Match: "all", Points: 10, Conditions: exists("country")},
			{Label: "Has a website", Match: "all", Points: 10, Conditions: exists("website")},
			{Label: "Already pays you", Match: "all", Points: 10, Conditions: exists("customer_since")},
		}},
	{Key: "sentiment", Label: "Sentiment", RecordType: "account", Base: 50, HighFrom: 70, LowBelow: 40, Nature: AI, HigherIsBetter: true, Needs: "sentiment.value",
		Description: "How a customer sounds in their conversations with you, read by your own AI. A customer nobody has talked to has no sentiment score, which is not a bad one.",
		Factors: []Factor{
			{Label: "Sounds happy in recent conversations", Match: "all", Points: 35, Conditions: cond("sentiment.label", "eq", "positive")},
			{Label: "Sounds unhappy in recent conversations", Match: "all", Points: -35, Conditions: cond("sentiment.label", "eq", "negative")},
			{Label: "Sounds better than before", Match: "all", Points: 10, Conditions: cond("sentiment.trend", "eq", "up")},
			{Label: "Sounds worse than before", Match: "all", Points: -15, Conditions: cond("sentiment.trend", "eq", "down")},
		}},
	{Key: "churn_risk", Label: "Churn risk", RecordType: "account", Base: 15, HighFrom: 60, LowBelow: 30, Nature: Predicted,
		Description: "How likely a customer is to leave, from falling usage, billing trouble and how they sound. A prediction: the higher, the more worrying.",
		Factors: []Factor{
			{Label: "Usage fell by a third or more on the week before", Match: "all", Points: 20, Conditions: cond("metrics.usage_7d_change", "lt", -29)},
			{Label: "No usage in the last 7 days", Match: "any", Points: 20, Conditions: []journeys.Condition{
				{Attribute: "metrics.usage_7d", Op: "not_exists"}, {Attribute: "metrics.usage_7d", Op: "lt", Value: 1}}},
			{Label: "Not seen for 30 days", Match: "all", Points: 15, Conditions: cond("metrics.last_seen", "lt", "-30d")},
			{Label: "A payment is past due", Match: "all", Points: 20, Conditions: cond("billing_status", "eq", "past_due")},
			{Label: "Canceled their subscription", Match: "all", Points: 40, Conditions: cond("billing_status", "eq", "canceled")},
			{Label: "Had a refund in the last 90 days", Match: "all", Points: 10, Conditions: cond("metrics.refunds_90d", "gt", 0)},
			{Label: "Sounds unhappy in recent conversations", Match: "all", Points: 20, Conditions: cond("sentiment.label", "eq", "negative")},
			{Label: "Sounds worse than before", Match: "all", Points: 10, Conditions: cond("sentiment.trend", "eq", "down")},
			{Label: "Renews in the next 60 days", Match: "all", Points: 5, Conditions: []journeys.Condition{
				{Attribute: "renews_on", Op: "gt", Value: "-1d"}, {Attribute: "renews_on", Op: "lt", Value: "+60d"}}},
			{Label: "Usage is up by a fifth or more on the week before", Match: "all", Points: -10, Conditions: cond("metrics.usage_7d_change", "gt", 19)},
			{Label: "Sounds happy in recent conversations", Match: "all", Points: -10, Conditions: cond("sentiment.label", "eq", "positive")},
		}},
	{Key: "expansion", Label: "Expansion potential", RecordType: "account", Base: 15, HighFrom: 60, LowBelow: 30, Nature: Predicted, HigherIsBetter: true,
		Description: "How likely a customer is to buy more, from growing usage, steady payments and how they sound. A prediction.",
		Factors: []Factor{
			{Label: "Usage is up by a fifth or more on the week before", Match: "all", Points: 25, Conditions: cond("metrics.usage_7d_change", "gt", 19)},
			{Label: "Used the product in the last 7 days", Match: "all", Points: 15, Conditions: cond("metrics.usage_7d", "gt", 0)},
			{Label: "Paid in the last 90 days", Match: "all", Points: 15, Conditions: cond("metrics.payments_90d", "gt", 0)},
			{Label: "Sounds happy in recent conversations", Match: "all", Points: 15, Conditions: cond("sentiment.label", "eq", "positive")},
			{Label: "A payment is past due", Match: "all", Points: -20, Conditions: cond("billing_status", "eq", "past_due")},
			{Label: "Sounds unhappy in recent conversations", Match: "all", Points: -20, Conditions: cond("sentiment.label", "eq", "negative")},
			{Label: "Usage fell by a third or more on the week before", Match: "all", Points: -20, Conditions: cond("metrics.usage_7d_change", "lt", -29)},
		}},
	{Key: "win", Label: "Win likelihood", RecordType: "deal", Base: 30, HighFrom: 60, LowBelow: 30, Nature: Predicted, HigherIsBetter: true,
		Description: "How likely a deal is to be won, from its stage, how long it has sat there, recent contact and how the company sounds. A prediction: once you have closed deals, Outflare shows how often deals in each band were really won.",
		Factors: []Factor{
			{Label: "Has a close date", Match: "all", Points: 5, Conditions: exists("close_date")},
			{Label: "Two or more people are on the deal", Match: "all", Points: 10, Conditions: cond("deal.contacts", "gt", 1)},
			{Label: "The company uses the product", Match: "all", Points: 10, Conditions: cond("account.metrics.usage_7d", "gt", 0)},
			{Label: "The company sounds happy in recent conversations", Match: "all", Points: 10, Conditions: cond("account.sentiment.label", "eq", "positive")},
			{Label: "The company sounds unhappy in recent conversations", Match: "all", Points: -20, Conditions: cond("account.sentiment.label", "eq", "negative")},
			{Label: "No contact logged for two weeks", Match: "all", Points: -15, Conditions: cond("deal.days_quiet", "gt", 13)},
			{Label: "In the same stage for over 30 days", Match: "all", Points: -15, Conditions: cond("deal.days_in_stage", "gt", 30)},
			{Label: "Past its close date", Match: "all", Points: -15, Conditions: cond("deal.days_to_close", "lt", 0)},
		}},
}

// WinKey is the key of the deal score the forecast reads.
const WinKey = "win"
