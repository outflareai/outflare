package linkorders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The donor email search: OutflareBot reads a donor's home page and up to
// maxFinderPages of its contact, advertising, imprint and about pages (in
// the languages donor lists are in), and keeps the addresses the site
// publishes for itself, best first: the advertising desk, then the
// editors, then a general mailbox. Addresses that must never get a
// placement pitch (the data-protection officer, abuse, legal, jobs,
// no-reply) are dropped. It obeys robots.txt and crawl opt-outs like every
// OutflareBot read, and costs nothing.

// JobFindEmails searches one donor's site.
const JobFindEmails = "link_orders.find_emails"

// MaxFindEmails is how many donors one request may queue.
const MaxFindEmails = 50

const (
	maxFinderPages  = 5
	maxFinderEmails = 3
)

// Email search outcomes (email_search_status).
const (
	EmailSearchFound       = "found"
	EmailSearchNone        = "none"
	EmailSearchBlocked     = "blocked"
	EmailSearchUnreachable = "unreachable"
	EmailSearchOptedOut    = "opted_out"
	EmailSearchFailed      = "failed"
)

// Finder runs the searches.
type Finder struct {
	Pool  *pgxpool.Pool
	Log   *slog.Logger
	Crawl *crawl.Service
}

// Register adds the job handler.
func (f *Finder) Register(q *jobs.Queue) { q.Handle(JobFindEmails, f.find) }

type findPayload struct {
	DonorID string `json:"donor_id"`
}

// QueueFindEmails queues a search of each donor's site, at most one a day
// per donor, and returns how many were queued. Donors of other brands are
// ignored.
func QueueFindEmails(ctx context.Context, tx pgx.Tx, workspaceID, brandID uuid.UUID, donorIDs []uuid.UUID, now time.Time) (int, error) {
	if len(donorIDs) == 0 || len(donorIDs) > MaxFindEmails {
		return 0, fmt.Errorf("%w: send 1 to %d donors", ErrInvalid, MaxFindEmails)
	}
	day := now.UTC().Format("2006-01-02")
	// Rows searched today, or with a search queued today, are left alone.
	rows, err := tx.Query(ctx, `SELECT d.id FROM coverage_donor_profiles d
		WHERE d.workspace_id = $1 AND d.brand_id = $2 AND d.id = ANY($3)
			AND (d.email_search_at IS NULL OR d.email_search_at < $4::date)
			AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.kind = $5 AND j.idempotency_key = 'find_emails:' || d.id::text || ':' || $6)`,
		workspaceID, brandID, donorIDs, day, JobFindEmails, day)
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, err
	}
	ws := workspaceID.String()
	n := 0
	for _, id := range ids {
		if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobFindEmails, Payload: findPayload{DonorID: id.String()}, WorkspaceID: &ws,
			MaxAttempts: 2, IdempotencyKey: "find_emails:" + id.String() + ":" + day}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

type finderDonor struct {
	ws, brand, domain string
	companyID         uuid.UUID
	test              bool
}

func (f *Finder) find(ctx context.Context, job jobs.Job) error {
	var p findPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	var d finderDonor
	err := f.Pool.QueryRow(ctx, `
		SELECT d.workspace_id::text, d.brand_id::text, p.domain, p.record_id, w.test
		FROM coverage_donor_profiles d
		JOIN coverage_publishers p ON p.id = d.publisher_id
		JOIN workspaces w ON w.id = d.workspace_id
		WHERE d.id = $1`, p.DonorID).Scan(&d.ws, &d.brand, &d.domain, &d.companyID, &d.test)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	start := time.Now()
	status, found := f.search(ctx, d)
	metrics.DonorEmailSearch(status, time.Since(start))
	ev := track.Event{Name: "link_order.emails_searched", WorkspaceID: d.ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"brand_id": d.brand, "donor_id": p.DonorID, "status": status, "found": len(found)}}
	err = pgx.BeginFunc(ctx, f.Pool, func(tx pgx.Tx) error {
		if len(found) > 0 {
			if err := f.keep(ctx, tx, d, found); err != nil {
				return err
			}
		}
		emails := make([]string, len(found))
		for i, c := range found {
			emails[i] = c.email
		}
		_, err := tx.Exec(ctx, `UPDATE coverage_donor_profiles SET email_search_at = now(), email_search_status = $2, email_search_emails = $3
			WHERE id = $1`, p.DonorID, status, emails)
		return err
	})
	if err != nil {
		track.Fail(ctx, f.Log, ev, "internal", fmt.Errorf("store the email search of donor %s: %w", p.DonorID, err))
		return err
	}
	if status == EmailSearchFailed || status == EmailSearchUnreachable {
		ev.Outcome = track.Failure
		track.Fail(ctx, f.Log, ev, "site_unreachable", fmt.Errorf("email search of donor %s: %s", p.DonorID, status))
		return nil
	}
	track.Emit(ctx, ev)
	return nil
}

