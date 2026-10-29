package api

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Install health: what a workspace put on its websites or wired to
// Outflare, checked for having gone quiet, fallen behind or broken. Outflare
// is young and changes fast, so a script copied months ago, an SDK pinned
// to an old version or an API route on its way out are found here before
// they quietly stop counting. It is worked out on read; a loop also opens
// and closes install_issues so each problem is announced once
// (install.issue_opened, a webhook) and again when it clears.

const (
	// siteScriptVersion is the version /site.js sends (VERSION in site.js);
	// siteScriptMinVersion is the oldest the server still takes as is.
	// Copies from before the script sent a version count as "1".
	siteScriptVersion    = 8
	siteScriptMinVersion = 1

	// A site key whose site sent visits but none for this long is silent.
	siteSilentAfter = 48 * time.Hour
	// A new key with no visit after this long is not installed.
	siteMissingAfter = 24 * time.Hour

	installHealthEvery = 10 * time.Minute
	// Calls looked at for old clients and deprecated routes: the newest
	// ones, at most a week old.
	installLogWindow = 20000
)

// scriptVersion reads the version a browser sent; "" for old copies.
func scriptVersion(v string) string {
	if n, err := strconv.Atoi(v); err == nil && n > 0 && n < 1000 {
		return strconv.Itoa(n)
	}
	return ""
}

// clientRelease is one of Outflare's own clients, found in request logs by
// its Outflare-Client header (client) or User-Agent (user_agent) prefix.
type clientRelease struct {
	prefix, column, name, upgrade string
	latest, min                   string
}

// clientReleases are the newest and oldest supported version of each
// client. A test keeps latest equal to the packages' own versions; raise
// min when a release stops working with this server.
var clientReleases = []clientRelease{
	{prefix: "sdk-ts/", column: "client", name: "TypeScript SDK (@outflare/sdk)", upgrade: "npm install @outflare/sdk@latest", latest: "0.1.0", min: "0.1.0"},
	{prefix: "mcp/", column: "client", name: "Outflare MCP server (@outflare/mcp)", upgrade: "Point your AI app at npx -y @outflare/mcp@latest, or use the hosted MCP URL, which is always current", latest: "0.1.0", min: "0.1.0"},
	{prefix: "outflare-python/", column: "user_agent", name: "Python SDK (outflare)", upgrade: "pip install -U outflare", latest: "0.1.0", min: "0.1.0"},
}

// deprecatedOperation is an API route on its way out. A test keeps this
// list equal to the operations api/openapi.yaml marks deprecated.
type deprecatedOperation struct {
	instead string
	// The day it stops working; zero while none is set.
	sunset time.Time
}

var deprecatedOperations = map[string]deprecatedOperation{
	"getMailboxWarmup": {instead: "GET /v1/workspaces/{ws}/mailboxes/{id}: its warmup.platform has the connected sequencer's warm-up score"},
}

