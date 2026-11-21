package linkorders

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// DonorOutreach is the latest Outreach enrollment of someone at a catalog
// site: a brand lead whose address is at the site's domain, or whose
// company is the site (Find email files a free-mail address that way), in
// one of the brand's sequences that is not archived. Stopped enrollments
// don't count: stopping someone takes the site out of outreach. Recommended
// donors leave these sites out, so a site already being written to isn't
// suggested again. It is computed on read; nothing is stored for it.
type DonorOutreach struct {
	SequenceID   uuid.UUID `json:"sequence_id"`
	SequenceName string    `json:"sequence_name"`
	// SequenceStatus is the sequence's own status (draft, active, paused).
	SequenceStatus string `json:"sequence_status"`
	// Status is the enrollment's: active, completed, replied, bounced,
	// unsubscribed, blocked or failed.
	Status     string    `json:"status"`
	Email      string    `json:"email"`
	EnrolledAt time.Time `json:"enrolled_at"`
}

// donorOutreachSQL finds, per catalog row of the brand ($1 workspace, $2
// brand, $3 row ids), its latest enrollment. Leads match on their stored
// email domain (indexed) or a subdomain of the site, or on their company.
const donorOutreachSQL = `
	WITH dd AS MATERIALIZED (
		SELECT p.id AS donor_id, lower(pub.domain) AS domain
		FROM coverage_donor_profiles p
		JOIN coverage_publishers pub ON pub.id = p.publisher_id
		WHERE p.workspace_id = $1 AND p.brand_id = $2 AND p.id = ANY($3)
	), matched AS (
		SELECT dd.donor_id, l.id AS lead_id, l.email FROM dd
		JOIN leads l ON l.workspace_id = $1 AND l.domain = dd.domain
		UNION
		SELECT dd.donor_id, l.id, l.email FROM dd
		JOIN leads l ON l.workspace_id = $1 AND substr(l.domain, strpos(l.domain, '.') + 1) = dd.domain
		UNION
		SELECT dd.donor_id, l.id, l.email FROM dd
		JOIN leads l ON l.workspace_id = $1 AND lower(l.company) = dd.domain
	)
	SELECT DISTINCT ON (m.donor_id) m.donor_id, s.id, s.name, s.status, e.status, m.email, e.created_at
	FROM matched m
	JOIN enrollments e ON e.lead_id = m.lead_id AND e.workspace_id = $1 AND e.status <> 'stopped'
	JOIN sequences s ON s.id = e.sequence_id AND s.workspace_id = $1 AND s.brand_id = $2 AND s.status <> 'archived'
	ORDER BY m.donor_id, e.created_at DESC`

// fillOutreach sets Outreach on each row with one query.
func fillOutreach(ctx context.Context, q Querier, workspaceID, brandID uuid.UUID, list []Donor) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(list))
	at := make(map[uuid.UUID]int, len(list))
	for i, d := range list {
		ids[i] = d.ID
		at[d.ID] = i
	}
	rows, err := q.Query(ctx, donorOutreachSQL, workspaceID, brandID, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var o DonorOutreach
		if err := rows.Scan(&id, &o.SequenceID, &o.SequenceName, &o.SequenceStatus, &o.Status, &o.Email, &o.EnrolledAt); err != nil {
			return err
		}
		if i, ok := at[id]; ok {
			list[i].Outreach = &o
		}
	}
	return rows.Err()
}
