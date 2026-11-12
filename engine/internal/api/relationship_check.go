package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/crmmail"
)

// The relationship check: before anyone reaches out from a card (a
// prospect, a partnership, a company or a person), what the brand already
// has with that company. Is it already a customer, is it in another
// pipeline, are there deals, when did anyone last talk to them and did the
// conversation go stale, and who on the team has emailed them, for a warm
// introduction. It reads the company and its people. Personal email counts
// only where the person asking may see it, the same as the email tab.

// staleAfter is how long a two-way conversation can be quiet before it
// counts as stale.
const staleAfter = 60 * 24 * time.Hour

// relationshipMailScan caps the personal emails one check reads.
const relationshipMailScan = 500

type relCheckPipeline struct {
	ID    string  `json:"id"`
	Type  string  `json:"type"`
	Name  string  `json:"name"`
	Stage *string `json:"stage"`
}

type relCheckTeammate struct {
	UserID   string    `json:"user_id"`
	Name     string    `json:"name"`
	Messages int       `json:"messages"`
	LastAt   time.Time `json:"last_at"`
}

type relationshipCheck struct {
	RecordID  string  `json:"record_id"`
	CompanyID *string `json:"company_id"`
	People    int     `json:"people"`
	Customer  struct {
		Is             bool    `json:"is"`
		Status         *string `json:"status"`
		ActiveProducts int     `json:"active_products"`
		WonDeals       int     `json:"won_deals"`
	} `json:"customer"`
	Deals struct {
		Open int `json:"open"`
		Won  int `json:"won"`
		Lost int `json:"lost"`
	} `json:"deals"`
	Pipelines []relCheckPipeline `json:"pipelines"`
	Contact   struct {
		LastSentAt     *time.Time `json:"last_sent_at"`
		LastReceivedAt *time.Time `json:"last_received_at"`
		LastMeetingAt  *time.Time `json:"last_meeting_at"`
		LastEmailAt    *time.Time `json:"last_email_at"`
		LastAt         *time.Time `json:"last_at"`
		Stale          bool       `json:"stale"`
	} `json:"contact"`
	Team []relCheckTeammate `json:"team"`
	// Flags sum it up: already_customer, in_other_pipeline, open_deal,
	// warm_intro and stale_thread.
	Flags []string `json:"flags"`
}

func (s *Server) routesRelationship(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/records/{record}/relationship", s.onBrand(auth.ScopeCustomersRead, s.recordRelationship))
}

