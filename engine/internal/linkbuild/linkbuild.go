// Package linkbuild checks link prospects' pages (link building, P2): is the
// page up, does it link to the site already, which of its outbound links are
// broken and what the Internet Archive kept of them, what the page is about
// and how well it fits (the workspace's own AI, when connected), which of the
// site's pages to offer, and who to contact there. OutflareBot reads at most
// a handful of pages per check, obeying robots.txt and crawl opt-outs; the
// email finder's free tier guesses an author's address. Contacts become
// person records on the website's company.
package linkbuild

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strconv"
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
	"github.com/outflareai/outflare/engine/internal/research"
	"github.com/outflareai/outflare/engine/internal/seosync"
	"github.com/outflareai/outflare/engine/internal/track"
)

// JobCheck checks one prospect's page.
const JobCheck = "link_prospects.check"

// PageReadTask is the AI task (and usage tool) of the page reader.
const PageReadTask = "link_page_reading"

// Limits per check.
const (
	// maxLinkChecks is how many outbound links a check reads.
	maxLinkChecks = 50
	// maxArchive is how many broken links it looks up in the archive.
	maxArchive = 10
	// maxContactPages is how many contact, about or write-for-us pages
	// it reads.
	maxContactPages = 2
	// aiCents is about what one page read costs on the workspace's AI.
	aiCents = 2
)

// Service runs the checks.
type Service struct {
	Pool  *pgxpool.Pool
	Log   *slog.Logger
	Crawl *crawl.Service
	// Models is the workspace's own AI; nil skips the page reader.
	Models research.Models
	// Emails guesses an author's address; nil skips it.
	Emails *emailfind.Service
	// Archive looks dead URLs up; nil uses the Internet Archive.
	Archive Archive
}

// Register adds the job handler.
func (s *Service) Register(q *jobs.Queue) {
	q.Handle(JobCheck, s.check)
	q.Handle(JobWriteOpener, s.writeOpener)
	q.Handle(JobReadReply, s.readReply)
}

type checkPayload struct {
	ProspectID string `json:"prospect_id"`
}

// StartCheck queues a check of a prospect's page, at most once an hour:
// not when it was checked in the last hour or a check is already queued
// this hour. It reports whether a new check was queued.
func StartCheck(ctx context.Context, q jobs.Querier, ws, prospectID string, now time.Time) (bool, error) {
	var last *time.Time
	if err := q.QueryRow(ctx, `SELECT max(checked_at) FROM link_page_checks WHERE prospect_id = $1 AND workspace_id = $2`, prospectID, ws).Scan(&last); err != nil {
		return false, err
	}
	if last != nil && last.After(now.Add(-time.Hour)) {
		return false, nil
	}
	// One check an hour, and a finished check never holds back the next.
	key := "link_check:" + prospectID + ":" + now.UTC().Format("2006-01-02T15")
	if last != nil {
		key += ":" + strconv.FormatInt(last.UnixMicro(), 10)
	}
	id, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobCheck, Payload: checkPayload{ProspectID: prospectID}, WorkspaceID: &ws,
		MaxAttempts: 3, IdempotencyKey: key})
	return id != 0, err
}

// Check is what a check found, stored in link_page_checks.
type Check struct {
	Status          string       `json:"status"`
	HTTPStatus      *int         `json:"http_status"`
	FinalURL        string       `json:"final_url"`
	RedirectTo      string       `json:"redirect_to"`
	Title           string       `json:"title"`
	Author          string       `json:"author"`
	Lang            string       `json:"lang"`
	Noindex         bool         `json:"noindex"`
	WordCount       int          `json:"word_count"`
	LinksToUs       bool         `json:"links_to_us"`
	OurLinks        []OurLink    `json:"our_links"`
	Outbound        int          `json:"outbound"`
	Broken          []BrokenLink `json:"broken"`
	PageKind        string       `json:"page_kind"`
	Topic           string       `json:"topic"`
	Fit             *int         `json:"fit"`
	FitReason       string       `json:"fit_reason"`
	AIError         *string      `json:"ai_error"`
	SuggestedTarget string       `json:"suggested_target"`
	SuggestedReason string       `json:"suggested_reason"`
	ErrorCode       *string      `json:"error_code"`
	CheckedAt       time.Time    `json:"checked_at"`
	contacts        []contact
	// liveOnly: a check of an agreed or live link, which keeps the earlier
	// check's read of the page.
	liveOnly bool
}

