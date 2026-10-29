package api

// Pre-registration: when a test starts, what it set out to show and how
// is kept as it was (the hypothesis, control, goals, guardrails, smallest
// change, the plan, and the flag's versions, weights and rollout). The
// hypothesis can't change after that, and the results say what changed in
// the flag since, since changing a running test's split or content mixes
// two experiments into one.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/outflareai/outflare/engine/internal/abtests"
)

type abRegisteredVersion struct {
	Key    string `json:"key"`
	Weight int    `json:"weight"`
	// ContentHash fingerprints the version's payload, so a change to it
	// shows without keeping the content twice.
	ContentHash string `json:"content_hash"`
}

type abRegisteredPlan struct {
	// Weeks the test was likely to need if the change is MinEffect; 0 when
	// it couldn't be planned.
	Weeks              float64 `json:"weeks"`
	WeeklyPerVersion   float64 `json:"weekly_per_version"`
	VisitorsPerVersion int64   `json:"visitors_per_version"`
}

type abRegistration struct {
	Hypothesis string                `json:"hypothesis"`
	Control    string                `json:"control"`
	Goals      []abtests.Goal        `json:"goals"`
	Guardrails []abtests.Goal        `json:"guardrails"`
	MinEffect  float64               `json:"min_effect"`
	Rollout    int                   `json:"rollout"`
	Versions   []abRegisteredVersion `json:"versions"`
	Plan       *abRegisteredPlan     `json:"plan"`
	// VarianceReduction says the test adjusts for visitors' history (CUPED)
	// for its whole life; tests registered from then on do.
	VarianceReduction bool      `json:"variance_reduction"`
	RegisteredAt      time.Time `json:"registered_at"`
}

func contentHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:8])
}

// abRegister is t's registration as it starts, with f as it is now.
func abRegister(ctx context.Context, q abtests.Querier, ws string, t abTestView, f flagView, now time.Time) (abRegistration, error) {
	reg := abRegistration{Hypothesis: t.Hypothesis, Control: t.Control, Goals: t.Goals, Guardrails: t.Guardrails, MinEffect: t.MinEffect,
		Rollout: f.Rollout, VarianceReduction: true, RegisteredAt: now.UTC()}
	weights := make([]int, len(f.Variants))
	for i, v := range f.Variants {
		reg.Versions = append(reg.Versions, abRegisteredVersion{Key: v.Key, Weight: v.Weight, ContentHash: contentHash(v.Payload)})
		weights[i] = v.Weight
	}
	if len(t.Goals) > 0 {
		g := t.Goals[0]
		tr, err := abtests.ReadTraffic(ctx, q, ws, g, "", now.Add(-abtests.TrafficWindow), now)
		if err != nil {
			return reg, err
		}
		m := g.MetricOf()
		p := abtests.MakePlan(tr.Moments(m), m, float64(tr.Visitors)/4*float64(f.Rollout)/100, weights, t.MinEffect)
		reg.Plan = &abRegisteredPlan{Weeks: p.Weeks, WeeklyPerVersion: p.WeeklyPerVersion, VisitorsPerVersion: int64(p.Weeks * p.WeeklyPerVersion)}
	}
	return reg, nil
}

// abFlagChanges says, in sentences, how flag f differs from what the test
// registered: versions added or removed, weights, content, rollout, and a
// flag turned off while the test runs.
func abFlagChanges(reg *abRegistration, f flagView, running bool) []string {
	if reg == nil {
		return nil
	}
	var out []string
	if running && f.Status != "on" {
		out = append(out, "The flag is off, so no new visitors join.")
	}
	if f.Rollout != reg.Rollout {
		out = append(out, fmt.Sprintf("The rollout changed from %d%% to %d%%.", reg.Rollout, f.Rollout))
	}
	now := map[string]int{}
	for i, v := range f.Variants {
		now[v.Key] = i
	}
	for _, r := range reg.Versions {
		i, ok := now[r.Key]
		if !ok {
			out = append(out, fmt.Sprintf("Version %s was removed from the flag.", r.Key))
			continue
		}
		v := f.Variants[i]
		if v.Weight != r.Weight {
			out = append(out, fmt.Sprintf("Version %s's share changed from %d to %d.", r.Key, r.Weight, v.Weight))
		}
		if contentHash(v.Payload) != r.ContentHash {
			out = append(out, fmt.Sprintf("Version %s's content changed.", r.Key))
		}
		delete(now, r.Key)
	}
	for _, v := range f.Variants {
		if _, added := now[v.Key]; added {
			out = append(out, fmt.Sprintf("Version %s was added to the flag.", v.Key))
		}
	}
	return out
}

// registeredWeights are the weights visitors were split by when the test
// started, in the order of keys; a version added since counts at its
// weight now.
func registeredWeights(reg *abRegistration, keys []string, current []int) []int {
	if reg == nil {
		return current
	}
	by := map[string]int{}
	for _, v := range reg.Versions {
		by[v.Key] = v.Weight
	}
	out := make([]int, len(keys))
	for i, k := range keys {
		if w, ok := by[k]; ok {
			out[i] = w
		} else {
			out[i] = current[i]
		}
	}
	return out
}
