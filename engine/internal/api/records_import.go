package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/outcomes"
	"github.com/outflareai/outflare/engine/internal/quota"
	"github.com/outflareai/outflare/engine/internal/smartlists"
)

// Importing people: a list someone has in hand (a CSV of sign-ups, an
// export of account ids) becomes people on customer records, so a broadcast
// can reach them through a saved list. Each row is matched first by the
// workspace's own user id, then by email; only a row with an email nobody
// in the brand holds makes a new person. Emails come in unverified: they
// claim no one's address, and a broadcast to the list still sends to them,
// because whoever imported them vouches for them.

const importMax = 10000

type peopleRow struct {
	Email  string `json:"email"`
	UserID string `json:"user_id"`
	Name   string `json:"name"`
	// What a new person's record starts with, such as from a business
	// card met at an event. A person already here keeps what they have.
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	JobTitle  string `json:"job_title"`
	// Company is the company's name: the brand's company of that name, else
	// the one of their email's domain, else a new one.
	Company string `json:"company"`
}

type peopleSkip struct {
	Row    int    `json:"row"`
	Reason string `json:"reason"`
}

type peopleImport struct {
	Created   int          `json:"created"`
	Matched   int          `json:"matched"`
	Skipped   []peopleSkip `json:"skipped"`
	RecordIDs []string     `json:"record_ids"`
	// The saved list they were put in, with how many it holds now.
	ListID      *string `json:"list_id"`
	ListMembers *int    `json:"list_members"`
}

// importOrigins are how a workspace can say it got the people it imports
// (customers.Basis): its own users or customers, a list it bought or found,
// or unknown.
var importOrigins = []string{"first_party", "prospect", "import"}

// personSources are the values of a person's Source (catalog option set
// person_source).
var personSources = []string{"cold", "inbound", "event", "signup", "invited", "customer", "other"}

