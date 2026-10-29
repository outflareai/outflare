package news

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/emailfind"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Reading articles: OutflareBot reads each new article's page once for its
// byline and date, obeying robots.txt and crawl opt-outs, and the free email
// finder looks for the author's address at the outlet. An author with an
// address becomes a journalist: a person record on the outlet's company,
// with the articles they wrote. Nothing here costs money.

// JobRead reads one article's page.
const JobRead = "news.read_article"

// MaxReadsPerRun is how many new articles one search run queues to read.
const MaxReadsPerRun = 20

// minConfidence is the least email finder confidence that makes an author
// a journalist record.
const minConfidence = 40

// Reader reads articles.
type Reader struct {
	Pool  *pgxpool.Pool
	Log   *slog.Logger
	Crawl *crawl.Service
	// Emails finds an author's address; nil skips it.
	Emails *emailfind.Service
}

// Register adds the job handler.
func (r *Reader) Register(q *jobs.Queue) { q.Handle(JobRead, r.read) }

type readPayload struct {
	ArticleID string `json:"article_id"`
}

// StartRead queues the read of an article that hasn't been read, once.
func StartRead(ctx context.Context, q jobs.Querier, ws, articleID string) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobRead, Payload: readPayload{ArticleID: articleID}, WorkspaceID: &ws,
		MaxAttempts: 3, IdempotencyKey: "news.read:" + articleID})
	return err
}

// Article is what a read found.
type Article struct {
	Status    string
	Author    string
	Published *time.Time
	// Email is the author's likely address at the outlet, and how sure.
	Email      string
	Confidence int
	code       string
}

func (r *Reader) read(ctx context.Context, job jobs.Job) error {
	var p readPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	var ws, raw string
	var test bool
	err := r.Pool.QueryRow(ctx, `SELECT a.workspace_id::text, a.url, w.test FROM news_articles a JOIN workspaces w ON w.id = a.workspace_id
		WHERE a.id = $1 AND a.read_at IS NULL`, p.ArticleID).Scan(&ws, &raw, &test)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // gone, or read already
	}
	if err != nil {
		return err
	}
	start := time.Now()
	a := r.Read(ctx, ws, raw, test)
	_, domain := ArticleURL(raw)
	ev := track.Event{Name: "news_article.read", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"article_id": p.ArticleID, "status": a.Status, "byline": a.Author != "", "journalist": a.Email != ""}}
	journalist, err := r.store(ctx, ws, p.ArticleID, domain, a)
	if err != nil {
		ev.Name = "news_article.read_failed"
		track.Fail(ctx, r.Log, ev, "internal", err)
		return err
	}
	metrics.NewsArticleRead(a.Status, journalist != "")
	ev.Props["new_journalist"] = journalist != ""
	ev.Props["duration_ms"] = time.Since(start).Milliseconds()
	if a.code != "" {
		ev.Props["error_code"] = a.code
	}
	track.Emit(ctx, ev)
	return nil
}

// Read reads an article's page and looks for its author's address. It
// never fails: what went wrong is the status.
func (r *Reader) Read(ctx context.Context, ws, raw string, test bool) Article {
	fail := func(status, code string) Article { return Article{Status: status, code: code} }
	u, err := url.Parse(raw)
	if err != nil {
		return fail("failed", "invalid_request")
	}
	if out, err := crawl.OptedOut(ctx, r.Pool, u.Hostname()); err != nil {
		return fail("failed", "internal")
	} else if out {
		return fail("opted_out", "crawl_opted_out")
	}
	if !test {
		if err := crawl.CheckURL(raw, r.Crawl.AllowPrivate); err != nil {
			return fail("unreachable", "site_unreachable")
		}
	}
	f, wait := r.Crawl.ReadFetcher(test)
	rd, err := crawl.ReadPage(ctx, f, wait, raw)
	switch {
	case errors.Is(err, crawl.ErrBlocked):
		return fail("blocked", "robots_disallowed")
	case err != nil:
		return fail("unreachable", "site_unreachable")
	case rd.Status == 404 || rd.Status == 410:
		return fail("gone", "")
	case rd.Status != 200:
		return fail("failed", "site_unreachable")
	}
	a := Article{Status: "up", Author: strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rd.Author), "By "))}
	if t, ok := publishedAt(rd.Published); ok {
		a.Published = &t
	}
	_, domain := ArticleURL(rd.FinalURL)
	if first, last := PersonName(a.Author); r.Emails != nil && first != "" && domain != "" {
		res, err := emailfind.FindEmails(ctx, r.Pool, r.Emails.Resolver, ws, emailfind.FindRequest{Items: []emailfind.FindItem{
			{Ref: "author", FirstName: first, LastName: last, Domain: domain}}})
		if err == nil && len(res.Items) == 1 {
			it := res.Items[0]
			ok := it.Code == emailfind.CodeVerified || it.Code == emailfind.CodeFound || it.Code == emailfind.CodeRisky
			if it.Email != "" && ok && it.Confidence >= minConfidence {
				a.Email, a.Confidence = strings.ToLower(it.Email), it.Confidence
			}
		}
	}
	return a
}

