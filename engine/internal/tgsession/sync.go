package tgsession

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// KindSync copies an account's chats into its brand's CRM.
const KindSync = "telegram.sync"

// SyncEvery is how often a caught-up account reads new messages.
const SyncEvery = 2 * time.Minute

// syncLease is how long one sync may hold an account.
const syncLease = 10 * time.Minute

// historyBudget is how many older messages one sync copies before it hands
// over to the next, so a big import runs as many short jobs.
const historyBudget = 2000

// newPages bounds how far back one sync reads a chat's new messages.
const newPages = 20

// unchangedStop: reading the chat list stops after this many chats in a
// row with nothing new (the list is newest first; pinned chats come first).
const unchangedStop = 30

const pageSize = 100

// CodeSlowedDown: Telegram asked Outflare to wait.
const CodeSlowedDown = "slowed_down"

// EnqueueSync queues a sync of an account at at, in the caller's
// transaction.
func EnqueueSync(ctx context.Context, q jobs.Querier, ws, account string, at time.Time) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{
		Kind: KindSync, WorkspaceID: &ws, RunAt: at, MaxAttempts: 3,
		IdempotencyKey: "sync:" + account + ":" + at.UTC().Truncate(time.Millisecond).Format(time.RFC3339Nano),
		Payload:        Payload{WorkspaceID: ws, AccountID: account},
	})
	return err
}

