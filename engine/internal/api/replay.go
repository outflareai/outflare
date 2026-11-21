package api

import (
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/blobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Session recordings and heatmaps. A brand switches them on; its website
// script (site.js) then records what a visitor's page showed and did and
// sends it here in parts, and counts where visitors click and how far they
// scroll. What a visitor types is never recorded: the script masks every
// field before anything leaves the page.
//
// A recording's parts are files in the server's object storage
// (OUTFLARE_S3_*), so recordings need a bucket; heatmaps are counts in
// Postgres and need none. The rows here are what lists, finds and deletes
// a recording.

// The recorder the website script loads for visits that are recorded:
// rrweb's record bundle, unchanged (scripts/vendor-rrweb.mjs).
//
//go:embed replay_record.js
var replayRecorder []byte

// BlobStore keeps a recording's parts: the server's bucket.
type BlobStore interface {
	Put(ctx context.Context, key, contentType string, body []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
}

const (
	// maxReplayBody is the largest part a browser may send. A page's first
	// snapshot is its whole document, so it is far above an event batch.
	maxReplayBody = 4 << 20
	// A recording stops at these: a visit left open all day isn't worth
	// keeping, and a page that changes without end can't fill the bucket.
	replayMaxParts = 600
	replayMaxBytes = 60 << 20
	replayMaxAge   = 4 * time.Hour
	// replayWorkspaceDailyBytes is what a workspace's sites may record in a
	// day (UTC), before compression.
	replayWorkspaceDailyBytes = 5 << 30
	// What one request may add to a page's heatmap.
	heatmapClicksPerPart = 50
	// heatmapKeepDays is how long heatmap counts are kept.
	heatmapKeepDays = 90
	// A page's picture for its heatmap: how big one may be once gzipped,
	// how many pages of a brand have one, and how old one gets before a
	// visitor's browser is asked for a new one.
	heatmapSnapshotBytes   = 1 << 20
	heatmapSnapshotsBrand  = 200
	heatmapSnapshotRefresh = 7 * 24 * time.Hour
	// replaySettingsTTL is how long a server remembers a brand's settings
	// between looks, so the script's requests don't each read them.
	replaySettingsTTL = 30 * time.Second
)

var replaySessionRe = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

// replaySecretRe is the key formats the website script hides before it
// sends anything (SECRET_KNOWN in site.js). The server hides them again,
// so a key never reaches storage from a saved copy of an older script.
var replaySecretRe = regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{16,}|sk-(?:proj-|live-|test-)?[A-Za-z0-9_-]{20,}|[sprk]k_(?:live|test)_[A-Za-z0-9]{16,}|gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}|glpat-[A-Za-z0-9_-]{20,}|xox[abprs]-[A-Za-z0-9-]{10,}|A[KS]IA[0-9A-Z]{16}|AIza[0-9A-Za-z_-]{35}|of[tsi]_[A-Za-z0-9_-]{20,}|eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}|-----BEGIN [A-Z ]*PRIVATE KEY-----[A-Za-z0-9+/=\\nr\s]*(?:-----END [A-Z ]*PRIVATE KEY-----)?`)

// hideReplaySecrets replaces each key in recorded events with stars of
// the same length. The matches hold no quote or brace, so the JSON stays
// as it was.
func hideReplaySecrets(events []byte) []byte {
	return replaySecretRe.ReplaceAllFunc(events, func(m []byte) []byte {
		out := make([]byte, len(m))
		for i, c := range m {
			if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
				out[i] = c
			} else {
				out[i] = '*'
			}
		}
		return out
	})
}

// A brand's own key shapes: x for any letter or digit, and the separators
// a key may hold. At least eight x, so a shape never hides ordinary words.
const replayShapeRules = 20

var (
	replayShapeRe  = regexp.MustCompile(`^[x\-_.:+/=~]{8,100}$`)
	replayEscapeRe = regexp.MustCompile(`\\u[0-9a-fA-F]{4}|\\.`)
)

// cleanReplayShapes checks a list of key shapes and drops repeats.
func cleanReplayShapes(in []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !replayShapeRe.MatchString(p) || strings.Count(p, "x") < 8 {
			return nil, errors.New("a shape is written with x for each letter or digit and the key's own separators (- _ . : + / = ~), with at least 8 x, such as xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx. Never send a real key")
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if len(out) > replayShapeRules {
		return nil, fmt.Errorf("list at most %d shapes", replayShapeRules)
	}
	return out, nil
}

// replayShapePattern is a shape as a pattern: each run of x is that many
// letters or digits, and a separator is itself.
func replayShapePattern(shape string) string {
	var b strings.Builder
	for i := 0; i < len(shape); {
		if shape[i] != 'x' {
			b.WriteString(regexp.QuoteMeta(shape[i : i+1]))
			i++
			continue
		}
		n := 0
		for ; i < len(shape) && shape[i] == 'x'; i++ {
			n++
		}
		fmt.Fprintf(&b, "[A-Za-z0-9]{%d}", n)
	}
	return b.String()
}

func isLetterOrDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// hideReplayShapes replaces whatever has one of the brand's key shapes
// with stars, where it stands on its own (no letter or digit on either
// side). It looks past JSON's escapes, so a key after a line break is
// found and the JSON stays as it was.
func hideReplayShapes(events []byte, shapes []string) []byte {
	if len(shapes) == 0 {
		return events
	}
	plain := replayEscapeRe.ReplaceAllFunc(events, func(m []byte) []byte { return bytes.Repeat([]byte{' '}, len(m)) })
	out := events
	for _, shape := range shapes {
		re, err := regexp.Compile(replayShapePattern(shape))
		if err != nil {
			continue
		}
		for from := 0; from < len(plain); {
			loc := re.FindIndex(plain[from:])
			if loc == nil {
				break
			}
			a, b := from+loc[0], from+loc[1]
			if (a > 0 && isLetterOrDigit(plain[a-1])) || (b < len(plain) && isLetterOrDigit(plain[b])) {
				from = a + 1
				continue
			}
			for i := a; i < b; i++ {
				out[i] = '*'
			}
			from = b
		}
	}
	return out
}

type replaySettings struct {
	Recordings    bool `json:"recordings"`
	Heatmaps      bool `json:"heatmaps"`
	SamplePercent int  `json:"sample_percent"`
	MaskText      bool `json:"mask_text"`
	KeepDays      int  `json:"keep_days"`
	// The pages recorded and counted for heatmaps: every page when
	// IncludePaths is empty, never one in ExcludePaths.
	IncludePaths []string `json:"include_paths"`
	ExcludePaths []string `json:"exclude_paths"`
	// Recordings of visits shorter than this are not kept.
	MinSeconds int `json:"min_seconds"`
	// The most visits recorded in a day (UTC); 0 is no limit.
	DailyLimit int `json:"daily_limit"`
	// The shapes of the brand's own keys, hidden wherever they show.
	HideShapes []string `json:"hide_shapes"`
	// Whether the server has object storage, which recordings need.
	Storage bool `json:"storage"`
}

// siteReplay is what the website script is told to do, with the answer to
// its events (siteEvents).
type siteReplay struct {
	Record   bool `json:"record"`
	Heatmaps bool `json:"heatmaps"`
	Sample   int  `json:"sample"`
	MaskText bool `json:"mask_text"`
	// The pages to record and count, for the script to leave the others
	// alone; the server checks them again.
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
	// The shapes of the brand's own keys, for the script to hide.
	Hide []string `json:"hide,omitempty"`

	dailyLimit int
}

// replayPathRules is how many pages a brand may list to record or leave out.
const replayPathRules = 50

// cleanReplayPaths tidies a list of page paths: each starts with /, may
// use * for any run of characters, and loses its trailing slash.
func cleanReplayPaths(in []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.HasPrefix(p, "/") || len(p) > 200 || strings.ContainsAny(p, "?# \t\n") {
			return nil, errors.New("a page is a path that starts with /, such as /pricing or /blog/*, without a query string")
		}
		if len(p) > 1 {
			p = strings.TrimRight(p, "/")
		}
		if p == "" {
			p = "/"
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if len(out) > replayPathRules {
		return nil, fmt.Errorf("list at most %d pages", replayPathRules)
	}
	return out, nil
}

// replayPathIn reports whether a page's path is one of the listed pages.
func replayPathIn(list []string, path string) bool {
	for _, p := range list {
		if p == path {
			return true
		}
		if !strings.Contains(p, "*") {
			continue
		}
		parts := strings.Split(p, "*")
		for i := range parts {
			parts[i] = regexp.QuoteMeta(parts[i])
		}
		if ok, _ := regexp.MatchString("^"+strings.Join(parts, ".*")+"$", path); ok {
			return true
		}
	}
	return false
}

// pageOn reports whether a page is recorded and counted.
func (c *siteReplay) pageOn(path string) bool {
	if replayPathIn(c.Exclude, path) {
		return false
	}
	return len(c.Include) == 0 || replayPathIn(c.Include, path)
}

type replayCache struct {
	mu sync.Mutex
	m  map[string]replayCached
}

type replayCached struct {
	at time.Time
	v  replaySettings
}

func (c *replayCache) get(brand string) (replaySettings, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	x, ok := c.m[brand]
	if !ok || time.Since(x.at) > replaySettingsTTL {
		return replaySettings{}, false
	}
	return x.v, true
}

func (c *replayCache) put(brand string, v replaySettings) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) > 10000 {
		c.m = map[string]replayCached{}
	}
	c.m[brand] = replayCached{time.Now(), v}
}

func (c *replayCache) drop(brand string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, brand)
}

func (s *Server) routesReplay(mux *http.ServeMux) {
	read, manage := auth.ScopeCustomersRead, auth.ScopeCustomersManage
	mux.HandleFunc("GET /v1/brands/{brand}/replay/settings", s.onBrand(read, s.getReplaySettings))
	mux.HandleFunc("PATCH /v1/brands/{brand}/replay/settings", s.onBrand(manage, s.updateReplaySettings))
	mux.HandleFunc("GET /v1/brands/{brand}/recordings", s.onBrand(read, s.listRecordings))
	mux.HandleFunc("GET /v1/brands/{brand}/recordings/{recording}", s.onBrand(read, s.getRecording))
	mux.HandleFunc("GET /v1/brands/{brand}/recordings/{recording}/parts/{part}", s.onBrand(read, s.getRecordingPart))
	mux.HandleFunc("DELETE /v1/brands/{brand}/recordings/{recording}", s.onBrand(manage, s.deleteRecording))
	mux.HandleFunc("POST /v1/brands/{brand}/recordings/delete", s.onBrand(manage, s.deleteRecordings))
	mux.HandleFunc("POST /v1/brands/{brand}/heatmaps/clear", s.onBrand(manage, s.clearHeatmaps))
	mux.HandleFunc("GET /v1/brands/{brand}/heatmaps/pages", s.onBrand(read, s.listHeatmapPages))
	mux.HandleFunc("GET /v1/brands/{brand}/heatmaps", s.onBrand(read, s.getHeatmap))
	mux.HandleFunc("GET /v1/brands/{brand}/heatmaps/snapshot", s.onBrand(read, s.getHeatmapSnapshot))
	// Public: the key in the body is the authority.
	mux.HandleFunc("POST /v1/site/replay", s.siteReplayPart)
	mux.HandleFunc("OPTIONS /v1/site/replay", s.siteEventsPreflight)
	mux.HandleFunc("GET /v1/site/replay.js", s.replayJS)
}

// replayJS serves the recorder.
func (s *Server) replayJS(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/javascript; charset=utf-8")
	h.Set("Cache-Control", "public, max-age=86400")
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	w.Write(replayRecorder)
}

// brandReplaySettings reads a brand's settings; a brand with none has
// everything off.
func (s *Server) brandReplaySettings(ctx context.Context, ws, brand string) (replaySettings, error) {
	v := replaySettings{SamplePercent: 100, KeepDays: 30, IncludePaths: []string{}, ExcludePaths: []string{}, HideShapes: []string{}, Storage: s.recordings != nil}
	err := s.pool.QueryRow(ctx, `SELECT recordings, heatmaps, sample_percent, mask_text, keep_days, include_paths, exclude_paths, min_seconds, daily_limit, hide_shapes FROM replay_settings
		WHERE workspace_id = $1 AND brand_id = $2`, ws, brand).Scan(&v.Recordings, &v.Heatmaps, &v.SamplePercent, &v.MaskText, &v.KeepDays, &v.IncludePaths, &v.ExcludePaths, &v.MinSeconds, &v.DailyLimit, &v.HideShapes)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return v, err
	}
	return v, nil
}

// siteReplayFor is what the script on a brand's site should do, or nil
// when the brand records nothing. Remembered for half a minute.
func (s *Server) siteReplayFor(ctx context.Context, ws string, brand *string) *siteReplay {
	if brand == nil {
		return nil
	}
	v, ok := s.replayCache.get(*brand)
	if !ok {
		var err error
		if v, err = s.brandReplaySettings(ctx, ws, *brand); err != nil {
			return nil
		}
		s.replayCache.put(*brand, v)
	}
	record := v.Recordings && v.Storage
	if !record && !v.Heatmaps {
		return nil
	}
	return &siteReplay{Record: record, Heatmaps: v.Heatmaps, Sample: v.SamplePercent, MaskText: v.MaskText, Include: v.IncludePaths, Exclude: v.ExcludePaths, Hide: v.HideShapes, dailyLimit: v.DailyLimit}
}

func (s *Server) getReplaySettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := s.brandReplaySettings(r.Context(), a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) updateReplaySettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Recordings    *bool     `json:"recordings"`
		Heatmaps      *bool     `json:"heatmaps"`
		SamplePercent *int      `json:"sample_percent"`
		MaskText      *bool     `json:"mask_text"`
		KeepDays      *int      `json:"keep_days"`
		IncludePaths  *[]string `json:"include_paths"`
		ExcludePaths  *[]string `json:"exclude_paths"`
		MinSeconds    *int      `json:"min_seconds"`
		DailyLimit    *int      `json:"daily_limit"`
		HideShapes    *[]string `json:"hide_shapes"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	v, err := s.brandReplaySettings(ctx, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if in.Recordings != nil {
		v.Recordings = *in.Recordings
	}
	if in.Heatmaps != nil {
		v.Heatmaps = *in.Heatmaps
	}
	if in.SamplePercent != nil {
		v.SamplePercent = *in.SamplePercent
	}
	if in.MaskText != nil {
		v.MaskText = *in.MaskText
	}
	if in.KeepDays != nil {
		v.KeepDays = *in.KeepDays
	}
	if in.MinSeconds != nil {
		v.MinSeconds = *in.MinSeconds
	}
	if in.DailyLimit != nil {
		v.DailyLimit = *in.DailyLimit
	}
	for _, l := range []struct {
		in  *[]string
		out *[]string
	}{{in.IncludePaths, &v.IncludePaths}, {in.ExcludePaths, &v.ExcludePaths}} {
		if l.in == nil {
			continue
		}
		clean, err := cleanReplayPaths(*l.in)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		*l.out = clean
	}
	if in.HideShapes != nil {
		clean, err := cleanReplayShapes(*in.HideShapes)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		v.HideShapes = clean
	}
	switch {
	case v.MinSeconds < 0 || v.MinSeconds > 600:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "min_seconds is 0 to 600")
		return
	case v.DailyLimit < 0 || v.DailyLimit > 100000:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "daily_limit is 0 (no limit) to 100000")
		return
	case v.SamplePercent < 1 || v.SamplePercent > 100:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "sample_percent is 1 to 100")
		return
	case v.KeepDays < 1 || v.KeepDays > 90:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "keep_days is 1 to 90")
		return
	case in.Recordings != nil && *in.Recordings && s.recordings == nil:
		writeError(w, r, http.StatusBadRequest, "not_configured", "recordings need object storage, and this server has none: its operator sets OUTFLARE_S3_ENDPOINT, OUTFLARE_S3_BUCKET and the bucket's keys. Heatmaps work without it")
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO replay_settings (workspace_id, brand_id, recordings, heatmaps, sample_percent, mask_text, keep_days, updated_by, include_paths, exclude_paths, min_seconds, daily_limit, hide_shapes)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			ON CONFLICT (brand_id) DO UPDATE SET recordings = EXCLUDED.recordings, heatmaps = EXCLUDED.heatmaps, sample_percent = EXCLUDED.sample_percent,
				mask_text = EXCLUDED.mask_text, keep_days = EXCLUDED.keep_days, updated_by = EXCLUDED.updated_by, updated_at = now(),
				include_paths = EXCLUDED.include_paths, exclude_paths = EXCLUDED.exclude_paths, min_seconds = EXCLUDED.min_seconds, daily_limit = EXCLUDED.daily_limit, hide_shapes = EXCLUDED.hide_shapes`,
			a.WorkspaceID, a.Brand, v.Recordings, v.Heatmaps, v.SamplePercent, v.MaskText, v.KeepDays, nullIfEmpty(a.ActorID), v.IncludePaths, v.ExcludePaths, v.MinSeconds, v.DailyLimit, v.HideShapes); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "replay_settings.updated", "brand", a.Brand, map[string]any{
			"brand_id": a.Brand, "recordings": v.Recordings, "heatmaps": v.Heatmaps, "sample_percent": v.SamplePercent,
			"mask_text": v.MaskText, "keep_days": v.KeepDays, "include_paths": len(v.IncludePaths), "exclude_paths": len(v.ExcludePaths),
			"min_seconds": v.MinSeconds, "daily_limit": v.DailyLimit, "hide_shapes": len(v.HideShapes)})
	})
	if s.recordError(w, r, err) {
		return
	}
	s.replayCache.drop(a.Brand)
	writeJSON(w, http.StatusOK, v)
}

// replayHost is the website a part came from: the host name of the
// request's Origin, or nothing when there is none.
func replayHost(origin string) string {
	u, err := url.Parse(origin)
	if err != nil {
		return ""
	}
	return cutUTF8(strings.ToLower(u.Hostname()), 255)
}

// replayKey is where one part of a recording is kept.
func replayKey(ws, brand, session string, part int) string {
	return fmt.Sprintf("replay/%s/%s/%s/%06d.json.gz", ws, brand, session, part)
}

// heatPath is a page's path as heatmaps and recordings keep it: no query
// string, no fragment, no trailing slash but the root's.
func heatPath(p string) string {
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	if !strings.HasPrefix(p, "/") {
		return ""
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	if p == "" {
		p = "/"
	}
	return cutUTF8(p, 500)
}

// heatmapScreens is the most screens one page's heatmap is split into; a
// page that names more (a heading with the visitor's name in it) keeps
// counting under the page itself.
const heatmapScreens = 12

// heatScreen tidies the name of a screen: one line, at most 60
// characters, without the # that joins it to its page.
func heatScreen(v string) string {
	v = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r == '#' || r < ' ' || r == 0x7f {
			return ' '
		}
		return r
	}, v)), " ")
	return strings.TrimSpace(cutUTF8(v, 60))
}

// heatBase is the page of a stored heatmap path, without its screen.
func heatBase(stored string) string {
	base, _, _ := strings.Cut(stored, "#")
	return base
}

// heatKey is where a page's counts are kept: the page, or the page and
// the screen it showed (/onboarding#Choose a plan). A screen is taken
// while the page has room for it: it has a picture already, or the page
// has fewer than heatmapScreens pictures of screens.
func (s *Server) heatKey(ctx context.Context, ws, brand, path, screen string) (string, error) {
	if screen = heatScreen(screen); screen == "" || path == "" {
		return path, nil
	}
	key := path + "#" + screen
	var known bool
	var screens int
	if err := s.pool.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM heatmap_snapshots WHERE workspace_id = $1 AND brand_id = $2 AND path = $3),
		(SELECT count(DISTINCT path)::int FROM heatmap_snapshots WHERE workspace_id = $1 AND brand_id = $2 AND starts_with(path, $4))`,
		ws, brand, key, path+"#").Scan(&known, &screens); err != nil {
		return "", err
	}
	if !known && screens >= heatmapScreens {
		return path, nil
	}
	return key, nil
}

