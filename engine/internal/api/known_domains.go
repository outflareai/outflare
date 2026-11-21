package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// knownDomainsLimit caps each list of the known-domains answer. A brand
// with more gets its newest, and truncated says so.
const knownDomainsLimit = 200000

// domainDigest is the short, salted digest of a website's domain that the
// known-domains list carries: the first 8 bytes of SHA-256(salt:domain), in
// hex. The browser extension computes the same for the site in front of it.
func domainDigest(salt, domain string) string {
	sum := sha256.Sum256([]byte(salt + ":" + domain))
	return hex.EncodeToString(sum[:8])
}

// knownDomains answers which websites a brand already knows, for the
// browser extension's toolbar icon: the domains of the brand's companies
// and the domains on its do-not-contact list, each as a salted digest. The
// extension keeps the list and checks the open site against it inside the
// browser, so the sites someone visits are never sent to Outflare.
func (s *Server) knownDomains(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		s.internal(w, r, err)
		return
	}
	salt := hex.EncodeToString(raw)
	companies, blocked := []string{}, []string{}
	truncated := false
	collect := func(tx pgx.Tx, into *[]string, sql string) error {
		rows, err := tx.Query(ctx, sql, a.WorkspaceID, a.Brand, knownDomainsLimit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d string
			if err := rows.Scan(&d); err != nil {
				return err
			}
			if len(*into) == knownDomainsLimit {
				truncated = true
				break
			}
			*into = append(*into, domainDigest(salt, d))
		}
		return rows.Err()
	}
	err := s.readTx(ctx, func(tx pgx.Tx) error {
		if err := collect(tx, &companies, `SELECT i.value FROM record_identities i JOIN records r ON r.id = i.record_id
			WHERE i.workspace_id = $1 AND i.brand_id = $2 AND i.namespace = 'domain' AND i.valid_to IS NULL
			  AND r.workspace_id = $1 AND r.brand_id = $2 AND r.merged_into IS NULL AND r.deleted_at IS NULL AND r.archived_at IS NULL
			GROUP BY i.value ORDER BY max(i.created_at) DESC LIMIT $3`); err != nil {
			return err
		}
		// The brand's own entries and those for every brand count.
		return collect(tx, &blocked, `SELECT s.value FROM suppressions s
			WHERE s.workspace_id = $1 AND (s.brand_id = $2 OR s.brand_id IS NULL) AND s.kind = 'domain'
			GROUP BY s.value ORDER BY max(s.created_at) DESC LIMIT $3`)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"salt": salt, "companies": companies, "do_not_contact": blocked, "truncated": truncated})
}
