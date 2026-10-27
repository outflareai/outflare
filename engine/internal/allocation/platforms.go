package allocation

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/jobs"
)

// DefaultSequenceDays is how long a mailbox leaving a platform campaign
// stays in it when the campaign's length is unknown.
const DefaultSequenceDays = 14

// StaleAfter is how old a platform account's last good sync may be before
// every mailbox seen there counts as busy: missing data never means free.
const StaleAfter = 30 * time.Minute

// MinPlatformWarmup is the lowest platform warm-up health score, 0-100, at
// which a mailbox found only on a platform counts as ready.
const MinPlatformWarmup = 75

// loadPlatforms adds what connected platform accounts report: the links
// each sequence runs as, where each mailbox can send, and what keeps a
// mailbox busy outside Outflare's control.
func (st *State) loadPlatforms(ctx context.Context, tx pgx.Tx, now time.Time) error {
	type linkKey struct{ integration, external string }
	byExt := map[linkKey]Link{}
	linkSeq := map[string]string{}
	linkName := map[string]string{}
	seqs := map[string]*Sequence{}
	for i := range st.Input.Sequences {
		seqs[st.Input.Sequences[i].ID] = &st.Input.Sequences[i]
	}
	rows, err := tx.Query(ctx, `
		SELECT l.id::text, l.sequence_id::text, l.integration_id::text, l.external_id, l.external_name, l.control, i.name,
		       ps.reviewed_at IS NOT NULL
		FROM campaign_links l
		JOIN integrations i ON i.id = l.integration_id
		LEFT JOIN platform_syncs ps ON ps.integration_id = l.integration_id
		WHERE l.workspace_id = $1
		ORDER BY l.created_at, l.id`, st.WorkspaceID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var l Link
		var seqID, ext, name, control string
		var reviewed bool
		if err := rows.Scan(&l.ID, &seqID, &l.IntegrationID, &ext, &name, &control, &l.Platform, &reviewed); err != nil {
			rows.Close()
			return err
		}
		l.ReadOnly = control == "read_only"
		byExt[linkKey{l.IntegrationID, ext}] = l
		linkSeq[l.ID] = seqID
		linkName[l.ID] = name
		if s := seqs[seqID]; s != nil {
			s.Links = append(s.Links, l)
			if !reviewed {
				// Nothing moves in a newly connected account until a person
				// reviews and applies its first plan.
				st.Modes[seqID] = ModeSuggest
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	boxes := map[string]*Mailbox{}
	for i := range st.Input.Mailboxes {
		boxes[st.Input.Mailboxes[i].ID] = &st.Input.Mailboxes[i]
	}
	rows, err = tx.Query(ctx, `
		SELECT p.mailbox_id::text, p.integration_id::text, i.name, p.status, COALESCE(p.status_reason, ''), p.warmup_score,
		       p.campaign_ids, ps.synced_at, COALESCE(ps.error, ''), COALESCE(ps.control, 'full')
		FROM mailbox_platforms p
		JOIN integrations i ON i.id = p.integration_id
		LEFT JOIN platform_syncs ps ON ps.integration_id = p.integration_id
		WHERE p.workspace_id = $1
		ORDER BY p.mailbox_id, i.created_at`, st.WorkspaceID)
	if err != nil {
		return err
	}
	defer rows.Close()
	usable := map[string]bool{} // mailbox can send in at least one platform account
	scores := map[string]string{}
	for rows.Next() {
		var mailboxID, integration, platform, status, reason, syncErr, control string
		var score *int
		var campaigns []string
		var synced *time.Time
		if err := rows.Scan(&mailboxID, &integration, &platform, &status, &reason, &score, &campaigns, &synced, &syncErr, &control); err != nil {
			return err
		}
		m := boxes[mailboxID]
		if m == nil {
			continue
		}
		stale := synced == nil || now.Sub(*synced) > StaleAfter || syncErr != ""
		if m.Platforms == nil {
			m.Platforms = map[string]bool{}
		}
		ok := status == "ok" && !stale
		m.Platforms[integration] = ok
		if ok {
			usable[m.ID] = true
		}
		if score != nil && *score < MinPlatformWarmup {
			scores[m.ID] = fmt.Sprintf("warm-up health %d in %s", *score, platform)
		}
		a := m.Assignment
		// A watched-only platform (Apollo) just flags the mailbox: it never
		// takes it out of allocation, stale or not.
		if stale && control != "read_only" && m.Outside == "" {
			m.Outside = platform + " has not synced recently, so what it does there is unknown"
		}
		for _, ext := range campaigns {
			l, linked := byExt[linkKey{integration, ext}]
			if a != nil && linked && a.LinkID == l.ID {
				continue // where it is assigned
			}
			if linked {
				m.Outside = fmt.Sprintf("attached to %s in %s, which it is not assigned to", linkName[l.ID], platform)
			} else {
				m.Outside = "attached to a campaign in " + platform + " that Outflare has not linked"
			}
		}
		// An assignment through a platform needs that account to be able to
		// send from the mailbox.
		if a != nil && a.LinkID != "" && !ok {
			if l, found := findLink(seqs[linkSeq[a.LinkID]], a.LinkID); found && l.IntegrationID == integration {
				why := platform + " reports it as " + status
				if stale {
					why = platform + " has not synced recently"
				} else if reason != "" {
					why += ": " + reason
				}
				m.NotReady = why
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range st.Input.Mailboxes {
		m := &st.Input.Mailboxes[i]
		if !m.NoLogin || m.NotReady != "" {
			continue
		}
		switch {
		case !usable[m.ID]:
			m.NotReady = "no connected platform account can send from it"
		case scores[m.ID] != "":
			m.NotReady = scores[m.ID]
		}
	}
	return nil
}

func findLink(s *Sequence, id string) (Link, bool) {
	if s == nil {
		return Link{}, false
	}
	for _, l := range s.Links {
		if l.ID == id {
			return l, true
		}
	}
	return Link{}, false
}

// JobPlatform attaches or detaches one mailbox in a platform campaign. The
// platforms package handles it.
const JobPlatform = "platforms.apply"

// Platform operations.
const (
	OpAttach = "attach"
	OpDetach = "detach"
)

// PlatformOp is JobPlatform's payload.
type PlatformOp struct {
	WorkspaceID string `json:"workspace_id"`
	Op          string `json:"op"`
	MailboxID   string `json:"mailbox_id"`
	LinkID      string `json:"link_id"`
}

func enqueuePlatform(ctx context.Context, tx pgx.Tx, ws, op, mailboxID, linkID string) error {
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{
		Kind: JobPlatform, WorkspaceID: &ws, Payload: PlatformOp{ws, op, mailboxID, linkID}, MaxAttempts: 5,
	})
	return err
}
