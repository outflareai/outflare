package emails

import (
	"context"
	"errors"
	"html"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/tracking"
)

// ---- Open and click tracking ----
//
// Both are off unless a template or a send turns them on. Clicks go
// through the same /c/ links as sequence email; opens are a one-pixel
// image at /o/. Links point at the workspace's tracking domain when it has
// a verified one.

// hrefs finds the target of each link in HTML.
var hrefs = regexp.MustCompile(`(?i)(<a\b[^>]*?\bhref\s*=\s*)(["'])(https?://[^"']+)(["'])`)

// addTracking rewrites an email's links to click links and adds the open
// pixel. It returns the new content and the links to save, by id.
func (s *Service) addTracking(base, emailID, htmlBody, text string, opens, clicks bool) (string, string, map[string]string) {
	links := map[string]string{} // id -> url
	ids := map[string]string{}   // url -> id
	link := func(url string) string {
		if strings.HasPrefix(url, base+"/") || strings.HasPrefix(url, strings.TrimRight(s.PublicURL, "/")+"/u/") {
			return url // the unsubscribe link, or one already tracked
		}
		id, ok := ids[url]
		if !ok {
			id = newID()
			ids[url], links[id] = id, url
		}
		return base + "/c/" + id
	}
	if clicks {
		htmlBody = hrefs.ReplaceAllStringFunc(htmlBody, func(m string) string {
			p := hrefs.FindStringSubmatch(m)
			return p[1] + p[2] + link(html.UnescapeString(p[3])) + p[4]
		})
		text = tracking.ReplaceLinks(text, link)
	}
	if opens && htmlBody != "" {
		pixel := `<img src="` + base + "/o/" + s.openToken(emailID) + `" width="1" height="1" alt="" style="display:block;width:1px;height:1px;border:0">`
		if i := strings.LastIndex(strings.ToLower(htmlBody), "</body>"); i >= 0 {
			htmlBody = htmlBody[:i] + pixel + htmlBody[i:]
		} else {
			htmlBody += pixel
		}
	}
	return htmlBody, text, links
}

var pixelTag = regexp.MustCompile(`<img src="[^"]*/o/[0-9a-f-]{36}\.[^"]*"[^>]*>`)
var clickLink = regexp.MustCompile(`https?://[^\s"'<>]+/c/([0-9a-f-]{36})`)

// Untracked is an email's content as written: click links back to their
// targets and no open pixel, so showing it in Outflare never counts as an
// open or a click.
func Untracked(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, ws, emailID, htmlBody, text string) (string, string, error) {
	rows, err := q.Query(ctx, `SELECT id::text, url FROM tracked_links WHERE workspace_id = $1 AND email_id = $2`, ws, emailID)
	if err != nil {
		return "", "", err
	}
	links := map[string]string{}
	for rows.Next() {
		var id, url string
		if err := rows.Scan(&id, &url); err != nil {
			rows.Close()
			return "", "", err
		}
		links[id] = url
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	back := func(escape bool) func(string) string {
		return func(m string) string {
			url, ok := links[clickLink.FindStringSubmatch(m)[1]]
			if !ok {
				return m
			}
			if escape {
				return html.EscapeString(url)
			}
			return url
		}
	}
	htmlBody = clickLink.ReplaceAllStringFunc(pixelTag.ReplaceAllString(htmlBody, ""), back(true))
	return htmlBody, clickLink.ReplaceAllStringFunc(text, back(false)), nil
}

func saveLinks(ctx context.Context, tx pgx.Tx, ws, emailID string, links map[string]string) error {
	for id, url := range links {
		if _, err := tx.Exec(ctx, `INSERT INTO tracked_links (id, workspace_id, email_id, step_position, url) VALUES ($1, $2, $3, 0, $4)`,
			id, ws, emailID, url); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) openToken(emailID string) string {
	return emailID + "." + s.Box.Sign("email_open", emailID)
}

// ParseOpen returns the email id in an open pixel's token, if genuine.
func (s *Service) ParseOpen(token string) (string, bool) {
	token = strings.TrimSuffix(token, ".gif")
	id, sig, ok := strings.Cut(token, ".")
	if !ok || len(id) != 36 || !s.Box.Verify("email_open", id, sig) {
		return "", false
	}
	return id, true
}

// Opened records that an email's pixel loaded. Loads by mail scanners and
// privacy proxies right after sending stay on the timeline but are not
// counted.
func (s *Service) Opened(ctx context.Context, emailID, userAgent string, now time.Time) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var ws string
		var sentAt *time.Time
		err := tx.QueryRow(ctx, `SELECT workspace_id, sent_at FROM emails WHERE id = $1 AND track_opens`, emailID).Scan(&ws, &sentAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return s.engaged(ctx, tx, ws, emailID, "opened", "", userAgent, sentAt, now)
	})
}

// Clicked records a click on a tracked link if the link is in a lifecycle
// email. Sequence links are recorded by tracking.Click alone.
func (s *Service) Clicked(ctx context.Context, linkID, userAgent string, now time.Time) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var ws, emailID, url string
		var sentAt *time.Time
		err := tx.QueryRow(ctx, `
			SELECT e.workspace_id, e.id, l.url, e.sent_at FROM tracked_links l JOIN emails e ON e.id = l.email_id
			WHERE l.id = $1`, linkID).Scan(&ws, &emailID, &url, &sentAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return s.engaged(ctx, tx, ws, emailID, "clicked", url, userAgent, sentAt, now)
	})
}

// engaged adds an open or a click to an email's timeline and counts it,
// unless it looks automated.
func (s *Service) engaged(ctx context.Context, tx pgx.Tx, ws, emailID, kind, detail, userAgent string, sentAt *time.Time, now time.Time) error {
	sent := now
	if sentAt != nil {
		sent = *sentAt
	}
	if tracking.LikelyBot(userAgent, sent, now) {
		note := "Likely automatic: a mail scanner or privacy proxy, so it is not counted."
		if detail != "" {
			note = detail + " · " + note
		}
		return addEvent(ctx, tx, ws, emailID, kind+"_automatic", "", note, "")
	}
	if err := addEvent(ctx, tx, ws, emailID, kind, "", detail, ""); err != nil {
		return err
	}
	return s.count(ctx, tx, ws, emailID, kind, now)
}

// count adds one open or click to an email; the first is an audit event
// (email.opened, email.clicked), so webhooks and journeys can act on it.
func (s *Service) count(ctx context.Context, q jobs.Querier, ws, emailID, kind string, at time.Time) error {
	col := map[string]string{"opened": "opens", "clicked": "clicks"}[kind]
	if col == "" {
		return nil
	}
	var first bool
	err := q.QueryRow(ctx, `
		UPDATE emails SET `+col+` = `+col+` + 1, `+kind+`_at = COALESCE(`+kind+`_at, $2), updated_at = now()
		WHERE id = $1 RETURNING `+col+` = 1`, emailID, at).Scan(&first)
	if err != nil || !first {
		return err
	}
	if err := onTimeline(ctx, q, ws, emailID, kind, nil, nil, at); err != nil {
		return err
	}
	return s.record(ctx, q, System, ws, "email."+kind, "email", emailID, nil)
}
