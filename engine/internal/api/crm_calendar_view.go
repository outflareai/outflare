package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/crmmail"
)

// Meetings from members' calendars are read like their email: a person
// sees their own, those shared with them (who and when, or in full), and,
// under team sharing, those the AI check found to be about work. The
// filters run again on every read.

type calendarItem struct {
	ID         string     `json:"id"`
	OwnerID    string     `json:"owner_id"`
	RecordID   string     `json:"record_id"`
	StartsAt   time.Time  `json:"starts_at"`
	EndsAt     *time.Time `json:"ends_at"`
	Canceled   bool       `json:"canceled"`
	Visibility string     `json:"visibility"`
	Title      *string    `json:"title"`
	Guests     []string   `json:"guests"`
}

func (s *Server) listCRMMeetings(w http.ResponseWriter, r *http.Request) {
	if !s.crmMailPerson(w, r) {
		return
	}
	a := accessFrom(r.Context())
	rec := r.PathValue("record")
	if s.allocError(w, r, s.mailRecord(r.Context(), s.pool, a, rec)) {
		return
	}
	if s.box == nil {
		writeError(w, r, 503, "unavailable", "personal mail encryption is not configured")
		return
	}
	list, err := s.visibleCalendarEvents(r.Context(), a, []string{rec}, 100)
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, 200, map[string]any{"data": list})
}

// visibleCalendarEvents is the latest meetings on records the person may
// see, newest first.
func (s *Server) visibleCalendarEvents(ctx context.Context, a *access, records []string, limit int) ([]calendarItem, error) {
	teamShared := false
	if cfg, err := mailConfig(ctx, s.pool, "crm_mail_policy", a.WorkspaceID, a.Brand, "", ""); err != nil {
		return nil, err
	} else {
		raw, _ := json.Marshal(cfg)
		var p crmmail.Policy
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		teamShared = p.TeamSharingOn(time.Now())
	}
	rows, err := s.pool.Query(ctx, `SELECT c.id::text, c.owner_id::text, c.record_id::text, c.content_sealed, c.starts_at, c.ends_at, c.canceled, c.privacy
		FROM crm_calendar_events c JOIN memberships m ON m.workspace_id = c.workspace_id AND m.user_id = c.owner_id
		JOIN users u ON u.id = c.owner_id AND u.disabled_at IS NULL
		WHERE c.workspace_id = $1 AND c.brand_id = $2 AND c.record_id = ANY($3)
			AND (m.brand_ids IS NULL OR cardinality(m.brand_ids) = 0 OR $2::uuid = ANY(m.brand_ids))
		ORDER BY c.starts_at DESC LIMIT 500`, a.WorkspaceID, a.Brand, records)
	if err != nil {
		return nil, err
	}
	type row struct {
		id, owner, rec string
		sealed         []byte
		starts         time.Time
		ends           *time.Time
		canceled       bool
		privacy        string
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var v row
		return v, r.Scan(&v.id, &v.owner, &v.rec, &v.sealed, &v.starts, &v.ends, &v.canceled, &v.privacy)
	})
	if err != nil {
		return nil, err
	}
	type key struct{ owner, rec string }
	shares, teamDefault, contexts := map[key]crmmail.Sharing{}, map[key]bool{}, map[key]mailContext{}
	out := []calendarItem{}
	for _, v := range all {
		if len(out) == limit {
			break
		}
		k := key{v.owner, v.rec}
		share, ok := shares[k]
		if !ok {
			cfg, err := mailConfig(ctx, s.pool, "crm_mail_sharing", a.WorkspaceID, a.Brand, v.owner, v.rec)
			if err != nil {
				return nil, err
			}
			raw, _ := json.Marshal(cfg)
			if err := json.Unmarshal(raw, &share); err != nil {
				return nil, err
			}
			if chosen, _ := cfg["version"].(int); chosen == 0 && teamShared {
				share = crmmail.TeamShared()
				teamDefault[k] = true
			}
			shares[k] = share
		}
		visibility := crmmail.Visibility(v.owner, a.UserID, share)
		if visibility == "private" || (teamDefault[k] && v.owner != a.UserID && v.privacy != "work") {
			continue
		}
		plain, err := s.box.Open(v.sealed, calendarAAD(v.id))
		if err != nil {
			return nil, err
		}
		var e calendarEvent
		if err := json.Unmarshal(plain, &e); err != nil {
			return nil, err
		}
		c, ok := contexts[k]
		if !ok {
			if c, err = s.crmMailContext(ctx, a, v.owner, v.rec); err != nil {
				return nil, err
			}
			contexts[k] = c
		}
		if !c.eligible(crmmail.Message{OwnerEmail: firstOr(e.Guests), Participants: e.Guests}) {
			continue
		}
		item := calendarItem{ID: v.id, OwnerID: v.owner, RecordID: v.rec, StartsAt: v.starts, EndsAt: v.ends, Canceled: v.canceled, Visibility: visibility, Guests: []string{}}
		if visibility == "full" {
			title := e.Title
			item.Title, item.Guests = &title, e.Guests
		}
		out = append(out, item)
	}
	return out, nil
}

func firstOr(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[0]
}