func (s *Server) recordRelationship(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	out := relationshipCheck{Pipelines: []relCheckPipeline{}, Team: []relCheckTeammate{}, Flags: []string{}}
	var subjects []string
	err := s.readTx(ctx, func(tx pgx.Tx) error {
		rec, err := loadRecord(ctx, tx, a, r.PathValue("record"))
		if err != nil {
			return err
		}
		// The card must be the brand's own.
		if rec.BrandID == nil || *rec.BrandID != a.Brand {
			return errNotFound
		}
		out.RecordID = rec.ID
		company, err := relationshipCompany(ctx, tx, a.WorkspaceID, rec)
		if err != nil {
			return err
		}
		subjects = []string{}
		if company != "" {
			out.CompanyID = &company
			subjects = append(subjects, company)
		}
		if rec.Type == "person" {
			subjects = append(subjects, rec.ID)
		}
		if company != "" {
			rows, err := tx.Query(ctx, `SELECT DISTINCT p.id::text FROM record_relationships l
				JOIN records p ON p.id = CASE WHEN l.from_id = $1 THEN l.to_id ELSE l.from_id END
				WHERE (l.from_id = $1 OR l.to_id = $1) AND l.valid_to IS NULL AND l.workspace_id = $2
					AND p.workspace_id = $2 AND p.record_type = 'person' AND p.deleted_at IS NULL LIMIT $3`, company, a.WorkspaceID, timelinePeople)
			if err != nil {
				return err
			}
			people, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
			for _, p := range people {
				if !slices.Contains(subjects, p) {
					subjects = append(subjects, p)
				}
			}
		}
		for _, id := range subjects {
			if id != company {
				out.People++
			}
		}
		if len(subjects) == 0 {
			return nil
		}
		// A paying customer of any product, from the lifecycle.
		if err := tx.QueryRow(ctx, `SELECT (array_agg(status ORDER BY active_products DESC, record_id))[1], COALESCE(max(active_products), 0)
			FROM lifecycle_overall WHERE workspace_id = $1 AND brand_id = $2 AND record_id = ANY($3)`,
			a.WorkspaceID, a.Brand, subjects).Scan(&out.Customer.Status, &out.Customer.ActiveProducts); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE d.state = 'open'), count(*) FILTER (WHERE d.state = 'won'), count(*) FILTER (WHERE d.state = 'lost')
			FROM deals d WHERE d.workspace_id = $1 AND (d.brand_id = $2 OR d.brand_id IS NULL) AND (d.record_id = ANY($3)
				OR EXISTS (SELECT 1 FROM record_relationships l WHERE l.from_id = d.id AND l.to_id = ANY($3) AND l.valid_to IS NULL))`,
			a.WorkspaceID, a.Brand, subjects).Scan(&out.Deals.Open, &out.Deals.Won, &out.Deals.Lost); err != nil {
			return err
		}
		out.Customer.WonDeals = out.Deals.Won
		out.Customer.Is = out.Customer.ActiveProducts > 0 || out.Deals.Won > 0
		// The company in the brand's other pipelines: records that point at
		// it with their company field, such as partnerships.
		if company != "" {
			rows, err := tx.Query(ctx, `SELECT r.id::text, r.record_type, r.name, r.attributes->>'stage' FROM records r
				WHERE r.workspace_id = $1 AND r.brand_id = $2 AND r.attributes->>'company' = $3 AND r.id <> $4
					AND r.record_type NOT IN ('person', 'account') AND r.deleted_at IS NULL AND r.merged_into IS NULL AND r.archived_at IS NULL
				ORDER BY r.updated_at DESC LIMIT 20`, a.WorkspaceID, a.Brand, company, rec.ID)
			if err != nil {
				return err
			}
			if out.Pipelines, err = pgx.CollectRows(rows, pgx.RowToStructByPos[relCheckPipeline]); err != nil {
				return err
			}
		}
		// Outreach sent, replies received and meetings.
		return tx.QueryRow(ctx, `SELECT
				(SELECT max(m.sent_at) FROM messages m WHERE m.workspace_id = $1 AND (m.brand_id = $2 OR m.brand_id IS NULL) AND m.record_id = ANY($3) AND m.sent_at IS NOT NULL),
				(SELECT max(n.received_at) FROM inbound_messages n WHERE n.workspace_id = $1 AND (n.brand_id = $2 OR n.brand_id IS NULL) AND n.record_id = ANY($3)),
				(SELECT max(g.starts_at) FROM meetings g WHERE g.workspace_id = $1 AND (g.brand_id = $2 OR g.brand_id IS NULL) AND g.record_id = ANY($3) AND g.starts_at <= now())`,
			a.WorkspaceID, a.Brand, subjects).Scan(&out.Contact.LastSentAt, &out.Contact.LastReceivedAt, &out.Contact.LastMeetingAt)
	})
	if s.recordError(w, r, err) {
		return
	}
	if a.UserID != "" && len(subjects) > 0 && s.box != nil {
		team, last, err := s.relationshipTeam(ctx, a, subjects)
		if s.allocError(w, r, err) {
			return
		}
		out.Team, out.Contact.LastEmailAt = team, last
	}
	for _, t := range []*time.Time{out.Contact.LastSentAt, out.Contact.LastReceivedAt, out.Contact.LastMeetingAt, out.Contact.LastEmailAt} {
		if t != nil && (out.Contact.LastAt == nil || t.After(*out.Contact.LastAt)) {
			out.Contact.LastAt = t
		}
	}
	// Two-way: they answered, met or emailed someone here.
	two := out.Contact.LastReceivedAt != nil || out.Contact.LastMeetingAt != nil || out.Contact.LastEmailAt != nil
	out.Contact.Stale = two && out.Contact.LastAt != nil && time.Since(*out.Contact.LastAt) > staleAfter
	if out.Customer.Is {
		out.Flags = append(out.Flags, "already_customer")
	}
	if len(out.Pipelines) > 0 {
		out.Flags = append(out.Flags, "in_other_pipeline")
	}
	if out.Deals.Open > 0 {
		out.Flags = append(out.Flags, "open_deal")
	}
	for _, t := range out.Team {
		if t.UserID != a.UserID {
			out.Flags = append(out.Flags, "warm_intro")
			break
		}
	}
	if out.Contact.Stale {
		out.Flags = append(out.Flags, "stale_thread")
	}
	writeJSON(w, http.StatusOK, out)
}

// relationshipCompany is the company a card is about: the company itself,
// a partnership's company field, or a person's company.
func relationshipCompany(ctx context.Context, tx pgx.Tx, ws string, rec recordView) (string, error) {
	switch rec.Type {
	case "account":
		return rec.ID, nil
	case "person":
		var id string
		err := tx.QueryRow(ctx, `SELECT c.id::text FROM record_relationships l
			JOIN records c ON c.id = CASE WHEN l.from_id = $1 THEN l.to_id ELSE l.from_id END
			WHERE (l.from_id = $1 OR l.to_id = $1) AND l.valid_to IS NULL AND l.workspace_id = $2
				AND c.record_type = 'account' AND c.deleted_at IS NULL ORDER BY l.valid_from LIMIT 1`, rec.ID, ws).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return id, err
	}
	company, _ := rec.Attributes["company"].(string)
	if !validUUID(company) {
		return "", nil
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM records WHERE id = $1 AND workspace_id = $2 AND record_type = 'account' AND deleted_at IS NULL)`, company, ws).Scan(&ok); err != nil || !ok {
		return "", err
	}
	return company, nil
}

