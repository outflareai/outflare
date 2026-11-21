package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/leadfeed"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The stored Slack posts as the Sales desk sees them: the list of posts
// waiting for a teammate (needs a look, spam, failed), the decision a
// teammate makes on one, and the item view the conversation page shows.
//
// Who sees a post: one on a Sales conversation follows salesVisible for that
// conversation; one on none is for anyone on the sales desk (salesDesk).

const (
	feedItemTextMax  = 20000 // characters of a post in a response
	feedReplyTextMax = 4000  // characters of a reply in a response
)

type feedFieldView struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// salesFeedItem is a stored post in API responses (SalesFeedItem).
type salesFeedItem struct {
	ID             string          `json:"id"`
	Kind           string          `json:"kind"`
	Status         string          `json:"status"`
	ChannelID      string          `json:"channel_id"`
	ChannelName    string          `json:"channel_name"`
	OccurredAt     time.Time       `json:"occurred_at"`
	EditedAt       *time.Time      `json:"edited_at"`
	DeletedInSlack bool            `json:"deleted_in_slack"`
	Link           string          `json:"link"`
	Source         string          `json:"source"`
	Text           string          `json:"text"`
	Fields         []feedFieldView `json:"fields"`
	// KeyFields are the fields its channel shows first, in order.
	KeyFields      []feedFieldView `json:"key_fields"`
	Summary        string          `json:"summary"`
	Message        string          `json:"message"`
	CRMLink        string          `json:"crm_link"`
	Candidates     []string        `json:"candidates"`
	Replies        []feedReply     `json:"replies"`
	RecordID       *string         `json:"record_id"`
	LeadID         *string         `json:"lead_id"`
	SpamConfidence *float64        `json:"spam_confidence"`
}

const feedItemSelect = `SELECT i.id::text,i.kind,i.status,i.feed_channel_id::text,c.channel_name,i.occurred_at,i.edited_at,i.deleted_in_slack,
	i.record_id::text,i.lead_id::text,i.spam_confidence::float8,i.content_sealed,c.preview_fields
	FROM sales_feed_items i JOIN sales_feed_channels c ON c.id=i.feed_channel_id AND c.workspace_id=i.workspace_id
	WHERE i.workspace_id=$1 AND i.brand_id=$2`

// The item's brand is the authority on who sees a post: a post whose person
// moved to another brand went with them, while its channel stays the old
// brand's. So the channel is joined by workspace only, for its name, and the
// items are filtered by the caller's brand.

func (s *Server) scanFeedItem(row pgx.Row) (salesFeedItem, error) {
	var v salesFeedItem
	var sealed []byte
	var layout []string
	if err := row.Scan(&v.ID, &v.Kind, &v.Status, &v.ChannelID, &v.ChannelName, &v.OccurredAt, &v.EditedAt, &v.DeletedInSlack,
		&v.RecordID, &v.LeadID, &v.SpamConfidence, &sealed, &layout); err != nil {
		return v, err
	}
	var c feedContent
	if err := s.salesOpen(sealed, v.ID, &c); err != nil {
		return v, fmt.Errorf("%w: %w", errFeedUnreadable, err)
	}
	v.Link, v.Text, v.CRMLink, v.Source = c.Link, feedCut(c.Text, feedItemTextMax), c.CRMLink, c.Source
	if c.Reading != nil {
		v.Summary, v.Message = c.Reading.Summary, c.Reading.Message
	}
	v.Fields = make([]feedFieldView, 0, len(c.Fields))
	for _, f := range c.Fields {
		v.Fields = append(v.Fields, feedFieldView{Label: f.Label, Value: f.Value})
	}
	v.KeyFields = feedKeyFields(c, layout, feedLayoutMax)
	v.Candidates = append([]string{}, c.Candidates...)
	v.Replies = make([]feedReply, 0, len(c.Replies))
	for _, r := range c.Replies {
		v.Replies = append(v.Replies, feedReply{Author: r.Author, At: r.At, Text: feedCut(r.Text, feedReplyTextMax)})
	}
	return v, nil
}

