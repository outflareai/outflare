package emailfind

import (
	"context"
	"encoding/json"
	"net"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is a pool or a transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Resolver looks up mail servers for provider detection. *net.Resolver
// satisfies it; nil uses the default resolver.
type Resolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
}

// Rebuild recomputes a domain's pattern profile from the workspace's own
// leads and their verification outcomes, and stores it. It spends nothing and
// reads only this workspace's data. An address we hold for a named person is a
// hit for the shape its local part fits; an address seen to bounce or verified
// undeliverable is a miss. The profile is per-workspace; nothing here crosses
// workspaces.
func Rebuild(ctx context.Context, q Querier, res Resolver, ws, domain string) (Profile, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	p := Profile{Domain: domain, Shapes: map[string]tally{}}
	if domain == "" {
		return p, nil
	}
	rows, err := q.Query(ctx, `
		SELECT l.first_name, l.last_name, lower(l.email) AS email,
		       coalesce(v.status, '') AS status, coalesce(v.catch_all, false) AS catch_all,
		       (s.value IS NOT NULL) AS bounced
		FROM leads l
		LEFT JOIN email_verifications v
		       ON v.workspace_id = l.workspace_id AND v.email = lower(l.email)
		LEFT JOIN suppressions s
		       ON s.workspace_id = l.workspace_id AND s.kind = 'email'
		      AND s.value = lower(l.email) AND s.reason = 'bounced'
		WHERE l.workspace_id = $1 AND l.domain = $2
		  AND l.first_name <> '' AND l.last_name <> ''`, ws, domain)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var first, last, email, status string
		var catchAll, bounced bool
		if err := rows.Scan(&first, &last, &email, &status, &catchAll, &bounced); err != nil {
			return p, err
		}
		if catchAll {
			p.CatchAll = true
		}
		names := inferShapes(first, last, localPart(email))
		if len(names) == 0 {
			continue // an address whose shape we don't recognise teaches nothing
		}
		valid := !(bounced || status == "undeliverable")
		p.observe(names, valid)
	}
	if err := rows.Err(); err != nil {
		return p, err
	}
	p.Provider = providerFromMX(ctx, res, domain)
	p.recompute()
	return p, save(ctx, q, ws, p)
}

func save(ctx context.Context, q Querier, ws string, p Profile) error {
	shapesJSON, err := json.Marshal(p.Shapes)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `
		INSERT INTO email_patterns (workspace_id, domain, shapes, top_shape, confidence,
			provider, catch_all, sample_size, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
		ON CONFLICT (workspace_id, domain) DO UPDATE SET
			shapes = EXCLUDED.shapes, top_shape = EXCLUDED.top_shape, confidence = EXCLUDED.confidence,
			provider = EXCLUDED.provider, catch_all = EXCLUDED.catch_all,
			sample_size = EXCLUDED.sample_size, updated_at = now()`,
		ws, p.Domain, shapesJSON, p.TopShape, p.Confidence, p.Provider, p.CatchAll, p.SampleSize)
	return err
}

// load returns a stored profile, or ok=false when the domain has none yet.
func load(ctx context.Context, q Querier, ws, domain string) (Profile, bool, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	p := Profile{Domain: domain, Shapes: map[string]tally{}}
	var shapesJSON []byte
	err := q.QueryRow(ctx, `
		SELECT shapes, top_shape, confidence, provider, catch_all, sample_size
		FROM email_patterns WHERE workspace_id = $1 AND domain = $2`, ws, domain).
		Scan(&shapesJSON, &p.TopShape, &p.Confidence, &p.Provider, &p.CatchAll, &p.SampleSize)
	if err == pgx.ErrNoRows {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	if err := json.Unmarshal(shapesJSON, &p.Shapes); err != nil {
		return p, false, err
	}
	if p.Shapes == nil {
		p.Shapes = map[string]tally{}
	}
	return p, true, nil
}

// providerFromMX classifies a domain's mail host from its MX records, for
// display and catch-all handling only. It never connects to a mailbox. An
// unknown or failed lookup returns "".
func providerFromMX(ctx context.Context, res Resolver, domain string) string {
	if res == nil {
		res = net.DefaultResolver
	}
	mxs, err := res.LookupMX(ctx, domain)
	if err != nil || len(mxs) == 0 {
		return ""
	}
	for _, mx := range mxs {
		h := strings.ToLower(mx.Host)
		switch {
		case strings.Contains(h, "google") || strings.Contains(h, "googlemail"):
			return "google"
		case strings.Contains(h, "outlook") || strings.Contains(h, "microsoft") || strings.Contains(h, "protection.outlook"):
			return "microsoft"
		}
	}
	return "other"
}
