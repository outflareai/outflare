package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/slack"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Sales alerts: one message per new Sales conversation in the Slack channel
// a brand picks, kept current as the conversation moves. The message says
// who it is, where they came from and the fields their channel shows first;
// emojis say where the conversation stands. It is posted by the workspace's
// own Slack (sales channels) connection, in a job, and never from a channel
// Outflare reads, so a post is never read back as a new person.

const jobSalesAlert = "sales.alert" // payload {"id": lead id, "brand_id"}

// The emoji on a conversation's message, by what is true of it.
const (
	alertSeen     = "eyes"
	alertAssigned = "raising_hand"
	alertTalking  = "speech_balloon"
	alertHandoff  = "handshake"
	alertCustomer = "tada"
	alertGood     = "+1"
	alertPoorFit  = "-1"
	alertJunk     = "wastebasket"
	alertArchived = "x"
)

type salesAlertsOut struct {
	Enabled     bool   `json:"enabled"`
	ChannelID   string `json:"channel_id"`
	ChannelName string `json:"channel_name"`
	Replies     bool   `json:"replies"`
	// ErrorCode is why the last post failed (slack_auth: the token was
	// refused or lacks the scopes to post; slack_channel: the app is not in
	// the channel), or empty.
	ErrorCode string `json:"error_code"`
}

func salesAlertSettings(ctx context.Context, q connectors.Querier, a *access) (salesAlertsOut, string, *time.Time, error) {
	out := salesAlertsOut{Replies: true}
	var integ string
	var since *time.Time
	err := q.QueryRow(ctx, `SELECT enabled,channel_id,channel_name,replies,error_code,COALESCE(integration_id::text,''),enabled_at
		FROM sales_alert_settings WHERE workspace_id=$1 AND brand_id=$2`, a.WorkspaceID, a.Brand).
		Scan(&out.Enabled, &out.ChannelID, &out.ChannelName, &out.Replies, &out.ErrorCode, &integ, &since)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return out, integ, since, err
}

// putSalesAlerts saves where the brand's new conversations are posted and
// which of them are.
func (s *Server) putSalesAlerts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Enabled   bool   `json:"enabled"`
		ChannelID string `json:"channel_id"`
		Replies   bool   `json:"replies"`
		// FeedChannels are the Sales channels (sales_feed_channels ids)
		// whose conversations are posted; the brand's others are not.
		FeedChannels []string `json:"feed_channels"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.ChannelID) > 40 || len(in.FeedChannels) > feedMaxChannels || slices.ContainsFunc(in.FeedChannels, func(v string) bool { return !validUUID(v) }) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "channel_id is a Slack channel id and feed_channels are Sales channel ids")
		return
	}
	if in.Enabled && in.ChannelID == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "choose the Slack channel to post in")
		return
	}
	integ, name := "", ""
	if in.ChannelID != "" {
		serving, err := s.feedServing(r.Context(), a)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if len(serving) == 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "connect Slack (sales channels) first")
			return
		}
		integ = serving[0].ID
		o, reader, ok := s.feedConnection(w, r, a, integ)
		if !ok {
			return
		}
		if _, ok := o.Connector.(connectors.FeedPoster); !ok {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "this Slack connection can't post")
			return
		}
		list, err := reader.Channels(r.Context(), o.Client, o.Creds)
		if err != nil {
			s.feedFailed(w, r, a, "channels", err)
			return
		}
		i := slices.IndexFunc(list, func(c connectors.Channel) bool { return c.ID == in.ChannelID })
		if i < 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "the Slack app can't see that channel: invite it first")
			return
		}
		name = list[i].Name
	}
	var out salesAlertsOut
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		// A channel Outflare reads is never one it posts to: its own posts
		// would come back as new people.
		var read bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM sales_feed_channels WHERE workspace_id=$1 AND brand_id=$2 AND channel_id=$3 AND removed_at IS NULL AND $3<>'')`,
			a.WorkspaceID, a.Brand, in.ChannelID).Scan(&read); err != nil {
			return err
		}
		if read {
			return errInvalid("that channel is one of the Sales channels Outflare reads; post alerts in another")
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO sales_alert_settings(workspace_id,brand_id,integration_id,channel_id,channel_name,enabled,replies,enabled_at)
			VALUES($1,$2,NULLIF($3,'')::uuid,$4,$5,$6,$7,CASE WHEN $6 THEN now() END)
			ON CONFLICT(workspace_id,brand_id) DO UPDATE SET integration_id=EXCLUDED.integration_id,channel_id=EXCLUDED.channel_id,channel_name=EXCLUDED.channel_name,
				enabled=EXCLUDED.enabled,replies=EXCLUDED.replies,error_code='',updated_at=now(),
				enabled_at=CASE WHEN NOT EXCLUDED.enabled THEN NULL
					WHEN sales_alert_settings.enabled AND sales_alert_settings.channel_id=EXCLUDED.channel_id THEN sales_alert_settings.enabled_at ELSE now() END`,
			a.WorkspaceID, a.Brand, integ, in.ChannelID, name, in.Enabled, in.Replies); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE sales_feed_channels SET alert=(id=ANY($3::uuid[])) WHERE workspace_id=$1 AND brand_id=$2 AND removed_at IS NULL`,
			a.WorkspaceID, a.Brand, in.FeedChannels); err != nil {
			return err
		}
		var err error
		if out, _, _, err = salesAlertSettings(r.Context(), tx, a); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "sales_alert.updated", "brand", a.Brand,
			map[string]any{"brand_id": a.Brand, "enabled": in.Enabled, "replies": in.Replies, "feed_channels": len(in.FeedChannels)})
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// enqueueSalesAlert queues a look at one conversation's Slack message. The
// job compares what is posted with what is true now, so queueing it twice
// posts nothing twice.
func (s *Server) enqueueSalesAlert(ctx context.Context, q jobs.Querier, a *access, lead string) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: jobSalesAlert, WorkspaceID: &a.WorkspaceID, Payload: map[string]string{"id": lead, "brand_id": a.Brand}, MaxAttempts: 5})
	return err
}

