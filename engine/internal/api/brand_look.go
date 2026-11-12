package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// A brand's logo and main colour, found on its own website: OutflareBot
// reads the home page (obeying robots.txt, like any read) and fills in
// what the brand's look doesn't have yet. It never replaces a logo or
// colour someone set.

const jobBrandLook = "brand.look_read"

func (s *Server) routesBrandLook(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/workspaces/{ws}/brands/{brand}/look/find", s.inBrand(auth.ScopeContextManage, s.findBrandLook))
}

func (s *Server) registerBrandLookJobs(q *jobs.Queue) {
	q.Handle(jobBrandLook, s.brandLookJob)
}

// enqueueBrandLook queues reading the brand's website for its look. key
// keeps a retry from queuing it twice.
func enqueueBrandLook(ctx context.Context, tx pgx.Tx, ws, brandID, key string) error {
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobBrandLook, WorkspaceID: &ws, Payload: map[string]string{"brand_id": brandID},
		IdempotencyKey: jobBrandLook + ":" + brandID + ":" + key, MaxAttempts: 2})
	return err
}

// findBrandLook queues reading the brand's website for its logo and main
// colour, which fill in whichever of them the brand doesn't have yet.
func (s *Server) findBrandLook(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	b, ok := s.brandFor(w, r, a)
	if !ok {
		return
	}
	if b.Website == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "give the brand a website first")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := enqueueBrandLook(ctx, tx, a.WorkspaceID, b.ID, "asked:"+newID()); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "brand.look_find_requested", "brand", b.ID, map[string]any{})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

func (s *Server) brandLookJob(ctx context.Context, job jobs.Job) error {
	ws, id, err := socialPayload(job, "brand_id")
	if err != nil {
		return err
	}
	return s.ReadBrandLook(ctx, ws, id)
}

// ReadBrandLook reads a brand's home page for its logo and theme colour
// and saves the ones the brand doesn't have yet. A site that can't be read
// or offers neither leaves the brand as it was.
func (s *Server) ReadBrandLook(ctx context.Context, ws, brandID string) error {
	start := time.Now()
	ev := track.Event{Name: "brand.look_read", WorkspaceID: ws, ActorType: "system", Surface: "job", Props: map[string]any{"brand_id": brandID}}
	var website, logo string
	var colors lookColors
	var test bool
	err := s.pool.QueryRow(ctx, `SELECT b.website, b.logo_url, b.colors, w.test FROM brands b JOIN workspaces w ON w.id = b.workspace_id
		WHERE b.workspace_id = $1 AND b.id = $2 AND b.status = 'active'`, ws, brandID).Scan(&website, &logo, &colors, &test)
	if errors.Is(err, pgx.ErrNoRows) || website == "" || s.crawler == nil {
		return nil
	}
	if err != nil {
		return err
	}
	if logo != "" && colors.Primary != "" {
		return nil
	}
	fetch, wait := s.crawler.ReadFetcher(test)
	rd, err := crawl.ReadPage(ctx, fetch, wait, "https://"+website+"/")
	if err == nil && (rd == nil || rd.Status != http.StatusOK) {
		err = errors.New("the home page did not answer 200")
	}
	if err != nil {
		code := "unreachable"
		if errors.Is(err, crawl.ErrBlocked) {
			code = "blocked"
		}
		ev.Name = "brand.look_read_failed"
		ev.Outcome = track.Failure // the site's answer, not something broken here
		track.Fail(ctx, s.log, ev, code, err)
		return nil
	}
	found := lookColors{}
	if logo == "" {
		logo = firstImage(ctx, fetch, rd.Look.Logos)
	} else {
		logo = ""
	}
	if colors.Primary == "" {
		found.Primary = rd.Look.ThemeColor
	}
	ev.Props["logo_found"], ev.Props["color_found"] = logo != "", found.Primary != ""
	ev.Props["duration_ms"] = time.Since(start).Milliseconds()
	if logo == "" && found.Primary == "" {
		track.Emit(ctx, ev)
		return nil
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Only what is still empty: someone may have set it meanwhile.
		tag, err := tx.Exec(ctx, `UPDATE brands SET
				logo_url = CASE WHEN logo_url = '' AND $3 <> '' THEN $3 ELSE logo_url END,
				colors = CASE WHEN COALESCE(colors->>'primary', '') = '' AND $4 <> '' THEN jsonb_set(COALESCE(colors, '{}'::jsonb), '{primary}', to_jsonb($4::text)) ELSE colors END,
				updated_at = now()
			WHERE workspace_id = $1 AND id = $2 AND ((logo_url = '' AND $3 <> '') OR (COALESCE(colors->>'primary', '') = '' AND $4 <> ''))`,
			ws, brandID, logo, found.Primary)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "brand.look_found", TargetType: "brand", TargetID: brandID,
			Data: map[string]any{"logo": logo != "", "primary_color": found.Primary != ""}})
	})
	if err != nil {
		return err
	}
	track.Emit(ctx, ev)
	return nil
}

// firstImage is the first address that answers 200 with an image, or ""
// when none does, so a broken or missing icon is never saved.
func firstImage(ctx context.Context, fetch crawl.Fetcher, urls []string) string {
	for _, raw := range urls {
		resp, err := fetch.Fetch(ctx, raw)
		if err != nil || resp.Status != http.StatusOK {
			continue
		}
		ct := resp.ContentType()
		icon := (ct == "" || ct == "application/octet-stream") && strings.HasSuffix(strings.ToLower(raw), ".ico")
		if strings.HasPrefix(ct, "image/") || icon {
			if logo, err := cleanLogoURL(raw); err == nil {
				return logo
			}
		}
	}
	return ""
}
