package api

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/inbox"
	"github.com/outflareai/outflare/engine/internal/journeys"
)

// MoveDeal is the engine's journeys.DealMover: a journey's move_deal step
// moves the person's open deal the same way a reply rule does, and with
// the same source, so it never overrides a stage a person set.
func (s *Server) MoveDeal(ctx context.Context, tx pgx.Tx, ws, recordID string, d journeys.DealMove, ref string) (string, string, error) {
	c, err := loadCatalog(ctx, tx, ws, false)
	if err != nil {
		return "", "", err
	}
	rule := inbox.DealRule{Type: d.Type, Field: d.Field, Stage: d.Stage, Closed: d.Closed}
	shape, err := checkDealRule(c, rule)
	if err != nil {
		// The catalog changed since the journey was published.
		return "failed", err.Error(), nil
	}
	a := &access{principal: principal{ActorType: "system"}, WorkspaceID: ws}
	res, err := moveReplyDeal(ctx, tx, c, shape, a, rule, recordID, "journey")
	switch {
	case err != nil:
		return "", "", err
	case res == nil:
		return "skipped", "their deal is already at or past " + d.Stage + ", a person set its stage, or there is nothing to point it at", nil
	case res.created:
		return "done", "opened a deal at " + d.Stage, nil
	}
	from := "no stage"
	if res.from != nil {
		from = *res.from
	}
	return "done", "moved their deal from " + from + " to " + d.Stage, nil
}

// checkJourneyActionRefs checks the references of the steps checkJourneyRefs
// doesn't: a transactional email's template, an owner step's members and
// a deal step's deal against the catalog.
func (s *Server) checkJourneyActionRefs(ctx context.Context, ws string, steps []journeys.Step, live bool) error {
	var c *catalog
	for _, st := range steps {
		switch st.Kind {
		case "transactional_email":
			var purpose, status string
			var published *int
			err := s.pool.QueryRow(ctx, `SELECT purpose, status, published_version FROM email_templates
				WHERE workspace_id = $1 AND (id::text = $2 OR handle = $2)`, ws, st.Template).Scan(&purpose, &status, &published)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				return errInvalid("step " + st.ID + ": template not found: " + truncate(st.Template, 63))
			case err != nil:
				return err
			case purpose != "transactional":
				return errInvalid("step " + st.ID + ": " + st.Template + " is a lifecycle template; send it with an email step")
			case status != "active":
				return errInvalid("step " + st.ID + ": the template " + st.Template + " is archived; restore it first")
			case live && published == nil:
				return errInvalid("step " + st.ID + ": the template " + st.Template + " has no published version yet; publish it first")
			}
		case "assign_owner":
			ids := st.Members
			if st.OwnerID != "" {
				ids = []string{st.OwnerID}
			}
			var n int
			if err := s.pool.QueryRow(ctx, `SELECT count(DISTINCT user_id) FROM memberships WHERE workspace_id = $1 AND user_id::text = ANY($2::text[])`,
				ws, ids).Scan(&n); err != nil {
				return err
			}
			if n != len(ids) {
				return errInvalid("step " + st.ID + ": every owner must be a member of this workspace")
			}
		case "move_deal":
			if c == nil {
				err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
					var err error
					c, err = loadCatalog(ctx, tx, ws, false)
					return err
				})
				if err != nil {
					return err
				}
			}
			if _, err := checkDealRule(c, inbox.DealRule{Type: st.Deal.Type, Field: st.Deal.Field, Stage: st.Deal.Stage, Closed: st.Deal.Closed}); err != nil {
				return errInvalid("step " + st.ID + ": " + err.Error())
			}
		}
	}
	return nil
}