// salesAlertText is a conversation's message, in Slack's markup. Names and
// values come from outside and are escaped.
func (s *Server) salesAlertText(ctx context.Context, a *access, l salesLead, owner string) string {
	esc := slack.Escape
	who := l.Name
	if who == "" {
		who = l.Company
	}
	if who == "" {
		who = "New conversation"
	}
	link := s.publicURL + s.brandAppPath(ctx, a.WorkspaceID, a.Brand, "crm/sales%3Alead%3A"+l.ID)
	head := []string{"*<" + link + "|" + esc(who) + ">*"}
	if l.Company != "" && l.Company != who {
		head = append(head, esc(l.Company))
	}
	if site := strings.TrimSpace(l.Website); site != "" && !strings.ContainsAny(site, " <>|") {
		if !strings.Contains(site, "://") {
			site = "https://" + site
		}
		if strings.HasPrefix(site, "https://") || strings.HasPrefix(site, "http://") {
			head = append(head, "<"+site+"|"+esc(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(site, "https://"), "http://"), "/"))+">")
		}
	}
	lines := []string{strings.Join(head, " · ")}
	from := "Email reply"
	if l.FeedKind != nil {
		from = "#" + l.FeedChannelName
		if l.FeedSource != nil && *l.FeedSource != "" {
			from = *l.FeedSource + " · " + from
		}
	}
	lines = append(lines, "Source: "+esc(from))
	if l.LatestPost != nil {
		var parts []string
		for _, f := range l.LatestPost.Fields {
			if f.Value != "" {
				parts = append(parts, esc(f.Label)+": "+esc(feedCut(strings.Join(strings.Fields(f.Value), " "), 120)))
			}
		}
		if len(parts) > 0 {
			lines = append(lines, strings.Join(parts, " · "))
		}
	}
	if owner != "" {
		lines = append(lines, "Owner: "+esc(owner))
	}
	return strings.Join(lines, "\n")
}

// salesAlertReactions are the emojis a conversation's message should carry.
func salesAlertReactions(l salesLead, seen bool) []string {
	var out []string
	add := func(on bool, emoji string) {
		if on {
			out = append(out, emoji)
		}
	}
	add(seen, alertSeen)
	add(l.OwnerID != nil, alertAssigned)
	add(l.State == "active" || l.State == "customer", alertTalking)
	add(l.SDRID != nil && l.AEID != nil, alertHandoff)
	add(l.State == "customer", alertCustomer)
	add(deref(l.Quality) == "good", alertGood)
	add(deref(l.Quality) == "poor_fit", alertPoorFit)
	add(deref(l.Quality) == "junk", alertJunk)
	add(l.State == "archived", alertArchived)
	return out
}

