package customers

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
)

// Company is a company to find or add by its domain.
type Company struct {
	Domain string
	Name   string
	// Field values for a new record (website, industry, country, ...), by
	// field key. Ignored when the company already exists: what is on its
	// record is kept.
	Attributes map[string]any
}

var companyDomain = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// CompanyDomain is a website or domain as a company identity: lower case,
// without scheme, path, port or "www.", or "" when it is not a domain.
func CompanyDomain(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, p := range []string{"https://", "http://"} {
		s = strings.TrimPrefix(s, p)
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if host, _, ok := strings.Cut(s, ":"); ok {
		s = host
	}
	s = strings.TrimSuffix(strings.TrimPrefix(s, "www."), ".")
	if len(s) > 253 || !companyDomain.MatchString(s) {
		return ""
	}
	return s
}

// EnsureCompanies returns the company record for each domain, adding
// prospect companies (of brand, nil for shared) for those nobody has, with
// via as the tool they came from (records.acquired_via). The
// domain is added unverified, so it never claims a domain a company record
// already proves. Run it in the same transaction as what it is for.
func EnsureCompanies(ctx context.Context, tx pgx.Tx, workspaceID string, brand *string, via string, companies []Company) (map[string]string, error) {
	out := map[string]string{}
	byDomain := map[string]Company{}
	var domains []string
	for _, c := range companies {
		d := CompanyDomain(c.Domain)
		if d == "" {
			continue
		}
		if _, ok := byDomain[d]; !ok {
			domains = append(domains, d)
		}
		byDomain[d] = c
	}
	if len(domains) == 0 {
		return out, nil
	}
	// A verified owner first, then the oldest record that names it.
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT ON (i.value) i.value, i.record_id::text
		FROM record_identities i JOIN records r ON r.id = i.record_id AND r.record_type = 'account' AND r.merged_into IS NULL
		WHERE i.workspace_id = $1 AND i.namespace = 'domain' AND i.value = ANY($2) AND i.valid_to IS NULL
		ORDER BY i.value, i.verified DESC, i.created_at`, workspaceID, domains)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d, id string
		if err := rows.Scan(&d, &id); err != nil {
			rows.Close()
			return nil, err
		}
		out[d] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var missing, names, attrs, sources []string
	for _, d := range domains {
		if _, ok := out[d]; ok {
			continue
		}
		c := byDomain[d]
		a, src := map[string]any{}, map[string]string{}
		for k, v := range c.Attributes {
			if s, ok := v.(string); (ok && strings.TrimSpace(s) == "") || v == nil {
				continue
			}
			a[k], src[k] = v, "enrichment"
		}
		if _, ok := a["website"]; !ok {
			a["website"], src["website"] = "https://"+d, "enrichment"
		}
		ab, _ := json.Marshal(a)
		sb, _ := json.Marshal(src)
		name := strings.TrimSpace(c.Name)
		if name == "" {
			name = d
		}
		missing = append(missing, d)
		names = append(names, name)
		attrs = append(attrs, string(ab))
		sources = append(sources, string(sb))
	}
	if len(missing) == 0 {
		return out, nil
	}
	rows, err = tx.Query(ctx, `
		WITH todo AS MATERIALIZED (
			SELECT gen_random_uuid() AS id, d, n, a::jsonb AS a, s::jsonb AS s FROM unnest($2::text[], $3::text[], $4::text[], $5::text[]) AS t(d, n, a, s)
		), made AS (
			INSERT INTO records (id, workspace_id, record_type, brand_id, name, attributes, attribute_sources, origin, acquired_via)
			SELECT id, $1, 'account', $6, n, a, s, 'prospect', left($7, 64) FROM todo
		), ids AS (
			INSERT INTO record_identities (workspace_id, record_id, namespace, value, verified, source)
			SELECT $1, id, 'domain', d, false, $7 FROM todo
		)
		SELECT d, id::text FROM todo`, workspaceID, missing, names, attrs, sources, brand, via)
	if err != nil {
		return nil, err
	}
	n, made := 0, ""
	for rows.Next() {
		var d, id string
		if err := rows.Scan(&d, &id); err != nil {
			rows.Close()
			return nil, err
		}
		out[d], made = id, id
		n++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if n == 1 {
		return out, audit.Record(ctx, tx, audit.Event{WorkspaceID: workspaceID, Action: "record.created", TargetType: "record", TargetID: made,
			ActorType: "system", Data: map[string]any{"type": "account", "origin": "prospect", "via": via, "identities": 1}})
	}
	return out, audit.Record(ctx, tx, audit.Event{WorkspaceID: workspaceID, Action: "record.bulk_created", TargetType: "workspace", TargetID: workspaceID,
		ActorType: "system", Data: map[string]any{"type": "account", "origin": "prospect", "via": via, "count": n}})
}
