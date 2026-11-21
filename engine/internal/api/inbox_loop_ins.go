package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/sysmail"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Telling a teammate they were looped in. A loop-in is new to them until
// they open the conversation (seen_at on the follower row). The Inbox page
// asks for the new ones and shows them on its Watching views; someone who
// hasn't opened it after loopInEmailAfter gets one account email with a
// link, and nothing about the customer in it.

const (
	loopInReply   = "reply"
	loopInSupport = "support"

	jobLoopInEmail = "inbox.loop_in_email"
)

// loopInEmailAfter is how long a loop-in may sit unopened before the
// email. A variable so tests don't wait.
var loopInEmailAfter = 5 * time.Minute

func (s *Server) routesInboxLoopIns(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/inbox-loop-ins", s.onBrand(auth.ScopeInboxRead, s.listInboxLoopIns))
}

type inboxLoopInView struct {
	// Kind is reply (a reply to outreach) or support (a support conversation).
	Kind    string         `json:"kind"`
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Subject string         `json:"subject"`
	By      *supportPerson `json:"by"`
	At      time.Time      `json:"at"`
}

// listInboxLoopIns is what the caller was looped in on and hasn't opened
// yet, newest first. An API token is nobody to loop in, so it gets none.
func (s *Server) listInboxLoopIns(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.inboxBrand(w, r)
	if !ok {
		return
	}
	list := []inboxLoopInView{}
	if a.UserID != "" {
		rows, err := s.pool.Query(r.Context(), `
			SELECT 'reply', i.id::text, COALESCE(NULLIF(trim(l.first_name || ' ' || l.last_name), ''), NULLIF(i.from_name, ''), i.from_email, ''),
				COALESCE(i.subject, ''), f.added_by::text, COALESCE(NULLIF(u.name, ''), u.email), f.created_at
			FROM inbox_followers f JOIN inbound_messages i ON i.id = f.inbound_id
			LEFT JOIN leads l ON l.id = i.lead_id LEFT JOIN users u ON u.id = f.added_by
			WHERE f.workspace_id = $1 AND f.brand_id = $2 AND f.user_id = $3 AND f.seen_at IS NULL
			UNION ALL
			SELECT 'support', c.id::text, COALESCE(NULLIF(rc.name, ''), c.contact_email, ''),
				COALESCE(c.subject, ''), f.added_by::text, COALESCE(NULLIF(u.name, ''), u.email), f.created_at
			FROM support_followers f JOIN support_conversations c ON c.id = f.conversation_id
			LEFT JOIN records rc ON rc.id = c.record_id LEFT JOIN users u ON u.id = f.added_by
			WHERE f.workspace_id = $1 AND f.brand_id = $2 AND f.user_id = $3 AND f.seen_at IS NULL
			ORDER BY 7 DESC, 2 LIMIT 50`, a.WorkspaceID, brand, a.UserID)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		list, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (inboxLoopInView, error) {
			var v inboxLoopInView
			var byID, byName *string
			err := row.Scan(&v.Kind, &v.ID, &v.Name, &v.Subject, &byID, &byName, &v.At)
			if byID != nil {
				v.By = &supportPerson{ID: *byID, Name: deref(byName)}
			}
			return v, err
		})
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if list == nil {
			list = []inboxLoopInView{}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"loop_ins": list})
}

// loopInTable is the follower table and its parent column for a kind.
func loopInTable(kind string) (table, column string) {
	if kind == loopInSupport {
		return "support_followers", "conversation_id"
	}
	return "inbox_followers", "inbound_id"
}

// sawLoopIn notes that user opened what they were looped in on, so it
// stops counting as new and no email follows.
func (s *Server) sawLoopIn(ctx context.Context, kind, ws, id, user string) {
	if user == "" {
		return
	}
	table, column := loopInTable(kind)
	if _, err := s.pool.Exec(ctx, `UPDATE `+table+` SET seen_at = now() WHERE `+column+` = $1 AND workspace_id = $2 AND user_id = $3 AND seen_at IS NULL`,
		id, ws, user); err != nil {
		s.log.Warn("loop-in not marked seen", "err", err)
	}
}

// tellLoopedIn queues the email for a teammate someone else looped in.
// Looping yourself in tells nobody.
func tellLoopedIn(ctx context.Context, tx pgx.Tx, ws, kind, id, user, by string) error {
	if user == by {
		return nil
	}
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobLoopInEmail, WorkspaceID: &ws, MaxAttempts: 3, RunAt: time.Now().Add(loopInEmailAfter),
		Payload:        map[string]any{"kind": kind, "id": id, "user_id": user},
		IdempotencyKey: jobLoopInEmail + ":" + id + ":" + user})
	return err
}

func (s *Server) loopInEmailJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		Kind   string `json:"kind"`
		ID     string `json:"id"`
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || !validUUID(p.ID) || !validUUID(p.UserID) ||
		(p.Kind != loopInReply && p.Kind != loopInSupport) {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	table, column := loopInTable(p.Kind)
	var email, workspace, handle string
	var by *string
	err := s.pool.QueryRow(ctx, `SELECT u.email, w.name, b.handle, COALESCE(NULLIF(ab.name, ''), ab.email)
		FROM `+table+` f JOIN users u ON u.id = f.user_id AND u.email_verified_at IS NOT NULL AND u.disabled_at IS NULL
		JOIN memberships m ON m.workspace_id = f.workspace_id AND m.user_id = f.user_id
		JOIN workspaces w ON w.id = f.workspace_id JOIN brands b ON b.id = f.brand_id
		LEFT JOIN users ab ON ab.id = f.added_by
		WHERE f.`+column+` = $1 AND f.workspace_id = $2 AND f.user_id = $3 AND f.seen_at IS NULL`, p.ID, ws, p.UserID).Scan(&email, &workspace, &handle, &by)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // opened already, taken off again, or no longer a member
	}
	if err != nil {
		return err
	}
	ev := track.Event{Name: "inbox.loop_in_emailed", WorkspaceID: ws, UserID: p.UserID, ActorType: "system", Surface: "job",
		Props: map[string]any{"kind": p.Kind}}
	path := p.ID
	if p.Kind == loopInSupport {
		path = "support-" + p.ID
	}
	url := s.publicURL + "/" + s.workspaceSlug(ctx, ws) + "/" + handle + "/inbox/" + path
	if err := s.sendAccountEmail(ctx, loopInMessage(url, email, workspace, deref(by))); err != nil {
		track.Fail(ctx, s.log, ev, "email_failed", err)
		return err
	}
	track.Emit(ctx, ev)
	return nil
}

func loopInMessage(url, email, workspace, by string) sysmail.Message {
	who := "A teammate"
	if by != "" {
		who = by
	}
	return sysmail.Email{To: email,
		Subject: who + " looped you in on a conversation",
		Preview: who + " looped you in on a conversation in " + workspace + ".",
		Heading: "You were looped in on a conversation",
		Body: []string{who + " looped you in on a conversation in " + workspace + ", so you can follow it and add a private note.",
			"It is under Watching in your Inbox."},
		Button: "Open the conversation", URL: url,
		Note: "You get this once per conversation, and only when you haven't opened it within a few minutes.",
	}.Message()
}
