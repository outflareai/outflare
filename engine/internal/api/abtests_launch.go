package api

// Before and after an A/B test: a checklist that says what would stop a
// draft from measuring anything useful, and shipping the version a person
// chose, which shows it to everyone and ends the test.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/abtests"
	"github.com/outflareai/outflare/engine/internal/flags"
)

// abCheck is one line of a test's checklist. status is ok, warn (it can
// start, but the result may disappoint) or fail (it can't start).
type abCheck struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Text   string `json:"text"`
}

// abTestChecklist checks a draft (or any test) before it starts: the flag,
// whether pages show its versions, whether visitors reach the goal, how
// long it will take, and guardrails.
func (s *Server) abTestChecklist(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	t, ok := s.oneABTest(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), abResultsTimeout)
	defer cancel()
	f, err := s.abFlag(ctx, nil, a.WorkspaceID, t.FlagID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	checks, err := s.abChecklist(ctx, a.WorkspaceID, t, f)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	ready := true
	for _, c := range checks {
		if c.Status == "fail" {
			ready = false
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"test_id": t.ID, "ready": ready, "checks": checks})
}

func (s *Server) abChecklist(ctx context.Context, ws string, t abTestView, f flagView) ([]abCheck, error) {
	var out []abCheck
	add := func(id, status, text string) { out = append(out, abCheck{id, status, text}) }

	switch {
	case f.Status == "archived":
		add("flag", "fail", "The flag is archived: test a live flag.")
	case len(f.Variants) < 2:
		add("flag", "fail", "The flag has one version: give it at least two, the control and one to try.")
	case !abtests.HasVariant(variantKeys(f), t.Control):
		add("flag", "fail", "The control isn't one of the flag's versions any more: pick it again.")
	case f.Status == "off":
		add("flag", "warn", "The flag is off: starting the test turns it on.")
	default:
		add("flag", "ok", fmt.Sprintf("The flag splits visitors between %d versions.", len(f.Variants)))
	}

	if t.Status == "draft" {
		var running int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM ab_tests WHERE workspace_id = $1 AND flag_id = $2 AND status = 'running' AND id <> $3`,
			ws, f.ID, t.ID).Scan(&running); err != nil {
			return nil, err
		}
		if running > 0 {
			add("one_at_a_time", "fail", "Another test of this flag is running: end it first, since a flag runs one test at a time.")
		}
	}

	// Pages show the flag's versions: seen in the last 7 days.
	var seen int64
	if err := s.pool.QueryRow(ctx, `SELECT count(DISTINCT anonymous_id) FROM customer_events
		WHERE workspace_id = $1 AND name = $2 AND properties->>'flag' = $3 AND occurred_at > now() - interval '7 days'`,
		ws, abtests.ExposureEvent, f.Key).Scan(&seen); err != nil {
		return nil, err
	}
	if seen == 0 {
		add("exposures", "warn", fmt.Sprintf(`No visitor was shown a version of %s in the last 7 days. Make sure pages read the flag (outflare.flag("%s"), or data-flags and its class), or visitors won't join.`, f.Key, f.Key))
	} else {
		add("exposures", "ok", fmt.Sprintf("Pages showed its versions to %d visitors in the last 7 days.", seen))
	}

	// How long visitors waited for their version, as the site script
	// measured it: a long wait means the original can show first.
	if c, ok, err := s.abDeliveryCheck(ctx, ws, f.Key); err != nil {
		return nil, err
	} else if ok {
		out = append(out, c)
	}

	if len(t.Goals) > 0 {
		g := t.Goals[0]
		now := time.Now()
		tr, err := abtests.ReadTraffic(ctx, s.pool, ws, g, "", now.Add(-abtests.TrafficWindow), now)
		if err != nil {
			return nil, err
		}
		weights := make([]int, len(f.Variants))
		for i, v := range f.Variants {
			weights[i] = v.Weight
		}
		m := g.MetricOf()
		plan := abtests.MakePlan(tr.Moments(m), m, float64(tr.Visitors)/4*float64(f.Rollout)/100, weights, t.MinEffect)
		switch {
		case tr.Converted == 0:
			add("goal", "warn", "No visitor reached the first goal in the last 28 days, so the test can't find a winner yet. Judge it on a goal people already reach.")
		default:
			add("goal", "ok", fmt.Sprintf("%d of %d recent visitors reached the first goal.", tr.Converted, tr.Visitors))
		}
		switch {
		case tr.Converted == 0:
		case plan.Weeks == 0 || plan.Weeks > 8:
			text := fmt.Sprintf("At this traffic, a change of +%.0f%% is unlikely to be safe to stop within 8 weeks.", t.MinEffect*100)
			if plan.Versions > 1 {
				text += fmt.Sprintf(" With %d versions against the control, each needs more visitors: fewer versions would finish sooner.", plan.Versions)
			}
			add("duration", "warn", text)
		default:
			add("duration", "ok", fmt.Sprintf("Likely safe to stop in about %.0f week(s) if the change is +%.0f%%.", max(1, plan.Weeks), t.MinEffect*100))
		}
	}

	if len(t.Guardrails) == 0 {
		add("guardrails", "warn", "No guardrails: add one for what a winner mustn't hurt, such as errors or refunds.")
	} else {
		add("guardrails", "ok", fmt.Sprintf("%d guardrail(s) watched.", len(t.Guardrails)))
	}
	return out, nil
}