// OurLink is a link from the page to the site.
type OurLink struct {
	URL      string `json:"url"`
	Anchor   string `json:"anchor"`
	Nofollow bool   `json:"nofollow"`
}

// BrokenLink is an outbound link that is gone, and what the archive kept.
type BrokenLink struct {
	URL        string `json:"url"`
	Status     int    `json:"status"`
	Anchor     string `json:"anchor"`
	ArchiveURL string `json:"archive_url"`
	ArchivedAt string `json:"archived_at"`
}

type contact struct {
	email, name, source string
	confidence          int
}

type prospect struct {
	ws, siteID, siteHost, recordID, pageURL, method, stage, targetURL string
	brandID                                                           *string
	rating, traffic                                                   *float64
	spam                                                              *int
	test                                                              bool
}

func (s *Service) check(ctx context.Context, job jobs.Job) error {
	var p checkPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	var pr prospect
	err := s.Pool.QueryRow(ctx, `
		SELECT x.workspace_id, x.site_id, s.host, x.record_id, x.page_url, x.method, x.stage, x.target_url, x.brand_id::text,
			x.domain_rating, x.traffic, x.spam_score, w.test
		FROM link_prospects x JOIN seo_sites s ON s.id = x.site_id JOIN workspaces w ON w.id = x.workspace_id
		WHERE x.id = $1`, p.ProspectID).Scan(&pr.ws, &pr.siteID, &pr.siteHost, &pr.recordID, &pr.pageURL, &pr.method, &pr.stage,
		&pr.targetURL, &pr.brandID, &pr.rating, &pr.traffic, &pr.spam, &pr.test)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	c := s.read(ctx, pr)
	metrics.LinkPageCheck(c.Status)
	if err := s.store(ctx, p.ProspectID, pr, c); err != nil {
		track.Fail(ctx, s.Log, track.Event{Name: "link_prospect.check_failed", WorkspaceID: pr.ws, Surface: "job",
			Props: map[string]any{"prospect_id": p.ProspectID, "site_id": pr.siteID}}, "internal", err)
		return err
	}
	return nil
}

