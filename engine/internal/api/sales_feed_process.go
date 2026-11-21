package api

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/leadfeed"
	"github.com/outflareai/outflare/engine/internal/outcomes"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Reading one stored post (jobSalesFeedItem): the rules, then the
// workspace's own AI, decide whether it names a person, only a company, or
// nobody clear; feedApply (sales_feed_apply.go) makes the person, their
// company and, by the channel's kind, a Sales conversation or a signal.

// feedDeletedText is what Slack shows for a deleted message that still has
// replies.
const feedDeletedText = "This message was deleted."

// errFeedAIFailed is the item job's error when the AI fails: the AI's own
// error can quote the post, so it never leaves the job.
var errFeedAIFailed = errors.New("sales feed item: the AI read failed")

// feedItem is one row of sales_feed_items.
type feedItem struct {
	ID         string
	ChannelID  string // sales_feed_channels.id
	SlackTS    string
	Kind       string
	Status     string
	RecordID   *string
	LeadID     *string
	ResolvedBy *string
	Hash       string
}

// feedDecide says what a post is about: one person (email, and domain when
// their company can be told from the address), only a company (domain), a
// post that needs a person to look (needs_look), or spam. It is pure, so a
// preview can show what reading a post would do.
func feedDecide(x leadfeed.Extracted, r feedReading, ok bool) (email, domain, outcome string) {
	if ok && r.Spam && r.SpamConfidence >= feedSpamSure {
		return "", "", "spam"
	}
	switch {
	case len(x.Emails) == 1:
		email = x.Emails[0]
	case len(x.Emails) > 1:
		if !ok || r.Email == "" || !slices.Contains(x.Emails, r.Email) {
			return "", "", "needs_look"
		}
		email = r.Email
	}
	if email != "" {
		_, host, _ := strings.Cut(email, "@")
		return email, feedCompanyDomain(host), "person"
	}
	site := ""
	switch {
	case ok && r.Website != "":
		site = r.Website
	case len(x.Links) == 1:
		site = x.Links[0]
	}
	if d := feedCompanyDomain(site); d != "" {
		return "", d, "company"
	}
	return "", "", "needs_look"
}

// feedCompanyDomain is a website's or address's domain as a company
// identity, or "" when it is not one or is a personal mail provider.
func feedCompanyDomain(s string) string {
	d := customers.CompanyDomain(s)
	if d == "" || outcomes.IsFreeMail(d) {
		return ""
	}
	return d
}

// feedOwn is what belongs to the workspace and brand: every member's
// address (a teammate is never the person who wrote in, whatever their
// status or brands) and the brand's domains. owners holds the members a post
// may make an Owner: enabled, with access to the brand.
func (s *Server) feedOwn(ctx context.Context, a *access) (own leadfeed.Own, owners map[string]bool, err error) {
	own = leadfeed.Own{Members: map[string]string{}}
	owners = map[string]bool{}
	rows, err := s.pool.Query(ctx, `SELECT u.id::text, lower(u.email),
			u.disabled_at IS NULL AND (m.brand_ids IS NULL OR cardinality(m.brand_ids) = 0 OR $2::uuid = ANY(m.brand_ids))
		FROM memberships m JOIN users u ON u.id = m.user_id WHERE m.workspace_id = $1`, a.WorkspaceID, a.Brand)
	if err != nil {
		return own, owners, err
	}
	for rows.Next() {
		var id, email string
		var eligible bool
		if err := rows.Scan(&id, &email, &eligible); err != nil {
			rows.Close()
			return own, owners, err
		}
		own.Members[email] = id
		if eligible {
			owners[id] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return own, owners, err
	}
	var domains []string
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(domains, '{}') FROM brands WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, a.Brand).Scan(&domains); err != nil {
		return own, owners, err
	}
	for _, d := range domains {
		own.Domains = append(own.Domains, strings.ToLower(d))
	}
	return own, owners, nil
}

// lockFeedItem locks an item and opens its content. found is false when the
// item is gone or its text is no longer hash ("" for any): the job queued
// for the new text reads it.
func (s *Server) lockFeedItem(ctx context.Context, tx pgx.Tx, a *access, id, hash string) (it feedItem, c feedContent, found bool, err error) {
	var sealed []byte
	err = tx.QueryRow(ctx, `SELECT id::text,feed_channel_id::text,slack_ts,kind,status,record_id::text,lead_id::text,resolved_by::text,content_hash,content_sealed
		FROM sales_feed_items WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 FOR UPDATE`, a.WorkspaceID, a.Brand, id).
		Scan(&it.ID, &it.ChannelID, &it.SlackTS, &it.Kind, &it.Status, &it.RecordID, &it.LeadID, &it.ResolvedBy, &it.Hash, &sealed)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && hash != "" && it.Hash != hash) {
		return it, c, false, nil
	}
	if err != nil {
		return it, c, false, err
	}
	if err = s.salesOpen(sealed, it.ID, &c); err != nil {
		return it, c, false, err
	}
	return it, c, true, nil
}

