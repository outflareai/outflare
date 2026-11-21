package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-sourcemap/sourcemap"
	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// Source maps (docs/error-tracking.md): a brand uploads the source map of
// each built file, and the Errors page shows every stack in the brand's
// own file names, lines and function names. A map is kept without the
// source code it may carry (sourcesContent): only names and positions are
// needed, and a brand's code isn't Outflare's to keep. Stacks are read
// through the maps when an issue is opened, so a map uploaded after the
// error still explains it, and nothing stored is rewritten.

const (
	// sourceMapUpload bounds one upload's body; sourceMapBytes bounds the
	// map as kept (gzip, without sourcesContent).
	sourceMapUpload = 64 << 20
	sourceMapBytes  = 8 << 20
	// sourceMapsABrand is how many a brand keeps; the oldest go first.
	sourceMapsABrand = 300
)

func (s *Server) routesSourceMaps(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeAnalyticsManage
	mux.HandleFunc("GET /v1/brands/{brand}/source-maps", s.onBrand(read, s.listSourceMaps))
	mux.HandleFunc("POST /v1/brands/{brand}/source-maps", s.onBrand(manage, s.uploadSourceMap))
	mux.HandleFunc("DELETE /v1/brands/{brand}/source-maps/{id}", s.onBrand(manage, s.deleteSourceMap))
}

type sourceMapView struct {
	ID        string    `json:"id"`
	File      string    `json:"file"`
	Release   string    `json:"release"`
	Sources   int       `json:"sources"`
	Bytes     int       `json:"bytes"`
	CreatedAt time.Time `json:"created_at"`
}

const sourceMapCols = `id::text, file, release, sources, bytes, created_at`

func scanSourceMap(row pgx.Row) (sourceMapView, error) {
	var v sourceMapView
	err := row.Scan(&v.ID, &v.File, &v.Release, &v.Sources, &v.Bytes, &v.CreatedAt)
	return v, err
}

func (s *Server) listSourceMaps(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), `SELECT `+sourceMapCols+` FROM source_maps WHERE workspace_id = $1 AND brand_id = $2
		ORDER BY created_at DESC, file LIMIT $3`, a.WorkspaceID, a.Brand, sourceMapsABrand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (sourceMapView, error) { return scanSourceMap(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []sourceMapView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "limit": sourceMapsABrand})
}

