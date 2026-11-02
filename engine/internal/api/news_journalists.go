package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/news"
)

// Journalists: people on customer records who write for an outlet. The
// news watch finds them (the authors of articles it read whose address the
// email finder found at the outlet), and people add the ones they know, by
// hand or from a file. Each is a person record marked by the catalog's
// journalist field, on their outlets' company records, with the topics they
// cover in beats and each outlet's tier on its company. This lists them
// with what they wrote, where, and which searches their articles came up
// in, so a media list is a list of these records. Reads only what is
// stored, so it costs nothing.

type journalistTopic struct {
	SearchID string `json:"search_id"`
	Name     string `json:"name"`
}

type journalistArticle struct {
	ID          string     `json:"id"`
	URL         string     `json:"url"`
	Title       string     `json:"title"`
	PublishedAt *time.Time `json:"published_at"`
}

type journalistOutlet struct {
	RecordID string  `json:"record_id"`
	Name     string  `json:"name"`
	Domain   *string `json:"domain"`
	Tier     *string `json:"tier"`
}

type journalistView struct {
	RecordID string  `json:"record_id"`
	BrandID  *string `json:"brand_id"`
	// False only for one a person unmarked, as a write returns it.
	Journalist bool    `json:"journalist"`
	Name       string  `json:"name"`
	Email      *string `json:"email"`
	JobTitle   *string `json:"job_title"`
	Confidence int     `json:"confidence"`
	// The topics they cover, and the names of the topic searches their
	// articles came up in that aren't among them yet.
	Beats          []string           `json:"beats"`
	SuggestedBeats []string           `json:"suggested_beats"`
	Outlets        []string           `json:"outlets"`
	OutletRecords  []journalistOutlet `json:"outlet_records"`
	Articles       int                `json:"articles"`
	LatestArticle  *journalistArticle `json:"latest_article"`
	Topics         []journalistTopic  `json:"topics"`
}

// journalistFilter narrows the list.
type journalistFilter struct {
	searchID string
	since    *time.Time
	beat     string
	tiers    []string
	// only is one record, the caller's own write, shown whatever else.
	only string
}

// notUnmarked keeps out of records r the people a person unmarked as a
// journalist: the news watch may have found them, but they said no.
const notUnmarked = `NOT r.attributes @> '{"journalist": false}'`

func (s *Server) listJournalists(w http.ResponseWriter, r *http.Request) {
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var f journalistFilter
	if v := q.Get("search_id"); v != "" {
		if !validUUID(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "search_id is a news search's id")
			return
		}
		f.searchID = v
	}
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "since is a time like 2026-10-28T00:00:00Z")
			return
		}
		f.since = &t
	}
	if v := q.Get("beat"); v != "" {
		if f.beat = news.CleanBeat(v); f.beat == "" || len(v) > 200 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "beat is a topic a journalist covers, at most 100 characters")
			return
		}
	}
	if v := q.Get("tier"); v != "" {
		for _, t := range strings.Split(v, ",") {
			if t = strings.TrimSpace(t); !slices.Contains(news.Tiers, t) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "tier is national, trade, regional, local or newsletter, or several separated by commas")
				return
			}
			f.tiers = append(f.tiers, strings.TrimSpace(t))
		}
	}
	limit, err := limitParam(r, 50, 200)
	if s.allocError(w, r, err) {
		return
	}
	offset, ok := pageOffsetParam(w, r)
	if !ok {
		return
	}
	var list []journalistView
	var total int
	err = s.readTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		list, total, err = queryJournalists(r.Context(), tx, accessFrom(r.Context()).WorkspaceID, sel, f, limit, offset)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "total": total, "next_cursor": nextCursor(pageOffset, nextOffset(offset, len(list), total))})
}

