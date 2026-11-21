// Package smartlists keeps smart lists up to date: groups of people on
// customer records that match a list's rules right now, such as "plan is
// pro and paid in the last 90 days". A refresh works out who matches in one
// statement, adds the people who started matching and removes the ones
// who stopped, so smart_list_members is always the current membership.
// A static list (static.go) holds whoever is put in it instead.
// Lists refresh when they are saved, when someone asks, and on a sweep
// every few minutes for rules that depend on time ("in the last 90 days").
// The API handlers in engine/internal/api own making and changing lists.
package smartlists

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

const (
	// JobRefresh refreshes one list.
	JobRefresh = "smart_list.refresh"
	// Every is how often each list is refreshed without being asked.
	Every = 15 * time.Minute
	// MaxRules is the most rules one group (the list itself, or a group
	// inside it) can have.
	MaxRules = 20
	// MaxTests is the most tests a list can have, counting those in its
	// groups.
	MaxTests = 50
	// MaxDepth is how deep groups go: the list's own rules are level 1, a
	// group in them level 2, a group in that level 3.
	MaxDepth = 3
	// refreshTimeout bounds the one statement a refresh runs.
	refreshTimeout = "120s"
)

// Rule is one test a person must pass. Set exactly one of:
//
//   - attribute: a field on their customer record (plan, address.country),
//     compared with op and value as journeys compare them.
//   - event: whether they did this customer event (exists) or not
//     (not_exists), ever or in the last within_days days.
//   - in_list: whether they are a lead in this lead list (exists) or not
//     (not_exists).
//   - news_search: whether they wrote an article this news search found
//     (exists) or not (not_exists), ever or in the last within_days days:
//     a media list of the journalists who cover a topic.
//   - company_attribute: a field on a company they are on (the companies
//     field), compared like attribute: such as a journalist's outlet tier,
//     outlet_tier. neq and not_exists hold when none of their companies
//     matches.
//   - product_stage: their lifecycle stage for product (a product's key or
//     id) is (eq) or isn't (neq) this stage (docs/product-lifecycle.md).
//     level is company (billing) or person (usage); by default the list's
//     own kind of record: billing stages for a company list, usage stages
//     for a people list.
//   - page: whether they viewed a page (exists) or not (not_exists) on one
//     of the brand's sites, by its host and path. Only views from a browser
//     tied to them by something they did (signing in to the product,
//     clicking a tracked link in an email Outflare sent, or a visitor link)
//     count, so nobody can put someone in a list by sending page views.
//   - in_smart_list: whether they are in another list of the brand (smart,
//     static or from a query) or not; a list can't depend on itself.
//   - marketing_email: whether they may get marketing email (exists) or not
//     (not_exists), as the People list's Marketing email column says.
//
// Event and page rules also take min_count (did it at least that many
// times; with not_exists, fewer), within_days (in the last that many
// days) or before_days (more than that many days ago), and an event rule
// where: tests on the event's properties, such as amount_minor over 10000.
//
// Or a rule is a group of rules (rules, with match) that passes when all,
// any or none of them pass, so a list can say "paid and (on pro or on
// team) and none of (refunded, churned)". Groups hold groups, MaxDepth
// deep.
//
// A list of companies (record_type account) tests the company: attribute,
// event (the company's own events and its people's) and product_stage.
type Rule struct {
	// A group: its rules and whether all, any or none of them must pass.
	Rules []Rule `json:"rules,omitempty"`
	Match string `json:"match,omitempty"`

	Attribute        string `json:"attribute,omitempty"`
	CompanyAttribute string `json:"company_attribute,omitempty"`
	Event            string `json:"event,omitempty"`
	WithinDays       int    `json:"within_days,omitempty"`
	InList           string `json:"in_list,omitempty"`
	NewsSearch       string `json:"news_search,omitempty"`
	ProductStage     string `json:"product_stage,omitempty"`
	Product          string `json:"product,omitempty"`
	Level            string `json:"level,omitempty"`
	Op               string `json:"op,omitempty"`
	Value            any    `json:"value,omitempty"`

	Page           *Page          `json:"page,omitempty"`
	InSmartList    string         `json:"in_smart_list,omitempty"`
	MarketingEmail bool           `json:"marketing_email,omitempty"`
	MinCount       int            `json:"min_count,omitempty"`
	BeforeDays     int            `json:"before_days,omitempty"`
	Where          []PropertyTest `json:"where,omitempty"`
}

// Page is the page a page rule tests: on the site Host (empty: any of the
// brand's sites), with a path that is, starts with or contains Path
// (Match; contains by default). Both empty is any page.
type Page struct {
	Host  string `json:"host,omitempty"`
	Path  string `json:"path,omitempty"`
	Match string `json:"match,omitempty"`
}

// PropertyTest is one test on an event's properties, compared as attributes are.
type PropertyTest struct {
	Property string `json:"property"`
	Op       string `json:"op"`
	Value    any    `json:"value,omitempty"`
}

// MaxWhere is the most property tests an event rule can have.
const MaxWhere = 5

// Record types a list can hold.
const (
	People    = "person"
	Companies = "account"
)