// siteReplayPart takes one part of a visit's recording and the clicks and
// scroll depth of its page, from the website script. The body is JSON sent
// as text/plain: {"key", "session", "anonymousId", "part", "path",
// "width", "height", "events": [...], "clicks": [[x, y], ...], "depth",
// "page_height"}. The answer says whether to keep going.
func (s *Server) siteReplayPart(w http.ResponseWriter, r *http.Request) {
	siteCORS(w)
	ctx := r.Context()
	outcome := "stored"
	defer func() { metrics.ReplayParts(outcome) }()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxReplayBody))
	if err != nil {
		outcome = "too_large"
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "a part must be under 4 MB")
		return
	}
	var in struct {
		Key         string            `json:"key"`
		Session     string            `json:"session"`
		AnonymousID string            `json:"anonymousId"`
		Part        int               `json:"part"`
		Path        string            `json:"path"`
		Width       int               `json:"width"`
		Height      int               `json:"height"`
		Events      []json.RawMessage `json:"events"`
		Clicks      [][2]int          `json:"clicks"`
		Depth       int               `json:"depth"`
		PageHeight  int               `json:"page_height"`
		// What the page showed when it was clicked, for a page whose
		// address stays the same while its content changes (the steps of
		// a sign-up): the step's or the first heading's text.
		Screen string `json:"screen"`
		// The page as the recorder first sees it, for the heatmap's
		// picture: sent only after an answer asked for it.
		Snapshot []json.RawMessage `json:"snapshot"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		outcome = "refused"
		writeError(w, r, http.StatusBadRequest, "invalid_request", `send {"key": "ofk_...", "session": "...", "part": 0, "path": "/", "events": [...]}`)
		return
	}
	conn := clientIP(r)
	if s.siteIPLimit.over(conn) {
		outcome = "refused"
		s.siteIPLimit.allow(w, r, conn)
		return
	}
	var k siteKeyRow
	if strings.HasPrefix(in.Key, auth.PrefixSiteKey) && len(in.Key) <= 100 {
		err = s.pool.QueryRow(ctx, `SELECT id::text, workspace_id::text, brand_id::text, origins, proxy_secret_hash FROM site_keys
			WHERE revoked_at IS NULL AND (key = $1 OR (previous_key = $1 AND previous_expires_at > now()))`, in.Key).
			Scan(&k.id, &k.workspaceID, &k.brandID, &k.origins, &k.proxyHash)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			outcome = "error"
			s.internal(w, r, err)
			return
		}
	}
	if k.id == "" {
		outcome = "refused"
		if !s.siteIPLimit.allow(w, r, conn) {
			return
		}
		writeError(w, r, http.StatusUnauthorized, "unauthenticated", "the site key is not valid: copy the snippet again from the workspace's website tracking settings")
		return
	}
	if !s.siteOriginOK(r, k, r.Header.Get("Origin")) {
		outcome = "refused"
		writeError(w, r, http.StatusForbidden, "forbidden", "this site key does not take requests from this origin; add it to the key's origins")
		return
	}
	if !s.siteReplayLimit.allow(w, r, k.id) {
		outcome = "refused"
		return
	}
	stop := map[string]bool{"recording": false, "heatmaps": false}
	ua := r.Header.Get("User-Agent")
	cfg := s.siteReplayFor(ctx, k.workspaceID, k.brandID)
	if cfg == nil || isBotUA(ua) {
		outcome = "off"
		writeJSON(w, http.StatusOK, stop)
		return
	}
	brand := *k.brandID
	path := heatPath(in.Path)
	device, browser, os := parseUA(ua)
	if device == "" {
		device = "desktop"
	}
	anon := strings.TrimSpace(in.AnonymousID)
	if len(in.Events) > 0 && (!replaySessionRe.MatchString(in.Session) || in.Part < 0 || len(anon) > 200) {
		outcome = "refused"
		writeError(w, r, http.StatusBadRequest, "invalid_request", "session is the visit's id from the script, and part counts up from 0")
		return
	}
	out := map[string]bool{"recording": false, "heatmaps": cfg.Heatmaps}
	if path != "" && !cfg.pageOn(path) {
		// A page the brand leaves out: nothing of it is kept, and the
		// visit's recording carries on with its next page.
		outcome = "off"
		out["recording"] = cfg.Record
		writeJSON(w, http.StatusOK, out)
		return
	}
	if cfg.Heatmaps && path != "" {
		heat, err := s.heatKey(ctx, k.workspaceID, brand, path, in.Screen)
		if err != nil {
			outcome = "error"
			s.internal(w, r, err)
			return
		}
		if err := s.saveHeat(ctx, k.workspaceID, brand, heat, device, in.Clicks, in.Depth, in.PageHeight); err != nil {
			outcome = "error"
			s.internal(w, r, err)
			return
		}
		// A page being clicked on gets a picture for its heatmap.
		if len(in.Clicks) > 0 || len(in.Snapshot) > 0 {
			want, err := s.heatSnapshot(ctx, k.workspaceID, brand, heat, device, clampInt(in.Width, 0, 20000), in.Snapshot, cfg.Hide)
			if err != nil {
				outcome = "error"
				s.internal(w, r, err)
				return
			}
			if want {
				out["snapshot"] = true
			}
		}
	}
	if !cfg.Record || len(in.Events) == 0 {
		if len(in.Events) == 0 {
			outcome = "heat_only"
			out["recording"] = cfg.Record
		} else {
			outcome = "off"
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	if in.Part >= replayMaxParts {
		outcome = "capped"
		writeJSON(w, http.StatusOK, out)
		return
	}
	// The recording so far, and the workspace's day.
	var bytesSoFar, today int64
	var visitsToday int
	var startedAt *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT
		coalesce((SELECT bytes FROM session_recordings WHERE workspace_id = $1 AND brand_id = $2 AND session_id = $3), 0),
		(SELECT started_at FROM session_recordings WHERE workspace_id = $1 AND brand_id = $2 AND session_id = $3),
		(SELECT coalesce(sum(bytes), 0)::bigint FROM session_recordings WHERE workspace_id = $1 AND started_at >= date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc'),
		(SELECT count(*)::int FROM session_recordings WHERE workspace_id = $1 AND brand_id = $2 AND started_at >= date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc')`,
		k.workspaceID, brand, in.Session).Scan(&bytesSoFar, &startedAt, &today, &visitsToday); err != nil {
		outcome = "error"
		s.internal(w, r, err)
		return
	}
	events, _ := json.Marshal(in.Events)
	events = hideReplayShapes(hideReplaySecrets(events), cfg.Hide)
	size := int64(len(events))
	if startedAt == nil && cfg.dailyLimit > 0 && visitsToday >= cfg.dailyLimit {
		// The brand's own limit for the day: this visit isn't recorded.
		outcome = "capped"
		writeJSON(w, http.StatusOK, out)
		return
	}
	if bytesSoFar+size > replayMaxBytes || today+size > replayWorkspaceDailyBytes || (startedAt != nil && time.Since(*startedAt) > replayMaxAge) {
		// Over a limit: the script stops recording this visit. A part
		// whose earlier parts never arrived (a page left before it was
		// sent) is kept: every page load starts with the whole page, so
		// the recording plays from there.
		outcome = "capped"
		writeJSON(w, http.StatusOK, out)
		return
	}
	var zipped bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	zw.Write(events)
	zw.Close()
	if err := s.recordings.Put(ctx, replayKey(k.workspaceID, brand, in.Session, in.Part), "application/gzip", zipped.Bytes()); err != nil {
		outcome = "error"
		track.Fail(ctx, s.log, track.Event{Name: "recording.store_failed", WorkspaceID: k.workspaceID, ActorType: "system", Surface: "system"}, "unavailable", err)
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "the recording could not be stored; try again")
		return
	}
	k.setPlace(visitorPlace(r, k))
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_recordings (workspace_id, brand_id, session_id, anonymous_id, parts, bytes, clicks, paths, device, browser, os, country, width, height, host)
		VALUES ($1, $2, $3, $4, $5, $6, $7, CASE WHEN $8 = '' THEN '{}'::text[] ELSE ARRAY[$8] END, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (brand_id, session_id) DO UPDATE SET last_at = now(), host = CASE WHEN session_recordings.host = '' THEN EXCLUDED.host ELSE session_recordings.host END, parts = greatest(session_recordings.parts, EXCLUDED.parts),
			bytes = session_recordings.bytes + EXCLUDED.bytes, clicks = session_recordings.clicks + EXCLUDED.clicks,
			paths = CASE WHEN $8 = '' OR $8 = ANY (session_recordings.paths) OR cardinality(session_recordings.paths) >= 50
				THEN session_recordings.paths ELSE session_recordings.paths || $8 END`,
		k.workspaceID, brand, in.Session, anon, in.Part+1, size, len(in.Clicks), path, device, cutUTF8(browser, 40), cutUTF8(os, 40), k.country,
		clampInt(in.Width, 0, 20000), clampInt(in.Height, 0, 20000), replayHost(r.Header.Get("Origin"))); err != nil {
		outcome = "error"
		s.internal(w, r, err)
		return
	}
	metrics.ReplayBytes(int(size))
	out["recording"] = true
	writeJSON(w, http.StatusOK, out)
}

func clampInt(n, lo, hi int) int {
	return max(lo, min(n, hi))
}

// saveHeat adds a page's clicks and scroll depth to today's counts.
func (s *Server) saveHeat(ctx context.Context, ws, brand, path, device string, clicks [][2]int, depth, pageHeight int) error {
	if len(clicks) > heatmapClicksPerPart {
		clicks = clicks[:heatmapClicksPerPart]
	}
	// One row per spot: a batch that clicks the same spot twice adds two.
	spots := map[[2]int]int{}
	for _, c := range clicks {
		if c[0] < 0 || c[0] > 99 || c[1] < 0 || c[1] > 5000 {
			continue
		}
		spots[c]++
	}
	if len(spots) == 0 && (depth < 1 || depth > 10) {
		return nil
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		xs, ys, ns := make([]int32, 0, len(spots)), make([]int32, 0, len(spots)), make([]int64, 0, len(spots))
		for c, n := range spots {
			xs, ys, ns = append(xs, int32(c[0])), append(ys, int32(c[1])), append(ns, int64(n))
		}
		if len(xs) > 0 {
			// Ordered, so two requests for one page never wait on each other.
			if _, err := tx.Exec(ctx, `INSERT INTO heatmap_clicks (workspace_id, brand_id, path, day, device, x, y, clicks)
				SELECT $1, $2, $3, (now() AT TIME ZONE 'utc')::date, $4, x, y, n FROM unnest($5::int[], $6::int[], $7::bigint[]) AS t (x, y, n) ORDER BY x, y
				ON CONFLICT (brand_id, path, day, device, x, y) DO UPDATE SET clicks = heatmap_clicks.clicks + EXCLUDED.clicks`,
				ws, brand, path, device, xs, ys, ns); err != nil {
				return err
			}
			metrics.HeatmapClicks(len(clicks))
		}
		if depth >= 1 && depth <= 10 {
			if _, err := tx.Exec(ctx, `INSERT INTO heatmap_scrolls (workspace_id, brand_id, path, day, device, depth, views, height)
				VALUES ($1, $2, $3, (now() AT TIME ZONE 'utc')::date, $4, $5, 1, $6)
				ON CONFLICT (brand_id, path, day, device, depth) DO UPDATE SET views = heatmap_scrolls.views + 1,
					height = greatest(heatmap_scrolls.height, EXCLUDED.height)`,
				ws, brand, path, device, depth, clampInt(pageHeight/10, 0, 5000)); err != nil {
				return err
			}
		}
		return nil
	})
}

// heatSnapshot keeps a page's picture when the browser sent one and the
// page has none or an old one, and says whether the browser should send
// one: a page with clicks counted and no picture less than a week old.
func (s *Server) heatSnapshot(ctx context.Context, ws, brand, path, device string, width int, snap []json.RawMessage, hide []string) (bool, error) {
	var fresh bool
	var pages int
	if err := s.pool.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM heatmap_snapshots WHERE workspace_id = $1 AND brand_id = $2 AND path = $3 AND device = $4 AND taken_at > now() - make_interval(secs => $5)),
		(SELECT count(*)::int FROM heatmap_snapshots WHERE workspace_id = $1 AND brand_id = $2 AND NOT (path = $3 AND device = $4))`,
		ws, brand, path, device, heatmapSnapshotRefresh.Seconds()).Scan(&fresh, &pages); err != nil {
		return false, err
	}
	if fresh || pages >= heatmapSnapshotsBrand {
		return false, nil
	}
	if len(snap) != 2 {
		return true, nil
	}
	// The recorder's note of the page's size, then the whole page.
	var kinds [2]struct {
		Type int `json:"type"`
	}
	for i, e := range snap {
		if json.Unmarshal(e, &kinds[i]) != nil {
			return false, nil
		}
	}
	if kinds[0].Type != 4 || kinds[1].Type != 2 {
		return false, nil
	}
	raw, _ := json.Marshal(snap)
	raw = hideReplayShapes(hideReplaySecrets(raw), hide)
	var zipped bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	zw.Write(raw)
	zw.Close()
	if zipped.Len() > heatmapSnapshotBytes {
		// Too big a page to keep: its heatmap is drawn on a plain sheet.
		return false, nil
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO heatmap_snapshots (workspace_id, brand_id, path, device, width, body) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (brand_id, path, device) DO UPDATE SET width = EXCLUDED.width, body = EXCLUDED.body, taken_at = now()`,
		ws, brand, path, device, width, zipped.Bytes())
	return false, err
}

type recordingView struct {
	ID          string `json:"id"`
	AnonymousID string `json:"anonymous_id"`
	// The visitor's customer record, once they are known.
	RecordID *string `json:"record_id"`
	// That person's name, or their email address when they have no name.
	RecordName *string `json:"record_name"`
	// The website the visit was on; empty for older recordings.
	Host      string     `json:"host"`
	StartedAt time.Time  `json:"started_at"`
	Seconds   int        `json:"seconds"`
	Parts     int        `json:"parts"`
	Bytes     int64      `json:"bytes"`
	Clicks    int        `json:"clicks"`
	Paths     []string   `json:"paths"`
	Device    string     `json:"device"`
	Browser   string     `json:"browser"`
	OS        string     `json:"os"`
	Country   string     `json:"country"`
	Width     int        `json:"width"`
	Height    int        `json:"height"`
	ViewedAt  *time.Time `json:"viewed_at"`
}

const recordingColumns = `r.id::text, r.anonymous_id,
	(SELECT sv.record_id::text FROM site_visitors sv WHERE sv.workspace_id = r.workspace_id AND sv.anonymous_id = r.anonymous_id AND r.anonymous_id <> '' LIMIT 1),
	(SELECT coalesce(nullif(rc.name, ''), rc.attributes->>'email') FROM site_visitors sv JOIN records rc ON rc.workspace_id = sv.workspace_id AND rc.id = sv.record_id
		WHERE sv.workspace_id = r.workspace_id AND sv.anonymous_id = r.anonymous_id AND r.anonymous_id <> '' LIMIT 1),
	r.host, r.started_at, greatest(extract(epoch FROM r.last_at - r.started_at), 0)::int, r.parts, r.bytes, r.clicks, r.paths, r.device, r.browser, r.os, r.country, r.width, r.height, r.viewed_at`

func scanRecording(row pgx.Row, v *recordingView) error {
	return row.Scan(&v.ID, &v.AnonymousID, &v.RecordID, &v.RecordName, &v.Host, &v.StartedAt, &v.Seconds, &v.Parts, &v.Bytes, &v.Clicks, &v.Paths, &v.Device, &v.Browser, &v.OS, &v.Country, &v.Width, &v.Height, &v.ViewedAt)
}

func replayDays(w http.ResponseWriter, r *http.Request, def, most int) (int, bool) {
	v := r.URL.Query().Get("days")
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > most {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "days is 1 to "+strconv.Itoa(most))
		return 0, false
	}
	return n, true
}

func deviceParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	switch d := r.URL.Query().Get("device"); d {
	case "", "desktop", "tablet", "mobile":
		return d, true
	}
	writeError(w, r, http.StatusBadRequest, "invalid_request", "device must be desktop, tablet or mobile")
	return "", false
}

func (s *Server) listRecordings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	days, ok := replayDays(w, r, 30, 90)
	if !ok {
		return
	}
	device, ok := deviceParam(w, r)
	if !ok {
		return
	}
	limit, err := limitParam(r, 50, 200)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	offset, ok := pageOffsetParam(w, r)
	if !ok {
		return
	}
	minSeconds := 0
	if v := q.Get("min_seconds"); v != "" {
		if minSeconds, err = strconv.Atoi(v); err != nil || minSeconds < 0 || minSeconds > 86400 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "min_seconds is 0 to 86400")
			return
		}
	}
	record := q.Get("record_id")
	if record != "" && !validUUID(record) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "record_id must be a record's id")
		return
	}
	path := ""
	if q.Get("path") != "" {
		if path = heatPath(q.Get("path")); path == "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "path starts with /")
			return
		}
	}
	host := strings.ToLower(strings.TrimSpace(q.Get("host")))
	visitor := q.Get("visitor")
	if len(host) > 255 || (visitor != "" && visitor != "known" && visitor != "anonymous") {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "host is a website's host name, and visitor is known or anonymous")
		return
	}
	// The websites with a recording in the period, for the picker.
	siteRows, err := s.pool.Query(r.Context(), `SELECT DISTINCT host FROM session_recordings
		WHERE workspace_id = $1 AND brand_id = $2 AND deleted_at IS NULL AND host <> '' AND started_at > now() - make_interval(days => $3) ORDER BY host LIMIT 50`, a.WorkspaceID, a.Brand, days)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	sites, err := pgx.CollectRows(siteRows, pgx.RowTo[string])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT `+recordingColumns+` FROM session_recordings r
		WHERE r.workspace_id = $1 AND r.brand_id = $2 AND r.deleted_at IS NULL AND r.started_at > now() - make_interval(days => $3)
		AND ($4 = '' OR r.device = $4) AND ($5 = '' OR r.paths @> ARRAY[$5])
		AND extract(epoch FROM r.last_at - r.started_at) >= $6
		AND (r.last_at > now() - interval '30 minutes' OR extract(epoch FROM r.last_at - r.started_at) >= coalesce((SELECT rs.min_seconds FROM replay_settings rs WHERE rs.workspace_id = r.workspace_id AND rs.brand_id = r.brand_id), 0))
		AND ($7 = '' OR r.anonymous_id IN (SELECT sv.anonymous_id FROM site_visitors sv WHERE sv.workspace_id = $1 AND sv.record_id = NULLIF($7, '')::uuid))
		AND ($10 = '' OR r.host = $10)
		AND ($11 = '' OR ($11 = 'known') = EXISTS (SELECT 1 FROM site_visitors sv WHERE sv.workspace_id = r.workspace_id AND sv.anonymous_id = r.anonymous_id AND r.anonymous_id <> '' AND sv.record_id IS NOT NULL))
		ORDER BY r.started_at DESC, r.id LIMIT $8 OFFSET $9`, a.WorkspaceID, a.Brand, days, device, path, minSeconds, record, limit+1, offset, host, visitor)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (recordingView, error) {
		var v recordingView
		return v, scanRecording(row, &v)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *int
	if len(list) > limit {
		list = list[:limit]
		n := offset + limit
		next = &n
	}
	if sites == nil {
		sites = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": nextCursor(pageOffset, next), "sites": sites})
}

// oneRecording loads the recording the path names, with its session id.
func (s *Server) oneRecording(w http.ResponseWriter, r *http.Request) (recordingView, string, bool) {
	a := accessFrom(r.Context())
	var v recordingView
	var session string
	id := r.PathValue("recording")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such recording")
		return v, "", false
	}
	row := s.pool.QueryRow(r.Context(), `SELECT r.session_id, `+recordingColumns+` FROM session_recordings r
		WHERE r.workspace_id = $1 AND r.brand_id = $2 AND r.id = $3 AND r.deleted_at IS NULL`, a.WorkspaceID, a.Brand, id)
	err := row.Scan(&session, &v.ID, &v.AnonymousID, &v.RecordID, &v.RecordName, &v.Host, &v.StartedAt, &v.Seconds, &v.Parts, &v.Bytes, &v.Clicks, &v.Paths, &v.Device, &v.Browser, &v.OS, &v.Country, &v.Width, &v.Height, &v.ViewedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such recording")
		return v, "", false
	}
	if err != nil {
		s.internal(w, r, err)
		return v, "", false
	}
	return v, session, true
}

