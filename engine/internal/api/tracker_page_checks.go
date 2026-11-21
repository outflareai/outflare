package api

// Page checks (plans/ad-tracker.md, B8). A lander or an offer is a page
// the brand pays to send clicks to; when it stops answering, every click
// is wasted until someone notices. A worker asks each active lander and
// offer that a link sends to for its page, over a client that reaches
// only public addresses: a lander about once an hour, an offer every six
// hours, and either again ten minutes after a failed check. Two failed
// checks in a row make it broken and raise the tracker.link_broken alert,
// once per run of failures.
//
// An offer is usually a network's own tracking link, where a request
// counts as a click, so offers are asked less often and each has a switch
// (check_enabled; the owner asked for offers to be checked too). Plain
// destinations, which are neither, are never asked.
//
// A page that turns automated requests away (401, 403, 429) is "refused",
// not broken: it can't be checked from here. And when every check of a
// pass fails before reaching a page, the fault is far more likely this
// server's network than every page at once, so the pass is thrown away
// and nobody is told.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/alerts"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/netguard"
	"github.com/outflareai/outflare/engine/internal/track"
)

// AlertTrackerLinkBroken is the alert for a page a tracker link sends to
// that stopped answering.
const AlertTrackerLinkBroken = "tracker.link_broken"

// A page's check status.
const (
	landerOK      = "ok"
	landerFailing = "failing" // one failed check
	landerBroken  = "broken"  // two or more in a row
	landerRefused = "refused" // the page turns automated requests away
)

const (
	// landerCheckBatch is how many pages of each kind one pass asks.
	landerCheckBatch = 30
	// landerCheckRetry is how soon a page is asked again after one failed
	// check, to tell a blip from a page that is gone.
	landerCheckRetry = 10 * time.Minute
	// landerBrokenAfter is how many failed checks in a row make it broken.
	landerBrokenAfter = 2
	// landerDiscardFrom is how many checks a pass needs before "every one
	// failed on the network" is read as this server's fault.
	landerDiscardFrom = 3
)

// A checkedPage is a kind of page the tracker sends clicks to and checks.
type checkedPage struct {
	// name labels the metrics and the alert's data: lander or offer.
	name string
	// table holds the pages, with the check columns; due picks more of
	// its rows out; used says a link sends to the row (alias p).
	table, due, used string
	// every is how often a page that answers (or is known broken) is
	// asked; overdue how long without a check counts as behind.
	every, overdue time.Duration
	// targetType is the alert's target; path the app page it opens;
	// created the audit action that made one, whose person gets the alert.
	targetType, path, created string
	// advice ends the alert's text.
	advice string
}

var checkedPages = []checkedPage{
	{name: "lander", table: "tracker_landers", due: "true",
		used:  `EXISTS (SELECT 1 FROM tracking_link_destinations d WHERE d.lander_id = p.id AND d.removed_at IS NULL)`,
		every: time.Hour, overdue: 6 * time.Hour, targetType: "tracker_lander", path: "tracker-landers", created: "tracker_lander.created",
		advice: "Fix the page, or point its links somewhere else."},
	// Offers: every six hours, since a network counts each request as a
	// click, and only while the offer's own switch is on.
	{name: "offer", table: "tracker_offers", due: "p.check_enabled",
		used:  `EXISTS (SELECT 1 FROM tracking_link_destinations d WHERE d.offer_id = p.id AND d.removed_at IS NULL)`,
		every: 6 * time.Hour, overdue: 24 * time.Hour, targetType: "tracker_offer", path: "tracker-offers", created: "tracker_offer.created",
		advice: "Check the offer with its network, or point its links somewhere else."},
}

// landerCheckClient asks landers for their page. Tests swap it for one
// that reaches their local server.
var landerCheckClient = netguard.Client(10 * time.Second)

func init() {
	alerts.Register(alerts.Kind{ID: AlertTrackerLinkBroken, Title: "Tracker page not answering",
		Description: "A lander or an offer your links send clicks to failed two checks in a row (not found, a server error, or unreachable), so those clicks are wasted. Once each time it starts. It goes to the person who added the page, else the owners and admins; several at once come as one email.",
		Scope:       auth.ScopeAnalyticsRead, Describe: describeLinkBroken})
}

// RunPageChecks checks due landers and offers every interval until ctx
// ends.
func (s *Server) RunPageChecks(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := s.CheckTrackerPages(ctx, time.Second); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.log, track.Event{Name: "tracker.page_checks_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

var landerToken = regexp.MustCompile(`\{[A-Za-z0-9_.:-]{1,60}\}`)

// landerCheckURL is the page to ask for: the lander's or offer's URL with
// its tokens left empty, since no click is behind the request.
func landerCheckURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(landerToken.ReplaceAllString(raw, "")))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", false
	}
	u.Fragment = ""
	return u.String(), true
}

// landerCheck is what asking for a page found: outcome ok,
// failed, refused or network (failed before any page answered), and the
// code that says why.
type landerCheck struct {
	outcome, code string
}

