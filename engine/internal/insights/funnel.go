// Package insights answers product analytics questions over everything a
// brand records: ad networks' daily numbers, website visits and page views
// from site.js, the workspace's own product events, saved conversions and
// payments (docs/insights.md). Every question is a spec (a Funnel today,
// trends next) that the API, the web app, MCP and saved reports share, and
// that compiles to one read-only SQL query kept to one workspace and brand.
//
// A funnel follows people. Someone counts at a step only when they did
// every step before it, in order, inside each step's time limit, so the
// drop-off between two steps is the people who stopped there. A person is
// a customer record once known, and otherwise one browser (its anonymous
// id): identifying a browser moves its earlier events and visits to the
// record, so a visit before sign-up and the payment after it line up.
//
// Ad networks report clicks as daily totals, not people, so ad clicks sit
// above a funnel (AdClicks) rather than inside it; the people funnel starts
// at the visit that carried the network's click id.
package insights

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/adtrack"
)

// Querier is a pool or a transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Step kinds.
const (
	// KindEvent is a product or website event by its catalog name.
	KindEvent = "event"
	// KindPage is a page view (the page.viewed event).
	KindPage = "page"
	// KindVisit is the start of a website visit, with how it arrived.
	KindVisit = "visit"
	// KindConversion is a saved conversion definition, by its key.
	KindConversion = "conversion"
	// KindPayment is a succeeded payment from billing or the API.
	KindPayment = "payment"
)

// Kinds lists every step kind.
var Kinds = []string{KindEvent, KindPage, KindVisit, KindConversion, KindPayment}

// Filter ops, the same names conversion definitions use, plus not_contains.
const (
	OpEq          = "eq"
	OpNeq         = "neq"
	OpGt          = "gt"
	OpGte         = "gte"
	OpLt          = "lt"
	OpLte         = "lte"
	OpIn          = "in"
	OpContains    = "contains"
	OpNotContains = "not_contains"
	OpExists      = "exists"
	OpNotExists   = "not_exists"
)

// Ops lists every filter op.
var Ops = []string{OpEq, OpNeq, OpGt, OpGte, OpLt, OpLte, OpIn, OpContains, OpNotContains, OpExists, OpNotExists}

// Filter is one condition a step's rows must pass.
type Filter struct {
	// Property names what is compared. For events and page views: a
	// property key or dotted path (plan.tier), or one of the page and
	// campaign fields in EventFields (path, param:ref, utm_source). For
	// visits, payments: one of VisitFields or PaymentFields.
	Property string `json:"property"`
	Op       string `json:"op"`
	// Value is a string or number; a list for in; nothing for exists.
	Value any `json:"value,omitempty"`
}

// Window is a time limit. On a step it runs from an earlier step (From,
// counted from 1; the step just before when unset); on a funnel it runs
// from step 1.
type Window struct {
	Value int    `json:"value"`
	Unit  string `json:"unit"`
	From  *int   `json:"from,omitempty"`
}

// Window units.
var Units = []string{"minute", "hour", "day", "week"}

// MaxWindow is the longest time limit, and DefaultWindow the funnel's
// when it sets none.
const (
	MaxWindow     = 365 * 24 * time.Hour
	DefaultWindow = 14 * 24 * time.Hour
)

func (w Window) duration() time.Duration {
	unit := map[string]time.Duration{"minute": time.Minute, "hour": time.Hour, "day": 24 * time.Hour, "week": 7 * 24 * time.Hour}[w.Unit]
	return time.Duration(w.Value) * unit
}

// Step is one step of a funnel.
type Step struct {
	Kind string `json:"kind"`
	// Event is the catalog name for an event step (signed_up).
	Event string `json:"event,omitempty"`
	// Conversion is a conversion definition's key for a conversion step.
	Conversion string `json:"conversion,omitempty"`
	// Filters must all pass.
	Filters []Filter `json:"filters,omitempty"`
	// First keeps only the person's first ever such event or payment,
	// so "first purchase" means it.
	First bool `json:"first,omitempty"`
	// Within is this step's time limit.
	Within *Window `json:"within,omitempty"`
	// Label is the step's name on the chart; a default fits its kind.
	Label string `json:"label,omitempty"`
}

// AdClicks asks for the ad networks' clicks, impressions and spend over
// the funnel's dates, shown above step 1. They are daily totals per
// campaign, so they never join the people below.
type AdClicks struct {
	// Network is an adtrack network id (google, meta, x); empty for all.
	Network string `json:"network,omitempty"`
	// Campaign keeps campaigns whose name contains it.
	Campaign string `json:"campaign,omitempty"`
}

// Exclusion drops from the whole funnel everyone who did something
// between two of its steps: for example, people who contacted support
// between signing up and paying. The thing is described like a step.
type Exclusion struct {
	Kind       string   `json:"kind"`
	Event      string   `json:"event,omitempty"`
	Conversion string   `json:"conversion,omitempty"`
	Filters    []Filter `json:"filters,omitempty"`
	// From and To are step numbers, counted from 1, From before To. The
	// person is dropped when they did it after their step From and before
	// their step To (or, when they never reached To, before step 1's
	// deadline).
	From int `json:"from"`
	To   int `json:"to"`
}

// MaxExclusions is how many exclusions a funnel takes.
const MaxExclusions = 5

// Funnel groupings: who a row of the funnel is.
const (
	ByPerson  = "person"
	ByCompany = "company"
)

// Funnel is a people funnel.
type Funnel struct {
	Steps []Step `json:"steps"`
	// Exclusions drop people who did something between two steps.
	Exclusions []Exclusion `json:"exclusions,omitempty"`
	// By is person (the default) or company: a company funnel counts each
	// company once, through the people linked to it, and a company reaches
	// a step when any of its people does. Browsers nobody has identified
	// belong to no company, so they drop out.
	By string `json:"by,omitempty"`
	// From and To bound when people did step 1: From inclusive, To
	// exclusive. Later steps may happen after To, within their limits.
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	// Window is the overall limit from step 1; DefaultWindow when unset.
	Window *Window `json:"window,omitempty"`
	// Breakdown splits people by a field of their step 1 (Breakdowns).
	Breakdown string `json:"breakdown,omitempty"`
	// Order is strict (the default): people do the steps in this order.
	// With any, they do them in any order: step 1 is whichever they did
	// first in the dates, and step N means N of the steps done within the
	// window, so the chart counts how many of the steps people got
	// through. Any-order funnels take no exclusions and no per-step time
	// limits, and a breakdown needs every step to be of the same kind.
	Order string `json:"order,omitempty"`
	// Interval adds conversion over time by when people did step 1:
	// day, week or month.
	Interval string    `json:"interval,omitempty"`
	AdClicks *AdClicks `json:"ad_clicks,omitempty"`
}

