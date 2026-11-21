package api

import (
	"errors"
	"net/http"
	"net/mail"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/tracking"
)

// Internal and test people (docs/insights.md, "Leaving out your own team").
// A brand lists the email domains of its own team and the addresses of its
// test accounts; while the setting is on, its analytics reports count no
// event, visit or payment of a known person with such an address. Nothing
// is deleted or changed on anyone's record: turning it off counts them
// again. The reports read the list themselves (insights.leaveOutInternal).

func (s *Server) routesInternalPeople(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/internal-people", s.onBrand(auth.ScopeAnalyticsRead, s.getInternalPeople))
	mux.HandleFunc("PUT /v1/brands/{brand}/internal-people", s.onBrand(auth.ScopeAnalyticsManage, s.putInternalPeople))
}

type internalPeopleView struct {
	Enabled bool     `json:"enabled"`
	Domains []string `json:"domains"`
	Emails  []string `json:"emails"`
	// Matched is how many of the brand's people have such an address now.
	Matched   int        `json:"matched"`
	UpdatedAt *time.Time `json:"updated_at"`
}

const maxInternalEntries = 500

func (s *Server) internalPeople(r *http.Request, q rowQuerier) (internalPeopleView, error) {
	a := accessFrom(r.Context())
	v := internalPeopleView{Domains: []string{}, Emails: []string{}}
	err := q.QueryRow(r.Context(), `SELECT enabled, domains, emails, updated_at FROM internal_people WHERE workspace_id = $1 AND brand_id = $2`,
		a.WorkspaceID, a.Brand).Scan(&v.Enabled, &v.Domains, &v.Emails, &v.UpdatedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return v, err
	}
	if len(v.Domains)+len(v.Emails) == 0 {
		return v, nil
	}
	err = q.QueryRow(r.Context(), `SELECT count(DISTINCT ri.record_id) FROM record_identities ri
		WHERE ri.workspace_id = $1 AND ri.brand_id = $2 AND ri.namespace = 'email' AND ri.valid_to IS NULL
			AND (lower(ri.value) = ANY ($3::text[]) OR split_part(lower(ri.value), '@', 2) = ANY ($4::text[]))`,
		a.WorkspaceID, a.Brand, v.Emails, v.Domains).Scan(&v.Matched)
	return v, err
}

func (s *Server) getInternalPeople(w http.ResponseWriter, r *http.Request) {
	v, err := s.internalPeople(r, s.pool)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// putInternalPeople replaces the brand's list and its switch.
func (s *Server) putInternalPeople(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var in struct {
		Enabled *bool     `json:"enabled"`
		Domains *[]string `json:"domains"`
		Emails  *[]string `json:"emails"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Enabled == nil || in.Domains == nil || in.Emails == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send enabled (true or false), domains and emails (lists, empty for none): the three replace what was saved")
		return
	}
	if len(*in.Domains) > maxInternalEntries || len(*in.Emails) > maxInternalEntries {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "domains and emails take at most "+strconv.Itoa(maxInternalEntries)+" entries each")
		return
	}
	domains, emails := []string{}, []string{}
	for _, d := range *in.Domains {
		c, ok := tracking.ValidDomain(d)
		if !ok {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "domains are email domains such as acme.example; "+strconv.Quote(cutUTF8(d, 60))+" is not one")
			return
		}
		if c = strings.TrimPrefix(c, "www."); !slices.Contains(domains, c) {
			domains = append(domains, c)
		}
	}
	for _, e := range *in.Emails {
		c := strings.ToLower(strings.TrimSpace(e))
		if addr, err := mail.ParseAddress(c); err != nil || addr.Address != c || len(c) > 254 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "emails are single addresses such as qa@acme.example; one of them is not an email address")
			return
		}
		if !slices.Contains(emails, c) {
			emails = append(emails, c)
		}
	}
	var v internalPeopleView
	err := s.retryTx(ctx, "put_internal_people", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO internal_people (brand_id, workspace_id, enabled, domains, emails) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (brand_id) DO UPDATE SET enabled = EXCLUDED.enabled, domains = EXCLUDED.domains, emails = EXCLUDED.emails, updated_at = now()`,
			a.Brand, a.WorkspaceID, *in.Enabled, domains, emails); err != nil {
			return err
		}
		// Counts only: an address can be a person's.
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "internal_people.updated", "brand", a.Brand, map[string]any{
			"brand_id": a.Brand, "enabled": *in.Enabled, "domains": len(domains), "emails": len(emails)}); err != nil {
			return err
		}
		var err error
		v, err = s.internalPeople(r, tx)
		return err
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}
