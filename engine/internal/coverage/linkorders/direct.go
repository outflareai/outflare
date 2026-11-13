package linkorders

import (
	"context"

	"github.com/google/uuid"

	"github.com/outflareai/outflare/engine/internal/leads"
)

// Why a catalog site is not a direct-contact recommendation. A site is
// recommended when none applies.
const (
	// DirectMissingPBN: the site is flagged as a PBN.
	DirectMissingPBN = "pbn"
	// DirectMissingMetrics: neither DR nor traffic is stored above zero.
	DirectMissingMetrics = "no_metrics"
	// DirectMissingEmail: no stored address at the site's own domain that
	// the brand may email.
	DirectMissingEmail = "no_email"
)

// DirectContact says whether a catalog site is worth writing to directly,
// not only through its intermediary. It is a recommendation computed on
// read from stored data; nothing is sent or enrolled because of it.
//
// The rules (first slice, no scores or cut-offs): not flagged as a PBN;
// DR or traffic stored and above zero (a missing value does not count);
// and at least one address at the site's own domain that Find email
// already has (people at its company, link-building check contacts, the
// brand's Outreach leads, the notes) and that is not suppressed for the
// brand. An intermediary's address in the notes is not a direct one.
type DirectContact struct {
	Recommended bool `json:"recommended"`
	// Emails is how many such addresses at the site are stored.
	Emails int `json:"emails"`
	// Missing lists the rules the site does not meet: pbn, no_metrics,
	// no_email. Empty when Recommended.
	Missing []string `json:"missing"`
}

// directMetricsSQL is the PBN and metrics part of the rules, on the
// catalog row aliased p.
const directMetricsSQL = `(NOT p.pbn AND (COALESCE(p.domain_rating, 0) > 0 OR COALESCE(p.traffic, 0) > 0))`

// emailInTextSQL matches an address in free text, like emailInText.
const emailInTextSQL = `[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}`

// directEmailsSQL counts, per catalog row of the brand ($1 workspace, $2
// brand) matching donorCond (on alias p), the stored addresses at the
// site's domain that are not suppressed for the brand. It reads the same
// stored places as FindDonorContacts in one pass for many rows, so a
// catalog page is one query. A stored address matches when its host is
// the site or one subdomain of it (mail.site.com), two hash joins over
// one scan; an address in the notes matches any subdomain.
func directEmailsSQL(donorCond string) string {
	return `
		WITH dd AS MATERIALIZED (
			SELECT p.id AS donor_id, lower(pub.domain) AS domain, p.notes || E'\n' || pub.notes AS notes
			FROM coverage_donor_profiles p
			JOIN coverage_publishers pub ON pub.id = p.publisher_id
			WHERE p.workspace_id = $1 AND p.brand_id = $2 AND ` + donorCond + `
		), stored AS (
			SELECT lower(i.value) AS email, i.record_id FROM record_identities i
			WHERE i.workspace_id = $1 AND i.namespace = 'email' AND i.valid_to IS NULL
			UNION ALL
			SELECT lower(l.email), NULL::uuid FROM leads l WHERE l.brand_id = $2
		), hosts AS MATERIALIZED (
			SELECT email, record_id, split_part(email, '@', 2) AS host FROM stored
		), matched AS (
			SELECT dd.donor_id, h.email, h.record_id FROM dd JOIN hosts h ON h.host = dd.domain
			UNION
			SELECT dd.donor_id, h.email, h.record_id FROM dd JOIN hosts h ON substr(h.host, strpos(h.host, '.') + 1) = dd.domain
			UNION
			SELECT dd.donor_id, lower(m[1]), NULL::uuid
			FROM dd CROSS JOIN LATERAL regexp_matches(dd.notes, '(` + emailInTextSQL + `)', 'gi') m
			WHERE split_part(lower(m[1]), '@', 2) = dd.domain OR split_part(lower(m[1]), '@', 2) LIKE '%.' || dd.domain
		)
		SELECT m.donor_id, count(DISTINCT m.email)::int
		FROM matched m
		WHERE (m.record_id IS NULL OR EXISTS (SELECT 1 FROM records r
				WHERE r.id = m.record_id AND r.workspace_id = $1 AND r.merged_into IS NULL AND (r.brand_id IS NULL OR r.brand_id = $2)))
			AND NOT EXISTS (` + leads.SuppressedInSQL("$1", "$2::uuid", "m.email") + `)
		GROUP BY m.donor_id`
}

// directContactCond is the list filter for direct-contact recommendations
// (want true) or the rest (want false), on the catalog row aliased p with
// $1 workspace and $2 brand.
func directContactCond(want bool) string {
	cond := directMetricsSQL + ` AND p.id IN (SELECT donor_id FROM (` + directEmailsSQL(directMetricsSQL) + `) de)`
	if want {
		return "(" + cond + ")"
	}
	return "NOT (" + cond + ")"
}

// fillDirectContact sets DirectContact on each row with one query.
func fillDirectContact(ctx context.Context, q Querier, workspaceID, brandID uuid.UUID, list []Donor) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(list))
	for i, d := range list {
		ids[i] = d.ID
	}
	emails := map[uuid.UUID]int{}
	rows, err := q.Query(ctx, directEmailsSQL("p.id = ANY($3)"), workspaceID, brandID, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id uuid.UUID
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			rows.Close()
			return err
		}
		emails[id] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range list {
		list[i].DirectContact = directContactFor(list[i], emails[list[i].ID])
	}
	return nil
}

func hasMetrics(d Donor) bool {
	return (d.DomainRating != nil && *d.DomainRating > 0) || (d.Traffic != nil && *d.Traffic > 0)
}

// directContactFor applies the rules to one row and its count of direct
// addresses.
func directContactFor(d Donor, emails int) DirectContact {
	out := DirectContact{Emails: emails, Missing: []string{}}
	if d.PBN {
		out.Missing = append(out.Missing, DirectMissingPBN)
	}
	if !hasMetrics(d) {
		out.Missing = append(out.Missing, DirectMissingMetrics)
	}
	if emails == 0 {
		out.Missing = append(out.Missing, DirectMissingEmail)
	}
	out.Recommended = len(out.Missing) == 0
	return out
}
