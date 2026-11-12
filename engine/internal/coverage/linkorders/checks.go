package linkorders

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/coverage"
	"github.com/outflareai/outflare/engine/internal/crawl"
)

// CheckEvidence is what coverage.check already stored on the placement
// observation. The hook copies it onto matching link orders.
type CheckEvidence struct {
	At          time.Time
	Health      string
	LinkPresent bool
	Links       []crawl.Link
}

// Conclusive reports whether the page read is evidence we can score
// live/target against. Network failures are not.
func (e CheckEvidence) Conclusive() bool {
	switch e.Health {
	case "up", "gone", "redirected", "blocked":
		return true
	default:
		return false
	}
}

// AppliedCheck is one order updated from a placement check.
type AppliedCheck struct {
	Order      *Order
	MarkedLost bool
}

// ApplyPlacementCheck writes last_check_* on published, paid, or verified
// orders for this placement. Two consecutive conclusive live/target
// failures move the order to lost. Network errors do not count as a
// failure and do not change last_check_live or last_check_target_match.
func ApplyPlacementCheck(ctx context.Context, tx pgx.Tx, workspaceID uuid.UUID, placementID string, ev CheckEvidence) ([]AppliedCheck, error) {
	pid, err := uuid.Parse(placementID)
	if err != nil {
		return nil, err
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	rows, err := tx.Query(ctx, `
		SELECT id, brand_id, target_url, status, last_check_at, last_check_live, last_check_target_match
		FROM coverage_link_orders
		WHERE workspace_id = $1 AND placement_id = $2
		  AND status IN ($3, $4, $5)
		FOR UPDATE`,
		workspaceID, pid, StatusPublished, StatusPaid, StatusVerified)
	if err != nil {
		return nil, err
	}
	type pending struct {
		id, brand uuid.UUID
		target    string
		status    string
		prevAt    *time.Time
		prevLive  *bool
		prevMatch *bool
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pending, error) {
		var v pending
		return v, row.Scan(&v.id, &v.brand, &v.target, &v.status, &v.prevAt, &v.prevLive, &v.prevMatch)
	})
	if err != nil {
		return nil, err
	}
	out := []AppliedCheck{}
	for _, p := range list {
		live, match, rel, anchor := scoreCheck(p.target, ev)
		if ev.Conclusive() {
			if _, err := tx.Exec(ctx, `
				UPDATE coverage_link_orders
				SET last_check_at = $2, last_check_live = $3, last_check_target_match = $4,
				    last_check_rel = $5, last_check_anchor = $6, updated_at = now()
				WHERE id = $1`, p.id, ev.At, live, match, rel, anchor); err != nil {
				return nil, err
			}
		} else if _, err := tx.Exec(ctx, `
			UPDATE coverage_link_orders
			SET last_check_at = $2, updated_at = now()
			WHERE id = $1`, p.id, ev.At); err != nil {
			return nil, err
		}
		failed := ev.Conclusive() && (!live || !match)
		prevFailed := p.prevAt != nil && p.prevLive != nil && p.prevMatch != nil && (!*p.prevLive || !*p.prevMatch)
		data := map[string]any{"live": live, "target_match": match, "health": ev.Health, "conclusive": ev.Conclusive()}
		if err := AppendEvent(ctx, tx, workspaceID, p.brand, p.id, uuid.Nil, "checked", &p.status, p.status, data); err != nil {
			return nil, err
		}
		applied := AppliedCheck{}
		if failed && prevFailed {
			note := "Two consecutive live-link checks failed"
			o, err := MarkLost(ctx, tx, workspaceID, p.brand, p.id, uuid.Nil, &note)
			if err != nil {
				return nil, err
			}
			applied.Order = o
			applied.MarkedLost = true
		} else {
			o, err := GetOrder(ctx, tx, workspaceID, p.brand, p.id)
			if err != nil {
				return nil, err
			}
			applied.Order = o
		}
		out = append(out, applied)
	}
	return out, nil
}

func scoreCheck(target string, ev CheckEvidence) (live, match bool, rel, anchor string) {
	if ev.Health != "up" {
		return false, false, "", ""
	}
	live = ev.LinkPresent || len(ev.Links) > 0
	var chosen *crawl.Link
	for i := range ev.Links {
		link := ev.Links[i]
		if urlsEquivalent(link.URL, target) {
			match = true
			chosen = &ev.Links[i]
			break
		}
	}
	if chosen == nil && len(ev.Links) > 0 {
		chosen = &ev.Links[0]
	}
	if chosen != nil {
		rel = strings.Join(chosen.Rel, " ")
		anchor = chosen.Anchor
	}
	return live, match, rel, anchor
}

func urlsEquivalent(a, b string) bool {
	ca, err1 := coverage.Canonical(a)
	cb, err2 := coverage.Canonical(b)
	if err1 == nil && err2 == nil {
		if ca == cb {
			return true
		}
		return stripURLSlash(ca) == stripURLSlash(cb)
	}
	return stripURLSlash(strings.ToLower(strings.TrimSpace(a))) == stripURLSlash(strings.ToLower(strings.TrimSpace(b)))
}

func stripURLSlash(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return strings.TrimRight(raw, "/")
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}