func (s *Server) getRecording(w http.ResponseWriter, r *http.Request) {
	v, _, ok := s.oneRecording(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// getRecordingPart answers one part's events, a JSON array, as the browser
// recorded them. Reading the first part marks the recording as watched.
func (s *Server) getRecordingPart(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, session, ok := s.oneRecording(w, r)
	if !ok {
		return
	}
	part, err := strconv.Atoi(r.PathValue("part"))
	if err != nil || part < 0 || part >= v.Parts {
		writeError(w, r, http.StatusNotFound, "not_found", "the recording has no such part")
		return
	}
	if s.recordings == nil {
		writeError(w, r, http.StatusServiceUnavailable, "not_configured", "this server has no object storage, so it can't read recordings")
		return
	}
	ctx := r.Context()
	raw, err := s.recordings.Get(ctx, replayKey(a.WorkspaceID, a.Brand, session, part))
	events := []byte("[]")
	switch {
	case errors.Is(err, blobs.ErrNotFound):
		// A part that never arrived (the visitor's connection dropped):
		// the player skips it.
	case err != nil:
		s.internal(w, r, err)
		return
	default:
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err == nil {
			events, err = io.ReadAll(io.LimitReader(zr, 64<<20))
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
	}
	if part == 0 && v.ViewedAt == nil {
		if _, err := s.pool.Exec(ctx, `UPDATE session_recordings SET viewed_at = now() WHERE workspace_id = $1 AND brand_id = $2 AND id = $3 AND viewed_at IS NULL`,
			a.WorkspaceID, a.Brand, v.ID); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"part": part, "events": json.RawMessage(events)})
}

// deleteRecording hides a recording at once; the sweep removes its file
// and then its row.
func (s *Server) deleteRecording(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, _, ok := s.oneRecording(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE session_recordings SET deleted_at = now() WHERE workspace_id = $1 AND brand_id = $2 AND id = $3`,
			a.WorkspaceID, a.Brand, v.ID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "recording.deleted", "recording", v.ID, map[string]any{"brand_id": a.Brand, "parts": v.Parts})
	})
	if s.recordError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type heatmapPageView struct {
	Path   string `json:"path"`
	Clicks int64  `json:"clicks"`
	Views  int64  `json:"views"`
	// How many screens the page's clicks are split into, when its content
	// changes while its address stays the same; 0 or 1 for a plain page.
	Screens int `json:"screens"`
}

// deleteRecordings hides many recordings at once: the ones named, or
// every one that started more than a number of days ago (0 is all of
// them). The sweep removes their files and then their rows.
func (s *Server) deleteRecordings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		IDs           []string `json:"ids"`
		OlderThanDays *int     `json:"older_than_days"`
	}
	if !decode(w, r, &in) {
		return
	}
	switch {
	case (len(in.IDs) > 0) == (in.OlderThanDays != nil):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send ids, or older_than_days (0 deletes every recording), not both")
		return
	case len(in.IDs) > 500:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name at most 500 recordings at a time")
		return
	case in.OlderThanDays != nil && (*in.OlderThanDays < 0 || *in.OlderThanDays > 90):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "older_than_days is 0 to 90")
		return
	}
	for _, id := range in.IDs {
		if !validUUID(id) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "ids are recordings' ids")
			return
		}
	}
	ctx := r.Context()
	var n int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var tag pgconn.CommandTag
		var err error
		if in.OlderThanDays != nil {
			tag, err = tx.Exec(ctx, `UPDATE session_recordings SET deleted_at = now() WHERE workspace_id = $1 AND brand_id = $2 AND deleted_at IS NULL
				AND started_at <= now() - make_interval(days => $3)`, a.WorkspaceID, a.Brand, *in.OlderThanDays)
		} else {
			tag, err = tx.Exec(ctx, `UPDATE session_recordings SET deleted_at = now() WHERE workspace_id = $1 AND brand_id = $2 AND deleted_at IS NULL
				AND id = ANY ($3::uuid[])`, a.WorkspaceID, a.Brand, in.IDs)
		}
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
		meta := map[string]any{"brand_id": a.Brand, "count": n, "by": "ids"}
		if in.OlderThanDays != nil {
			meta["by"], meta["older_than_days"] = "age", *in.OlderThanDays
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "recordings.bulk_deleted", "brand", a.Brand, meta)
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n})
}

// clearHeatmaps deletes what has been counted for one page, or for every
// page of the brand, so its heatmaps start again from nothing.
func (s *Server) clearHeatmaps(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Path string `json:"path"`
	}
	if !decode(w, r, &in) {
		return
	}
	path := ""
	if in.Path != "" {
		if path = heatPath(in.Path); path == "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "path starts with /")
			return
		}
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, t := range []string{"heatmap_clicks", "heatmap_scrolls", "heatmap_snapshots"} {
			if _, err := tx.Exec(ctx, `DELETE FROM `+t+` WHERE workspace_id = $1 AND brand_id = $2 AND ($3 = '' OR path = $3 OR starts_with(path, $3 || '#'))`, a.WorkspaceID, a.Brand, path); err != nil {
				return err
			}
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "heatmaps.cleared", "brand", a.Brand, map[string]any{"brand_id": a.Brand, "every_page": path == ""})
	})
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cleared": true})
}

// listHeatmapPages is the brand's pages with the most clicks.
func (s *Server) listHeatmapPages(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	days, ok := replayDays(w, r, 30, heatmapKeepDays)
	if !ok {
		return
	}
	device, ok := deviceParam(w, r)
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), `WITH c AS (
			SELECT split_part(path, '#', 1) AS path, sum(clicks)::bigint AS clicks, count(DISTINCT path) FILTER (WHERE position('#' in path) > 0)::int AS screens FROM heatmap_clicks
			WHERE workspace_id = $1 AND brand_id = $2 AND day > (now() AT TIME ZONE 'utc')::date - $3::int AND ($4 = '' OR device = $4) GROUP BY 1),
		v AS (
			SELECT split_part(path, '#', 1) AS path, sum(views)::bigint AS views FROM heatmap_scrolls
			WHERE workspace_id = $1 AND brand_id = $2 AND day > (now() AT TIME ZONE 'utc')::date - $3::int AND ($4 = '' OR device = $4) GROUP BY 1)
		SELECT coalesce(c.path, v.path), coalesce(c.clicks, 0), coalesce(v.views, 0), coalesce(c.screens, 0) FROM c FULL JOIN v ON v.path = c.path
		ORDER BY coalesce(c.clicks, 0) DESC, coalesce(v.views, 0) DESC, 1 LIMIT 200`, a.WorkspaceID, a.Brand, days, device)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (heatmapPageView, error) {
		var v heatmapPageView
		return v, row.Scan(&v.Path, &v.Clicks, &v.Views, &v.Screens)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "days": days})
}

type heatmapView struct {
	Path   string `json:"path"`
	Days   int    `json:"days"`
	Device string `json:"device"`
	// Each spot is [x, y, clicks]: x in percent across the page, y in
	// pixels from the top of the page.
	Clicks      [][3]int64 `json:"clicks"`
	TotalClicks int64      `json:"total_clicks"`
	// Views that got at least this far down, by tenth of the page.
	Scroll []heatmapScroll `json:"scroll"`
	Views  int64           `json:"views"`
	// The page's height in pixels, the tallest it was.
	Height int `json:"height"`
	// A recent recording that starts on this page, to draw the page behind
	// the heatmap. Null when there is none.
	RecordingID *string `json:"recording_id"`
	// When the page's own picture for the heatmap was taken (read it at
	// /heatmaps/snapshot). Null when a visitor's browser hasn't sent one.
	SnapshotAt *time.Time `json:"snapshot_at"`
	// The screen shown, of a page whose content changes while its address
	// stays the same, and every screen counted, most clicked first. A
	// screen named "" is clicks from before screens were told apart.
	// Screens is empty for a plain page.
	Screen  string          `json:"screen"`
	Screens []heatmapScreen `json:"screens"`
}

type heatmapScreen struct {
	Screen string `json:"screen"`
	Clicks int64  `json:"clicks"`
}

// heatPaths is the stored paths one heatmap reads: the page and its one
// screen together, or the screen asked for, with the page's screens. A
// page with two or more screens shows its most clicked one unless asked.
func (s *Server) heatPaths(ctx context.Context, ws, brand, path, device string, days int, q url.Values) ([]string, string, []heatmapScreen, error) {
	rows, err := s.pool.Query(ctx, `SELECT path, sum(clicks)::bigint FROM heatmap_clicks
		WHERE workspace_id = $1 AND brand_id = $2 AND (path = $3 OR starts_with(path, $3 || '#')) AND ($4 = '' OR device = $4) AND day > (now() AT TIME ZONE 'utc')::date - $5::int
		GROUP BY path ORDER BY 2 DESC, 1`, ws, brand, path, device, days)
	if err != nil {
		return nil, "", nil, err
	}
	all, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (heatmapScreen, error) {
		var v heatmapScreen
		var stored string
		if err := row.Scan(&stored, &v.Clicks); err != nil {
			return v, err
		}
		_, v.Screen, _ = strings.Cut(stored, "#")
		return v, nil
	})
	if err != nil {
		return nil, "", nil, err
	}
	named := 0
	for _, v := range all {
		if v.Screen != "" {
			named++
		}
	}
	if q.Has("screen") {
		screen := heatScreen(q.Get("screen"))
		if screen == "" {
			return []string{path}, "", all, nil
		}
		return []string{path + "#" + screen}, screen, all, nil
	}
	if named < 2 {
		// A plain page: its counts from before and after screens were
		// named are one heatmap.
		paths := []string{path}
		screen := ""
		for _, v := range all {
			if v.Screen != "" {
				paths = append(paths, path+"#"+v.Screen)
				screen = v.Screen
			}
		}
		return paths, screen, []heatmapScreen{}, nil
	}
	for _, v := range all {
		if v.Screen != "" {
			return []string{path + "#" + v.Screen}, v.Screen, all, nil
		}
	}
	return []string{path}, "", all, nil
}

type heatmapScroll struct {
	Depth int   `json:"depth"`
	Views int64 `json:"views"`
}

// getHeatmap is one page's clicks and scroll depth.
func (s *Server) getHeatmap(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	days, ok := replayDays(w, r, 30, heatmapKeepDays)
	if !ok {
		return
	}
	device, ok := deviceParam(w, r)
	if !ok {
		return
	}
	if device == "" {
		device = "desktop"
	}
	path := heatPath(r.URL.Query().Get("path"))
	if path == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "path is the page's path, such as /pricing")
		return
	}
	ctx := r.Context()
	out := heatmapView{Path: path, Days: days, Device: device, Clicks: [][3]int64{}, Scroll: []heatmapScroll{}}
	paths, screen, screens, err := s.heatPaths(ctx, a.WorkspaceID, a.Brand, path, device, days, r.URL.Query())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out.Screen, out.Screens = screen, screens
	rows, err := s.pool.Query(ctx, `SELECT x, y * 10, sum(clicks)::bigint FROM heatmap_clicks
		WHERE workspace_id = $1 AND brand_id = $2 AND path = ANY ($3) AND device = $4 AND day > (now() AT TIME ZONE 'utc')::date - $5::int
		GROUP BY x, y ORDER BY sum(clicks) DESC, x, y LIMIT 5000`, a.WorkspaceID, a.Brand, paths, device, days)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for rows.Next() {
		var c [3]int64
		if err := rows.Scan(&c[0], &c[1], &c[2]); err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		out.Clicks = append(out.Clicks, c)
		out.TotalClicks += c[2]
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	// A view counted at depth 7 also got to 1 through 6.
	byDepth := map[int]int64{}
	rows, err = s.pool.Query(ctx, `SELECT depth, sum(views)::bigint, max(height) * 10 FROM heatmap_scrolls
		WHERE workspace_id = $1 AND brand_id = $2 AND path = ANY ($3) AND device = $4 AND day > (now() AT TIME ZONE 'utc')::date - $5::int
		GROUP BY depth`, a.WorkspaceID, a.Brand, paths, device, days)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for rows.Next() {
		var d, h int
		var n int64
		if err := rows.Scan(&d, &n, &h); err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		byDepth[d] = n
		out.Views += n
		out.Height = max(out.Height, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}
	var reached int64
	for d := 10; d >= 1; d-- {
		reached += byDepth[d]
		out.Scroll = append([]heatmapScroll{{Depth: d, Views: reached}}, out.Scroll...)
	}
	if err := s.pool.QueryRow(ctx, `SELECT taken_at FROM heatmap_snapshots WHERE workspace_id = $1 AND brand_id = $2 AND path = ANY ($3) AND device = $4 ORDER BY taken_at DESC LIMIT 1`,
		a.WorkspaceID, a.Brand, paths, device).Scan(&out.SnapshotAt); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.internal(w, r, err)
		return
	}
	if s.recordings != nil && out.SnapshotAt == nil && len(screens) == 0 {
		var id string
		err := s.pool.QueryRow(ctx, `SELECT id::text FROM session_recordings
			WHERE workspace_id = $1 AND brand_id = $2 AND deleted_at IS NULL AND device = $3 AND $4 = ANY (paths) AND parts > 0
			ORDER BY (paths[1] = $4) DESC, started_at DESC LIMIT 1`, a.WorkspaceID, a.Brand, device, path).Scan(&id)
		if err == nil {
			out.RecordingID = &id
		} else if !errors.Is(err, pgx.ErrNoRows) {
			s.internal(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// getHeatmapSnapshot is the picture of a page behind its heatmap: the
// recorder's two events (the page's size, then its whole document), which
// a player draws.
func (s *Server) getHeatmapSnapshot(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	device, ok := deviceParam(w, r)
	if !ok {
		return
	}
	if device == "" {
		device = "desktop"
	}
	path := heatPath(r.URL.Query().Get("path"))
	if path == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "path is the page's path, such as /pricing")
		return
	}
	var raw []byte
	var width int
	var at time.Time
	// The screen asked for, or the page's newest picture.
	paths, _, _, err := s.heatPaths(r.Context(), a.WorkspaceID, a.Brand, path, device, heatmapKeepDays, r.URL.Query())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	err = s.pool.QueryRow(r.Context(), `SELECT body, width, taken_at FROM heatmap_snapshots WHERE workspace_id = $1 AND brand_id = $2 AND path = ANY ($3) AND device = $4 ORDER BY taken_at DESC LIMIT 1`,
		a.WorkspaceID, a.Brand, paths, device).Scan(&raw, &width, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "this page has no picture yet: one is taken the next time a visitor clicks on it")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	var events []byte
	if err == nil {
		events, err = io.ReadAll(io.LimitReader(zr, 64<<20))
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "device": device, "width": width, "taken_at": at, "events": json.RawMessage(events)})
}

// RunReplaySweep deletes recordings a person deleted, that are past their
// brand's keep_days or that ended shorter than its min_seconds, file
// first, and heatmap counts past 90 days.
func (s *Server) RunReplaySweep(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		if _, err := s.SweepReplay(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.log, track.Event{Name: "recording.sweep_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// SweepReplay does one round of RunReplaySweep and returns how many
// recordings it removed.
func (s *Server) SweepReplay(ctx context.Context) (int, error) {
	for _, t := range []string{"heatmap_clicks", "heatmap_scrolls"} {
		if _, err := s.pool.Exec(ctx, `DELETE FROM `+t+` WHERE day < (now() AT TIME ZONE 'utc')::date - $1::int`, heatmapKeepDays); err != nil {
			return 0, err
		}
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM heatmap_snapshots WHERE taken_at < now() - make_interval(days => $1)`, heatmapKeepDays); err != nil {
		return 0, err
	}
	var waiting int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM session_recordings WHERE deleted_at IS NOT NULL`).Scan(&waiting); err != nil {
		return 0, err
	}
	metrics.ReplayWaitingDelete(waiting)
	if s.recordings == nil {
		return 0, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT r.id::text, r.workspace_id::text, r.brand_id::text, r.session_id, r.parts FROM session_recordings r
		LEFT JOIN replay_settings rs ON rs.brand_id = r.brand_id
		WHERE r.deleted_at IS NOT NULL OR r.started_at < now() - make_interval(days => coalesce(rs.keep_days, 30))
			OR (r.last_at < now() - interval '30 minutes' AND extract(epoch FROM r.last_at - r.started_at) < coalesce(rs.min_seconds, 0))
		ORDER BY r.started_at LIMIT 500`)
	if err != nil {
		return 0, err
	}
	type gone struct {
		id, ws, brand, session string
		parts                  int
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (gone, error) {
		var g gone
		return g, row.Scan(&g.id, &g.ws, &g.brand, &g.session, &g.parts)
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, g := range list {
		for p := 0; p < g.parts; p++ {
			if err := s.recordings.Delete(ctx, replayKey(g.ws, g.brand, g.session, p)); err != nil && !errors.Is(err, blobs.ErrNotFound) {
				return n, err
			}
		}
		if _, err := s.pool.Exec(ctx, `DELETE FROM session_recordings WHERE workspace_id = $1 AND id = $2`, g.ws, g.id); err != nil {
			return n, err
		}
		n++
	}
	metrics.ReplayDeleted(n)
	return n, nil
}
