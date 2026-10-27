package api

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/sociallinks"
)

// Social accounts and growth: the brand's own pages and profiles on X and
// LinkedIn, found through the workspace's own connections, with their
// followers day by day and how their own posts did. These are the numbers
// the Social overview, the brand and agents read.

func (s *Server) routesSocialAccounts(mux *http.ServeMux) {
	read, manage := auth.ScopeSocialRead, auth.ScopeSocialManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/social/accounts", s.inBrand(read, s.listSocialAccounts))
	mux.HandleFunc("POST /v1/workspaces/{ws}/social/accounts/sync", s.inBrand(manage, s.syncSocialAccounts))
	mux.HandleFunc("GET /v1/workspaces/{ws}/social/accounts/{id}", s.inBrand(read, s.getSocialAccount))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/social/accounts/{id}", s.inBrand(manage, s.updateSocialAccount))
	mux.HandleFunc("GET /v1/workspaces/{ws}/social/accounts/{id}/days", s.inBrand(read, s.listSocialAccountDays))
	mux.HandleFunc("GET /v1/workspaces/{ws}/social/posts", s.inBrand(read, s.listSocialPosts))
	mux.HandleFunc("GET /v1/workspaces/{ws}/social/growth", s.inBrand(read, s.socialGrowth))
}