var (
	fieldRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}(\.[a-z0-9_]{1,62})*$`)
	eventRE = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$`)
	uuidRE  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	ops     = map[string]bool{"eq": true, "neq": true, "gt": true, "lt": true, "contains": true, "exists": true, "not_exists": true,
		"starts_with": true, "in": true, "not_in": true, "in_last_days": true, "more_than_days_ago": true}
	hostRE     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)
	propertyRE = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$-]{0,99}(\.[A-Za-z0-9_$-]{1,100})*$`)
	numericRE  = `'^\s*-?[0-9]+(\.[0-9]+)?\s*$'`
)

// Check reports what is wrong with a people list's rules, or "".
func Check(rules []Rule, match string) string { return CheckFor(People, rules, match) }

// CheckFor reports what is wrong with the rules of a list of recordType,
// or "".
func CheckFor(recordType string, rules []Rule, match string) string {
	if recordType != People && recordType != Companies {
		return "record_type is person or account"
	}
	if match != "all" && match != "any" {
		return "match is all or any"
	}
	if n := Tests(rules); n > MaxTests {
		return fmt.Sprintf("a smart list has at most %d rules, counting those in groups", MaxTests)
	}
	return checkGroup(recordType, rules, "", 1)
}

// Tests counts the tests in rules, those in groups included.
func Tests(rules []Rule) int {
	n := 0
	for _, r := range rules {
		if r.group() {
			n += Tests(r.Rules)
		} else {
			n++
		}
	}
	return n
}

// group reports whether r is a group of rules rather than one test.
func (r Rule) group() bool { return r.Rules != nil || r.Match != "" }

// checkGroup checks the rules of a group at depth; at names rules in
// messages: "rule 2.1" is the first rule of the group that is rule 2.
func checkGroup(recordType string, rules []Rule, at string, depth int) string {
	if len(rules) > MaxRules {
		if at == "" {
			return fmt.Sprintf("a smart list has at most %d rules", MaxRules)
		}
		return fmt.Sprintf("rule %s: a group has at most %d rules", at, MaxRules)
	}
	for i, r := range rules {
		name := strconv.Itoa(i + 1)
		if at != "" {
			name = at + "." + name
		}
		var msg string
		switch {
		case r.group():
			msg = checkGroupRule(r, depth)
			if msg == "" {
				if sub := checkGroup(recordType, r.Rules, name, depth+1); sub != "" {
					return sub
				}
			}
		default:
			msg = checkRule(r)
			if msg == "" && recordType == Companies && (r.CompanyAttribute != "" || r.InList != "" || r.NewsSearch != "" || r.Page != nil || r.MarketingEmail) {
				msg = "a company list tests attribute, event, product_stage or in_smart_list"
			}
		}
		if msg != "" {
			return fmt.Sprintf("rule %s: %s", name, msg)
		}
	}
	return ""
}

// checkGroupRule checks a group itself, not its rules.
func checkGroupRule(r Rule, depth int) string {
	switch {
	case depth >= MaxDepth:
		return fmt.Sprintf("groups go at most %d levels deep", MaxDepth-1)
	case r.Match != "all" && r.Match != "any" && r.Match != "none":
		return "a group's match is all, any or none"
	case len(r.Rules) == 0:
		return "a group has at least one rule"
	case r.Attribute != "" || r.CompanyAttribute != "" || r.Event != "" || r.InList != "" || r.NewsSearch != "" || r.ProductStage != "" ||
		r.Product != "" || r.Level != "" || r.Op != "" || r.Value != nil || r.WithinDays != 0 || r.Page != nil || r.InSmartList != "" ||
		r.MarketingEmail || r.MinCount != 0 || r.BeforeDays != 0 || r.Where != nil:
		return "a group has only rules and match"
	}
	return ""
}

func checkRule(r Rule) string {
	set := 0
	for _, v := range []string{r.Attribute, r.CompanyAttribute, r.Event, r.InList, r.NewsSearch, r.ProductStage, r.InSmartList} {
		if v != "" {
			set++
		}
	}
	if r.Page != nil {
		set++
	}
	if r.MarketingEmail {
		set++
	}
	if set != 1 {
		return "each rule tests one of attribute, company_attribute, event, page, in_list, in_smart_list, news_search, product_stage or marketing_email, or is a group (rules and match)"
	}
	if msg := checkCounts(r); msg != "" {
		return msg
	}
	if r.Page != nil || r.InSmartList != "" || r.MarketingEmail {
		switch {
		case r.Op != "exists" && r.Op != "not_exists":
			return "page, in_smart_list and marketing_email rules use exists (did, is, may) or not_exists (didn't, isn't, may not)"
		case r.Value != nil:
			return "op " + r.Op + " takes no value"
		case r.InSmartList != "" && !uuidRE.MatchString(r.InSmartList):
			return "in_smart_list is a list's id"
		case (r.InSmartList != "" || r.MarketingEmail) && r.WithinDays != 0:
			return "within_days is only for event, page and news_search rules"
		}
		if p := r.Page; p != nil {
			switch {
			case p.Host != "" && (len(p.Host) > 253 || !hostRE.MatchString(p.Host)):
				return "page.host is a site's host name in lowercase, such as example.com or app.example.com"
			case len(p.Path) > 500 || strings.ContainsAny(p.Path, "\n\r"):
				return "page.path is at most 500 characters"
			case p.Match != "" && p.Match != "is" && p.Match != "starts_with" && p.Match != "contains":
				return "page.match is is, starts_with or contains"
			}
		}
		if r.WithinDays < 0 || r.WithinDays > 3650 {
			return "within_days is 1 to 3650, or 0 for ever"
		}
		return ""
	}
	if r.ProductStage != "" {
		switch {
		case r.Product == "" || len(r.Product) > 200:
			return "a product_stage rule names its product: a product's key or id"
		case len(r.ProductStage) > 40:
			return "product_stage is a stage's key, such as churned"
		case r.Op != "eq" && r.Op != "neq":
			return "a product_stage rule uses eq (is in the stage) or neq (isn't)"
		case r.Level != "" && r.Level != "company" && r.Level != "person":
			return "level is company or person"
		case r.Value != nil || r.WithinDays != 0:
			return "a product_stage rule takes no value or within_days"
		}
		return ""
	}
	if r.Product != "" || r.Level != "" {
		return "product and level are only for product_stage rules"
	}
	if !ops[r.Op] {
		return "op is eq, neq, gt, lt, contains, starts_with, in, not_in, in_last_days, more_than_days_ago, exists or not_exists"
	}
	switch {
	case r.Attribute != "" && (!fieldRE.MatchString(r.Attribute) || len(r.Attribute) > 200):
		return "attribute is a field name such as plan or address.country"
	case r.CompanyAttribute != "" && (!fieldRE.MatchString(r.CompanyAttribute) || len(r.CompanyAttribute) > 200):
		return "company_attribute is a company field's name such as outlet_tier or industry"
	case r.Event != "" && (!eventRE.MatchString(r.Event) || len(r.Event) > 100):
		return "event is an event name such as order_completed"
	case r.InList != "" && !uuidRE.MatchString(r.InList):
		return "in_list is a lead list's id"
	case r.NewsSearch != "" && !uuidRE.MatchString(r.NewsSearch):
		return "news_search is a news search's id"
	case (r.Event != "" || r.InList != "" || r.NewsSearch != "") && r.Op != "exists" && r.Op != "not_exists":
		return "event, in_list and news_search rules use exists (did, is in) or not_exists (didn't, isn't)"
	case r.WithinDays != 0 && r.Event == "" && r.NewsSearch == "":
		return "within_days is only for event, page and news_search rules"
	case r.WithinDays < 0 || r.WithinDays > 3650:
		return "within_days is 1 to 3650, or 0 for ever"
	case (r.Event != "" || r.InList != "" || r.NewsSearch != "") && r.Value != nil:
		return "op " + r.Op + " takes no value"
	}
	if r.Attribute != "" || r.CompanyAttribute != "" {
		return checkValue(r.Op, r.Value)
	}
	for i, w := range r.Where {
		switch {
		case !propertyRE.MatchString(w.Property):
			return fmt.Sprintf("where %d: property is a property's name, such as plan or amount_minor", i+1)
		case !ops[w.Op]:
			return fmt.Sprintf("where %d: op is eq, neq, gt, lt, contains, starts_with, in, not_in, in_last_days, more_than_days_ago, exists or not_exists", i+1)
		}
		if msg := checkValue(w.Op, w.Value); msg != "" {
			return fmt.Sprintf("where %d: %s", i+1, msg)
		}
	}
	return ""
}

// checkCounts checks the parts of an event or page rule that say how often
// and when, and that no other rule sets them.
func checkCounts(r Rule) string {
	counted := r.Event != "" || r.Page != nil
	switch {
	case !counted && (r.MinCount != 0 || r.BeforeDays != 0):
		return "min_count and before_days are only for event and page rules"
	case r.Where != nil && r.Event == "":
		return "where is only for event rules"
	case len(r.Where) > MaxWhere:
		return fmt.Sprintf("an event rule has at most %d where tests", MaxWhere)
	case r.MinCount < 0 || r.MinCount > 100000:
		return "min_count is 1 to 100000, or 0 for once"
	case r.BeforeDays < 0 || r.BeforeDays > 3650:
		return "before_days is 1 to 3650, or 0"
	case r.BeforeDays != 0 && r.WithinDays != 0:
		return "set within_days (in the last days) or before_days (more than that many days ago), not both"
	}
	return ""
}

// checkValue checks what op compares with: nothing for exists and
// not_exists, a list for in and not_in, a whole number of days for
// in_last_days and more_than_days_ago, otherwise text, a number or true
// or false.
func checkValue(op string, value any) string {
	switch op {
	case "exists", "not_exists":
		if value != nil {
			return "op " + op + " takes no value"
		}
		return ""
	case "in", "not_in":
		list, ok := value.([]any)
		if !ok || len(list) == 0 || len(list) > 100 {
			return "op " + op + " takes a list of 1 to 100 values"
		}
		for _, v := range list {
			if msg := checkScalar(v); msg != "" {
				return msg
			}
		}
		return ""
	case "in_last_days", "more_than_days_ago":
		n, ok := number(value)
		d, err := strconv.Atoi(n)
		if !ok || err != nil || d < 1 || d > 36500 {
			return "op " + op + " takes a whole number of days, 1 to 36500"
		}
		return ""
	}
	if value == nil {
		return "op " + op + " needs a value"
	}
	return checkScalar(value)
}

func checkScalar(v any) string {
	switch x := v.(type) {
	case bool, float64, json.Number, int, int64:
	case string:
		if len(x) > 500 {
			return "value is at most 500 characters"
		}
	default:
		return "value is text, a number or true or false"
	}
	return ""
}

// InLists is every lead list the rules name, for the caller to check they
// belong to the workspace.
func InLists(rules []Rule) []string {
	out := []string{}
	for _, r := range rules {
		if r.InList != "" {
			out = append(out, r.InList)
		}
		out = append(out, InLists(r.Rules)...)
	}
	return out
}

// SmartLists is every list the rules name with in_smart_list, for the
// caller to check they are the brand's and that no list depends on itself.
func SmartLists(rules []Rule) []string {
	out := []string{}
	for _, r := range rules {
		if r.InSmartList != "" {
			out = append(out, r.InSmartList)
		}
		out = append(out, SmartLists(r.Rules)...)
	}
	return out
}

// Negative reports whether the rules pick nearly everyone, because what
// decides who is in only leaves people out (didn't, isn't, is not): every
// rule when all must pass, or any one of them when any may.
func Negative(rules []Rule, match string) bool {
	return len(rules) > 0 && broad(rules, match, false)
}

// broad reports whether a group of rules passes for nearly everyone;
// flipped is whether something above it says "none of".
func broad(rules []Rule, match string, flipped bool) bool {
	inner := flipped != (match == "none")
	// All must pass, unless a "none of" above turns it round.
	every := (match == "all") != inner
	for _, r := range rules {
		var b bool
		if r.group() {
			b = broad(r.Rules, r.Match, inner)
		} else {
			b = (r.Op == "not_exists" || r.Op == "neq" || r.Op == "not_in") != inner
		}
		if b != every {
			return b
		}
	}
	return every
}

// NewsSearches is every news search the rules name, for the caller to
// check they belong to the workspace.
func NewsSearches(rules []Rule) []string {
	out := []string{}
	for _, r := range rules {
		if r.NewsSearch != "" {
			out = append(out, r.NewsSearch)
		}
		out = append(out, NewsSearches(r.Rules)...)
	}
	return out
}

// text is a value as journeys compare it.
func text(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case json.Number:
		return x.String()
	}
	return fmt.Sprint(v)
}

func number(v any) (string, bool) {
	switch x := v.(type) {
	case bool, nil:
		return "", false
	case string:
		if _, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err != nil {
			return "", false
		}
		return strings.TrimSpace(x), true
	}
	s := text(v)
	_, err := strconv.ParseFloat(s, 64)
	return s, err == nil
}

// builder collects a statement's arguments.
type builder struct {
	args       []any
	recordType string
}

func (b *builder) arg(v any) string {
	b.args = append(b.args, v)
	return "$" + strconv.Itoa(len(b.args))
}

// Where returns the SQL that picks a list's people from records aliased r:
// people in the workspace (args[0] must be its id), not merged or archived,
// in the list's brand or shared by every brand, who pass the rules. No
// rules picks everyone. args is extended with the values it uses.
func Where(rules []Rule, match string, brandID *string, args []any) (string, []any) {
	return WhereFor(People, rules, match, brandID, args)
}

// WhereFor is Where for a list of recordType (People or Companies).
func WhereFor(recordType string, rules []Rule, match string, brandID *string, args []any) (string, []any) {
	b := &builder{args: args, recordType: recordType}
	where := `r.workspace_id = $1 AND r.record_type = ` + b.arg(recordType) + ` AND r.merged_into IS NULL AND r.archived_at IS NULL`
	return b.where(where, rules, match, brandID)
}

// Seen narrows records r to people of the brands in (and the shared ones),
// for a caller limited to some brands; nil lets everyone through.
func Seen(in []string, args []any) (string, []any) {
	if in == nil {
		return "", args
	}
	args = append(args, in)
	return ` AND r.brand_id = ANY($` + strconv.Itoa(len(args)) + `::uuid[])`, args
}

func (b *builder) where(where string, rules []Rule, match string, brandID *string) (string, []any) {
	eventBrand := ""
	if brandID != nil {
		p := b.arg(*brandID)
		where += ` AND r.brand_id = ` + p
		eventBrand = ` AND e.brand_id = ` + p
	}
	if len(rules) == 0 {
		return where, b.args
	}
	return where + ` AND ` + b.group(rules, match, eventBrand), b.args
}

// group is the SQL that holds when all, any or none of rules pass. Each
// rule is never NULL, so NOT is safe.
func (b *builder) group(rules []Rule, match, eventBrand string) string {
	parts := make([]string, 0, len(rules))
	for _, r := range rules {
		parts = append(parts, `COALESCE(`+b.rule(r, eventBrand)+`, false)`)
	}
	switch match {
	case "any":
		return `(` + strings.Join(parts, " OR ") + `)`
	case "none":
		return `NOT (` + strings.Join(parts, " OR ") + `)`
	}
	return `(` + strings.Join(parts, " AND ") + `)`
}

func (b *builder) rule(r Rule, eventBrand string) string {
	switch {
	case r.group():
		return b.group(r.Rules, r.Match, eventBrand)
	case r.ProductStage != "":
		level := r.Level
		if level == "" {
			level = "person"
			if b.recordType == Companies {
				level = "company"
			}
		}
		sql := `EXISTS (SELECT 1 FROM lifecycle_states ls JOIN products p ON p.id = ls.product_id AND p.archived_at IS NULL
			WHERE ls.workspace_id = $1 AND ls.record_id = r.id AND ls.level = ` + b.arg(level) + ` AND ls.stage = ` + b.arg(r.ProductStage) +
			` AND (p.key = ` + b.arg(strings.ToLower(r.Product)) + ` OR p.id::text = ` + b.arg(strings.ToLower(r.Product)) + `))`
		if r.Op == "neq" {
			return `NOT ` + sql
		}
		return sql
	case r.Event != "":
		// Browser events can be sent by anyone with the site key, so
		// they never put someone in a list, as in journeys. A company
		// counts its own events and its people's.
		who := `e.record_id = r.id`
		if b.recordType == Companies {
			who = `(e.record_id = r.id OR e.account_id = r.id OR e.record_id IN (SELECT rr.from_id FROM record_relationships rr
				WHERE rr.workspace_id = $1 AND rr.to_id = r.id AND rr.kind = 'companies' AND rr.valid_to IS NULL))`
		}
		cond := `e.workspace_id = $1 AND ` + who + ` AND e.name = ` + b.arg(r.Event) + ` AND e.source <> 'browser'` + eventBrand + b.when(r)
		for _, w := range r.Where {
			path := b.arg(strings.Split(w.Property, "."))
			cond += ` AND COALESCE(` + b.compare(`(e.properties #> `+path+`::text[])`, `(e.properties #>> `+path+`::text[])`, w.Op, w.Value) + `, false)`
		}
		return b.counted(cond, r)
	case r.Page != nil:
		// Page views come from the site script, which anyone can call, so
		// only views from a browser the person was tied to by something
		// they did count: signing in to the product, clicking a tracked
		// link in an email Outflare sent, or a visitor link.
		cond := `e.workspace_id = $1 AND e.record_id = r.id AND e.name = 'page.viewed'` + eventBrand + b.when(r) + `
			AND EXISTS (SELECT 1 FROM site_visitors sv WHERE sv.workspace_id = $1 AND sv.anonymous_id = e.anonymous_id
				AND sv.record_id = r.id AND sv.identified_by IN ('product', 'email_click', 'link'))`
		if h := strings.ToLower(strings.TrimPrefix(r.Page.Host, "www.")); h != "" {
			p := b.arg(h)
			cond += ` AND ` + PageHostSQL + ` IN (` + p + `, 'www.' || ` + p + `)`
		}
		if r.Page.Path != "" {
			path := `lower(COALESCE(e.context->'page'->>'path', ''))`
			p := b.arg(strings.ToLower(r.Page.Path))
			switch r.Page.Match {
			case "is":
				cond += ` AND rtrim(` + path + `, '/') = rtrim(` + p + `, '/')`
			case "starts_with":
				cond += ` AND starts_with(` + path + `, ` + p + `)`
			default:
				cond += ` AND strpos(` + path + `, ` + p + `) > 0`
			}
		}
		return b.counted(cond, r)
	case r.InSmartList != "":
		// The other list's members as of its last refresh.
		l := b.arg(r.InSmartList)
		sql := `(EXISTS (SELECT 1 FROM smart_list_members m WHERE m.workspace_id = $1 AND m.list_id = ` + l + `::uuid AND m.record_id = r.id)
			OR EXISTS (SELECT 1 FROM static_list_members m WHERE m.workspace_id = $1 AND m.list_id = ` + l + `::uuid AND m.record_id = r.id))`
		if r.Op == "not_exists" {
			return `NOT ` + sql
		}
		return sql
	case r.MarketingEmail:
		if r.Op == "not_exists" {
			return `NOT ` + MayMarketSQL
		}
		return MayMarketSQL
	case r.NewsSearch != "":
		sql := `EXISTS (SELECT 1 FROM news_article_authors aa JOIN news_hits h ON h.article_id = aa.article_id
			JOIN news_articles x ON x.id = aa.article_id
			WHERE aa.workspace_id = $1 AND aa.record_id = r.id AND h.search_id = ` + b.arg(r.NewsSearch) + `::uuid`
		if r.WithinDays > 0 {
			sql += ` AND coalesce(x.published_at, x.first_seen_at) >= now() - make_interval(days => ` + b.arg(r.WithinDays) + `::int)`
		}
		sql += `)`
		if r.Op == "not_exists" {
			return `NOT ` + sql
		}
		return sql
	case r.InList != "":
		sql := `EXISTS (SELECT 1 FROM lead_list_members m JOIN leads l ON l.id = m.lead_id
			JOIN lead_lists ll ON ll.id = m.list_id AND ll.workspace_id = $1
			WHERE m.list_id = ` + b.arg(r.InList) + ` AND l.record_id = r.id)`
		if r.Op == "not_exists" {
			return `NOT ` + sql
		}
		return sql
	}
	if r.CompanyAttribute != "" {
		// Any of their companies passing; neq and not_exists ask that
		// none of them has the value.
		op, not := r.Op, false
		switch op {
		case "neq":
			op, not = "eq", true
		case "not_exists":
			op, not = "exists", true
		}
		sql := `EXISTS (SELECT 1 FROM record_relationships rr JOIN records c ON c.id = rr.to_id AND c.record_type = 'account' AND c.merged_into IS NULL
			WHERE rr.workspace_id = $1 AND rr.from_id = r.id AND rr.kind = 'companies' AND rr.valid_to IS NULL
			AND COALESCE(` + b.test("c", r.CompanyAttribute, op, r.Value) + `, false))`
		if not {
			return `NOT ` + sql
		}
		return sql
	}
	return b.test("r", r.Attribute, r.Op, r.Value)
}

