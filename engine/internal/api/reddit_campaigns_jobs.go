package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/engain"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Reddit campaigns in the background: sending approved actions to Engain,
// and following each one afterwards, on Engain and on Reddit as a
// logged-out visitor sees it, often at first and then less and less.

const (
	jobRedditSend  = "reddit.send"
	jobRedditCheck = "reddit.check"

	// An approved action whose own send job hasn't run this long after
	// approval is queued again, in case its job was lost.
	redditSendLate = 2 * time.Minute
	// An action still sending this long lost its job mid-call.
	redditSendStuck = 5 * time.Minute
	// A task Engain hasn't published this long after it was sent is
	// marked never published.
	redditNeverPublished = 14 * 24 * time.Hour
	// A removed or deleted action is looked at again for this long, since
	// moderators sometimes restore one.
	redditRecheckEnded = 7 * 24 * time.Hour
)

func (s *Server) registerRedditJobs(q *jobs.Queue) {
	q.Handle(jobRedditSend, s.redditSendJob)
	q.Handle(jobRedditCheck, s.redditCheckJob)
}

func enqueueRedditSend(ctx context.Context, tx pgx.Tx, ws, id string, approvedAt time.Time) error {
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobRedditSend, WorkspaceID: &ws, Payload: map[string]string{"action_id": id},
		IdempotencyKey: "reddit.send:" + id + ":" + strconv.FormatInt(approvedAt.UnixMicro(), 10), MaxAttempts: 1})
	return err
}

func enqueueRedditCheck(ctx context.Context, tx pgx.Tx, ws, id, key string) error {
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobRedditCheck, WorkspaceID: &ws, Payload: map[string]string{"action_id": id},
		IdempotencyKey: "reddit.check:" + id + ":" + key, MaxAttempts: 2})
	return err
}

// QueueDueReddit queues the checks that are due, sends approved actions
// whose job was lost, and settles sends that died mid-call. It reports how
// many jobs it queued.
func (s *Server) QueueDueReddit(ctx context.Context) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Move the next check on first, so a slow check isn't queued twice.
		rows, err := tx.Query(ctx, `UPDATE reddit_actions a SET next_check_at = now() + interval '15 minutes'
			WHERE a.id IN (SELECT id FROM reddit_actions WHERE next_check_at <= now() ORDER BY next_check_at LIMIT 200 FOR UPDATE SKIP LOCKED)
			RETURNING a.workspace_id::text, a.id::text`)
		if err != nil {
			return err
		}
		var due [][2]string
		var ws, id string
		if _, err := pgx.ForEachRow(rows, []any{&ws, &id}, func() error { due = append(due, [2]string{ws, id}); return nil }); err != nil {
			return err
		}
		key := strconv.FormatInt(time.Now().Unix()/60, 10)
		for _, d := range due {
			if err := enqueueRedditCheck(ctx, tx, d[0], d[1], key); err != nil {
				return err
			}
		}
		n += len(due)
		rows, err = tx.Query(ctx, `SELECT workspace_id::text, id::text, approved_at FROM reddit_actions
			WHERE status = 'approved' AND approved_at < now() - make_interval(secs => $1) ORDER BY approved_at LIMIT 100`, redditSendLate.Seconds())
		if err != nil {
			return err
		}
		var at time.Time
		var late []struct {
			ws, id string
			at     time.Time
		}
		if _, err := pgx.ForEachRow(rows, []any{&ws, &id, &at}, func() error {
			late = append(late, struct {
				ws, id string
				at     time.Time
			}{ws, id, at})
			return nil
		}); err != nil {
			return err
		}
		for _, l := range late {
			if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobRedditSend, WorkspaceID: &l.ws, Payload: map[string]string{"action_id": l.id},
				IdempotencyKey: "reddit.send:" + l.id + ":" + strconv.FormatInt(l.at.UnixMicro(), 10) + ":late", MaxAttempts: 1}); err != nil {
				return err
			}
		}
		n += len(late)
		return nil
	})
	if err != nil {
		return n, err
	}
	rows, err := s.pool.Query(ctx, `SELECT workspace_id::text, id::text, kind FROM reddit_actions
		WHERE status = 'sending' AND updated_at < now() - make_interval(secs => $1) LIMIT 100`, redditSendStuck.Seconds())
	if err != nil {
		return n, err
	}
	var stuck [][3]string
	var ws, id, kind string
	if _, err := pgx.ForEachRow(rows, []any{&ws, &id, &kind}, func() error { stuck = append(stuck, [3]string{ws, id, kind}); return nil }); err != nil {
		return n, err
	}
	for _, x := range stuck {
		if err := s.failRedditAction(ctx, x[0], x[1], x[2], "sending", "connector_unavailable",
			errors.New("the call to Engain never reported back, so it may or may not have been created; check Engain before sending it again")); err != nil {
			return n, err
		}
	}
	return n, nil
}

