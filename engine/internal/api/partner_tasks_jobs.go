package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/sociallinks"
	"github.com/outflareai/outflare/engine/internal/track"
	"golang.org/x/net/html"
)

const jobPartnerTaskCheck = "partners.task_check"

func (s *Server) partnerTaskCheckJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		Action string `json:"action"`
		ID     string `json:"id"`
	}
	if json.Unmarshal(job.Payload, &p) != nil || job.WorkspaceID == nil || !validUUID(p.ID) || !strings.Contains("|verify|refresh|submission|release|", "|"+p.Action+"|") {
		return fmt.Errorf("%w: invalid task check", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	var err error
	if p.Action == "verify" || p.Action == "refresh" {
		err = s.checkPartnerAudience(ctx, ws, p.ID, p.Action)
	} else {
		err = s.checkPartnerSubmission(ctx, ws, p.ID, p.Action)
	}
	ev := track.Event{Name: "partner_task.checked", WorkspaceID: ws, ActorType: "system", Surface: "job", Props: map[string]any{"kind": p.Action}}
	if err != nil {
		track.Fail(ctx, s.log, ev, "partner_task_check_failed", err)
	} else {
		track.Emit(ctx, ev)
	}
	return err
}

func (s *Server) partnerTaskPage(ctx context.Context, ws, raw string) (connectors.FetchedPage, error) {
	// No platform credentials or direct-fetch fallback.
	ids, err := s.pageReaders(ctx, s.pool, ws)
	if err != nil {
		return connectors.FetchedPage{}, err
	}
	if len(ids) == 0 {
		return connectors.FetchedPage{}, errInvalid("connect your own Geonode account in Connections to check pages")
	}
	o, err := s.integrations.Open(ctx, s.pool, ws, ids[0])
	if err != nil {
		return connectors.FetchedPage{}, err
	}
	f, ok := o.Connector.(connectors.PageFetcher)
	if !ok {
		return connectors.FetchedPage{}, errors.New("connected page reader cannot fetch pages")
	}
	page, err := f.FetchPage(ctx, o.Client, o.Creds, raw, true)
	if err != nil {
		return page, err
	}
	if page.Status != 200 {
		return page, fmt.Errorf("page returned HTTP %d", page.Status)
	}
	// Cross-host redirects can reveal a different asset's reach or bypass evidence checks.
	_, want, err := partnerAudienceURL(raw)
	if err != nil {
		return page, err
	}
	_, got, err := partnerAudienceURL(page.URL)
	if err != nil || got != want {
		return page, errors.New("the page redirected to another hostname")
	}
	if len(page.HTML) > 4<<20 {
		return page, errors.New("page exceeds the 4 MB check limit")
	}
	return page, nil
}

func partnerDNSProof(records []string, challenge string) bool {
	for _, v := range records {
		if v == "outflare-partner="+challenge {
			return true
		}
	}
	return false
}

func (s *Server) checkPartnerAudience(ctx context.Context, ws, id, action string) error {
	var raw, host, challenge, kind string
	err := s.pool.QueryRow(ctx, `SELECT url,host,challenge,kind FROM partner_audiences WHERE workspace_id=$1 AND id=$2`, ws, id).Scan(&raw, &host, &challenge, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var reach int64
	if action == "verify" {
		if kind != "website" {
			return nil
		}
		var records []string
		var resolver interface {
			LookupTXT(context.Context, string) ([]string, error)
		} = net.DefaultResolver
		if s.crawler != nil && s.crawler.Resolver != nil {
			resolver = s.crawler.Resolver
		}
		records, err = resolver.LookupTXT(ctx, "_outflare-partner."+host)
		if err == nil && !partnerDNSProof(records, challenge) {
			err = errors.New("ownership record not found; add the exact TXT record and try again")
		}
	} else {
		var page connectors.FetchedPage
		page, err = s.partnerTaskPage(ctx, ws, raw)
		if err == nil {
			var ok bool
			reach, ok = partnerPageReach(page.HTML)
			if !ok {
				err = errors.New("no reliable follower or member count found; the previous reach was kept")
			}
		}
	}
	// Expected verification failures stay visible and can be rechecked after DNS propagates.
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err != nil {
			if _, e := tx.Exec(ctx, `UPDATE partner_audiences SET last_error=$3, verified_at=CASE WHEN $4 THEN NULL ELSE verified_at END WHERE workspace_id=$1 AND id=$2`, ws, id, truncate(err.Error(), 500), action == "verify"); e != nil {
				return e
			}
			track.Fail(ctx, s.log, track.Event{Name: "partner_audience.check_failed", WorkspaceID: ws, ActorType: "system", Surface: "job"}, "partner_audience_check_failed", err)
		} else if action == "verify" {
			if _, e := tx.Exec(ctx, `UPDATE partner_audiences SET verified_at=now(),last_error='' WHERE workspace_id=$1 AND id=$2`, ws, id); e != nil {
				return e
			}
		} else {
			if _, e := tx.Exec(ctx, `UPDATE partner_audiences SET reach=$3,reach_source='scraped',measured_at=now(),last_error='' WHERE workspace_id=$1 AND id=$2`, ws, id, reach); e != nil {
				return e
			}
		}
		return partnerRecord(ctx, tx, nil, ws, "partner_audience.checked", "partner_audience", id, map[string]any{"kind": action, "status": map[bool]string{true: "failed", false: "success"}[err != nil]})
	})
}

