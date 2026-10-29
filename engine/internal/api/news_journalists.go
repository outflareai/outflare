package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/news"
)

// Journalists: the authors of news articles the news watch read whose
// address the email finder found at the outlet. Each is a person record on
// the outlet's company; this lists them with what they wrote, where, and
// which searches their articles came up in, so a media list is a list of
// these records. Reads only what is stored, so it costs nothing.

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

type journalistView struct {
	RecordID      string            `json:"record_id"`
	Name          string            `json:"name"`
	Email         *string           `json:"email"`
	Confidence    int               `json:"confidence"`
	Outlets       []string          `json:"outlets"`
	Articles      int               `json:"articles"`
	LatestArticle journalistArticle `json:"latest_article"`
	Topics        []journalistTopic `json:"topics"`
}

func (s *Server) listJournalists(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	args := []any{a.WorkspaceID}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	// A journalist shows through the articles the caller sees: those found
	// by a search the caller sees, or by one search.
	hits := `h.article_id = x.id`
	if v := q.Get("search_id"); v != "" {
		if !validUUID(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "search_id is a news search's id")
			return
		}
		hits += ` AND h.search_id = ` + arg(v) + `::uuid`
	}
	topics := sel.sql("n.brand_id", &args)
	hits += topics
	where := `aa.workspace_id = $1 AND EXISTS (SELECT 1 FROM news_hits h JOIN news_searches n ON n.id = h.search_id WHERE ` + hits + `)`
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "since is a time like 2026-10-28T00:00:00Z")
			return
		}
		where += ` AND coalesce(x.published_at, x.first_seen_at) >= ` + arg(t)
	}
	limit, err := limitParam(r, 50, 200)
	if s.allocError(w, r, err) {
		return
	}
	offset, ok := offsetParam(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	// One row per journalist: their articles in view, newest first.
	base := `FROM news_article_authors aa JOIN news_articles x ON x.id = aa.article_id
		JOIN records r ON r.id = aa.record_id AND r.merged_into IS NULL WHERE ` + where
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(DISTINCT aa.record_id) `+base, args...).Scan(&total); err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err := s.pool.Query(ctx, `SELECT aa.record_id::text, r.name,
			(SELECT i.value FROM record_identities i WHERE i.record_id = aa.record_id AND i.namespace = 'email' AND i.valid_to IS NULL
				ORDER BY i.verified DESC, i.created_at LIMIT 1),
			max(aa.confidence), count(DISTINCT x.id)::int, array_agg(DISTINCT x.url),
			(array_agg(jsonb_build_object('id', x.id, 'url', x.url, 'title', x.title, 'published_at', x.published_at)
				ORDER BY coalesce(x.published_at, x.first_seen_at) DESC))[1],
			(SELECT coalesce(jsonb_agg(DISTINCT jsonb_build_object('search_id', n.id, 'name', n.name)), '[]'::jsonb)
				FROM news_article_authors a2 JOIN news_hits h ON h.article_id = a2.article_id JOIN news_searches n ON n.id = h.search_id
				WHERE a2.workspace_id = $1 AND a2.record_id = aa.record_id`+topics+`)
		`+base+` GROUP BY aa.record_id, r.name
		ORDER BY count(DISTINCT x.id) DESC, max(coalesce(x.published_at, x.first_seen_at)) DESC, aa.record_id
		LIMIT `+arg(limit)+` OFFSET `+arg(offset), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (journalistView, error) {
		var v journalistView
		var urls []string
		var latest, topics []byte
		if err := row.Scan(&v.RecordID, &v.Name, &v.Email, &v.Confidence, &v.Articles, &urls, &latest, &topics); err != nil {
			return v, err
		}
		v.Outlets = []string{}
		for _, u := range urls {
			if _, d := news.ArticleURL(u); d != "" && !slices.Contains(v.Outlets, d) {
				v.Outlets = append(v.Outlets, d)
			}
		}
		slices.Sort(v.Outlets)
		v.Topics = []journalistTopic{}
		if err := json.Unmarshal(latest, &v.LatestArticle); err != nil {
			return v, err
		}
		return v, json.Unmarshal(topics, &v.Topics)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []journalistView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "total": total, "next_offset": nextOffset(offset, len(list), total)})
}