// failRedditAction marks an action failed with its reason.
func (s *Server) failRedditAction(ctx context.Context, ws, id, kind, from, code string, cause error) error {
	outcome := "failed"
	if code == "connector_unavailable" {
		outcome = "unknown"
	}
	metrics.RedditSent(kind, outcome)
	track.Fail(ctx, s.log, track.Event{Name: "reddit_action.send_failed", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"kind": kind, "action_id": id}}, code, cause)
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE reddit_actions SET status = 'failed', status_reason = $4, next_check_at = NULL, updated_at = now()
			WHERE workspace_id = $1 AND id = $2 AND status = $3`, ws, id, from, truncate(cause.Error(), 500))
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return redditEvent(ctx, tx, ws, id, "send_failed", from, "failed", map[string]any{"code": code})
	})
}

type redditSendRow struct {
	brand, kind, targetURL, targetKind, title, content, upStart string
	children                                                    json.RawMessage
	goal, perDay                                                int
	scheduled                                                   *time.Time
}

func (s *Server) redditSendJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		ActionID string `json:"action_id"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return jobs.ErrPermanent
	}
	return s.SendRedditAction(ctx, *job.WorkspaceID, p.ActionID)
}

// SendRedditAction sends an approved action to Engain, once.
func (s *Server) SendRedditAction(ctx context.Context, ws, id string) error {
	var a redditSendRow
	err := s.pool.QueryRow(ctx, `UPDATE reddit_actions SET status = 'sending', updated_at = now()
		WHERE workspace_id = $1 AND id = $2 AND status = 'approved'
		RETURNING brand_id::text, kind, target_url, target_kind, title, content, children, upvotes_goal, upvotes_per_day, upvotes_start, scheduled_at`, ws, id).
		Scan(&a.brand, &a.kind, &a.targetURL, &a.targetKind, &a.title, &a.content, &a.children, &a.goal, &a.perDay, &a.upStart, &a.scheduled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // cancelled or already sent
	}
	if err != nil {
		return err
	}
	fail := func(code string, cause error) error {
		return s.failRedditAction(ctx, ws, id, a.kind, "sending", code, cause)
	}
	var set redditSettingsView
	if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		set, err = redditSettings(ctx, tx, ws, a.brand, false)
		return err
	}); err != nil {
		return fail("internal", err)
	}
	o, err := s.openEngain(ctx, ws, a.brand, set)
	if err != nil {
		if errors.Is(err, connectors.ErrNoCapable) {
			return fail("connector_needed", errors.New("no Engain connection: connect Engain under Connectors, then send it again"))
		}
		return fail("connector_auth", err)
	}
	project, err := s.engainProject(ctx, o, set)
	if err != nil {
		return fail("connector_"+string(connectors.KindOf(err)), err)
	}
	created, err := sendToEngain(ctx, o, project, a)
	if err != nil {
		k := connectors.KindOf(err)
		if k == connectors.ErrAuth {
			_ = s.integrations.Checked(ctx, s.pool, ws, o.ID, nil, err)
		}
		if k == connectors.ErrUnavailable {
			err = errors.New("the Engain call got no answer, so it may or may not have been created; check Engain before sending it again: " + err.Error())
		}
		code := "connector_unavailable"
		if k != "" {
			code = "connector_" + string(k)
		}
		return fail(code, err)
	}
	metrics.RedditSent(a.kind, "submitted")
	track.Emit(ctx, track.Event{Name: "reddit_action.submitted", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"kind": a.kind, "action_id": id}})
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		ids := created.IDs
		if ids == nil {
			ids = []string{}
		}
		_, err := tx.Exec(ctx, `UPDATE reddit_actions SET status = 'submitted', engain_ids = $3, engain_raw = $4, integration_id = $5, sent_at = now(),
			status_reason = NULL, next_check_at = now() + interval '2 minutes', updated_at = now() WHERE workspace_id = $1 AND id = $2`,
			ws, id, ids, []byte(created.Raw), o.ID)
		if err != nil {
			return err
		}
		return redditEvent(ctx, tx, ws, id, "submitted", "sending", "submitted", map[string]any{"engain_ids": ids})
	})
}

