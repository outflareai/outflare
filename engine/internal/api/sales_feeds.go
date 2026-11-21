package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/leadfeed"
	"github.com/outflareai/outflare/engine/internal/signals"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The settings behind the Sales channels: which Slack channels a brand reads
// and what each one carries (sales_feed_channels). The reading itself is
// sales_feed_poll.go and sales_feed_process.go.

const (
	feedMaxChannels  = 50
	feedPreviewPosts = 5
	feedDefaultBack  = 3
	feedDefaultType  = "upsell"
)

var feedKinds = []string{"new_person", "handover", "signal", "context"}

type feedCounts struct {
	Read    int `json:"read"`
	NeedsLk int `json:"needs_look"`
	Spam    int `json:"spam"`
	Failed  int `json:"failed"`
}

type feedChannelOut struct {
	ID           string `json:"id"`
	ChannelID    string `json:"channel_id"`
	ChannelName  string `json:"channel_name"`
	Kind         string `json:"kind"`
	SignalType   string `json:"signal_type"`
	ReadBackDays int    `json:"read_back_days"`
	Enabled      bool   `json:"enabled"`
	ErrorCode    string `json:"error_code"`
	// ConnectionRemoved is true when its Slack connection was deleted: the
	// channel and its posts wait, unread, until it is saved with another.
	ConnectionRemoved bool       `json:"connection_removed"`
	LastReadAt        *time.Time `json:"last_read_at"`
	Counts7d          feedCounts `json:"counts_7d"`
	// PreviewFields are the labels of the fields its posts show first, in
	// order; empty when Outflare picks.
	PreviewFields []string `json:"preview_fields"`
	// Alert says whether the conversations it starts are posted as Sales
	// alerts, when the brand has those on.
	Alert bool `json:"alert"`
}

// feedSourceQuality is how reps marked the conversations one source started
// in the last 30 days.
type feedSourceQuality struct {
	// Source is the posting app's name; "" with Reply for a conversation
	// that started from an email reply, "" without for a person's post.
	Source  string `json:"source"`
	Reply   bool   `json:"reply"`
	Total   int    `json:"total"`
	Good    int    `json:"good"`
	PoorFit int    `json:"poor_fit"`
	Junk    int    `json:"junk"`
}

type feedSettingsOut struct {
	IntegrationID *string          `json:"integration_id"`
	AI            string           `json:"ai"`
	Search        bool             `json:"search"`
	Channels      []feedChannelOut `json:"channels"`
	// Sources are the apps whose posts started the brand's conversations,
	// the most common first: the queue's source filter.
	Sources []string `json:"sources"`
	// SourceQuality counts the last 30 days' conversations by source and by
	// the mark reps gave them, the busiest source first.
	SourceQuality []feedSourceQuality `json:"source_quality"`
	// Alerts is where new conversations are posted in Slack, and which.
	Alerts salesAlertsOut `json:"alerts"`
}

// feedServing is the brand's Slack (sales channels) connections that serve
// it, brand-specific ones first.
func (s *Server) feedServing(ctx context.Context, a *access) ([]connectors.Integration, error) {
	list, err := s.integrations.List(ctx, s.pool, a.WorkspaceID, "slack_feeds")
	if err != nil {
		return nil, err
	}
	var out []connectors.Integration
	for _, i := range list {
		if i.Serves(a.Brand, list) {
			out = append(out, i)
		}
	}
	slices.SortStableFunc(out, func(x, y connectors.Integration) int {
		switch {
		case x.BrandID != nil && y.BrandID == nil:
			return -1
		case x.BrandID == nil && y.BrandID != nil:
			return 1
		}
		return 0
	})
	return out, nil
}