// checkLander asks for the page once.
func checkLander(ctx context.Context, raw string) landerCheck {
	target, ok := landerCheckURL(raw)
	if !ok {
		return landerCheck{"failed", "bad_url"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return landerCheck{"failed", "bad_url"}
	}
	req.Header.Set("User-Agent", crawl.UserAgent)
	req.Header.Set("Accept", "text/html,*/*;q=0.8")
	resp, err := landerCheckClient.Do(req)
	if err != nil {
		return landerCheck{"network", landerErrorCode(err)}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch c := resp.StatusCode; {
	case c >= 200 && c < 400:
		return landerCheck{"ok", ""}
	case c == http.StatusUnauthorized || c == http.StatusForbidden || c == http.StatusTooManyRequests:
		return landerCheck{"refused", "http_" + strconv.Itoa(c)}
	default:
		return landerCheck{"failed", "http_" + strconv.Itoa(c)}
	}
}

// landerErrorCode names why a request never got an answer.
func landerErrorCode(err error) string {
	var dns *net.DNSError
	var cert *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var host x509.HostnameError
	var invalid x509.CertificateInvalidError
	var netErr net.Error
	switch {
	case errors.As(err, &dns):
		return "dns"
	case errors.As(err, &cert), errors.As(err, &unknown), errors.As(err, &host), errors.As(err, &invalid):
		return "tls"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	case strings.Contains(err.Error(), "stopped after"):
		return "redirect_loop"
	}
	return "unreachable"
}

// CheckTrackerPages asks the landers and offers that are due, up to
// landerCheckBatch of each, waiting pause between them. It returns how
// many it asked. Only one worker runs it at a time.
func (s *Server) CheckTrackerPages(ctx context.Context, pause time.Duration) (int, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended('tracker_page_checks', 0))`).Scan(&locked); err != nil {
		return 0, err
	}
	if !locked {
		return 0, nil // another worker has it
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtextextended('tracker_page_checks', 0))`)

	now := time.Now()
	type due struct {
		kind               checkedPage
		id, ws, brand, url string
		res                landerCheck
	}
	var list []due
	for _, k := range checkedPages {
		// One failed check is asked again soon; everything else at the
		// kind's own pace, a known broken page included.
		rows, err := s.pool.Query(ctx, `SELECT p.id::text, p.workspace_id::text, p.brand_id::text, p.url FROM `+k.table+` p
			WHERE p.status = 'active' AND `+k.due+` AND `+k.used+` AND (p.checked_at IS NULL OR p.checked_at < $1
				OR (p.check_status = 'failing' AND p.checked_at < $2))
			ORDER BY p.checked_at NULLS FIRST, p.id LIMIT $3`, now.Add(-k.every), now.Add(-landerCheckRetry), landerCheckBatch)
		if err != nil {
			return 0, err
		}
		d := due{kind: k}
		if _, err := pgx.ForEachRow(rows, []any{&d.id, &d.ws, &d.brand, &d.url}, func() error {
			list = append(list, d)
			return nil
		}); err != nil {
			return 0, err
		}
	}
	network := 0
	for i := range list {
		if i > 0 && pause > 0 {
			select {
			case <-ctx.Done():
				return i, ctx.Err()
			case <-time.After(pause):
			}
		}
		list[i].res = checkLander(ctx, list[i].url)
		if ctx.Err() != nil {
			return i, ctx.Err()
		}
		if list[i].res.outcome == "network" {
			network++
		}
	}
	// Every check failed before reaching a page: this server's network,
	// far more likely than every page at once. Nothing is recorded, so
	// they are asked again next pass.
	if len(list) >= landerDiscardFrom && network == len(list) {
		for _, d := range list {
			metrics.TrackerPageCheck(d.kind.name, "discarded")
		}
		track.Fail(ctx, s.log, track.Event{Name: "tracker.page_checks_failed", ActorType: "system", Surface: "system",
			Props: map[string]any{"checks": len(list)}}, "unavailable", errors.New("every page check of the pass failed before reaching a page"))
		return len(list), nil
	}
	for _, d := range list {
		outcome := d.res.outcome
		if outcome == "network" {
			outcome = "failed"
		}
		metrics.TrackerPageCheck(d.kind.name, outcome)
		if err := s.savePageCheck(ctx, d.kind, d.id, d.ws, d.brand, outcome, d.res.code, now); err != nil {
			return len(list), err
		}
	}
	for _, k := range checkedPages {
		var broken, overdue int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE p.check_status = 'broken'),
				count(*) FILTER (WHERE p.checked_at IS NULL OR p.checked_at < $1)
			FROM `+k.table+` p WHERE p.status = 'active' AND `+k.due+` AND `+k.used, now.Add(-k.overdue)).Scan(&broken, &overdue); err != nil {
			return len(list), err
		}
		metrics.TrackerPagesChecked(k.name, broken, overdue)
	}
	return len(list), nil
}

// savePageCheck records one check and, in the same transaction, raises
// the alert when it is the one that makes the page broken.
func (s *Server) savePageCheck(ctx context.Context, k checkedPage, id, ws, brand, outcome, code string, now time.Time) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if outcome != "failed" {
			status := landerOK
			if outcome == "refused" {
				status = landerRefused
			}
			_, err := tx.Exec(ctx, `UPDATE `+k.table+` SET check_status = $3, check_code = $4, check_failures = 0, checked_at = $5, broken_since = NULL
				WHERE id = $1 AND workspace_id = $2`, id, ws, status, code, now)
			return err
		}
		var failures int
		var since time.Time
		err := tx.QueryRow(ctx, `UPDATE `+k.table+` SET check_failures = check_failures + 1, check_code = $3, checked_at = $4,
				broken_since = COALESCE(broken_since, $4),
				check_status = CASE WHEN check_failures + 1 >= $5 THEN 'broken' ELSE 'failing' END
			WHERE id = $1 AND workspace_id = $2 RETURNING check_failures, broken_since`, id, ws, code, now, landerBrokenAfter).Scan(&failures, &since)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // removed while it was asked
		}
		if err != nil || failures < landerBrokenAfter {
			return err
		}
		owner, err := pageOwner(ctx, tx, k, id, ws)
		if err != nil {
			return err
		}
		// Once per run of failures: the key holds when the run began.
		_, err = alerts.Raise(ctx, tx, alerts.Alert{WorkspaceID: ws, BrandID: &brand, Kind: AlertTrackerLinkBroken, OwnerID: owner,
			DedupeKey:  id + ":" + since.UTC().Format(time.RFC3339Nano),
			TargetType: k.targetType, TargetID: id, Data: map[string]any{"page": k.name, "code": code}})
		return err
	})
}

// pageOwner is the person who added a lander or an offer, read from the
// audit event that created it (a person, or the person who made the API
// token that did), so the alert goes to them rather than every owner and
// admin. Nil when it can't be told; then the owners and admins hear.
func pageOwner(ctx context.Context, q jobs.Querier, k checkedPage, id, ws string) (*string, error) {
	var owner string
	err := q.QueryRow(ctx, `SELECT u.id::text FROM `+k.table+` p
		JOIN audit_events e ON e.workspace_id = p.workspace_id AND e.action = $3 AND e.target_id = p.id::text
			AND e.created_at BETWEEN p.created_at - interval '1 minute' AND p.created_at + interval '1 minute'
		LEFT JOIN api_tokens t ON t.actor_id = e.actor_id
		JOIN users u ON u.actor_id = e.actor_id OR u.id = t.created_by
		WHERE p.id = $1 AND p.workspace_id = $2
		ORDER BY e.id LIMIT 1`, id, ws, k.created).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &owner, nil
}

// landerCheckWords says a check's code in plain words.
func landerCheckWords(code string) string {
	switch {
	case code == "http_404" || code == "http_410":
		return "the page isn't there any more (" + strings.TrimPrefix(code, "http_") + ")"
	case strings.HasPrefix(code, "http_5"):
		return "its server answers with an error (" + strings.TrimPrefix(code, "http_") + ")"
	case strings.HasPrefix(code, "http_"):
		return "it answers " + strings.TrimPrefix(code, "http_")
	case code == "dns":
		return "its domain can't be found"
	case code == "tls":
		return "its HTTPS certificate isn't valid"
	case code == "timeout":
		return "it takes over ten seconds to answer"
	case code == "redirect_loop":
		return "it redirects round and round"
	case code == "bad_url":
		return "its URL isn't a web address"
	}
	return "it can't be reached"
}

// describeLinkBroken says where the page stands now, read fresh: it may
// answer again since the alert.
func describeLinkBroken(ctx context.Context, q jobs.Querier, a alerts.Row) (alerts.View, error) {
	k := checkedPages[0]
	for _, x := range checkedPages {
		if x.targetType == a.TargetType {
			k = x
		}
	}
	var name, status, code string
	if err := q.QueryRow(ctx, `SELECT name, check_status, check_code FROM `+k.table+` WHERE id = $1 AND workspace_id = $2`,
		a.TargetID, a.WorkspaceID).Scan(&name, &status, &code); err != nil {
		return alerts.View{}, err
	}
	v := alerts.View{Path: k.path}
	if status != landerBroken && status != landerFailing {
		v.Title = name + " is answering again"
		v.Text = "The " + k.name + " failed two checks in a row earlier; its last check went through."
		if status == "" {
			v.Text = "The " + k.name + " failed two checks in a row earlier; it has changed since and hasn't been checked again yet."
		}
		return v, nil
	}
	v.Title = name + " isn't answering"
	v.Text = fmt.Sprintf("Clicks sent to this %s are wasted: %s. %s", k.name, landerCheckWords(code), k.advice)
	return v, nil
}