// PageHostSQL is the host of a page.viewed event e: the one the script
// sent, or the one in its URL, in lowercase.
const PageHostSQL = `COALESCE(NULLIF(lower(e.context->'page'->>'host'), ''), lower(substring(e.context->'page'->>'url' FROM '^https?://([^/:?#]+)')), '')`

// when is the condition on an event e's time: in the last within_days
// days, or more than before_days days ago.
func (b *builder) when(r Rule) string {
	switch {
	case r.WithinDays > 0:
		return ` AND e.occurred_at >= now() - make_interval(days => ` + b.arg(r.WithinDays) + `::int)`
	case r.BeforeDays > 0:
		return ` AND e.occurred_at < now() - make_interval(days => ` + b.arg(r.BeforeDays) + `::int)`
	}
	return ""
}

// counted is the SQL that holds when events e matching cond happened (op
// exists) at least min_count times, or (not_exists) fewer. It stops
// counting at min_count, so a busy person costs no more than a quiet one.
func (b *builder) counted(cond string, r Rule) string {
	sql := `EXISTS (SELECT 1 FROM customer_events e WHERE ` + cond + `)`
	if r.MinCount > 1 {
		n := b.arg(r.MinCount)
		sql = `((SELECT count(*) FROM (SELECT 1 FROM customer_events e WHERE ` + cond + ` LIMIT ` + n + `::int) x) >= ` + n + `::int)`
	}
	if r.Op == "not_exists" {
		return `NOT ` + sql
	}
	return sql
}

