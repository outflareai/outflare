package api

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/leadfeed"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/routing"
	"github.com/outflareai/outflare/engine/internal/signals"
	"github.com/outflareai/outflare/engine/internal/track"
)

// What a read post makes: its person and their company as customer records,
// and by the channel's kind a Sales conversation (new_person, handover), a
// signal (signal) or nothing more (context). Every change to a record is in
// its own history, written in the same transaction as the item.

// feedVia is the tool records made from a post came from (acquired_via).
const feedVia = "slack_feeds"

// feedSignalTitleMax and feedSignalDetailMax bound what a signal quotes of
// a post, in characters.
const (
	feedSignalTitleMax  = 120
	feedSignalDetailMax = 2000
)

// feedSource is where a Slack conversation came from, sealed in
// sales_leads.source_sealed.
type feedSource struct {
	Channel string `json:"channel"`
	TS      string `json:"ts"`
	Link    string `json:"link"`
}

// feedApply does the writes for one item whose person (email) or company (domain) is known.
// email "" with domain set attaches the item to the company and leaves it needs_look.
func (s *Server) feedApply(ctx context.Context, tx pgx.Tx, a *access, item feedItem, ch feedChannel, c feedContent, email, domain, ownerID string) error {
	// Posts about one company, or one person, take turns, so two at once
	// make one record and one conversation. Domain first, always.
	for _, key := range []string{"domain:" + domain, "email:" + email} {
		if strings.HasSuffix(key, ":") {
			continue
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "sales-feed:"+a.Brand+":"+key); err != nil {
			return err
		}
	}
	var reading feedReading
	if c.Reading != nil {
		reading = *c.Reading
	}
	company := ""
	if domain != "" {
		got, err := customers.EnsureCompanies(ctx, tx, a.WorkspaceID, &a.Brand, feedVia, []customers.Company{{Domain: domain, Name: reading.Company}})
		if err != nil {
			return err
		}
		company = got[domain]
		if site := feedWebsite(c, domain); company != "" && site != "" {
			// Never over a value a person or a better source set.
			if _, err := customers.SetFields(ctx, tx, a.WorkspaceID, company, "enrichment",
				[]customers.Field{{Key: "website", Label: "Website", Type: "url"}}, map[string]any{"website": site}); err != nil {
				return err
			}
		}
	}
	status, target, made := "needs_look", company, "none"
	var lead *string
	if email != "" {
		person, err := s.feedPerson(ctx, tx, a, email, reading.Name, company, ownerID)
		switch {
		case errors.Is(err, errFeedPersonLocked):
			// In Recently deleted, merged meanwhile, or not the brand's to
			// change: a teammate decides, and the job is done.
			target = ""
		case err != nil:
			return err
		default:
			status, target = "read", person
			if made, lead, err = s.feedMakes(ctx, tx, a, item, ch, c, reading, person); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE sales_feed_items SET record_id=NULLIF($4,'')::uuid,lead_id=$5,status=$6,error_code='',content_sealed=$7,updated_at=now()
		WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, item.ID, target, lead, status, s.salesSeal(item.ID, c)); err != nil {
		return err
	}
	action := "sales_feed.item_read"
	if status == "needs_look" {
		action = "sales_feed.item_needs_look"
	}
	data := map[string]any{"kind": item.Kind, "feed_channel_id": item.ChannelID, "made": made}
	return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, action, "sales_feed_item", item.ID, data, data)
}

// feedMakes makes what the channel's kind asks for about person: a Sales
// conversation (new_person, handover), a signal, or nothing (context). It
// says what it made and the conversation.
func (s *Server) feedMakes(ctx context.Context, tx pgx.Tx, a *access, item feedItem, ch feedChannel, c feedContent, reading feedReading, person string) (string, *string, error) {
	if item.Kind != "context" {
		if err := feedPersonSource(ctx, tx, a, ch, person); err != nil {
			return "", nil, err
		}
	}
	switch item.Kind {
	case "new_person", "handover":
		id, joined, err := s.feedConversation(ctx, tx, a, item, ch, c, person)
		if err != nil {
			return "", nil, err
		}
		if joined {
			return "joined", &id, nil
		}
		return "conversation", &id, nil
	case "signal":
		raised, err := s.feedSignal(ctx, tx, a, item, ch, c, reading, person)
		if err != nil || !raised {
			return "none", nil, err
		}
		return "signal", nil, nil
	}
	return "none", nil, nil
}