// sendToEngain makes the Engain call for an action.
func sendToEngain(ctx context.Context, o *connectors.Opened, project string, a redditSendRow) (engain.Created, error) {
	e := engain.Engain{}
	at := ""
	if a.scheduled != nil && a.scheduled.After(time.Now()) {
		at = a.scheduled.UTC().Format(time.RFC3339)
	}
	var rank *engain.Rank
	if a.goal > 0 && a.kind != "upvotes" {
		rank = &engain.Rank{UpvotesGoal: a.goal, UpvotesPerDay: max(a.perDay, 1), StartingDate: a.upStart}
	}
	switch a.kind {
	case "comment", "reply":
		return e.CreateTask(ctx, o.Client, o.Creds, engain.TaskRequest{Kind: a.kind, ProjectID: project, URL: a.targetURL, Content: a.content,
			ScheduledAt: at, Rank: rank})
	case "post":
		return e.CreateTask(ctx, o.Client, o.Creds, engain.TaskRequest{Kind: "post", ProjectID: project, SubredditURL: a.targetURL, PostTitle: a.title,
			Content: a.content, ScheduledAt: at, Rank: rank})
	case "thread":
		var kids []redditChild
		if err := json.Unmarshal(a.children, &kids); err != nil {
			return engain.Created{}, connectors.Errf(connectors.ErrInvalid, "the thread's replies are unreadable: %v", err)
		}
		parent := map[string]any{"content": a.content}
		switch a.targetKind {
		case "subreddit":
			parent["type"], parent["subredditUrl"], parent["postTitle"] = "post", a.targetURL, a.title
		case "comment":
			parent["type"], parent["url"] = "reply", a.targetURL
		default:
			parent["type"], parent["url"] = "comment", a.targetURL
		}
		if at != "" {
			parent["scheduledAt"] = at
		}
		if rank != nil {
			parent["rank"] = rank
		}
		t := engain.ThreadReq{ProjectID: project, ParentTask: parent}
		for i, k := range kids {
			typ := k.Type
			if typ == "" {
				typ = "reply"
			}
			t.ChildTasks = append(t.ChildTasks, engain.ThreadChild{ID: k.ID, ParentID: k.ParentID, Type: typ, PosterAccount: k.Account,
				Content: k.Content, ThreadOrder: i, ScheduleDelay: map[string]any{"amount": max(k.DelayMinutes, 1), "unit": "minutes"}})
		}
		return e.CreateTask(ctx, o.Client, o.Creds, engain.TaskRequest{Kind: "thread", Thread: &t})
	case "upvotes":
		target := "comment"
		if a.targetKind == "thread" {
			target = "post"
		}
		return e.CreateUpvoteOrder(ctx, o.Client, o.Creds, engain.VoteOrder{ProjectID: project, URL: a.targetURL, Target: target, Quantity: a.goal,
			VotesPerDay: max(a.perDay, 1), ScheduledAt: at})
	}
	return engain.Created{}, connectors.Errf(connectors.ErrInvalid, "unknown action kind %q", a.kind)
}

