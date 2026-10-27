package api

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/track"
)

// Outflare's own product events (every action and error, package track)
// can be kept in a workspace on this server, set by
// OUTFLARE_EVENTS_WORKSPACE: the operator's own, such as Outflare Cloud's
// "Outflare" workspace. They arrive as product events like any product's,
// so the people who use this server become customer records there, and
// the workspace's analytics, segments and lifecycle email work on them.
//
// Each person is identified once per process with their account's email
// and name, so their record carries them; events themselves keep to ids,
// codes and counts. Storing events records no events of its own.

// productEventSource is the source Outflare's own events are stored with.
const productEventSource = "outflare"

type productEventSink struct {
	s   *Server
	ws  string
	mu  sync.Mutex
	ids map[string]bool // people identified since start
}

// ProductEventSink returns the sink that stores product events in
// workspace ws. It fails when the workspace doesn't exist.
func (s *Server) ProductEventSink(ctx context.Context, ws string) (track.Sink, error) {
	var ok bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM workspaces WHERE id::text = $1)`, ws).Scan(&ok); err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("OUTFLARE_EVENTS_WORKSPACE names no workspace on this server")
	}
	return &productEventSink{s: s, ws: ws, ids: map[string]bool{}}, nil
}

func (p *productEventSink) Send(ctx context.Context, events []track.Event) {
	ctx = track.Mute(ctx)
	msgs, people := p.messages(events)
	err := pgx.BeginFunc(ctx, p.s.pool, func(tx pgx.Tx) error {
		ids, err := p.identities(ctx, tx, people)
		if err != nil {
			return err
		}
		msgs = append(ids, msgs...)
		c, err := loadCatalog(ctx, tx, p.ws, false)
		if err != nil {
			return err
		}
		a := &access{principal: principal{ActorType: "system"}, WorkspaceID: p.ws}
		totals := ingestTotals{names: map[string]*seenName{}}
		now := time.Now().UTC()
		rejected := 0
		for _, m := range msgs {
			sp, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			var res eventResult
			if err := p.s.ingestOne(ctx, sp, a, c, nil, productEventSource, nil, m, now, &res, &totals); err != nil {
				rejected++
				if rb := sp.Rollback(ctx); rb != nil {
					return rb
				}
				p.s.log.Debug("product event not stored", "err", err)
				continue
			}
			if err := sp.Commit(ctx); err != nil {
				return err
			}
		}
		if rejected > 0 {
			p.s.log.Warn("product events not stored", "count", rejected)
		}
		if err := saveEventNames(ctx, tx, p.ws, totals.names); err != nil {
			return err
		}
		// Outflare's code names these events, so they need no approval.
		names := make([]string, 0, len(totals.names))
		for n := range totals.names {
			names = append(names, n)
		}
		_, err = tx.Exec(ctx, `UPDATE event_definitions SET status = 'approved'
			WHERE workspace_id = $1 AND name = ANY($2) AND status = 'quarantined'`, p.ws, names)
		return err
	})
	if err != nil {
		p.s.log.Warn("product events: store", "err", err, "count", len(events))
		return
	}
	p.mu.Lock()
	for _, id := range people {
		p.ids[id] = true
	}
	p.mu.Unlock()
}

// messages turns events into track calls, and lists the people not
// identified yet.
func (p *productEventSink) messages(events []track.Event) ([]eventMessage, []string) {
	msgs := make([]eventMessage, 0, len(events))
	var people []string
	seen := map[string]bool{}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range events {
		props := make(map[string]any, len(e.Props)+8)
		for k, v := range e.Props {
			props[k] = v
		}
		props["outcome"] = e.Outcome
		set := func(k, v string) {
			if v != "" {
				props[k] = v
			}
		}
		set("error_code", e.ErrorCode)
		set("surface", e.Surface)
		set("actor_type", e.ActorType)
		set("actor_id", e.ActorID)
		set("workspace_id", e.WorkspaceID)
		set("request_id", e.RequestID)
		m := eventMessage{
			"type":       raw("track"),
			"event":      raw(e.Name),
			"timestamp":  raw(e.At.UTC().Format(time.RFC3339Nano)),
			"properties": raw(props),
			"context":    raw(map[string]any{"library": map[string]string{"name": "outflare-engine", "version": Version}}),
		}
		switch {
		case e.UserID != "":
			m["userId"] = raw(e.UserID)
			if !p.ids[e.UserID] && !seen[e.UserID] {
				seen[e.UserID] = true
				people = append(people, e.UserID)
			}
		case e.ActorID != "":
			// Work with no person behind it (a system job, a token whose
			// maker left) makes no person.
			m["anonymousId"] = raw("actor:" + e.ActorID)
		case e.WorkspaceID != "":
			m["anonymousId"] = raw("workspace:" + e.WorkspaceID)
		default:
			m["anonymousId"] = raw("system")
		}
		msgs = append(msgs, m)
	}
	return msgs, people
}

// identities are identify calls for people seen for the first time: their
// account's email (verified once they confirmed it) and name.
func (p *productEventSink) identities(ctx context.Context, tx pgx.Tx, people []string) ([]eventMessage, error) {
	if len(people) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text, email, name, email_verified_at IS NOT NULL FROM users WHERE id::text = ANY($1)`, people)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []eventMessage
	for rows.Next() {
		var id, email, name string
		var verified bool
		if err := rows.Scan(&id, &email, &name, &verified); err != nil {
			return nil, err
		}
		traits := map[string]any{"email": email, "email_verified": verified}
		if name != "" {
			traits["name"] = name
		}
		out = append(out, eventMessage{"type": raw("identify"), "userId": raw(id), "traits": raw(traits)})
	}
	return out, rows.Err()
}

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
