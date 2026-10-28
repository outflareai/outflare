package customers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/classify"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// A lead's details live on its person: name, job title, LinkedIn, phone
// and company. Whenever a lead is added, imported, edited or enriched,
// SyncLeads copies what it knows onto the person record, and merge tags
// in cold email read the person back (PersonVars), so a correction made in
// the CRM is the one that goes out.
//
// A lead's values are written with the source import, which every field
// ranks below a person's own edit, first-party data, a CRM sync and
// enrichment: they fill what is empty, and update what an import set
// before, but never overwrite a better source.

// leadSource is the attribute source lead details are written with.
const leadSource = "import"

// JobLeadFields copies the details of a workspace's existing leads onto
// their people, in batches (migration 0232).
const JobLeadFields = "customers.lead_fields"

// leadFieldsBatch is how many leads one job run copies.
const leadFieldsBatch = 500

var e164 = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

// leadPhone is a phone number in international format, or "".
func leadPhone(s string) string {
	s = strings.NewReplacer(" ", "", "-", "", "(", "", ")", "", ".", "").Replace(strings.TrimSpace(s))
	if strings.HasPrefix(s, "00") {
		s = "+" + s[2:]
	}
	if !e164.MatchString(s) {
		return ""
	}
	return s
}

// leadURL is a LinkedIn or other profile link as an http(s) URL, or "".
func leadURL(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t\n") || len(s) > 2000 {
		return ""
	}
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		s = "https://" + s
	}
	if !strings.Contains(strings.SplitN(strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://"), "/", 2)[0], ".") {
		return ""
	}
	return s
}

// SyncLeads copies these leads' details onto their person records. Leads
// not linked to a person yet are left for LinkLeads, which calls this. It
// returns how many people changed.
func SyncLeads(ctx context.Context, tx pgx.Tx, workspaceID string, leadIDs []string) (int, error) {
	if len(leadIDs) == 0 {
		return 0, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT l.record_id::text, l.email, l.first_name, l.last_name, l.title, l.linkedin_url, l.phone, l.company, l.website,
			r.name, r.attributes, r.attribute_sources
		FROM leads l JOIN records r ON r.id = l.record_id AND r.record_type = 'person' AND r.merged_into IS NULL
		WHERE l.workspace_id = $1 AND l.id = ANY($2::uuid[])
		ORDER BY l.record_id, l.updated_at
		FOR UPDATE OF r`, workspaceID, leadIDs)
	if err != nil {
		return 0, err
	}
	type person struct {
		id, email, first, last, title, linkedin, phone, company, website, name string
		attrs                                                                  map[string]any
		sources                                                                map[string]string
	}
	var people []*person
	byID := map[string]*person{}
	for rows.Next() {
		p := &person{}
		if err := rows.Scan(&p.id, &p.email, &p.first, &p.last, &p.title, &p.linkedin, &p.phone, &p.company, &p.website,
			&p.name, &p.attrs, &p.sources); err != nil {
			rows.Close()
			return 0, err
		}
		// Two leads of one person: the one changed last wins.
		if prev, ok := byID[p.id]; ok {
			*prev = *p
			continue
		}
		byID[p.id] = p
		people = append(people, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// Companies first, all at once: the website's domain, or else the
	// email's when it is a company's own.
	intel := classify.Builtin()
	var companies []Company
	domainOf := map[string]string{}
	for _, p := range people {
		if refs, _ := p.attrs["companies"].([]any); len(refs) > 0 {
			continue
		}
		d := CompanyDomain(p.website)
		if d == "" && p.company != "" {
			if at := strings.LastIndexByte(p.email, '@'); at >= 0 {
				if e := CompanyDomain(p.email[at+1:]); e != "" && !intel.MailboxProvider(e) {
					d = e
				}
			}
		}
		if d == "" || intel.MailboxProvider(d) {
			continue
		}
		domainOf[p.id] = d
		companies = append(companies, Company{Domain: d, Name: p.company})
	}
	accounts, err := EnsureCompanies(ctx, tx, workspaceID, nil, "lead", companies)
	if err != nil {
		return 0, err
	}

	changed := 0
	last := ""
	for _, p := range people {
		want := map[string]any{"first_name": strings.TrimSpace(p.first), "last_name": strings.TrimSpace(p.last),
			"job_title": strings.TrimSpace(p.title), "linkedin_url": leadURL(p.linkedin)}
		if acct := accounts[domainOf[p.id]]; acct != "" {
			want["companies"] = []any{acct}
		}
		if p.attrs == nil {
			p.attrs = map[string]any{}
		}
		if p.sources == nil {
			p.sources = map[string]string{}
		}
		dirty := false
		for k, v := range want {
			if s, ok := v.(string); ok && s == "" {
				continue
			}
			prev, had := p.attrs[k]
			if src := p.sources[k]; had && src != "" && src != leadSource {
				continue // a better source set it
			}
			if had && sameValue(prev, v) {
				continue
			}
			if err := attributeChange(ctx, tx, workspaceID, p.id, k, prev, had, v); err != nil {
				return 0, err
			}
			if k == "companies" {
				if _, err := tx.Exec(ctx, `INSERT INTO record_relationships (workspace_id, from_id, to_id, kind, field_key)
					VALUES ($1, $2, $3, 'companies', 'companies') ON CONFLICT DO NOTHING`, workspaceID, p.id, v.([]any)[0]); err != nil {
					return 0, err
				}
			}
			p.attrs[k], p.sources[k] = v, leadSource
			dirty = true
		}
		name := p.name
		if full := strings.TrimSpace(p.first + " " + p.last); name == "" && full != "" {
			if err := attributeChange(ctx, tx, workspaceID, p.id, "name", "", true, full); err != nil {
				return 0, err
			}
			name, dirty = full, true
		}
		if phone := leadPhone(p.phone); phone != "" {
			tag, err := tx.Exec(ctx, `INSERT INTO record_identities (workspace_id, record_id, namespace, value, verified, source)
				VALUES ($1, $2, 'phone', $3, false, 'lead') ON CONFLICT DO NOTHING`, workspaceID, p.id, phone)
			if err != nil {
				return 0, err
			}
			dirty = dirty || tag.RowsAffected() > 0
		}
		if !dirty {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE records SET name = $2, attributes = $3, attribute_sources = $4, version = version + 1, updated_at = now()
			WHERE id = $1`, p.id, name, p.attrs, p.sources); err != nil {
			return 0, err
		}
		changed++
		last = p.id
	}
	// One person changed is record.updated, as anywhere else; more are one
	// event for the lot, so a big import never floods webhooks.
	switch {
	case changed == 1:
		return changed, audit.Record(ctx, tx, audit.Event{WorkspaceID: workspaceID, Action: "record.updated", TargetType: "record", TargetID: last,
			ActorType: "system", Data: map[string]any{"type": "person", "via": "lead"}})
	case changed > 1:
		return changed, audit.Record(ctx, tx, audit.Event{WorkspaceID: workspaceID, Action: "record.bulk_updated", TargetType: "workspace",
			TargetID: workspaceID, ActorType: "system", Data: map[string]any{"type": "person", "via": "lead", "count": changed}})
	}
	return 0, nil
}