// redditSeen is what Reddit shows a logged-out visitor of one item.
type redditSeen struct {
	State           string // live, frozen, removed, gone
	Score           *int
	Replies         *int
	RemovalCategory string
	Author          string
	Locked          bool
	Archived        bool
	BodyHash        string
}

// readRedditItem reads one item from Reddit's public info endpoint, as a
// logged-out visitor sees it: through the workspace's own page reader
// (Geonode) when it has one, since Reddit turns many servers away, or
// straight from here.
func (s *Server) readRedditItem(ctx context.Context, ws, brand, fullname string) (redditSeen, error) {
	base := "https://www.reddit.com"
	if s.integrations != nil && s.integrations.Client != nil {
		base = s.integrations.Client.Base("reddit.public", base)
	}
	u := base + "/api/info.json?raw_json=1&id=" + fullname
	var body []byte
	if s.integrations != nil && base == "https://www.reddit.com" {
		if o, err := s.integrations.OpenCapable(ctx, s.pool, ws, brand, connectors.CapCrawlFetch); err == nil {
			if f, ok := o.Connector.(connectors.PageFetcher); ok {
				page, err := f.FetchPage(ctx, o.Client, o.Creds, u, false)
				if err == nil && page.Status == http.StatusOK {
					body = []byte(page.HTML)
				}
			}
		}
	}
	if body == nil {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		req.Header.Set("User-Agent", "web:outflare-reddit-check:1.0")
		req.Header.Set("Accept", "application/json")
		do := (&http.Client{Timeout: 20 * time.Second}).Do
		if s.integrations != nil && s.integrations.Client != nil {
			do = s.integrations.Client.Do
		}
		resp, err := do(req)
		if err != nil {
			return redditSeen{}, fmt.Errorf("reading Reddit: %w", err)
		}
		body, err = io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		resp.Body.Close()
		if err != nil {
			return redditSeen{}, fmt.Errorf("reading Reddit: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			return redditSeen{}, fmt.Errorf("the Reddit read got HTTP %d; connect Geonode to read Reddit through its network", resp.StatusCode)
		}
	}
	return parseRedditInfo(body)
}

// parseRedditInfo reads an /api/info answer for one item.
func parseRedditInfo(body []byte) (redditSeen, error) {
	// A page reader may wrap the JSON in <pre>.
	if i := strings.IndexByte(string(body), '{'); i > 0 {
		body = body[i:]
	}
	if j := strings.LastIndexByte(string(body), '}'); j >= 0 {
		body = body[:j+1]
	}
	var l struct {
		Data struct {
			Children []struct {
				Kind string `json:"kind"`
				Data struct {
					Author            string  `json:"author"`
					Body              *string `json:"body"`
					Selftext          *string `json:"selftext"`
					Score             *int    `json:"score"`
					NumComments       *int    `json:"num_comments"`
					RemovedByCategory *string `json:"removed_by_category"`
					Locked            bool    `json:"locked"`
					Archived          bool    `json:"archived"`
				} `json:"data"`
			} `json:"children"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &l); err != nil {
		return redditSeen{}, fmt.Errorf("the Reddit read is not readable JSON: %w", err)
	}
	if len(l.Data.Children) == 0 {
		return redditSeen{State: "gone"}, nil
	}
	d := l.Data.Children[0].Data
	v := redditSeen{Score: d.Score, Replies: d.NumComments, Author: d.Author, Locked: d.Locked, Archived: d.Archived, State: "live"}
	text := ""
	if d.Body != nil {
		text = *d.Body
	} else if d.Selftext != nil {
		text = *d.Selftext
	}
	h := sha256.Sum256([]byte(text))
	v.BodyHash = hex.EncodeToString(h[:8])
	switch {
	case d.RemovedByCategory != nil && (*d.RemovedByCategory == "deleted" || *d.RemovedByCategory == "author"):
		v.State, v.RemovalCategory = "gone", *d.RemovedByCategory
	case d.RemovedByCategory != nil && *d.RemovedByCategory != "":
		v.State, v.RemovalCategory = "removed", *d.RemovedByCategory
	case text == "[removed]":
		v.State, v.RemovalCategory = "removed", "removed"
	case text == "[deleted]" || d.Author == "[deleted]":
		v.State, v.RemovalCategory = "gone", "deleted"
	case d.Locked || d.Archived:
		v.State = "frozen"
	}
	return v, nil
}

// engainState maps Engain's status words onto ours: failed, done (an
// order finished), published, or "" (still in progress or unknown).
func engainState(status string) string {
	s := strings.ToLower(status)
	switch {
	case s == "":
		return ""
	case strings.Contains(s, "fail"), strings.Contains(s, "reject"), strings.Contains(s, "error"), strings.Contains(s, "cancel"), strings.Contains(s, "refund"):
		return "failed"
	case strings.Contains(s, "remov"), strings.Contains(s, "delet"):
		return "removed"
	case strings.Contains(s, "complete"), strings.Contains(s, "done"), strings.Contains(s, "finish"), strings.Contains(s, "deliver"):
		return "done"
	case strings.Contains(s, "publish"), strings.Contains(s, "posted"), strings.Contains(s, "live"):
		return "published"
	}
	return ""
}

// redditNextCheck is when to look at an action again, or nil to stop.
func redditNextCheck(kind, status string, liveAt, endedAt *time.Time, now time.Time) *time.Time {
	in := func(d time.Duration) *time.Time { t := now.Add(d); return &t }
	switch status {
	case "approved", "sending":
		return in(15 * time.Minute)
	case "submitted":
		if kind == "upvotes" {
			return in(time.Hour)
		}
		return in(15 * time.Minute)
	case "live", "frozen":
		if liveAt == nil {
			return in(time.Hour)
		}
		age := now.Sub(*liveAt)
		switch {
		case age < 2*time.Hour:
			return in(10 * time.Minute)
		case age < 24*time.Hour:
			return in(time.Hour)
		case age < 7*24*time.Hour:
			return in(6 * time.Hour)
		case age < 30*24*time.Hour:
			return in(24 * time.Hour)
		case age < 180*24*time.Hour:
			return in(7 * 24 * time.Hour)
		}
		return nil
	case "removed", "hidden", "gone":
		if endedAt != nil && now.Sub(*endedAt) < redditRecheckEnded {
			return in(24 * time.Hour)
		}
		return nil
	}
	return nil
}

type redditCheckRow struct {
	brand, kind, status, permalink, fullname, author string
	engainIDs                                        []string
	sentAt, liveAt, endedAt                          *time.Time
	peak                                             *int
	integration                                      *string
}

// redditCheckJob reads an action on Engain and on Reddit, records what it
// saw and moves its status on.
func (s *Server) redditCheckJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		ActionID string `json:"action_id"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return jobs.ErrPermanent
	}
	return s.CheckRedditAction(ctx, *job.WorkspaceID, p.ActionID)
}

// CheckRedditAction reads a sent action on Engain and Reddit now.
func (s *Server) CheckRedditAction(ctx context.Context, ws, id string) error {
	var a redditCheckRow
	err := s.pool.QueryRow(ctx, `SELECT brand_id::text, kind, status, permalink, reddit_fullname, author, engain_ids, sent_at, live_at, ended_at, peak_score,
		integration_id::text FROM reddit_actions WHERE workspace_id = $1 AND id = $2`, ws, id).
		Scan(&a.brand, &a.kind, &a.status, &a.permalink, &a.fullname, &a.author, &a.engainIDs, &a.sentAt, &a.liveAt, &a.endedAt, &a.peak, &a.integration)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if a.sentAt == nil || a.status == "cancelled" || a.status == "failed" || a.status == "done" {
		_, err := s.pool.Exec(ctx, `UPDATE reddit_actions SET next_check_at = NULL WHERE workspace_id = $1 AND id = $2`, ws, id)
		return err
	}
	now := time.Now()
	upd := redditUpdate{status: a.status}
	var checkErr []string

	// Engain's view.
	var it *engain.Item
	if len(a.engainIDs) > 0 && (a.status == "submitted" || a.kind != "upvotes") {
		set := defaultRedditSettings(a.brand)
		set.IntegrationID = a.integration
		if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			x, err := redditSettings(ctx, tx, ws, a.brand, false)
			if err == nil {
				set = x
				if a.integration != nil {
					set.IntegrationID = a.integration
				}
			}
			return err
		}); err != nil {
			return err
		}
		o, err := s.openEngain(ctx, ws, a.brand, set)
		if err == nil {
			e := engain.Engain{}
			if a.kind == "upvotes" {
				project, perr := s.engainProject(ctx, o, set)
				if perr != nil {
					err = perr
				} else {
					var list []engain.Item
					if list, err = e.Orders(ctx, o.Client, o.Creds, project, a.engainIDs[:1]); err == nil && len(list) > 0 {
						it = &list[0]
					}
				}
			} else {
				var x engain.Item
				if x, err = e.Task(ctx, o.Client, o.Creds, a.engainIDs[0]); err == nil {
					it = &x
				}
			}
		}
		if err != nil {
			checkErr = append(checkErr, "Engain: "+err.Error())
		}
	}
	if it != nil {
		upd.engainStatus, upd.engainRaw = &it.Status, it.Raw
		if it.Credits != nil {
			upd.credits = it.Credits
		}
		if it.Delivered != nil {
			n := int(*it.Delivered)
			upd.delivered = &n
		}
		if it.Replies != nil {
			n := int(*it.Replies)
			upd.replies = &n
		}
		if it.Upvotes != nil && a.permalink == "" {
			n := int(*it.Upvotes)
			upd.score = &n
		}
		if it.Author != "" && a.author == "" {
			upd.author = &it.Author
		}
		if it.URL != "" && a.permalink == "" && a.kind != "upvotes" {
			pl := redditPermalink(it.URL)
			if fn := redditFullname(pl); fn != "" {
				a.permalink, a.fullname = pl, fn
				upd.permalink, upd.fullname = &pl, &fn
			}
		}
		switch engainState(it.Status) {
		case "failed":
			if a.status == "submitted" {
				upd.status = "failed"
				r := "Engain reports the task as " + it.Status
				upd.reason = &r
			}
		case "done":
			if a.kind == "upvotes" {
				upd.status = "done"
			} else if a.status == "submitted" && a.permalink != "" {
				upd.status = "live"
			}
		case "published":
			if a.status == "submitted" && a.permalink != "" {
				upd.status = "live"
			}
		case "removed":
			if a.permalink == "" && (a.status == "submitted" || a.status == "live") {
				upd.status = "removed"
			}
		}
	}

	// Reddit's view, the one that counts.
	read := false
	var seen redditSeen
	if a.fullname != "" && a.kind != "upvotes" {
		v, err := s.readRedditItem(ctx, ws, a.brand, a.fullname)
		if err != nil {
			checkErr = append(checkErr, "Reddit: "+err.Error())
		} else {
			read, seen = true, v
			upd.status = v.State
			upd.score, upd.removal = v.Score, &v.RemovalCategory
			if a.kind == "post" || (a.kind == "thread" && strings.HasPrefix(a.fullname, "t3_")) {
				upd.replies = v.Replies
			}
			if v.Author != "" && v.Author != "[deleted]" && a.author == "" {
				upd.author = &v.Author
			}
		}
	}
	if upd.status == "submitted" && a.sentAt != nil && now.Sub(*a.sentAt) > redditNeverPublished {
		upd.status = "failed"
		r := "not published on Reddit within 14 days of sending"
		upd.reason = &r
	}

	outcome := "ok"
	if len(checkErr) > 0 {
		outcome = "engain_error"
		if !read && a.fullname != "" {
			outcome = "reddit_error"
		}
	}
	metrics.RedditChecked(outcome, read)
	if len(checkErr) > 0 {
		track.Fail(ctx, s.log, track.Event{Name: "reddit_action.check_failed", WorkspaceID: ws, ActorType: "system", Surface: "job",
			Props: map[string]any{"kind": a.kind, "action_id": id}}, "connector_unavailable", errors.New(strings.Join(checkErr, "; ")))
	}

	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		liveAt, endedAt := a.liveAt, a.endedAt
		live := func(st string) bool { return st == "live" || st == "frozen" }
		ended := func(st string) bool { return st == "removed" || st == "hidden" || st == "gone" }
		kind := ""
		switch {
		case live(upd.status) && !live(a.status) && liveAt == nil:
			kind, liveAt = "went_live", &now
		case live(upd.status) && ended(a.status):
			kind, endedAt = "restored", nil
		case ended(upd.status) && !ended(a.status):
			kind, endedAt = upd.status, &now
			if liveAt == nil {
				liveAt = &now
			}
		case upd.status == "frozen" && a.status == "live":
			kind = "frozen"
		case upd.status != a.status:
			kind = upd.status
		}
		if kind != "" {
			detail := map[string]any{}
			if upd.removal != nil && *upd.removal != "" {
				detail["removal_category"] = *upd.removal
			}
			if upd.reason != nil {
				detail["reason"] = *upd.reason
			}
			if err := redditEvent(ctx, tx, ws, id, kind, a.status, upd.status, detail); err != nil {
				return err
			}
			if ended(upd.status) || upd.status == "failed" {
				track.Emit(ctx, track.Event{Name: "reddit_action." + upd.status, WorkspaceID: ws, ActorType: "system", Surface: "job",
					Props: map[string]any{"kind": a.kind, "action_id": id}})
			}
		}
		if read {
			if _, err := tx.Exec(ctx, `INSERT INTO reddit_action_snapshots (workspace_id, action_id, state, score, reply_count, removal_category, locked, archived, body_hash)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, ws, id, seen.State, seen.Score, seen.Replies, seen.RemovalCategory, seen.Locked, seen.Archived, seen.BodyHash); err != nil {
				return err
			}
		}
		peak := a.peak
		if upd.score != nil && (peak == nil || *upd.score > *peak) {
			peak = upd.score
		}
		next := redditNextCheck(a.kind, upd.status, liveAt, endedAt, now)
		if len(checkErr) > 0 && next != nil && next.Sub(now) > 30*time.Minute && !read {
			t := now.Add(30 * time.Minute)
			next = &t
		}
		var errText *string
		if len(checkErr) > 0 {
			e := truncate(strings.Join(checkErr, "; "), 500)
			errText = &e
		}
		_, err := tx.Exec(ctx, `UPDATE reddit_actions SET status = $3, status_reason = coalesce($4, status_reason),
				engain_status = coalesce($5, engain_status), engain_raw = coalesce($6, engain_raw), credits = coalesce($7, credits),
				upvotes_delivered = coalesce($8, upvotes_delivered), reply_count = coalesce($9, reply_count), score = coalesce($10, score), peak_score = $11,
				author = coalesce($12, author), permalink = coalesce($13, permalink), reddit_fullname = coalesce($14, reddit_fullname),
				removal_category = CASE WHEN $3 IN ('live', 'frozen') THEN '' ELSE coalesce($15, removal_category) END,
				live_at = $16, ended_at = $17, last_checked_at = now(), next_check_at = $18, check_error = $19, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`,
			ws, id, upd.status, upd.reason, upd.engainStatus, nullJSON(upd.engainRaw), upd.credits, upd.delivered, upd.replies, upd.score, peak,
			upd.author, upd.permalink, upd.fullname, upd.removal, liveAt, endedAt, next, errText)
		return err
	})
}

// redditUpdate is what one check found.
type redditUpdate struct {
	status                                string
	reason, engainStatus, author, removal *string
	permalink, fullname                   *string
	engainRaw                             json.RawMessage
	credits                               *float64
	delivered, replies, score             *int
}