// keep makes each address a person record on the site's company, in the
// donor's brand, unverified, so it shows with the donor's contacts.
func (f *Finder) keep(ctx context.Context, tx pgx.Tx, d finderDonor, found []foundEmail) error {
	people := make([]customers.Person, len(found))
	for i, c := range found {
		people[i] = customers.Person{Email: c.email, Attributes: map[string]any{"companies": []any{d.companyID.String()}}}
	}
	ids, err := customers.EnsurePeopleIn(ctx, tx, d.ws, d.brand, "prospect", "link_orders", people)
	if err != nil {
		return err
	}
	for _, c := range found {
		rid, ok := ids[c.email]
		if !ok {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO record_relationships (workspace_id, from_id, to_id, kind, field_key)
			SELECT $1, $2, $3, 'companies', 'companies'
			WHERE NOT EXISTS (SELECT 1 FROM record_relationships WHERE workspace_id = $1 AND from_id = $2 AND to_id = $3 AND kind = 'companies' AND valid_to IS NULL)`,
			d.ws, rid, d.companyID); err != nil {
			return err
		}
	}
	return nil
}

type foundEmail struct {
	email string
	score int
}

// search reads the site and returns its outcome and addresses, best
// first. It never fails: what went wrong is the outcome.
func (f *Finder) search(ctx context.Context, d finderDonor) (string, []foundEmail) {
	home := "https://" + d.domain + "/"
	if out, err := crawl.OptedOut(ctx, f.Pool, d.domain); err != nil {
		return EmailSearchFailed, nil
	} else if out {
		return EmailSearchOptedOut, nil
	}
	if !d.test {
		if err := crawl.CheckURL(home, f.Crawl.AllowPrivate); err != nil {
			return EmailSearchUnreachable, nil
		}
	}
	fetch, wait := f.Crawl.ReadFetcher(d.test)
	rd, err := crawl.ReadPage(ctx, fetch, wait, home)
	switch {
	case errors.Is(err, crawl.ErrBlocked):
		return EmailSearchBlocked, nil
	case err != nil, rd.Status != 200:
		return EmailSearchUnreachable, nil
	}
	got := map[string]int{}
	take := func(r *crawl.Read, pageScore int) {
		for _, e := range pageEmails(r) {
			if s, ok := emailScore(e, d.domain); ok && s+pageScore > got[e] {
				got[e] = s + pageScore
			}
		}
	}
	take(rd, 0)
	for _, u := range contactPages(rd, maxFinderPages) {
		cp, err := crawl.ReadPage(ctx, fetch, wait, u.url)
		if err != nil || cp.Status != 200 {
			continue
		}
		take(cp, u.score)
	}
	if len(got) == 0 {
		return EmailSearchNone, nil
	}
	out := make([]foundEmail, 0, len(got))
	for e, s := range got {
		out = append(out, foundEmail{email: e, score: s})
	}
	slices.SortFunc(out, func(a, b foundEmail) int {
		if a.score != b.score {
			return b.score - a.score
		}
		return strings.Compare(a.email, b.email)
	})
	return EmailSearchFound, out[:min(len(out), maxFinderEmails)]
}

// pageHints are the pages worth opening, best first, by words in their
// address or link text, in the languages donor lists are in. An imprint
// is where a European publisher must list its address.
var pageHints = []struct {
	score int
	re    *regexp.Regexp
}{
	{6, regexp.MustCompile(`(?i)impressum|imprint|legal[-_ ]?notice|impresum|kolofon|aviso[-_ ]?legal|note[-_ ]?legali|mentions[-_ ]?legales`)},
	{6, regexp.MustCompile(`(?i)\bwerbung\b|\bwerben\b|mediadaten|advertis|anzeigen|media[-_ ]?kit|oglas[ei]vanj|oglaševanj|publicidad|pubblicit|publicit[eé]|reklam|annonser|advertentie|inzercia|inzerce|sponsor`)},
	{5, regexp.MustCompile(`(?i)\bkontakt\b|\bcontact\b|kontakte|contact[-_ ]?us|kontakti|contacto|cont[aá]ctanos|contatti|contattaci|contactez|ileti[sş]im|contato|kapcsolat|napi[sš]ite`)},
	{4, regexp.MustCompile(`(?i)write[-_ ]?for[-_ ]?us|guest[-_ ]?post|gastbeitrag|gastartikel|contribute|advertorial|collabora|escribe[-_ ]?para`)},
	{3, regexp.MustCompile(`(?i)redaktion|editorial|\bteam\b|ueber[-_ ]?uns|about[-_ ]?us|\babout\b|uredni[sš]tv|redakcij|\bo[-_ ]nas\b|over[-_ ]?ons|chi[-_ ]?siamo|qui[eé]nes|qui[-_ ]?sommes|sobre[-_ ]?n[oó]s`)},
}

// skipPage is a page that never lists who to write to about a placement.
var skipPage = regexp.MustCompile(`(?i)datenschutz|privacy|cookie|\bagb\b|\bterms\b|widerruf|/tag/|/category/|/author/|/feed|/rss|wp-login|wp-admin|/cart|/checkout|/login|/search|\.(?:jpe?g|png|gif|webp|svg|pdf|zip|css|js)(?:$|\?)`)

type pageLink struct {
	url   string
	score int
}

// contactPages are the site's own pages worth reading, best first.
func contactPages(rd *crawl.Read, limit int) []pageLink {
	base, err := url.Parse(rd.FinalURL)
	if err != nil {
		return nil
	}
	seen := map[string]bool{rd.FinalURL: true}
	var out []pageLink
	for _, l := range rd.Links {
		lu, err := url.Parse(l.URL)
		if err != nil || !sameSite(lu.Hostname(), base.Hostname()) || skipPage.MatchString(lu.Path) {
			continue
		}
		lu.Fragment = ""
		u := lu.String()
		if seen[u] {
			continue
		}
		best := 0
		for _, h := range pageHints {
			if h.score > best && (h.re.MatchString(lu.Path) || h.re.MatchString(l.Anchor)) {
				best = h.score
			}
		}
		if best == 0 {
			continue
		}
		seen[u] = true
		out = append(out, pageLink{url: u, score: best})
	}
	slices.SortStableFunc(out, func(a, b pageLink) int { return b.score - a.score })
	return out[:min(len(out), limit)]
}

func sameSite(a, b string) bool {
	a, b = strings.TrimPrefix(strings.ToLower(a), "www."), strings.TrimPrefix(strings.ToLower(b), "www.")
	return a == b || strings.HasSuffix(a, "."+b) || strings.HasSuffix(b, "."+a)
}

var (
	textEmail = regexp.MustCompile(`(?i)\b([a-z0-9._%+\-]{1,64})@([a-z0-9](?:[a-z0-9\-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9\-]{0,61}[a-z0-9])?)+)\b`)
	// An address with "at" in brackets and "dot" written out, as publishers
	// hide them. A bare " at " is not read: "reach us at example.com" is
	// not an address.
	hiddenEmail = regexp.MustCompile(`(?i)\b([a-z0-9._%+\-]{1,64})\s*(?:\[\s*(?:at|ät)\s*\]|\(\s*(?:at|ät)\s*\)|\{\s*(?:at|ät)\s*\})\s*([a-z0-9\-]{1,63}(?:(?:\.|\s*(?:\[\s*(?:dot|punkt)\s*\]|\(\s*(?:dot|punkt)\s*\)|\s(?:dot|punkt)\s)\s*)[a-z0-9\-]{1,63})+)\b`)
	hiddenDot   = regexp.MustCompile(`(?i)\s*(?:\[\s*(?:dot|punkt)\s*\]|\(\s*(?:dot|punkt)\s*\)|\s(?:dot|punkt)\s)\s*`)
	invisible   = strings.NewReplacer("\u200b", "", "\u200c", "", "\u200d", "", "\u2060", "", "\ufeff", "", "\u00ad", "", "\uff20", "@", "\uff0e", ".")
)

// pageEmails are the addresses a page shows: its mailto links and the
// ones written in its text, including those written with "at" and "dot".
func pageEmails(r *crawl.Read) []string {
	out := slices.Clone(r.Emails)
	text := invisible.Replace(html.UnescapeString(r.Text))
	for _, m := range textEmail.FindAllStringSubmatch(text, -1) {
		out = append(out, strings.ToLower(m[1]+"@"+m[2]))
	}
	for _, m := range hiddenEmail.FindAllStringSubmatch(text, -1) {
		out = append(out, strings.ToLower(m[1]+"@"+hiddenDot.ReplaceAllString(m[2], ".")))
	}
	return out
}

// neverPitch is a mailbox that must never get a placement pitch: the
// data-protection officer (who files the complaint), abuse, legal, jobs,
// billing and machines.
var neverPitch = regexp.MustCompile(`^(?:datenschutz|privacy|dpo|gdpr|jugendschutz|compliance|abuse|security|postmaster|mailer-daemon|bounce|dmca|legal|recht|copyright|jobs|karriere|career|bewerbung|recruiting|hr|noreply|no-reply|donotreply|do-not-reply|newsletter|unsubscribe|billing|rechnung|buchhaltung|invoice|payment|example|test|demo|your|ihre|muster)(?:[._+\-]|$)`)

// rolePrefix ranks a mailbox by who answers a placement enquiry.
var rolePrefix = []struct {
	prefix string
	score  int
}{
	{"werbung", 95}, {"anzeigen", 95}, {"werben", 95}, {"anzeige", 95}, {"advertising", 95}, {"advertise", 95},
	{"ads", 90}, {"mediadaten", 90}, {"mediasales", 90}, {"reklame", 90}, {"marketing", 85}, {"sales", 85},
	{"vertrieb", 85}, {"partner", 80}, {"kooperation", 80}, {"sponsor", 80}, {"redaktion", 80}, {"editor", 80},
	{"editorial", 80}, {"media", 70}, {"presse", 70}, {"press", 70}, {"kontakt", 65}, {"contact", 65},
	{"office", 60}, {"hello", 50}, {"hallo", 50}, {"info", 50}, {"mail", 50}, {"team", 50},
	{"support", 35}, {"service", 35},
}

// freeMail hosts are not the site's own, but publishers do list them as
// their only address; they count, below the site's own.
var freeMail = map[string]bool{"gmail.com": true, "googlemail.com": true, "outlook.com": true, "hotmail.com": true,
	"live.com": true, "yahoo.com": true, "aol.com": true, "icloud.com": true, "proton.me": true, "protonmail.com": true,
	"gmx.de": true, "gmx.net": true, "web.de": true, "t-online.de": true, "mail.ru": true, "yandex.ru": true, "zoho.com": true}

// emailScore is how good an address is to pitch, and whether it counts:
// the site's own domain (or a free-mail address) only, never a mailbox on
// the never list.
func emailScore(email, domain string) (int, bool) {
	at := strings.LastIndexByte(email, '@')
	if at < 1 || len(email) > 254 {
		return 0, false
	}
	local, host := email[:at], strings.TrimPrefix(email[at+1:], "www.")
	if neverPitch.MatchString(local) {
		return 0, false
	}
	score := 40 // a named person, most likely
	for _, r := range rolePrefix {
		if local == r.prefix || strings.HasPrefix(local, r.prefix+".") || strings.HasPrefix(local, r.prefix+"-") || strings.HasPrefix(local, r.prefix+"_") {
			score = r.score
			break
		}
	}
	switch {
	case sameSite(host, domain):
	case freeMail[host]:
		score -= 20
	default:
		return 0, false
	}
	return score, true
}
