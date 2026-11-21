package api

// The brand's and its competitors' own channels. A video a competitor posts
// on its own YouTube channel names it, but it isn't a mention: the listener
// looks for other people naming the brand or its competitors. Each owner's
// known profiles (the brand's links, each competitor's profiles on the
// Competitors page) and its name tell its channels apart. When a video turns
// out to be on a competitor's channel that isn't among its profiles yet, the
// channel is suggested there, so the Competitors page learns it.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/mentions"
	"github.com/outflareai/outflare/engine/internal/sociallinks"
)

// mentionOwners reads the brand and its competitors (all but dismissed
// ones) with their known profiles.
func (s *Server) mentionOwners(ctx context.Context, q ownersQuerier, ws, brand string) ([]mentions.Owner, error) {
	var name, website string
	var links []byte
	if err := q.QueryRow(ctx, `SELECT name, website, links FROM brands WHERE workspace_id = $1 AND id = $2`, ws, brand).Scan(&name, &website, &links); err != nil {
		return nil, err
	}
	own := mentions.Owner{About: "brand", Name: name, Host: website}
	var brandLinks []struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(links, &brandLinks)
	for _, l := range brandLinks {
		own.Profiles = append(own.Profiles, l.URL)
	}
	out := []mentions.Owner{own}
	rows, err := q.Query(ctx, `SELECT id::text, name, host, links FROM seo_competitors
		WHERE workspace_id = $1 AND brand_id = $2 AND status <> 'dismissed'`, ws, brand)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var o mentions.Owner
		var cl []sociallinks.CompetitorLink
		if err := rows.Scan(&o.CompetitorID, &o.Name, &o.Host, &cl); err != nil {
			return nil, err
		}
		o.About = "competitor"
		for _, l := range cl {
			if l.State != sociallinks.Dismissed {
				o.Profiles = append(o.Profiles, l.URL)
			}
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ownersQuerier is a pool or a transaction.
type ownersQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// suggestCompetitorChannel adds a channel found to be a competitor's own to
// its profiles on the Competitors page, as a suggestion a person confirms
// or dismisses, unless it is there already.
func suggestCompetitorChannel(ctx context.Context, tx pgx.Tx, ws, competitor, channelURL string) (bool, error) {
	u := strings.TrimSpace(channelURL)
	if u == "" {
		return false, nil
	}
	l, ok := sociallinks.Clean(u, nil)
	if !ok {
		return false, nil
	}
	var list []sociallinks.CompetitorLink
	if err := tx.QueryRow(ctx, `SELECT links FROM seo_competitors WHERE workspace_id = $1 AND id = $2 FOR UPDATE`, ws, competitor).Scan(&list); err != nil {
		return false, err
	}
	key := mentions.ProfileKey(l.URL)
	for _, x := range list {
		if mentions.ProfileKey(x.URL) == key {
			return false, nil
		}
	}
	list = append(list, sociallinks.CompetitorLink{Kind: l.Kind, URL: l.URL, State: sociallinks.Suggested, Source: sociallinks.FromMentions})
	_, err := tx.Exec(ctx, `UPDATE seo_competitors SET links = $3, updated_at = now() WHERE workspace_id = $1 AND id = $2`, ws, competitor, list)
	return err == nil, err
}
