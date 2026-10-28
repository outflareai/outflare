package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/sociallinks"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Finding a brand's links: Outflare reads the brand's own website through
// the workspace's own page reader (Geonode) and suggests the social
// profiles and app store listings it links to. A person adds each one to
// the brand's links, edited if need be, or dismisses it. Nothing is added
// on its own, and with no page reader connected nothing is read.

const jobBrandDiscoverLinks = "brand.discover_links"

// commonLinkKinds are the places most brands have, shown as gaps to fill
// in when neither a link nor a suggestion covers them.
var commonLinkKinds = []string{"x", "linkedin", "youtube", "instagram", "tiktok", "facebook"}

type brandLinkSuggestion struct {
	ID        string     `json:"id"`
	BrandID   string     `json:"brand_id"`
	Kind      string     `json:"kind"`
	URL       string     `json:"url"`
	FoundOn   string     `json:"found_on"`
	Status    string     `json:"status"`
	DecidedAt *time.Time `json:"decided_at"`
	CreatedAt time.Time  `json:"created_at"`
}

func (s *Server) routesBrandLinks(mux *http.ServeMux) {
	read, manage := auth.ScopeWorkspaceRead, auth.ScopeContextManage
	mux.HandleFunc("POST /v1/workspaces/{ws}/brands/{brand}/links/discover", s.inBrand(manage, s.discoverBrandLinks))
	mux.HandleFunc("GET /v1/workspaces/{ws}/brands/{brand}/link-suggestions", s.inBrand(read, s.listBrandLinkSuggestions))
	mux.HandleFunc("POST /v1/workspaces/{ws}/brands/{brand}/link-suggestions/{suggestion}/add", s.inBrand(manage, s.addBrandLinkSuggestion))
	mux.HandleFunc("POST /v1/workspaces/{ws}/brands/{brand}/link-suggestions/{suggestion}/dismiss", s.inBrand(manage, s.dismissBrandLinkSuggestion))
}

func (s *Server) registerBrandLinkJobs(q *jobs.Queue) {
	q.Handle(jobBrandDiscoverLinks, s.discoverBrandLinksJob)
}

