package api

// Mentions in podcasts. A scan with podcasts on searches a podcast
// directory (Podcast Index), on the workspace's own free connection, for
// episodes whose title, show notes or people name the brand or a tracked
// competitor, and keeps each episode. Show notes that name one are a
// mention straight away. When the show publishes a transcript in its feed,
// a job reads it from the show's own host, so the mention quotes what was
// said and the second it was said. The brand's and competitors' own shows
// are skipped. Audio is never downloaded or transcribed here.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/mentions"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/netguard"
	"github.com/outflareai/outflare/engine/internal/track"
)

const (
	jobMentionsEpisode = "mentions.episode"
	// At most this many names a scan searches podcasts for.
	mentionEpisodeSearches = 20
	// At most this many episodes one search returns.
	mentionEpisodesPerSearch = 40
	// At most this many transcripts a scan queues to read.
	mentionEpisodeReads = 30
	mentionEpisodeTries = 3
	// The largest transcript read.
	mentionTranscriptMax = 8 << 20
)

// mentionTranscriptHTTP reads transcripts from shows' own hosts: public
// files, read with no key, never from a private address.
var mentionTranscriptHTTP = netguard.Client(30 * time.Second)

// podcastSearchers lists the workspace's connections that search podcasts.
func (s *Server) podcastSearchers(ctx context.Context, q connectors.Querier, ws string) ([]string, error) {
	if s.integrations == nil {
		return nil, nil
	}
	var ids []string
	for _, d := range s.integrations.Registry.All() {
		if d.Has(connectors.CapPodcastSearch) {
			ids = append(ids, d.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, `SELECT id::text FROM integrations WHERE workspace_id = $1 AND status = 'connected' AND connector = ANY($2) ORDER BY created_at`, ws, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// mentionsFromPodcasts searches episodes naming each subject, keeps each
// episode found (skipping the brand's and competitors' own shows), queues
// reading the transcripts of new ones, and returns the mentions in their
// titles and show notes. errCode is set when searching failed.
func (s *Server) mentionsFromPodcasts(ctx context.Context, ws, brand string, subjects []mentions.Subject, owners []mentions.Owner) (found []foundMention, searches int, errCode string, err error) {
	if s.integrations == nil {
		return nil, 0, "connector_needed", nil
	}
	o, err := s.integrations.OpenCapable(ctx, s.pool, ws, brand, connectors.CapPodcastSearch)
	if errors.Is(err, connectors.ErrNoCapable) {
		return nil, 0, "connector_needed", nil
	}
	if err != nil {
		return nil, 0, "connector_" + connectors.KindOf(err), nil
	}
	searcher, ok := o.Connector.(connectors.PodcastSearcher)
	if !ok {
		return nil, 0, "connector_needed", nil
	}
	directory := o.Connector.Definition().ID
	type episode struct {
		connectors.PodcastEpisode
		query string
		owner *mentions.Owner
	}
	var episodes []episode
	seen := map[string]bool{}
	for _, sub := range subjects {
		for _, term := range sub.Terms {
			if searches >= mentionEpisodeSearches {
				break
			}
			res, serr := searcher.SearchPodcastEpisodes(ctx, o.Client, o.Creds, connectors.PodcastQuery{Term: term, Max: mentionEpisodesPerSearch})
			searches++
			if serr != nil {
				metrics.MentionPodcastSearch("connector_error")
				errCode = "connector_" + connectors.KindOf(serr)
				track.Fail(ctx, s.log, track.Event{Name: "brand_mentions.podcast_search_failed", WorkspaceID: ws, ActorType: "system", Surface: "job",
					Props: map[string]any{"brand_id": brand}}, errCode, serr)
				break
			}
			metrics.MentionPodcastSearch("success")
			for _, e := range res {
				if e.ID == "" || seen[e.ID] {
					continue
				}
				seen[e.ID] = true
				ep := episode{PodcastEpisode: e, query: term}
				ep.owner = mentions.OwnerOf(owners, e.ShowTitle, e.ShowLink)
				if ep.owner == nil && e.Author != "" {
					ep.owner = mentions.OwnerOf(owners, e.Author, "")
				}
				episodes = append(episodes, ep)
			}
		}
		if errCode != "" {
			break
		}
	}
	if len(episodes) == 0 {
		return nil, searches, errCode, nil
	}
	own := map[string]bool{}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		queued := 0
		for _, e := range episodes {
			status := "none"
			if e.TranscriptURL != "" && e.owner == nil {
				status = "waiting"
			}
			var ownAbout, ownCompetitor *string
			if e.owner != nil {
				a := e.owner.About
				ownAbout = &a
				if e.owner.CompetitorID != "" {
					c := e.owner.CompetitorID
					ownCompetitor = &c
				}
			}
			var id string
			var inserted bool
			var wasOwn bool
			if err := tx.QueryRow(ctx, `INSERT INTO mention_episodes AS e (workspace_id, brand_id, episode_id, directory, title, description, link, audio_url,
				seconds, published_at, show_id, show_title, show_link, show_image, author, found_by, transcript_url, transcript_type, transcript_status,
				own_about, own_competitor_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
				ON CONFLICT (brand_id, directory, episode_id) DO UPDATE SET updated_at = now()
				RETURNING id::text, xmax = 0, e.own_about IS NOT NULL`,
				ws, brand, truncRunes(e.ID, 100), directory, truncRunes(e.Title, 500), truncRunes(e.Description, 20000), httpsOnly(e.Link, 2048),
				httpsOnly(e.AudioURL, 2048), nonNegative(e.Seconds), e.Published, truncRunes(e.ShowID, 100), truncRunes(e.ShowTitle, 500),
				httpsOnly(e.ShowLink, 2048), httpsOnly(e.ShowImage, 2048), truncRunes(e.Author, 300), truncRunes(e.query, 200),
				httpsOnly(e.TranscriptURL, 2048), truncRunes(e.TranscriptType, 100), status, ownAbout, ownCompetitor).
				Scan(&id, &inserted, &wasOwn); err != nil {
				return err
			}
			if wasOwn || e.owner != nil {
				own[e.ID] = true
				continue
			}
			if inserted && status == "waiting" && queued < mentionEpisodeReads {
				queued++
				if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobMentionsEpisode, WorkspaceID: &ws, Payload: map[string]string{"episode_id": id},
					IdempotencyKey: "mentions.episode:" + id, MaxAttempts: mentionEpisodeTries}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, searches, "", err
	}
	for _, e := range episodes {
		if own[e.ID] {
			continue
		}
		notes := mentions.ParseTranscript(e.Description, "text/html")
		var text strings.Builder
		text.WriteString(e.Title)
		for _, l := range notes {
			text.WriteString("\n")
			text.WriteString(l.Text)
		}
		for _, h := range mentions.Find(text.String(), subjects) {
			found = append(found, foundMention{source: "podcast", externalID: directory + ":" + e.ID, url: firstNonEmpty(e.Link, e.ShowLink),
				title: e.Title, quote: h.Quote, about: h.Subject.About, competitorID: subjectCompetitor(h.Subject), matched: h.Matched,
				authorName: firstNonEmpty(e.ShowTitle, e.Author), authorURL: e.ShowLink, publishedAt: e.Published, origin: "podcast_search"})
		}
	}
	return found, searches, errCode, nil
}

// httpsOnly keeps an https address, cut to n characters, or "".
func httpsOnly(u string, n int) string {
	u = strings.TrimSpace(u)
	if !strings.HasPrefix(u, "https://") {
		return ""
	}
	return truncRunes(u, n)
}

func nonNegative(n int) *int {
	if n <= 0 {
		return nil
	}
	return &n
}

// mentionEpisodeJob reads one found episode's transcript.
func (s *Server) mentionEpisodeJob(ctx context.Context, job jobs.Job) error {
	ws, id, err := socialPayload(job, "episode_id")
	if err != nil {
		return err
	}
	return s.ReadMentionEpisode(ctx, ws, id)
}

// ReadMentionEpisode reads the transcript a found episode's show publishes,
// keeps it, and keeps each place the brand or a tracked competitor is
// named in it, with the second when the transcript is timed. A mention
// already kept from the show notes gets the spoken quote and second.
func (s *Server) ReadMentionEpisode(ctx context.Context, ws, id string) error {
	var brand, episodeID, directory, title, link, showTitle, showLink, transcriptURL, transcriptType, status string
	var published *time.Time
	var tries int
	err := s.pool.QueryRow(ctx, `SELECT brand_id::text, episode_id, directory, title, link, show_title, show_link, transcript_url, transcript_type,
		transcript_status, published_at, tries FROM mention_episodes WHERE workspace_id = $1 AND id = $2`, ws, id).
		Scan(&brand, &episodeID, &directory, &title, &link, &showTitle, &showLink, &transcriptURL, &transcriptType, &status, &published, &tries)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != "waiting") {
		return nil
	}
	if err != nil {
		return err
	}
	ev := track.Event{Name: "mention_episode.read", WorkspaceID: ws, ActorType: "system", Surface: "job", Props: map[string]any{"brand_id": brand}}
	setAside := func(code string, cause error) error {
		metrics.MentionEpisodeRead(code)
		final := tries+1 >= mentionEpisodeTries || code == "transcript_unreadable"
		next := "waiting"
		if final {
			next = "failed"
		}
		if _, err := s.pool.Exec(ctx, `UPDATE mention_episodes SET transcript_status = $3, tries = tries + 1, last_error_code = $4, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, ws, id, next, code); err != nil {
			return err
		}
		ev.Name = "mention_episode.read_failed"
		track.Fail(ctx, s.log, ev, code, cause)
		if !final {
			return cause
		}
		return nil
	}
	body, err := fetchTranscript(ctx, transcriptURL)
	if err != nil {
		code := "transcript_unavailable"
		var bad *badTranscript
		if errors.As(err, &bad) {
			code = "transcript_unreadable"
		}
		return setAside(code, err)
	}
	lines := mentions.ParseTranscript(body, transcriptType)
	if len(lines) == 0 {
		return setAside("transcript_unreadable", errors.New("the transcript has no lines"))
	}
	timed := false
	for _, l := range lines {
		if l.Start > 0 {
			timed = true
			break
		}
	}
	subjects, err := s.mentionSubjects(ctx, ws, brand)
	if err != nil {
		return err
	}
	spoken := mentions.FindSpoken(lines, subjects)
	linesJSON, err := json.Marshal(lines)
	if err != nil {
		return err
	}
	external := directory + ":" + episodeID
	url := firstNonEmpty(link, showLink)
	kept := 0
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE mention_episodes SET transcript_status = 'read', transcript = $3, tries = tries + 1, last_error_code = NULL,
			read_at = now(), updated_at = now() WHERE workspace_id = $1 AND id = $2`, ws, id, linesJSON); err != nil {
			return err
		}
		for _, h := range spoken {
			var at *int
			if timed {
				a := h.At
				at = &a
			}
			score := mentions.Score(h.Subject.About, "podcast", nil, published, time.Now())
			tag, err := tx.Exec(ctx, `INSERT INTO brand_mentions AS m (workspace_id, brand_id, source, external_id, url, title, quote, at_seconds, about,
				competitor_id, matched, author_name, author_url, score, origin, origin_id, published_at)
				VALUES ($1, $2, 'podcast', $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 'podcast_search', $14, $15)
				ON CONFLICT (brand_id, source, external_id, COALESCE(competitor_id, '00000000-0000-0000-0000-000000000000'::uuid))
				DO UPDATE SET quote = excluded.quote, at_seconds = excluded.at_seconds, updated_at = now()
				WHERE m.at_seconds IS NULL AND excluded.at_seconds IS NOT NULL
				RETURNING xmax = 0`,
				ws, brand, external, url, truncRunes(title, 500), truncRunes(h.Quote, 2000), at, h.Subject.About, subjectCompetitor(h.Subject),
				truncRunes(h.Matched, 200), truncRunes(showTitle, 200), showLink, score, id, published)
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
	metrics.MentionEpisodeRead("read")
	ev.Outcome = track.Success
	ev.Props["lines"] = len(lines)
	ev.Props["timed"] = timed
	ev.Props["spoken"] = len(spoken)
	ev.Props["kept"] = kept
	track.Emit(ctx, ev)
	return nil
}

type badTranscript struct{ status int }

func (b *badTranscript) Error() string {
	return fmt.Sprintf("the transcript answered HTTP %d", b.status)
}

// fetchTranscript reads a transcript from the show's own host.
func fetchTranscript(ctx context.Context, u string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", &badTranscript{}
	}
	req.Header.Set("User-Agent", "Outflare")
	resp, err := mentionTranscriptHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusForbidden {
		return "", &badTranscript{status: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("the transcript answered HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, mentionTranscriptMax))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