// errFeedUnreadable is a stored post whose sealed content can't be opened.
var errFeedUnreadable = errors.New("the stored post can't be opened")

// feedItems are the posts sql finds, in a's brand, and the ids of every row
// it found in order, also those left out. A post that can't be opened is
// left out, logged and recorded (sales_feed.item_open_failed), so one bad
// post never fails the page it is on.
func (s *Server) feedItems(ctx context.Context, a *access, sql string, args ...any) ([]salesFeedItem, []string, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	list := []salesFeedItem{}
	var ids []string
	for rows.Next() {
		v, err := s.scanFeedItem(rows)
		if errors.Is(err, errFeedUnreadable) {
			ids = append(ids, v.ID)
			track.Fail(ctx, s.log.With("request_id", RequestID(ctx), "item_id", v.ID), track.Event{Name: "sales_feed.item_open_failed", WorkspaceID: a.WorkspaceID, Outcome: track.Error,
				Props: map[string]any{"item_id": v.ID, "feed_channel_id": v.ChannelID, "brand_id": a.Brand}}, "internal", err)
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		ids = append(ids, v.ID)
		list = append(list, v)
	}
	return list, ids, rows.Err()
}

// feedRelatedMax is how many of a person's and their company's posts on no
// conversation the conversation page shows, newest first.
const feedRelatedMax = 50

// leadFeedItems are the posts on a Sales conversation and, so the page shows
// the whole context, the newest 50 posts on no conversation about its person
// or their company (Signal and Context posts, a post that named only the
// company), leaving out spam, posts set aside and posts deleted in Slack;
// oldest first. lead_id tells the two apart. The caller has checked the
// conversation is theirs to see, and a post on no conversation is for anyone
// on the sales desk, which they are.
func (s *Server) leadFeedItems(ctx context.Context, a *access, l salesLead) ([]salesFeedItem, error) {
	own, _, err := s.feedItems(ctx, a, feedItemSelect+` AND i.lead_id=$3 ORDER BY i.occurred_at,i.id`, a.WorkspaceID, a.Brand, l.ID)
	if err != nil {
		return nil, err
	}
	about := []string{l.RecordID}
	company, err := customers.CompanyOf(ctx, s.pool, a.WorkspaceID, l.RecordID, nil)
	if err != nil {
		return nil, err
	}
	if company != "" {
		about = append(about, company)
	}
	related, _, err := s.feedItems(ctx, a, feedItemSelect+` AND i.lead_id IS NULL AND i.record_id=ANY($3::uuid[]) AND i.status NOT IN ('spam','ignored') AND NOT i.deleted_in_slack
		ORDER BY i.occurred_at DESC,i.id DESC LIMIT $4`, a.WorkspaceID, a.Brand, about, feedRelatedMax)
	if err != nil {
		return nil, err
	}
	all := append(own, related...)
	slices.SortStableFunc(all, func(x, y salesFeedItem) int {
		if c := x.OccurredAt.Compare(y.OccurredAt); c != 0 {
			return c
		}
		return strings.Compare(x.ID, y.ID)
	})
	return all, nil
}

// salesDesk says whether the caller works the sales desk: a Sales role, or a
// manager.
func salesDesk(a *access, m salesMember) bool {
	return m.Role != "none" || salesManager(a, m)
}

// feedDeskOnly answers 403 to a caller off the sales desk.
func (s *Server) feedDeskOnly(w http.ResponseWriter, r *http.Request) (salesMember, bool) {
	m, ok := s.salesPerson(w, r)
	if !ok {
		return m, false
	}
	if !salesDesk(accessFrom(r.Context()), m) {
		writeError(w, r, http.StatusForbidden, "forbidden", "Sales channels are for the Sales team: ask a manager for a Sales role")
		return m, false
	}
	return m, true
}

func (s *Server) listSalesFeedItems(w http.ResponseWriter, r *http.Request) {
	m, ok := s.feedDeskOnly(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	q := r.URL.Query()
	limit, err := limitParam(r, 50, 100)
	if s.allocError(w, r, err) {
		return
	}
	status := q.Get("status")
	if !slices.Contains([]string{"needs_look", "spam", "failed"}, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is needs_look, spam or failed")
		return
	}
	args := []any{a.WorkspaceID, a.Brand, status}
	where := ` AND i.status=$3`
	if !salesManager(a, m) {
		// salesVisible, for the conversation a post is on: a conversation
		// the caller could not open is not listed.
		args = append(args, a.UserID)
		where += fmt.Sprintf(` AND (i.lead_id IS NULL OR EXISTS (SELECT 1 FROM sales_leads l JOIN records p ON p.id=l.record_id AND p.workspace_id=l.workspace_id AND p.brand_id=l.brand_id
			WHERE l.workspace_id=$1 AND l.brand_id=$2 AND l.id=i.lead_id AND p.deleted_at IS NULL AND p.merged_into IS NULL
				AND (l.state='new' OR $%d::uuid IN (l.owner_id,l.sdr_id,l.ae_id))))`, len(args))
	}
	if ch := q.Get("feed_channel"); ch != "" {
		if !validUUID(ch) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "feed_channel must be a Sales channel id")
			return
		}
		args = append(args, ch)
		where += fmt.Sprintf(` AND i.feed_channel_id=$%d`, len(args))
	}
	if cursor := q.Get("cursor"); cursor != "" {
		if !validUUID(cursor) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid cursor")
			return
		}
		args = append(args, cursor)
		where += fmt.Sprintf(` AND (i.occurred_at,i.id)<(SELECT occurred_at,id FROM sales_feed_items WHERE workspace_id=$1 AND brand_id=$2 AND id=$%d)`, len(args))
	}
	args = append(args, limit+1)
	list, ids, err := s.feedItems(r.Context(), a, feedItemSelect+where+fmt.Sprintf(` ORDER BY i.occurred_at DESC,i.id DESC LIMIT $%d`, len(args)), args...)
	if s.allocError(w, r, err) {
		return
	}
	// The page is the first limit rows found, less any that can't be
	// opened, and the next one starts after the last of them.
	var next *string
	if len(ids) > limit {
		last := ids[limit-1]
		next = &last
		if i := slices.IndexFunc(list, func(v salesFeedItem) bool { return v.ID == ids[limit] }); i >= 0 {
			list = list[:i]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": next})
}

// feedLoadChannel is a post's channel, turned on or off, removed or not: a
// teammate may decide about a post of a channel that is off or removed. It
// is found in the workspace whatever its brand, since a post whose person
// moved to another brand keeps the old brand's channel; the caller has
// checked the post is in its brand.
func feedLoadChannel(ctx context.Context, tx pgx.Tx, a *access, id string) (feedChannel, error) {
	return scanFeedChannel(tx.QueryRow(ctx, `SELECT `+feedChannelColumns+`
		FROM sales_feed_channels WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, id))
}

func (s *Server) actSalesFeedItem(w http.ResponseWriter, r *http.Request) {
	m, ok := s.feedDeskOnly(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	ctx := r.Context()
	var in struct {
		Action string `json:"action"`
		Email  string `json:"email"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !slices.Contains([]string{"assign_person", "not_spam", "ignore"}, in.Action) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "action is assign_person, not_spam or ignore")
		return
	}
	id := r.PathValue("id")
	if !validUUID(id) {
		s.allocError(w, r, errNotFound)
		return
	}
	own, owners, err := s.feedOwn(ctx, a)
	if s.allocError(w, r, err) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if in.Action == "assign_person" {
		// Extract keeps only outside addresses: a teammate's, or one on the
		// brand's own domains, is never the person who wrote in.
		x := leadfeed.Extract(email, own)
		if _, valid := validEmail(email); !valid || len(x.Emails) != 1 || x.Emails[0] != email {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "email must be the address of the person who wrote in: not a teammate's, and not on one of the brand's own domains")
			return
		}
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		it, c, found, err := s.lockFeedItem(ctx, tx, a, id, "")
		if err != nil {
			return err
		}
		if !found {
			return errNotFound
		}
		if it.LeadID != nil {
			l, err := s.salesLoad(ctx, tx, a, *it.LeadID, false)
			if err != nil {
				return err // errNotFound when its person is gone
			}
			if !salesVisible(l, a, m) {
				return errNotFound
			}
		}
		x := leadfeed.Extract(c.Text, own)
		if !owners[x.OwnerID] {
			x.OwnerID = ""
		}
		switch in.Action {
		case "assign_person":
			// A post with no person yet, or one only a company was found for.
			if it.RecordID != nil && it.Status != "needs_look" {
				return recordConflict{"conflict", "This post already has a person."}
			}
			ch, err := feedLoadChannel(ctx, tx, a, it.ChannelID)
			if err != nil {
				return err
			}
			_, host, _ := strings.Cut(email, "@")
			if err := s.feedApply(ctx, tx, a, it, ch, c, email, feedCompanyDomain(host), x.OwnerID); err != nil {
				return err
			}
			var status string
			if err := tx.QueryRow(ctx, `SELECT status FROM sales_feed_items WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, it.ID).Scan(&status); err != nil {
				return err
			}
			if status != "read" {
				// The person is in Recently deleted, merged, or not the
				// brand's to change: nothing is kept.
				return recordConflict{"conflict", "That person can't be changed here. Check them in People, then try again."}
			}
		case "not_spam":
			if it.Status != "spam" {
				return recordConflict{"conflict", "This post is not marked as spam."}
			}
			// The decision without the spam step, from the stored reading:
			// the AI is not asked again.
			var reading feedReading
			ok := c.Reading != nil
			if ok {
				reading = *c.Reading
				reading.Spam = false
			}
			email, domain, outcome := feedDecide(x, reading, ok)
			if outcome == "needs_look" {
				if _, err := tx.Exec(ctx, `UPDATE sales_feed_items SET status='needs_look',updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, it.ID); err != nil {
					return err
				}
				break
			}
			ch, err := feedLoadChannel(ctx, tx, a, it.ChannelID)
			if err != nil {
				return err
			}
			if err := s.feedApply(ctx, tx, a, it, ch, c, email, domain, x.OwnerID); err != nil {
				return err
			}
		case "ignore":
			if !slices.Contains([]string{"needs_look", "spam", "failed"}, it.Status) {
				return recordConflict{"conflict", "Only a post that needs a look, is spam or failed can be set aside."}
			}
			if _, err := tx.Exec(ctx, `UPDATE sales_feed_items SET status='ignored',updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, it.ID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE sales_feed_items SET resolved_by=$4,updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, it.ID, a.UserID); err != nil {
			return err
		}
		data := map[string]any{"action": in.Action, "kind": it.Kind, "feed_channel_id": it.ChannelID}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "sales_feed.item_resolved", "sales_feed_item", it.ID, data, data)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = errNotFound
	}
	if s.recordError(w, r, err) {
		return
	}
	v, err := s.scanFeedItem(s.pool.QueryRow(ctx, feedItemSelect+` AND i.id=$3`, a.WorkspaceID, a.Brand, id))
	if s.allocError(w, r, err) {
		return
	}
	if v.LeadID != nil {
		// The post may have joined a conversation the caller can't open
		// (another rep's): it is not named to them.
		l, err := s.salesLoad(ctx, s.pool, a, *v.LeadID, false)
		if err != nil && !errors.Is(err, errNotFound) {
			s.internal(w, r, err)
			return
		}
		if err != nil || !salesVisible(l, a, m) {
			v.LeadID = nil
		}
	}
	writeJSON(w, http.StatusOK, v)
}
