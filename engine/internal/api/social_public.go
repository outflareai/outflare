package api

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/sociallinks"
)

// Public profiles: the profiles in a brand's links (X, a LinkedIn company
// page, YouTube, Instagram, TikTok, Facebook, a subreddit, GitHub, a
// Discord server, Bluesky, Threads, Mastodon, DEV, Docker Hub), read from
// their public page or the channel's public API through the workspace's own
// page reader (Geonode), with no app of the channel's own. The page shows
// followers (and sometimes following and posts); posts and their numbers
// need the channel's own app, as X and LinkedIn have.

// publicAccounts are the profiles of one channel in the workspace's brand
// links, with the brand of each. A profile a channel's own connection
// already reads (an X app, a LinkedIn sign-in) is left to it.
func (s *Server) publicAccounts(ctx context.Context, ws, integrationID, channel string) ([]connectors.SocialAccount, map[string]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT b.id::text, l->>'url' FROM brands b, jsonb_array_elements(b.links) l
		WHERE b.workspace_id = $1 AND b.status = 'active' AND l->>'kind' = $2
		ORDER BY b.created_at`, ws, channel)
	if err != nil {
		return nil, nil, err
	}
	type link struct{ brand, url string }
	var links []link
	var l link
	if _, err := pgx.ForEachRow(rows, []any{&l.brand, &l.url}, func() error { links = append(links, l); return nil }); err != nil {
		return nil, nil, err
	}
	owned := map[string]bool{}
	rows, err = s.pool.Query(ctx, `
		SELECT lower(a.handle) FROM social_accounts a JOIN integrations i ON i.id = a.integration_id AND i.workspace_id = a.workspace_id
		WHERE a.workspace_id = $1 AND a.channel = $3 AND a.status <> 'disconnected' AND i.connector = $3 AND a.integration_id <> $2`, ws, integrationID, channel)
	if err != nil {
		return nil, nil, err
	}
	var h string
	if _, err := pgx.ForEachRow(rows, []any{&h}, func() error { owned[h] = true; return nil }); err != nil {
		return nil, nil, err
	}
	out := []connectors.SocialAccount{}
	brands := map[string]string{}
	for _, l := range links {
		p, ok := sociallinks.PublicProfile(channel, l.url)
		if !ok || owned[strings.ToLower(p.Handle)] {
			continue
		}
		if _, dup := brands[p.ID]; dup {
			continue
		}
		brands[p.ID] = l.brand
		kind := connectors.SocialProfile
		if p.Page {
			kind = connectors.SocialPage
		}
		out = append(out, connectors.SocialAccount{ExternalID: p.ID, Kind: kind, Handle: p.Handle, URL: p.URL})
	}
	return out, brands, nil
}

// publicProfiles reads one channel's public profile pages through a page
// reader.
type publicProfiles struct {
	channel  string
	fetch    connectors.PageFetcher
	accounts []connectors.SocialAccount
}

func (p publicProfiles) SocialAccounts(context.Context, *connectors.Client, connectors.Creds) ([]connectors.SocialAccount, error) {
	return p.accounts, nil
}

// SocialStats reads the profile page. A page with no follower count (a
// bot check, a login wall, a suspended account) is a refusal kept on the
// account and tried again the same day (retry.After), never a zero.
func (p publicProfiles) SocialStats(ctx context.Context, cl *connectors.Client, creds connectors.Creds, a connectors.SocialAccount) (connectors.SocialStats, error) {
	pub, ok := sociallinks.PublicProfile(p.channel, a.URL)
	if !ok {
		return connectors.SocialStats{}, readFailed(readInvalidAddress, "%s is not a profile Outflare can read", a.URL)
	}
	// Pages that draw their counts with scripts need them run; the JSON
	// answers (GitHub, Reddit, Discord) don't.
	render := !slices.Contains(sociallinks.JSONKinds, p.channel)
	page, err := p.fetch.FetchPage(ctx, cl, creds, pub.ReadURL, render)
	if err != nil {
		return connectors.SocialStats{}, err
	}
	if page.Status == 404 || page.Status == 410 {
		return connectors.SocialStats{}, readFailed(readNotFound, "there is no public profile at %s", pub.URL)
	}
	prof, ok := sociallinks.ReadProfile(p.channel, page.HTML)
	if !ok {
		if page.Blocked() {
			return connectors.SocialStats{}, readFailed(readBlocked, "the site showed a bot check instead of the profile; Outflare tries again later today")
		}
		return connectors.SocialStats{}, readFailed(readNoCount, "the profile page showed no follower count")
	}
	return connectors.SocialStats{Followers: prof.Followers, Following: prof.Following, Posts: prof.Posts, Name: prof.Name, AvatarURL: prof.AvatarURL}, nil
}

func (publicProfiles) SocialPosts(context.Context, *connectors.Client, connectors.Creds, connectors.SocialAccount, time.Time, int) ([]connectors.SocialOwnPost, error) {
	return nil, nil
}