// pageReaders are the workspace's connected connections that read pages
// (Geonode), first connected first.
func (s *Server) pageReaders(ctx context.Context, q connectors.Querier, ws string) ([]string, error) {
	if s.integrations == nil {
		return nil, nil
	}
	var ids []string
	for _, d := range s.integrations.Registry.All() {
		if d.Has(connectors.CapCrawlFetch) {
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

// enqueueLinkDiscovery queues reading a brand's website for links, when
// the workspace has a page reader, and searching for its review pages. It
// reports whether it queued one.
func (s *Server) enqueueLinkDiscovery(ctx context.Context, tx pgx.Tx, ws, brandID, key string) (bool, error) {
	readers, err := s.pageReaders(ctx, tx, ws)
	if err != nil || len(readers) == 0 {
		return false, err
	}
	if _, err := s.enqueueReviewDiscovery(ctx, tx, ws, brandID, key); err != nil {
		return false, err
	}
	_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobBrandDiscoverLinks, WorkspaceID: &ws, Payload: map[string]string{"brand_id": brandID},
		IdempotencyKey: "brand.discover_links:" + brandID + ":" + key, MaxAttempts: 3})
	return err == nil, err
}

// enqueuePublicSocialSync reads the X profiles in brand links now, through
// each page reader, after links change.
// Review pages in the links are read too.
func (s *Server) enqueuePublicSocialSync(ctx context.Context, tx pgx.Tx, ws, key string) error {
	readers, err := s.pageReaders(ctx, tx, ws)
	if err != nil {
		return err
	}
	for _, id := range readers {
		if err := enqueueSocialSync(ctx, tx, ws, id, key); err != nil {
			return err
		}
	}
	return s.enqueueReviewRead(ctx, tx, ws, key)
}

// watchPageReaders reads every brand's website for links when a page
// reader is first connected.
func (s *Server) watchPageReaders() {
	if s.integrations == nil || s.integrations.Registry == nil {
		return
	}
	s.integrations.OnConnect = append(s.integrations.OnConnect, func(ctx context.Context, tx pgx.Tx, i connectors.Integration) error {
		c, ok := s.integrations.Registry.Get(i.Connector)
		if !ok || !c.Definition().Has(connectors.CapCrawlFetch) {
			return nil
		}
		rows, err := tx.Query(ctx, `SELECT id::text FROM brands b WHERE workspace_id = $1 AND status = 'active' AND website <> ''
			AND NOT EXISTS (SELECT 1 FROM brand_link_suggestions x WHERE x.brand_id = b.id) LIMIT 50`, i.WorkspaceID)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobBrandDiscoverLinks, WorkspaceID: &i.WorkspaceID, Payload: map[string]string{"brand_id": id},
				IdempotencyKey: "brand.discover_links:" + id + ":connected:" + i.ID, MaxAttempts: 3}); err != nil {
				return err
			}
			if _, err := s.enqueueReviewDiscovery(ctx, tx, i.WorkspaceID, id, "connected:"+i.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

var (
	errNoPageReader = errors.New("no page reader")
	errNoWebsite    = errors.New("no website")
)

func (s *Server) discoverBrandLinks(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	b, ok := s.brandFor(w, r, a)
	if !ok {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if b.Website == "" {
			return errNoWebsite
		}
		queued, err := s.enqueueLinkDiscovery(ctx, tx, a.WorkspaceID, b.ID, "asked:"+newID())
		if err != nil {
			return err
		}
		if !queued {
			return errNoPageReader
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "brand.links_discovery_requested", "brand", b.ID, map[string]any{})
	})
	switch {
	case errors.Is(err, errNoWebsite):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "give the brand a website first")
	case errors.Is(err, errNoPageReader):
		writeError(w, r, http.StatusConflict, "connector_needed", "connect Geonode first: links are read from the brand's website with your own Geonode key")
	case err != nil:
		s.internal(w, r, err)
	default:
		writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
	}
}

func (s *Server) loadLinkSuggestions(ctx context.Context, ws, brandID, status string) ([]brandLinkSuggestion, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, brand_id::text, kind, url, found_on, status, decided_at, created_at FROM brand_link_suggestions
		WHERE workspace_id = $1 AND brand_id = $2 AND ($3 = 'all' OR status = $3)
		ORDER BY created_at, url`, ws, brandID, status)
	if err != nil {
		return nil, err
	}
	out := []brandLinkSuggestion{}
	var x brandLinkSuggestion
	_, err = pgx.ForEachRow(rows, []any{&x.ID, &x.BrandID, &x.Kind, &x.URL, &x.FoundOn, &x.Status, &x.DecidedAt, &x.CreatedAt}, func() error {
		out = append(out, x)
		return nil
	})
	return out, err
}

func (s *Server) listBrandLinkSuggestions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	b, ok := s.brandFor(w, r, a)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "suggested"
	}
	if !slices.Contains([]string{"suggested", "added", "dismissed", "all"}, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is suggested, added, dismissed or all")
		return
	}
	ctx := r.Context()
	list, err := s.loadLinkSuggestions(ctx, a.WorkspaceID, b.ID, status)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// Gaps: common places with neither a link nor a waiting suggestion.
	have := map[string]bool{}
	for _, l := range b.Links {
		have[l.Kind] = true
	}
	waiting, err := s.loadLinkSuggestions(ctx, a.WorkspaceID, b.ID, "suggested")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for _, l := range waiting {
		have[l.Kind] = true
	}
	gaps := []string{}
	for _, k := range commonLinkKinds {
		if !have[k] {
			gaps = append(gaps, k)
		}
	}
	readers, err := s.pageReaders(ctx, s.pool, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "gaps": gaps, "can_discover": len(readers) > 0 && b.Website != ""})
}

// decideLinkSuggestion loads one waiting suggestion of the brand.
func (s *Server) decideLinkSuggestion(ctx context.Context, tx pgx.Tx, ws, brandID, id string) (brandLinkSuggestion, error) {
	var x brandLinkSuggestion
	err := tx.QueryRow(ctx, `SELECT id::text, brand_id::text, kind, url, found_on, status, decided_at, created_at FROM brand_link_suggestions
		WHERE workspace_id = $1 AND brand_id = $2 AND id = $3 FOR UPDATE`, ws, brandID, id).
		Scan(&x.ID, &x.BrandID, &x.Kind, &x.URL, &x.FoundOn, &x.Status, &x.DecidedAt, &x.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return x, errNotFound
	}
	return x, err
}

var errDecided = errors.New("already decided")

func (s *Server) addBrandLinkSuggestion(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		URL   *string `json:"url"`
		Kind  *string `json:"kind"`
		Label *string `json:"label"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	b, ok := s.brandFor(w, r, a)
	if !ok {
		return
	}
	id := r.PathValue("suggestion")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such suggestion")
		return
	}
	ctx := r.Context()
	var out brandLinkSuggestion
	var links []brandLink
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		x, err := s.decideLinkSuggestion(ctx, tx, a.WorkspaceID, b.ID, id)
		if err != nil {
			return err
		}
		if x.Status != "suggested" {
			return errDecided
		}
		// The person may correct the address or the kind before adding it.
		l := brandLink{Kind: x.Kind, URL: x.URL}
		edited := false
		if req.URL != nil && strings.TrimSpace(*req.URL) != x.URL {
			l.URL, l.Kind, edited = *req.URL, "", true
		}
		if req.Kind != nil {
			l.Kind = *req.Kind
		}
		if req.Label != nil {
			l.Label = *req.Label
		}
		var cur []brandLink
		if err := tx.QueryRow(ctx, `SELECT links FROM brands WHERE workspace_id = $1 AND id = $2 FOR UPDATE`, a.WorkspaceID, b.ID).Scan(&cur); err != nil {
			return err
		}
		checked, err := validLinks(append(slices.Clone(cur), l))
		if err != nil {
			return err
		}
		added := checked[len(checked)-1]
		links = checked
		for _, c := range cur {
			if sameLink(c.URL, added.URL) {
				links = checked[:len(checked)-1]
				break
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE brands SET links = $3, updated_at = now() WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, b.ID, links); err != nil {
			return err
		}
		// The suggestion keeps the address as found, so reading the website
		// again does not suggest it once more after an edit.
		if err := tx.QueryRow(ctx, `UPDATE brand_link_suggestions SET status = 'added', decided_by = $4, decided_at = now(), updated_at = now()
			WHERE workspace_id = $1 AND brand_id = $2 AND id = $3 RETURNING status, decided_at`,
			a.WorkspaceID, b.ID, id, a.ActorID).Scan(&x.Status, &x.DecidedAt); err != nil {
			return err
		}
		out = x
		// A profile whose followers are public is read straight away.
		if slices.Contains(sociallinks.PublicKinds, added.Kind) {
			if err := s.enqueuePublicSocialSync(ctx, tx, a.WorkspaceID, "link:"+id); err != nil {
				return err
			}
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "brand.link_added", "brand", b.ID, map[string]any{
			"suggestion_id": id, "kind": added.Kind, "edited": edited,
		})
	})
	if s.writeLinkDecisionError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"suggestion": out, "links": links})
}