// shipABTest shows the chosen version to everyone: the flag gives it all
// the traffic (weight 100, the others 0) at full rollout, and a running
// test ends with it as the decision. A person asks for it; nothing ships on
// its own.
func (s *Server) shipABTest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Variant string `json:"variant"`
	}
	if !decode(w, r, &in) {
		return
	}
	a := accessFrom(r.Context())
	var flagID string
	s.changeABTest(w, r, func(ctx context.Context, tx pgx.Tx, t *abTestView, f flagView) error {
		if t.Status == "draft" {
			return errABConflict("this test hasn't run: start it, or change the flag directly")
		}
		if !abtests.HasVariant(variantKeys(f), in.Variant) {
			return errInvalid("variant is one of the flag's versions: " + strings.Join(variantKeys(f), ", "))
		}
		if f.Status == "archived" {
			return errABConflict("the flag is archived")
		}
		if !a.writes(f.BrandID) {
			return errDenied(brandLimitedMessage)
		}
		// End a running test first, keeping its results and the choice.
		if t.Status == "running" {
			res, err := s.abResults(ctx, tx, a.WorkspaceID, *t, f, time.Now())
			if err != nil {
				return err
			}
			snap, _ := json.Marshal(res)
			if _, err := tx.Exec(ctx, `UPDATE ab_tests SET status = 'done', ended_at = now(), decision = $3, result = $4, updated_at = now() WHERE id = $1 AND workspace_id = $2`,
				t.ID, a.WorkspaceID, in.Variant, snap); err != nil {
				return err
			}
			t.Status = "done"
			data := abAuditData(*t)
			winner := abtests.Winner(res, t.Control)
			data["decided"], data["had_winner"], data["chose_winner"] = true, winner != "", winner == in.Variant
			data["chose_control"] = in.Variant == t.Control
			data["sample_ratio_mismatch"] = res.SampleRatio
			var visitors int64
			for _, arm := range res.Arms {
				visitors += arm.Visitors
			}
			data["visitors"] = visitors
			if err := recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "ab_test.ended", "ab_test", t.ID, data, data); err != nil {
				return err
			}
			if err := s.queueABLearning(ctx, tx, &a.principal, a.WorkspaceID, a.ActorID, *t); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `UPDATE ab_tests SET decision = $3, updated_at = now() WHERE id = $1 AND workspace_id = $2`, t.ID, a.WorkspaceID, in.Variant); err != nil {
			return err
		}
		variants := make([]flags.Variant, len(f.Variants))
		for i, v := range f.Variants {
			v.Weight = 0
			if v.Key == in.Variant {
				v.Weight = 100
			}
			variants[i] = v
		}
		raw, _ := json.Marshal(variants)
		if _, err := tx.Exec(ctx, `UPDATE flags SET variants = $3, rollout = 100, status = 'on', updated_at = now() WHERE id = $1 AND workspace_id = $2`,
			f.ID, a.WorkspaceID, raw); err != nil {
			return err
		}
		fdata := flagAuditData(f)
		fdata["status"], fdata["rollout"], fdata["shipped_from_ab_test"] = "on", 100, t.ID
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "flag.updated", "flag", f.ID, fdata); err != nil {
			return err
		}
		flagID = f.ID
		data := abAuditData(*t)
		data["shipped_control"] = in.Variant == t.Control
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "ab_test.shipped", "ab_test", t.ID, data, data)
	})
	if flagID != "" {
		s.flagCache.forget(a.WorkspaceID)
	}
}

// abDeliveryMin is how many timed exposures the delivery line needs, and
// abDeliverySlowMs the wait (for one visitor in ten) above which visitors
// may see the original before their version.
const (
	abDeliveryMin    = 20
	abDeliverySlowMs = 400
)

// abDeliveryCheck reads the wait the site script reports with Experiment
// Viewed (wait_ms, and source: bootstrap, kept or fetched) over the last 7
// days. ok is false until enough visitors have been timed.
func (s *Server) abDeliveryCheck(ctx context.Context, ws, key string) (abCheck, bool, error) {
	var n, fetched int64
	var p90 *float64
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE properties->>'source' = 'fetched'),
			percentile_cont(0.9) WITHIN GROUP (ORDER BY (properties->>'wait_ms')::float)
		FROM customer_events
		WHERE workspace_id = $1 AND name = $2 AND properties->>'flag' = $3 AND occurred_at > now() - interval '7 days'
			AND properties->>'wait_ms' ~ '^[0-9]{1,6}$'`, ws, abtests.ExposureEvent, key).Scan(&n, &fetched, &p90); err != nil {
		return abCheck{}, false, err
	}
	if n < abDeliveryMin || p90 == nil {
		return abCheck{}, false, nil
	}
	if *p90 > abDeliverySlowMs {
		text := fmt.Sprintf("One visitor in ten waited over %s for their version, so some may see the original first.", waitText(*p90))
		if fetched*2 > n {
			text += " Decide the flags on your server and hand them to the page, so the version shows at once, or hide the page with of-wait until it arrives."
		}
		return abCheck{"delivery", "warn", text}, true, nil
	}
	return abCheck{"delivery", "ok", fmt.Sprintf("Nine visitors in ten saw their version within %s.", waitText(*p90))}, true, nil
}

func waitText(ms float64) string {
	if ms >= 1000 {
		return fmt.Sprintf("%.1f seconds", ms/1000)
	}
	return fmt.Sprintf("%.0f ms", ms)
}
