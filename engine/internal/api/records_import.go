package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
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

func (s *Server) importRecords(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var in struct {
		People      []peopleRow `json:"people"`
		Origin      *string     `json:"origin"`
		AcquiredVia *string     `json:"acquired_via"`
		ListID      *string     `json:"list_id"`
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
	}
	res := peopleImport{Skipped: []peopleSkip{}, RecordIDs: []string{}}
	rows := make([]row, 0, len(in.People))
	seen := map[string]bool{}
	for i, p := range in.People {
		x := row{n: i + 1, name: strings.TrimSpace(p.Name), user: strings.TrimSpace(p.UserID)}
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
		for i := range rows {
			x := &rows[i]
			switch {
			case x.holder != "":
				res.Matched++
				ids = append(ids, x.holder)
				continue
			case x.email == "":
				res.Skipped = append(res.Skipped, peopleSkip{x.n, "no person in this brand has this user id, and the row has no email"})
				continue
			}
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO records (workspace_id, record_type, brand_id, name, origin, acquired_via, created_by)
				VALUES ($1, 'person', $2, $3, $4, $5, $6) RETURNING id::text`,
				a.WorkspaceID, *brand, x.name, origin, via, nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
				return err
			}
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, "record.created", "record", id,
				map[string]any{"type": "person", "origin": origin, "via": "import"}); err != nil {
				return err
			}
			rec := recordView{ID: id, Attributes: map[string]any{}, AttributeSources: map[string]string{}}
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
			ids = append(ids, id)
		}
		res.RecordIDs = ids
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
				"origin": origin, "to_list": in.ListID != nil})
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