type socialAccountView struct {
	ID            string  `json:"id"`
	Channel       string  `json:"channel"`
	Kind          string  `json:"kind"`
	BrandID       *string `json:"brand_id"`
	IntegrationID *string `json:"integration_id"`
	ExternalID    string  `json:"external_id"`
	Handle        string  `json:"handle"`
	Name          string  `json:"name"`
	URL           string  `json:"url"`
	AvatarURL     string  `json:"avatar_url"`
	Status        string  `json:"status"`
	Followers     *int64  `json:"followers"`
	Following     *int64  `json:"following"`
	Posts         *int64  `json:"posts"`
	// Followers gained (or lost) since yesterday and over the last 7 and
	// 28 days, from the day-by-day counts; null until there is a count
	// that old.
	FollowersChange1d  *int64     `json:"followers_change_1d"`
	FollowersChange7d  *int64     `json:"followers_change_7d"`
	FollowersChange28d *int64     `json:"followers_change_28d"`
	LastSyncedAt       *time.Time `json:"last_synced_at"`
	NextSyncAt         time.Time  `json:"next_sync_at"`
	LastError          *string    `json:"last_error"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

const socialAccountSelect = `
	SELECT a.id::text, a.channel, a.kind, a.brand_id::text, a.integration_id::text, a.external_id, a.handle, a.name, a.url, a.avatar_url,
		a.status, a.followers, a.following, a.posts,
		a.followers - (SELECT d.followers FROM social_account_days d WHERE d.account_id = a.id AND d.followers IS NOT NULL
			AND d.day <= (now() AT TIME ZONE 'UTC')::date - 1 ORDER BY d.day DESC LIMIT 1),
		a.followers - (SELECT d.followers FROM social_account_days d WHERE d.account_id = a.id AND d.followers IS NOT NULL
			AND d.day <= (now() AT TIME ZONE 'UTC')::date - 7 ORDER BY d.day DESC LIMIT 1),
		a.followers - (SELECT d.followers FROM social_account_days d WHERE d.account_id = a.id AND d.followers IS NOT NULL
			AND d.day <= (now() AT TIME ZONE 'UTC')::date - 28 ORDER BY d.day DESC LIMIT 1),
		a.last_synced_at, a.next_sync_at, a.last_error, a.created_at, a.updated_at
	FROM social_accounts a WHERE a.workspace_id = $1`

func scanSocialAccount(row pgx.Row) (socialAccountView, error) {
	var v socialAccountView
	err := row.Scan(&v.ID, &v.Channel, &v.Kind, &v.BrandID, &v.IntegrationID, &v.ExternalID, &v.Handle, &v.Name, &v.URL, &v.AvatarURL,
		&v.Status, &v.Followers, &v.Following, &v.Posts, &v.FollowersChange1d, &v.FollowersChange7d, &v.FollowersChange28d,
		&v.LastSyncedAt, &v.NextSyncAt, &v.LastError, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

// socialAccountChannel checks ?channel= against the channels that have
// accounts.
func socialAccountChannel(w http.ResponseWriter, r *http.Request) (string, bool) {
	ch := r.URL.Query().Get("channel")
	if ch != "" && !slices.Contains(sociallinks.PublicKinds, ch) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "channel is one of "+strings.Join(sociallinks.PublicKinds, ", "))
		return "", false
	}
	return ch, true
}

func (s *Server) listSocialAccounts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	ch, ok := socialAccountChannel(w, r)
	if !ok {
		return
	}
	st := r.URL.Query().Get("status")
	if st != "" && st != "active" && st != "paused" && st != "disconnected" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is active, paused or disconnected")
		return
	}
	args := []any{a.WorkspaceID, ch, st}
	rows, err := s.pool.Query(r.Context(), socialAccountSelect+` AND ($2 = '' OR a.channel = $2) AND ($3 = '' OR a.status = $3)`+
		sel.sql("a.brand_id", &args)+` ORDER BY a.channel, a.kind DESC, a.created_at LIMIT 500`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (socialAccountView, error) { return scanSocialAccount(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []socialAccountView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// findSocialAccount loads an account the caller may see.
func (s *Server) findSocialAccount(w http.ResponseWriter, r *http.Request, a *access, id string) (*socialAccountView, bool) {
	if validUUID(id) {
		v, err := scanSocialAccount(s.pool.QueryRow(r.Context(), socialAccountSelect+` AND a.id = $2`, a.WorkspaceID, id))
		if err == nil && a.sees(nil, v.BrandID) {
			return &v, true
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return nil, false
		}
	}
	writeError(w, r, http.StatusNotFound, "not_found", "account not found")
	return nil, false
}

func (s *Server) getSocialAccount(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if v, ok := s.findSocialAccount(w, r, a, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// updateSocialAccount puts an account on a brand, or pauses or resumes
// reading it.
func (s *Server) updateSocialAccount(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findSocialAccount(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	var req struct {
		BrandID optional[string] `json:"brand_id"`
		Status  *string          `json:"status"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !a.writes(nil, cur.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", string(errSharedRecord))
		return
	}
	ctx := r.Context()
	brand, status := cur.BrandID, cur.Status
	changed := map[string]any{"channel": cur.Channel}
	if req.BrandID.Set {
		brand = nil
		if req.BrandID.Value != nil && *req.BrandID.Value != "" {
			id, err := s.brandIDFor(ctx, a.WorkspaceID, *req.BrandID.Value)
			if errors.Is(err, errNotFound) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "brand_id: brand not found")
				return
			}
			if err != nil {
				s.internal(w, r, err)
				return
			}
			brand = &id
		}
		if !a.writes(nil, brand) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "brand_id: you can only put accounts on your own brands")
			return
		}
		changed["brand_changed"] = deref(brand) != deref(cur.BrandID)
	}
	if req.Status != nil {
		switch {
		case *req.Status != "active" && *req.Status != "paused":
			writeError(w, r, http.StatusBadRequest, "invalid_request", "status is active or paused")
			return
		case cur.Status == "disconnected" && *req.Status == "active":
			writeError(w, r, http.StatusConflict, "conflict", "its connection no longer reaches this account; sign in again with an account that does")
			return
		}
		if cur.Status != "disconnected" {
			status = *req.Status
		}
		changed["status"] = status
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE social_accounts SET brand_id = $3, status = $4,
			next_sync_at = CASE WHEN status = 'paused' AND $4 = 'active' THEN now() ELSE next_sync_at END, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, cur.ID, brand, status); err != nil {
			return err
		}
		// The account's posts follow it to its brand.
		if _, err := tx.Exec(ctx, `UPDATE social_posts SET brand_id = $3 WHERE workspace_id = $1 AND account_id = $2 AND brand_id IS DISTINCT FROM $3`,
			a.WorkspaceID, cur.ID, brand); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "social_account.updated", "social_account", cur.ID, changed)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findSocialAccount(w, r, a, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

var errNoSocialConnection = errors.New("no social connection")

// syncSocialAccounts reads every connection that reaches accounts now:
// it finds new pages and profiles and reads the ones that are due.
func (s *Server) syncSocialAccounts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	ids, _ := s.socialMetricsConnectors()
	n := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM integrations WHERE workspace_id = $1 AND connector = ANY($2) AND status = 'connected'`, a.WorkspaceID, ids)
		if err != nil {
			return err
		}
		list, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		// A minute's key: pressing it twice reads once.
		key := "now:" + strconv.FormatInt(time.Now().Unix()/60, 10)
		for _, id := range list {
			if err := enqueueSocialSync(ctx, tx, a.WorkspaceID, id, key); err != nil {
				return err
			}
		}
		n = len(list)
		if n == 0 {
			return errNoSocialConnection
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "social_account.sync_requested", "workspace", a.WorkspaceID, map[string]any{"connections": n})
	})
	if errors.Is(err, errNoSocialConnection) {
		writeError(w, r, http.StatusConflict, "connector_needed", "connect X or LinkedIn first")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "connections": n})
}

type socialDayView struct {
	Day       string `json:"day"`
	Followers *int64 `json:"followers"`
	Following *int64 `json:"following"`
	Posts     *int64 `json:"posts"`
}

// daysParam reads ?days= (1 to 365, default def).
func daysParam(w http.ResponseWriter, r *http.Request, def int) (int, bool) {
	q := r.URL.Query().Get("days")
	if q == "" {
		return def, true
	}
	n, err := strconv.Atoi(q)
	if err != nil || n < 1 || n > 365 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "days is 1 to 365")
		return 0, false
	}
	return n, true
}

func (s *Server) listSocialAccountDays(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findSocialAccount(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	days, ok := daysParam(w, r, 90)
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT to_char(day, 'YYYY-MM-DD'), followers, following, posts FROM social_account_days
		WHERE workspace_id = $1 AND account_id = $2 AND day > (now() AT TIME ZONE 'UTC')::date - $3::int ORDER BY day`, a.WorkspaceID, cur.ID, days)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (socialDayView, error) {
		var v socialDayView
		err := row.Scan(&v.Day, &v.Followers, &v.Following, &v.Posts)
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []socialDayView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

type socialPostView struct {
	ID          string    `json:"id"`
	AccountID   string    `json:"account_id"`
	Channel     string    `json:"channel"`
	BrandID     *string   `json:"brand_id"`
	ExternalID  string    `json:"external_id"`
	URL         string    `json:"url"`
	Body        string    `json:"body"`
	PostedAt    time.Time `json:"posted_at"`
	Impressions *int64    `json:"impressions"`
	Likes       *int64    `json:"likes"`
	Replies     *int64    `json:"replies"`
	Reposts     *int64    `json:"reposts"`
	Quotes      *int64    `json:"quotes"`
	Bookmarks   *int64    `json:"bookmarks"`
	Clicks      *int64    `json:"clicks"`
	// Likes, replies, reposts and quotes together, and their share of
	// impressions; null when the channel gives no impressions.
	Engagements    int64      `json:"engagements"`
	EngagementRate *float64   `json:"engagement_rate"`
	MetricsAt      *time.Time `json:"metrics_at"`
}

// socialEngagements is the SQL sum of a post's engagements.
const socialEngagements = `(COALESCE(p.likes, 0) + COALESCE(p.replies, 0) + COALESCE(p.reposts, 0) + COALESCE(p.quotes, 0))`

func (s *Server) listSocialPosts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	ch, ok := socialAccountChannel(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	acct := q.Get("account_id")
	if acct != "" && !validUUID(acct) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "account_id is an account id")
		return
	}
	days, ok := daysParam(w, r, 28)
	if !ok {
		return
	}
	order := `p.posted_at DESC`
	switch q.Get("sort") {
	case "", "recent":
	case "engagements":
		order = socialEngagements + ` DESC, p.posted_at DESC`
	case "impressions":
		order = `p.impressions DESC NULLS LAST, p.posted_at DESC`
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "sort is recent, engagements or impressions")
		return
	}
	limit := 50
	if l := q.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 || n > 200 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit is 1 to 200")
			return
		}
		limit = n
	}
	args := []any{a.WorkspaceID, ch, acct, days, limit}
	rows, err := s.pool.Query(r.Context(), `
		SELECT p.id::text, p.account_id::text, p.channel, p.brand_id::text, p.external_id, p.url, p.body, p.posted_at,
			p.impressions, p.likes, p.replies, p.reposts, p.quotes, p.bookmarks, p.clicks, `+socialEngagements+`, p.metrics_at
		FROM social_posts p WHERE p.workspace_id = $1 AND ($2 = '' OR p.channel = $2) AND ($3 = '' OR p.account_id::text = $3)
			AND p.posted_at > now() - make_interval(days => $4)`+sel.sql("p.brand_id", &args)+`
		ORDER BY `+order+` LIMIT $5`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (socialPostView, error) {
		var v socialPostView
		err := row.Scan(&v.ID, &v.AccountID, &v.Channel, &v.BrandID, &v.ExternalID, &v.URL, &v.Body, &v.PostedAt,
			&v.Impressions, &v.Likes, &v.Replies, &v.Reposts, &v.Quotes, &v.Bookmarks, &v.Clicks, &v.Engagements, &v.MetricsAt)
		if v.Impressions != nil && *v.Impressions > 0 {
			rate := float64(v.Engagements) / float64(*v.Impressions)
			v.EngagementRate = &rate
		}
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []socialPostView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

type socialChannelGrowth struct {
	Channel  string `json:"channel"`
	Accounts int    `json:"accounts"`
	// Followers across the channel's active accounts now, and the change
	// over the period (null until there are counts that old).
	Followers       int64  `json:"followers"`
	FollowersChange *int64 `json:"followers_change"`
	// The accounts' own posts in the period and how they did so far.
	Posts          int      `json:"posts"`
	Impressions    int64    `json:"impressions"`
	Engagements    int64    `json:"engagements"`
	Clicks         int64    `json:"clicks"`
	EngagementRate *float64 `json:"engagement_rate"`
}

type socialGrowthDay struct {
	Day       string `json:"day"`
	Followers int64  `json:"followers"`
}

// socialGrowth sums followers and post results per channel over a period,
// with total followers day by day.
func (s *Server) socialGrowth(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	days, ok := daysParam(w, r, 28)
	if !ok {
		return
	}
	ctx := r.Context()
	args := []any{a.WorkspaceID, days}
	bs := sel.sql("a.brand_id", &args)
	rows, err := s.pool.Query(ctx, `
		WITH accts AS (SELECT a.id, a.channel, a.followers FROM social_accounts a WHERE a.workspace_id = $1 AND a.status = 'active'`+bs+`),
		before AS (
			SELECT DISTINCT ON (d.account_id) d.account_id, d.followers FROM social_account_days d JOIN accts ON accts.id = d.account_id
			WHERE d.followers IS NOT NULL AND d.day <= (now() AT TIME ZONE 'UTC')::date - $2::int ORDER BY d.account_id, d.day DESC),
		posts AS (
			SELECT p.channel, count(*) AS n, COALESCE(sum(p.impressions), 0) AS imp, COALESCE(sum(`+socialEngagements+`), 0) AS eng,
				COALESCE(sum(p.clicks), 0) AS clicks, COALESCE(sum(p.impressions) FILTER (WHERE p.impressions IS NOT NULL), 0) AS rated_imp,
				COALESCE(sum(`+socialEngagements+`) FILTER (WHERE p.impressions IS NOT NULL), 0) AS rated_eng
			FROM social_posts p JOIN accts ON accts.id = p.account_id
			WHERE p.posted_at > now() - make_interval(days => $2) GROUP BY p.channel)
		SELECT accts.channel, count(*)::int, COALESCE(sum(accts.followers), 0)::bigint,
			CASE WHEN count(before.account_id) = 0 THEN NULL ELSE (sum(accts.followers) FILTER (WHERE before.account_id IS NOT NULL) - sum(before.followers))::bigint END,
			COALESCE(max(posts.n), 0)::int, COALESCE(max(posts.imp), 0)::bigint, COALESCE(max(posts.eng), 0)::bigint, COALESCE(max(posts.clicks), 0)::bigint,
			max(posts.rated_imp)::bigint, max(posts.rated_eng)::bigint
		FROM accts LEFT JOIN before ON before.account_id = accts.id LEFT JOIN posts ON posts.channel = accts.channel
		GROUP BY accts.channel ORDER BY accts.channel`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	channels, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (socialChannelGrowth, error) {
		var g socialChannelGrowth
		var ratedImp, ratedEng *int64
		err := row.Scan(&g.Channel, &g.Accounts, &g.Followers, &g.FollowersChange, &g.Posts, &g.Impressions, &g.Engagements, &g.Clicks, &ratedImp, &ratedEng)
		if ratedImp != nil && *ratedImp > 0 && ratedEng != nil {
			rate := float64(*ratedEng) / float64(*ratedImp)
			g.EngagementRate = &rate
		}
		return g, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if channels == nil {
		channels = []socialChannelGrowth{}
	}
	// Followers day by day: each account's latest count on or before each
	// day, summed, so a day an account was not read does not dip.
	args = []any{a.WorkspaceID, days}
	bs = sel.sql("a.brand_id", &args)
	rows, err = s.pool.Query(ctx, `
		WITH accts AS (SELECT a.id FROM social_accounts a WHERE a.workspace_id = $1 AND a.status = 'active'`+bs+`),
		span AS (SELECT generate_series((now() AT TIME ZONE 'UTC')::date - ($2::int - 1), (now() AT TIME ZONE 'UTC')::date, interval '1 day')::date AS day)
		SELECT to_char(span.day, 'YYYY-MM-DD'), COALESCE(sum(last.followers), 0)::bigint FROM span
		CROSS JOIN accts
		LEFT JOIN LATERAL (SELECT d.followers FROM social_account_days d WHERE d.account_id = accts.id AND d.day <= span.day AND d.followers IS NOT NULL
			ORDER BY d.day DESC LIMIT 1) last ON true
		GROUP BY span.day ORDER BY span.day`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	series, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (socialGrowthDay, error) {
		var d socialGrowthDay
		err := row.Scan(&d.Day, &d.Followers)
		return d, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if series == nil {
		series = []socialGrowthDay{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": days, "channels": channels, "followers_by_day": series})
}