// builtFile is a built file's name as stacks are matched to it: the path
// of its address, without the site, a query string or a leading "./".
func builtFile(name string) string {
	name = strings.TrimSpace(name)
	if u, err := url.Parse(name); err == nil && u.Host != "" {
		name = u.Path
	} else if i := strings.IndexAny(name, "?#"); i >= 0 {
		name = name[:i]
	}
	name = path.Clean("/" + strings.ReplaceAll(name, `\`, "/"))
	if name == "/" {
		return ""
	}
	return name
}

type sourceMapInput struct {
	File    string          `json:"file"`
	Release string          `json:"release"`
	Map     json.RawMessage `json:"map"`
}

func (s *Server) uploadSourceMap(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in sourceMapInput
	if !decodeLarge(w, r, &in, sourceMapUpload) {
		return
	}
	file := builtFile(in.File)
	release := strings.TrimSpace(in.Release)
	if file == "" || len(file) > 500 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "file is the built file the map belongs to, as your site serves it, such as /assets/app-3f9a1c.js")
		return
	}
	if len(release) > 100 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "release is at most 100 characters")
		return
	}
	raw := in.Map
	// A map sent as a JSON string (the file's text) is the same map.
	var text string
	if json.Unmarshal(raw, &text) == nil && text != "" {
		raw = json.RawMessage(text)
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || len(m["mappings"]) == 0 || len(m["sources"]) == 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "map is the source map your build made for the file: the contents of its .map file")
		return
	}
	// Names and positions only: the code itself is never kept.
	delete(m, "sourcesContent")
	delete(m, "x_google_ignoreList")
	slim, err := json.Marshal(m)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if _, err := sourcemap.Parse(file, slim); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "map isn't a source map this server can read (version 3, with mappings)")
		return
	}
	var sources []json.RawMessage
	_ = json.Unmarshal(m["sources"], &sources)
	var packed bytes.Buffer
	zw := gzip.NewWriter(&packed)
	if _, err := zw.Write(slim); err != nil {
		s.internal(w, r, err)
		return
	}
	if err := zw.Close(); err != nil {
		s.internal(w, r, err)
		return
	}
	if packed.Len() > sourceMapBytes {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "the map is too large to keep, even without its source code: upload the maps of smaller files")
		return
	}
	ctx := r.Context()
	var v sourceMapView
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		v, err = scanSourceMap(tx.QueryRow(ctx, `INSERT INTO source_maps (workspace_id, brand_id, file, release, map, sources, bytes, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (brand_id, file, release) DO UPDATE SET map = EXCLUDED.map, sources = EXCLUDED.sources, bytes = EXCLUDED.bytes,
				created_by = EXCLUDED.created_by, created_at = now()
			RETURNING `+sourceMapCols, a.WorkspaceID, a.Brand, file, release, packed.Bytes(), len(sources), len(slim), nullableString(a.ActorID)))
		if err != nil {
			return err
		}
		// The oldest make room.
		if _, err := tx.Exec(ctx, `DELETE FROM source_maps WHERE brand_id = $1 AND id IN (
				SELECT id FROM source_maps WHERE brand_id = $1 ORDER BY created_at DESC, id OFFSET $2)`, a.Brand, sourceMapsABrand); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "source_map.uploaded", "source_map", v.ID,
			map[string]any{"brand_id": a.Brand, "file": file, "release": release}, map[string]any{"sources": v.Sources, "bytes": v.Bytes})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	sourceMaps.forget(a.Brand)
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) deleteSourceMap(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such source map")
		return
	}
	ctx := r.Context()
	found := true
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var file string
		err := tx.QueryRow(ctx, `DELETE FROM source_maps WHERE id = $1 AND workspace_id = $2 AND brand_id = $3 RETURNING file`, id, a.WorkspaceID, a.Brand).Scan(&file)
		if errors.Is(err, pgx.ErrNoRows) {
			found = false
			return nil
		}
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "source_map.deleted", "source_map", id, map[string]any{"brand_id": a.Brand, "file": file})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !found {
		writeError(w, r, http.StatusNotFound, "not_found", "no such source map")
		return
	}
	sourceMaps.forget(a.Brand)
	w.WriteHeader(http.StatusNoContent)
}

// stackFrame finds a built file's address with its line and column in a
// stack line, as Chrome (at f (https://x/app.js:1:2)) and Firefox and
// Safari (f@https://x/app.js:1:2) write them.
var stackFrame = regexp.MustCompile(`((?:https?|file)://[^\s()]+?|/[^\s():]+?):(\d+):(\d+)`)

var chromeFrame = regexp.MustCompile(`^(\s*at )(?:async )?(?:new )?([^\s(]+) \($`)

// originalStack reads a stack through the brand's source maps: each line
// whose file has a map is rewritten to the original file, line, column
// and function name. It returns "" when no line could be read.
func (s *Server) originalStack(ctx context.Context, ws, brand, stack string, maps map[string]*sourcemap.Consumer) string {
	if stack == "" {
		return ""
	}
	lines := strings.Split(stack, "\n")
	changed := false
	for i, line := range lines {
		loc := stackFrame.FindStringSubmatchIndex(line)
		if loc == nil {
			continue
		}
		file := builtFile(line[loc[2]:loc[3]])
		ln, _ := strconv.Atoi(line[loc[4]:loc[5]])
		col, _ := strconv.Atoi(line[loc[6]:loc[7]])
		if file == "" || ln < 1 || col < 1 {
			continue
		}
		c, seen := maps[file]
		if !seen {
			c = s.loadSourceMap(ctx, ws, brand, file)
			maps[file] = c
		}
		if c == nil {
			continue
		}
		src, name, oln, ocol, ok := c.Source(ln, col-1)
		if !ok || src == "" {
			continue
		}
		src = strings.TrimPrefix(strings.TrimPrefix(src, "webpack://"), "./")
		at := src + ":" + strconv.Itoa(oln) + ":" + strconv.Itoa(ocol+1)
		before, after := line[:loc[0]], line[loc[1]:]
		if m := chromeFrame.FindStringSubmatch(before); m != nil && name != "" {
			before = m[1] + name + " ("
		} else if j := strings.LastIndex(before, "@"); j >= 0 && name != "" && !strings.ContainsAny(before[:j], " \t(") {
			before = name + "@"
		}
		lines[i] = before + at + after
		changed = true
	}
	if !changed {
		return ""
	}
	return strings.Join(lines, "\n")
}

// loadSourceMap reads the brand's newest map for a built file: by its
// path, or, for a map uploaded under a bare file name, by that name. Nil
// when there is none or it can't be read.
func (s *Server) loadSourceMap(ctx context.Context, ws, brand, file string) *sourcemap.Consumer {
	if !sourceMaps.any(ctx, s, ws, brand) {
		return nil
	}
	var id string
	var packed []byte
	err := s.pool.QueryRow(ctx, `SELECT id::text, map FROM source_maps WHERE workspace_id = $1 AND brand_id = $2 AND file = ANY($3)
		ORDER BY (file = $4) DESC, created_at DESC LIMIT 1`, ws, brand, []string{file, "/" + path.Base(file)}, file).Scan(&id, &packed)
	if err != nil {
		return nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(zr, sourceMapUpload))
	if err != nil {
		return nil
	}
	c, err := sourcemap.Parse(file, raw)
	if err != nil {
		return nil
	}
	return c
}
