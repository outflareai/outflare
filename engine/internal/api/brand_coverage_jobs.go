package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/usagelimits"
)

const jobCoverageCheck = "coverage.check"

// QueueDueCoverage only observes and creates maintenance work. It never
// chooses an outreach action, sends a message or purchases a placement.
func (s *Server) QueueDueCoverage(ctx context.Context) (int, error) {
	if err := s.queueCoverageCommercialDeadlines(ctx); err != nil {
		return 0, err
	}
	if err := s.queueLinkOrderChecks(ctx); err != nil {
		return 0, err
	}
	if err := s.queueLinkOrderReminders(ctx); err != nil {
		return 0, err
	}
	if err := s.queueCoverageDeadlines(ctx); err != nil {
		return 0, err
	}
	if err := s.queueCoverageAccuracy(ctx); err != nil {
		return 0, err
	}
	if err := s.queueCoverageDiscovery(ctx); err != nil {
		return 0, err
	}
	if err := s.queueCompetitorScans(ctx); err != nil {
		return 0, err
	}
	if err := s.queuePartnershipSyncs(ctx); err != nil {
		return 0, err
	}
	n := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `UPDATE coverage_placements SET next_check_at=now()+make_interval(hours=>least(8760,check_every_hours*(1+least(failure_count,6))))
 WHERE id IN (SELECT id FROM coverage_placements WHERE monitor AND url<>'' AND stage NOT IN ('archived','cancelled','declined') AND next_check_at<=now() ORDER BY next_check_at LIMIT 50 FOR UPDATE SKIP LOCKED)
 RETURNING workspace_id::text,brand_id::text,id::text,next_check_at`)
		if err != nil {
			return err
		}
		type due struct {
			ws, brand, id string
			at            time.Time
		}
		list := []due{}
		for rows.Next() {
			var d due
			if err = rows.Scan(&d.ws, &d.brand, &d.id, &d.at); err != nil {
				rows.Close()
				return err
			}
			list = append(list, d)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		for _, d := range list {
			_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobCoverageCheck, WorkspaceID: &d.ws, Payload: map[string]string{"placement_id": d.id, "brand_id": d.brand, "scheduled": "true"}, IdempotencyKey: d.id + ":" + d.at.Format(time.RFC3339Nano), MaxAttempts: 3})
			if err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}