// test compares a field on records aliased alias with op and value.
func (b *builder) test(alias, field, op string, value any) string {
	path := b.arg(strings.Split(field, "."))
	return b.compare(`(`+alias+`.attributes #> `+path+`::text[])`, `(`+alias+`.attributes #>> `+path+`::text[])`, op, value)
}

// compare tests a JSON value, j as jsonb and t as text, with op and value.
func (b *builder) compare(j, t, op string, value any) string {
	switch op {
	case "exists":
		return `COALESCE(` + t + `, '') <> ''`
	case "not_exists":
		return `COALESCE(` + t + `, '') = ''`
	case "in_last_days", "more_than_days_ago":
		n, _ := number(value)
		at := `outflare_try_timestamptz(` + t + `)`
		since := `now() - make_interval(days => ` + b.arg(n) + `::int)`
		if op == "in_last_days" {
			return `(` + at + ` >= ` + since + ` AND ` + at + ` <= now() + interval '1 day')`
		}
		return `(` + at + ` < ` + since + `)`
	case "in", "not_in":
		list, _ := value.([]any)
		parts := make([]string, 0, len(list))
		for _, v := range list {
			parts = append(parts, `COALESCE(`+b.eq(t, v)+`, false)`)
		}
		in := `(` + strings.Join(parts, " OR ") + `)`
		if op == "not_in" {
			return `(` + t + ` IS NULL OR NOT ` + in + `)`
		}
		return in
	}
	switch op {
	case "eq":
		return b.eq(t, value)
	case "neq":
		return `(` + t + ` IS NULL OR NOT COALESCE(` + b.eq(t, value) + `, false))`
	}
	s := b.arg(text(value))
	switch op {
	case "gt", "lt":
		cmp := ">"
		if op == "lt" {
			cmp = "<"
		}
		// Numbers compare as numbers, everything else as text, so ISO
		// dates sort.
		asText := `(` + t + ` COLLATE "C") ` + cmp + ` (` + s + ` COLLATE "C")`
		if n, ok := number(value); ok {
			return `(CASE WHEN ` + t + ` ~ ` + numericRE + ` THEN ` + t + `::numeric ` + cmp + ` ` + b.arg(n) + `::text::numeric ELSE ` + asText + ` END)`
		}
		return asText
	case "contains":
		return `(CASE WHEN jsonb_typeof(` + j + `) = 'array'
			THEN EXISTS (SELECT 1 FROM jsonb_array_elements_text(` + j + `) x WHERE lower(x) = lower(` + s + `))
			ELSE strpos(lower(` + t + `), lower(` + s + `)) > 0 END)`
	case "starts_with":
		return `starts_with(lower(` + t + `), lower(` + s + `))`
	}
	return `false`
}