func newID() string {
	var b [16]byte
	rand.Read(b[:]) //nolint:errcheck // never fails
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ChatDetails is what is sealed on a chat.
type ChatDetails struct {
	Title      string `json:"title"`
	Username   string `json:"username,omitempty"`
	Phone      string `json:"phone,omitempty"`
	AccessHash int64  `json:"access_hash,omitempty"`
	Members    int    `json:"members,omitempty"`
}

// MessageContent is what is sealed on a message.
type MessageContent struct {
	Text     string `json:"text"`
	FromName string `json:"from_name,omitempty"`
	Media    string `json:"media,omitempty"`
	FileName string `json:"file_name,omitempty"`
	ReplyTo  int    `json:"reply_to,omitempty"`
	Service  bool   `json:"service,omitempty"`
}

// OpenChat reads a chat's sealed details.
func (s *Service) OpenChat(id string, sealed []byte) (ChatDetails, error) {
	var d ChatDetails
	plain, err := s.Box.Open(sealed, []byte(id))
	if err != nil {
		return d, err
	}
	return d, json.Unmarshal(plain, &d)
}

// OpenMessage reads a message's sealed content.
func (s *Service) OpenMessage(id string, sealed []byte) (MessageContent, error) {
	var c MessageContent
	plain, err := s.Box.Open(sealed, []byte(id))
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(plain, &c)
}

func (s *Service) sealJSON(v any, id string) []byte {
	b, _ := json.Marshal(v)
	return s.Box.Seal(b, []byte(id))
}

type chatState struct {
	id           string
	kind         string
	peer         Peer
	last, oldest int
	done         bool
	details      []byte
	fresh        bool // TopMessage beyond last this run
	top          int
}

func peerID(kind string, id int64) string { return kind + ":" + strconv.FormatInt(id, 10) }

type syncRun struct {
	s       *Service
	p       Payload
	brand   string
	cutoff  time.Time
	chats   map[string]*chatState
	in, out int
	budget  int
}

// sync copies new messages and, while the import lasts, older ones.
func (s *Service) sync(ctx context.Context, j jobs.Job) error {
	p, err := payload(j)
	if err != nil {
		return err
	}
	var (
		integration *string
		sealed      []byte
		brand       *string
		started     *time.Time
		days        int
		attempt     int
	)
	// Take the account for this run, or leave it to the run holding it.
	err = s.Pool.QueryRow(ctx, `UPDATE telegram_sessions SET sync_lease_until = now() + make_interval(secs => $3)
		WHERE workspace_id = $1 AND account_id = $2 AND status = 'connected' AND sync_status IN ('importing', 'live')
		  AND brand_id IS NOT NULL AND (sync_lease_until IS NULL OR sync_lease_until < now())
		RETURNING integration_id::text, session_sealed, brand_id::text, sync_started_at, history_days, attempt`,
		p.WorkspaceID, p.AccountID, syncLease.Seconds()).Scan(&integration, &sealed, &brand, &started, &days, &attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	p.Attempt = attempt
	release := func(next time.Time, code string) error {
		return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE telegram_sessions SET sync_lease_until = NULL, sync_error = NULLIF($3, ''), updated_at = now()
				WHERE workspace_id = $1 AND account_id = $2`, p.WorkspaceID, p.AccountID, code); err != nil {
				return err
			}
			return EnqueueSync(ctx, tx, p.WorkspaceID, p.AccountID, next)
		})
	}
	app, err := s.app(ctx, p.WorkspaceID, integration)
	if err != nil {
		return s.syncFailed(ctx, p, err, release)
	}
	stored, err := s.open(sealed, p.AccountID)
	if err != nil {
		return s.syncFailed(ctx, p, err, release)
	}
	from := time.Now()
	if started != nil {
		from = *started
	}
	run := &syncRun{s: s, p: p, brand: *brand, cutoff: from.AddDate(0, 0, -days), budget: historyBudget}
	if err := run.load(ctx); err != nil {
		_ = release(time.Now().Add(SyncEvery), "")
		return err
	}
	colleagues, err := run.colleagues(ctx)
	if err != nil {
		_ = release(time.Now().Add(SyncEvery), "")
		return err
	}
	var importing bool
	session, err := s.Telegram.Read(ctx, app, stored, func(r Reader) error {
		if err := run.list(ctx, r, colleagues); err != nil {
			return err
		}
		if err := run.copyNew(ctx, r); err != nil {
			return err
		}
		importing, err = run.copyHistory(ctx, r)
		return err
	})
	metrics.TelegramSynced("in", run.in)
	metrics.TelegramSynced("out", run.out)
	if errors.Is(err, ErrSignedOut) {
		metrics.TelegramSyncRun(CodeSignedOut)
		return s.signedOut(ctx, p)
	}
	if err != nil {
		return s.syncFailed(ctx, p, err, release)
	}
	status := "live"
	next := time.Now().Add(SyncEvery)
	if importing {
		status, next = "importing", time.Now().Add(5*time.Second)
	}
	metrics.TelegramSyncRun(status)
	track.Emit(ctx, track.Event{Name: "telegram_account.synced", WorkspaceID: p.WorkspaceID, ActorType: "system", Surface: "job",
		Props: map[string]any{"status": status, "received": run.in, "sent": run.out, "chats": len(run.chats)}})
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE telegram_sessions SET session_sealed = COALESCE($3, session_sealed), synced_at = now(), sync_error = NULL,
			sync_lease_until = NULL, sync_status = CASE WHEN sync_status IN ('importing', 'live') THEN $4 ELSE sync_status END, updated_at = now()
			WHERE workspace_id = $1 AND account_id = $2`, p.WorkspaceID, p.AccountID, s.seal(session, p.AccountID), status); err != nil {
			return err
		}
		if err := EnqueueSync(ctx, tx, p.WorkspaceID, p.AccountID, next); err != nil {
			return err
		}
		// Chats nobody has matched yet go to matching.
		var pending bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM telegram_chats WHERE workspace_id = $1 AND account_id = $2 AND match_status = 'pending'
			AND kind IN ('unsorted', 'customer'))`, p.WorkspaceID, p.AccountID).Scan(&pending); err != nil || !pending {
			return err
		}
		return EnqueueMatch(ctx, tx, p.WorkspaceID, p.AccountID, time.Now())
	})
}

func (s *Service) syncFailed(ctx context.Context, p Payload, err error, release func(time.Time, string) error) error {
	code := codeOf(err)
	next := time.Now().Add(5 * time.Minute)
	var wait *WaitError
	if errors.As(err, &wait) {
		code, next = CodeSlowedDown, time.Now().Add(wait.For+5*time.Second)
	}
	metrics.TelegramSyncRun(code)
	track.Fail(ctx, s.Log, track.Event{Name: "telegram_account.sync_failed", WorkspaceID: p.WorkspaceID, ActorType: "system", Surface: "job",
		Props: map[string]any{"reason": code}}, "connector_unavailable", err)
	return release(next, code)
}

// signedOut records that Telegram ended the session.
func (s *Service) signedOut(ctx context.Context, p Payload) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE telegram_sessions SET status = 'signed_out', session_sealed = NULL, error_code = $3, sync_error = $3,
			sync_lease_until = NULL, last_checked_at = now(), updated_at = now()
			WHERE workspace_id = $1 AND account_id = $2`, p.WorkspaceID, p.AccountID, CodeSignedOut); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: p.WorkspaceID, ActorType: "system", Action: "telegram_account.signed_out",
			TargetType: "staff_account", TargetID: p.AccountID, Data: map[string]any{"by": "telegram"}})
	})
}

