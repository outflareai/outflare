package customers

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// Linking leads and activity to people. Every lead is a person record
// (origin prospect, or the person who already has its email), and every
// cold email, reply, meeting and deal points at its person, so a profile
// shows all of it. Migration 0120's triggers link activity as it is
// written; this file links leads, and the job catches up on older rows.

// JobLink links a workspace's unlinked leads and activity in batches.
const JobLink = "customers.link"

// linkBatch is how many leads one pass links.
const linkBatch = 500

// Person is someone to find or add by email.
type Person struct {
	Email string
	Name  string
	// Field values for a new record, by field key. Ignored when the
	// person already exists: what is on their record is kept.
	Attributes map[string]any
	// The tool a new record came from (records.acquired_via), such as
	// csv or apollo; EnsurePeople's via when empty.
	Via string
}

// EnsurePeople returns the person record for each email (lowercased), adding
// records of the given origin for those nobody has. An unverified email is
// added, so it never claims an address someone else proves they own. Run it
// in the same transaction as what it is for.
func EnsurePeople(ctx context.Context, tx pgx.Tx, workspaceID, origin, via string, people []Person) (map[string]string, error) {
	out := map[string]string{}
	if len(people) == 0 {
		return out, nil
	}
	byEmail := map[string]Person{}
	var emails []string
	for _, p := range people {
		e := strings.ToLower(strings.TrimSpace(p.Email))
		if e == "" {
			continue
		}
		if _, ok := byEmail[e]; !ok {
			emails = append(emails, e)
		}
		byEmail[e] = p
	}
	if len(emails) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT e, outflare_person_with_email($1, e)::text FROM unnest($2::text[]) AS e`, workspaceID, emails)
	if err != nil {
		return nil, err
	}
	var missing []string
	for rows.Next() {
		var e string
		var id *string
		if err := rows.Scan(&e, &id); err != nil {
			rows.Close()
			return nil, err
		}
		if id != nil {
			out[e] = *id
		} else {
			missing = append(missing, e)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(missing) == 0 {
		return out, nil
	}
	names := make([]string, len(missing))
	attrs := make([]string, len(missing))
	sources := make([]string, len(missing))
	vias := make([]string, len(missing))
	for i, e := range missing {
		p := byEmail[e]
		names[i] = strings.TrimSpace(p.Name)
		if vias[i] = p.Via; vias[i] == "" {
			vias[i] = via
		}
		vias[i] = vias[i][:min(len(vias[i]), 64)]
		a := map[string]any{}
		src := map[string]string{}
		for k, v := range p.Attributes {
			if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
				continue
			}
			if v == nil {
				continue
			}
			a[k], src[k] = v, "import"
		}
		ab, _ := json.Marshal(a)
		sb, _ := json.Marshal(src)
		attrs[i], sources[i] = string(ab), string(sb)
	}
	rows, err = tx.Query(ctx, `
		WITH todo AS MATERIALIZED (
			SELECT gen_random_uuid() AS id, e, n, a::jsonb AS a, s::jsonb AS s, v FROM unnest($2::text[], $3::text[], $4::text[], $5::text[], $8::text[]) AS t(e, n, a, s, v)
		), made AS (
			INSERT INTO records (id, workspace_id, record_type, name, attributes, attribute_sources, origin, acquired_via)
			SELECT id, $1, 'person', n, a, s, $6, v FROM todo
		), ids AS (
			INSERT INTO record_identities (workspace_id, record_id, namespace, value, verified, source)
			SELECT $1, id, 'email', e, false, $7 FROM todo
		)
		SELECT e, id::text FROM todo`, workspaceID, missing, names, attrs, sources, origin, via, vias)
	if err != nil {
		return nil, err
	}
	n, made := 0, ""
	for rows.Next() {
		var e, id string
		if err := rows.Scan(&e, &id); err != nil {
			rows.Close()
			return nil, err
		}
		out[e], made = id, id
		n++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// One new person is record.created, as anywhere else; more are one
	// event for the lot.
	if n == 1 {
		return out, audit.Record(ctx, tx, audit.Event{WorkspaceID: workspaceID, Action: "record.created", TargetType: "record", TargetID: made,
			ActorType: "system", Data: map[string]any{"type": "person", "origin": origin, "via": via, "identities": 1}})
	}
	return out, audit.Record(ctx, tx, audit.Event{WorkspaceID: workspaceID, Action: "record.bulk_created", TargetType: "workspace", TargetID: workspaceID,
		ActorType: "system", Data: map[string]any{"type": "person", "origin": origin, "via": via, "count": n}})
}

// LinkLeads gives the workspace's unlinked leads (these ids, or any when
// ids is nil, up to limit) their person record, and points their activity
// at it. It returns how many leads it linked.
func LinkLeads(ctx context.Context, tx pgx.Tx, workspaceID string, ids []string, limit int) (int, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, email, first_name, last_name, title, linkedin_url, source FROM leads
		WHERE workspace_id = $1 AND record_id IS NULL AND ($2::uuid[] IS NULL OR id = ANY($2))
		ORDER BY id LIMIT $3 FOR UPDATE SKIP LOCKED`, workspaceID, ids, limit)
	if err != nil {
		return 0, err
	}
	type lead struct{ id, email string }
	var leads []lead
	var people []Person
	for rows.Next() {
		var id, email, first, last, title, linkedin, source string
		if err := rows.Scan(&id, &email, &first, &last, &title, &linkedin, &source); err != nil {
			rows.Close()
			return 0, err
		}
		leads = append(leads, lead{id, strings.ToLower(strings.TrimSpace(email))})
		people = append(people, Person{Email: email, Name: strings.TrimSpace(first + " " + last), Attributes: map[string]any{
			"first_name": first, "last_name": last, "job_title": title, "linkedin_url": linkedin}, Via: source})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(leads) == 0 {
		return 0, nil
	}
	owners, err := EnsurePeople(ctx, tx, workspaceID, "prospect", "lead", people)
	if err != nil {
		return 0, err
	}
	leadIDs := make([]string, 0, len(leads))
	recordIDs := make([]string, 0, len(leads))
	for _, l := range leads {
		if r, ok := owners[l.email]; ok {
			leadIDs, recordIDs = append(leadIDs, l.id), append(recordIDs, r)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE leads l SET record_id = t.r FROM unnest($2::uuid[], $3::uuid[]) AS t(l, r)
		WHERE l.workspace_id = $1 AND l.id = t.l`, workspaceID, leadIDs, recordIDs); err != nil {
		return 0, err
	}
	// The lead's id is one of the person's identities, so a lead found
	// anywhere leads to them.
	if _, err := tx.Exec(ctx, `INSERT INTO record_identities (workspace_id, record_id, namespace, value, verified, verified_at, source)
		SELECT $1, t.r, 'outflare.lead', t.l::text, true, now(), 'system' FROM unnest($2::uuid[], $3::uuid[]) AS t(l, r)
		ON CONFLICT DO NOTHING`, workspaceID, leadIDs, recordIDs); err != nil {
		return 0, err
	}
	for _, table := range []string{"messages", "inbound_messages", "meetings", "deals"} {
		if _, err := tx.Exec(ctx, `UPDATE `+table+` a SET record_id = l.record_id FROM leads l
			WHERE a.workspace_id = $1 AND a.lead_id = l.id AND l.id = ANY($2::uuid[]) AND a.record_id IS NULL`, workspaceID, leadIDs); err != nil {
			return 0, err
		}
	}
	// A person who was already here gets what the lead knows, and every
	// person the lead's company and phone.
	if _, err := SyncLeads(ctx, tx, workspaceID, leadIDs); err != nil {
		return 0, err
	}
	return len(leadIDs), nil
}

// linkByEmail points older activity with no lead at the person who has its
// address, up to limit rows a table. It reports whether any table may have
// more.
func linkByEmail(ctx context.Context, tx pgx.Tx, workspaceID string, limit int) (bool, error) {
	more := false
	for _, t := range []struct{ table, column string }{{"inbound_messages", "from_email"}, {"meetings", "attendee_email"}, {"messages", "to_email"}} {
		tag, err := tx.Exec(ctx, `UPDATE `+t.table+` a SET record_id = p.r FROM (
				SELECT id, outflare_person_with_email(workspace_id, `+t.column+`) AS r FROM `+t.table+`
				WHERE workspace_id = $1 AND record_id IS NULL AND lead_id IS NULL AND `+t.column+` IS NOT NULL AND `+t.column+` <> ''
				ORDER BY id LIMIT $2) p
			WHERE a.id = p.id AND p.r IS NOT NULL`, workspaceID, limit)
		if err != nil {
			return false, err
		}
		if tag.RowsAffected() > 0 {
			more = true
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE emails e SET record_id = p.r FROM (
			SELECT e.id, i.record_id AS r FROM emails e
			JOIN record_identities i ON i.workspace_id = e.workspace_id AND i.namespace = 'email' AND i.value = lower(e.to_addresses[1])
				AND i.valid_to IS NULL AND i.verified
			JOIN records r ON r.id = i.record_id AND r.merged_into IS NULL
			WHERE e.workspace_id = $1 AND e.record_id IS NULL AND NOT e.test AND cardinality(e.to_addresses) = 1
			LIMIT $2) p
		WHERE e.id = p.id`, workspaceID, limit)
	if err != nil {
		return false, err
	}
	return more || tag.RowsAffected() > 0, nil
}

// Linker runs JobLink.
type Linker struct {
	Pool *pgxpool.Pool
}

// Register adds the link job to the queue.
func (l *Linker) Register(q *jobs.Queue) { q.Handle(JobLink, l.job) }

func (l *Linker) job(ctx context.Context, j jobs.Job) error {
	if j.WorkspaceID == nil {
		return nil
	}
	ws := *j.WorkspaceID
	more := false
	err := pgx.BeginFunc(ctx, l.Pool, func(tx pgx.Tx) error {
		n, err := LinkLeads(ctx, tx, ws, nil, linkBatch)
		if err != nil {
			return err
		}
		if n == linkBatch {
			more = true
			return nil
		}
		if more, err = linkByEmail(ctx, tx, ws, linkBatch); err != nil || more {
			return err
		}
		more, err = linkUnsubscribes(ctx, tx, ws, linkBatch)
		return err
	})
	if err != nil || !more {
		return err
	}
	// Carry on in a new job, so each batch commits on its own.
	_, err = jobs.Enqueue(ctx, l.Pool, jobs.NewJob{Kind: JobLink, WorkspaceID: &ws})
	return err
}

// Ref is a column that points at a record.
type Ref struct{ Table, Column string }

// Follows are the columns that move to the record kept when two records
// merge: everything Outflare knows a person did or was sent. A test fails
// when a column points at records and is neither here nor in Owned, so a
// new link is never left behind on a merged record.
var Follows = []Ref{
	{"customer_events", "record_id"}, {"customer_events", "account_id"}, {"consents", "record_id"},
	{"leads", "record_id"}, {"messages", "record_id"}, {"inbound_messages", "record_id"},
	{"meetings", "record_id"}, {"deals", "record_id"}, {"emails", "record_id"},
	{"email_broadcast_recipients", "record_id"}, {"suppressions", "record_id"},
	{"journey_runs", "record_id"}, {"payment_subscriptions", "record_id"}, {"seo_competitors", "record_id"},
	{"site_sessions", "record_id"}, {"site_visitors", "record_id"}, {"site_visitors", "account_id"},
	{"ad_conversion_sends", "record_id"}, {"attribution_conversions", "record_id"}, {"link_prospects", "record_id"},
	{"rule_firings", "record_id"},
}

// Owned are the record columns a merge handles itself (identities, links,
// history) or that belong to the merged record for good.
var Owned = []Ref{
	{"record_identities", "record_id"}, {"record_relationships", "from_id"}, {"record_relationships", "to_id"},
	{"attribute_changes", "record_id"}, {"records", "merged_into"},
	{"record_merges", "into_id"}, {"record_merges", "from_id"},
	{"record_merge_candidates", "record_a"}, {"record_merge_candidates", "record_b"},
	// An alert was about the record it was sent for.
	{"visitor_alerts", "record_id"},
	{"alerts", "record_id"},
	// The next refresh drops the merged record and adds the one kept, if
	// it matches.
	{"smart_list_members", "record_id"},
	// Where the merged record stood with each rule stays its own: the
	// record kept has its own state, so it neither fires again nor misses
	// its own timers.
	{"rule_states", "record_id"},
}

// Follow moves every Follows column from one record to another and
// returns how many rows moved, by table. Tables not created yet are
// skipped. A broadcast that reached both
// keeps the row it has for the record kept. A journey both are in keeps
// the kept record's run; the other's ends with exit_reason "merged".
func Follow(ctx context.Context, tx pgx.Tx, workspaceID, fromID, intoID string) (map[string]int, error) {
	moved := map[string]int{}
	if _, err := tx.Exec(ctx, `UPDATE journey_runs f SET status = 'exited', exit_reason = 'merged', finished_at = now(), next_at = NULL, updated_at = now()
		WHERE f.workspace_id = $3 AND f.record_id = $1 AND f.status = 'active'
		  AND EXISTS (SELECT 1 FROM journey_runs k WHERE k.journey_id = f.journey_id AND k.record_id = $2 AND k.status = 'active')`, fromID, intoID, workspaceID); err != nil {
		return nil, err
	}
	for _, r := range Follows {
		// A table a newer migration adds may not exist yet: its column is
		// listed here first so a rollback to this version still moves it.
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, r.Table).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		sql := `UPDATE ` + r.Table + ` SET ` + r.Column + ` = $2 WHERE workspace_id = $3 AND ` + r.Column + ` = $1`
		if r.Table == "email_broadcast_recipients" {
			sql += ` AND NOT EXISTS (SELECT 1 FROM email_broadcast_recipients o WHERE o.broadcast_id = email_broadcast_recipients.broadcast_id AND o.record_id = $2)`
		}
		// A rule that fired for both in the same episode keeps the kept
		// record's firing.
		if r.Table == "rule_firings" {
			sql += ` AND NOT EXISTS (SELECT 1 FROM rule_firings o WHERE o.rule_id = rule_firings.rule_id AND o.record_id = $2 AND o.episode_key = rule_firings.episode_key)`
		}
		tag, err := tx.Exec(ctx, sql, fromID, intoID, workspaceID)
		if err != nil {
			return nil, err
		}
		moved[r.Table] += int(tag.RowsAffected())
	}
	return moved, nil
}