// feedPersonSource says where a person a Sales channel brought in came
// from: Source Inbound, and the channel as their Sub-source (named after
// it; the team can rename it under the brand's sub-sources). Only for a
// person this connection added whose Source is still Outflare's own guess:
// a Source someone set, or one from before, is never changed.
func feedPersonSource(ctx context.Context, tx pgx.Tx, a *access, ch feedChannel, person string) error {
	var guessed bool
	if err := tx.QueryRow(ctx, `SELECT acquired_via=$3 AND attributes->>'source'='signup' AND attribute_sources->>'source'='first_party' AND NOT attributes ? 'sub_source'
			AND NOT EXISTS (SELECT 1 FROM field_definitions f WHERE f.workspace_id=r.workspace_id AND f.record_type='person' AND f.key IN ('source','sub_source') AND f.brand_id IS NOT DISTINCT FROM r.brand_id)
		FROM records r WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, person, feedVia).Scan(&guessed); err != nil || !guessed {
		return err
	}
	set := map[string]any{"source": "inbound"}
	if label := cleanSubSourceLabel(strings.NewReplacer("-", " ", "_", " ").Replace(ch.ChannelName)); label != "" && len(label) <= 120 {
		sub, _, err := ensureSubSource(ctx, tx, a, a.Brand, "inbound", label)
		if err != nil {
			return err
		}
		set["sub_source"] = sub.Key
	}
	raw, _ := json.Marshal(set)
	if _, err := tx.Exec(ctx, `UPDATE records SET attributes=attributes||$3::jsonb,
			attribute_sources=attribute_sources||CASE WHEN $3::jsonb ? 'sub_source' THEN '{"sub_source":"first_party"}'::jsonb ELSE '{}'::jsonb END,
			version=version+1,updated_at=now() WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, person, raw); err != nil {
		return err
	}
	if err := attributeChanged(ctx, tx, a, person, "source", "signup", "inbound", true, "first_party"); err != nil {
		return err
	}
	if sub, ok := set["sub_source"]; ok {
		return attributeChanged(ctx, tx, a, person, "sub_source", nil, sub, false, "first_party")
	}
	return nil
}

// feedWebsite is the website the post gives for the company with domain:
// the reading's, else a link on that domain; "" when it gives none.
func feedWebsite(c feedContent, domain string) string {
	var sites []string
	if c.Reading != nil && c.Reading.Website != "" {
		sites = append(sites, c.Reading.Website)
	}
	sites = append(sites, leadfeed.Extract(c.Text, leadfeed.Own{}).Links...)
	for _, site := range sites {
		if customers.CompanyDomain(site) == domain {
			return site
		}
	}
	return ""
}

// errFeedPersonLocked is feedPerson finding a person it may not write to.
var errFeedPersonLocked = errors.New("sales feed: the person can't be changed")

// feedPerson finds or makes the person with email in the brand, puts them
// on company, and gives them an Owner when they have none: the owner of
// their open Sales conversation when it has one (so the two never
// disagree), else the teammate the post named, else whoever the brand's
// routing rules pick. It returns their record, locked for the rest of the
// transaction.
func (s *Server) feedPerson(ctx context.Context, tx pgx.Tx, a *access, email, name, company, ownerID string) (string, error) {
	people, err := customers.EnsurePeopleIn(ctx, tx, a.WorkspaceID, a.Brand, "first_party", feedVia, []customers.Person{{Email: email, Name: name}})
	if err != nil {
		return "", err
	}
	person := people[email]
	if person == "" {
		return "", errors.New("sales feed: no person for the address")
	}
	// Their open conversations first, then the record: the order assigning
	// and taking a conversation lock them in, so the two never wait on each
	// other. (Two posts about the same person already take turns on the
	// advisory lock in feedApply.) The newest one's owner is kept.
	rows, err := tx.Query(ctx, `SELECT COALESCE(owner_id::text,'') FROM sales_leads WHERE workspace_id=$1 AND brand_id=$2 AND record_id=$3 AND state<>'archived'
		ORDER BY created_at DESC, id FOR UPDATE`, a.WorkspaceID, a.Brand, person)
	if err != nil {
		return "", err
	}
	owners, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", err
	}
	convOwner := ""
	for _, o := range owners {
		if o != "" {
			convOwner = o
			break
		}
	}
	// The person's lock keeps two posts about them from reading or writing
	// their conversation at once.
	rec, err := lockRecord(ctx, tx, a, person)
	var conflict recordConflict
	if errors.Is(err, errNotFound) || errors.Is(err, errSharedRecord) || errors.As(err, &conflict) {
		return "", errFeedPersonLocked
	}
	if err != nil {
		return "", err
	}
	if company != "" {
		if err := feedLinkCompany(ctx, tx, a, &rec, company); err != nil {
			return "", err
		}
	}
	owner, _ := rec.Attributes["owner"].(string)
	switch {
	case owner != "":
	case convOwner != "":
		// Assigned before they had an Owner (a conversation from before
		// Sales channels): its owner becomes theirs.
		err = salesSetOwner(ctx, tx, a, person, convOwner)
	case ownerID != "":
		err = salesSetOwner(ctx, tx, a, person, ownerID)
	default:
		// New records are routed about once a minute in the background;
		// the conversation needs its owner now.
		var res routing.Result
		if res, err = routing.Route(ctx, tx, a.WorkspaceID, person, nil, false, false); err == nil && res.Outcome == "assigned" {
			err = record(ctx, tx, &a.principal, a.WorkspaceID, "record.routed", "record", person, map[string]any{"record_id": person, "rule_id": res.RuleID, "owner_id": res.OwnerID})
		}
	}
	return person, err
}