func sameValue(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func attributeChange(ctx context.Context, tx pgx.Tx, ws, id, key string, prev any, had bool, next any) error {
	var old *string
	if had {
		b, _ := json.Marshal(prev)
		s := string(b)
		old = &s
	}
	now, _ := json.Marshal(next)
	if _, err := tx.Exec(ctx, `INSERT INTO attribute_changes (workspace_id, record_id, field_key, old_value, new_value, source)
		VALUES ($1, $2, $3, $4, $5, $6)`, ws, id, key, old, string(now), leadSource); err != nil {
		return err
	}
	return FieldChanged(ctx, tx, ws, FieldChange{RecordID: id, Field: key, Old: prev, HadOld: had, Source: leadSource})
}

// PersonVars puts the lead's person into a cold email's merge tags: the
// person's name, job title, LinkedIn and company win over the lead's copy,
// so a correction made in the CRM goes out, and the person's other text
// fields (by field key) fill tags the lead leaves out.
func PersonVars(ctx context.Context, q jobs.Querier, leadID string, vars map[string]string) error {
	var name string
	var attrs map[string]any
	var company *string
	err := q.QueryRow(ctx, `
		SELECT r.name, r.attributes, c.name FROM leads l
		JOIN records r ON r.id = l.record_id AND r.merged_into IS NULL
		LEFT JOIN records c ON c.id::text = r.attributes -> 'companies' ->> 0 AND c.workspace_id = r.workspace_id AND c.merged_into IS NULL
		WHERE l.id = $1`, leadID).Scan(&name, &attrs, &company)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	for tag, field := range map[string]string{"first_name": "first_name", "last_name": "last_name", "title": "job_title", "linkedin_url": "linkedin_url"} {
		if s, ok := attrs[field].(string); ok && strings.TrimSpace(s) != "" {
			vars[tag] = s
		}
	}
	if company != nil && strings.TrimSpace(*company) != "" && !strings.Contains(*company, ".") {
		vars["company"] = *company
	}
	for k, v := range attrs {
		if _, set := vars[k]; set {
			continue
		}
		if s, ok := v.(string); ok && s != "" {
			vars[k] = s
		}
	}
	return nil
}

// LeadFields runs JobLeadFields.
type LeadFields struct {
	Pool *pgxpool.Pool
}

// Register adds the job to the queue.
func (l *LeadFields) Register(q *jobs.Queue) { q.Handle(JobLeadFields, l.job) }

func (l *LeadFields) job(ctx context.Context, j jobs.Job) error {
	var p struct {
		After string `json:"after"`
	}
	if err := json.Unmarshal(j.Payload, &p); err != nil || j.WorkspaceID == nil {
		return fmt.Errorf("%w: bad lead fields payload", jobs.ErrPermanent)
	}
	ws := *j.WorkspaceID
	if p.After == "" {
		p.After = "00000000-0000-0000-0000-000000000000"
	}
	var next string
	err := pgx.BeginFunc(ctx, l.Pool, func(tx pgx.Tx) error {
		var ids []string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(array_agg(id::text ORDER BY id), '{}') FROM (
				SELECT id FROM leads WHERE workspace_id = $1 AND id > $2 AND record_id IS NOT NULL ORDER BY id LIMIT $3) x`,
			ws, p.After, leadFieldsBatch).Scan(&ids); err != nil {
			return err
		}
		if _, err := SyncLeads(ctx, tx, ws, ids); err != nil {
			return err
		}
		if len(ids) == leadFieldsBatch {
			next = ids[len(ids)-1]
		}
		return nil
	})
	if err != nil || next == "" {
		return err
	}
	// Carry on in a new job, so each batch commits on its own.
	_, err = jobs.Enqueue(ctx, l.Pool, jobs.NewJob{Kind: JobLeadFields, WorkspaceID: &ws, Payload: map[string]string{"after": next},
		IdempotencyKey: "lead_fields:" + ws + ":" + next})
	return err
}