func (r *syncRun) load(ctx context.Context) error {
	rows, err := r.s.Pool.Query(ctx, `SELECT id::text, kind, peer_kind, peer_id, last_message_id, oldest_message_id, history_done, details_sealed
		FROM telegram_chats WHERE workspace_id = $1 AND account_id = $2 AND brand_id = $3`, r.p.WorkspaceID, r.p.AccountID, r.brand)
	if err != nil {
		return err
	}
	r.chats = map[string]*chatState{}
	for rows.Next() {
		c := &chatState{}
		if err := rows.Scan(&c.id, &c.kind, &c.peer.Kind, &c.peer.ID, &c.last, &c.oldest, &c.done, &c.details); err != nil {
			rows.Close()
			return err
		}
		r.chats[peerID(c.peer.Kind, c.peer.ID)] = c
	}
	return rows.Err()
}

// colleagues are the Telegram users of the workspace's other company
// accounts: chats with them are internal.
func (r *syncRun) colleagues(ctx context.Context) (map[int64]bool, error) {
	rows, err := r.s.Pool.Query(ctx, `SELECT telegram_user_id FROM telegram_sessions
		WHERE workspace_id = $1 AND account_id <> $2 AND telegram_user_id IS NOT NULL`, r.p.WorkspaceID, r.p.AccountID)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	out := map[int64]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, err
}

// list reads the chat list, newest first, and keeps each chat's details.
func (r *syncRun) list(ctx context.Context, rd Reader, colleagues map[int64]bool) error {
	unchanged := 0
	return rd.Dialogs(ctx, func(d Dialog) (bool, error) {
		if d.Self || d.Bot || d.Broadcast || d.TopMessage == 0 {
			return true, nil
		}
		key := peerID(d.Peer.Kind, d.Peer.ID)
		c, known := r.chats[key]
		if known && d.TopMessage <= c.last {
			unchanged++
			return unchanged < unchangedStop, nil
		}
		unchanged = 0
		kind := "unsorted"
		if d.Peer.Kind == "user" && colleagues[d.Peer.ID] {
			kind = "internal"
		}
		details := ChatDetails{Title: d.Title, Username: d.Username, Phone: d.Phone, AccessHash: d.Peer.AccessHash, Members: d.Members}
		if !known {
			c = &chatState{id: newID(), kind: kind, peer: d.Peer}
			if _, err := r.s.Pool.Exec(ctx, `INSERT INTO telegram_chats (id, workspace_id, brand_id, account_id, peer_kind, peer_id, details_sealed, kind)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT (account_id, peer_kind, peer_id) DO NOTHING`,
				c.id, r.p.WorkspaceID, r.brand, r.p.AccountID, d.Peer.Kind, d.Peer.ID, r.s.sealJSON(details, c.id), kind); err != nil {
				return false, err
			}
			r.chats[key] = c
		} else {
			if kind == "internal" && c.kind == "unsorted" {
				c.kind = kind
			}
			if _, err := r.s.Pool.Exec(ctx, `UPDATE telegram_chats SET details_sealed = $3, kind = $4, updated_at = now() WHERE workspace_id = $1 AND id = $2`,
				r.p.WorkspaceID, c.id, r.s.sealJSON(details, c.id), c.kind); err != nil {
				return false, err
			}
		}
		c.peer = d.Peer
		c.fresh, c.top = true, d.TopMessage
		return true, nil
	})
}

func (c *chatState) copies() bool { return c.kind != "internal" && c.kind != "personal" }

// peerFor returns a chat's address, from this run's chat list or the
// details kept from an earlier one.
func (r *syncRun) peerFor(c *chatState) (Peer, error) {
	if c.peer.AccessHash != 0 || c.peer.Kind == "group" || c.fresh {
		return c.peer, nil
	}
	d, err := r.s.OpenChat(c.id, c.details)
	if err != nil {
		return Peer{}, err
	}
	c.peer.AccessHash = d.AccessHash
	return c.peer, nil
}

// copyNew copies messages newer than the last one copied, in chats the
// import has started on.
func (r *syncRun) copyNew(ctx context.Context, rd Reader) error {
	for _, c := range r.chats {
		if !c.fresh || !c.copies() || c.last == 0 {
			continue
		}
		peer, err := r.peerFor(c)
		if err != nil {
			return err
		}
		offset := 0
		for page := 0; page < newPages; page++ {
			msgs, err := rd.History(ctx, peer, offset, c.last, pageSize)
			if err != nil {
				return err
			}
			if err := r.store(ctx, c, msgs); err != nil {
				return err
			}
			if len(msgs) < pageSize {
				break
			}
			offset = msgs[len(msgs)-1].ID
		}
	}
	return nil
}