// feedLinkCompany adds company to the person's companies, unless they are
// on it already.
func feedLinkCompany(ctx context.Context, tx pgx.Tx, a *access, rec *recordView, company string) error {
	rows, err := tx.Query(ctx, `SELECT DISTINCT to_id::text FROM record_relationships
		WHERE workspace_id = $1 AND from_id = $2 AND field_key = 'companies' AND valid_to IS NULL`, a.WorkspaceID, rec.ID)
	if err != nil {
		return err
	}
	companies, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if list, ok := rec.Attributes["companies"].([]any); ok {
		for _, x := range list {
			if s, ok := x.(string); ok && !slices.Contains(companies, s) {
				companies = append(companies, s)
			}
		}
	}
	if slices.Contains(companies, company) {
		return nil
	}
	cat, err := loadCatalog(ctx, tx, a.WorkspaceID, a.Brand, false)
	if err != nil {
		return err
	}
	t := cat.recordType("person")
	if t == nil || t.field("companies") == nil {
		return nil
	}
	if rec.Attributes == nil {
		rec.Attributes = map[string]any{}
	}
	if rec.AttributeSources == nil {
		rec.AttributeSources = map[string]string{}
	}
	value, _ := json.Marshal(append(companies, company))
	changed, err := applyAttributes(ctx, tx, cat, t, a, rec, map[string]json.RawMessage{"companies": value}, "first_party")
	if err != nil || !changed {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE records SET attributes = $2, attribute_sources = $3, version = version + 1, updated_at = now() WHERE id = $1`,
		rec.ID, rec.Attributes, rec.AttributeSources); err != nil {
		return err
	}
	// The relationship keeps the link however companies is set.
	if _, err := tx.Exec(ctx, `INSERT INTO record_relationships (workspace_id, from_id, to_id, kind, field_key, created_by)
		SELECT $1, $2, $3, 'companies', 'companies', NULL
		WHERE NOT EXISTS (SELECT 1 FROM record_relationships WHERE workspace_id = $1 AND from_id = $2 AND to_id = $3 AND kind = 'companies' AND valid_to IS NULL)`,
		a.WorkspaceID, rec.ID, company); err != nil {
		return err
	}
	return record(ctx, tx, &a.principal, a.WorkspaceID, "record.updated", "record", rec.ID, map[string]any{"type": rec.Type, "via": feedVia})
}

// feedConversation puts the post on the person's open Sales conversation
// (joined true), or starts one in the New queue, owned by the person's
// Owner when they are an SDR or account manager on the brand's sales desk.
func (s *Server) feedConversation(ctx context.Context, tx pgx.Tx, a *access, item feedItem, ch feedChannel, c feedContent, person string) (string, bool, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM sales_leads WHERE workspace_id=$1 AND brand_id=$2 AND record_id=$3 AND state<>'archived'
		ORDER BY created_at DESC, id LIMIT 1 FOR UPDATE`, a.WorkspaceID, a.Brand, person).Scan(&id)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE sales_leads SET version=version+1,updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, id)
		return id, true, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	var owner string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(attributes->>'owner','') FROM records WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, person).Scan(&owner); err != nil {
		return "", false, err
	}
	var ownerID, sdr, ae *string
	if validUUID(owner) {
		m, err := salesMemberAt(ctx, tx, a, owner)
		if err != nil && !errors.Is(err, errNotFound) {
			return "", false, err
		}
		switch m.Role {
		case "sdr":
			ownerID, sdr = &m.UserID, &m.UserID
		case "ae":
			ownerID, ae = &m.UserID, &m.UserID
		}
	}
	// The post is the conversation's source (channel and timestamp). A post
	// can start a second conversation, when its first was archived and the
	// post is read again for no one: that one's source carries the item too,
	// so the two never collide. One still open for this person is joined.
	sourceID := ch.ChannelID + ":" + item.SlackTS
	for _, candidate := range []string{sourceID, sourceID + ":" + item.ID, sourceID + ":" + newID()} {
		var taken, open bool
		var other string
		err := tx.QueryRow(ctx, `SELECT true,state<>'archived',id::text FROM sales_leads WHERE workspace_id=$1 AND brand_id=$2 AND source='slack' AND source_id=$3 AND record_id=$4 FOR UPDATE`,
			a.WorkspaceID, a.Brand, candidate, person).Scan(&taken, &open, &other)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", false, err
		}
		if open {
			_, err = tx.Exec(ctx, `UPDATE sales_leads SET version=version+1,updated_at=now() WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, other)
			return other, true, err
		}
		if !taken {
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sales_leads WHERE workspace_id=$1 AND brand_id=$2 AND source='slack' AND source_id=$3)`,
				a.WorkspaceID, a.Brand, candidate).Scan(&taken); err != nil {
				return "", false, err
			}
		}
		if !taken {
			sourceID = candidate
			break
		}
	}
	// integration_id stays empty: Sales reads it as the EmailBison
	// connection a conversation replies through.
	id = newID()
	if _, err := tx.Exec(ctx, `INSERT INTO sales_leads(id,workspace_id,brand_id,record_id,source,source_id,source_sealed,owner_id,sdr_id,ae_id,feed_kind,feed_channel_id,feed_source)
		VALUES($1,$2,$3,$4,'slack',$5,$6,$7,$8,$9,$10,$11,NULLIF($12,''))`, id, a.WorkspaceID, a.Brand, person, sourceID,
		s.salesSeal(id, feedSource{Channel: ch.ChannelID, TS: item.SlackTS, Link: c.Link}), ownerID, sdr, ae, item.Kind, item.ChannelID, c.Source); err != nil {
		return "", false, err
	}
	l, err := s.salesLoad(ctx, tx, a, id, false)
	if err != nil {
		return "", false, err
	}
	return id, false, s.salesEvent(ctx, tx, a, l, "feed", "lead_arrived", nil, map[string]string{"source": "slack"})
}