// eq is the SQL that t equals value: as numbers when both are, otherwise
// as text, ignoring case.
func (b *builder) eq(t string, value any) string {
	s := b.arg(text(value))
	if n, ok := number(value); ok {
		return `(CASE WHEN ` + t + ` ~ ` + numericRE + ` THEN ` + t + `::numeric = ` + b.arg(n) + `::text::numeric ELSE lower(` + t + `) = lower(` + s + `) END)`
	}
	return `lower(` + t + `) = lower(` + s + `)`
}

// MayMarketSQL is true for a person (records r) who may get marketing
// email, as the People list's Marketing email column works it out: not
// held for review, nothing on the opt-out list that stops marketing email,
// and their contact basis lets it under the workspace's rule
// (customers.MayMarket).
const MayMarketSQL = `(r.record_type = 'person' AND NOT r.needs_review
	AND NOT EXISTS (SELECT 1 FROM record_identities i JOIN suppressions s ON s.workspace_id = r.workspace_id AND (
			(s.kind = 'email' AND s.value = i.value) OR (s.kind = 'domain' AND s.value = split_part(i.value, '@', 2)))
		WHERE i.record_id = r.id AND i.namespace = 'email' AND i.valid_to IS NULL
			AND COALESCE(NULLIF(split_part(s.reason, ':', 2), ''), s.reason) NOT IN ('customer', 'partner', 'blocklist'))
	AND CASE (SELECT c.status FROM consents c WHERE c.workspace_id = r.workspace_id AND c.record_id = r.id
			AND c.channel = 'email' AND c.topic = '' AND c.purpose = 'marketing' ORDER BY c.occurred_at DESC, c.created_at DESC LIMIT 1)
		WHEN 'granted' THEN true WHEN 'withdrawn' THEN false
		ELSE outflare_contact_origin(r.origin, r.attributes) <> 'prospect'
			AND (SELECT w.customer_marketing FROM workspaces w WHERE w.id = r.workspace_id) = 'inferred' END)`