// semverLess compares dotted version numbers; anything unreadable is not
// less.
func semverLess(a, b string) bool {
	pa, pb := strings.Split(strings.SplitN(a, "-", 2)[0], "."), strings.Split(strings.SplitN(b, "-", 2)[0], ".")
	for i := 0; i < 3; i++ {
		x, y := 0, 0
		var err error
		if i < len(pa) {
			if x, err = strconv.Atoi(pa[i]); err != nil {
				return false
			}
		}
		if i < len(pb) {
			if y, err = strconv.Atoi(pb[i]); err != nil {
				return false
			}
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// installCheck is one thing wrong with an install.
type installCheck struct {
	Key      string  `json:"key"`
	Kind     string  `json:"kind"`
	Severity string  `json:"severity"`
	Title    string  `json:"title"`
	Detail   string  `json:"detail"`
	BrandID  *string `json:"brand_id"`
	// What it is about: a site key, site or connection id, or a client.
	Subject string    `json:"subject"`
	Fix     statusFix `json:"fix"`
	// When Outflare first saw it; null until the health loop has.
	Since *time.Time `json:"since"`
}

type installHealthView struct {
	Checks []installCheck `json:"checks"`
	// What was looked at, so an empty list means healthy, not unchecked.
	Checked struct {
		SiteKeys    int `json:"site_keys"`
		Sites       int `json:"sites"`
		Connections int `json:"connections"`
		Clients     int `json:"clients"`
	} `json:"checked"`
	Latest struct {
		SiteScript string            `json:"site_script"`
		Clients    map[string]string `json:"clients"`
	} `json:"latest"`
	CheckedAt time.Time `json:"checked_at"`
}

// installChecks works out what is wrong with the workspace's installs, for
// one brand (its own and the workspace's) or every brand when brand is "".
func (s *Server) installChecks(ctx context.Context, ws, brand string) (installHealthView, error) {
	v := installHealthView{Checks: []installCheck{}, CheckedAt: time.Now().UTC()}
	v.Latest.SiteScript = strconv.Itoa(siteScriptVersion)
	v.Latest.Clients = map[string]string{}
	for _, c := range clientReleases {
		v.Latest.Clients[strings.TrimSuffix(c.prefix, "/")] = c.latest
	}
	brandPage := s.brandAppPath(ctx, ws, brand, "")
	page := func(p string) string { return brandPage + p }
	forBrand := func(b *string) bool { return brand == "" || b == nil || *b == brand }
	add := func(c installCheck) {
		if forBrand(c.BrandID) {
			v.Checks = append(v.Checks, c)
		}
	}

	// Website scripts.
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, brand_id::text, name, script_version, last_used_at, created_at FROM site_keys
		WHERE workspace_id = $1 AND revoked_at IS NULL ORDER BY created_at`, ws)
	if err != nil {
		return v, err
	}
	type key struct {
		id, name, version string
		brand             *string
		last              *time.Time
		created           time.Time
	}
	keys, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (key, error) {
		var k key
		err := row.Scan(&k.id, &k.brand, &k.name, &k.version, &k.last, &k.created)
		return k, err
	})
	if err != nil {
		return v, err
	}
	now := time.Now()
	for _, k := range keys {
		if !forBrand(k.brand) {
			continue
		}
		v.Checked.SiteKeys++
		fix := statusFix{Tool: "get_brand_setup", Page: page("setup"), Who: "person"}
		switch {
		case k.last == nil && now.Sub(k.created) > siteMissingAfter:
			add(installCheck{Key: "site_script_missing:" + k.id, Kind: "site_script_missing", Severity: "warning", BrandID: k.brand, Subject: k.id, Fix: fix,
				Title:  "No visits yet on " + k.name,
				Detail: "The website script isn't on the site, or nobody has visited since it was added. Open the site and look for site.js in the page source; the setup checklist has one-click ways to add it."})
		case k.last != nil && now.Sub(*k.last) > siteSilentAfter:
			add(installCheck{Key: "site_script_silent:" + k.id, Kind: "site_script_silent", Severity: "critical", BrandID: k.brand, Subject: k.id, Fix: fix,
				Title:  "No visits from " + k.name + " since " + k.last.UTC().Format("2 Jan"),
				Detail: "The website script stopped sending. It was probably removed in a redesign, blocked by a Content-Security-Policy, or the tag was paused in Tag Manager. Visits aren't counted until it's back."})
		case k.last != nil:
			n, _ := strconv.Atoi(k.version)
			if n == 0 {
				n = 1
			}
			if n < siteScriptVersion {
				sev, title := "warning", k.name+" runs an old copy of the website script"
				if n < siteScriptMinVersion {
					sev = "critical"
				}
				add(installCheck{Key: "site_script_outdated:" + k.id, Kind: "site_script_outdated", Severity: sev, BrandID: k.brand, Subject: k.id, Fix: fix, Title: title,
					Detail: "The site loads a saved copy of site.js (version " + strconv.Itoa(n) + ", now " + strconv.Itoa(siteScriptVersion) + ") instead of " +
						strings.TrimRight(s.publicURL, "/") + "/site.js, so it misses fixes and new tracking. Load the script from Outflare, which keeps it current."})
			}
		}
	}

	// Sites waiting for their proof.
	rows, err = s.pool.Query(ctx, `
		SELECT id::text, brand_id::text, host, verify_watch_until FROM seo_sites
		WHERE workspace_id = $1 AND verified_at IS NULL AND verify_watch_until IS NOT NULL`, ws)
	if err != nil {
		return v, err
	}
	type site struct {
		id, host string
		brand    *string
		until    time.Time
	}
	sites, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (site, error) {
		var x site
		err := row.Scan(&x.id, &x.brand, &x.host, &x.until)
		return x, err
	})
	if err != nil {
		return v, err
	}
	for _, x := range sites {
		if !forBrand(x.brand) {
			continue
		}
		v.Checked.Sites++
		if x.until.Before(now) {
			add(installCheck{Key: "site_unverified:" + x.id, Kind: "site_unverified", Severity: "warning", BrandID: x.brand, Subject: x.id,
				Fix:    statusFix{Tool: "check_brand_setup", Page: page("setup"), Who: "person"},
				Title:  "Outflare couldn't find the DNS record for " + x.host,
				Detail: "It looked for three days. Check the record's name and value on your DNS host, then check again from the setup checklist."})
		}
	}

	// Connections that fail.
	if s.integrations != nil {
		all, err := s.integrations.List(ctx, s.pool, ws, "")
		if err != nil {
			return v, err
		}
		for _, i := range all {
			if brand != "" && !i.Serves(brand, all) {
				continue
			}
			v.Checked.Connections++
			if i.Status != "error" {
				continue
			}
			name := i.Connector
			if d, ok := s.integrations.Registry.Definition(i.Connector); ok {
				name = d.Name
			}
			detail := "Outflare can't reach it with the saved sign-in, so nothing from it updates."
			if i.LastError != nil && *i.LastError != "" {
				detail = *i.LastError
			}
			c := installCheck{Key: "connection_broken:" + i.ID, Kind: "connection_broken", Severity: "critical", Subject: i.ID,
				Fix:   statusFix{Tool: "get_integration", Page: page("connectors"), Who: "person"},
				Title: name + " stopped working", Detail: detail}
			c.BrandID = i.BrandID
			v.Checks = append(v.Checks, c)
		}
	}

	// Outflare's own clients and API routes, from the newest calls.
	rows, err = s.pool.Query(ctx, `
		SELECT client, user_agent, operation, max(created_at) FROM (
			SELECT client, user_agent, operation, created_at FROM request_logs
			WHERE workspace_id = $1 ORDER BY id DESC LIMIT $2) l
		WHERE created_at > now() - interval '7 days' AND (client <> '' OR user_agent LIKE 'outflare-%' OR operation = ANY($3))
		GROUP BY 1, 2, 3`, ws, installLogWindow, deprecatedOps())
	if err != nil {
		return v, err
	}
	type seen struct {
		client, ua, op string
		at             time.Time
	}
	calls, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (seen, error) {
		var x seen
		err := row.Scan(&x.client, &x.ua, &x.op, &x.at)
		return x, err
	})
	if err != nil {
		return v, err
	}
	oldClients := map[string]installCheck{}
	clients := map[string]bool{}
	for _, call := range calls {
		if d, ok := deprecatedOperations[call.op]; ok {
			detail := "A token or app still calls " + call.op + ", which is deprecated. Use " + d.instead + "."
			if !d.sunset.IsZero() {
				detail += " It stops working on " + d.sunset.UTC().Format("2 Jan 2006") + "."
			}
			add(installCheck{Key: "api_deprecated:" + call.op, Kind: "api_deprecated", Severity: "warning", Subject: call.op,
				Fix:   statusFix{Page: page("logs"), Who: "person"},
				Title: "Calls to a deprecated API route (" + call.op + ")", Detail: detail})
		}
		for _, c := range clientReleases {
			raw := call.client
			if c.column == "user_agent" {
				raw = call.ua
			}
			if !strings.HasPrefix(raw, c.prefix) {
				continue
			}
			ver := strings.Fields(strings.TrimPrefix(raw, c.prefix) + " ")[0]
			clients[c.prefix+ver] = true
			if !semverLess(ver, c.latest) {
				continue
			}
			sev := "warning"
			if semverLess(ver, c.min) {
				sev = "critical"
			}
			k := "client_outdated:" + strings.TrimSuffix(c.prefix, "/") + "@" + ver
			oldClients[k] = installCheck{Key: k, Kind: "client_outdated", Severity: sev, Subject: c.prefix + ver,
				Fix:    statusFix{Page: page("logs"), Who: "person"},
				Title:  "An old " + c.name + " (" + ver + ") is calling the API",
				Detail: "The newest is " + c.latest + ". " + c.upgrade + "."}
		}
	}
	v.Checked.Clients = len(clients)
	for _, c := range oldClients {
		add(c)
	}

	// Remember when each problem was first seen.
	since := map[string]time.Time{}
	rows, err = s.pool.Query(ctx, `SELECT key, opened_at FROM install_issues WHERE workspace_id = $1 AND resolved_at IS NULL`, ws)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var k string
		var at time.Time
		if err := rows.Scan(&k, &at); err != nil {
			rows.Close()
			return v, err
		}
		since[k] = at
	}
	rows.Close()
	for i := range v.Checks {
		if at, ok := since[v.Checks[i].Key]; ok {
			v.Checks[i].Since = &at
		}
	}
	slices.SortStableFunc(v.Checks, func(x, y installCheck) int {
		if d := severityRank[x.Severity] - severityRank[y.Severity]; d != 0 {
			return d
		}
		return strings.Compare(x.Key, y.Key)
	})
	return v, rows.Err()
}

func deprecatedOps() []string {
	out := make([]string, 0, len(deprecatedOperations))
	for op := range deprecatedOperations {
		out = append(out, op)
	}
	slices.Sort(out)
	return out
}

func (s *Server) getInstallHealth(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand := ""
	if ref := r.URL.Query().Get("brand"); ref != "" {
		id, err := s.brandIDFor(r.Context(), a.WorkspaceID, ref)
		if err == nil && !a.sees(&id) {
			err = errNotFound
		}
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "brand not found: brand is a brand's id or handle")
			return
		}
		brand = id
	} else if a.Brands != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "brand is required: this token sees only some brands")
		return
	}
	v, err := s.installChecks(r.Context(), a.WorkspaceID, brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// refreshInstallIssues opens an issue for each new problem and resolves
// the ones that cleared, in every workspace with something installed.
func (s *Server) refreshInstallIssues(ctx context.Context) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text FROM workspaces w WHERE
			EXISTS (SELECT 1 FROM site_keys k WHERE k.workspace_id = w.id AND k.revoked_at IS NULL)
			OR EXISTS (SELECT 1 FROM integrations i WHERE i.workspace_id = w.id)
			OR EXISTS (SELECT 1 FROM install_issues i WHERE i.workspace_id = w.id AND i.resolved_at IS NULL)
			OR EXISTS (SELECT 1 FROM seo_sites s WHERE s.workspace_id = w.id AND s.verify_watch_until IS NOT NULL AND s.verified_at IS NULL)`)
	if err != nil {
		track.Fail(ctx, s.log, track.Event{Name: "install.health_failed", ActorType: "system", Surface: "system"}, "internal", err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		track.Fail(ctx, s.log, track.Event{Name: "install.health_failed", ActorType: "system", Surface: "system"}, "internal", err)
		return
	}
	for _, ws := range list {
		if ctx.Err() != nil {
			return
		}
		if err := s.refreshWorkspaceIssues(ctx, ws); err != nil {
			track.Fail(ctx, s.log, track.Event{Name: "install.health_failed", WorkspaceID: ws, ActorType: "system", Surface: "system"}, "internal", err)
		}
	}
}

func (s *Server) refreshWorkspaceIssues(ctx context.Context, ws string) error {
	v, err := s.installChecks(ctx, ws, "")
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		open := map[string]string{}
		rows, err := tx.Query(ctx, `SELECT key, id::text FROM install_issues WHERE workspace_id = $1 AND resolved_at IS NULL FOR UPDATE`, ws)
		if err != nil {
			return err
		}
		for rows.Next() {
			var k, id string
			if err := rows.Scan(&k, &id); err != nil {
				rows.Close()
				return err
			}
			open[k] = id
		}
		rows.Close()
		now := map[string]bool{}
		for _, c := range v.Checks {
			now[c.Key] = true
			if id, ok := open[c.Key]; ok {
				if _, err := tx.Exec(ctx, `UPDATE install_issues SET last_seen_at = now(), severity = $2, title = $3 WHERE id = $1`, id, c.Severity, c.Title); err != nil {
					return err
				}
				continue
			}
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO install_issues (workspace_id, brand_id, key, kind, severity, title) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`,
				ws, c.BrandID, c.Key, c.Kind, c.Severity, c.Title).Scan(&id); err != nil {
				return err
			}
			if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "install.issue_opened", TargetType: "install_issue", TargetID: id,
				BrandID: deref(c.BrandID), Data: map[string]any{"kind": c.Kind, "severity": c.Severity, "subject": c.Subject, "brand_id": c.BrandID}}); err != nil {
				return err
			}
		}
		for k, id := range open {
			if now[k] {
				continue
			}
			var kind string
			var brand *string
			if err := tx.QueryRow(ctx, `UPDATE install_issues SET resolved_at = now() WHERE id = $1 RETURNING kind, brand_id::text`, id).Scan(&kind, &brand); err != nil {
				return err
			}
			if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "install.issue_resolved", TargetType: "install_issue", TargetID: id,
				BrandID: deref(brand), Data: map[string]any{"kind": kind, "brand_id": brand}}); err != nil {
				return err
			}
		}
		return nil
	})
}
