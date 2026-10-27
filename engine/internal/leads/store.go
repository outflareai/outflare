package leads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/verify"
)

// linkNow is the most leads an import links to their person records in
// its own transaction; bigger imports leave it to customers.JobLink.
const linkNow = 1000

// ImportResult summarises an import.
type ImportResult struct {
	Created    int        `json:"created"`
	Updated    int        `json:"updated"`
	Duplicates int        `json:"duplicates"` // rows merged into an earlier row for the same email
	Suppressed int        `json:"suppressed"` // imported leads that are on the suppression list
	Errors     []RowError `json:"errors"`
}

// Import upserts leads by email in one round trip: the rows are streamed into
// a temporary table with COPY, then merged. Non-empty imported fields
// overwrite stored ones, empty ones keep what is stored, and custom fields
// are merged. When listID is set every imported lead is added to that list.
// Leads must already be deduplicated (see Dedupe).
func Import(ctx context.Context, tx pgx.Tx, workspaceID, source string, listID *string, leads []Lead) (ImportResult, error) {
	res := ImportResult{Errors: []RowError{}}
	if len(leads) == 0 {
		return res, nil
	}
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE lead_import (
			email text, first_name text, last_name text, company text, title text,
			phone text, linkedin_url text, website text, custom jsonb
		) ON COMMIT DROP`); err != nil {
		return res, err
	}
	rows := make([][]any, len(leads))
	for i, l := range leads {
		custom := []byte("{}")
		if len(l.Custom) > 0 {
			var err error
			if custom, err = json.Marshal(l.Custom); err != nil {
				return res, err
			}
		}
		rows[i] = []any{l.Email, l.FirstName, l.LastName, l.Company, l.Title, l.Phone, l.LinkedInURL, l.Website, string(custom)}
	}
	cols := []string{"email", "first_name", "last_name", "company", "title", "phone", "linkedin_url", "website", "custom"}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"lead_import"}, cols, pgx.CopyFromRows(rows)); err != nil {
		return res, fmt.Errorf("copy leads: %w", err)
	}

	keep := func(c string) string {
		return fmt.Sprintf("%[1]s = CASE WHEN EXCLUDED.%[1]s = '' THEN leads.%[1]s ELSE EXCLUDED.%[1]s END", c)
	}
	sets := []string{}
	for _, c := range cols[1:8] {
		sets = append(sets, keep(c))
	}
	var ids []string
	err := tx.QueryRow(ctx, `
		WITH up AS (
			INSERT INTO leads (workspace_id, email, first_name, last_name, company, title, phone, linkedin_url, website, custom, source)
			SELECT $1, email, first_name, last_name, company, title, phone, linkedin_url, website, custom, $2 FROM lead_import
			ON CONFLICT (workspace_id, lower(email)) DO UPDATE SET
				`+strings.Join(sets, ",\n\t\t\t\t")+`,
				custom = leads.custom || EXCLUDED.custom,
				updated_at = now()
			RETURNING id, (xmax = 0) AS inserted
		), members AS (
			INSERT INTO lead_list_members (list_id, lead_id)
			SELECT $3::uuid, id FROM up WHERE $3::uuid IS NOT NULL
			ON CONFLICT DO NOTHING
		)
		SELECT count(*) FILTER (WHERE inserted), count(*) FILTER (WHERE NOT inserted), COALESCE(array_agg(id::text), '{}') FROM up`,
		workspaceID, source, listID,
	).Scan(&res.Created, &res.Updated, &ids)
	if err != nil {
		return res, err
	}
	// Each lead is a person: link small batches now, and leave big
	// imports to the link job so the import stays quick.
	if len(ids) <= linkNow {
		if _, err := customers.LinkLeads(ctx, tx, workspaceID, ids, len(ids)); err != nil {
			return res, err
		}
	} else if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: customers.JobLink, WorkspaceID: &workspaceID}); err != nil {
		return res, err
	}
	err = tx.QueryRow(ctx, `
		SELECT count(*) FROM lead_import i
		WHERE EXISTS (`+SuppressedSQL("$1", "i.email")+`)`, workspaceID).Scan(&res.Suppressed)
	if err != nil {
		return res, err
	}
	emails := make([]string, len(leads))
	for i, l := range leads {
		emails[i] = l.Email
	}
	return res, verify.OnImport(ctx, tx, workspaceID, emails)
}

// SuppressedSQL is a subquery body matching suppressions of an email
// expression in a workspace, by address or by domain.
func SuppressedSQL(ws, email string) string {
	return fmt.Sprintf(`SELECT 1 FROM suppressions s WHERE s.workspace_id = %[1]s AND (
		(s.kind = 'email' AND s.value = lower(%[2]s)) OR
		(s.kind = 'domain' AND s.value = lower(split_part(%[2]s, '@', 2))))`, ws, email)
}

// Suppression reasons. competitor, customer and partner are people never
// to cold email; legal is a request or a law that says so.
var Reasons = []string{"unsubscribed", "bounced", "complained", "manual", "do_not_contact", "competitor", "customer", "partner", "legal"}

// How a suppression was added.
const (
	MethodManual      = "manual"       // a person added it
	MethodAutomated   = "automated"    // a bounce, an unsubscribe link, a sync
	MethodAISuggested = "ai_suggested" // the AI read a reply as an opt-out
)

// Methods lists every method.
var Methods = []string{MethodManual, MethodAutomated, MethodAISuggested}

// ParseSuppression reads an email address or a domain (also accepted as
// "@acme-com.example" or a URL) and returns its kind and normalised value.
func ParseSuppression(v string) (kind, value string, err error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "", "", errors.New("empty value")
	}
	if i := strings.IndexByte(v, '@'); i > 0 {
		a, err := mail.ParseAddress(v)
		if err != nil || a.Address != v {
			return "", "", fmt.Errorf("invalid email %q", v)
		}
		return "email", v, nil
	}
	d := strings.TrimPrefix(v, "@")
	if strings.Contains(d, "://") {
		if u, err := url.Parse(d); err == nil {
			d = u.Hostname()
		}
	}
	d = strings.TrimSuffix(strings.SplitN(d, "/", 2)[0], ".")
	if !validDomain(d) {
		return "", "", fmt.Errorf("invalid email or domain %q", v)
	}
	return "domain", d, nil
}

func validDomain(d string) bool {
	if len(d) < 3 || len(d) > 253 || !strings.Contains(d, ".") {
		return false
	}
	for _, label := range strings.Split(d, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

// Suppression is one entry to add.
type Suppression struct {
	Kind, Value string
}

// AddSuppressions inserts entries added automatically, skipping ones
// already present, and returns how many were new.
func AddSuppressions(ctx context.Context, tx pgx.Tx, workspaceID, reason, source, note string, entries []Suppression) (int, error) {
	return Suppress(ctx, tx, workspaceID, Added{Reason: reason, Source: source, Note: note}, entries)
}

// Added says why, how and by whom entries join the suppression list.
type Added struct {
	Reason string
	// Source is the tool or feature: manual, api, reply, smtp,
	// emailbison_sync, apollo_import and so on.
	Source, Note string
	// Method is how: manual, automated (the default) or ai_suggested.
	Method string
	// The person or token who added them, if one did.
	ActorID, ActorType string
	// The connection they came from, if any.
	IntegrationID string
	// How sure the AI was, for ai_suggested entries.
	Confidence *float64
	// Ids that show why, such as the reply's.
	Evidence map[string]any
}

// Suppress inserts entries, skipping ones already present, and returns how
// many were new.
func Suppress(ctx context.Context, tx pgx.Tx, workspaceID string, by Added, entries []Suppression) (int, error) {
	if len(entries) == 0 {
		return 0, nil
	}
	reason, source, note := by.Reason, by.Source, by.Note
	if by.Method == "" {
		by.Method = MethodAutomated
	}
	if by.ActorType == "" {
		by.ActorType = "system"
	}
	if source == "" {
		source = "manual"
	}
	evidence := []byte("{}")
	if len(by.Evidence) > 0 {
		var err error
		if evidence, err = json.Marshal(by.Evidence); err != nil {
			return 0, err
		}
	}
	kinds := make([]string, len(entries))
	values := make([]string, len(entries))
	for i, e := range entries {
		kinds[i], values[i] = e.Kind, e.Value
	}
	rows, err := tx.Query(ctx, `
		INSERT INTO suppressions (workspace_id, kind, value, reason, source, note, method, added_by, added_by_type, integration_id, confidence, evidence)
		SELECT $1, k, v, $4, $5, $6, $7, $8::uuid, $9, $10::uuid, $11, $12 FROM unnest($2::text[], $3::text[]) AS t(k, v)
		ON CONFLICT (workspace_id, kind, value) DO NOTHING
		RETURNING kind, value`,
		workspaceID, kinds, values, reason, source, note, by.Method, nullable(by.ActorID), by.ActorType, nullable(by.IntegrationID), by.Confidence, string(evidence))
	if err != nil {
		return 0, err
	}
	added := 0
	var unsubs []customers.Unsubscribe
	for rows.Next() {
		var kind, value string
		if err := rows.Scan(&kind, &value); err != nil {
			rows.Close()
			return 0, err
		}
		added++
		if kind == "email" {
			unsubs = append(unsubs, customers.Unsubscribe{Email: value})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// An unsubscribe is also the person withdrawing consent to all
	// marketing email, on their record, so every module sees it.
	if reason == "unsubscribed" {
		if _, err := customers.WithdrawEmails(ctx, tx, workspaceID, source, "", unsubs); err != nil {
			return 0, err
		}
	}
	return added, nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
