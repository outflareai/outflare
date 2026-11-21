package adconv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Audiences: a brand names one of its lists as an audience and chooses the
// ad accounts it goes to (ad_audiences, ad_audience_destinations). A job
// per destination makes the customer list on the network the first time,
// then once a day sends what changed: people who joined the list and may
// be added (consent as for conversions, for ad data and ad
// personalisation) go in with their hashed emails and phones; people who
// left the list, withdrew consent, or whose record was merged away or
// archived come out. ad_audience_members holds who was sent, so a sync
// never sends the whole list again. An erased person's identifiers are
// gone with them, so they leave when their membership expires on the
// network.

const (
	// JobAudience syncs one destination of an audience.
	JobAudience = "ads.audience_sync"
	// audienceEvery is how often a destination is synced.
	audienceEvery = 23 * time.Hour
	// audienceMax is how many people one sync adds or takes out; the next
	// sync carries on.
	audienceMax = 100000
	// AudienceDescription is written on the list Outflare makes.
	AudienceDescription = "Kept in step with a list in Outflare."
)

type audiencePayload struct {
	Destination string `json:"destination_id"`
}

// EnqueueAudience asks for a sync of one destination; key makes the
// request idempotent (the day, or a reason).
func EnqueueAudience(ctx context.Context, q jobs.Querier, ws, destinationID, key string) (int64, error) {
	return jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobAudience, WorkspaceID: &ws, Payload: audiencePayload{Destination: destinationID},
		IdempotencyKey: "audience:" + destinationID + ":" + key, MaxAttempts: 3})
}

// queueAudiences asks for a sync of every destination that is due.
func (s *Sender) queueAudiences(ctx context.Context) error {
	rows, err := s.Pool.Query(ctx, `SELECT d.id, d.workspace_id FROM ad_audience_destinations d
		JOIN ad_audiences a ON a.id = d.audience_id
		JOIN ad_accounts c ON c.id = d.ad_account_id
		WHERE a.enabled AND a.list_id IS NOT NULL AND c.status = 'active'
		  AND (d.synced_at IS NULL OR d.synced_at < now() - $1::interval)`, fmt.Sprintf("%d seconds", int(audienceEvery.Seconds())))
	if err != nil {
		return err
	}
	type due struct{ id, ws string }
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.ws); err != nil {
			rows.Close()
			return err
		}
		list = append(list, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	day := s.now().UTC().Format("2006-01-02")
	for _, d := range list {
		if _, err := EnqueueAudience(ctx, s.Pool, d.ws, d.id, day); err != nil {
			return err
		}
	}
	return nil
}

func (s *Sender) audienceJob(ctx context.Context, job jobs.Job) error {
	var p audiencePayload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return nil
	}
	ws := *job.WorkspaceID
	res, err := s.SyncAudience(ctx, ws, p.Destination)
	ev := track.Event{Name: "ad_audience.synced", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"destination_id": p.Destination, "connector": res.Connector, "members": res.Members, "added": res.Added,
			"removed": res.Removed, "skipped": res.Skipped, "created": res.Created, "excluded": res.Excluded}}
	var db dbError
	switch {
	case err == nil:
		metrics.AdsFeedBatch("audience", "sent")
		track.Emit(ctx, ev)
		return nil
	case errors.Is(err, connectors.ErrNotFound):
		return nil
	case errors.As(err, &db):
		track.Fail(ctx, s.Log, ev, "internal", err)
		return err
	}
	metrics.AdsFeedBatch("audience", "failed")
	kind := connectors.KindOf(err)
	ev.Outcome = track.Failure
	if kind == connectors.ErrUnavailable {
		ev.Outcome = track.Error
	}
	track.Fail(ctx, s.Log, ev, "connector_"+kind, err)
	if kind == connectors.ErrUnavailable || kind == connectors.ErrQuota {
		return err
	}
	return nil
}

// AudienceResult counts what one sync did.
type AudienceResult struct {
	Connector string `json:"connector"`
	// Members is how many people Outflare has on the network's list now;
	// Added and Removed what this sync changed; Skipped how many people
	// in the list could not go (no consent, nothing to match on).
	Members int `json:"members"`
	Added   int `json:"added"`
	Removed int `json:"removed"`
	Skipped int `json:"skipped"`
	// Created is true when this sync made the list on the network.
	Created bool `json:"created"`
	// Excluded is how many campaigns this sync queued the list's
	// exclusion on (an audience with exclude on).
	Excluded int `json:"excluded"`
}