// relationshipTeam is who in the brand has emailed the company's people
// from their own mailbox, counting only email the person asking may see:
// their own, shared with them, or shared with the team and found to be
// about work. Nothing about the emails themselves is returned.
func (s *Server) relationshipTeam(ctx context.Context, a *access, records []string) ([]relCheckTeammate, *time.Time, error) {
	var brandPolicy crmmail.Policy
	cfg, err := mailConfig(ctx, s.pool, "crm_mail_policy", a.WorkspaceID, a.Brand, "", "")
	if err != nil {
		return nil, nil, err
	}
	raw, _ := json.Marshal(cfg)
	if err := json.Unmarshal(raw, &brandPolicy); err != nil {
		return nil, nil, err
	}
	teamShared := brandPolicy.TeamSharingOn(time.Now())
	rows, err := s.pool.Query(ctx, `SELECT c.id::text, c.owner_id::text, c.record_id::text, c.content_sealed, c.occurred_at, c.privacy, COALESCE(u.name, '')
		FROM crm_mail_messages c JOIN memberships m ON m.workspace_id = c.workspace_id AND m.user_id = c.owner_id
		JOIN users u ON u.id = c.owner_id AND u.disabled_at IS NULL
		WHERE c.workspace_id = $1 AND c.brand_id = $2 AND c.record_id = ANY($3)
			AND (m.brand_ids IS NULL OR cardinality(m.brand_ids) = 0 OR $2::uuid = ANY(m.brand_ids))
		ORDER BY c.occurred_at DESC LIMIT $4`, a.WorkspaceID, a.Brand, records, relationshipMailScan)
	if err != nil {
		return nil, nil, err
	}
	type row struct {
		id, owner, rec string
		sealed         []byte
		at             time.Time
		privacy, name  string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var v row
		return v, r.Scan(&v.id, &v.owner, &v.rec, &v.sealed, &v.at, &v.privacy, &v.name)
	})
	if err != nil {
		return nil, nil, err
	}
	type key struct{ owner, rec string }
	shares, teamDefault := map[key]crmmail.Sharing{}, map[key]bool{}
	contexts := map[key]mailContext{}
	byOwner := map[string]*relCheckTeammate{}
	var order []string
	var last *time.Time
	for _, v := range list {
		k := key{v.owner, v.rec}
		share, ok := shares[k]
		if !ok {
			cfg, err := mailConfig(ctx, s.pool, "crm_mail_sharing", a.WorkspaceID, a.Brand, v.owner, v.rec)
			if err != nil {
				return nil, nil, err
			}
			raw, _ := json.Marshal(cfg)
			if err := json.Unmarshal(raw, &share); err != nil {
				return nil, nil, err
			}
			if chosen, _ := cfg["version"].(int); chosen == 0 && teamShared {
				share = crmmail.TeamShared()
				teamDefault[k] = true
			}
			shares[k] = share
		}
		if crmmail.Visibility(v.owner, a.UserID, share) == "private" {
			continue
		}
		if teamDefault[k] && v.owner != a.UserID && v.privacy != "work" {
			continue
		}
		// The filters run again on every read, as on the email tab.
		c, ok := contexts[k]
		if !ok {
			if c, err = s.crmMailContext(ctx, a, v.owner, v.rec); err != nil {
				return nil, nil, err
			}
			contexts[k] = c
		}
		plain, err := s.box.Open(v.sealed, mailAAD(v.id))
		if err != nil {
			return nil, nil, err
		}
		var m crmmail.Message
		if err := json.Unmarshal(plain, &m); err != nil {
			return nil, nil, err
		}
		if !c.eligible(m) {
			continue
		}
		t := byOwner[v.owner]
		if t == nil {
			t = &relCheckTeammate{UserID: v.owner, Name: v.name, LastAt: v.at}
			byOwner[v.owner] = t
			order = append(order, v.owner)
		}
		t.Messages++
		if last == nil {
			at := v.at
			last = &at
		}
	}
	team := []relCheckTeammate{}
	for _, id := range order {
		team = append(team, *byOwner[id])
	}
	return team, last, nil
}