// Funnel orders.
const (
	OrderStrict = "strict"
	OrderAny    = "any"
)

// Intervals for conversion over time.
var Intervals = []string{"day", "week", "month"}

// Limits.
const (
	MinSteps  = 2
	MaxSteps  = 10
	MaxRange  = 366 * 24 * time.Hour
	MaxGroups = 25
)

// Invalid is a spec the caller can fix; its message says how.
type Invalid string

func (e Invalid) Error() string { return string(e) }

// EventFields are the built-in fields of an event or page view: its page
// (from site.js or a page call) and the campaign tags it arrived with.
// param:<name> reads one parameter of the page address.
var EventFields = map[string]string{
	"url":          `e.context #>> '{page,url}'`,
	"path":         `e.context #>> '{page,path}'`,
	"title":        `e.context #>> '{page,title}'`,
	"referrer":     `e.context #>> '{page,referrer}'`,
	"host":         `lower(substring(e.context #>> '{page,url}' from '^[a-zA-Z]+://([^/:?#]+)'))`,
	"utm_source":   `e.context #>> '{campaign,source}'`,
	"utm_medium":   `e.context #>> '{campaign,medium}'`,
	"utm_campaign": `e.context #>> '{campaign,name}'`,
	"utm_term":     `e.context #>> '{campaign,term}'`,
	"utm_content":  `e.context #>> '{campaign,content}'`,
	"source":       `e.source`,
}

// VisitFields are what a visit can be filtered and broken down by.
var VisitFields = map[string]string{
	"channel":       `NULLIF(s.channel, '')`,
	"source":        `NULLIF(s.source, '')`,
	"campaign":      `NULLIF(s.campaign, '')`,
	"network":       networkExpr,
	"utm_source":    `NULLIF(s.utm_source, '')`,
	"utm_medium":    `NULLIF(s.utm_medium, '')`,
	"utm_campaign":  `NULLIF(s.utm_campaign, '')`,
	"utm_term":      `NULLIF(s.utm_term, '')`,
	"utm_content":   `NULLIF(s.utm_content, '')`,
	"landing_path":  `NULLIF(s.landing_path, '')`,
	"landing_url":   `NULLIF(s.landing_url, '')`,
	"referrer_host": `NULLIF(s.referrer_host, '')`,
	"device":        `NULLIF(s.device, '')`,
	"country":       `NULLIF(s.country, '')`,
	"browser":       `NULLIF(s.browser, '')`,
	"os":            `NULLIF(s.os, '')`,
	// Where the visit came from and on what, in more detail; empty on
	// visits from before they were kept.
	"region":          `NULLIF(s.region, '')`,
	"city":            `NULLIF(s.city, '')`,
	"isp":             `NULLIF(s.isp, '')`,
	"asn":             `NULLIF(s.asn, 0)::text`,
	"device_brand":    `NULLIF(s.device_brand, '')`,
	"device_model":    `NULLIF(s.device_model, '')`,
	"os_version":      `NULLIF(s.os_version, '')`,
	"browser_version": `NULLIF(s.browser_version, '')`,
	"language":        `NULLIF(s.language, '')`,
	"partner":         `NULLIF(s.ref, '')`,
	// The network's own ids, as the tracking template recorded them.
	"ad_campaign": `NULLIF(s.ad ->> 'campaign', '')`,
	"ad_set":      `NULLIF(s.ad ->> 'adset', '')`,
	"ad":          `NULLIF(s.ad ->> 'ad', '')`,
}

// PaymentFields are what a payment can be filtered and broken down by.
// amount is in the brand's currency's major unit (49.00).
var PaymentFields = map[string]string{
	"amount":   `(t.brand_amount_minor::numeric / 100)`,
	"product":  `(SELECT COALESCE(NULLIF(pr.key, ''), pr.name) FROM products pr WHERE pr.id = t.product_id)`,
	"source":   `t.source`,
	"currency": `t.currency`,
}

// networkExpr is the ad network whose click id a visit carried, in the
// order attribution checks them.
var networkExpr = func() string {
	var b strings.Builder
	b.WriteString(`CASE`)
	for _, c := range adtrack.ClickIDs {
		if c.Channel != "paid_ads" {
			continue
		}
		fmt.Fprintf(&b, ` WHEN s.click_ids ? '%s' THEN '%s'`, c.Param, c.Network)
	}
	b.WriteString(` END`)
	return b.String()
}()

