package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Search finds customer records and their history (emails sent and
// received, meetings, deals, lifecycle email) by the words in them, with
// Postgres full-text search (migration 0200). Each kind needs the read
// scope the API asks for it, and brands follow the record timeline.

func (s *Server) routesSearch(mux *http.ServeMux) {
	// Any caller in the workspace: each kind needs its own read scope.
	mux.HandleFunc("GET /v1/workspaces/{ws}/search", s.inBrand("", s.search))
}

// searchKinds are what search finds, with the scope each needs. A message
// is an email sent (sequences.read) or received (inbox.read).
var searchKinds = []struct{ kind, scope string }{
	{"record", auth.ScopeCustomersRead},
	{"message", auth.ScopeSequencesRead},
	{"message", auth.ScopeInboxRead},
	{"meeting", auth.ScopeOutcomesRead},
	{"deal", auth.ScopeOutcomesRead},
	{"email", auth.ScopeEmailsRead},
}

type searchHit struct {
	Kind       string    `json:"kind"`
	ID         string    `json:"id"`
	RecordID   *string   `json:"record_id"`
	RecordName *string   `json:"record_name"`
	Title      string    `json:"title"`
	Snippet    string    `json:"snippet"`
	Detail     string    `json:"detail"`
	At         time.Time `json:"at"`
	Rank       float64   `json:"rank"`
}

// searchQuery turns what someone typed into a tsquery every word of which
// must match, each as a prefix. Only letters, digits and @ . _ - are kept,
// so nothing typed is ever tsquery syntax.
func searchQuery(q string) string {
	words := strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("@._-", r))
	})
	var parts []string
	for _, w := range words {
		w = strings.Trim(w, "._-")
		if w == "" || len(parts) == 10 {
			continue
		}
		parts = append(parts, "'"+w+"':*")
	}
	return strings.Join(parts, " & ")
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	q := r.URL.Query()
	tsq := searchQuery(q.Get("q"))
	if tsq == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "q is the words to search for")
		return
	}
	limit := 20
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit is 1 to 100")
			return
		}
		limit = n
	}
	want := map[string]bool{}
	if v := q.Get("kinds"); v != "" {
		for _, k := range strings.Split(v, ",") {
			k = strings.TrimSpace(k)
			if k != "record" && k != "message" && k != "meeting" && k != "deal" && k != "email" {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "kinds are record, message, meeting, deal and email")
				return
			}
			want[k] = true
		}
	}
	// What the caller may find: a kind it asked for (or all), with its scope.
	may := map[string]bool{}
	var missing []string
	for _, k := range searchKinds {
		if len(want) > 0 && !want[k.kind] {
			continue
		}
		if a.has(k.scope) {
			may[k.scope] = true
		} else {
			missing = append(missing, k.scope)
		}
	}
	if len(may) == 0 {
		writeError(w, r, http.StatusForbidden, "forbidden", s.missingScope(a, missing[0]))
		return
	}
	start := time.Now()
	// Each kind's best matches, then the best of those. The vectors are
	// the indexed functions of migration 0200, so each kind uses its index.
	rows, err := s.pool.Query(ctx, `
		WITH q AS (SELECT to_tsquery('simple', $2) AS t),
		hits AS (
			(SELECT 'record' AS kind, r.id, r.id AS record_id, r.name AS title, r.record_type AS detail, r.updated_at AS at,
				ts_rank(search_vector_record(r.name, r.attributes), q.t) + 0.5 AS rank, NULL::text AS body
			FROM q, records r
			WHERE $3 AND r.workspace_id = $1 AND r.merged_into IS NULL AND r.archived_at IS NULL
				AND search_vector_record(r.name, r.attributes) @@ q.t
				AND ($9::uuid[] IS NULL OR r.brand_id IS NULL OR r.brand_id = ANY ($9))
			ORDER BY 7 DESC, 6 DESC LIMIT 200)
			UNION ALL
			(SELECT 'record', r.id, r.id, r.name, i.namespace || ': ' || i.value, r.updated_at, ts_rank(search_vector_identity(i.value), q.t) + 0.5, NULL
			FROM q, record_identities i JOIN records r ON r.id = i.record_id
			WHERE $3 AND i.workspace_id = $1 AND r.workspace_id = $1 AND i.valid_to IS NULL AND r.merged_into IS NULL AND r.archived_at IS NULL
				AND search_vector_identity(i.value) @@ q.t
				AND ($9::uuid[] IS NULL OR r.brand_id IS NULL OR r.brand_id = ANY ($9))
			ORDER BY 7 DESC, 6 DESC LIMIT 200)
			UNION ALL
			(SELECT 'message', m.id, m.record_id, coalesce(m.subject, ''), 'sent', coalesce(m.sent_at, m.created_at),
				ts_rank(search_vector_message(m.subject, NULL), q.t), NULL
			FROM q, messages m LEFT JOIN sequences x ON x.id = m.sequence_id
			WHERE $4 AND m.workspace_id = $1 AND search_vector_message(m.subject, NULL) @@ q.t
				AND ($9::uuid[] IS NULL OR x.brand_id IS NULL OR x.brand_id = ANY ($9))
			ORDER BY 7 DESC, 6 DESC LIMIT 200)
			UNION ALL
			(SELECT 'message', n.id, n.record_id, coalesce(n.subject, ''), 'received', n.received_at,
				ts_rank(search_vector_message(n.subject, n.body), q.t), left(coalesce(n.body, ''), 20000)
			FROM q, inbound_messages n LEFT JOIN mailboxes x ON x.id = n.mailbox_id
			WHERE $5 AND n.workspace_id = $1 AND search_vector_message(n.subject, n.body) @@ q.t
				AND ($9::uuid[] IS NULL OR x.brand_id IS NULL OR x.brand_id = ANY ($9))
			ORDER BY 7 DESC, 6 DESC LIMIT 200)
			UNION ALL
			(SELECT 'meeting', g.id, g.record_id, coalesce(g.title, ''), coalesce(g.status, ''), coalesce(g.booked_at, g.created_at),
				ts_rank(search_vector_title(g.title, g.notes), q.t), left(coalesce(g.notes, ''), 20000)
			FROM q, meetings g LEFT JOIN sequences x ON x.id = g.sequence_id
			WHERE $6 AND g.workspace_id = $1 AND search_vector_title(g.title, g.notes) @@ q.t
				AND ($9::uuid[] IS NULL OR x.brand_id IS NULL OR x.brand_id = ANY ($9))
			ORDER BY 7 DESC, 6 DESC LIMIT 200)
			UNION ALL
			(SELECT 'deal', d.id, d.record_id, coalesce(d.name, ''), coalesce(d.stage, ''), coalesce(d.opened_at, d.created_at),
				ts_rank(search_vector_title(d.name, d.stage), q.t), NULL
			FROM q, deals d LEFT JOIN sequences x ON x.id = d.sequence_id
			WHERE $7 AND d.workspace_id = $1 AND search_vector_title(d.name, d.stage) @@ q.t
				AND ($9::uuid[] IS NULL OR x.brand_id IS NULL OR x.brand_id = ANY ($9))
			ORDER BY 7 DESC, 6 DESC LIMIT 200)
			UNION ALL
			(SELECT 'email', e.id, e.record_id, coalesce(e.subject, ''), coalesce(e.status, ''), coalesce(e.sent_at, e.created_at),
				ts_rank(search_vector_title(e.subject, NULL), q.t), NULL
			FROM q, emails e
			WHERE $8 AND e.workspace_id = $1 AND search_vector_title(e.subject, NULL) @@ q.t
				AND ($9::uuid[] IS NULL OR e.brand_id IS NULL OR e.brand_id = ANY ($9))
			ORDER BY 7 DESC, 6 DESC LIMIT 200)
		),
		best AS (
			SELECT DISTINCT ON (kind, id) * FROM hits ORDER BY kind, id, rank DESC
		)
		SELECT b.kind, b.id::text, b.record_id::text, rr.name, b.title, b.detail, b.at, b.rank::float8,
			CASE WHEN b.body IS NULL OR b.body = '' THEN ''
				ELSE ts_headline('simple', b.body, q.t, 'MaxWords=30, MinWords=12, MaxFragments=1, StartSel="", StopSel=""') END
		FROM best b CROSS JOIN q
		-- The record's name, for a caller who may read records and its brand.
		LEFT JOIN records rr ON rr.id = b.record_id AND rr.workspace_id = $1 AND $10
			AND ($9::uuid[] IS NULL OR rr.brand_id IS NULL OR rr.brand_id = ANY ($9))
		ORDER BY b.rank DESC, b.at DESC LIMIT $11`,
		a.WorkspaceID, tsq,
		may[auth.ScopeCustomersRead] && (len(want) == 0 || want["record"]),
		may[auth.ScopeSequencesRead] && (len(want) == 0 || want["message"]),
		may[auth.ScopeInboxRead] && (len(want) == 0 || want["message"]),
		may[auth.ScopeOutcomesRead] && (len(want) == 0 || want["meeting"]),
		may[auth.ScopeOutcomesRead] && (len(want) == 0 || want["deal"]),
		may[auth.ScopeEmailsRead] && (len(want) == 0 || want["email"]),
		brandList(a.Brands), a.has(auth.ScopeCustomersRead), limit)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	hits, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (searchHit, error) {
		var h searchHit
		err := row.Scan(&h.Kind, &h.ID, &h.RecordID, &h.RecordName, &h.Title, &h.Detail, &h.At, &h.Rank, &h.Snippet)
		return h, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	kinds := map[string]int{}
	for _, h := range hits {
		kinds[h.Kind]++
	}
	ev := track.Event{Name: "search.ran", UserID: a.UserID, ActorID: a.ActorID, ActorType: a.ActorType, WorkspaceID: a.WorkspaceID, RequestID: RequestID(ctx),
		Props: map[string]any{"results": len(hits), "kinds": kinds, "words": strings.Count(tsq, "&") + 1, "duration_ms": time.Since(start).Milliseconds()}}
	if info := reqInfoFrom(ctx); info != nil {
		ev.Surface = surface(r, info)
	}
	track.Emit(ctx, ev)
	writeJSON(w, http.StatusOK, map[string]any{"data": hits})
}