// store keeps the read on the article and, when the author has an address,
// makes them a person record on the outlet's company and notes the article
// as theirs, in one transaction. It returns the journalist's record id
// when this article is new to them.
func (r *Reader) store(ctx context.Context, ws, articleID, domain string, a Article) (string, error) {
	var journalist string
	err := pgx.BeginFunc(ctx, r.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE news_articles SET read_status = $3, read_at = now(),
				author = CASE WHEN $4 = '' THEN author ELSE $4 END, published_at = COALESCE($5, published_at)
			WHERE id = $1 AND workspace_id = $2`, articleID, ws, a.Status, clip(a.Author, 200), a.Published); err != nil {
			return err
		}
		if a.Email == "" {
			return nil
		}
		outlets, err := customers.EnsureCompanies(ctx, tx, ws, nil, "news_watch", []customers.Company{{Domain: domain}})
		if err != nil {
			return err
		}
		outlet, ok := outlets[customers.CompanyDomain(domain)]
		if !ok {
			return nil
		}
		people, err := customers.EnsurePeople(ctx, tx, ws, "prospect", "news_watch", []customers.Person{{Email: a.Email, Name: a.Author,
			Attributes: map[string]any{"companies": []any{outlet}}}})
		if err != nil {
			return err
		}
		person, ok := people[a.Email]
		if !ok {
			return nil
		}
		if _, err := tx.Exec(ctx, `INSERT INTO record_relationships (workspace_id, from_id, to_id, kind, field_key)
			SELECT $1, $2, $3, 'companies', 'companies'
			WHERE NOT EXISTS (SELECT 1 FROM record_relationships WHERE workspace_id = $1 AND from_id = $2 AND to_id = $3 AND kind = 'companies' AND valid_to IS NULL)`,
			ws, person, outlet); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO news_article_authors (article_id, workspace_id, record_id, confidence) SELECT $1, $2, $3, $4
			WHERE NOT EXISTS (SELECT 1 FROM news_article_authors WHERE article_id = $1 AND record_id = $3)`, articleID, ws, person, a.Confidence)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		journalist = person
		data := map[string]any{"article_id": articleID, "outlet_record_id": outlet, "confidence": a.Confidence}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "journalist.found",
			TargetType: "record", TargetID: person, Data: data, Props: data})
	})
	return journalist, err
}

// PersonName is a byline's first and last name, or "" when it isn't one
// person's name (a team, a handle, several people, the outlet).
func PersonName(s string) (string, string) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "By "))
	parts := strings.Fields(s)
	if len(parts) < 2 || len(parts) > 3 || strings.ContainsAny(s, "@,&|/") {
		return "", ""
	}
	for _, p := range parts {
		if r := []rune(p); len(r) < 2 || r[0] < 'A' || r[0] > 'Z' {
			return "", ""
		}
	}
	return parts[0], parts[len(parts)-1]
}

// publishedAt reads a page's publish date in the shapes sites write it.
func publishedAt(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z0700", "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