// read looks at the page and everything it leads to. It never fails: what
// went wrong is the check's status.
func (s *Service) read(ctx context.Context, pr prospect) Check {
	c := Check{OurLinks: []OurLink{}, Broken: []BrokenLink{}, CheckedAt: time.Now().UTC()}
	fail := func(status, code string) Check {
		c.Status = status
		if code != "" {
			c.ErrorCode = &code
		}
		return c
	}
	u, err := url.Parse(pr.pageURL)
	if err != nil {
		return fail("failed", "invalid_request")
	}
	if out, err := crawl.OptedOut(ctx, s.Pool, u.Hostname()); err != nil {
		return fail("failed", "internal")
	} else if out {
		return fail("opted_out", "crawl_opted_out")
	}
	if !pr.test {
		if err := crawl.CheckURL(pr.pageURL, s.Crawl.AllowPrivate); err != nil {
			return fail("unreachable", "site_unreachable")
		}
	}
	f, wait := s.Crawl.ReadFetcher(pr.test)
	rd, err := crawl.ReadPage(ctx, f, wait, pr.pageURL)
	switch {
	case errors.Is(err, crawl.ErrBlocked):
		return fail("blocked", "robots_disallowed")
	case err != nil:
		return fail("unreachable", "site_unreachable")
	}
	c.HTTPStatus, c.FinalURL, c.RedirectTo = &rd.Status, rd.FinalURL, rd.Redirect
	switch {
	case rd.Redirect != "":
		c.Status = "redirected"
		return c
	case rd.Status == 404 || rd.Status == 410:
		c.Status = "gone"
		return c
	case rd.Status != 200:
		return fail("failed", "site_unreachable")
	}
	c.Status = "up"
	c.Title, c.Author, c.Lang, c.Noindex, c.WordCount = rd.Title, rd.Author, rd.Lang, rd.Noindex, rd.WordCount
	site := seosync.ProspectSite{ID: pr.siteID, Host: pr.siteHost}
	page, _ := url.Parse(rd.FinalURL)
	var outbound []crawl.Link
	for _, l := range rd.Links {
		lu, err := url.Parse(l.URL)
		if err != nil {
			continue
		}
		_, d := seosync.ProspectPage(l.URL)
		switch {
		case seosync.OwnDomain(site, d):
			c.LinksToUs = true
			c.OurLinks = append(c.OurLinks, OurLink{URL: l.URL, Anchor: clip(l.Anchor, 200), Nofollow: l.Nofollow})
		case !strings.EqualFold(strings.TrimPrefix(lu.Hostname(), "www."), strings.TrimPrefix(page.Hostname(), "www.")):
			outbound = append(outbound, l)
		}
	}
	c.Outbound = len(outbound)
	// An agreed or live link only needs to know whether it is there.
	if c.liveOnly = pr.stage == "agreed" || pr.stage == "live"; c.liveOnly {
		return c
	}
	// Broken outbound links, and what the archive kept of them.
	check := outbound[:min(len(outbound), maxLinkChecks)]
	urls := make([]string, len(check))
	for i, l := range check {
		urls[i] = l.URL
	}
	for i, st := range crawl.CheckLinks(ctx, f, wait, urls) {
		if st.Broken {
			c.Broken = append(c.Broken, BrokenLink{URL: st.URL, Status: st.Status, Anchor: clip(check[i].Anchor, 200)})
		}
	}
	archive := s.Archive
	if archive == nil {
		archive = wayback{test: pr.test}
	}
	for i := range c.Broken[:min(len(c.Broken), maxArchive)] {
		snap, at, err := archive.Closest(ctx, c.Broken[i].URL)
		outcome := "none"
		switch {
		case err != nil:
			outcome = "error"
		case snap != "":
			outcome = "found"
		}
		metrics.ArchiveLookup(outcome)
		if err == nil {
			c.Broken[i].ArchiveURL, c.Broken[i].ArchivedAt = snap, at
		}
	}
	s.readWithAI(ctx, pr, rd, &c)
	if c.SuggestedTarget == "" {
		c.SuggestedTarget, c.SuggestedReason = s.bestPage(ctx, pr, rd, c)
	}
	c.contacts = s.findContacts(ctx, pr, f, wait, rd, c.Author)
	return c
}