func (s *Server) salesAlertJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		ID    string `json:"id"`
		Brand string `json:"brand_id"`
	}
	if json.Unmarshal(job.Payload, &p) != nil || job.WorkspaceID == nil || !validUUID(p.ID) || !validUUID(p.Brand) {
		return jobs.ErrPermanent
	}
	a := &access{WorkspaceID: *job.WorkspaceID, Brand: p.Brand}
	set, integ, since, err := salesAlertSettings(ctx, s.pool, a)
	if err != nil || !set.Enabled || set.ChannelID == "" || integ == "" || since == nil {
		return err
	}
	// One look at a conversation at a time, so two jobs never both post it.
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var locked bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, "sales-alert:"+p.ID).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return jobs.RetryLater(errors.New("the conversation's alert is being posted"), 5*time.Second)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(c, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, "sales-alert:"+p.ID)
	}()

	l, err := s.salesLoad(ctx, s.pool, a, p.ID, false)
	if errors.Is(err, errNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var ts, hash, channel string
	var has []string
	err = s.pool.QueryRow(ctx, `SELECT channel_id,slack_ts,text_hash,reactions FROM sales_lead_alerts WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=$3`,
		a.WorkspaceID, a.Brand, l.ID).Scan(&channel, &ts, &hash, &has)
	posted := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if !posted {
		// Only conversations that arrived since alerts were turned on, from
		// a source the brand chose.
		wanted := set.Replies
		if l.FeedChannelID != nil {
			if err = s.pool.QueryRow(ctx, `SELECT alert FROM sales_feed_channels WHERE workspace_id=$1 AND id=$2`, a.WorkspaceID, *l.FeedChannelID).Scan(&wanted); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if !wanted || l.CreatedAt.Before(*since) {
			return nil
		}
		channel = set.ChannelID
	}

	var seen bool
	var owner string
	if err = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sales_seen WHERE workspace_id=$1 AND brand_id=$2 AND lead_id=$3),
		COALESCE((SELECT name FROM users WHERE id=$4),'')`, a.WorkspaceID, a.Brand, l.ID, l.OwnerID).Scan(&seen, &owner); err != nil {
		return err
	}
	one := []salesLead{l}
	s.salesLatestPosts(ctx, a, one)
	text := s.salesAlertText(ctx, a, one[0], owner)
	want := salesAlertReactions(l, seen)

	failed := func(err error) error {
		if !isConnectorErr(err) {
			return err
		}
		if connectors.KindOf(err) == connectors.ErrQuota {
			return jobs.RetryLater(feedErr(err), feedQuotaWait)
		}
		code := feedRefused(err)
		if code == "" {
			return feedErr(err) // Slack was unreachable: the job tries again
		}
		// Slack refused: the settings say why, once, and the job stops.
		if code != set.ErrorCode {
			track.Fail(ctx, s.log, track.Event{Name: "sales_alert.post_failed", ActorType: "system", Surface: "job", WorkspaceID: a.WorkspaceID, Outcome: track.Failure,
				Props: map[string]any{"brand_id": a.Brand, "integration_id": integ}}, code, feedErr(err))
		}
		_, e := s.pool.Exec(ctx, `UPDATE sales_alert_settings SET error_code=$3,updated_at=now() WHERE workspace_id=$1 AND brand_id=$2`, a.WorkspaceID, a.Brand, code)
		return e
	}
	o, err := s.integrations.Open(ctx, s.pool, a.WorkspaceID, integ)
	if errors.Is(err, connectors.ErrNotFound) {
		return failed(connectors.Errf(connectors.ErrAuth, "the Slack connection is gone"))
	}
	if err != nil {
		return failed(err)
	}
	poster, ok := o.Connector.(connectors.FeedPoster)
	if !ok {
		return failed(connectors.Errf(connectors.ErrAuth, "the Slack connection can't post"))
	}
	sum := salesHash(text)
	if !posted {
		if ts, err = poster.PostAlert(ctx, o.Client, o.Creds, channel, text); err != nil {
			return failed(err)
		}
		if _, err = s.pool.Exec(ctx, `INSERT INTO sales_lead_alerts(lead_id,workspace_id,brand_id,channel_id,slack_ts,text_hash)VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(lead_id) DO NOTHING`,
			l.ID, a.WorkspaceID, a.Brand, channel, ts, sum); err != nil {
			return err
		}
		track.Emit(ctx, track.Event{Name: "sales_alert.posted", ActorType: "system", Surface: "job", WorkspaceID: a.WorkspaceID,
			Props: map[string]any{"brand_id": a.Brand, "lead_id": l.ID, "from_channel": l.FeedKind != nil}})
	} else if sum != hash {
		if err = poster.UpdateAlert(ctx, o.Client, o.Creds, channel, ts, text); err != nil {
			return failed(err)
		}
		if _, err = s.pool.Exec(ctx, `UPDATE sales_lead_alerts SET text_hash=$3,updated_at=now() WHERE workspace_id=$1 AND lead_id=$2`, a.WorkspaceID, l.ID, sum); err != nil {
			return err
		}
	}
	// Each emoji is saved as it lands, so a failure halfway is picked up
	// where it stopped.
	for _, step := range []struct {
		emojis []string
		on     bool
	}{{want, true}, {has, false}} {
		for _, e := range step.emojis {
			if step.on == slices.Contains(has, e) || !step.on && slices.Contains(want, e) {
				continue
			}
			if err = poster.React(ctx, o.Client, o.Creds, channel, ts, e, step.on); err != nil {
				return failed(err)
			}
			sql := `UPDATE sales_lead_alerts SET reactions=array_append(reactions,$3),updated_at=now() WHERE workspace_id=$1 AND lead_id=$2`
			if !step.on {
				sql = `UPDATE sales_lead_alerts SET reactions=array_remove(reactions,$3),updated_at=now() WHERE workspace_id=$1 AND lead_id=$2`
			}
			if _, err = s.pool.Exec(ctx, sql, a.WorkspaceID, l.ID, e); err != nil {
				return err
			}
		}
	}
	if set.ErrorCode != "" {
		_, err = s.pool.Exec(ctx, `UPDATE sales_alert_settings SET error_code='',updated_at=now() WHERE workspace_id=$1 AND brand_id=$2`, a.WorkspaceID, a.Brand)
	}
	return err
}