var partnerReachRE = regexp.MustCompile(`(?i)([0-9][0-9,.]*(?:\s*[kmb])?)\s+(?:followers|subscribers|members)\b`)

func partnerPageReach(page string) (int64, bool) {
	// Count visible text only, excluding scripts, styles and unrelated JSON counters.
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return 0, false
	}
	var text strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "noscript") {
			return
		}
		if n.Type == html.TextNode {
			text.WriteString(n.Data)
			text.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	matches := partnerReachRE.FindAllStringSubmatch(text.String(), -1)
	var count int64
	found := false
	for _, m := range matches {
		v, ok := sociallinks.ParseCount(m[1])
		if !ok {
			continue
		}
		if found && v != count {
			return 0, false
		}
		count, found = v, true
	}
	return count, found
}

func partnerHasLink(page, base, required string) bool {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return false
	}
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	want, err := url.Parse(required)
	if err != nil {
		return false
	}
	found := false
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "template") {
			return
		}
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, a := range n.Attr {
				if a.Key != "href" {
					continue
				}
				link, e := url.Parse(a.Val)
				if e != nil {
					continue
				}
				link = u.ResolveReference(link)
				if link.Scheme == want.Scheme && strings.EqualFold(link.Host, want.Host) && strings.TrimRight(link.Path, "/") == strings.TrimRight(want.Path, "/") && link.RawQuery == want.RawQuery {
					found = true
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return found
}

func (s *Server) checkPartnerSubmission(ctx context.Context, ws, id, action string) error {
	var cfg partnerTaskConfig
	var evidence, assetHost, status, memberStatus, programStatus string
	var verified *time.Time
	err := s.pool.QueryRow(ctx, `SELECT t.config,x.evidence_url,COALESCE(a.host,''),a.verified_at,x.status,m.status,p.status FROM partner_task_submissions x JOIN partner_tasks t ON t.id=x.task_id JOIN partner_memberships m ON m.id=x.membership_id JOIN partner_programs p ON p.id=x.program_id LEFT JOIN partner_audiences a ON a.id=x.audience_id AND a.membership_id=x.membership_id AND a.kind='website' WHERE x.workspace_id=$1 AND x.id=$2`, ws, id).Scan(&cfg, &evidence, &assetHost, &verified, &status, &memberStatus, &programStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status != "submitted" || cfg.Verification == "human" || memberStatus != "active" || programStatus != "active" {
		return nil
	}
	_, host, err := partnerAudienceURL(evidence)
	check := "passed"
	message := ""
	if err != nil || host != assetHost || verified == nil {
		check = "human_required"
		message = "automatic checks require evidence on your verified website"
	} else {
		page, e := s.partnerTaskPage(ctx, ws, evidence)
		if e != nil {
			track.Fail(ctx, s.log, track.Event{Name: "partner_task.check_failed", WorkspaceID: ws, ActorType: "system", Surface: "job", Props: map[string]any{"kind": "submission"}}, "partner_task_check_failed", e)
			check = "human_required"
			message = e.Error()
		} else if !partnerHasLink(page.HTML, evidence, cfg.RequiredLink) {
			check = "human_required"
			message = "the required link was not found; human review is needed"
		}
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var cur string
		var hold *time.Time
		if err := tx.QueryRow(ctx, `SELECT status,hold_until FROM partner_task_submissions WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, id).Scan(&cur, &hold); err != nil {
			return err
		}
		if cur != "submitted" {
			return nil
		}
		if check == "passed" && cfg.Verification == "auto_hold" && hold == nil {
			at := time.Now().Add(time.Duration(cfg.HoldDays) * 24 * time.Hour)
			hold = &at
			check = "held"
			if _, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobPartnerTaskCheck, WorkspaceID: &ws, Payload: map[string]string{"action": "release", "id": id}, RunAt: at, IdempotencyKey: "partner.task.release:" + id, MaxAttempts: 3}); err != nil {
				return err
			}
		} else if check == "passed" && (hold == nil || !hold.After(time.Now())) {
			if err := s.approveTaskSubmission(ctx, tx, nil, ws, id, cfg, "auto"); err != nil {
				return err
			}
		} else if check == "passed" {
			check = "held"
		}
		if _, err := tx.Exec(ctx, `UPDATE partner_task_submissions SET check_status=$3,hold_until=$4,note=CASE WHEN $5='' THEN note ELSE $5 END,updated_at=now() WHERE workspace_id=$1 AND id=$2`, ws, id, check, hold, truncate(message, 500)); err != nil {
			return err
		}
		return partnerRecord(ctx, tx, nil, ws, "partner_task_submission.checked", "partner_task_submission", id, map[string]any{"status": check})
	})
}
