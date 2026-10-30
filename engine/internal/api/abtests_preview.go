package api

// Preview and QA: links that show a person each version of a test on the
// pages that show its flag, and a check per version that a person looked
// and it renders right. A check holds for the version's content as it was
// checked: change the content and the check no longer counts.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/abtests"
)

// abPreviewPages is how many of the pages that showed the flag get links.
const abPreviewPages = 5

type abPreviewLink struct {
	Variant string `json:"variant"`
	URL     string `json:"url"`
}

type abPreviewPage struct {
	URL      string          `json:"url"`
	Visitors int64           `json:"visitors"`
	Links    []abPreviewLink `json:"links"`
}

type abVersionCheck struct {
	Status    string    `json:"status"`
	Note      string    `json:"note"`
	CheckedBy *string   `json:"checked_by"`
	CheckedAt time.Time `json:"checked_at"`
	// Stale: the version's content changed after it was checked.
	Stale bool `json:"stale"`
}

type abPreviewVersion struct {
	Key     string          `json:"key"`
	Control bool            `json:"control"`
	Check   *abVersionCheck `json:"check"`
}

type abPreviews struct {
	TestID   string             `json:"test_id"`
	Pages    []abPreviewPage    `json:"pages"`
	Versions []abPreviewVersion `json:"versions"`
	Hint     string             `json:"hint"`
}

// abTestPreviews gives preview links for each version on the pages that
// showed the flag in the last 28 days (or on ?page=), and each version's
// check.
func (s *Server) abTestPreviews(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	t, ok := s.oneABTest(w, r)
	if !ok {
		return
	}
	var page string
	if raw := strings.TrimSpace(r.URL.Query().Get("page")); raw != "" {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(raw) > 2000 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "page is a page of your site, starting with https://")
			return
		}
		page = raw
	}
	ctx, cancel := context.WithTimeout(r.Context(), abResultsTimeout)
	defer cancel()
	f, err := s.abFlag(ctx, nil, a.WorkspaceID, t.FlagID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := s.abPreviews(ctx, a.WorkspaceID, t, f, page)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) abPreviews(ctx context.Context, ws string, t abTestView, f flagView, page string) (abPreviews, error) {
	out := abPreviews{TestID: t.ID, Pages: []abPreviewPage{}, Versions: []abPreviewVersion{}}
	var pages []abPreviewPage
	if page != "" {
		pages = append(pages, abPreviewPage{URL: page})
	} else {
		rows, err := s.pool.Query(ctx, `
			SELECT split_part(split_part(context->'page'->>'url', '#', 1), '?', 1) AS page, count(DISTINCT anonymous_id)
			FROM customer_events
			WHERE workspace_id = $1 AND name = $2 AND properties->>'flag' = $3 AND occurred_at > now() - interval '28 days'
				AND context->'page'->>'url' ~ '^https?://'
			GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT $4`, ws, abtests.ExposureEvent, f.Key, abPreviewPages)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var p abPreviewPage
			if err := rows.Scan(&p.URL, &p.Visitors); err != nil {
				rows.Close()
				return out, err
			}
			pages = append(pages, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return out, err
		}
		// No page has shown it yet: the brand's website, if it has one.
		if len(pages) == 0 && t.BrandID != nil {
			var site string
			if err := s.pool.QueryRow(ctx, `SELECT website FROM brands WHERE id = $1 AND workspace_id = $2`, *t.BrandID, ws).Scan(&site); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return out, err
			}
			if strings.HasPrefix(site, "https://") || strings.HasPrefix(site, "http://") {
				pages = append(pages, abPreviewPage{URL: site})
			}
		}
	}
	for _, p := range pages {
		for _, v := range f.Variants {
			if link, ok := previewURL(p.URL, f.Key, v.Key); ok {
				p.Links = append(p.Links, abPreviewLink{Variant: v.Key, URL: link})
			}
		}
		if len(p.Links) > 0 {
			out.Pages = append(out.Pages, p)
		}
	}

	checks, err := s.abVersionChecks(ctx, ws, t.ID, f)
	if err != nil {
		return out, err
	}
	for _, v := range f.Variants {
		out.Versions = append(out.Versions, abPreviewVersion{Key: v.Key, Control: v.Key == t.Control, Check: checks[v.Key]})
	}

	switch {
	case !f.OnSite:
		out.Hint = "The flag isn't sent to your site's script, so these links can't preview it. Turn on \"On the site\" for the flag, or preview it where your server reads it."
	case len(out.Pages) == 0:
		out.Hint = "No page has shown the flag yet. Give the address of a page that reads it to get links."
	default:
		out.Hint = "Each link shows that version to you alone, for the rest of your visit, and counts nothing. Pages that read the flag on your server don't take these links."
	}
	return out, nil
}