func (s *Server) importRecords(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var in struct {
		People      []peopleRow `json:"people"`
		Origin      *string     `json:"origin"`
		AcquiredVia *string     `json:"acquired_via"`
		ListID      *string     `json:"list_id"`
		// Where these people are from: their Source (catalog option set
		// person_source), set on everyone created and on matched people who
		// have none yet. Cold makes them prospects when origin is left out.
		Source *string `json:"source"`
		// Which one within it: a sub-source's key (GET .../sub-sources),
		// such as the event they were met at. Without source, its own
		// Source is used.
		SubSource *string `json:"sub_source"`
	}
	if !decode(w, r, &in) {
		return
	}
	bad := func(msg string) { writeError(w, r, http.StatusBadRequest, "invalid_request", msg) }
	if len(in.People) == 0 || len(in.People) > importMax {
		bad("people is 1 to 10,000 rows, each with an email, a user_id or both")
		return
	}
	origin := "import"
	if in.Origin != nil {
		if !containsString(importOrigins, *in.Origin) {
			bad("origin is first_party (your own users or customers), prospect (a list you bought or found) or import (unknown)")
			return
		}
		origin = *in.Origin
	}
	source := ""
	if in.Source != nil && *in.Source != "" {
		source = *in.Source
		if !containsString(personSources, source) {
			bad("source is where these people are from: " + strings.Join(personSources, ", "))
			return
		}
		if in.Origin == nil {
			switch source {
			case "cold":
				origin = "prospect"
			case "signup", "customer", "invited":
				origin = "first_party"
			}
		}
	}
	subSource := ""
	if in.SubSource != nil && *in.SubSource != "" {
		subSource = *in.SubSource
	}
	via := "csv"
	if in.AcquiredVia != nil {
		via = strings.TrimSpace(*in.AcquiredVia)
		if !suppressionSource.MatchString(via) {
			bad("acquired_via is a short lowercase label such as csv or admin_export")
			return
		}
	}
	if in.ListID != nil && !validUUID(*in.ListID) {
		bad("list_id must be a saved list's id, from GET .../smart-lists")
		return
	}
	brand, err := brandWrite(ctx, s.pool, a, nil)
	if s.allocError(w, r, err) {
		return
	}
	if brand == nil {
		bad("import people on a brand's path: /v1/brands/{brand}/records/import")
		return
	}
	if subSource != "" {
		c, err := s.readCatalog(ctx, a.WorkspaceID, *brand, false)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		var of *catalogOption
		if set := c.optionSet(subSourceSet); set != nil {
			for i := range set.Options {
				if set.Options[i].Key == subSource && !set.Options[i].Archived {
					of = &set.Options[i]
				}
			}
		}
		switch {
		case of == nil:
			bad("sub_source is one of the brand's sub-sources' keys (GET .../sub-sources); add a new one with POST .../sub-sources")
			return
		case source != "" && of.Source != source:
			bad("sub_source " + subSource + " belongs to source " + of.Source + ", not " + source)
			return
		case source == "":
			source = of.Source
		}
	}
	if in.ListID != nil {
		v, ok := s.findSmartList(w, r, a, *in.ListID)
		if !ok {
			return
		}
		if v.Kind != smartlists.KindStatic {
			writeError(w, r, http.StatusConflict, "conflict", smartlists.ErrNotStatic.Error())
			return
		}
	}

	// Clean every row, and drop repeats of the same person.
	type row struct {
		n            int
		email, user  string
		name         string
		holder, made string
		attrs        map[string]string
		company      string
		emailMatch   bool
	}
	res := peopleImport{Skipped: []peopleSkip{}, RecordIDs: []string{}}
	rows := make([]row, 0, len(in.People))
	seen := map[string]bool{}
	for i, p := range in.People {
		x := row{n: i + 1, name: strings.TrimSpace(p.Name), user: strings.TrimSpace(p.UserID), attrs: map[string]string{},
			company: clip(cleanSubSourceLabel(p.Company), 200)}
		for k, v := range map[string]string{"first_name": p.FirstName, "last_name": p.LastName, "job_title": p.JobTitle} {
			if v = clip(strings.TrimSpace(v), 200); v != "" {
				x.attrs[k] = v
			}
		}
		if x.name == "" {
			x.name = strings.TrimSpace(x.attrs["first_name"] + " " + x.attrs["last_name"])
		}
		if len(x.name) > 200 {
			x.name = x.name[:200]
		}
		if len(x.user) > 500 {
			res.Skipped = append(res.Skipped, peopleSkip{x.n, "the user id is longer than 500 characters"})
			continue
		}
		if e := strings.TrimSpace(p.Email); e != "" {
			norm, err := normalizeEmail(e)
			if err != nil {
				res.Skipped = append(res.Skipped, peopleSkip{x.n, "not an email address"})
				continue
			}
			x.email = norm
		}
		if x.email == "" && x.user == "" {
			res.Skipped = append(res.Skipped, peopleSkip{x.n, "no email or user id"})
			continue
		}
		key := "u:" + x.user
		if x.user == "" {
			key = "e:" + x.email
		}
		if seen[key] {
			res.Skipped = append(res.Skipped, peopleSkip{x.n, "a repeat of an earlier row"})
			continue
		}
		seen[key] = true
		rows = append(rows, x)
	}

	// Who each row is already: the workspace's own user id first, then the
	// email.
	newPeople := 0
	for i := range rows {
		x := &rows[i]
		if x.user != "" {
			h, err := customers.HoldersIn(ctx, s.pool, a.WorkspaceID, *brand, "user_id", x.user)
			if err != nil {
				s.internal(w, r, err)
				return
			}
			if len(h) > 0 {
				x.holder = h[0]
				continue
			}
		}
		if x.email != "" {
			h, err := customers.HoldersIn(ctx, s.pool, a.WorkspaceID, *brand, "email", x.email)
			if err != nil {
				s.internal(w, r, err)
				return
			}
			if len(h) > 0 {
				x.holder = h[0]
				x.emailMatch = true
				continue
			}
			newPeople++
		}
	}
	if newPeople > 0 && !s.allowQuota(w, r, a.WorkspaceID, quota.Records, int64(newPeople)) {
		return
	}

	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		ids := make([]string, 0, len(rows))
		var made []string
		cat, err := loadCatalog(ctx, tx, a.WorkspaceID, *brand, false)
		if err != nil {
			return err
		}
		personType := cat.recordType("person")
		companies := map[string]string{}
		for i := range rows {
			x := &rows[i]
			switch {
			case x.holder != "":
				if x.emailMatch && x.user != "" {
					// Check under the record lock: an earlier row or another
					// import may already have attached a user id to this person.
					rec, err := lockRecord(ctx, tx, a, x.holder)
					if err != nil {
						return err
					}
					var hasUserID, differentUserID bool
					if err := tx.QueryRow(ctx, `SELECT count(*) > 0, COALESCE(bool_or(value <> $4), false)
						FROM record_identities WHERE workspace_id = $1 AND brand_id = $2 AND record_id = $3
						AND namespace = 'user_id' AND valid_to IS NULL`, a.WorkspaceID, *brand, rec.ID, x.user).
						Scan(&hasUserID, &differentUserID); err != nil {
						return err
					}
					if differentUserID {
						res.Skipped = append(res.Skipped, peopleSkip{x.n, "this email belongs to a person with another user id"})
						continue
					}
					if !hasUserID {
						ident := identityInput{Namespace: "user_id", Value: x.user, Source: "import"}
						if err := ident.normalize(); err != nil {
							return err
						}
						if _, _, err := putIdentity(ctx, tx, a, &rec, ident); err != nil {
							return err
						}
						if err := touch(ctx, tx, rec.ID); err != nil {
							return err
						}
					}
				}
				res.Matched++
				ids = append(ids, x.holder)
				continue
			case x.email == "":
				res.Skipped = append(res.Skipped, peopleSkip{x.n, "no person in this brand has this user id, and the row has no email"})
				continue
			}
			var id string
			attrs, srcs := map[string]any{}, map[string]string{}
			if source != "" {
				attrs["source"], srcs["source"] = source, "manual"
			}
			// What the row says about a new person: their name, job title,
			// company and sub-source, each in their history.
			more := map[string]json.RawMessage{}
			for k, v := range x.attrs {
				if personType != nil && personType.field(k) != nil {
					more[k], _ = json.Marshal(v)
				}
			}
			if subSource != "" {
				more["sub_source"], _ = json.Marshal(subSource)
			}
			if x.company != "" && personType != nil && personType.field("companies") != nil {
				key := strings.ToLower(x.company)
				co, ok := companies[key]
				if !ok {
					if co, err = importCompany(ctx, tx, a, *brand, origin, via, x.company, x.email); err != nil {
						return err
					}
					companies[key] = co
				}
				more["companies"], _ = json.Marshal([]string{co})
			}
			if err := tx.QueryRow(ctx, `INSERT INTO records (workspace_id, record_type, brand_id, name, origin, acquired_via, created_by, attributes, attribute_sources)
				VALUES ($1, 'person', $2, $3, $4, $5, $6, $7, $8) RETURNING id::text`,
				a.WorkspaceID, *brand, x.name, origin, via, nullIfEmpty(a.ActorID), attrs, srcs).Scan(&id); err != nil {
				return err
			}
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, "record.created", "record", id,
				map[string]any{"type": "person", "origin": origin, "via": "import"}); err != nil {
				return err
			}
			rec := recordView{ID: id, Type: "person", Attributes: map[string]any{}, AttributeSources: map[string]string{}}
			for k, v := range attrs {
				rec.Attributes[k], rec.AttributeSources[k] = v, srcs[k]
			}
			if len(more) > 0 && personType != nil {
				if _, err := applyAttributes(ctx, tx, cat, personType, a, &rec, more, "manual"); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE records SET attributes = $2, attribute_sources = $3, version = version + 1, updated_at = now() WHERE id = $1`,
					id, rec.Attributes, rec.AttributeSources); err != nil {
					return err
				}
			}
			idents := []identityInput{{Namespace: "email", Value: x.email, Source: "import"}}
			if x.user != "" {
				idents = append(idents, identityInput{Namespace: "user_id", Value: x.user, Source: "import"})
			}
			for _, ident := range idents {
				if err := ident.normalize(); err != nil {
					return err
				}
				if _, _, err := putIdentity(ctx, tx, a, &rec, ident); err != nil {
					return err
				}
			}
			res.Created++
			ids, made = append(ids, id), append(made, id)
		}
		res.RecordIDs = ids
		if source != "" && len(ids) > 0 {
			// The person importing said where these people are from: the
			// people it made have it already, matched people with no Source
			// yet get it now. Both go in each person's history.
			if _, err := tx.Exec(ctx, `WITH set AS (
					UPDATE records r SET attributes = r.attributes || jsonb_build_object('source', $3::text),
						attribute_sources = r.attribute_sources || '{"source": "manual"}'::jsonb, version = r.version + 1, updated_at = now()
					WHERE r.workspace_id = $1 AND r.id = ANY($2::uuid[]) AND r.record_type = 'person' AND NOT r.attributes ? 'source'
					RETURNING r.id, r.brand_id)
				INSERT INTO attribute_changes (workspace_id, brand_id, record_id, field_key, old_value, new_value, source, actor_id, request_id)
				SELECT $1, x.brand_id, x.id, 'source', NULL, to_jsonb($3::text), 'manual', $4, $5
				FROM (SELECT id, brand_id FROM set UNION ALL SELECT id, brand_id FROM records WHERE id = ANY($6::uuid[])) x`,
				a.WorkspaceID, ids, source, nullIfEmpty(a.ActorID), RequestID(ctx), made); err != nil {
				return err
			}
		}
		if subSource != "" && len(ids) > 0 {
			// Matched people with no sub-source yet whose Source is its
			// Source get it too, in their history.
			if _, err := tx.Exec(ctx, `WITH set AS (
					UPDATE records r SET attributes = r.attributes || jsonb_build_object('sub_source', $3::text),
						attribute_sources = r.attribute_sources || '{"sub_source": "manual"}'::jsonb, version = r.version + 1, updated_at = now()
					WHERE r.workspace_id = $1 AND r.id = ANY($2::uuid[]) AND r.record_type = 'person' AND NOT r.attributes ? 'sub_source'
					  AND r.attributes ->> 'source' = $6 AND NOT r.id = ANY($7::uuid[])
					RETURNING r.id, r.brand_id)
				INSERT INTO attribute_changes (workspace_id, brand_id, record_id, field_key, old_value, new_value, source, actor_id, request_id)
				SELECT $1, x.brand_id, x.id, 'sub_source', NULL, to_jsonb($3::text), 'manual', $4, $5 FROM set x`,
				a.WorkspaceID, ids, subSource, nullIfEmpty(a.ActorID), RequestID(ctx), source, made); err != nil {
				return err
			}
		}
		if in.ListID != nil && len(ids) > 0 {
			c, err := smartlists.AddStatic(ctx, tx, a.WorkspaceID, *in.ListID, ids, "import", "")
			if err != nil {
				return err
			}
			res.ListID, res.ListMembers = in.ListID, &c.Members
			if c.Changed > 0 {
				if err := record(ctx, tx, &a.principal, a.WorkspaceID, "smart_list.members_added", "smart_list", *in.ListID,
					map[string]any{"count": c.Changed, "members": c.Members, "via": "import"}); err != nil {
					return err
				}
			}
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "records.imported", "brand", *brand,
			map[string]any{"rows": len(in.People), "created": res.Created, "matched": res.Matched, "skipped": len(res.Skipped),
				"origin": origin, "to_list": in.ListID != nil, "source": source, "sub_source": subSource != ""})
	})
	var inv errInvalid
	switch {
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
	case errors.Is(err, smartlists.ErrGone):
		writeError(w, r, http.StatusNotFound, "not_found", "smart list not found")
	case s.recordError(w, r, err):
	default:
		writeJSON(w, http.StatusOK, res)
	}
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// importCompany is the company an imported person works at, by name: the
// brand's company of that name, else the one of their work email's domain
// (named so when new), else a new company of that name.
func importCompany(ctx context.Context, tx pgx.Tx, a *access, brand, origin, via, name, email string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM records WHERE workspace_id = $1 AND brand_id = $2 AND record_type = 'account'
		AND merged_into IS NULL AND deleted_at IS NULL AND lower(name) = lower($3) ORDER BY created_at LIMIT 1`, a.WorkspaceID, brand, name).Scan(&id)
	if err == nil || !errors.Is(err, pgx.ErrNoRows) {
		return id, err
	}
	if _, domain, ok := strings.Cut(email, "@"); ok && !outcomes.IsFreeMail(domain) {
		got, err := customers.EnsureCompanies(ctx, tx, a.WorkspaceID, &brand, via, []customers.Company{{Domain: domain, Name: name}})
		if err != nil {
			return "", err
		}
		if id := got[customers.CompanyDomain(domain)]; id != "" {
			return id, nil
		}
	}
	if err := tx.QueryRow(ctx, `INSERT INTO records (workspace_id, record_type, brand_id, name, origin, acquired_via, created_by)
		VALUES ($1, 'account', $2, $3, $4, $5, $6) RETURNING id::text`,
		a.WorkspaceID, brand, name, origin, via, nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
		return "", err
	}
	return id, record(ctx, tx, &a.principal, a.WorkspaceID, "record.created", "record", id,
		map[string]any{"type": "account", "origin": origin, "via": "import"})
}
