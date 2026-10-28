package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/track"
	"github.com/outflareai/outflare/engine/internal/visitors"
)

// Person reveal: a visitor identification tool on the workspace's own
// account (RB2B, Vector, Warmly) pushes the people it names on the
// workspace's website to a webhook. Each becomes a person record like
// anyone else, tagged with the tool (acquired_via) as a third-party
// prospect and held for review: hidden from the people list and blocked by
// the policy check until someone approves them. Nothing happens until the
// workspace turns it on and accepts the notice.

// visitorHookPurpose signs a connection's webhook address.
const visitorHookPurpose = "visitor_hook"

// visitorHookPath is where a connection's tool sends people.
func (s *Server) visitorHookURL(integrationID string) string {
	return s.publicURL + "/v1/visitor-hooks/" + integrationID + "/" + s.box.Sign(visitorHookPurpose, integrationID)
}

// canRevealPeople reports whether a connector names visitors.
func (s *Server) canRevealPeople(connector string) bool {
	d, ok := s.integrations.Registry.Definition(connector)
	return ok && d.Has(connectors.CapRevealPerson)
}

// peopleConnections lists the workspace's connections that name people,
// with the webhook address for each, for the settings.
func (s *Server) peopleConnections(ctx context.Context, ws string) ([]map[string]string, error) {
	out := []map[string]string{}
	if s.integrations == nil {
		return out, nil
	}
	list, err := s.integrations.List(ctx, s.pool, ws, "")
	if err != nil {
		return nil, err
	}
	for _, i := range list {
		if s.canRevealPeople(i.Connector) {
			out = append(out, map[string]string{"id": i.ID, "name": i.Name, "connector": i.Connector, "status": i.Status, "webhook_url": s.visitorHookURL(i.ID)})
		}
	}
	slices.SortStableFunc(out, func(a, b map[string]string) int { return strings.Compare(a["connector"], b["connector"]) })
	return out, nil
}

// visitorHook takes in the people a connected tool names.
func (s *Server) visitorHook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if s.integrations == nil || !validUUID(id) || !s.box.Verify(visitorHookPurpose, id, r.PathValue("token")) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such webhook")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "the body is limited to 1 MiB")
		return
	}
	var ws, connector string
	err = s.pool.QueryRow(ctx, `SELECT workspace_id::text, connector FROM integrations WHERE id = $1`, id).Scan(&ws, &connector)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such webhook")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	conn, ok := s.integrations.Registry.Get(connector)
	parser, can := conn.(connectors.PersonRevealer)
	if !ok || !can {
		writeError(w, r, http.StatusNotFound, "not_found", "no such webhook")
		return
	}
	people, err := parser.ParsePeople(body)
	if err != nil {
		track.Emit(ctx, track.Event{Name: "visitor.people_rejected", WorkspaceID: ws, ActorType: "system", Surface: "api", Outcome: track.Failure,
			ErrorCode: "invalid_request", Props: map[string]any{"connector": connector}})
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var res peopleResult
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		set, err := visitors.Load(ctx, tx, ws)
		if err != nil {
			return err
		}
		if !set.RevealPeople || set.PeopleNoticeAcceptedAt == nil {
			res.Ignored = "off"
			return nil
		}
		res, err = s.takePeople(ctx, tx, ws, connector, set, people)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if res.Ignored != "" {
		// The tool keeps sending until someone takes the address out; say
		// so in an event rather than an error it would retry.
		track.Emit(ctx, track.Event{Name: "visitor.people_ignored", WorkspaceID: ws, ActorType: "system", Surface: "api", Outcome: track.Failure,
			ErrorCode: "feature_off", Props: map[string]any{"connector": connector, "people": len(people)}})
	}
	res.Received = len(people)
	writeJSON(w, http.StatusOK, res)
}

type peopleResult struct {
	Received int `json:"received"`
	// New people, held for review.
	Added int `json:"added"`
	// People the CRM already had: left as they were.
	Known int `json:"known"`
	// People without an email address, or at a domain the workspace
	// leaves out.
	Skipped int    `json:"skipped"`
	Ignored string `json:"ignored,omitempty"`
}

