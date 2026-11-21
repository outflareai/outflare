package api

// Mentions on YouTube. A scan with YouTube on searches the web for videos
// that name the brand or a tracked competitor, through the workspace's own
// web search connection (Geonode), and keeps each video it finds. A job
// then reads the video's watch page and captions through the workspace's
// own page reading connection, so the mention says who made the video, how
// many saw it, what was said around the name and the second it was said.
// Searches and reads cost the workspace's own provider tokens, booked as
// data spend; a spent usage limit stops them.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/mentions"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/outcomes"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/usagelimits"
)

const (
	jobMentionsVideo = "mentions.video"
	// At most this many names a scan searches YouTube for (the brand
	// first, then competitors).
	mentionVideoSearches = 12
	// At most this many new videos a scan queues to read.
	mentionVideoReads = 20
	// A video's read is tried this many times before it is set aside.
	mentionVideoTries = 3
)

// mentionsFromYouTube searches for videos naming each subject, keeps each
// video found, queues reading the new ones, and returns the mentions in
// their titles and descriptions. errCode is set when searching failed (no
// connection, a spent limit, a provider error); what was found before it
// is still returned.
func (s *Server) mentionsFromYouTube(ctx context.Context, ws, brand string, last *time.Time, subjects []mentions.Subject) (found []foundMention, searches int, errCode string, err error) {
	if s.integrations == nil {
		return nil, 0, "connector_needed", nil
	}
	o, err := s.integrations.OpenCapable(ctx, s.pool, ws, brand, connectors.CapWebSearch)
	if errors.Is(err, connectors.ErrNoCapable) {
		return nil, 0, "connector_needed", nil
	}
	if err != nil {
		return nil, 0, "connector_" + connectors.KindOf(err), nil
	}
	searcher, ok := o.Connector.(connectors.WebSearcher)
	if !ok {
		return nil, 0, "connector_needed", nil
	}
	timeRange := "month"
	if last != nil && time.Since(*last) < 6*24*time.Hour {
		timeRange = "week"
	}
	type video struct {
		id, title, snippet, query string
	}
	var videos []video
	seen := map[string]bool{}
	for i, sub := range subjects {
		if i >= mentionVideoSearches || len(sub.Terms) == 0 {
			break
		}
		if lerr := usagelimits.Check(ctx, s.pool, ws, o.ID, "", 0); lerr != nil {
			var le *usagelimits.Error
			if errors.As(lerr, &le) {
				metrics.MentionVideoSearch("usage_limit_reached")
				errCode = "usage_limit_reached"
				break
			}
			return nil, searches, "", lerr
		}
		q := mentions.YouTubeQuery(sub.Terms[0])
		res, serr := searcher.WebSearch(ctx, o.Client, o.Creds, connectors.WebQuery{Query: q, TimeRange: timeRange})
		searches++
		if berr := s.bookMentionCost(ctx, ws, brand, o, "mentions_search", "Mentions: YouTube search", res.Cost); berr != nil {
			return nil, searches, "", berr
		}
		if serr != nil {
			metrics.MentionVideoSearch("connector_error")
			errCode = "connector_" + connectors.KindOf(serr)
			track.Fail(ctx, s.log, track.Event{Name: "brand_mentions.video_search_failed", WorkspaceID: ws, ActorType: "system", Surface: "job",
				Props: map[string]any{"brand_id": brand}}, errCode, serr)
			break
		}
		metrics.MentionVideoSearch("success")
		for _, r := range res.Results {
			id := mentions.VideoID(r.URL)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			videos = append(videos, video{id: id, title: r.Title, snippet: r.Snippet, query: sub.Terms[0]})
		}
	}
	if len(videos) == 0 {
		return nil, searches, errCode, nil
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		queued := 0
		for _, v := range videos {
			var id string
			var inserted bool
			if err := tx.QueryRow(ctx, `INSERT INTO mention_videos (workspace_id, brand_id, video_id, title, found_by, snippet)
				VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT (brand_id, video_id) DO UPDATE SET updated_at = now()
				RETURNING id::text, xmax = 0`, ws, brand, v.id, truncRunes(v.title, 500), truncRunes(v.query, 200), truncRunes(v.snippet, 2000)).
				Scan(&id, &inserted); err != nil {
				return err
			}
			if inserted && queued < mentionVideoReads {
				queued++
				if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobMentionsVideo, WorkspaceID: &ws, Payload: map[string]string{"video_id": id},
					IdempotencyKey: "mentions.video:" + id, MaxAttempts: mentionVideoTries}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, searches, "", err
	}
	for _, v := range videos {
		for _, h := range mentions.Find(v.title+"\n\n"+v.snippet, subjects) {
			found = append(found, foundMention{source: "youtube", externalID: v.id, url: mentions.WatchURL(v.id), title: v.title, quote: h.Quote,
				about: h.Subject.About, competitorID: subjectCompetitor(h.Subject), matched: h.Matched, origin: "video_search"})
		}
	}
	return found, searches, errCode, nil
}

// queueFoundVideos queues reading the videos other sources found (Presence
// sees a video naming a competitor, but not whose channel it is or what was
// said), so each gets its channel, views and the second the name was said,
// like videos found by searching. Up to mentionVideoReads a scan.
func (s *Server) queueFoundVideos(ctx context.Context, ws, brand string) error {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT external_id, url, title FROM brand_mentions m
		WHERE m.workspace_id = $1 AND m.brand_id = $2 AND m.source = 'youtube' AND m.origin <> 'video_search' AND m.author_url = ''
		LIMIT 200`, ws, brand)
	if err != nil {
		return err
	}
	type found struct{ id, title string }
	var videos []found
	seen := map[string]bool{}
	for rows.Next() {
		var ext, u, title string
		if err := rows.Scan(&ext, &u, &title); err != nil {
			rows.Close()
			return err
		}
		id := mentions.VideoID(u)
		if id == "" {
			id = mentions.VideoID(mentions.WatchURL(ext))
		}
		if id != "" && !seen[id] {
			seen[id] = true
			videos = append(videos, found{id, strings.TrimSuffix(title, " - YouTube")})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		queued := 0
		for _, v := range videos {
			if queued >= mentionVideoReads {
				break
			}
			var id string
			err := tx.QueryRow(ctx, `INSERT INTO mention_videos (workspace_id, brand_id, video_id, title, found_by)
				VALUES ($1, $2, $3, $4, 'presence') ON CONFLICT (brand_id, video_id) DO NOTHING RETURNING id::text`,
				ws, brand, v.id, truncRunes(v.title, 500)).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			queued++
			if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobMentionsVideo, WorkspaceID: &ws, Payload: map[string]string{"video_id": id},
				IdempotencyKey: "mentions.video:" + id, MaxAttempts: mentionVideoTries}); err != nil {
				return err
			}
		}
		return nil
	})
}

// bookMentionCost books what a search or a page read cost on the
// workspace's own provider account, as data spend for the brand.
func (s *Server) bookMentionCost(ctx context.Context, ws, brand string, o *connectors.Opened, kind, note string, cost connectors.SEOCost) error {
	if cost.USD <= 0 && cost.Units <= 0 {
		return nil
	}
	id, ref := o.ID, kind+":"+brand+":"+strconv.FormatInt(time.Now().UnixNano(), 10)
	b := brand
	e := outcomes.Entry{WorkspaceID: ws, Category: outcomes.CategoryData, Provider: o.Connector.Definition().ID, IntegrationID: &id,
		OccurredAt: time.Now(), Source: outcomes.SourceAuto, Ref: &ref, Note: note, BrandID: &b, Channel: outcomes.ChannelShared}
	if cost.Units > 0 {
		u := cost.Units
		e.Units, e.UnitName = &u, "token"
	} else {
		cents := int64(math.Ceil(cost.USD * 100))
		e.AmountCents, e.Currency = &cents, "USD"
	}
	if cost.Estimated {
		e.Note += ", estimated from the price list"
	}
	_, _, err := outcomes.Record(ctx, s.pool, e)
	return err
}

// mentionVideoJob reads one found video's watch page and captions.
func (s *Server) mentionVideoJob(ctx context.Context, job jobs.Job) error {
	ws, id, err := socialPayload(job, "video_id")
	if err != nil {
		return err
	}
	return s.ReadMentionVideo(ctx, ws, id)
}

// ReadMentionVideo reads a found video's watch page (who made it, how many
// saw it, when) and its captions, keeps them, and keeps each place the
// brand or a tracked competitor is named in what was said, with the second
// it was said. A mention already kept from the video's title gets the
// spoken quote and second. Failures the workspace can fix (no page
// reading connection, a spent usage limit) set the video aside; a
// provider outage is tried again.
func (s *Server) ReadMentionVideo(ctx context.Context, ws, id string) error {
	var brand, videoID, status string
	var tries int
	err := s.pool.QueryRow(ctx, `SELECT brand_id::text, video_id, captions_status, tries FROM mention_videos WHERE workspace_id = $1 AND id = $2`, ws, id).
		Scan(&brand, &videoID, &status, &tries)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != "waiting") {
		return nil
	}
	if err != nil {
		return err
	}
	ev := track.Event{Name: "mention_video.read", WorkspaceID: ws, ActorType: "system", Surface: "job", Props: map[string]any{"brand_id": brand}}
	setAside := func(outcome, code string, cause error) error {
		metrics.MentionVideoRead(outcome)
		final := code == "connector_needed" || code == "usage_limit_reached" || tries+1 >= mentionVideoTries
		next := "waiting"
		if final {
			next = "failed"
		}
		if _, err := s.pool.Exec(ctx, `UPDATE mention_videos SET captions_status = $3, tries = tries + 1, last_error_code = $4, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, ws, id, next, code); err != nil {
			return err
		}
		ev.Name = "mention_video.read_failed"
		track.Fail(ctx, s.log, ev, code, cause)
		if !final && (code == "page_unreadable" || strings.HasSuffix(code, connectors.ErrUnavailable) || strings.HasSuffix(code, connectors.ErrQuota)) {
			return cause // the job is tried again
		}
		return nil
	}
	if s.integrations == nil {
		return setAside("connector_needed", "connector_needed", errors.New("no page reading connection"))
	}
	o, err := s.integrations.OpenCapable(ctx, s.pool, ws, brand, connectors.CapCrawlFetch)
	if errors.Is(err, connectors.ErrNoCapable) {
		return setAside("connector_needed", "connector_needed", err)
	}
	if err != nil {
		return setAside("connector_error", "connector_"+connectors.KindOf(err), err)
	}
	fetcher, ok := o.Connector.(connectors.PageFetcher)
	if !ok {
		return setAside("connector_needed", "connector_needed", errors.New("the connection does not read pages"))
	}
	fetch := func(url, note string) (connectors.FetchedPage, error) {
		if lerr := usagelimits.Check(ctx, s.pool, ws, o.ID, "", 0); lerr != nil {
			return connectors.FetchedPage{}, lerr
		}
		page, ferr := fetcher.FetchPage(ctx, o.Client, o.Creds, url, false)
		if berr := s.bookMentionCost(ctx, ws, brand, o, "mentions_read", note, page.Cost); berr != nil {
			return page, berr
		}
		return page, ferr
	}
	failed := func(err error) error {
		var le *usagelimits.Error
		if errors.As(err, &le) {
			return setAside("usage_limit_reached", "usage_limit_reached", err)
		}
		var ce *connectors.Error
		if errors.As(err, &ce) {
			return setAside("connector_error", "connector_"+ce.Kind, err)
		}
		// The page itself failed (a bot check, a slow page): tried again.
		return setAside("connector_error", "page_unreadable", err)
	}
	page, err := fetch(mentions.WatchURL(videoID), "Mentions: YouTube video page")
	if err != nil {
		return failed(err)
	}
	v, ok := mentions.ParseWatch(page.HTML)
	if !ok {
		return setAside("internal", "internal", fmt.Errorf("no player data on the watch page (status %d)", page.Status))
	}
	var lines []mentions.Line
	captions := "none"
	if v.CaptionsURL != "" {
		cp, err := fetch(mentions.CaptionsJSONURL(v.CaptionsURL), "Mentions: YouTube captions")
		if err != nil {
			return failed(err)
		}
		lines = mentions.ParseCaptions(pageText(cp.HTML))
		if len(lines) > 0 {
			captions = "read"
		}
	}
	subjects, err := s.mentionSubjects(ctx, ws, brand)
	if err != nil {
		return err
	}
	owners, err := s.mentionOwners(ctx, s.pool, ws, brand)
	if err != nil {
		return err
	}
	if owner := mentions.OwnerOf(owners, v.Channel, v.ChannelURL); owner != nil {
		return s.keepOwnVideo(ctx, ws, brand, id, videoID, v, captions, owner, ev)
	}
	spoken := mentions.FindSpoken(lines, subjects)
	var linesJSON []byte
	if len(lines) > 0 {
		if linesJSON, err = json.Marshal(lines); err != nil {
			return err
		}
	}
	kept := 0
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE mention_videos SET title = CASE WHEN $3 = '' THEN title ELSE $3 END, channel_name = $4, channel_url = $5,
			views = $6, seconds = $7, published_at = $8, captions_status = $9, captions_lang = $10, captions = $11, tries = tries + 1,
			last_error_code = NULL, read_at = now(), updated_at = now() WHERE workspace_id = $1 AND id = $2`,
			ws, id, truncRunes(v.Title, 500), truncRunes(v.Channel, 200), truncRunes(v.ChannelURL, 2048), v.Views, v.Seconds, v.Published,
			captions, truncRunes(v.CaptionsLang, 20), linesJSON); err != nil {
			return err
		}
		// Mentions kept from the title learn who made the video.
		if _, err := tx.Exec(ctx, `UPDATE brand_mentions SET author_name = $4, author_url = $5, reach = $6, published_at = COALESCE(published_at, $7),
			title = CASE WHEN $8 = '' THEN title ELSE $8 END, updated_at = now()
			WHERE workspace_id = $1 AND brand_id = $2 AND source = 'youtube' AND (external_id = $3 OR (origin = 'presence' AND url LIKE '%' || $3 || '%'))`,
			ws, brand, videoID, truncRunes(v.Channel, 200), truncRunes(v.ChannelURL, 2048), v.Views, v.Published, truncRunes(v.Title, 500)); err != nil {
			return err
		}
		for _, h := range spoken {
			at := h.At
			score := mentions.Score(h.Subject.About, "youtube", v.Views, v.Published, time.Now())
			tag, err := tx.Exec(ctx, `INSERT INTO brand_mentions AS m (workspace_id, brand_id, source, external_id, url, title, quote, at_seconds, about,
				competitor_id, matched, author_name, author_url, reach, score, origin, origin_id, published_at)
				VALUES ($1, $2, 'youtube', $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 'video_search', $15, $16)
				ON CONFLICT (brand_id, source, external_id, COALESCE(competitor_id, '00000000-0000-0000-0000-000000000000'::uuid))
				DO UPDATE SET quote = excluded.quote, at_seconds = excluded.at_seconds, url = excluded.url, updated_at = now()
				WHERE m.at_seconds IS NULL
				RETURNING xmax = 0`,
				ws, brand, videoID, mentions.WatchURL(videoID)+"&t="+strconv.Itoa(at)+"s", truncRunes(v.Title, 500), truncRunes(h.Quote, 2000), at,
				h.Subject.About, subjectCompetitor(h.Subject), truncRunes(h.Matched, 200), truncRunes(v.Channel, 200), truncRunes(v.ChannelURL, 2048),
				v.Views, score, id, v.Published)
			if err != nil {
				return err
			}
			if tag.RowsAffected() > 0 {
				kept++
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	metrics.MentionVideoRead(captions)
	ev.Outcome = track.Success
	ev.Props["captions"] = captions
	ev.Props["lines"] = len(lines)
	ev.Props["spoken"] = len(spoken)
	ev.Props["kept"] = kept
	track.Emit(ctx, ev)
	return nil
}

var preText = regexp.MustCompile(`(?is)<pre[^>]*>(.*?)</pre>`)

// pageText is the text a page reader handed back for a non-HTML answer:
// as it came, or inside the <pre> a browser wraps it in.
func pageText(body string) string {
	if m := preText.FindStringSubmatch(body); m != nil {
		return html.UnescapeString(m[1])
	}
	if i := strings.Index(body, "<body"); i >= 0 {
		if j := strings.IndexByte(body[i:], '>'); j >= 0 {
			body = body[i+j+1:]
		}
		body, _, _ = strings.Cut(body, "</body>")
		return html.UnescapeString(body)
	}
	return body
}

// keepOwnVideo marks a video posted on the brand's own channel or a
// competitor's: it isn't a mention, so mentions kept from it that nobody
// has acted on are removed and later scans skip it. A competitor's channel
// that isn't among its profiles yet is suggested there.
func (s *Server) keepOwnVideo(ctx context.Context, ws, brand, id, videoID string, v mentions.Video, captions string, owner *mentions.Owner, ev track.Event) error {
	var competitor *string
	if owner.CompetitorID != "" {
		c := owner.CompetitorID
		competitor = &c
	}
	removed, suggested := int64(0), false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE mention_videos SET title = CASE WHEN $3 = '' THEN title ELSE $3 END, channel_name = $4, channel_url = $5,
			views = $6, seconds = $7, published_at = $8, captions_status = $9, own_about = $10, own_competitor_id = $11, tries = tries + 1,
			last_error_code = NULL, read_at = now(), updated_at = now() WHERE workspace_id = $1 AND id = $2`,
			ws, id, truncRunes(v.Title, 500), truncRunes(v.Channel, 200), truncRunes(v.ChannelURL, 2048), v.Views, v.Seconds, v.Published,
			captions, owner.About, competitor); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM brand_mentions WHERE workspace_id = $1 AND brand_id = $2 AND source = 'youtube' AND status = 'new'
			AND (external_id = $3 OR (origin = 'presence' AND url LIKE '%' || $3 || '%'))`, ws, brand, videoID)
		if err != nil {
			return err
		}
		removed = tag.RowsAffected()
		if competitor != nil {
			suggested, err = suggestCompetitorChannel(ctx, tx, ws, *competitor, v.ChannelURL)
		}
		return err
	})
	if err != nil {
		return err
	}
	metrics.MentionVideoRead("own_channel")
	ev.Outcome = track.Success
	ev.Props["own_channel"] = owner.About
	ev.Props["removed"] = removed
	ev.Props["channel_suggested"] = suggested
	track.Emit(ctx, ev)
	return nil
}