func (s *Server) dismissBrandLinkSuggestion(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	b, ok := s.brandFor(w, r, a)
	if !ok {
		return
	}
	id := r.PathValue("suggestion")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such suggestion")
		return
	}
	ctx := r.Context()
	var out brandLinkSuggestion
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		x, err := s.decideLinkSuggestion(ctx, tx, a.WorkspaceID, b.ID, id)
		if err != nil {
			return err
		}
		if x.Status != "suggested" {
			return errDecided
		}
		if err := tx.QueryRow(ctx, `UPDATE brand_link_suggestions SET status = 'dismissed', decided_by = $4, decided_at = now(), updated_at = now()
			WHERE workspace_id = $1 AND brand_id = $2 AND id = $3 RETURNING status, decided_at`, a.WorkspaceID, b.ID, id, a.ActorID).Scan(&x.Status, &x.DecidedAt); err != nil {
			return err
		}
		out = x
		return record(ctx, tx, &a.principal, a.WorkspaceID, "brand.link_dismissed", "brand", b.ID, map[string]any{"suggestion_id": id, "kind": x.Kind})
	})
	if s.writeLinkDecisionError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) writeLinkDecisionError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "no such suggestion")
	case errors.Is(err, errDecided):
		writeError(w, r, http.StatusConflict, "conflict", "this suggestion was already added or dismissed")
	default:
		return s.allocError(w, r, err)
	}
	return true
}

