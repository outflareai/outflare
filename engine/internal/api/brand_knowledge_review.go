package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The knowledge sheet, reviewed (plans/paid-ads/plan.md, "Knowledge
// sheet"). Paid ads are written from what the brand's website says it
// offers (brand_context.knowledge), so before a draft is written a person
// says the sheet looks right (POST .../knowledge/review), and the read
// that fills it never fails silently: without a page reader or an AI key
// the sheet carries the error code, the status shows a blocker, and the
// draft writer refuses. Once an ad account is connected to the brand the
// website is read again every day; a read that changes what the brand
// sells clears the review so the owner looks again.

func (s *Server) routesBrandKnowledgeReview(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/knowledge", s.onBrand(auth.ScopeWorkspaceRead, s.getBrandKnowledge))
	mux.HandleFunc("POST /v1/brands/{brand}/knowledge/review", s.onBrand(auth.ScopeContextManage, s.reviewBrandKnowledge))
}

// knowledgeReview is the part of a brand's sheet that says whether it can
// be trusted: how the last read went and who reviewed it.
type knowledgeReview struct {
	ReadAt       *time.Time `json:"read_at"`
	Pages        int        `json:"pages"`
	ReadError    string     `json:"read_error"`
	ReviewedAt   *time.Time `json:"reviewed_at"`
	ReviewedBy   *string    `json:"reviewed_by"`
	ChangedAt    *time.Time `json:"changed_at"`
	RefreshDaily bool       `json:"refresh_daily"`
	// ChangedSinceReview is true when a read changed what the brand sells
	// and nobody has reviewed the sheet since.
	ChangedSinceReview bool `json:"changed_since_review"`
	// Trusted is true when a person reviewed the sheet, nothing changed
	// since, and the last read worked: the ads draft writer's gate.
	Trusted bool `json:"trusted"`
}

func (k *knowledgeReview) derive() {
	k.ChangedSinceReview = k.ChangedAt != nil && (k.ReviewedAt == nil || k.ChangedAt.After(*k.ReviewedAt))
	k.Trusted = k.ReviewedAt != nil && !k.ChangedSinceReview && k.ReadError == ""
}

// brandKnowledgeView is GET /v1/brands/{brand}/knowledge: the sheet with
// its review.
type brandKnowledgeView struct {
	BrandID   string         `json:"brand_id"`
	Knowledge BrandKnowledge `json:"knowledge"`
	knowledgeReview
}

// loadKnowledgeReview reads how a brand's sheet stands. A brand that was
// never read has an empty, unreviewed sheet.
func loadKnowledgeReview(ctx context.Context, q jobs.Querier, ws, brandID string) (knowledgeReview, error) {
	var k knowledgeReview
	err := q.QueryRow(ctx, `SELECT c.knowledge_read_at, COALESCE(c.knowledge_pages, 0), COALESCE(c.knowledge_error, ''), c.knowledge_reviewed_at, c.knowledge_reviewed_by::text,
		c.knowledge_changed_at, COALESCE(c.knowledge_refresh_daily, false)
		FROM brand_context c JOIN brands b ON b.id = c.brand_id WHERE c.brand_id = $1 AND b.workspace_id = $2`, brandID, ws).Scan(
		&k.ReadAt, &k.Pages, &k.ReadError, &k.ReviewedAt, &k.ReviewedBy, &k.ChangedAt, &k.RefreshDaily)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	k.derive()
	return k, err
}

func (s *Server) loadBrandKnowledgeView(ctx context.Context, ws, brandID string) (brandKnowledgeView, error) {
	v := brandKnowledgeView{BrandID: brandID}
	var err error
	if v.Knowledge, _, _, _, err = loadBrandKnowledge(ctx, s.pool, ws, brandID); err != nil {
		return v, err
	}
	v.knowledgeReview, err = loadKnowledgeReview(ctx, s.pool, ws, brandID)
	return v, err
}

func (s *Server) getBrandKnowledge(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, err := s.loadBrandKnowledgeView(r.Context(), a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// reviewBrandKnowledge is a person saying the sheet looks right. Only a
// person: an agent writes ads from the sheet, so it never signs it off.
func (s *Server) reviewBrandKnowledge(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if a.ActorType != "human" {
		writeError(w, r, http.StatusForbidden, "forbidden", "only a person reviews the brand's knowledge sheet, in Outflare under Paid ads (Set up) or on the brand's page; agents write ads from it")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO brand_context (brand_id, workspace_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, a.Brand, a.WorkspaceID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE brand_context SET knowledge_reviewed_at = now(), knowledge_reviewed_by = $2 WHERE brand_id = $1`, a.Brand, a.ActorID); err != nil {
			return err
		}
		k, _, _, _, err := loadBrandKnowledge(ctx, tx, a.WorkspaceID, a.Brand)
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "brand.knowledge_reviewed", "brand", a.Brand, map[string]any{"brand_id": a.Brand, "items": k.count()})
	})
	if s.allocError(w, r, err) {
		return
	}
	v, err := s.loadBrandKnowledgeView(ctx, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// brandKnowledgeNotRead records that a brand's website could not be read
// for a reason the workspace can fix (code), so the sheet never stays
// silently empty: the brand's context and GET .../knowledge carry the
// code, the status shows a blocker, and the event says why.
func (s *Server) brandKnowledgeNotRead(ctx context.Context, tx pgx.Tx, ws, brandID, code string, cause error) error {
	if _, err := tx.Exec(ctx, `INSERT INTO brand_context (brand_id, workspace_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, brandID, ws); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE brand_context SET knowledge_error = $2 WHERE brand_id = $1`, brandID, code); err != nil {
		return err
	}
	track.Fail(ctx, s.log, track.Event{Name: "brand.knowledge_read_failed", WorkspaceID: ws, ActorType: "system", Surface: "job", Outcome: track.Failure,
		Props: map[string]any{"brand_id": brandID}}, code, cause)
	metrics.BrandKnowledgeRead("failed")
	return nil
}

// knowledgePriceRE finds prices and percentages in text: $49, 49 USD,
// €12.50, 20%, "from 49 a month".
var knowledgePriceRE = regexp.MustCompile(`(?i)(?:[$€£]\s?\d[\d,]*(?:\.\d+)?|\d[\d,]*(?:\.\d+)?\s?(?:%|usd|eur|gbp|aud|cad|nzd|dollars?|euros?|pounds?)|\d[\d,]*(?:\.\d+)?\s?(?:/|a|per)\s?(?:mo|month|yr|year|user|seat))`)

// knowledgeNumbers are the digits of every price or percentage in text,
// so "$49/mo" and "49 a month" count as the same price.
func knowledgeNumbers(text string) map[string]bool {
	out := map[string]bool{}
	for _, m := range knowledgePriceRE.FindAllString(text, -1) {
		digits := strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' || r == '.' {
				return r
			}
			return -1
		}, m)
		if digits != "" {
			out[strings.TrimSuffix(digits, ".")] = true
		}
	}
	return out
}

// knowledgeOfferChanged reports whether a read changed what the brand
// sells: its products, services, prices or positioning (the summary).
// Features, integrations and audiences change wording often and don't
// need the owner's eye again.
func knowledgeOfferChanged(prev, next BrandKnowledge) bool {
	if strings.TrimSpace(prev.Summary) != strings.TrimSpace(next.Summary) {
		return true
	}
	texts := func(k BrandKnowledge, kind string) string {
		list := make([]string, 0, len(k.Lists[kind]))
		for _, it := range k.Lists[kind] {
			list = append(list, strings.ToLower(it.Text))
		}
		sort.Strings(list)
		return strings.Join(list, "\n")
	}
	for _, kind := range []string{"products", "services"} {
		if texts(prev, kind) != texts(next, kind) {
			return true
		}
	}
	a, b := knowledgeNumbers(prev.Text()), knowledgeNumbers(next.Text())
	if len(a) != len(b) {
		return true
	}
	for n := range a {
		if !b[n] {
			return true
		}
	}
	return false
}

// knowledgeRefreshOn turns on the daily read of a brand's website, once
// an ad account is connected to it, and queues the first read right away.
// Called in the transaction that gives the account its brand.
func (s *Server) knowledgeRefreshOn(ctx context.Context, tx pgx.Tx, ws, brandID string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO brand_context (brand_id, workspace_id, knowledge_refresh_daily) VALUES ($1, $2, true)
		ON CONFLICT (brand_id) DO UPDATE SET knowledge_refresh_daily = true`, brandID, ws); err != nil {
		return err
	}
	return enqueueBrandKnowledgeDaily(ctx, tx, ws, brandID, time.Now())
}

// enqueueBrandKnowledgeDaily queues the day's read of a brand's website,
// once a day whoever asks. The job itself records a missing page reader
// or AI key on the sheet, so the sheet never stays silently stale.
func enqueueBrandKnowledgeDaily(ctx context.Context, tx pgx.Tx, ws, brandID string, now time.Time) error {
	_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobBrandKnowledge, WorkspaceID: &ws, Payload: map[string]string{"brand_id": brandID},
		IdempotencyKey: jobBrandKnowledge + ":" + brandID + ":daily:" + now.UTC().Format("2006-01-02"), MaxAttempts: 3})
	return err
}

// QueueDueBrandKnowledge turns on the daily read for every brand with a
// connected ad account (whichever path connected it) and queues the reads
// due: brands with the daily read on, a website, and no read in the last
// 23 hours. It says how many it queued.
func (s *Server) QueueDueBrandKnowledge(ctx context.Context) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO brand_context (brand_id, workspace_id, knowledge_refresh_daily)
			SELECT b.id, b.workspace_id, true FROM brands b
			WHERE b.status = 'active' AND EXISTS (SELECT 1 FROM ad_accounts a WHERE a.workspace_id = b.workspace_id AND a.brand_id = b.id AND a.status = 'active')
			ON CONFLICT (brand_id) DO UPDATE SET knowledge_refresh_daily = true WHERE NOT brand_context.knowledge_refresh_daily`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT b.workspace_id::text, b.id::text FROM brands b JOIN brand_context c ON c.brand_id = b.id
			WHERE b.status = 'active' AND b.website <> '' AND c.knowledge_refresh_daily
			  AND (c.knowledge_read_at IS NULL OR c.knowledge_read_at < now() - interval '23 hours')
			ORDER BY c.knowledge_read_at NULLS FIRST LIMIT 200`)
		if err != nil {
			return err
		}
		type due struct{ ws, brand string }
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
			var d due
			return d, row.Scan(&d.ws, &d.brand)
		})
		if err != nil {
			return err
		}
		now := time.Now()
		for _, d := range list {
			if err := enqueueBrandKnowledgeDaily(ctx, tx, d.ws, d.brand, now); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// RunBrandKnowledgeRefresh queues the daily reads of brands' websites
// that are due, every interval.
func (s *Server) RunBrandKnowledgeRefresh(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := s.QueueDueBrandKnowledge(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.log, track.Event{Name: "brand.knowledge_refresh_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// knowledgeGate refuses writing an ad draft from a sheet nobody has
// signed off (422 knowledge_unreviewed), unless a person overrides it for
// this draft. It answers the error itself.
func (s *Server) knowledgeGate(w http.ResponseWriter, r *http.Request, a *access, override bool) bool {
	k, err := loadKnowledgeReview(r.Context(), s.pool, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return false
	}
	if k.Trusted {
		return true
	}
	if override && a.ActorType == "human" {
		return true
	}
	why := "nobody has reviewed the brand's knowledge sheet yet"
	switch {
	case k.ReadError != "":
		why = "the last read of the brand's website failed (" + k.ReadError + ")"
	case k.ChangedSinceReview:
		why = "a read changed what the brand sells and nobody has reviewed the sheet since"
	}
	writeError(w, r, http.StatusUnprocessableEntity, "knowledge_unreviewed", "ads are written from the brand's knowledge sheet, and "+why+
		": a person checks it under Paid ads, Set up (or the brand's page) and marks it reviewed; a person may also send override_knowledge true to write this one draft anyway")
	return false
}