func (s *Server) coverageCheckJob(ctx context.Context, j jobs.Job) error {
	var req struct {
		Reader    string `json:"reader"`
		Scheduled string `json:"scheduled"`
		ID        string `json:"placement_id"`
		Brand     string `json:"brand_id"`
	}
	if err := json.Unmarshal(j.Payload, &req); err != nil {
		return err
	}
	if j.WorkspaceID == nil || !validUUID(req.ID) || !validUUID(req.Brand) {
		return fmt.Errorf("%w: invalid coverage job", jobs.ErrPermanent)
	}
	ws := *j.WorkspaceID
	if done, err := s.coverageJobObserved(ctx, ws, req.Brand, req.ID, "page_read", j.ID); err != nil {
		return err
	} else if done {
		return nil
	}
	var page, target, kind, mention, expectedRel, configuredReader string
	var expect, test, monitor bool
	var stage, brandName, website string
	var domains []string
	var version int
	err := s.pool.QueryRow(ctx, `SELECT p.url,p.target_url,p.kind,p.expected_link,p.expected_mention,p.version,w.test,p.expected_rel,p.reader,p.monitor,p.stage,b.name,b.website,b.domains FROM coverage_placements p JOIN workspaces w ON w.id=p.workspace_id JOIN brands b ON b.id=p.brand_id AND b.workspace_id=p.workspace_id WHERE p.workspace_id=$1 AND p.brand_id=$2 AND p.id=$3`, ws, req.Brand, req.ID).Scan(&page, &target, &kind, &expect, &mention, &version, &test, &expectedRel, &configuredReader, &monitor, &stage, &brandName, &website, &domains)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if page == "" || (req.Scheduled == "true" && (!monitor || stage == "archived" || stage == "cancelled" || stage == "declined")) {
		return nil
	}
	if req.Reader == "" {
		req.Reader = configuredReader
	}
	health := "unreachable"
	evidence := map[string]any{"url": page, "links": []crawl.Link{}, "job_id": j.ID, "link_present": false, "mention_present": nil}
	// A public-page read is bounded, robots-aware and protected at DNS/dial time.
	// The native reader does not use any provider or ambient credential.
	var rd *crawl.Read
	if err = crawl.CheckURL(page, false); err == nil {
		u, _ := url.Parse(page)
		var opted bool
		opted, err = crawl.OptedOut(ctx, s.pool, u.Hostname())
		if opted {
			health = "blocked"
			err = crawl.ErrBlocked
		}
		if err == nil {
			f, wait := s.crawler.ReadFetcher(test)
			if req.Reader == "connected" && !test {
				f, err = s.coverageReader(ctx, ws, req.Brand, req.ID, f)
			}
			if err == nil {
				rd, err = crawl.ReadPage(ctx, f, wait, page)
			}
			if err != nil && req.Reader == "connected" {
				code := "connector_" + connectors.KindOf(err)
				if errors.Is(err, connectors.ErrNoCapable) {
					code = "connector_needed"
				}
				var limit *usagelimits.Error
				if errors.As(err, &limit) {
					code = "usage_limit_reached"
				}
				evidence["error_code"] = code
			}
			evidence["reader"] = req.Reader
		}
	}
	if errors.Is(err, crawl.ErrBlocked) {
		health = "blocked"
	}
	if rd != nil {
		evidence["http_status"] = rd.Status
		evidence["final_url"] = rd.FinalURL
		evidence["redirect"] = rd.Redirect
		evidence["title"] = rd.Title
		evidence["text"] = rd.Text
		switch {
		case rd.Redirect != "":
			health = "redirected"
		case rd.Status == 404 || rd.Status == 410:
			health = "gone"
		case rd.Status == 401 || rd.Status == 403 || rd.Status == 429:
			health = "blocked"
		case rd.Status >= 200 && rd.Status < 300:
			health = "up"
		}
		links := []crawl.Link{}
		// Links to the target page's site; with no target, links to any of
		// the brand's own domains, so "did they link to us?" is always asked.
		hosts := coverageBrandHosts(target, website, domains)
		for _, link := range rd.Links {
			u, _ := url.Parse(link.URL)
			if u != nil && coverageHostIn(u.Hostname(), hosts) {
				links = append(links, link)
			}
		}
		evidence["links"] = links
		evidence["link_present"] = len(links) > 0
		// The expected wording, or else the brand's name, so "is the brand
		// mentioned?" is always asked too.
		if mention == "" {
			mention = strings.TrimSpace(brandName)
			evidence["mention_expected"] = "brand_name"
		}
		if mention != "" && health == "up" {
			evidence["mention_present"] = strings.Contains(strings.ToLower(rd.Text), strings.ToLower(mention))
			evidence["text_sample_only"] = true
		}
	}
	if err != nil {
		code := "unavailable"
		if v, ok := evidence["error_code"].(string); ok {
			code = v
		}
		if health == "blocked" {
			code = "blocked"
		}
		track.Fail(ctx, s.log, track.Event{Name: "coverage.check_failed", WorkspaceID: ws, ActorType: "system", Surface: "job", Props: map[string]any{"brand_id": req.Brand}}, code, err)
	}
	metrics.CoverageCheck(health)
	// Two distinct missing observations are needed before reporting a loss.
	// Network errors and rotating inventory never count as negative evidence.
	rotating := kind == "display_ad" || kind == "newsletter" || kind == "sponsorship"
	a := &access{WorkspaceID: ws, Brand: req.Brand}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var current int
		var firstVerified *time.Time
		var arrangement string
		if err := tx.QueryRow(ctx, "SELECT version,first_verified_at,arrangement FROM coverage_placements WHERE workspace_id=$1 AND brand_id=$2 AND id=$3 FOR UPDATE", ws, req.Brand, req.ID).Scan(&current, &firstVerified, &arrangement); err != nil {
			return err
		}
		if version != current {
			return nil
		} // result belongs to old terms/URL
		var seen bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM coverage_observations WHERE workspace_id=$1 AND brand_id=$2 AND placement_id=$3 AND evidence->>'job_id'=$4)", ws, req.Brand, req.ID, fmt.Sprint(j.ID)).Scan(&seen); err != nil {
			return err
		}
		if seen {
			return nil
		}
		var previousHealth string
		var previousEvidence []byte
		err := tx.QueryRow(ctx, "SELECT health,evidence FROM coverage_observations WHERE workspace_id=$1 AND brand_id=$2 AND placement_id=$3 AND source='page_read' ORDER BY created_at DESC LIMIT 1", ws, req.Brand, req.ID).Scan(&previousHealth, &previousEvidence)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		previous := map[string]any{}
		_ = json.Unmarshal(previousEvidence, &previous)
		if previous["url"] != page {
			previousHealth = ""
			previous = map[string]any{}
		}
		observed := health
		if health == "gone" && (previousHealth != "gone" || rotating) {
			observed = "unchecked"
		}
		// Verification keeps its meaning: what the placement expects (its
		// target page, its wording). Finding the brand's name or a link to
		// another of its pages is kept as link and mention status instead.
		verifiedPresence := health == "up" && !rotating && ((target != "" && evidence["link_present"] == true) || (evidence["mention_expected"] == nil && evidence["mention_present"] == true))
		evidence["presence_verified"] = verifiedPresence
		evidence["effective_health"] = observed
		evidence["rel_matches"] = coverageRelMatches(evidence, expectedRel)
		if _, err = tx.Exec(ctx, "INSERT INTO coverage_observations(workspace_id,brand_id,placement_id,health,source,evidence) VALUES($1,$2,$3,$4,'page_read',$5)", ws, req.Brand, req.ID, health, evidence); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE coverage_placements SET health=$4,last_check_at=now(),first_verified_at=CASE WHEN $5 THEN coalesce(first_verified_at,now()) ELSE first_verified_at END,last_verified_at=CASE WHEN $5 THEN now() ELSE last_verified_at END,failure_count=CASE WHEN $4 IN ('up','gone') THEN 0 ELSE failure_count+1 END WHERE workspace_id=$1 AND brand_id=$2 AND id=$3`, ws, req.Brand, req.ID, observed, verifiedPresence); err != nil {
			return err
		}
		linkStatus, mentionStatus, err := coverageWatch(ctx, tx, ws, req.Brand, req.ID, observed, health, rotating, evidence, previous, previousHealth)
		if err != nil {
			return err
		}
		findings := map[string]bool{"mention_missing": mentionStatus == "removed", "page_gone": observed == "gone", "link_changed": !rotating && expect && expectedRel != "any" && health == "up" && previousHealth == "up" && evidence["link_present"] == true && previous["link_present"] == true && !coverageRelMatches(evidence, expectedRel) && !coverageRelMatches(previous, expectedRel), "link_missing": linkStatus == "removed" || (!rotating && expect && target != "" && health == "up" && previousHealth == "up" && evidence["link_present"] == false && previous["link_present"] == false)}
		labels := map[string]string{"link_changed": "Expected backlink attributes changed", "page_gone": "Page is no longer available", "link_missing": "Expected backlink was not found", "mention_missing": "The brand mention was removed"}
		for reason, active := range findings {
			if active {
				if enabled, e := coverageRuleEnabled(ctx, tx, ws, req.Brand, reason); e != nil {
					return e
				} else if !enabled {
					continue
				}
				var recovery string
				if err = tx.QueryRow(ctx, `SELECT coalesce((SELECT id::text FROM coverage_observations WHERE workspace_id=$1 AND brand_id=$2 AND placement_id=$3 AND source='page_read' AND health='up' AND ($4='page_gone' OR ($4='link_missing' AND evidence->>'link_present'='true') OR ($4='mention_missing' AND evidence->>'mention_present'='true') OR ($4='link_changed' AND evidence->>'link_present'='true' AND evidence->>'rel_matches'='true')) ORDER BY created_at DESC LIMIT 1),'none')`, ws, req.Brand, req.ID, reason).Scan(&recovery); err != nil {
					return err
				}
				detail := "Two checks agree. Open the placement observation history for evidence. Last recovery observation: " + recovery
				err = coverageRaiseTask(ctx, tx, a, req.ID, labels[reason], reason, detail)
			} else if health == "up" && (reason == "page_gone" || (reason == "link_missing" && evidence["link_present"] == true) || (reason == "mention_missing" && evidence["mention_present"] == true) || (reason == "link_changed" && evidence["link_present"] == true && coverageRelMatches(evidence, expectedRel))) {
				err = coverageResolveTask(ctx, tx, a, req.ID, reason, "Recovered on a successful page check")
			}
			if err != nil {
				return err
			}
		}
		if verifiedPresence && firstVerified == nil {
			for _, reason := range []string{"first_live", "relationship_followup"} {
				if reason == "relationship_followup" && arrangement != "earned" {
					continue
				}
				if enabled, e := coverageRuleEnabled(ctx, tx, ws, req.Brand, reason); e != nil {
					return e
				} else if !enabled {
					continue
				}
				title := "Verify the placement's delivery"
				detail := "First observed brand mention or backlink. Review the agreed delivery before accepting it."
				if reason == "relationship_followup" {
					title = "Review the publisher relationship"
					detail = "New earned coverage. Check the publisher's CRM conversations, relationship owner and contact preferences before thanking them. A link request is a separate action."
				}
				err = coverageRaiseTask(ctx, tx, a, req.ID, title, reason, detail)
				if err != nil {
					return err
				}
			}
			if err = coverageResolveTask(ctx, tx, a, req.ID, "publication_overdue", "Publication verified by a successful page check"); err != nil {
				return err
			}
		}
		if err = s.applyLinkOrderCheck(ctx, tx, a, req.ID, health, evidence); err != nil {
			return err
		}
		return coverageHistory(ctx, tx, a, "placements", req.ID, "checked", map[string]any{"health": health})
	})
}

// Date-based work remains useful even when a placement has no page yet.
// Dismissal/resolution is respected for the same promised date. Moving the
// deadline creates new evidence and permits another reminder.
func (s *Server) queueCoverageDeadlines(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `INSERT INTO coverage_tasks(workspace_id,brand_id,placement_id,publisher_id,title,reason,owner_id,due_on,evidence)
 SELECT p.workspace_id,p.brand_id,p.id,p.publisher_id,'Publication is overdue','publication_overdue',coalesce((SELECT owner_id FROM coverage_rules r WHERE r.workspace_id=p.workspace_id AND r.brand_id=p.brand_id AND r.kind='publication_overdue'),p.owner_id),p.expected_on,'Promised publication date: '||p.expected_on::text
 FROM coverage_placements p WHERE NOT EXISTS(SELECT 1 FROM coverage_rules r WHERE r.workspace_id=p.workspace_id AND r.brand_id=p.brand_id AND r.kind='publication_overdue' AND NOT r.enabled) AND p.stage IN ('agreed','awaiting_publication') AND p.expected_on<current_date AND p.first_verified_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM coverage_tasks t WHERE t.workspace_id=p.workspace_id AND t.brand_id=p.brand_id AND t.placement_id=p.id AND t.reason='publication_overdue' AND t.evidence='Promised publication date: '||p.expected_on::text)
 ORDER BY p.expected_on LIMIT 100
 ON CONFLICT(workspace_id,brand_id,placement_id,reason) WHERE reason<>'manual' AND status IN ('open','in_progress','snoozed') DO NOTHING
 RETURNING workspace_id::text,brand_id::text,id::text`)
		if err != nil {
			return err
		}
		type created struct{ ws, brand, id string }
		made := []created{}
		for rows.Next() {
			var c created
			if err = rows.Scan(&c.ws, &c.brand, &c.id); err != nil {
				rows.Close()
				return err
			}
			made = append(made, c)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		for _, c := range made {
			if err = coverageHistory(ctx, tx, &access{WorkspaceID: c.ws, Brand: c.brand}, "tasks", c.id, "created", map[string]any{"reason": "publication_overdue"}); err != nil {
				return err
			}
		}
		return nil
	})
}