// sameLink compares two addresses without their scheme, www and trailing
// slash.
func sameLink(a, b string) bool {
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
		return strings.TrimSuffix(strings.TrimPrefix(s, "www."), "/")
	}
	return norm(a) == norm(b)
}

func (s *Server) discoverBrandLinksJob(ctx context.Context, job jobs.Job) error {
	ws, id, err := socialPayload(job, "brand_id")
	if err != nil {
		return err
	}
	return s.DiscoverBrandLinks(ctx, ws, id)
}

// DiscoverBrandLinks reads a brand's homepage through the workspace's page
// reader and stores the profiles and listings it links to as suggestions.
// A page built in the browser is read again with JavaScript on.
func (s *Server) DiscoverBrandLinks(ctx context.Context, ws, brandID string) error {
	start := time.Now()
	ev := track.Event{Name: "brand.links_discovered", WorkspaceID: ws, ActorType: "system", Surface: "job", Props: map[string]any{"brand_id": brandID}}
	var website string
	var links []brandLink
	err := s.pool.QueryRow(ctx, `SELECT website, links FROM brands WHERE workspace_id = $1 AND id = $2 AND status = 'active'`, ws, brandID).Scan(&website, &links)
	if errors.Is(err, pgx.ErrNoRows) || website == "" {
		return nil
	}
	if err != nil {
		return err
	}
	fail := func(code string, err error) error {
		ev.Name = "brand.links_discovery_failed"
		track.Fail(ctx, s.log, ev, code, err)
		if k := connectors.KindOf(err); k == connectors.ErrUnavailable || k == connectors.ErrQuota {
			return err
		}
		return nil
	}
	readers, err := s.pageReaders(ctx, s.pool, ws)
	if err != nil {
		return err
	}
	if len(readers) == 0 {
		return fail("connector_needed", errNoPageReader)
	}
	o, err := s.integrations.Open(ctx, s.pool, ws, readers[0])
	if err != nil {
		return fail("connector_"+connectors.KindOf(err), err)
	}
	f, ok := o.Connector.(connectors.PageFetcher)
	if !ok {
		return nil
	}
	pageURL := "https://" + website + "/"
	var found []sociallinks.Link
	tokens := 0.0
	for _, js := range []bool{false, true} {
		p, err := f.FetchPage(ctx, o.Client, o.Creds, pageURL, js)
		tokens += p.Cost.Units
		if err != nil {
			ev.Props["tokens"] = tokens
			return fail("connector_"+connectors.KindOf(err), err)
		}
		if p.URL != "" {
			pageURL = p.URL
		}
		found = sociallinks.Find(p.HTML, pageURL)
		if len(found) > 0 {
			break
		}
	}
	added := 0
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, l := range found {
			if slices.ContainsFunc(links, func(b brandLink) bool { return sameLink(b.URL, l.URL) }) {
				continue
			}
			tag, err := tx.Exec(ctx, `INSERT INTO brand_link_suggestions (workspace_id, brand_id, kind, url, found_on)
				VALUES ($1, $2, $3, $4, $5) ON CONFLICT (brand_id, url) DO NOTHING`, ws, brandID, l.Kind, truncate(l.URL, 2000), truncate(pageURL, 2000))
			if err != nil {
				return err
			}
			added += int(tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		return err
	}
	ev.Props["found"], ev.Props["suggested"], ev.Props["tokens"] = len(found), added, tokens
	ev.Props["duration_ms"] = time.Since(start).Milliseconds()
	track.Emit(ctx, ev)
	return nil
}
