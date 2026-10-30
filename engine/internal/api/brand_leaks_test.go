package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.yaml.in/yaml/v4"

	spec "github.com/outflareai/outflare/api"
)

// brandWide lists the GET endpoints that answer for the whole workspace on
// purpose, so a picked brand doesn't narrow them, with the reason. Anything
// else that returns another brand's records under ?brand= fails
// TestNoBrandLeaks.
var brandWide = map[string]string{
	"/v1/workspaces/{ws}/brands":            "the brand list itself, for the switcher",
	"/v1/workspaces/{ws}/brand-rules":       "workspace rules about every brand",
	"/v1/workspaces/{ws}/members":           "people and the brands they may see",
	"/v1/workspaces/{ws}/invites":           "invites and the brands they give",
	"/v1/workspaces/{ws}/api-tokens":        "tokens and the brand each is kept to",
	"/v1/workspaces/{ws}/agent-apps":        "connected AI apps and the brand each is kept to",
	"/v1/workspaces/{ws}/senders":           "senders are the team's people and personas; their mailboxes carry the brand",
	"/v1/workspaces/{ws}/connectors":        "the connector catalog with every connection",
	"/v1/workspaces/{ws}/oauth-connections": "sign-ins held for the workspace",
	"/v1/workspaces/{ws}/webhooks":          "webhook endpoints are set up for the workspace",
	"/v1/workspaces/{ws}/spend":             "spend is the workspace's bill",
	"/v1/workspaces/{ws}/spend-recurring":   "spend is the workspace's bill",
	"/v1/workspaces/{ws}/ai-usage":          "AI spend is the workspace's bill",
	"/v1/workspaces/{ws}/jobs":              "background work of the whole server",
	"/v1/workspaces/{ws}/export":            "a full export of the workspace",
	"/v1/workspaces/{ws}/query/tables":      "the table list, not rows",
	"/v1/workspaces/{ws}/context":           "the company context names every brand, so agents know how they relate",
	"/v1/workspaces/{ws}/sending-servers":   "sending servers are shared by every brand",
	"/v1/workspaces/{ws}/suppressions":      "an unsubscribe counts for every brand",
	"/v1/workspaces/{ws}/imports":           "imports bring in the workspace's records",
	"/v1/workspaces/{ws}/crm-imports":       "imports bring in the workspace's records",
	"/v1/workspaces/{ws}/verifications":     "an address checked once is known for every brand",
	"/v1/workspaces/{ws}/digests/preview":   "digests report on the whole workspace",
	"/v1/workspaces/{ws}/digests":           "digests report on the whole workspace",
	// The catalog rows carry their brand since 0316, but reads and writes
	// stay per workspace until the catalog-per-brand step of
	// plans/brands/README.md switches them; take these off then.
	"/v1/workspaces/{ws}/catalog":             "the catalog is read per workspace until the catalog-per-brand step",
	"/v1/workspaces/{ws}/catalog/types":       "the catalog is read per workspace until the catalog-per-brand step",
	"/v1/workspaces/{ws}/catalog/option-sets": "the catalog is read per workspace until the catalog-per-brand step",
	"/v1/workspaces/{ws}/catalog/events":      "the catalog is read per workspace until the catalog-per-brand step",
}

// sweepQuery is the rest of the query string the sweep sends, for
// endpoints that need one. Asking Alpha about Bravo's website or searching
// for Bravo must not find it either.
var sweepQuery = map[string]string{
	"/v1/workspaces/{ws}/search":            "q=bravo",
	"/v1/workspaces/{ws}/outcomes/export":   "dataset=report",
	"/v1/workspaces/{ws}/seo/history":       "report=keywords&host=bravo.example",
	"/v1/workspaces/{ws}/analytics/compare": "host=bravo.example",
	"/v1/workspaces/{ws}/records/lookup":    "namespace=email&value=leak@bravo.example",
}