// feedSettings is what the settings page shows.
func (s *Server) feedSettings(ctx context.Context, q connectors.Querier, a *access) (feedSettingsOut, error) {
	out := feedSettingsOut{AI: "on", Channels: []feedChannelOut{}, Sources: []string{}, SourceQuality: []feedSourceQuality{}}
	// A removed channel is kept with its posts, and not listed.
	rows, err := q.Query(ctx, `SELECT id::text,COALESCE(integration_id::text,''),channel_id,channel_name,kind,signal_type,read_back_days,enabled,error_code,last_read_at,preview_fields,alert
		FROM sales_feed_channels WHERE workspace_id=$1 AND brand_id=$2 AND removed_at IS NULL ORDER BY created_at,channel_name,id`, a.WorkspaceID, a.Brand)
	if err != nil {
		return out, err
	}
	var integs []string
	for rows.Next() {
		var c feedChannelOut
		var integ string
		if err := rows.Scan(&c.ID, &integ, &c.ChannelID, &c.ChannelName, &c.Kind, &c.SignalType, &c.ReadBackDays, &c.Enabled, &c.ErrorCode, &c.LastReadAt, &c.PreviewFields, &c.Alert); err != nil {
			rows.Close()
			return out, err
		}
		c.ConnectionRemoved = integ == ""
		if c.PreviewFields == nil {
			c.PreviewFields = []string{}
		}
		out.Channels = append(out.Channels, c)
		if integ != "" {
			integs = append(integs, integ)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	counts, err := q.Query(ctx, `SELECT feed_channel_id::text,status,count(*) FROM sales_feed_items
		WHERE workspace_id=$1 AND brand_id=$2 AND occurred_at >= now()-interval '7 days' AND status IN ('read','needs_look','spam','failed')
		GROUP BY 1,2`, a.WorkspaceID, a.Brand)
	if err != nil {
		return out, err
	}
	for counts.Next() {
		var id, status string
		var n int
		if err := counts.Scan(&id, &status, &n); err != nil {
			counts.Close()
			return out, err
		}
		i := slices.IndexFunc(out.Channels, func(c feedChannelOut) bool { return c.ID == id })
		if i < 0 {
			continue
		}
		c := &out.Channels[i].Counts7d
		switch status {
		case "read":
			c.Read = n
		case "needs_look":
			c.NeedsLk = n
		case "spam":
			c.Spam = n
		case "failed":
			c.Failed = n
		}
	}
	counts.Close()
	if err := counts.Err(); err != nil {
		return out, err
	}
	sources, err := q.Query(ctx, `SELECT feed_source FROM sales_leads WHERE workspace_id=$1 AND brand_id=$2 AND feed_source IS NOT NULL
		GROUP BY 1 ORDER BY count(*) DESC,1 LIMIT 12`, a.WorkspaceID, a.Brand)
	if err != nil {
		return out, err
	}
	for sources.Next() {
		var name string
		if err := sources.Scan(&name); err != nil {
			sources.Close()
			return out, err
		}
		out.Sources = append(out.Sources, name)
	}
	sources.Close()
	if err := sources.Err(); err != nil {
		return out, err
	}
	if out.Alerts, _, _, err = salesAlertSettings(ctx, q, a); err != nil {
		return out, err
	}
	// Only conversations whose person is still there, as the queue lists them.
	quality, err := q.Query(ctx, `SELECT COALESCE(l.feed_source,''),l.feed_kind IS NULL,count(*),
			count(*) FILTER (WHERE l.quality='good'),count(*) FILTER (WHERE l.quality='poor_fit'),count(*) FILTER (WHERE l.quality='junk')
		FROM sales_leads l JOIN records r ON r.id=l.record_id AND r.workspace_id=l.workspace_id AND r.brand_id=l.brand_id
		WHERE l.workspace_id=$1 AND l.brand_id=$2 AND l.created_at >= now()-interval '30 days' AND r.deleted_at IS NULL AND r.merged_into IS NULL
		GROUP BY 1,2 ORDER BY 3 DESC,1,2 LIMIT 20`, a.WorkspaceID, a.Brand)
	if err != nil {
		return out, err
	}
	for quality.Next() {
		var v feedSourceQuality
		if err := quality.Scan(&v.Source, &v.Reply, &v.Total, &v.Good, &v.PoorFit, &v.Junk); err != nil {
			quality.Close()
			return out, err
		}
		out.SourceQuality = append(out.SourceQuality, v)
	}
	quality.Close()
	if err := quality.Err(); err != nil {
		return out, err
	}

	serving, err := s.feedServing(ctx, a)
	if err != nil {
		return out, err
	}
	var in *connectors.Integration
	for _, id := range integs {
		if i := slices.IndexFunc(serving, func(i connectors.Integration) bool { return i.ID == id }); i >= 0 {
			in = &serving[i]
			break
		}
	}
	if in == nil && len(serving) > 0 {
		in = &serving[0]
	}
	if in != nil {
		out.IntegrationID = &in.ID
		if o, err := s.integrations.Open(ctx, s.pool, a.WorkspaceID, in.ID); err == nil {
			out.Search = connectors.CanSearchMessages(o.Connector, o.Creds)
		}
	}

	if _, err := s.aiModel(ctx, a.WorkspaceID, taskLeadChannels); err != nil {
		var ae errAI
		if !errors.As(err, &ae) || (ae.Code() != "connector_needed" && ae.Code() != "ai_model_needed") {
			return out, err
		}
		out.AI = ae.Code()
	}
	return out, nil
}

func (s *Server) getSalesFeeds(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	out, err := s.feedSettings(r.Context(), s.pool, a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// feedConnection opens a Slack (sales channels) connection that serves the
// brand, or answers 400 (not one) or the connector's own failure.
func (s *Server) feedConnection(w http.ResponseWriter, r *http.Request, a *access, id string) (*connectors.Opened, connectors.FeedReader, bool) {
	const notOne = "integration_id is not a Slack (sales channels) connection that serves this brand"
	if !validUUID(id) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", notOne)
		return nil, nil, false
	}
	serving, err := s.feedServing(r.Context(), a)
	if err != nil {
		s.internal(w, r, err)
		return nil, nil, false
	}
	if !slices.ContainsFunc(serving, func(i connectors.Integration) bool { return i.ID == id }) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", notOne)
		return nil, nil, false
	}
	o, err := s.integrations.Open(r.Context(), s.pool, a.WorkspaceID, id)
	if errors.Is(err, connectors.ErrNotFound) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", notOne)
		return nil, nil, false
	}
	if err != nil {
		s.feedFailed(w, r, a, "open", err)
		return nil, nil, false
	}
	reader, ok := o.Connector.(connectors.FeedReader)
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", notOne)
		return nil, nil, false
	}
	return o, reader, true
}