// store keeps the check, moves the prospect when the page says so, adds
// the contacts as person records and records link_prospect.checked, in one
// transaction.
func (s *Service) store(ctx context.Context, id string, pr prospect, c Check) error {
	our, _ := json.Marshal(c.OurLinks)
	broken, _ := json.Marshal(c.Broken)
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if c.liveOnly {
			if _, err := tx.Exec(ctx, `
				INSERT INTO link_page_checks (prospect_id, workspace_id, status, http_status, final_url, redirect_to, links_to_us, our_links, error_code, checked_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
				ON CONFLICT (prospect_id) DO UPDATE SET status = EXCLUDED.status, http_status = EXCLUDED.http_status, final_url = EXCLUDED.final_url,
					redirect_to = EXCLUDED.redirect_to, links_to_us = EXCLUDED.links_to_us, our_links = EXCLUDED.our_links,
					error_code = EXCLUDED.error_code, checked_at = EXCLUDED.checked_at`,
				id, pr.ws, c.Status, c.HTTPStatus, clip(c.FinalURL, 2048), clip(c.RedirectTo, 2048), c.LinksToUs, our, c.ErrorCode, c.CheckedAt); err != nil {
				return err
			}
		} else if err := s.newlyBroken(ctx, tx, id, pr, c); err != nil {
			return err
		} else if _, err := tx.Exec(ctx, `
			INSERT INTO link_page_checks (prospect_id, workspace_id, status, http_status, final_url, redirect_to, title, author, lang, noindex,
				word_count, links_to_us, our_links, outbound, broken, page_kind, topic, fit, fit_reason, ai_error, suggested_target,
				suggested_reason, error_code, checked_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24)
			ON CONFLICT (prospect_id) DO UPDATE SET status = EXCLUDED.status, http_status = EXCLUDED.http_status, final_url = EXCLUDED.final_url,
				redirect_to = EXCLUDED.redirect_to, title = EXCLUDED.title, author = EXCLUDED.author, lang = EXCLUDED.lang, noindex = EXCLUDED.noindex,
				word_count = EXCLUDED.word_count, links_to_us = EXCLUDED.links_to_us, our_links = EXCLUDED.our_links, outbound = EXCLUDED.outbound,
				broken = EXCLUDED.broken, page_kind = EXCLUDED.page_kind, topic = EXCLUDED.topic, fit = EXCLUDED.fit, fit_reason = EXCLUDED.fit_reason,
				ai_error = EXCLUDED.ai_error, suggested_target = EXCLUDED.suggested_target, suggested_reason = EXCLUDED.suggested_reason,
				error_code = EXCLUDED.error_code, checked_at = EXCLUDED.checked_at`,
			id, pr.ws, c.Status, c.HTTPStatus, clip(c.FinalURL, 2048), clip(c.RedirectTo, 2048), c.Title, c.Author, clip(c.Lang, 20), c.Noindex,
			c.WordCount, c.LinksToUs, our, c.Outbound, broken, c.PageKind, c.Topic, c.Fit, c.FitReason, c.AIError, c.SuggestedTarget,
			c.SuggestedReason, c.ErrorCode, c.CheckedAt); err != nil {
			return err
		}
		// What the page says about the prospect: a page that already links
		// or is gone is kept out of outreach while nobody has pitched it.
		var reason *string
		if pr.stage == "prospect" {
			switch {
			case c.Status == "gone":
				reason = ptr("page_gone")
			case c.LinksToUs && pr.method != "reclaim" && pr.method != "unlinked_mention":
				reason = ptr("links_to_us")
			}
		}
		score, factors := seosync.ProspectScore(pr.method, pr.rating, pr.traffic, pr.spam, c.Fit)
		fb, _ := json.Marshal(factors)
		// A live check leaves the score as the full check left it.
		if !c.liveOnly {
			if _, err := tx.Exec(ctx, `UPDATE link_prospects SET score = $3, score_factors = $4,
					target_url = CASE WHEN target_url = '' THEN $5 ELSE target_url END,
					stage = CASE WHEN $6::text IS NULL THEN stage ELSE 'excluded' END,
					excluded_reason = COALESCE($6, excluded_reason),
					stage_changed_at = CASE WHEN $6::text IS NULL THEN stage_changed_at ELSE now() END, updated_at = now()
				WHERE id = $1 AND workspace_id = $2`, id, pr.ws, score, fb, c.SuggestedTarget, reason); err != nil {
				return err
			}
		}
		rec := seosync.SystemRecorder(pr.ws)
		// The live check: an agreed link now on the page is live; a live
		// one gone from a page that is up, or from a page that is gone, is
		// removed.
		move, live := "", ""
		switch {
		case pr.stage == "agreed" && c.Status == "up" && c.LinksToUs:
			move, live = "live", "live"
		case pr.stage == "agreed":
			live = map[bool]string{true: "not_yet", false: "unreachable"}[c.Status == "up"]
		case pr.stage == "live" && ((c.Status == "up" && !c.LinksToUs) || c.Status == "gone"):
			move, live = "removed", "removed"
		case pr.stage == "live":
			live = map[bool]string{true: "still_live", false: "unreachable"}[c.Status == "up"]
		}
		if live != "" {
			metrics.LinkLiveCheck(live)
		}
		if move != "" {
			stamp := map[string]string{"live": ", live_at = now()", "removed": ""}[move]
			if _, err := tx.Exec(ctx, `UPDATE link_prospects SET stage = $2, stage_changed_at = now(), updated_at = now()`+stamp+`
				WHERE id = $1`, id, move); err != nil {
				return err
			}
			data := map[string]any{"site_id": pr.siteID, "brand_id": pr.brandID, "record_id": pr.recordID, "method": pr.method, "stage": move,
				"was": pr.stage, "by": "live_check", "links": c.OurLinks}
			if err := rec(ctx, tx, "link_prospect."+move, id, data, map[string]any{"site_id": pr.siteID, "method": pr.method, "stage": move,
				"was": pr.stage}); err != nil {
				return err
			}
		}
		if reason != nil {
			if err := rec(ctx, tx, "link_prospect.excluded", id, map[string]any{"site_id": pr.siteID, "brand_id": pr.brandID, "record_id": pr.recordID,
				"method": pr.method, "stage": "excluded", "was": pr.stage, "excluded_reason": *reason, "by": "page_check"},
				map[string]any{"site_id": pr.siteID, "method": pr.method, "stage": "excluded", "was": pr.stage, "excluded_reason": *reason}); err != nil {
				return err
			}
		}
		added, err := s.addContacts(ctx, tx, id, pr, c.contacts)
		if err != nil {
			return err
		}
		data := map[string]any{"site_id": pr.siteID, "brand_id": pr.brandID, "record_id": pr.recordID, "status": c.Status, "links_to_us": c.LinksToUs,
			"broken": len(c.Broken), "fit": c.Fit, "score": score, "contacts": added, "error_code": c.ErrorCode}
		return rec(ctx, tx, "link_prospect.checked", id, data, map[string]any{"site_id": pr.siteID, "method": pr.method, "status": c.Status,
			"links_to_us": c.LinksToUs, "broken": len(c.Broken), "fit": c.Fit, "contacts": added, "ai": c.Fit != nil})
	})
}