// copyHistory copies older messages back to the cutoff, newest chats
// first, until the budget runs out. It reports whether any is left.
func (r *syncRun) copyHistory(ctx context.Context, rd Reader) (bool, error) {
	var todo []*chatState
	for _, c := range r.chats {
		if !c.done && c.copies() {
			todo = append(todo, c)
		}
	}
	// Chats in this run's list first (they are the most recent).
	for pass := 0; pass < 2; pass++ {
		for _, c := range todo {
			if c.done || c.fresh != (pass == 0) {
				continue
			}
			if r.budget <= 0 {
				return true, nil
			}
			peer, err := r.peerFor(c)
			if err != nil {
				return false, err
			}
			for !c.done && r.budget > 0 {
				msgs, err := rd.History(ctx, peer, c.oldest, 0, pageSize)
				if err != nil {
					return false, err
				}
				var keep []Message
				reached := len(msgs) < pageSize
				for _, m := range msgs {
					if m.At.Before(r.cutoff) {
						reached = true
						continue
					}
					keep = append(keep, m)
				}
				if err := r.store(ctx, c, keep); err != nil {
					return false, err
				}
				r.budget -= len(msgs)
				if len(msgs) > 0 && c.oldest == 0 || len(msgs) > 0 && msgs[len(msgs)-1].ID < c.oldest {
					c.oldest = msgs[len(msgs)-1].ID
				}
				if reached {
					c.done = true
				}
				if _, err := r.s.Pool.Exec(ctx, `UPDATE telegram_chats SET oldest_message_id = $3, history_done = $4,
					last_message_id = GREATEST(last_message_id, $5), updated_at = now() WHERE workspace_id = $1 AND id = $2`,
					r.p.WorkspaceID, c.id, c.oldest, c.done, c.last); err != nil {
					return false, err
				}
			}
		}
	}
	for _, c := range todo {
		if !c.done {
			return true, nil
		}
	}
	return false, nil
}

// store keeps a page of messages: new ones are added, edited ones are
// replaced.
func (r *syncRun) store(ctx context.Context, c *chatState, msgs []Message) error {
	if len(msgs) == 0 {
		return nil
	}
	ids := make([]int32, len(msgs))
	for i, m := range msgs {
		ids[i] = int32(m.ID)
	}
	rows, err := r.s.Pool.Query(ctx, `SELECT message_id, id::text, edited_at FROM telegram_messages WHERE chat_id = $1 AND message_id = ANY($2)`, c.id, ids)
	if err != nil {
		return err
	}
	type have struct {
		id     string
		edited *time.Time
	}
	known := map[int]have{}
	for rows.Next() {
		var mid int
		var h have
		if err := rows.Scan(&mid, &h.id, &h.edited); err != nil {
			rows.Close()
			return err
		}
		known[mid] = h
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, r.s.Pool, func(tx pgx.Tx) error {
		added := 0
		var newest *time.Time
		top := c.last
		for _, m := range msgs {
			if m.ID > top {
				top = m.ID
			}
			content := MessageContent{Text: m.Text, FromName: m.FromName, Media: m.Media, FileName: m.FileName, ReplyTo: m.ReplyTo, Service: m.Service}
			if h, ok := known[m.ID]; ok {
				if m.EditedAt != nil && (h.edited == nil || m.EditedAt.After(*h.edited)) {
					if _, err := tx.Exec(ctx, `UPDATE telegram_messages SET content_sealed = $2, edited_at = $3 WHERE id = $1`,
						h.id, r.s.sealJSON(content, h.id), m.EditedAt); err != nil {
						return err
					}
				}
				continue
			}
			id := newID()
			var sender *int64
			if m.From != 0 {
				sender = &m.From
			}
			if _, err := tx.Exec(ctx, `INSERT INTO telegram_messages (id, workspace_id, brand_id, chat_id, message_id, outgoing, sender_id, content_sealed, sent_at, edited_at, service)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) ON CONFLICT (chat_id, message_id) DO NOTHING`,
				id, r.p.WorkspaceID, r.brand, c.id, m.ID, m.Out, sender, r.s.sealJSON(content, id), m.At, m.EditedAt, m.Service); err != nil {
				return err
			}
			added++
			if m.Out {
				r.out++
			} else {
				r.in++
			}
			if newest == nil || m.At.After(*newest) {
				at := m.At
				newest = &at
			}
		}
		c.last = top
		if _, err := tx.Exec(ctx, `UPDATE telegram_chats SET message_count = message_count + $3, last_message_id = GREATEST(last_message_id, $4),
			last_message_at = GREATEST(last_message_at, $5), updated_at = now() WHERE workspace_id = $1 AND id = $2`,
			r.p.WorkspaceID, c.id, added, top, newest); err != nil {
			return err
		}
		if added == 0 {
			return nil
		}
		return Waiting(ctx, tx, r.p.WorkspaceID, c.id)
	})
}
