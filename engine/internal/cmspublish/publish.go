// Package cmspublish carries content out to a brand's own website: the
// content.publish job takes a content_publishes row the API queued (in the
// same transaction as its audit event), renders the item's Markdown to
// HTML, creates or updates the post on the workspace's own CMS connection
// (connectors.CMSPublisher) and reads it back, so what the CMS shows is
// what Outflare records. The CMS call is the side effect, so it runs here
// and never in a request handler.
package cmspublish

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// JobPublish carries one content_publishes row out.
const JobPublish = "content.publish"

// Publisher runs the job.
type Publisher struct {
	Pool  *pgxpool.Pool
	Store *connectors.Store
	Log   *slog.Logger
	// Now is the clock; tests set it.
	Now func() time.Time
}

func (p *Publisher) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Register adds the job handler.
func (p *Publisher) Register(q *jobs.Queue) { q.Handle(JobPublish, p.job) }

type payload struct {
	Publish string `json:"publish_id"`
}

// Enqueue queues the job for a content_publishes row, once: a retry of
// the request that made it finds the same job.
func Enqueue(ctx context.Context, q jobs.Querier, ws, publishID string) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobPublish, WorkspaceID: &ws, Payload: payload{Publish: publishID},
		IdempotencyKey: "content.publish:" + publishID, MaxAttempts: 6})
	return err
}

func (p *Publisher) job(ctx context.Context, job jobs.Job) error {
	var pl payload
	if err := json.Unmarshal(job.Payload, &pl); err != nil || job.WorkspaceID == nil {
		return nil
	}
	return p.Apply(ctx, *job.WorkspaceID, pl.Publish, job.Attempt >= job.MaxAttempts)
}

// markdown renders the workspace's Markdown with raw HTML left out and
// unsafe links dropped (no WithUnsafe), so a page never carries script.
var markdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
)

// HTML renders a body for the CMS.
func HTML(body string) (string, error) {
	var buf bytes.Buffer
	if err := markdown.Convert([]byte(body), &buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// Apply carries one queued publication out. last says this is the job's
// last try, so an unreachable CMS ends it as failed instead of leaving it
// queued.
func (p *Publisher) Apply(ctx context.Context, ws, publishID string, last bool) error {
	var (
		status, kind, itemID, integrationID, brand              string
		live                                                    bool
		title, slug, target, body, excerpt, canonical, external string
		tags                                                    []string
	)
	err := p.Pool.QueryRow(ctx, `
		SELECT u.status, u.kind, u.live, u.item_id, u.integration_id, u.brand_id,
		       i.title, i.slug, i.target, i.body, i.excerpt, i.canonical_url, i.tags, i.external_id
		FROM content_publishes u JOIN content_items i ON i.id = u.item_id
		WHERE u.workspace_id = $1 AND u.id = $2`, ws, publishID).
		Scan(&status, &kind, &live, &itemID, &integrationID, &brand, &title, &slug, &target, &body, &excerpt, &canonical, &tags, &external)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status != "queued" {
		return nil
	}
	ev := track.Event{Name: "content.published", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"kind": kind, "live": live, "publish_id": publishID, "item_id": itemID, "brand_id": brand}}
	var connector string
	fail := func(outcome, code string, err error) error {
		metrics.ContentPublished(connector, kind, outcome)
		ev.Name = "content.publish_failed"
		if outcome == "failed" {
			ev.Outcome = track.Failure
		}
		track.Fail(ctx, p.Log, ev, code, err)
		if outcome == "error" {
			return err
		}
		_, dbErr := p.Pool.Exec(ctx, `UPDATE content_publishes SET status = 'failed', error = $3, applied_at = now() WHERE workspace_id = $1 AND id = $2 AND status = 'queued'`,
			ws, publishID, trim(err.Error(), 500))
		return dbErr
	}

	o, err := p.Store.Open(ctx, p.Pool, ws, integrationID)
	if err != nil {
		if errors.Is(err, connectors.ErrNotFound) {
			return fail("failed", "connector_gone", errors.New("the CMS connection is gone"))
		}
		return fail("error", "internal", err)
	}
	connector = o.Connector.Definition().ID
	ev.Props["connector"] = connector
	m, ok := o.Connector.(connectors.CMSPublisher)
	if !ok {
		return fail("failed", "connector_invalid", fmt.Errorf("%s cannot publish content", o.Connector.Definition().Name))
	}
	html, err := HTML(body)
	if err != nil {
		return fail("failed", "invalid_request", fmt.Errorf("rendering the body: %w", err))
	}
	in := connectors.CMSPostInput{Target: target, Title: title, Slug: slug, HTML: html, Excerpt: excerpt, CanonicalURL: canonical, Tags: tags, Live: live,
		IdempotencyKey: "outflare:" + publishID}
	if kind == "update" {
		if external == "" {
			return fail("failed", "connector_invalid", errors.New("the item is not on the CMS yet; publish it first"))
		}
		in.ExternalID = external
	}
	res, err := m.Publish(ctx, o.Client, o.Creds, in)
	if err != nil {
		k := connectors.KindOf(err)
		// A create whose outcome is unknown is never tried again, so a
		// post is never made twice; an update is safe to retry.
		if k == connectors.ErrUnavailable && !last && kind == "update" {
			return err
		}
		return fail("failed", "connector_"+k, err)
	}

	// Read it back: what the CMS shows is what counts.
	now := p.now()
	got, readErr := m.ReadPost(ctx, o.Client, o.Creds, target, res.ExternalID)
	verified := readErr == nil && got.ExternalID == res.ExternalID && (got.Status == "published") == live
	if readErr != nil {
		got = res
	}
	err = pgx.BeginFunc(ctx, p.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE content_publishes SET status = 'applied', applied_at = $3, external_id = $4, url = $5, cms_status = $6, verified = $7
			WHERE workspace_id = $1 AND id = $2 AND status = 'queued'`, ws, publishID, now, got.ExternalID, got.URL, got.Status, verified); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE content_items SET integration_id = $2, external_id = $3, url = $4, cms_status = $5,
			slug = CASE WHEN slug = '' THEN $6 ELSE slug END,
			status = CASE WHEN $5 = 'published' THEN 'published' WHEN status = 'published' THEN 'approved' ELSE status END,
			published_at = CASE WHEN $5 = 'published' THEN coalesce(published_at, $7) ELSE published_at END, updated_at = now()
			WHERE id = $1`, itemID, integrationID, got.ExternalID, got.URL, got.Status, trim(got.Slug, 200), now)
		return err
	})
	if err != nil {
		return fail("error", "internal", err)
	}
	outcome := "applied"
	if !verified {
		outcome = "unverified"
		ev.Props["unverified"] = true
	}
	metrics.ContentPublished(connector, kind, outcome)
	track.Emit(ctx, ev)
	return nil
}

func trim(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