// Querier is a pool or a transaction.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Person is one member, as a preview or a member list shows them.
type Person struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	EnteredAt time.Time `json:"entered_at,omitzero"`
	// Members only: when the record was made, their first signed_up event,
	// and what they have paid less refunds.
	CreatedAt       *time.Time `json:"created_at,omitempty"`
	SignedUpAt      *time.Time `json:"signed_up_at,omitempty"`
	LifetimeRevenue *Money     `json:"lifetime_revenue,omitempty"`
}

// Money is an amount in one currency, in minor units (cents).
type Money struct {
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
}

// memberCols are what Members adds for each person: when the record was
// made, their first signed_up event, and their lifetime revenue. That is
// the record's own computed field (from a billing connection) when it has
// one, and otherwise their payment events less refunds, in the currency
// they paid most in.
const memberCols = `, r.created_at,
	(SELECT min(e.occurred_at) FROM customer_events e WHERE e.record_id = r.id AND e.workspace_id = r.workspace_id AND e.name = 'signed_up'),
	COALESCE(r.attributes->'lifetime_revenue'->>'currency', pay.currency, ''),
	COALESCE((r.attributes->'lifetime_revenue'->>'amount_minor')::bigint, pay.net, 0)`

const memberPay = ` LEFT JOIN LATERAL (
		SELECT upper(e.properties->>'currency') AS currency, GREATEST(sum(CASE WHEN e.name = 'payment.succeeded' THEN (e.properties->>'amount_minor')::numeric
			ELSE -(e.properties->>'amount_minor')::numeric END), 0)::bigint AS net
		FROM customer_events e
		WHERE e.record_id = r.id AND e.workspace_id = r.workspace_id AND e.name IN ('payment.succeeded', 'payment.refunded')
			AND e.source <> 'browser' AND e.properties->>'amount_minor' ~ '^-?[0-9]+(\.[0-9]+)?$' AND e.properties->>'currency' ~ '^[A-Za-z]{3}$'
		GROUP BY 1 ORDER BY 2 DESC LIMIT 1) pay ON r.attributes->'lifetime_revenue'->>'currency' IS NULL`

// personCols is a person's name and first verified email, from records r.
// For a company it is the main domain.
const personCols = `r.id, r.name, COALESCE((SELECT i.value FROM record_identities i WHERE i.record_id = r.id
	AND i.namespace = CASE WHEN r.record_type = 'account' THEN 'domain' ELSE 'email' END
	AND i.valid_to IS NULL ORDER BY i.verified DESC, i.valid_from, i.id LIMIT 1), '')`

// Preview counts who the rules pick now and returns up to limit of them,
// newest records first, without saving anything.
//
// seen limits it to some brands' people (Seen); nil counts everyone.
func Preview(ctx context.Context, q Querier, ws string, rules []Rule, match string, brandID *string, seen []string, limit int) (int, []Person, error) {
	return PreviewFor(ctx, q, ws, People, rules, match, brandID, seen, limit)
}

// PreviewFor is Preview for a list of recordType.
func PreviewFor(ctx context.Context, q Querier, ws, recordType string, rules []Rule, match string, brandID *string, seen []string, limit int) (int, []Person, error) {
	where, args := WhereFor(recordType, rules, match, brandID, []any{ws})
	more, args := Seen(seen, args)
	where += more
	var n int
	if err := q.QueryRow(ctx, `SELECT count(*)::int FROM records r WHERE `+where, args...).Scan(&n); err != nil {
		return 0, nil, err
	}
	rows, err := q.Query(ctx, `SELECT `+personCols+` FROM records r WHERE `+where+` ORDER BY r.created_at DESC, r.id DESC LIMIT `+strconv.Itoa(limit), args...)
	if err != nil {
		return 0, nil, err
	}
	sample, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Person, error) {
		var p Person
		return p, row.Scan(&p.ID, &p.Name, &p.Email)
	})
	if sample == nil {
		sample = []Person{}
	}
	return n, sample, err
}