// maxBrokenEvents is how many outbound_link.broken events one check
// records.
const maxBrokenEvents = 10

// newlyBroken records outbound_link.broken for each link on the page that
// is broken now and wasn't at the page's previous check (every broken link,
// on its first check), before the check is stored. A playbook can start on
// it: the page needs a replacement for that link.
func (s *Service) newlyBroken(ctx context.Context, tx pgx.Tx, id string, pr prospect, c Check) error {
	if len(c.Broken) == 0 {
		return nil
	}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT COALESCE(broken, '[]') FROM link_page_checks WHERE prospect_id = $1`, id).Scan(&raw)
	first := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !first {
		return err
	}
	var before []BrokenLink
	_ = json.Unmarshal(raw, &before)
	seen := map[string]bool{}
	for _, b := range before {
		seen[b.URL] = true
	}
	n := 0
	for _, b := range c.Broken {
		if seen[b.URL] || n >= maxBrokenEvents {
			continue
		}
		n++
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: pr.ws, ActorType: "system", Action: "outbound_link.broken",
			TargetType: "link_prospect", TargetID: id, Data: map[string]any{
				"site_id": pr.siteID, "brand_id": pr.brandID, "prospect_id": id, "record_id": pr.recordID, "method": pr.method,
				"page_url": pr.pageURL, "broken_url": b.URL, "status": b.Status, "anchor": b.Anchor, "archive_url": b.ArchiveURL, "first_check": first},
			Props: map[string]any{"site_id": pr.siteID, "method": pr.method, "status": b.Status, "first_check": first, "archived": b.ArchiveURL != ""}}); err != nil {
			return err
		}
	}
	return nil
}

// addContacts makes each contact a person record on the website's company
// (an unverified address, so it never claims one someone proves) and notes
// which prospect found them. It returns how many are new to the prospect.
func (s *Service) addContacts(ctx context.Context, tx pgx.Tx, id string, pr prospect, list []contact) (int, error) {
	if len(list) == 0 {
		return 0, nil
	}
	people := make([]customers.Person, len(list))
	for i, c := range list {
		people[i] = customers.Person{Email: c.email, Name: c.name, Attributes: map[string]any{"companies": []any{pr.recordID}}}
	}
	ids, err := customers.EnsurePeople(ctx, tx, pr.ws, "prospect", "link_building", people)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range list {
		rid, ok := ids[c.email]
		if !ok {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO record_relationships (workspace_id, from_id, to_id, kind, field_key)
			SELECT $1, $2, $3, 'companies', 'companies'
			WHERE NOT EXISTS (SELECT 1 FROM record_relationships WHERE workspace_id = $1 AND from_id = $2 AND to_id = $3 AND kind = 'companies' AND valid_to IS NULL)`,
			pr.ws, rid, pr.recordID); err != nil {
			return n, err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO link_prospect_contacts (workspace_id, prospect_id, record_id, source, confidence)
			SELECT $1, $2, $3, $4, $5 WHERE NOT EXISTS (SELECT 1 FROM link_prospect_contacts WHERE prospect_id = $2 AND record_id = $3)`,
			pr.ws, id, rid, c.source, c.confidence)
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}

var contactPath = regexp.MustCompile(`(?i)/(contact|contact-us|about|about-us|write-for-us|contribute|guest-post|advertise|team|editorial)(/|\.html?)?$`)
var contactAnchor = regexp.MustCompile(`(?i)\b(contact|about|write for us|contribute|guest post|advertise|our team|editorial)\b`)

// genericMailbox is an address shape that reaches a team rather than a
// person; it is kept, but after a named person.
var genericMailbox = regexp.MustCompile(`^(info|hello|contact|editor|editors|editorial|team|press|media|admin|support|office|mail|web|webmaster|partnerships?)@`)

// findContacts finds who to write to: addresses on the page, then on up to
// two of the site's contact, about or write-for-us pages, and the author's
// likely address from the free email finder. Only addresses at the
// website's own domain count, so a widget's or another site's never does.
func (s *Service) findContacts(ctx context.Context, pr prospect, f crawl.Fetcher, wait func(context.Context, time.Duration) error, rd *crawl.Read, author string) []contact {
	_, domain := seosync.ProspectPage(pr.pageURL)
	seen := map[string]bool{}
	var out []contact
	add := func(email, name, source string, conf int) {
		email = strings.ToLower(strings.TrimSpace(email))
		at := strings.LastIndex(email, "@")
		if at < 0 || seen[email] {
			return
		}
		host := email[at+1:]
		if host != domain && !strings.HasSuffix(host, "."+domain) && !strings.HasSuffix(domain, "."+host) {
			return
		}
		seen[email] = true
		if genericMailbox.MatchString(email) {
			conf -= 10
		}
		out = append(out, contact{email: email, name: clip(name, 200), source: source, confidence: conf})
	}
	for _, e := range rd.Emails {
		add(e, "", "page", 70)
	}
	page, _ := url.Parse(rd.FinalURL)
	read := 0
	for _, l := range rd.Links {
		if read >= maxContactPages {
			break
		}
		lu, err := url.Parse(l.URL)
		if err != nil || !strings.EqualFold(strings.TrimPrefix(lu.Hostname(), "www."), strings.TrimPrefix(page.Hostname(), "www.")) {
			continue
		}
		if !contactPath.MatchString(lu.Path) && !contactAnchor.MatchString(l.Anchor) {
			continue
		}
		read++
		cp, err := crawl.ReadPage(ctx, f, wait, l.URL)
		if err != nil || cp.Status != 200 {
			continue
		}
		for _, e := range cp.Emails {
			add(e, "", "contact_page", 60)
		}
	}
	if first, last := splitName(author); s.Emails != nil && first != "" && last != "" {
		res, err := emailfind.FindEmails(ctx, s.Pool, s.Emails.Resolver, pr.ws, emailfind.FindRequest{Items: []emailfind.FindItem{
			{Ref: "author", FirstName: first, LastName: last, Domain: domain}}})
		if err == nil && len(res.Items) == 1 {
			it := res.Items[0]
			if it.Email != "" && (it.Code == emailfind.CodeFound || it.Code == emailfind.CodeRisky) && it.Confidence >= 40 {
				add(it.Email, author, "author", it.Confidence)
			}
		}
	}
	// Named people first, then the team addresses, the surest first.
	slices.SortStableFunc(out, func(a, b contact) int { return b.confidence - a.confidence })
	return out[:min(len(out), 5)]
}

// splitName is a person's first and last name, or "" when the author line
// isn't one person's name (a team, a handle, several people).
func splitName(s string) (string, string) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "By "))
	parts := strings.Fields(s)
	if len(parts) < 2 || len(parts) > 3 || strings.ContainsAny(s, "@,&|/") {
		return "", ""
	}
	for _, p := range parts {
		if r := []rune(p); len(r) < 2 || !(r[0] >= 'A' && r[0] <= 'Z') {
			return "", ""
		}
	}
	return parts[0], parts[len(parts)-1]
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8Start(s[n]) {
		n--
	}
	return s[:n]
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func ptr[T any](v T) *T { return &v }