// feedSignal raises the channel's signal type on the person's company, or
// on the person when they have none. It reports whether one was raised: an
// open signal of the type already, or the record's limit, is not an error.
func (s *Server) feedSignal(ctx context.Context, tx pgx.Tx, a *access, item feedItem, ch feedChannel, c feedContent, reading feedReading, person string) (bool, error) {
	target, err := customers.CompanyOf(ctx, tx, a.WorkspaceID, person, nil)
	if err != nil {
		return false, err
	}
	if target == "" {
		target = person
	}
	if err := signals.EnsureTypes(ctx, tx, a.WorkspaceID, a.Brand); err != nil {
		return false, err
	}
	var known bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM signal_types WHERE workspace_id = $1 AND brand_id = $2 AND key = $3 AND archived_at IS NULL)`, a.WorkspaceID, a.Brand, ch.SignalType).Scan(&known); err != nil {
		return false, err
	}
	if !known {
		// The brand has no such type, or archived it: the post is still
		// read, and the event says why nothing was raised.
		track.Fail(ctx, s.log, track.Event{Name: "sales_feed.signal_failed", WorkspaceID: a.WorkspaceID, ActorType: "system", Surface: "job", Outcome: track.Failure,
			Props: map[string]any{"brand_id": a.Brand, "feed_channel_id": item.ChannelID, "item_id": item.ID}}, "signal_type_missing", errFeedSignalType)
		return false, nil
	}
	text := strings.TrimSpace(c.Text)
	title := reading.Summary
	if title == "" {
		title = feedCut(text, feedSignalTitleMax)
	}
	_, outcome, err := signals.Raise(ctx, tx, signals.Signal{WorkspaceID: a.WorkspaceID, BrandID: a.Brand, RecordID: target, Type: ch.SignalType,
		Title: title, Detail: feedCut(text, feedSignalDetailMax), Source: "feed", RequestID: RequestID(ctx),
		Data: map[string]any{"link": c.Link, "feed_channel_id": item.ChannelID, "item_id": item.ID}})
	if err != nil {
		return false, err
	}
	metrics.SignalRaised("feed", outcome)
	return outcome == signals.Raised, nil
}

// errFeedSignalType is a signal channel's type the brand doesn't have.
var errFeedSignalType = errors.New("sales feed: the channel's signal type is missing or archived")

// feedCut is s cut to at most n characters.
func feedCut(s string, n int) string {
	if rs := []rune(s); len(rs) > n {
		return string(rs[:n])
	}
	return s
}