// Members lists a list's members, newest to join first, after the cursor
// (the entered_at and record id of the last one seen). seen limits it to
// some brands' people (Seen); nil lists everyone.
func Members(ctx context.Context, q Querier, ws, listID string, seen []string, after *time.Time, afterID string, limit int) ([]Person, error) {
	args := []any{ws, listID, limit}
	cond := ""
	if after != nil {
		args = append(args, *after, afterID)
		cond = ` AND (m.entered_at, m.record_id) < ($4, $5::uuid)`
	}
	more, args := Seen(seen, args)
	cond += more
	table, err := membersTable(ctx, q, ws, listID)
	if err != nil {
		return nil, err
	}
	// A deleted person (Recently deleted) keeps their place, hidden, so
	// restoring them isn't joining again.
	rows, err := q.Query(ctx, `SELECT `+personCols+`, m.entered_at`+memberCols+` FROM `+table+` m JOIN records r ON r.id = m.record_id`+memberPay+`
		WHERE m.workspace_id = $1 AND m.list_id = $2 AND r.deleted_at IS NULL`+cond+` ORDER BY m.entered_at DESC, m.record_id DESC LIMIT $3`, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Person, error) {
		var p Person
		var currency string
		var net int64
		if err := row.Scan(&p.ID, &p.Name, &p.Email, &p.EnteredAt, &p.CreatedAt, &p.SignedUpAt, &currency, &net); err != nil {
			return p, err
		}
		if currency != "" {
			p.LifetimeRevenue = &Money{Currency: strings.ToUpper(currency), AmountMinor: net}
		}
		return p, nil
	})
	if out == nil {
		out = []Person{}
	}
	return out, err
}

// Result is what one refresh changed.
type Result struct {
	Joined  int `json:"joined"`
	Left    int `json:"left"`
	Members int `json:"members"`
}

// ErrGone means the list was deleted before it refreshed.
var ErrGone = errors.New("smart list not found")

// Refresh brings one list's members up to date in tx and reports who
// joined and left. A deleted person (Recently deleted) neither leaves nor
// counts: they keep their place, hidden, so restoring them doesn't fire
// the list's enter rules again.
func Refresh(ctx context.Context, tx pgx.Tx, ws, listID string) (Result, error) {
	return RefreshWith(ctx, tx, ws, listID, nil)
}

// ErrNeedsQuery means a query list was refreshed without its query's
// records: the Service fetches them first (QueryIDs).
var ErrNeedsQuery = errors.New("smart lists: a query list needs its query's records")

// MaxQueryMembers is the most records a query list holds.
const MaxQueryMembers = 50000

// RefreshWith is Refresh for a list of any kind; ids are a query list's
// records, from its saved query (nil for other kinds). Records that are
// not the list's kind, brand, or live are left out.
func RefreshWith(ctx context.Context, tx pgx.Tx, ws, listID string, ids []string) (Result, error) {
	var res Result
	var raw []byte
	var match, kind, recordType string
	var brandID *string
	err := tx.QueryRow(ctx, `SELECT rules, match, brand_id, kind, record_type FROM smart_lists WHERE workspace_id = $1 AND id = $2 FOR UPDATE`, ws, listID).
		Scan(&raw, &match, &brandID, &kind, &recordType)
	if errors.Is(err, pgx.ErrNoRows) {
		return res, ErrGone
	}
	if err != nil {
		return res, err
	}
	// A static list has no rules to refresh from: only its count.
	if kind == KindStatic {
		err := tx.QueryRow(ctx, `UPDATE smart_lists SET members = (SELECT count(*) FROM static_list_members WHERE list_id = $2), error_code = NULL
			WHERE workspace_id = $1 AND id = $2 RETURNING members`, ws, listID).Scan(&res.Members)
		return res, err
	}
	if kind == KindQuery && ids == nil {
		return res, ErrNeedsQuery
	}
	var rules []Rule
	if err := json.Unmarshal(raw, &rules); err != nil {
		return res, fmt.Errorf("%w: rules: %v", jobs.ErrPermanent, err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '`+refreshTimeout+`'`); err != nil {
		return res, err
	}
	// Rules that act on people joining or leaving need to know who did;
	// otherwise counts are enough.
	watched, err := customers.ListWatched(ctx, tx, ws, listID)
	if err != nil {
		return res, err
	}
	who := `'{}'::text[], '{}'::text[]`
	if watched {
		who = `(SELECT COALESCE(array_agg(record_id::text), '{}') FROM added), (SELECT COALESCE(array_agg(record_id::text), '{}') FROM gone)`
	}
	where, args := WhereFor(recordType, rules, match, brandID, []any{ws})
	if kind == KindQuery {
		// The query's records, kept to the list's kind and brand, live.
		args = []any{ws, ids, recordType, brandID}
		where = `r.workspace_id = $1 AND r.id = ANY ($2::uuid[]) AND r.record_type = $3 AND r.merged_into IS NULL AND r.archived_at IS NULL
			AND r.deleted_at IS NULL AND ($4::uuid IS NULL OR r.brand_id = $4)`
	}
	args = append(args, listID)
	l := "$" + strconv.Itoa(len(args))
	var joined, left []string
	err = tx.QueryRow(ctx, `WITH want AS (SELECT r.id FROM records r WHERE `+where+`),
		gone AS (DELETE FROM smart_list_members m WHERE m.list_id = `+l+`
			AND NOT EXISTS (SELECT 1 FROM want w WHERE w.id = m.record_id)
			AND NOT EXISTS (SELECT 1 FROM records d WHERE d.id = m.record_id AND d.deleted_at IS NOT NULL) RETURNING m.record_id),
		added AS (INSERT INTO smart_list_members (workspace_id, list_id, record_id) SELECT $1, `+l+`, id FROM want
			ON CONFLICT (list_id, record_id) DO NOTHING RETURNING record_id),
		history AS (INSERT INTO smart_list_changes (workspace_id, brand_id, list_id, record_id, change)
			SELECT $1, COALESCE((SELECT sl.brand_id FROM smart_lists sl WHERE sl.id = `+l+`), outflare_first_brand($1)), `+l+`, x.record_id, x.change
			FROM (SELECT record_id, 'enter' AS change FROM added UNION ALL SELECT record_id, 'leave' FROM gone) x)
		SELECT (SELECT count(*) FROM added)::int, (SELECT count(*) FROM gone)::int, (SELECT count(*) FROM want)::int, `+who, args...).
		Scan(&res.Joined, &res.Left, &res.Members, &joined, &left)
	if err != nil {
		return res, err
	}
	at := time.Now()
	for on, ids := range map[string][]string{"enter": joined, "leave": left} {
		if len(ids) == 0 {
			continue
		}
		if err := customers.ListChanged(ctx, tx, ws, customers.ListChange{ListID: listID, On: on, RecordIDs: ids, At: at}); err != nil {
			return res, err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE smart_lists SET members = $3, refreshed_at = now(), error_code = NULL WHERE workspace_id = $1 AND id = $2`,
		ws, listID, res.Members)
	return res, err
}

// QueryFailed is a query list's query that could not give its records:
// the list keeps its members and shows Code. It is final, not retried.
type QueryFailed struct{ Code, Message string }

func (e QueryFailed) Error() string { return "smart lists: query: " + e.Code + ": " + e.Message }

// QueryIDs fetches a query list's records: its saved query's record_id
// (or id) column. The API sets it, since only the API runs saved queries.
type QueryIDs func(ctx context.Context, ws, listID string) ([]string, error)

type payload struct {
	List   string `json:"list_id"`
	Manual bool   `json:"manual,omitempty"`
}

// Enqueue asks for a refresh of one list in q's transaction. manual marks
// one someone asked for (saving the list, or Refresh now), which is
// recorded as an event even when nobody joined or left.
func Enqueue(ctx context.Context, q jobs.Querier, ws, listID string, manual bool) (int64, error) {
	key := fmt.Sprintf("smart_list:%s:%d", listID, time.Now().UnixNano())
	if !manual {
		key = fmt.Sprintf("smart_list:%s:%d", listID, time.Now().Truncate(Every).Unix())
	}
	return jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobRefresh, WorkspaceID: &ws, Payload: payload{List: listID, Manual: manual},
		IdempotencyKey: key, MaxAttempts: 3})
}