// salesFeedItemJob reads one stored post and makes what it is about. It
// runs as the system: no person is behind it. A post its last attempt can't
// read either is marked failed, so it is listed under Failed instead of
// waiting unseen.
func (s *Server) salesFeedItemJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		ID    string `json:"id"`
		Brand string `json:"brand_id"`
	}
	if json.Unmarshal(job.Payload, &p) != nil || job.WorkspaceID == nil || !validUUID(p.ID) || !validUUID(p.Brand) {
		return jobs.ErrPermanent
	}
	a := &access{principal: principal{ActorType: "system"}, WorkspaceID: *job.WorkspaceID, Brand: p.Brand}
	err := s.feedReadItem(ctx, job, a, p.ID)
	if err == nil || job.Attempt < job.MaxAttempts || errors.Is(err, jobs.ErrPermanent) || ctx.Err() != nil {
		return err
	}
	// Only the code leaves the job: the error can quote the post.
	const code = "sales_feed_item_failed"
	if _, e := s.pool.Exec(ctx, `UPDATE sales_feed_items SET status=CASE WHEN record_id IS NULL AND status IN ('pending','failed') THEN 'failed' ELSE status END,
		error_code=$4,updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, p.ID, code); e != nil {
		return e
	}
	var channel string
	_ = s.pool.QueryRow(ctx, `SELECT feed_channel_id::text FROM sales_feed_items WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, p.ID).Scan(&channel)
	track.Fail(ctx, s.log, track.Event{Name: "sales_feed.item_failed", WorkspaceID: a.WorkspaceID, ActorType: "system", Surface: "job", Outcome: track.Error,
		Props: map[string]any{"brand_id": a.Brand, "feed_channel_id": channel, "item_id": p.ID}}, code, err)
	return nil
}

// feedReadItem is one attempt of salesFeedItemJob.
func (s *Server) feedReadItem(ctx context.Context, job jobs.Job, a *access, id string) error {
	p := struct{ ID string }{id}
	var it feedItem
	var c feedContent
	var found bool
	if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) (err error) {
		it, c, found, err = s.lockFeedItem(ctx, tx, a, p.ID, "")
		return err
	}); err != nil || !found {
		return err
	}
	ev := track.Event{WorkspaceID: a.WorkspaceID, ActorType: "system", Surface: "job",
		Props: map[string]any{"brand_id": a.Brand, "feed_channel_id": it.ChannelID, "item_id": it.ID, "kind": it.Kind}}

	if c.Text == feedDeletedText {
		// A deleted parent that still has replies: the item stays as it was.
		_, err := s.pool.Exec(ctx, `UPDATE sales_feed_items SET deleted_in_slack=true,updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 AND content_hash=$4`,
			a.WorkspaceID, a.Brand, it.ID, it.Hash)
		return err
	}
	ch, err := loadFeedChannel(ctx, s.pool, a, it.ChannelID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // removed or turned off: its posts wait
	}
	if err != nil {
		return err
	}
	own, owners, err := s.feedOwn(ctx, a)
	if err != nil {
		return err
	}
	x := leadfeed.Extract(c.Text, own)
	if !owners[x.OwnerID] {
		x.OwnerID = "" // a disabled teammate, or one kept to other brands
	}
	r, ok, err := s.feedRead(ctx, a.WorkspaceID, it.ID, c.Text, x)
	if err != nil {
		// The error can quote the post: only its code leaves this job.
		ev.Name, ev.Outcome, ev.ErrorCode = "sales_feed.item_failed", track.Error, "ai_failed"
		ev.Props["reason"] = feedAIReason(err)
		track.Emit(ctx, ev)
		if job.Attempt < job.MaxAttempts {
			if _, e := s.pool.Exec(ctx, `UPDATE sales_feed_items SET status=CASE WHEN record_id IS NULL AND status IN ('pending','failed') THEN 'failed' ELSE status END,
				error_code='ai_failed',updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, it.ID); e != nil {
				return e
			}
			// The job queue logs its error and keeps it in jobs.last_error.
			return errFeedAIFailed
		}
		// The last attempt carries on with the rules alone.
		r, ok = feedReading{}, false
	}
	email, domain, outcome := feedDecide(x, r, ok)

	refreshed := false
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		it, cur, found, err := s.lockFeedItem(ctx, tx, a, p.ID, it.Hash)
		if err != nil || !found {
			return err
		}
		// The reading replaces the last one; the post and its replies are
		// as stored now.
		// Without the AI the last reading stays.
		cur.Fields, cur.CRMLink, cur.Candidates = x.Fields, x.CRMLink, x.Emails
		var conf *float64
		if ok {
			cur.Reading, conf = &r, &r.SpamConfidence
		}
		data := map[string]any{"kind": it.Kind, "feed_channel_id": it.ChannelID}
		set := func(status string) error {
			_, err := tx.Exec(ctx, `UPDATE sales_feed_items SET status=COALESCE(NULLIF($4,''),status),content_sealed=$5,spam_confidence=COALESCE($6,spam_confidence),
				error_code='',updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, it.ID, status, s.salesSeal(it.ID, cur), conf)
			return err
		}
		switch {
		case it.RecordID != nil || it.ResolvedBy != nil || it.Status == "ignored":
			// Read before, or resolved by a person: only the reading is new,
			// and what it made or they decided stays as it is.
			refreshed = true
			return set("")
		case outcome == "spam":
			if err := set("spam"); err != nil {
				return err
			}
			return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "sales_feed.item_spam", "sales_feed_item", it.ID, data, data)
		case outcome == "needs_look":
			if err := set("needs_look"); err != nil {
				return err
			}
			data["made"] = "none"
			return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "sales_feed.item_needs_look", "sales_feed_item", it.ID, data, data)
		}
		if conf != nil {
			if _, err := tx.Exec(ctx, `UPDATE sales_feed_items SET spam_confidence=$4 WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, it.ID, *conf); err != nil {
				return err
			}
		}
		return s.feedApply(ctx, tx, a, it, ch, cur, email, domain, x.OwnerID)
	})
	if err != nil {
		return err
	}
	if refreshed {
		ev.Name, ev.Outcome, ev.ErrorCode = "sales_feed.item_refreshed", track.Success, ""
		track.Emit(ctx, ev)
	}
	return nil
}
