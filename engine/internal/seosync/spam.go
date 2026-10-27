package seosync

import (
	"context"
	"encoding/json"
	"math"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Link spam: every backlink gets a score from 0 (clean) to 100 (spam), with
// the factors behind it, so a person can see why and decide. The score is
// the highest of the providers' own spam judgements and Outflare's checks
// added up. Nothing is disavowed or removed automatically: a person (or an
// agent they ask) reviews a link and marks it ok or to disavow, and the
// disavow file lists only what people marked.

// Spam levels by score.
const (
	SpamSuspect = 30
	SpamToxic   = 60
)

// SpamFactor is one reason a link looks like spam.
type SpamFactor struct {
	Code   string `json:"code"`
	Points int    `json:"points"`
}

// spamTLDs are top-level domains spam networks use far more than anyone
// else (Spamhaus and others publish such lists); a link from one is only a
// hint.
var spamTLDs = map[string]bool{
	"xyz": true, "top": true, "click": true, "loan": true, "work": true, "gq": true, "tk": true, "ml": true, "cf": true,
	"ga": true, "icu": true, "buzz": true, "rest": true, "cyou": true, "monster": true, "bond": true, "sbs": true, "cfd": true,
}

// spamWords in an anchor are topics link spam pushes.
var spamWords = []string{"casino", "betting", "poker", "slots", "viagra", "cialis", "porn", "xxx", "escort", "payday", "replica", "essay writing"}

// SpamInput is what a link's score is worked out from.
type SpamInput struct {
	SourceDomain string
	Anchor       string
	SourceRating *float64
	Providers    map[string]providerView
	// SameDomain is how many of the site's links come from the same
	// linking domain.
	SameDomain int
}

// SpamScore scores one link and says why.
func SpamScore(in SpamInput) (int, []SpamFactor) {
	factors := []SpamFactor{}
	add := func(code string, points int) { factors = append(factors, SpamFactor{Code: code, Points: points}) }
	provider := 0.0
	var external float64
	for _, v := range in.Providers {
		if v.Spam != nil {
			provider = math.Max(provider, *v.Spam)
		}
		if v.ExternalLinks != nil {
			external = math.Max(external, *v.ExternalLinks)
		}
	}
	if provider > 0 {
		add("provider_spam", int(math.Round(provider)))
	}
	if in.SourceRating != nil {
		switch {
		case *in.SourceRating < 5:
			add("weak_domain", 25)
		case *in.SourceRating < 15:
			add("weak_domain", 10)
		}
	}
	d := strings.ToLower(in.SourceDomain)
	if i := strings.LastIndexByte(d, '.'); i >= 0 && spamTLDs[d[i+1:]] {
		add("spam_tld", 15)
	}
	anchor := strings.ToLower(in.Anchor)
	for _, w := range spamWords {
		if strings.Contains(anchor, w) {
			add("spam_anchor", 40)
			break
		}
	}
	switch {
	case external >= 500:
		add("link_farm", 35)
	case external >= 150:
		add("many_outlinks", 15)
	}
	if in.SameDomain >= 100 {
		add("sitewide", 10)
	}
	ours := 0
	for _, f := range factors {
		if f.Code != "provider_spam" {
			ours += f.Points
		}
	}
	return min(100, max(int(math.Round(provider)), ours)), factors
}

// SpamLevel names a score: clean, suspect or toxic.
func SpamLevel(score int) string {
	switch {
	case score >= SpamToxic:
		return "toxic"
	case score >= SpamSuspect:
		return "suspect"
	}
	return "clean"
}

// ScoreSiteLinks scores every link of a site again, after a read changed
// what the providers say.
func ScoreSiteLinks(ctx context.Context, tx pgx.Tx, ws, siteID string) error {
	rows, err := tx.Query(ctx, `
		SELECT id::text, source_domain, anchor, source_rating, providers, count(*) OVER (PARTITION BY source_domain)
		FROM backlinks WHERE workspace_id = $1 AND site_id = $2`, ws, siteID)
	if err != nil {
		return err
	}
	var ids []string
	var scores []int
	var facts [][]byte
	for rows.Next() {
		var id string
		var in SpamInput
		var raw []byte
		if err := rows.Scan(&id, &in.SourceDomain, &in.Anchor, &in.SourceRating, &raw, &in.SameDomain); err != nil {
			rows.Close()
			return err
		}
		_ = json.Unmarshal(raw, &in.Providers)
		score, f := SpamScore(in)
		b, _ := json.Marshal(f)
		ids, scores, facts = append(ids, id), append(scores, score), append(facts, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		UPDATE backlinks b SET spam_score = t.score, spam_factors = t.factors
		FROM unnest($3::uuid[], $4::int[], $5::jsonb[]) AS t(id, score, factors)
		WHERE b.id = t.id AND b.workspace_id = $1 AND b.site_id = $2
		  AND (b.spam_score IS DISTINCT FROM t.score OR b.spam_factors <> t.factors)`, ws, siteID, ids, scores, facts)
	return err
}