// TestNoBrandLeaks fills one brand (Bravo) with a row in every table that
// belongs to a brand, and in the tables keyed by its website, then calls
// every list endpoint with the other brand (Alpha) picked. None may answer
// with anything of Bravo's. A new table with brand_id is filled
// automatically, so a new endpoint that forgets ?brand= fails here.
func TestNoBrandLeaks(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Alpha", "website": "alpha.example"})
	bravo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Bravo", "website": "bravo.example"})["id"].(string)

	s := &seeder{t: t, pool: e.pool, ws: ws, brand: bravo, rows: map[string]map[string]any{}, failed: map[string]string{}}
	s.load()
	for _, tbl := range s.wanted() {
		s.seed(tbl)
	}
	// Connections shared by every brand hold every brand's numbers, keyed
	// by website: fill those for Bravo's website too.
	shared := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})["id"].(string)
	s.fixed = map[string]map[string]any{"integrations": {"id": shared, "workspace_id": ws}}
	for _, tbl := range s.wanted() {
		if s.has(tbl, "integration_id") && !s.branded[tbl] {
			delete(s.rows, tbl)
			s.seed(tbl)
		}
	}
	var missed []string
	for tbl, why := range s.failed {
		if s.branded[tbl] && !slices.Contains(skipSeed, tbl) {
			missed = append(missed, tbl+": "+why)
		}
	}
	sort.Strings(missed)
	if len(missed) > 0 {
		t.Fatalf("could not fill these brand tables with Bravo's rows; teach the seeder their columns (valueFor):\n%s", strings.Join(missed, "\n"))
	}

	leak := regexp.MustCompile(`(?i)bravo|` + regexp.QuoteMeta(bravo))
	sweep := func(brand string) (checked int, found []string) {
		for _, path := range workspaceGets(t) {
			if _, ok := brandWide[path]; ok {
				continue
			}
			p := strings.ReplaceAll(path, "{ws}", ws)
			p = strings.ReplaceAll(p, "{brand}", "alpha")
			if strings.Contains(p, "{") {
				continue // one item, opened by its id
			}
			q := "?brand=" + brand
			if extra := sweepQuery[path]; extra != "" {
				q += "&" + extra
			}
			req := httptest.NewRequest("GET", p+q, nil)
			req.AddCookie(owner.cookie)
			rec := httptest.NewRecorder()
			e.h.ServeHTTP(rec, req)
			if rec.Code != 200 {
				continue
			}
			checked++
			body := rec.Body.String()
			if m := leak.FindString(body); m != "" {
				at := strings.Index(body, m)
				found = append(found, fmt.Sprintf("%s: …%s…", path, body[max(0, at-120):min(len(body), at+80)]))
			}
		}
		return checked, found
	}
	// Under all brands Bravo's rows show up, so the sweep reaches them.
	if _, found := sweep("all"); len(found) < 50 {
		t.Fatalf("only %d endpoints show Bravo's rows under all brands; the seeding is not reaching them", len(found))
	}
	checked, leaks := sweep("alpha")
	if checked < 100 {
		t.Fatalf("only %d endpoints answered; the sweep is not reaching them", checked)
	}
	if len(leaks) > 0 {
		t.Fatalf("with Alpha picked, %d endpoints answer with Bravo's records:\n%s", len(leaks), strings.Join(leaks, "\n"))
	}
}