// Service runs refreshes as jobs and schedules the sweep.
type Service struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	// Query fetches query lists' records; without it they don't refresh.
	Query QueryIDs
}

// Register adds the job handler.
func (s *Service) Register(q *jobs.Queue) { q.Handle(JobRefresh, s.job) }

func (s *Service) job(ctx context.Context, job jobs.Job) error {
	var p payload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || p.List == "" {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	start := time.Now()
	var res Result
	// A query list runs its query first, outside the transaction: the query
	// has its own read-only connection. A failed query leaves the members
	// as they were, with the list's error_code saying why.
	var ids []string
	var kind string
	if err := s.Pool.QueryRow(ctx, `SELECT kind FROM smart_lists WHERE workspace_id = $1 AND id = $2`, ws, p.List).Scan(&kind); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	var queryErr error
	if kind == KindQuery {
		if s.Query == nil {
			queryErr = ErrNeedsQuery
		} else if ids, queryErr = s.Query(ctx, ws, p.List); queryErr == nil && ids == nil {
			ids = []string{}
		}
	}
	err := queryErr
	if err == nil {
		err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			var err error
			res, err = RefreshWith(ctx, tx, ws, p.List, ids)
			return err
		})
	}
	if errors.Is(err, ErrGone) {
		return nil
	}
	ev := track.Event{Name: "smart_list.refreshed", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"list_id": p.List, "joined": res.Joined, "left": res.Left, "members": res.Members,
			"manual": p.Manual, "duration_ms": time.Since(start).Milliseconds()}}
	if err != nil {
		code := "internal"
		var pg *pgconn.PgError
		var qf QueryFailed
		switch {
		case errors.As(err, &pg) && pg.Code == "57014":
			code = "query_timeout"
		case errors.As(err, &qf):
			code = qf.Code
		}
		track.Fail(ctx, s.Log, ev, code, err)
		if _, uerr := s.Pool.Exec(ctx, `UPDATE smart_lists SET error_code = $3 WHERE workspace_id = $1 AND id = $2`, ws, p.List, code); uerr != nil {
			s.Log.Error("smart lists: record refresh error", "err", uerr)
		}
		if errors.As(err, &qf) {
			// Running it again now gives the same answer; the next sweep
			// tries again.
			return fmt.Errorf("%w: %w", jobs.ErrPermanent, err)
		}
		return err
	}
	if p.Manual || res.Joined > 0 || res.Left > 0 {
		track.Emit(ctx, ev)
	}
	return nil
}

// Run schedules a refresh of every list not refreshed for Every, until ctx
// ends.
func (s *Service) Run(ctx context.Context, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		if err := s.Sweep(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.Log, track.Event{Name: "smart_list.sweep_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Sweep enqueues a refresh for every list that is due.
func (s *Service) Sweep(ctx context.Context) error {
	rows, err := s.Pool.Query(ctx, `SELECT workspace_id::text, id::text FROM smart_lists
		WHERE kind IN ('smart', 'query') AND (refreshed_at IS NULL OR refreshed_at < now() - $1::interval) ORDER BY refreshed_at NULLS FIRST LIMIT 1000`,
		fmt.Sprintf("%d seconds", int(Every.Seconds())))
	if err != nil {
		return err
	}
	type due struct{ ws, id string }
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
		var d due
		return d, row.Scan(&d.ws, &d.id)
	})
	if err != nil {
		return err
	}
	for _, d := range list {
		if _, err := Enqueue(ctx, s.Pool, d.ws, d.id, false); err != nil {
			return err
		}
	}
	return nil
}