// takePeople adds the people a tool named that the CRM doesn't have yet, as
// prospects from the tool held for review, and records each sighting.
func (s *Server) takePeople(ctx context.Context, tx pgx.Tx, ws, connector string, set visitors.Settings, people []connectors.RevealedPerson) (peopleResult, error) {
	var res peopleResult
	var list []customers.Person
	seen := map[string]connectors.RevealedPerson{}
	for _, p := range people {
		_, domain, _ := strings.Cut(p.Email, "@")
		if p.Email == "" || visitors.Matches(domain, set.ExcludeDomains) {
			res.Skipped++
			continue
		}
		if _, dup := seen[p.Email]; dup {
			continue
		}
		seen[p.Email] = p
		list = append(list, customers.Person{Email: p.Email, Name: p.Name, Attributes: map[string]any{
			"first_name": p.FirstName, "last_name": p.LastName, "job_title": p.Title, "linkedin_url": p.LinkedInURL}})
	}
	if len(list) == 0 {
		return res, nil
	}
	ids, err := customers.EnsurePeople(ctx, tx, ws, "prospect", connector, list)
	if err != nil {
		return res, err
	}
	all := make([]string, 0, len(ids))
	for _, id := range ids {
		all = append(all, id)
	}
	// Records made in this transaction are the new ones: hold them.
	rows, err := tx.Query(ctx, `UPDATE records SET needs_review = true
		WHERE workspace_id = $1 AND id = ANY($2) AND created_at = now() AND origin = 'prospect' RETURNING id::text`, ws, all)
	if err != nil {
		return res, err
	}
	made, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return res, err
	}
	res.Added, res.Known = len(made), len(ids)-len(made)
	for email, id := range ids {
		p := seen[email]
		data := map[string]any{"record_id": id, "connector": connector, "new": slices.Contains(made, id), "page": pagePath(p.Page)}
		if !p.SeenAt.IsZero() {
			data["seen_at"] = p.SeenAt
		}
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "visitor.person_revealed",
			TargetType: "record", TargetID: id, RequestID: RequestID(ctx), Data: data,
			Props: map[string]any{"connector": connector, "new": data["new"]}}); err != nil {
			return res, err
		}
	}
	return res, nil
}

// pagePath is a page's path, without its address's query or fragment.
func pagePath(page string) string {
	u, err := url.Parse(page)
	if err != nil || page == "" {
		return ""
	}
	return cutUTF8(u.EscapedPath(), 300)
}

// reviewRecord approves a record held for review, so it is listed and may
// be emailed, or rejects it, which archives it.
func (s *Server) reviewRecord(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var in struct {
		Decision string `json:"decision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Decision != "approve" && in.Decision != "reject" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "decision is approve or reject")
		return
	}
	id := r.PathValue("record")
	var view recordView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		v, err := lockRecord(ctx, tx, a, id)
		if err == nil && (!a.writes(nil, v.BrandID) || v.MergedInto != nil) {
			err = errNotFound
		}
		if err != nil {
			return err
		}
		if !v.NeedsReview {
			return recordConflict{"not_held", "this record isn't waiting for review"}
		}
		var via string
		if err := tx.QueryRow(ctx, `SELECT acquired_via FROM records WHERE id = $1`, id).Scan(&via); err != nil {
			return err
		}
		action := "record.review_approved"
		if in.Decision == "approve" {
			_, err = tx.Exec(ctx, `UPDATE records SET needs_review = false, version = version + 1, updated_at = now() WHERE id = $1`, id)
		} else {
			action = "record.review_rejected"
			_, err = tx.Exec(ctx, `UPDATE records SET needs_review = false, archived_at = now(), version = version + 1, updated_at = now() WHERE id = $1`, id)
		}
		if err != nil {
			return err
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, action, "record", id, map[string]any{"record_id": id, "acquired_via": via}); err != nil {
			return err
		}
		view, err = loadRecord(ctx, tx, a, id)
		return err
	})
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "no such record")
	case s.recordError(w, r, err):
	default:
		writeJSON(w, http.StatusOK, view)
	}
}