// Breakdowns lists what a funnel starting with a step of kind can be
// broken down by. Events and page views also take property:<key>.
func Breakdowns(kind string) []string {
	var m map[string]string
	switch kind {
	case KindVisit:
		m = VisitFields
	case KindPayment:
		m = PaymentFields
	default:
		m = EventFields
	}
	out := make([]string, 0, len(m))
	for k := range m {
		if k == "amount" {
			continue
		}
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Label is the step's name: its own, or one from its kind.
func (s Step) DefaultLabel(conversionName string) string {
	if s.Label != "" {
		return s.Label
	}
	switch s.Kind {
	case KindPage:
		return "Viewed a page"
	case KindVisit:
		return "Visited the website"
	case KindConversion:
		if conversionName != "" {
			return conversionName
		}
		return s.Conversion
	case KindPayment:
		if s.First {
			return "First payment"
		}
		return "Paid"
	}
	if s.First {
		return s.Event + " (first time)"
	}
	return s.Event
}

var eventName = func(s string) bool {
	if s == "" || len(s) > 100 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

func validWindow(w *Window, step, field string) error {
	if w == nil {
		return nil
	}
	if !slices.Contains(Units, w.Unit) || w.Value < 1 || w.duration() > MaxWindow {
		return Invalid(fmt.Sprintf("%s is a time limit: value 1 or more in minute, hour, day or week, at most 365 days", field))
	}
	return nil
}

// Validate checks a funnel's shape without the database.
func (f Funnel) Validate() error {
	if len(f.Steps) < MinSteps || len(f.Steps) > MaxSteps {
		return Invalid(fmt.Sprintf("steps has %d to %d steps", MinSteps, MaxSteps))
	}
	if f.From.IsZero() || f.To.IsZero() || !f.To.After(f.From) || f.To.Sub(f.From) > MaxRange {
		return Invalid("from and to are dates, from before to, at most 366 days apart")
	}
	if err := validWindow(f.Window, "", "window"); err != nil {
		return err
	}
	if f.Interval != "" && !slices.Contains(Intervals, f.Interval) {
		return Invalid("interval is day, week or month")
	}
	for i, s := range f.Steps {
		at := fmt.Sprintf("steps[%d]", i)
		switch s.Kind {
		case KindEvent:
			if !eventName(s.Event) {
				return Invalid(at + ".event is an event's catalog name, such as signed_up")
			}
		case KindConversion:
			if s.Conversion == "" {
				return Invalid(at + ".conversion is a conversion definition's key (GET /v1/brands/{brand}/conversion-definitions)")
			}
		case KindPage, KindVisit, KindPayment:
		default:
			return Invalid(at + ".kind is event, page, visit, conversion or payment")
		}
		if s.First && s.Kind != KindEvent && s.Kind != KindPayment {
			return Invalid(at + ".first only applies to event and payment steps")
		}
		if len(s.Label) > 80 {
			return Invalid(at + ".label is at most 80 characters")
		}
		if len(s.Filters) > 20 {
			return Invalid(at + ".filters has at most 20 filters")
		}
		for j, fl := range s.Filters {
			if err := validFilter(s.Kind, fl, fmt.Sprintf("%s.filters[%d]", at, j)); err != nil {
				return err
			}
		}
		if err := validWindow(s.Within, at, at+".within"); err != nil {
			return err
		}
		if s.Within != nil && s.Within.From != nil && (*s.Within.From < 1 || *s.Within.From > i) {
			return Invalid(fmt.Sprintf("%s.within.from is an earlier step's number, 1 to %d", at, i))
		}
		if i == 0 && s.Within != nil {
			return Invalid("steps[0].within: the first step has no time limit; set the funnel's dates")
		}
	}
	if f.Breakdown != "" {
		if _, err := breakdownExpr(f.Steps[0].Kind, f.Breakdown); err != nil {
			return err
		}
	}
	if f.By != "" && f.By != ByPerson && f.By != ByCompany {
		return Invalid("by is person or company")
	}
	switch f.Order {
	case "", OrderStrict:
	case OrderAny:
		if len(f.Exclusions) > 0 {
			return Invalid("exclusions need steps in order: an any-order funnel has no between")
		}
		for i, s := range f.Steps {
			if s.Within != nil {
				return Invalid(fmt.Sprintf("steps[%d].within needs steps in order; an any-order funnel has only the overall window", i))
			}
			if f.Breakdown != "" && s.Kind != f.Steps[0].Kind {
				return Invalid("breakdown in an any-order funnel needs every step to be of the same kind, because any step can be the first")
			}
		}
	default:
		return Invalid("order is strict or any")
	}
	if len(f.Exclusions) > MaxExclusions {
		return Invalid(fmt.Sprintf("exclusions has at most %d exclusions", MaxExclusions))
	}
	for i, x := range f.Exclusions {
		at := fmt.Sprintf("exclusions[%d]", i)
		switch x.Kind {
		case KindEvent:
			if !eventName(x.Event) {
				return Invalid(at + ".event is an event's catalog name, such as support_contacted")
			}
		case KindConversion:
			if x.Conversion == "" {
				return Invalid(at + ".conversion is a conversion definition's key")
			}
		case KindPage, KindVisit, KindPayment:
		default:
			return Invalid(at + ".kind is event, page, visit, conversion or payment")
		}
		if len(x.Filters) > 20 {
			return Invalid(at + ".filters has at most 20 filters")
		}
		for j, fl := range x.Filters {
			if err := validFilter(x.Kind, fl, fmt.Sprintf("%s.filters[%d]", at, j)); err != nil {
				return err
			}
		}
		if x.From < 1 || x.To <= x.From || x.To > len(f.Steps) {
			return Invalid(fmt.Sprintf("%s.from and .to are step numbers, from before to, 1 to %d", at, len(f.Steps)))
		}
	}
	if f.AdClicks != nil && f.AdClicks.Network != "" {
		if _, ok := networkByID(f.AdClicks.Network); !ok {
			return Invalid("ad_clicks.network is an ad network id, such as google, meta or x")
		}
	}
	if f.AdClicks != nil && len(f.AdClicks.Campaign) > 200 {
		return Invalid("ad_clicks.campaign is at most 200 characters")
	}
	return nil
}

func networkByID(id string) (adtrack.Network, bool) {
	for _, n := range adtrack.Networks {
		if n.ID == id {
			return n, true
		}
	}
	return adtrack.Network{}, false
}

func validFilter(kind string, f Filter, at string) error {
	if !slices.Contains(Ops, f.Op) {
		return Invalid(at + ".op is one of " + strings.Join(Ops, ", "))
	}
	if f.Property == "" || len(f.Property) > 200 {
		return Invalid(at + ".property names what to compare")
	}
	if strings.HasPrefix(f.Property, ExperimentPrefix) {
		var a args
		if _, err := experimentExpr(kind, strings.TrimPrefix(f.Property, ExperimentPrefix), &a); err != nil {
			return err
		}
	} else {
		switch kind {
		case KindVisit:
			if _, ok := VisitFields[f.Property]; !ok {
				return Invalid(at + ".property for a visit is one of " + strings.Join(Breakdowns(KindVisit), ", "))
			}
		case KindPayment:
			if _, ok := PaymentFields[f.Property]; !ok {
				return Invalid(at + ".property for a payment is amount, product, source or currency")
			}
		}
	}
	switch f.Op {
	case OpExists, OpNotExists:
	case OpIn:
		list, ok := f.Value.([]any)
		if !ok || len(list) == 0 || len(list) > 100 {
			return Invalid(at + ".value is a list of 1 to 100 values for in")
		}
	case OpGt, OpGte, OpLt, OpLte:
		if _, ok := number(f.Value); !ok {
			return Invalid(at + ".value is a number for " + f.Op)
		}
	default:
		if f.Value == nil {
			return Invalid(at + ".value is what to compare with")
		}
		if s := fmt.Sprint(f.Value); len(s) > 500 {
			return Invalid(at + ".value is at most 500 characters")
		}
	}
	return nil
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

func text(v any) string {
	if f, ok := v.(float64); ok {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

// args collects query parameters and hands back their placeholders.
type args struct{ list []any }

func (a *args) add(v any) string {
	a.list = append(a.list, v)
	return "$" + strconv.Itoa(len(a.list))
}

// fieldExpr is the SQL for a filter's or breakdown's property on a row of
// a step of kind. Property keys go in as parameters, never as SQL.
func fieldExpr(kind, property string, a *args) (string, error) {
	if flag, ok := strings.CutPrefix(property, ExperimentPrefix); ok {
		return experimentExpr(kind, flag, a)
	}
	switch kind {
	case KindVisit:
		if x, ok := VisitFields[property]; ok {
			return x, nil
		}
	case KindPayment:
		if x, ok := PaymentFields[property]; ok {
			return x, nil
		}
	default:
		if x, ok := EventFields[property]; ok {
			return x, nil
		}
		if p, ok := strings.CutPrefix(property, "param:"); ok && p != "" {
			// One parameter of the page address, decoded no further.
			return `(SELECT split_part(kv, '=', 2) FROM unnest(string_to_array(ltrim(e.context #>> '{page,search}', '?'), '&')) kv
				WHERE split_part(kv, '=', 1) = ` + a.add(p) + ` LIMIT 1)`, nil
		}
		path := strings.TrimPrefix(property, "property:")
		if path == "" {
			break
		}
		return `(e.properties #>> ` + a.add(strings.Split(path, ".")) + `::text[])`, nil
	}
	return "", Invalid(fmt.Sprintf("%q isn't a field of a %s step", property, kind))
}

// ExperimentPrefix names an A/B test's flag as a field: experiment:<flag
// key> is the version the person was first shown (from the website
// script's Experiment Viewed), or "" when they weren't in it, so funnels
// and trends can be split or filtered by test version.
const ExperimentPrefix = "experiment:"

// exposureEvent is Experiment Viewed's catalog name (abtests.ExposureEvent).
const exposureEvent = "experiment_viewed"

var flagKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func experimentExpr(kind, flag string, a *args) (string, error) {
	if !flagKeyPattern.MatchString(flag) {
		return "", Invalid("experiment:<flag> names a flag by its key, such as experiment:pricing-headline")
	}
	var match string
	switch kind {
	case KindVisit:
		match = `x.workspace_id = s.workspace_id AND x.anonymous_id = s.anonymous_id`
	case KindPayment:
		match = `x.workspace_id = t.workspace_id AND x.record_id = t.record_id`
	case KindEvent, KindPage, KindConversion:
		match = `x.workspace_id = e.workspace_id AND x.anonymous_id = e.anonymous_id`
	default:
		return "", Invalid("an A/B test's version splits events, page views, conversions, visits and payments")
	}
	return `(SELECT x.properties->>'variant' FROM customer_events x WHERE ` + match + ` AND x.name = '` + exposureEvent + `'
		AND x.properties->>'flag' = ` + a.add(flag) + ` ORDER BY x.occurred_at LIMIT 1)`, nil
}

func breakdownExpr(kind, breakdown string) (string, error) {
	if strings.HasPrefix(breakdown, ExperimentPrefix) {
		var a args
		return experimentExpr(kind, strings.TrimPrefix(breakdown, ExperimentPrefix), &a)
	}
	if kind == KindEvent || kind == KindPage || kind == KindConversion {
		if !strings.HasPrefix(breakdown, "property:") && !strings.HasPrefix(breakdown, "param:") {
			if _, ok := EventFields[breakdown]; !ok {
				return "", Invalid("breakdown for a first step of this kind is one of " + strings.Join(Breakdowns(kind), ", ") + ", param:<name> or property:<key>")
			}
		}
		return "", nil
	}
	if breakdown == "amount" {
		return "", Invalid("breakdown can't be amount")
	}
	var a args
	return fieldExpr(kind, breakdown, &a)
}

func filterSQL(kind string, f Filter, a *args) (string, error) {
	x, err := fieldExpr(kind, f.Property, a)
	if err != nil {
		return "", err
	}
	if kind == KindVisit && f.Property == "network" && (f.Op == OpEq || f.Op == OpNeq) {
		// By the network's click ids, so the index on click_ids can help.
		var params []string
		for _, c := range adtrack.ClickIDs {
			if c.Network == text(f.Value) {
				params = append(params, c.Param)
			}
		}
		cond := `s.click_ids ?| ` + a.add(params) + `::text[]`
		if f.Op == OpNeq {
			cond = `NOT (` + cond + `)`
		}
		return cond, nil
	}
	num := `(CASE WHEN (` + x + `) ~ '^\s*-?[0-9]+(\.[0-9]+)?\s*$' THEN trim(` + x + `)::numeric END)`
	switch f.Op {
	case OpExists:
		return `NULLIF(` + x + `, '') IS NOT NULL`, nil
	case OpNotExists:
		return `NULLIF(` + x + `, '') IS NULL`, nil
	case OpEq:
		return `lower(` + x + `) = lower(` + a.add(text(f.Value)) + `)`, nil
	case OpNeq:
		return `lower(` + x + `) IS DISTINCT FROM lower(` + a.add(text(f.Value)) + `)`, nil
	case OpContains:
		return `strpos(lower(` + x + `), lower(` + a.add(text(f.Value)) + `)) > 0`, nil
	case OpNotContains:
		return `COALESCE(strpos(lower(` + x + `), lower(` + a.add(text(f.Value)) + `)), 0) = 0`, nil
	case OpIn:
		list, _ := f.Value.([]any)
		vals := make([]string, len(list))
		for i, v := range list {
			vals[i] = strings.ToLower(text(v))
		}
		return `lower(` + x + `) = ANY (` + a.add(vals) + `::text[])`, nil
	}
	n, _ := number(f.Value)
	op := map[string]string{OpGt: ">", OpGte: ">=", OpLt: "<", OpLte: "<="}[f.Op]
	return num + ` ` + op + ` ` + a.add(n) + `::numeric`, nil
}

// conversion is a saved conversion definition a step names.
type conversion struct {
	Name, Event string
	Filters     []Filter
}

// loadConversion reads the conversion a step or series of kind names;
// nothing for other kinds. at names the step in the error.
func loadConversion(ctx context.Context, q Querier, ws, brand, kind, key, at string) (conversion, error) {
	var conv conversion
	if kind != KindConversion {
		return conv, nil
	}
	var raw []byte
	// A definition is its brand's; to_jsonb reads brand_id, which a later
	// migration adds, and until then every definition counts for every brand.
	err := q.QueryRow(ctx, `SELECT name, event_name, filters FROM conversion_definitions d
		WHERE workspace_id = $1 AND COALESCE(to_jsonb(d) ->> 'brand_id', $3::text) = $3::text AND key = $2 AND archived_at IS NULL`, ws, key, brand).
		Scan(&conv.Name, &conv.Event, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return conv, Invalid(fmt.Sprintf("%s.conversion %q isn't a saved conversion of this brand", at, key))
	}
	if err != nil {
		return conv, err
	}
	if err := json.Unmarshal(raw, &conv.Filters); err != nil {
		return conv, fmt.Errorf("insights: conversion %s filters: %w", key, err)
	}
	return conv, nil
}

// rowSource is the rows one step or series counts: the table, the
// person and time of each row, and the conditions that pick them.
type rowSource struct {
	from, actor, ts string
	where           []string
}

// source builds the rows of kind between from and to (placeholders), with
// the filters applied. first keeps only each person's first ever one.
func source(kind, event string, conv conversion, filters []Filter, first bool, from, to, pWS, pBrand string, a *args) (rowSource, error) {
	var src rowSource
	switch kind {
	case KindEvent, KindPage, KindConversion:
		name := event
		switch kind {
		case KindPage:
			name = "page.viewed"
		case KindConversion:
			name = conv.Event
			filters = append(slices.Clone(conv.Filters), filters...)
		}
		src = rowSource{from: `customer_events e`, actor: `COALESCE(e.record_id::text, 'a:' || e.anonymous_id)`, ts: `e.occurred_at`}
		src.where = append(src.where, `e.workspace_id = `+pWS, `e.brand_id = `+pBrand, `e.name = `+a.add(name),
			`(e.record_id IS NOT NULL OR e.anonymous_id IS NOT NULL)`,
			`e.occurred_at >= `+from, `e.occurred_at < `+to)
		if first {
			src.where = append(src.where, `NOT EXISTS (SELECT 1 FROM customer_events e0 WHERE e0.workspace_id = e.workspace_id
				AND e0.brand_id = e.brand_id AND e0.name = e.name AND e0.occurred_at < e.occurred_at
				AND ((e.record_id IS NOT NULL AND e0.record_id = e.record_id)
					OR (e.record_id IS NULL AND e0.record_id IS NULL AND e0.anonymous_id = e.anonymous_id)))`)
		}
		for _, fl := range filters {
			w, err := filterSQL(KindEvent, fl, a)
			if err != nil {
				return src, err
			}
			src.where = append(src.where, w)
		}
	case KindVisit:
		src = rowSource{from: `site_sessions s`, actor: `COALESCE(s.record_id::text, 'a:' || s.anonymous_id)`, ts: `s.started_at`}
		src.where = append(src.where, `s.workspace_id = `+pWS, `s.brand_id = `+pBrand, `s.started_at >= `+from, `s.started_at < `+to)
		for _, fl := range filters {
			w, err := filterSQL(KindVisit, fl, a)
			if err != nil {
				return src, err
			}
			src.where = append(src.where, w)
		}
	case KindPayment:
		src = rowSource{from: `transactions t`, actor: `t.record_id::text`, ts: `t.occurred_at`}
		src.where = append(src.where, `t.workspace_id = `+pWS, `t.brand_id = `+pBrand, `t.kind = 'payment'`, `t.status = 'succeeded'`,
			`t.record_id IS NOT NULL`, `t.occurred_at >= `+from, `t.occurred_at < `+to)
		if first {
			src.where = append(src.where, `NOT EXISTS (SELECT 1 FROM transactions t0 WHERE t0.brand_id = t.brand_id AND t0.record_id = t.record_id
				AND t0.kind = 'payment' AND t0.status = 'succeeded' AND (t0.occurred_at, t0.id) < (t.occurred_at, t.id))`)
		}
		for _, fl := range filters {
			w, err := filterSQL(KindPayment, fl, a)
			if err != nil {
				return src, err
			}
			src.where = append(src.where, w)
		}
	default:
		return src, Invalid("kind is one of " + strings.Join(Kinds, ", "))
	}
	return src, nil
}

// Compiled is a funnel turned into SQL: the CTEs that give each person's
// time at every step, ready for the counts or the people.
type Compiled struct {
	ws, brand string
	f         Funnel
	labels    []string
	with      string
	args      args
}

// Labels are the steps' names, in order.
func (c *Compiled) Labels() []string { return c.labels }

// Compile turns a validated funnel into SQL kept to workspace ws and
// brand. Conversion steps read their definitions from q.
func Compile(ctx context.Context, q Querier, ws, brand string, f Funnel) (*Compiled, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	c := &Compiled{ws: ws, brand: brand, f: f}
	a := &c.args
	pWS, pBrand := a.add(ws), a.add(brand)
	overall := DefaultWindow
	if f.Window != nil {
		overall = f.Window.duration()
	}
	pFrom, pTo := a.add(f.From), a.add(f.To)
	pOverall := a.add(fmt.Sprintf("%d seconds", int64(overall.Seconds())))
	// Later steps can't happen after the last deadline.
	pEnd := a.add(f.To.Add(overall))

	anyOrder := f.Order == OrderAny
	var ctes []string
	for i, s := range f.Steps {
		n := i + 1
		from, to := pFrom, pEnd
		if i == 0 && !anyOrder {
			to = pTo
		}
		conv, err := loadConversion(ctx, q, ws, brand, s.Kind, s.Conversion, fmt.Sprintf("steps[%d]", i))
		if err != nil {
			return nil, err
		}
		c.labels = append(c.labels, s.DefaultLabel(conv.Name))
		if anyOrder {
			c.labels[i] = anyOrderLabel(n, len(f.Steps))
		}

		bk := `''`
		if (i == 0 || anyOrder) && f.Breakdown != "" {
			x, err := fieldExpr(s.Kind, f.Breakdown, a)
			if err != nil {
				return nil, err
			}
			bk = `COALESCE(NULLIF(left((` + x + `)::text, 200), ''), '')`
		}
		src, err := source(s.Kind, s.Event, conv, s.Filters, s.First, from, to, pWS, pBrand, a)
		if err != nil {
			return nil, err
		}
		ctes = append(ctes, fmt.Sprintf("s%d AS (%s)", n, c.rows(src, bk, pWS)))
	}
	for i, x := range f.Exclusions {
		conv, err := loadConversion(ctx, q, ws, brand, x.Kind, x.Conversion, fmt.Sprintf("exclusions[%d]", i))
		if err != nil {
			return nil, err
		}
		src, err := source(x.Kind, x.Event, conv, x.Filters, false, pFrom, pEnd, pWS, pBrand, a)
		if err != nil {
			return nil, err
		}
		ctes = append(ctes, fmt.Sprintf("x%d AS (%s)", i+1, c.rows(src, `''`, pWS)))
	}

	if anyOrder {
		c.compileAnyOrder(&ctes, pTo, pOverall)
		return c, nil
	}

	// Each person's first step 1 in the dates, then the first time they
	// did each later step after the one before, inside its limits.
	// g1 is everyone at step 1; gN is everyone who reached step N, with
	// their times at every step so far, so a limit can run from any of
	// them. Each is a hash join on the person, not a scan per person.
	ctes = append(ctes, `g1 AS (SELECT DISTINCT ON (actor) actor, bk, ts AS t1 FROM s1 ORDER BY actor, ts)`)
	carried := []string{`p.actor`, `p.bk`, `p.t1`}
	cols := []string{`g1.actor`, `g1.bk`, `g1.t1`}
	var joins []string
	for i := 1; i < len(f.Steps); i++ {
		n := i + 1
		s := f.Steps[i]
		prev := fmt.Sprintf(`p.t%d`, i)
		deadline := `p.t1 + ` + pOverall + `::interval`
		if s.Within != nil {
			anchor := prev
			if s.Within.From != nil {
				anchor = fmt.Sprintf(`p.t%d`, *s.Within.From)
			}
			deadline = `LEAST(` + deadline + `, ` + anchor + ` + ` + a.add(fmt.Sprintf("%d seconds", int64(s.Within.duration().Seconds()))) + `::interval)`
		}
		after := `>=`
		if sameStep(f.Steps[i-1], s) {
			after = `>`
		}
		ctes = append(ctes, fmt.Sprintf(`g%d AS (SELECT %s, min(x.ts) AS t%d FROM g%d p JOIN s%d x ON x.actor = p.actor AND x.ts %s %s AND x.ts <= %s GROUP BY %s)`,
			n, strings.Join(carried, ", "), n, i, n, after, prev, deadline, strings.Join(carried, ", ")))
		carried = append(carried, fmt.Sprintf(`p.t%d`, n))
		cols = append(cols, fmt.Sprintf(`g%d.t%d`, n, n))
		joins = append(joins, fmt.Sprintf(`LEFT JOIN g%d ON g%d.actor = g1.actor`, n, n))
	}
	// An exclusion removes the person from every step: the funnel is
	// about everyone else.
	var excluded []string
	for i, x := range f.Exclusions {
		after := fmt.Sprintf(`g%d.t%d`, x.From, x.From)
		before := fmt.Sprintf(`COALESCE(g%d.t%d, g1.t1 + %s::interval)`, x.To, x.To, pOverall)
		excluded = append(excluded, fmt.Sprintf(`NOT EXISTS (SELECT 1 FROM x%d WHERE x%d.actor = g1.actor AND x%d.ts > %s AND x%d.ts < %s)`, i+1, i+1, i+1, after, i+1, before))
	}
	where := ""
	if len(excluded) > 0 {
		where = ` WHERE ` + strings.Join(excluded, " AND ")
	}
	ctes = append(ctes, `f AS (SELECT `+strings.Join(cols, ", ")+` FROM g1 `+strings.Join(joins, " ")+where+`)`)
	c.with = `WITH ` + strings.Join(ctes, ",\n")
	return c, nil
}

// anyOrderLabel names step n of an any-order funnel: how many of the
// steps the person has done.
func anyOrderLabel(n, total int) string {
	switch {
	case n == total:
		return fmt.Sprintf("All %d steps", total)
	case n == 1:
		return "1 step"
	}
	return fmt.Sprintf("%d steps", n)
}

// compileAnyOrder finishes an any-order funnel: step 1 is each person's
// first of any step in the dates; then each step's first time from then
// on, within the window, ranked by time, so tK is when they had done K
// of the steps. The result has the same shape as an ordered funnel's f.
func (c *Compiled) compileAnyOrder(ctes *[]string, pTo, pOverall string) {
	n := len(c.f.Steps)
	var union []string
	for i := 1; i <= n; i++ {
		union = append(union, fmt.Sprintf(`SELECT actor, ts, bk, %d AS step FROM s%d`, i, i))
	}
	*ctes = append(*ctes, `allsteps AS (`+strings.Join(union, " UNION ALL ")+`)`)
	*ctes = append(*ctes, `g1 AS (SELECT DISTINCT ON (actor) actor, bk, ts AS t1 FROM allsteps WHERE ts < `+pTo+` ORDER BY actor, ts)`)
	*ctes = append(*ctes, `d AS (SELECT p.actor, x.step, min(x.ts) AS ts FROM g1 p JOIN allsteps x ON x.actor = p.actor AND x.ts >= p.t1 AND x.ts <= p.t1 + `+pOverall+`::interval GROUP BY p.actor, x.step)`)
	*ctes = append(*ctes, `r AS (SELECT actor, ts, row_number() OVER (PARTITION BY actor ORDER BY ts, step) AS k FROM d)`)
	cols := []string{`g1.actor`, `g1.bk`, `g1.t1`}
	var joins []string
	for k := 2; k <= n; k++ {
		cols = append(cols, fmt.Sprintf(`r%d.ts AS t%d`, k, k))
		joins = append(joins, fmt.Sprintf(`LEFT JOIN r r%d ON r%d.actor = g1.actor AND r%d.k = %d`, k, k, k, k))
	}
	*ctes = append(*ctes, `f AS (SELECT `+strings.Join(cols, ", ")+` FROM g1 `+strings.Join(joins, " ")+`)`)
	c.with = `WITH ` + strings.Join(*ctes, ",\n")
}

// rows selects a source's rows as actor, ts, bk. A company funnel swaps
// each identified person for the companies they are linked to.
func (c *Compiled) rows(src rowSource, bk, pWS string) string {
	sel := `SELECT ` + src.actor + ` AS actor, ` + src.ts + ` AS ts, ` + bk + ` AS bk FROM ` + src.from + ` WHERE ` + strings.Join(src.where, " AND ")
	if c.f.By != ByCompany {
		return sel
	}
	return `SELECT DISTINCT c.to_id::text AS actor, p.ts, p.bk FROM (` + sel + `) p
		JOIN record_relationships c ON c.workspace_id = ` + pWS + ` AND c.kind = 'companies' AND c.valid_to IS NULL
			AND c.from_id = (CASE WHEN p.actor LIKE 'a:%' THEN NULL ELSE p.actor END)::uuid
		WHERE p.actor NOT LIKE 'a:%'`
}

func sameStep(a, b Step) bool {
	a.Within, b.Within, a.Label, b.Label = nil, nil, "", ""
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// StepResult is one step's numbers.
type StepResult struct {
	Label string `json:"label"`
	// Count is how many people reached the step.
	Count int `json:"count"`
	// Rates are 0 to 1: of the people at step 1, and at the step before.
	ConversionFromStart    float64 `json:"conversion_from_start"`
	ConversionFromPrevious float64 `json:"conversion_from_previous"`
	// Dropped is how many reached the step before but not this one.
	Dropped int `json:"dropped"`
	// Times from the step before, in seconds; null on step 1 and when
	// nobody reached the step.
	MedianSeconds  *float64 `json:"median_seconds"`
	AverageSeconds *float64 `json:"average_seconds"`
}

// Group is one breakdown value's steps.
type Group struct {
	// Value is the step 1 field's value, "" when it had none, or
	// "$other" for every value past the biggest MaxGroups.
	Value string       `json:"value"`
	Steps []StepResult `json:"steps"`
}

// Period is how the people who did step 1 in one interval converted.
type Period struct {
	Start  time.Time `json:"start"`
	Counts []int     `json:"counts"`
}

// Money is an amount in a currency's minor unit.
type Money struct {
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
}

// AdClickResult is what the ad networks reported over the dates.
type AdClickResult struct {
	Clicks      int64   `json:"clicks"`
	Impressions int64   `json:"impressions"`
	Spend       []Money `json:"spend"`
	// Campaigns is how many campaigns the numbers came from.
	Campaigns int `json:"campaigns"`
}

// Result is a funnel's answer.
type Result struct {
	Steps     []StepResult   `json:"steps"`
	Breakdown []Group        `json:"breakdown"`
	OverTime  []Period       `json:"over_time"`
	AdClicks  *AdClickResult `json:"ad_clicks"`
}

func steps(labels []string, counts []int, medians, averages []*float64) []StepResult {
	out := make([]StepResult, len(labels))
	for i := range labels {
		r := StepResult{Label: labels[i], Count: counts[i]}
		if counts[0] > 0 {
			r.ConversionFromStart = float64(counts[i]) / float64(counts[0])
		}
		if i == 0 {
			r.ConversionFromPrevious = 1
			if counts[0] == 0 {
				r.ConversionFromPrevious = 0
			}
		} else {
			r.Dropped = counts[i-1] - counts[i]
			if counts[i-1] > 0 {
				r.ConversionFromPrevious = float64(counts[i]) / float64(counts[i-1])
			}
			r.MedianSeconds, r.AverageSeconds = medians[i], averages[i]
		}
		out[i] = r
	}
	return out
}

// Run answers a compiled funnel.
func (c *Compiled) Run(ctx context.Context, q Querier) (Result, error) {
	n := len(c.f.Steps)
	var sel []string
	for i := 1; i <= n; i++ {
		sel = append(sel, fmt.Sprintf(`count(t%d)::int`, i))
	}
	for i := 2; i <= n; i++ {
		d := fmt.Sprintf(`extract(epoch FROM t%d - t%d)`, i, i-1)
		sel = append(sel, `(percentile_cont(0.5) WITHIN GROUP (ORDER BY `+d+`))::float8`, `avg(`+d+`)::float8`)
	}
	a := args{list: slices.Clone(c.args.list)}
	period := `NULL::timestamptz`
	if c.f.Interval != "" {
		period = `date_trunc(` + a.add(c.f.Interval) + `, t1 AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`
	}
	sql := c.with + `
		SELECT GROUPING(bk)::int, GROUPING(p)::int, bk, p, ` + strings.Join(sel, ", ") + `
		FROM (SELECT f.*, ` + period + ` AS p FROM f) g
		GROUP BY GROUPING SETS ((), (bk), (p))`
	rows, err := q.Query(ctx, sql, a.list...)
	if err != nil {
		return Result{}, err
	}
	defer rows.Close()
	res := Result{Breakdown: []Group{}, OverTime: []Period{}}
	for rows.Next() {
		var gbk, gp int
		var bk *string
		var p *time.Time
		counts := make([]int, n)
		medians := make([]*float64, n)
		averages := make([]*float64, n)
		dest := []any{&gbk, &gp, &bk, &p}
		for i := range counts {
			dest = append(dest, &counts[i])
		}
		for i := 1; i < n; i++ {
			dest = append(dest, &medians[i], &averages[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return Result{}, err
		}
		switch {
		case gbk == 1 && gp == 1:
			res.Steps = steps(c.labels, counts, medians, averages)
		case gbk == 0 && c.f.Breakdown != "":
			v := ""
			if bk != nil {
				v = *bk
			}
			res.Breakdown = append(res.Breakdown, Group{Value: v, Steps: steps(c.labels, counts, medians, averages)})
		case gp == 0 && p != nil:
			res.OverTime = append(res.OverTime, Period{Start: p.UTC(), Counts: counts})
		}
	}
	if err := rows.Err(); err != nil {
		return Result{}, err
	}
	if res.Steps == nil {
		res.Steps = steps(c.labels, make([]int, n), make([]*float64, n), make([]*float64, n))
	}
	res.Breakdown = topGroups(res.Breakdown, c.labels)
	slices.SortFunc(res.OverTime, func(x, y Period) int { return x.Start.Compare(y.Start) })
	if c.f.AdClicks != nil {
		ac, err := adClicks(ctx, q, c.ws, c.brand, c.f)
		if err != nil {
			return Result{}, err
		}
		res.AdClicks = &ac
	}
	return res, nil
}

// topGroups keeps the biggest MaxGroups by step 1 and adds the rest up as
// "$other". Medians can't be added up, so other has none.
func topGroups(groups []Group, labels []string) []Group {
	slices.SortStableFunc(groups, func(x, y Group) int {
		if x.Steps[0].Count != y.Steps[0].Count {
			return y.Steps[0].Count - x.Steps[0].Count
		}
		return strings.Compare(x.Value, y.Value)
	})
	if len(groups) <= MaxGroups {
		return groups
	}
	counts := make([]int, len(labels))
	for _, g := range groups[MaxGroups:] {
		for i, s := range g.Steps {
			counts[i] += s.Count
		}
	}
	other := Group{Value: "$other", Steps: steps(labels, counts, make([]*float64, len(labels)), make([]*float64, len(labels)))}
	return append(groups[:MaxGroups:MaxGroups], other)
}

func adClicks(ctx context.Context, q Querier, ws, brand string, f Funnel) (AdClickResult, error) {
	connector := ""
	if f.AdClicks.Network != "" {
		n, _ := networkByID(f.AdClicks.Network)
		connector = n.Connector
		if connector == "" {
			connector = "-"
		}
	}
	rows, err := q.Query(ctx, `
		SELECT m.currency, sum(m.clicks)::bigint, sum(m.impressions)::bigint, (sum(m.spend_micros) / 10000)::bigint, count(DISTINCT e.id)::int
		FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id
		JOIN ad_metrics_daily m ON m.ad_entity_id = e.id AND m.day >= $3::date AND m.day < $4::date
		WHERE e.workspace_id = $1 AND e.level = 'campaign' AND COALESCE(e.brand_id, a.brand_id) = $2
			AND ($5 = '' OR a.connector = $5) AND ($6 = '' OR strpos(lower(e.name), lower($6)) > 0)
		GROUP BY m.currency ORDER BY 4 DESC`,
		ws, brand, f.From.UTC().Format(time.DateOnly), f.To.UTC().Format(time.DateOnly), connector, f.AdClicks.Campaign)
	if err != nil {
		return AdClickResult{}, err
	}
	defer rows.Close()
	out := AdClickResult{Spend: []Money{}}
	for rows.Next() {
		var m Money
		var clicks, imps int64
		var campaigns int
		if err := rows.Scan(&m.Currency, &clicks, &imps, &m.AmountMinor, &campaigns); err != nil {
			return AdClickResult{}, err
		}
		out.Clicks += clicks
		out.Impressions += imps
		out.Campaigns += campaigns
		out.Spend = append(out.Spend, m)
	}
	return out, rows.Err()
}

// Person is someone at a step: a customer record, or a browser not yet
// tied to one.
type Person struct {
	RecordID    *string `json:"record_id"`
	AnonymousID *string `json:"anonymous_id"`
	Name        string  `json:"name"`
	Email       string  `json:"email"`
	// Breakdown is their step 1's breakdown value.
	Breakdown string `json:"breakdown"`
	// Times are when they did each step they reached, from step 1.
	Times []time.Time `json:"times"`
}

// PeopleQuery picks who to list.
type PeopleQuery struct {
	// Step is counted from 1.
	Step int
	// Dropped lists who reached the step before Step but not Step;
	// otherwise those who reached Step.
	Dropped bool
	// Breakdown keeps one breakdown value ("" for none, when the funnel
	// has a breakdown and BreakdownSet).
	Breakdown    string
	BreakdownSet bool
	Limit        int
	Offset       int
}

// People lists the people at a step, the latest to reach it first, and
// says whether there are more.
func (c *Compiled) People(ctx context.Context, q Querier, pq PeopleQuery) ([]Person, bool, error) {
	n := len(c.f.Steps)
	if pq.Step < 1 || pq.Step > n || (pq.Dropped && pq.Step == 1) {
		return nil, false, Invalid(fmt.Sprintf("step is 1 to %d; dropped people are from step 2 on", n))
	}
	a := args{list: slices.Clone(c.args.list)}
	where := []string{fmt.Sprintf(`t%d IS NOT NULL`, pq.Step)}
	order := fmt.Sprintf(`t%d`, pq.Step)
	if pq.Dropped {
		where = []string{fmt.Sprintf(`t%d IS NOT NULL`, pq.Step-1), fmt.Sprintf(`t%d IS NULL`, pq.Step)}
		order = fmt.Sprintf(`t%d`, pq.Step-1)
	}
	if pq.BreakdownSet {
		where = append(where, `bk = `+a.add(pq.Breakdown))
	}
	var ts []string
	for i := 1; i <= n; i++ {
		ts = append(ts, fmt.Sprintf(`t%d`, i))
	}
	sql := c.with + `
		SELECT r.id::text, CASE WHEN f.actor LIKE 'a:%' THEN substr(f.actor, 3) END, COALESCE(r.name, ''),
			COALESCE(r.attributes ->> 'email', ''), f.bk, ARRAY[` + strings.Join(ts, ", ") + `]::timestamptz[]
		FROM f LEFT JOIN records r ON f.actor NOT LIKE 'a:%' AND r.id = (CASE WHEN f.actor LIKE 'a:%' THEN NULL ELSE f.actor END)::uuid
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY ` + order + ` DESC, f.actor LIMIT ` + a.add(pq.Limit+1) + ` OFFSET ` + a.add(pq.Offset)
	rows, err := q.Query(ctx, sql, a.list...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []Person{}
	for rows.Next() {
		var p Person
		var times []*time.Time
		if err := rows.Scan(&p.RecordID, &p.AnonymousID, &p.Name, &p.Email, &p.Breakdown, &times); err != nil {
			return nil, false, err
		}
		p.Times = []time.Time{}
		for _, t := range times {
			if t == nil {
				break
			}
			p.Times = append(p.Times, t.UTC())
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > pq.Limit
	if more {
		out = out[:pq.Limit]
	}
	return out, more, nil
}