// feedFailed answers a failed call to Slack: an event with the connector
// error's code, the full error in the log, and the answer without Slack's
// own words.
func (s *Server) feedFailed(w http.ResponseWriter, r *http.Request, a *access, step string, err error) {
	if isConnectorErr(err) {
		code := "connector_unavailable"
		switch connectors.KindOf(err) {
		case connectors.ErrAuth:
			code = "connector_auth"
		case connectors.ErrQuota:
			code = "connector_quota"
		case connectors.ErrInvalid:
			code = "connector_invalid"
		}
		track.Fail(r.Context(), s.log, track.Event{Name: "sales_feed.slack_failed", WorkspaceID: a.WorkspaceID, Outcome: track.Failure,
			Props: map[string]any{"brand_id": a.Brand, "step": step}}, code, err)
		err = feedErr(err)
	}
	s.connectorFailed(w, r, err)
}

func (s *Server) listSalesFeedChannels(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	o, reader, ok := s.feedConnection(w, r, a, r.URL.Query().Get("integration_id"))
	if !ok {
		return
	}
	list, err := reader.Channels(r.Context(), o.Client, o.Creds)
	_ = s.integrations.Checked(r.Context(), s.pool, a.WorkspaceID, o.ID, nil, err)
	if err != nil {
		s.feedFailed(w, r, a, "channels", err)
		return
	}
	if list == nil {
		list = []connectors.Channel{}
	}
	slices.SortStableFunc(list, func(x, y connectors.Channel) int {
		return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
	})
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

type feedChannelIn struct {
	ChannelID    string  `json:"channel_id"`
	Kind         string  `json:"kind"`
	SignalType   *string `json:"signal_type"`
	ReadBackDays *int    `json:"read_back_days"`
	Enabled      *bool   `json:"enabled"`
}

func (s *Server) putSalesFeeds(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		IntegrationID string          `json:"integration_id"`
		Channels      []feedChannelIn `json:"channels"`
	}
	if !decode(w, r, &in) {
		return
	}
	bad := func(msg string) { writeError(w, r, http.StatusBadRequest, "invalid_request", msg) }
	if len(in.Channels) > feedMaxChannels {
		bad("choose at most " + strconv.Itoa(feedMaxChannels) + " channels")
		return
	}
	var seen []string
	for i := range in.Channels {
		c := &in.Channels[i]
		c.ChannelID = strings.TrimSpace(c.ChannelID)
		switch {
		case c.ChannelID == "" || len(c.ChannelID) > 40:
			bad("each channel needs a channel_id")
			return
		case slices.Contains(seen, c.ChannelID):
			bad("a channel is listed twice")
			return
		case !slices.Contains(feedKinds, c.Kind):
			bad("kind is one of " + strings.Join(feedKinds, ", "))
			return
		case c.ReadBackDays != nil && (*c.ReadBackDays < 0 || *c.ReadBackDays > 7):
			bad("read_back_days is from 0 to 7")
			return
		}
		seen = append(seen, c.ChannelID)
		if c.SignalType != nil && (*c.SignalType == "" || len(*c.SignalType) > 100) {
			bad("signal_type is one of the brand's signal types")
			return
		}
	}
	o, reader, ok := s.feedConnection(w, r, a, in.IntegrationID)
	if !ok {
		return
	}
	names := map[string]string{}
	if len(in.Channels) > 0 {
		list, err := reader.Channels(r.Context(), o.Client, o.Creds)
		_ = s.integrations.Checked(r.Context(), s.pool, a.WorkspaceID, o.ID, nil, err)
		if err != nil {
			s.feedFailed(w, r, a, "save", err)
			return
		}
		for _, ch := range list {
			names[ch.ID] = ch.Name
		}
		for _, c := range in.Channels {
			if _, ok := names[c.ChannelID]; !ok {
				bad("Slack does not list channel " + c.ChannelID + " for this connection")
				return
			}
		}
	}

	var out feedSettingsOut
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		// One save at a time per brand, so two never add the same channel.
		if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "sales-feeds:"+a.Brand); err != nil {
			return err
		}
		// A brand has its starter signal types from its first use.
		if err := signals.EnsureTypes(r.Context(), tx, a.WorkspaceID, a.Brand); err != nil {
			return err
		}
		for _, c := range in.Channels {
			if c.Kind != "signal" {
				continue // stored but not used, so not checked
			}
			signal := feedDefaultType
			if c.SignalType != nil {
				signal = *c.SignalType
			}
			var known bool
			if err := tx.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM signal_types WHERE workspace_id=$1 AND brand_id=$2 AND key=$3 AND archived_at IS NULL)`,
				a.WorkspaceID, a.Brand, signal).Scan(&known); err != nil {
				return err
			}
			if !known {
				return errInvalid("signal_type " + signal + " is not one of this brand's signal types")
			}
		}
		// Existing channels by Slack channel id, removed ones too: a channel
		// keeps its row, and its posts, when the connection changes, when a
		// save leaves it out and when it is added back.
		type was struct {
			id      string
			enabled bool
			removed bool
			back    int
		}
		existing := map[string]was{}
		rows, err := tx.Query(r.Context(), `SELECT id::text,channel_id,enabled,removed_at IS NOT NULL,read_back_days
			FROM sales_feed_channels WHERE workspace_id=$1 AND brand_id=$2 FOR UPDATE`, a.WorkspaceID, a.Brand)
		if err != nil {
			return err
		}
		for rows.Next() {
			var w was
			var ch string
			if err := rows.Scan(&w.id, &ch, &w.enabled, &w.removed, &w.back); err != nil {
				rows.Close()
				return err
			}
			existing[ch] = w
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		now := time.Now()
		keep := []string{}
		for _, c := range in.Channels {
			signal := feedDefaultType
			if c.SignalType != nil {
				signal = *c.SignalType
			}
			prev, found := existing[c.ChannelID]
			enabled := c.Enabled == nil || *c.Enabled
			if found && !prev.removed && c.Enabled == nil {
				enabled = prev.enabled // left out: the channel stays as it is
			}
			back := feedDefaultBack
			if c.ReadBackDays != nil {
				back = *c.ReadBackDays
			}
			if !found {
				var id string
				if err := tx.QueryRow(r.Context(), `INSERT INTO sales_feed_channels(workspace_id,brand_id,integration_id,channel_id,channel_name,kind,signal_type,read_back_days,enabled,read_since)
					VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id::text`, a.WorkspaceID, a.Brand, o.ID, c.ChannelID, names[c.ChannelID], c.Kind, signal, back, enabled,
					feedReadStart(now, back)).Scan(&id); err != nil {
					return err
				}
				keep = append(keep, id)
				continue
			}
			keep = append(keep, prev.id)
			// On another connection now (the old one was replaced or
			// deleted), it moves to this one, and the old one's error goes.
			if _, err := tx.Exec(r.Context(), `UPDATE sales_feed_channels SET integration_id=$4,channel_name=$5,kind=$6,signal_type=$7,enabled=$8,
				error_code=CASE WHEN integration_id IS DISTINCT FROM $4::uuid THEN '' ELSE error_code END,removed_at=NULL,updated_at=now()
				WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`,
				a.WorkspaceID, a.Brand, prev.id, o.ID, names[c.ChannelID], c.Kind, signal, enabled); err != nil {
				return err
			}
			if enabled && (!prev.enabled || prev.removed) {
				// Turned on again, or added back: reading starts again
				// read_back_days before now, so posts made before that while
				// it was off are not read. Added back, it takes the days
				// given, like a channel added for the first time.
				days := prev.back
				if prev.removed {
					days = back
				}
				start := feedReadStart(now, days)
				if _, err := tx.Exec(r.Context(), `UPDATE sales_feed_channels SET read_back_days=$4,read_since=$5,cursor_ts=$6,updated_at=now()
					WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, a.WorkspaceID, a.Brand, prev.id, days, start, feedStamp(start)); err != nil {
					return err
				}
				if err := s.feedRequeue(r.Context(), tx, a, prev.id); err != nil {
					return err
				}
			}
		}
		// What is left out stops being read and is no longer listed. Its row
		// and the posts stored from it stay, on the conversations and records
		// they are about.
		if _, err := tx.Exec(r.Context(), `UPDATE sales_feed_channels SET removed_at=now(),enabled=false,updated_at=now()
			WHERE workspace_id=$1 AND brand_id=$2 AND removed_at IS NULL AND id::text <> ALL($3)`, a.WorkspaceID, a.Brand, keep); err != nil {
			return err
		}
		data := map[string]any{"brand_id": a.Brand, "channels": len(in.Channels)}
		if err := recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "sales_feed.updated", "brand", a.Brand, data, data); err != nil {
			return err
		}
		out, err = s.feedSettings(r.Context(), tx, a)
		return err
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// feedRequeue queues the posts that waited while the channel was off. Each
// turn-on is its own version, so a job that ended while the channel was off
// does not stand in for this one.
func (s *Server) feedRequeue(ctx context.Context, tx pgx.Tx, a *access, channel string) error {
	rows, err := tx.Query(ctx, `SELECT id::text,content_hash FROM sales_feed_items WHERE workspace_id=$1 AND brand_id=$2 AND feed_channel_id=$3 AND status='pending' ORDER BY occurred_at`, a.WorkspaceID, a.Brand, channel)
	if err != nil {
		return err
	}
	type waiting struct{ id, hash string }
	var list []waiting
	for rows.Next() {
		var w waiting
		if err := rows.Scan(&w.id, &w.hash); err != nil {
			rows.Close()
			return err
		}
		list = append(list, w)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	on := strconv.FormatInt(time.Now().UnixMicro(), 10)
	for _, w := range list {
		if err := s.enqueueFeedItem(ctx, tx, a, w.id, w.hash+":on:"+on); err != nil {
			return err
		}
	}
	return nil
}

// feedWouldMake is what reading a post makes, from what feedDecide found and
// the channel's kind.
func feedWouldMake(outcome, kind string) string {
	switch outcome {
	case "spam":
		return "spam"
	case "person":
		switch kind {
		case "signal":
			return "signal"
		case "context":
			return "context"
		}
		return "conversation"
	}
	return "needs_look"
}

type feedFieldOut struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type feedPreviewOut struct {
	SlackTS    string         `json:"slack_ts"`
	At         time.Time      `json:"at"`
	Text       string         `json:"text"`
	WouldMake  string         `json:"would_make"`
	Fields     []feedFieldOut `json:"fields"`
	Candidates []string       `json:"candidates"`
	Summary    string         `json:"summary"`
}

// testSalesFeedChannel reads a channel's latest posts and says what each
// would become. Nothing is stored. The answer holds post text for the person
// trying the channel; none of it goes into an event or a log.
func (s *Server) testSalesFeedChannel(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		IntegrationID string `json:"integration_id"`
		ChannelID     string `json:"channel_id"`
		Kind          string `json:"kind"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.ChannelID = strings.TrimSpace(in.ChannelID)
	if in.ChannelID == "" || len(in.ChannelID) > 40 || !slices.Contains(feedKinds, in.Kind) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "provide a channel_id and a kind, one of "+strings.Join(feedKinds, ", "))
		return
	}
	o, reader, ok := s.feedConnection(w, r, a, in.IntegrationID)
	if !ok {
		return
	}
	// One call for the newest posts: a preview never reads a channel's history.
	msgs, err := reader.Latest(r.Context(), o.Client, o.Creds, in.ChannelID, feedPreviewPosts)
	_ = s.integrations.Checked(r.Context(), s.pool, a.WorkspaceID, o.ID, nil, err)
	if err != nil {
		s.feedFailed(w, r, a, "test", err)
		return
	}
	if len(msgs) > feedPreviewPosts {
		msgs = msgs[:feedPreviewPosts]
	}
	own, _, err := s.feedOwn(r.Context(), a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := make([]feedPreviewOut, 0, len(msgs))
	aiFailed := false
	for _, m := range msgs {
		x := leadfeed.Extract(m.Text, own)
		// No item exists: each call books its own spend under a ref of its own.
		rd, ok, err := s.feedRead(r.Context(), a.WorkspaceID, "preview:"+newID(), m.Text, x)
		if err != nil {
			// Its text can quote the post: only the code is kept.
			aiFailed = true
			rd, ok = feedReading{}, false
		}
		_, _, outcome := feedDecide(x, rd, ok)
		p := feedPreviewOut{SlackTS: m.ID, At: m.At, Text: m.Text, WouldMake: feedWouldMake(outcome, in.Kind),
			Fields: []feedFieldOut{}, Candidates: []string{}}
		for _, f := range x.Fields {
			p.Fields = append(p.Fields, feedFieldOut{Label: f.Label, Value: f.Value})
		}
		p.Candidates = append(p.Candidates, x.Emails...)
		if ok {
			p.Summary = rd.Summary
		}
		out = append(out, p)
	}
	if aiFailed {
		track.Fail(r.Context(), s.log, track.Event{Name: "sales_feed.test_ai_failed", WorkspaceID: a.WorkspaceID, Outcome: track.Error,
			Props: map[string]any{"brand_id": a.Brand}}, "ai_failed", errFeedAIFailed)
	}
	track.Emit(r.Context(), track.Event{Name: "sales_feed.tested", WorkspaceID: a.WorkspaceID,
		Props: map[string]any{"brand_id": a.Brand, "kind": in.Kind, "posts": len(out)}})
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}