type audiencePerson struct {
	id     string
	member connectors.AudienceMember
}

// SyncAudience brings one destination's list on the network in step with
// the audience's list, and writes how it went on the destination.
// connectors.ErrNotFound means there is nothing to sync: the destination,
// its audience's list or its account is gone, or the audience is off.
func (s *Sender) SyncAudience(ctx context.Context, ws, destinationID string) (AudienceResult, error) {
	var res AudienceResult
	var integrationID, brand, name, listID, kind, externalID string
	var acct connectors.AdAccount
	var accountID string
	var meta []byte
	var exclude bool
	err := s.Pool.QueryRow(ctx, `SELECT c.id, c.integration_id, c.connector, c.external_id, c.name, c.currency, c.time_zone, c.meta,
			a.brand_id, a.name, a.list_id, l.kind, d.external_id, a.exclude
		FROM ad_audience_destinations d
		JOIN ad_audiences a ON a.id = d.audience_id AND a.enabled
		JOIN smart_lists l ON l.id = a.list_id AND l.workspace_id = a.workspace_id AND l.record_type = 'person'
		JOIN ad_accounts c ON c.id = d.ad_account_id AND c.workspace_id = d.workspace_id AND c.status = 'active'
		WHERE d.workspace_id = $1 AND d.id = $2`, ws, destinationID).Scan(
		&accountID, &integrationID, &res.Connector, &acct.ID, &acct.Name, &acct.Currency, &acct.TimeZone, &meta, &brand, &name, &listID, &kind, &externalID, &exclude)
	if errors.Is(err, pgx.ErrNoRows) {
		return res, connectors.ErrNotFound
	}
	if err != nil {
		return res, dbError{err}
	}
	_ = json.Unmarshal(meta, &acct.Meta)
	finish := func(requestID string, size *connectors.AudienceSize, callErr error) {
		msg := ""
		if callErr != nil {
			msg = trim(callErr.Error(), 300)
		}
		var sz []byte
		if size != nil {
			sz, _ = json.Marshal(size)
		}
		_, _ = s.Pool.Exec(ctx, `UPDATE ad_audience_destinations d SET synced_at = $3, error = $4,
				request_id = CASE WHEN $5 <> '' THEN $5 ELSE request_id END, skipped = $6,
				size = COALESCE($7::jsonb, size),
				members = (SELECT count(*)::int FROM ad_audience_members m WHERE m.destination_id = d.id)
			WHERE d.workspace_id = $1 AND d.id = $2`, ws, destinationID, s.now(), msg, trim(requestID, 200), res.Skipped, sz)
	}
	fail := func(err error) (AudienceResult, error) {
		finish("", nil, err)
		return res, err
	}
	o, err := s.Store.Open(ctx, s.Pool, ws, integrationID)
	if err != nil {
		return fail(err)
	}
	feeder, ok := o.Connector.(connectors.AudienceCreator)
	if !ok || !o.Connector.Definition().Has(connectors.CapAdsAudiences) {
		return fail(connectors.Errf(connectors.ErrInvalid, "%s cannot fill audiences from here", o.Connector.Definition().Name))
	}
	if externalID == "" {
		id, err := feeder.CreateAudience(ctx, o.Client, o.Creds, acct, AudienceListName(name), AudienceDescription)
		if err != nil {
			_ = s.Store.Checked(ctx, s.Pool, ws, o.ID, nil, err)
			return fail(err)
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE ad_audience_destinations SET external_id = $3 WHERE workspace_id = $1 AND id = $2`, ws, destinationID, id); err != nil {
			return res, dbError{err}
		}
		externalID, res.Created = id, true
	}
	mode, err := ConsentMode(ctx, s.Pool, ws, brand)
	if err != nil {
		return res, dbError{err}
	}
	// People in the list who haven't been sent, and people sent who are
	// no longer in it.
	join, err := s.audiencePeople(ctx, ws, `SELECT m.record_id FROM `+listTable(kind)+` m
		JOIN records r ON r.id = m.record_id AND r.workspace_id = m.workspace_id
		WHERE m.workspace_id = $1 AND m.list_id = $2 AND r.record_type = 'person'
		  AND r.merged_into IS NULL AND r.archived_at IS NULL AND r.deleted_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM ad_audience_members x WHERE x.destination_id = $3 AND x.record_id = m.record_id)
		ORDER BY m.record_id LIMIT $4`, listID, destinationID, audienceMax)
	if err != nil {
		return res, dbError{err}
	}
	leave, err := s.audiencePeople(ctx, ws, `SELECT x.record_id FROM ad_audience_members x
		JOIN records r ON r.id = x.record_id
		WHERE x.workspace_id = $1 AND x.destination_id = $3 AND $4 > 0
		  AND (r.merged_into IS NOT NULL OR r.archived_at IS NOT NULL OR r.deleted_at IS NOT NULL
		       OR NOT EXISTS (SELECT 1 FROM `+listTable(kind)+` m WHERE m.list_id = $2 AND m.record_id = x.record_id))
		ORDER BY x.record_id LIMIT $4`, listID, destinationID, audienceMax)
	if err != nil {
		return res, dbError{err}
	}
	// People already sent who withdrew consent since come out too.
	sent, err := s.audienceSent(ctx, ws, destinationID, leave)
	if err != nil {
		return res, dbError{err}
	}
	for _, p := range sent {
		ok, _, _, err := AudienceConsent(ctx, s.Pool, ws, p.id, mode)
		if err != nil {
			return res, dbError{err}
		}
		if !ok {
			leave = append(leave, p)
		}
	}
	var add []audiencePerson
	for _, p := range join {
		if len(p.member.Emails) == 0 && len(p.member.Phones) == 0 {
			res.Skipped++
			continue
		}
		ok, granted, _, err := AudienceConsent(ctx, s.Pool, ws, p.id, mode)
		if err != nil {
			return res, dbError{err}
		}
		if !ok {
			res.Skipped++
			continue
		}
		p.member.ConsentGranted = granted
		add = append(add, p)
	}
	requestID := ""
	if len(add) > 0 {
		if requestID, err = feeder.IngestAudience(ctx, o.Client, o.Creds, acct, externalID, connectors.AudienceAdd, members(add)); err != nil {
			_ = s.Store.Checked(ctx, s.Pool, ws, o.ID, nil, err)
			return fail(err)
		}
		if _, err := s.Pool.Exec(ctx, `INSERT INTO ad_audience_members (workspace_id, brand_id, destination_id, record_id)
			SELECT $1, $2, $3, unnest($4::uuid[]) ON CONFLICT DO NOTHING`, ws, brand, destinationID, ids(add)); err != nil {
			return res, dbError{err}
		}
		res.Added = len(add)
	}
	if len(leave) > 0 {
		// Someone with nothing left to match on (merged away) only leaves
		// Outflare's own record of who was sent.
		var known []audiencePerson
		for _, p := range leave {
			if len(p.member.Emails) > 0 || len(p.member.Phones) > 0 {
				known = append(known, p)
			}
		}
		if len(known) > 0 {
			if _, err := feeder.IngestAudience(ctx, o.Client, o.Creds, acct, externalID, connectors.AudienceRemove, members(known)); err != nil {
				_ = s.Store.Checked(ctx, s.Pool, ws, o.ID, nil, err)
				finish(requestID, nil, err)
				return res, err
			}
		}
		if _, err := s.Pool.Exec(ctx, `DELETE FROM ad_audience_members WHERE workspace_id = $1 AND destination_id = $2 AND record_id = ANY($3::uuid[])`,
			ws, destinationID, ids(leave)); err != nil {
			return res, dbError{err}
		}
		res.Removed = len(leave)
	}
	_ = s.Store.Checked(ctx, s.Pool, ws, o.ID, nil, nil)
	// The network's own count is nice to have: a failure to read it never
	// fails the sync.
	var size *connectors.AudienceSize
	if sizer, ok := o.Connector.(connectors.AudienceSizer); ok && !res.Created {
		if sz, err := sizer.AudienceSize(ctx, o.Client, o.Creds, acct, externalID); err == nil {
			size = &sz
		}
	}
	finish(requestID, size, nil)
	// An excluded audience: every active campaign of the account that
	// hasn't had the exclusion gets it queued.
	if exclude && CanExclude(o.Connector) {
		if res.Excluded, err = s.excludeEverywhere(ctx, ws, brand, destinationID); err != nil {
			return res, dbError{err}
		}
	}
	if err := s.Pool.QueryRow(ctx, `SELECT members FROM ad_audience_destinations WHERE workspace_id = $1 AND id = $2`, ws, destinationID).Scan(&res.Members); err != nil {
		return res, dbError{err}
	}
	return res, nil
}

// AudienceListName is the name of the list Outflare makes on a network
// for an audience.
func AudienceListName(name string) string { return trim(name, 100) + " (Outflare)" }

func listTable(kind string) string {
	if kind == "static" {
		return "static_list_members"
	}
	return "smart_list_members"
}

func members(ps []audiencePerson) []connectors.AudienceMember {
	out := make([]connectors.AudienceMember, len(ps))
	for i, p := range ps {
		out[i] = p.member
	}
	return out
}

func ids(ps []audiencePerson) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.id
	}
	return out
}

// audiencePeople reads the people a query picks ($1 the workspace, then
// args) with their hashed emails and phones.
func (s *Sender) audiencePeople(ctx context.Context, ws, pick string, args ...any) ([]audiencePerson, error) {
	return audiencePeople(ctx, s.Pool, ws, pick, args...)
}

func audiencePeople(ctx context.Context, pool *pgxpool.Pool, ws, pick string, args ...any) ([]audiencePerson, error) {
	rows, err := pool.Query(ctx, `WITH picked AS (`+pick+`)
		SELECT p.record_id,
			coalesce(array_agg(i.value) FILTER (WHERE i.namespace = 'email'), '{}'), coalesce(array_agg(i.value) FILTER (WHERE i.namespace = 'phone'), '{}')
		FROM picked p
		LEFT JOIN record_identities i ON i.record_id = p.record_id AND i.workspace_id = $1 AND i.valid_to IS NULL AND i.namespace IN ('email', 'phone')
		GROUP BY p.record_id ORDER BY p.record_id`, append([]any{ws}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []audiencePerson
	for rows.Next() {
		var p audiencePerson
		var emails, phones []string
		if err := rows.Scan(&p.id, &emails, &phones); err != nil {
			return nil, err
		}
		for _, e := range emails {
			p.member.Emails = append(p.member.Emails, connectors.HashID(e))
			p.member.GoogleEmails = append(p.member.GoogleEmails, connectors.HashGoogleEmail(e))
		}
		for _, ph := range phones {
			p.member.Phones = append(p.member.Phones, connectors.HashID(ph))
			p.member.PhoneDigits = append(p.member.PhoneDigits, connectors.HashID(strings.TrimPrefix(ph, "+")))
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// audienceSent reads the people already sent to a destination, leaving
// out the ones about to be taken out.
func (s *Sender) audienceSent(ctx context.Context, ws, destinationID string, leaving []audiencePerson) ([]audiencePerson, error) {
	return s.audiencePeople(ctx, ws, `SELECT x.record_id FROM ad_audience_members x
		WHERE x.workspace_id = $1 AND x.destination_id = $2 AND NOT (x.record_id = ANY($3::uuid[]))
		ORDER BY x.record_id LIMIT $4`, destinationID, ids(leaving), audienceMax)
}

// AudienceFile is an audience's people as a file a network that only takes
// uploads can read: the hashed emails and phones (E.164 with the plus) of
// everyone in the list who may be added, and how many could not go. The
// same consent rules as a sync apply.
func AudienceFile(ctx context.Context, pool *pgxpool.Pool, ws, brand, listID, kind string) (people []connectors.AudienceMember, skipped int, err error) {
	all, err := audiencePeople(ctx, pool, ws, `SELECT m.record_id FROM `+listTable(kind)+` m
		JOIN records r ON r.id = m.record_id AND r.workspace_id = m.workspace_id
		WHERE m.workspace_id = $1 AND m.list_id = $2 AND r.record_type = 'person'
		  AND r.merged_into IS NULL AND r.archived_at IS NULL AND r.deleted_at IS NULL
		ORDER BY m.record_id LIMIT $3`, listID, audienceMax)
	if err != nil {
		return nil, 0, err
	}
	mode, err := ConsentMode(ctx, pool, ws, brand)
	if err != nil {
		return nil, 0, err
	}
	for _, p := range all {
		if len(p.member.Emails) == 0 && len(p.member.Phones) == 0 {
			skipped++
			continue
		}
		ok, _, _, err := AudienceConsent(ctx, pool, ws, p.id, mode)
		if err != nil {
			return nil, 0, err
		}
		if !ok {
			skipped++
			continue
		}
		people = append(people, p.member)
	}
	return people, skipped, nil
}