// previewURL is page with ?of_flag=key:variant, which the site script reads.
func previewURL(page, key, variant string) (string, bool) {
	u, err := url.Parse(page)
	if err != nil || u.Host == "" {
		return "", false
	}
	q := u.Query()
	q.Del("of_flag")
	q.Add("of_flag", key+":"+variant)
	u.RawQuery = q.Encode()
	return u.String(), true
}

// payloadHash fingerprints a version's content, so a check can tell it
// changed since.
func payloadHash(f flagView, variant string) string {
	for _, v := range f.Variants {
		if v.Key == variant {
			sum := sha256.Sum256(v.Payload)
			return hex.EncodeToString(sum[:])
		}
	}
	return ""
}

func (s *Server) abVersionChecks(ctx context.Context, ws, testID string, f flagView) (map[string]*abVersionCheck, error) {
	rows, err := s.pool.Query(ctx, `SELECT variant, status, note, checked_by::text, checked_at, payload_hash FROM ab_test_checks
		WHERE workspace_id = $1 AND test_id = $2`, ws, testID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*abVersionCheck{}
	for rows.Next() {
		var key, hash string
		c := &abVersionCheck{}
		if err := rows.Scan(&key, &c.Status, &c.Note, &c.CheckedBy, &c.CheckedAt, &hash); err != nil {
			return nil, err
		}
		c.Stale = hash != payloadHash(f, key)
		out[key] = c
	}
	return out, rows.Err()
}

// checkABTestVersion records that a person looked at a version: it renders
// right (ok) or it's broken, with an optional note.
func (s *Server) checkABTestVersion(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Variant string `json:"variant"`
		Status  string `json:"status"`
		Note    string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	a := accessFrom(r.Context())
	t, ok := s.oneABTest(w, r)
	if !ok {
		return
	}
	if !a.writes(t.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", brandLimitedMessage)
		return
	}
	if in.Status != "ok" && in.Status != "broken" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is ok (it renders right) or broken")
		return
	}
	in.Note = strings.TrimSpace(in.Note)
	if len(in.Note) > 1000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "note is at most 1,000 characters")
		return
	}
	ctx := r.Context()
	f, err := s.abFlag(ctx, nil, a.WorkspaceID, t.FlagID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if t.Status == "done" {
		writeError(w, r, http.StatusConflict, "conflict", "this test has ended")
		return
	}
	hash := payloadHash(f, in.Variant)
	if hash == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "variant is one of the flag's versions: "+strings.Join(variantKeys(f), ", "))
		return
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO ab_test_checks (workspace_id, test_id, variant, status, note, payload_hash, checked_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (test_id, variant) DO UPDATE SET status = EXCLUDED.status, note = EXCLUDED.note, payload_hash = EXCLUDED.payload_hash,
				checked_by = EXCLUDED.checked_by, checked_at = now()`,
			a.WorkspaceID, t.ID, in.Variant, in.Status, in.Note, hash, nullIfEmpty(a.ActorID)); err != nil {
			return err
		}
		// The event leaves out the version's key, which a person named.
		props := map[string]any{"status": in.Status, "test_status": t.Status, "control": in.Variant == t.Control, "flag_id": t.FlagID}
		data := map[string]any{"variant": in.Variant, "status": in.Status, "note": in.Note}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "ab_test.version_checked", "ab_test", t.ID, data, props)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := s.abPreviews(ctx, a.WorkspaceID, t, f, "")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// abQACheck is the checklist's line for preview and QA.
func (s *Server) abQACheck(ctx context.Context, ws string, t abTestView, f flagView) (abCheck, error) {
	checks, err := s.abVersionChecks(ctx, ws, t.ID, f)
	if err != nil {
		return abCheck{}, err
	}
	var broken, unchecked []string
	for _, v := range f.Variants {
		c := checks[v.Key]
		switch {
		case c == nil || c.Stale:
			unchecked = append(unchecked, v.Key)
		case c.Status == "broken":
			broken = append(broken, v.Key)
		}
	}
	switch {
	case len(broken) > 0:
		return abCheck{"qa", "fail", fmt.Sprintf("Marked broken: %s. Fix it, preview it again and mark it as looking right.", strings.Join(broken, ", "))}, nil
	case len(unchecked) > 0:
		return abCheck{"qa", "warn", fmt.Sprintf("Nobody has previewed %s since it last changed: open its preview link and check it renders right.", strings.Join(unchecked, ", "))}, nil
	}
	return abCheck{"qa", "ok", "A person previewed every version and it renders right."}, nil
}