// workspaceGets lists the spec's GET paths under a workspace that need no
// query parameter.
func workspaceGets(t *testing.T) []string {
	type param struct {
		Ref      string `yaml:"$ref"`
		In       string `yaml:"in"`
		Required bool   `yaml:"required"`
	}
	var doc struct {
		Paths      map[string]map[string]yaml.Node `yaml:"paths"`
		Components struct {
			Parameters map[string]param `yaml:"parameters"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(spec.OpenAPI, &doc); err != nil {
		t.Fatal(err)
	}
	var out []string
	for p, ops := range doc.Paths {
		node, ok := ops["get"]
		if !ok || !strings.HasPrefix(p, "/v1/workspaces/{ws}/") {
			continue
		}
		var op struct {
			Parameters []param `yaml:"parameters"`
		}
		if err := node.Decode(&op); err != nil {
			t.Fatal(err)
		}
		needs := false
		for _, pr := range op.Parameters {
			if pr.Ref != "" {
				pr = doc.Components.Parameters[strings.TrimPrefix(pr.Ref, "#/components/parameters/")]
			}
			needs = needs || (pr.In == "query" && pr.Required)
		}
		if _, ok := sweepQuery[p]; ok || !needs {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// seeder puts one row of Bravo's in each table that belongs to a brand,
// filling required columns from their types and constraints, and making
// the rows they point at first.
type seeder struct {
	t       *testing.T
	pool    *pgxpool.Pool
	ws      string
	brand   string
	cols    map[string][]column
	fks     map[string][]fk
	checks  map[string][]string
	branded map[string]bool
	rows    map[string]map[string]any
	failed  map[string]string
	want    []string
	fixed   map[string]map[string]any
	depth   int
}

type column struct {
	name, typ, udt string
	nullable, def  bool
}

type fk struct {
	cols, refCols []string
	ref           string
}

// skipSeed are tables the sweep leaves alone: access records, not data.
var skipSeed = []string{"workspaces", "users", "actors", "sessions", "memberships", "invites", "api_tokens", "agent_apps", "oauth_codes", "brands", "brand_context", "jobs", "auth_attempts"}

func (s *seeder) load() {
	ctx := context.Background()
	s.cols, s.fks, s.checks, s.branded = map[string][]column{}, map[string][]fk{}, map[string][]string{}, map[string]bool{}
	rows, err := s.pool.Query(ctx, `SELECT table_name, column_name, data_type, udt_name, is_nullable = 'YES',
		column_default IS NOT NULL OR is_identity = 'YES' OR is_generated = 'ALWAYS'
		FROM information_schema.columns c WHERE table_schema = 'public'
		AND EXISTS (SELECT 1 FROM information_schema.tables x WHERE x.table_name = c.table_name AND x.table_schema = 'public' AND x.table_type = 'BASE TABLE')
		ORDER BY table_name, ordinal_position`)
	if err != nil {
		s.t.Fatal(err)
	}
	for rows.Next() {
		var tbl string
		var c column
		if err := rows.Scan(&tbl, &c.name, &c.typ, &c.udt, &c.nullable, &c.def); err != nil {
			s.t.Fatal(err)
		}
		s.cols[tbl] = append(s.cols[tbl], c)
		if c.name == "brand_id" || c.name == "brand_ids" {
			s.branded[tbl] = true
		}
	}
	rows.Close()
	rows, err = s.pool.Query(ctx, `SELECT conrelid::regclass::text, confrelid::regclass::text,
		(SELECT array_agg(attname ORDER BY k.n) FROM unnest(conkey) WITH ORDINALITY k(a, n) JOIN pg_attribute ON attrelid = conrelid AND attnum = k.a),
		(SELECT array_agg(attname ORDER BY k.n) FROM unnest(confkey) WITH ORDINALITY k(a, n) JOIN pg_attribute ON attrelid = confrelid AND attnum = k.a)
		FROM pg_constraint WHERE contype = 'f' AND connamespace = 'public'::regnamespace`)
	if err != nil {
		s.t.Fatal(err)
	}
	for rows.Next() {
		var tbl string
		var f fk
		if err := rows.Scan(&tbl, &f.ref, &f.cols, &f.refCols); err != nil {
			s.t.Fatal(err)
		}
		s.fks[tbl] = append(s.fks[tbl], f)
	}
	rows.Close()
	rows, err = s.pool.Query(ctx, `SELECT conrelid::regclass::text, pg_get_constraintdef(oid) FROM pg_constraint WHERE contype = 'c' AND connamespace = 'public'::regnamespace`)
	if err != nil {
		s.t.Fatal(err)
	}
	for rows.Next() {
		var tbl, def string
		if err := rows.Scan(&tbl, &def); err != nil {
			s.t.Fatal(err)
		}
		s.checks[tbl] = append(s.checks[tbl], def)
	}
	rows.Close()
}

func (s *seeder) has(tbl, col string) bool {
	return slices.ContainsFunc(s.cols[tbl], func(c column) bool { return c.name == col })
}

// wanted is every workspace table that belongs to a brand, is keyed by a
// website's host, or hangs off one of those.
func (s *seeder) wanted() []string {
	if s.want != nil {
		return s.want
	}
	want := map[string]bool{}
	for tbl := range s.cols {
		if slices.Contains(skipSeed, tbl) || !s.has(tbl, "workspace_id") {
			continue
		}
		if s.branded[tbl] || s.has(tbl, "host") {
			want[tbl] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for tbl := range s.cols {
			if want[tbl] || slices.Contains(skipSeed, tbl) {
				continue
			}
			for _, f := range s.fks[tbl] {
				if want[f.ref] && f.ref != tbl {
					want[tbl], changed = true, true
					break
				}
			}
		}
	}
	var out []string
	for tbl := range want {
		out = append(out, tbl)
	}
	sort.Strings(out)
	s.want = out
	return out
}

// seedValues are values for columns whose constraints the seeder can't
// work out, as table.column. They are set even where the column may be null.
var seedValues = map[string]string{
	"mailbox_platforms.status":       "ok",
	"sending_domains.spf":            "pending",
	"sending_domains.dkim":           "pending",
	"sending_domains.dmarc":          "pending",
	"request_logs.surface":           "api",
	"fixes.source":                   "seo_audit",
	"fixes.rule":                     "title_missing",
	"fixes.subject_type":             "seo_site",
	"allocation_changes.action":      "assign",
	"deploys.kind":                   "push",
	"integrations.connector":         "plausible",
	"social_accounts.channel":        "x",
	"integrations.brand_ids":         "{}",
	"customer_events.name":           "bravo_event",
	"site_keys.key":                  "ofk_bravo",
	"social_watches.keywords":        "{bravo}",
	"spend_entries.units":            "1",
	"tracking_campaigns.code":        "bravoleak1",
	"tracking_campaigns.medium":      "email",
	"tracking_campaigns.source":      "bravo",
	"reviews.platform":               "g2",
	"record_types.key":               "bravo_type",
	"option_sets.key":                "bravo_options",
	"field_definitions.key":          "bravo_field",
	"event_definitions.name":         "bravo.defined",
	"record_identities.namespace":    "email",
	"record_relationships.kind":      "bravo_link",
	"record_merge_candidates.reason": "email",
	"import_items.kind":              "campaign",
}

// seedPairs are tables that point at two different rows of one table (a
// link between two records), as the second column, the table and whether
// the first must sort before the second.
var seedPairs = map[string]struct {
	a, b, ref string
	ordered   bool
}{
	"record_relationships":    {"from_id", "to_id", "records", false},
	"record_merge_candidates": {"record_a", "record_b", "records", true},
}

var checkIn = regexp.MustCompile(`'([^']*)'`)

// valueFor is a value for a column the seeder must fill.
func (s *seeder) valueFor(tbl string, c column) (string, any) {
	cast := "::" + c.udt
	if c.typ == "ARRAY" {
		cast = "::" + strings.TrimPrefix(c.udt, "_") + "[]"
	}
	switch c.name {
	case "workspace_id":
		return cast, s.ws
	case "brand_id":
		return cast, s.brand
	case "brand_ids":
		return cast, "{" + s.brand + "}"
	case "host", "hostname", "domain", "site":
		return cast, "bravo.example"
	case "email", "from_email", "address", "recipient":
		return cast, "leak@bravo.example"
	case "url", "website", "page_url", "target":
		return cast, "https://bravo.example/"
	case "emit_event":
		return cast, "bravo.emitted"
	}
	if tbl == "rules" && c.name == "trigger" {
		return cast, `{"kind": "event", "event": "bravo.happened"}`
	}
	if c.typ == "ARRAY" {
		return cast, "{}"
	}
	switch c.udt {
	case "text", "varchar", "citext", "bpchar":
		for _, def := range s.checks[tbl] {
			if !strings.Contains(def, "ANY (ARRAY[") {
				continue
			}
			if strings.Contains(def, "("+c.name+" ") || strings.Contains(def, "(("+c.name+")") || strings.Contains(def, " "+c.name+" ") {
				if m := checkIn.FindStringSubmatch(def); m != nil {
					return cast, m[1]
				}
			}
		}
		return cast, "bravo-" + c.name
	case "int2", "int4", "int8", "numeric", "float4", "float8":
		return cast, "1"
	case "bool":
		return cast, "false"
	case "timestamptz", "timestamp":
		return cast, "2026-01-01T00:00:00Z"
	case "date":
		return cast, "2026-01-01"
	case "interval":
		return cast, "1 day"
	case "jsonb", "json":
		return cast, "{}"
	case "uuid":
		return "::uuid", "00000000-0000-4000-8000-000000000001"
	case "bytea":
		return "::bytea", "\\x00"
	case "inet", "cidr":
		return cast, "192.0.2.1"
	}
	var label string
	if err := s.pool.QueryRow(context.Background(), `SELECT enumlabel FROM pg_enum e JOIN pg_type t ON t.oid = e.enumtypid WHERE t.typname = $1 ORDER BY enumsortorder LIMIT 1`, c.udt).Scan(&label); err == nil {
		return cast, label
	}
	return cast, "bravo"
}

// parent returns a row of tbl to point at: the workspace's own for
// workspaces and people, Bravo for brands, or a new row.
func (s *seeder) parent(tbl string) map[string]any {
	if r, ok := s.fixed[tbl]; ok {
		return r
	}
	ctx := context.Background()
	var q string
	switch tbl {
	case "workspaces":
		q = `SELECT to_jsonb(x) FROM workspaces x WHERE id = $1`
	case "brands":
		q = `SELECT to_jsonb(x) FROM brands x WHERE workspace_id = $1 AND handle = 'bravo'`
	case "actors":
		q = `SELECT to_jsonb(x) FROM actors x WHERE workspace_id = $1 OR workspace_id IS NULL LIMIT 1`
	case "users":
		q = `SELECT to_jsonb(x) FROM users x WHERE $1::uuid IS NOT NULL LIMIT 1`
	default:
		return s.seed(tbl)
	}
	var raw []byte
	if err := s.pool.QueryRow(ctx, q, s.ws).Scan(&raw); err != nil {
		if q2 := strings.Replace(q, "workspace_id = $1 OR workspace_id IS NULL", "$1::uuid IS NOT NULL", 1); q2 != q && s.pool.QueryRow(ctx, q2, s.ws).Scan(&raw) == nil {
			goto ok
		}
		return nil
	}
ok:
	var row map[string]any
	_ = json.Unmarshal(raw, &row)
	return row
}

// seed inserts one row in tbl and returns it, or nil when it can't.
func (s *seeder) seed(tbl string) map[string]any {
	if r, ok := s.rows[tbl]; ok {
		return r
	}
	if _, ok := s.failed[tbl]; ok || s.depth > 12 {
		return nil
	}
	s.depth++
	defer func() { s.depth-- }()
	s.rows[tbl] = nil // a cycle stops here
	set := map[string]string{}
	var args []any
	put := func(col, cast string, v any) {
		args = append(args, v)
		set[col] = fmt.Sprintf("$%d%s", len(args), cast)
	}
	for _, f := range s.fks[tbl] {
		if f.ref == tbl {
			continue
		}
		need := slices.ContainsFunc(f.cols, func(col string) bool {
			for _, c := range s.cols[tbl] {
				if c.name == col {
					return !c.nullable && !c.def
				}
			}
			return false
		})
		if !need && !s.branded[f.ref] && !slices.Contains(s.wanted(), f.ref) {
			continue
		}
		row := s.parent(f.ref)
		if p, ok := seedPairs[tbl]; ok && p.ref == f.ref && slices.Contains(f.cols, p.b) {
			// The second end is another row of the same table.
			saved := s.rows[p.ref]
			delete(s.rows, p.ref)
			row = s.seed(p.ref)
			s.rows[p.ref] = saved
		}
		if row == nil {
			if need {
				delete(s.rows, tbl)
				s.failed[tbl] = "no " + f.ref + " row to point at"
				return nil
			}
			continue
		}
		for i, col := range f.cols {
			if _, done := set[col]; done {
				continue
			}
			v := row[f.refCols[i]]
			if v == nil {
				continue
			}
			for _, c := range s.cols[tbl] {
				if c.name == col {
					put(col, "::"+c.udt, fmt.Sprint(v))
				}
			}
		}
	}
	if p, ok := seedPairs[tbl]; ok && p.ordered && set[p.a] != "" && set[p.b] != "" {
		a, b := set[p.a], set[p.b]
		set[p.a], set[p.b] = "LEAST("+a+", "+b+")", "GREATEST("+a+", "+b+")"
	}
	for _, c := range s.cols[tbl] {
		if _, done := set[c.name]; done {
			continue
		}
		if v, ok := seedValues[tbl+"."+c.name]; ok {
			cast, _ := s.valueFor(tbl, c)
			put(c.name, cast, v)
		}
	}
	for _, c := range s.cols[tbl] {
		if _, done := set[c.name]; done {
			continue
		}
		if (c.nullable || c.def) && !slices.Contains([]string{"workspace_id", "brand_id", "brand_ids", "host"}, c.name) {
			continue
		}
		cast, v := s.valueFor(tbl, c)
		if c.name == "brand_ids" && !s.branded[tbl] {
			continue
		}
		put(c.name, cast, v)
	}
	var cols, vals []string
	for c, v := range set {
		cols = append(cols, pgx.Identifier{c}.Sanitize())
		vals = append(vals, v)
	}
	q := fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s) RETURNING to_jsonb(%s.*)`, pgx.Identifier{tbl}.Sanitize(), strings.Join(cols, ", "), strings.Join(vals, ", "), pgx.Identifier{tbl}.Sanitize())
	var raw []byte
	if err := s.pool.QueryRow(context.Background(), q, args...).Scan(&raw); err != nil {
		delete(s.rows, tbl)
		s.failed[tbl] = err.Error()
		return nil
	}
	var row map[string]any
	_ = json.Unmarshal(raw, &row)
	s.rows[tbl] = row
	return row
}