// queryJournalists reads one page of journalists and how many there are.
// A journalist shows through the articles the caller sees (those found by a
// search the caller sees, or by one search), or, with no search or time
// asked for, through their record being in a brand the caller sees.
func queryJournalists(ctx context.Context, tx pgx.Tx, ws string, sel brandSel, f journalistFilter, limit, offset int) ([]journalistView, int, error) {
	args := []any{ws}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	hits := `h.article_id = x.id`
	if f.searchID != "" {
		hits += ` AND h.search_id = ` + arg(f.searchID) + `::uuid`
	}
	topics := sel.sql("n.brand_id", &args)
	hits += topics
	seen := `aa.workspace_id = $1 AND EXISTS (SELECT 1 FROM news_hits h JOIN news_searches n ON n.id = h.search_id WHERE ` + hits + `)`
	if f.since != nil {
		seen += ` AND coalesce(x.published_at, x.first_seen_at) >= ` + arg(*f.since)
	}
	cand := `SELECT aa.record_id AS id FROM news_article_authors aa JOIN news_articles x ON x.id = aa.article_id WHERE ` + seen
	if f.searchID == "" && f.since == nil {
		cand += ` UNION SELECT r.id FROM records r WHERE r.workspace_id = $1 AND r.record_type = 'person'
			AND r.attributes @> '{"journalist": true}'` + sel.sql("r.brand_id", &args)
	}
	if f.only != "" {
		cand = `SELECT ` + arg(f.only) + `::uuid AS id`
	}
	where := `r.workspace_id = $1 AND r.record_type = 'person' AND r.merged_into IS NULL AND r.deleted_at IS NULL`
	if f.only == "" {
		where += ` AND ` + notUnmarked
	}
	if f.beat != "" {
		where += ` AND jsonb_typeof(r.attributes -> 'beats') = 'array'
			AND EXISTS (SELECT 1 FROM jsonb_array_elements_text(r.attributes -> 'beats') b WHERE lower(b) = lower(` + arg(f.beat) + `))`
	}
	if f.tiers != nil {
		where += ` AND EXISTS (SELECT 1 FROM record_relationships rr JOIN records c ON c.id = rr.to_id AND c.merged_into IS NULL
			WHERE rr.workspace_id = $1 AND rr.from_id = r.id AND rr.kind = 'companies' AND rr.valid_to IS NULL
			AND c.attributes ->> 'outlet_tier' = ANY(` + arg(f.tiers) + `::text[]))`
	}
	from := `FROM (` + cand + `) c JOIN records r ON r.id = c.id`
	var total int
	if err := tx.QueryRow(ctx, `SELECT count(*) `+from+` WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := tx.Query(ctx, `SELECT r.id::text, r.brand_id::text, `+notUnmarked+`, r.name,
			(SELECT i.value FROM record_identities i WHERE i.record_id = r.id AND i.namespace = 'email' AND i.valid_to IS NULL
				ORDER BY i.verified DESC, i.created_at LIMIT 1),
			r.attributes ->> 'job_title', r.attributes -> 'beats',
			coalesce(art.n, 0), coalesce(art.confidence, 0), coalesce(art.urls, '{}'), art.latest,
			(SELECT coalesce(jsonb_agg(DISTINCT jsonb_build_object('search_id', n.id, 'name', n.name, 'purpose', n.purpose)), '[]'::jsonb)
				FROM news_article_authors a2 JOIN news_hits h ON h.article_id = a2.article_id JOIN news_searches n ON n.id = h.search_id
				WHERE a2.workspace_id = $1 AND a2.record_id = r.id`+topics+`),
			(SELECT coalesce(jsonb_agg(jsonb_build_object('record_id', o.id, 'name', o.name, 'tier', o.attributes ->> 'outlet_tier',
					'domain', (SELECT i.value FROM record_identities i WHERE i.record_id = o.id AND i.namespace = 'domain' AND i.valid_to IS NULL
						ORDER BY i.verified DESC, i.created_at LIMIT 1)) ORDER BY o.name, o.id), '[]'::jsonb)
				FROM (SELECT DISTINCT o.id, o.name, o.attributes FROM record_relationships rr
					JOIN records o ON o.id = rr.to_id AND o.record_type = 'account' AND o.merged_into IS NULL AND o.deleted_at IS NULL
					WHERE rr.workspace_id = $1 AND rr.from_id = r.id AND rr.kind = 'companies' AND rr.valid_to IS NULL) o)
		`+from+` LEFT JOIN LATERAL (
			SELECT count(DISTINCT x.id)::int AS n, max(aa.confidence) AS confidence, array_agg(DISTINCT x.url) AS urls,
				(array_agg(jsonb_build_object('id', x.id, 'url', x.url, 'title', x.title, 'published_at', x.published_at)
					ORDER BY coalesce(x.published_at, x.first_seen_at) DESC))[1] AS latest,
				max(coalesce(x.published_at, x.first_seen_at)) AS last_at
			FROM news_article_authors aa JOIN news_articles x ON x.id = aa.article_id
			WHERE aa.record_id = r.id AND `+seen+`) art ON true
		WHERE `+where+`
		ORDER BY coalesce(art.n, 0) DESC, art.last_at DESC NULLS LAST, CASE WHEN art.last_at IS NULL THEN r.created_at END DESC NULLS LAST, r.id
		LIMIT `+arg(limit)+` OFFSET `+arg(offset), args...)
	if err != nil {
		return nil, 0, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (journalistView, error) {
		var v journalistView
		var urls []string
		var beats, latest, topics, outlets []byte
		if err := row.Scan(&v.RecordID, &v.BrandID, &v.Journalist, &v.Name, &v.Email, &v.JobTitle, &beats, &v.Articles, &v.Confidence, &urls,
			&latest, &topics, &outlets); err != nil {
			return v, err
		}
		var b any
		if len(beats) > 0 {
			if err := json.Unmarshal(beats, &b); err != nil {
				return v, err
			}
		}
		v.Beats = news.Beats(b)
		if len(latest) > 0 {
			v.LatestArticle = &journalistArticle{}
			if err := json.Unmarshal(latest, v.LatestArticle); err != nil {
				return v, err
			}
		}
		var found []struct {
			journalistTopic
			Purpose string `json:"purpose"`
		}
		if err := json.Unmarshal(topics, &found); err != nil {
			return v, err
		}
		v.Topics, v.SuggestedBeats = []journalistTopic{}, []string{}
		have := map[string]bool{}
		for _, b := range v.Beats {
			have[strings.ToLower(news.CleanBeat(b))] = true
		}
		for _, t := range found {
			v.Topics = append(v.Topics, t.journalistTopic)
			if b := news.CleanBeat(t.Name); t.Purpose == "topic" && b != "" && !have[strings.ToLower(b)] {
				have[strings.ToLower(b)] = true
				v.SuggestedBeats = append(v.SuggestedBeats, b)
			}
		}
		if err := json.Unmarshal(outlets, &v.OutletRecords); err != nil {
			return v, err
		}
		v.Outlets = []string{}
		for _, u := range urls {
			if _, d := news.ArticleURL(u); d != "" && !slices.Contains(v.Outlets, d) {
				v.Outlets = append(v.Outlets, d)
			}
		}
		for _, o := range v.OutletRecords {
			if o.Domain != nil && !slices.Contains(v.Outlets, *o.Domain) {
				v.Outlets = append(v.Outlets, *o.Domain)
			}
		}
		slices.Sort(v.Outlets)
		return v, nil
	})
	if list == nil {
		list = []journalistView{}
	}
	return list, total, err
}
