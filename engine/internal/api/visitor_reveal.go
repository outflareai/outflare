package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/netguard"
	"github.com/outflareai/outflare/engine/internal/tracking"
	"github.com/outflareai/outflare/engine/internal/visitors"
)

// Company reveal: the company behind an unknown visitor's IP address,
// looked up with the workspace's own provider account. Only visitors who
// accepted the site's cookies are looked up, each at most once a day, and
// one address at most once a week. The address goes to the provider in a
// job and is never stored: lookups are kept by a keyed hash.

const (
	jobVisitorReveal = "visitor_reveal"
	// revealEvery is how often one browser is looked up at most.
	revealEvery = 24 * time.Hour
	// revealCacheFor is how long a lookup of one address stands.
	revealCacheFor = 7 * 24 * time.Hour
	// revealForgetAfter is how long a found company stays on a visitor who
	// doesn't come back, unless someone added it to the CRM.
	revealForgetAfter = 90 * 24 * time.Hour
	// revealWait is how long a lookup waits for a visit still in the inbox.
	revealWait = 15 * time.Second
)

// ipHash keys an address so lookups can be found again without keeping it.
func (s *Server) ipHash(ip string) string { return s.box.Sign("visitor_ip", ip) }

type revealLookup struct {
	Found   bool
	Network string
	Domain  string
	Name    string
}

// cachedReveal is the standing lookup of an address, if any.
func cachedReveal(ctx context.Context, q pgx.Tx, ws, hash string, now time.Time) (*revealLookup, error) {
	var l revealLookup
	err := q.QueryRow(ctx, `SELECT found, network, domain, name FROM visitor_ip_lookups WHERE workspace_id = $1 AND ip_hash = $2 AND checked_at > $3`,
		ws, hash, now.Add(-revealCacheFor)).Scan(&l.Found, &l.Network, &l.Domain, &l.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &l, err
}

// revealConnection is the connection that looks addresses up: the one the
// settings name, else the first connected one that can.
func (s *Server) revealConnection(ctx context.Context, q connectors.Querier, ws string, set visitors.Settings) (string, error) {
	if s.integrations == nil {
		return "", nil
	}
	list, err := s.integrations.List(ctx, q, ws, "")
	if err != nil {
		return "", err
	}
	first := ""
	for _, i := range list {
		d, _ := s.integrations.Registry.Definition(i.Connector)
		if !d.Has(connectors.CapRevealCompany) || i.Status != connectors.StatusConnected {
			continue
		}
		if set.RevealIntegrationID != nil && *set.RevealIntegrationID == i.ID {
			return i.ID, nil
		}
		if first == "" {
			first = i.ID
		}
	}
	return first, nil
}

// queueReveal asks for the company behind a consenting, unknown visitor's
// address, after their page views are taken (stored, or put in the inbox
// for the workers). A known address is answered from the last lookup
// without calling anyone. A browser the workers haven't stored yet is
// looked up all the same, at most once a day: the answer is kept, and its
// next visit takes it.
func (s *Server) queueReveal(ctx context.Context, ws, anon, ip string) error {
	if s.box == nil || anon == "" {
		return nil
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil || !netguard.PublicAddr(addr) {
		return nil
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		set, err := visitors.Load(ctx, tx, ws)
		if err != nil || !set.RevealCompanies {
			return err
		}
		return s.queueRevealTx(ctx, tx, ws, set, anon, addr, time.Now().UTC())
	})
}

func (s *Server) queueRevealTx(ctx context.Context, tx pgx.Tx, ws string, set visitors.Settings, anon string, addr netip.Addr, now time.Time) error {
	var known bool
	var checked *time.Time
	err := tx.QueryRow(ctx, `SELECT record_id IS NOT NULL OR account_id IS NOT NULL OR company_domain <> '', reveal_checked_at FROM site_visitors WHERE workspace_id = $1 AND anonymous_id = $2`,
		ws, anon).Scan(&known, &checked)
	stored := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if known || (checked != nil && now.Sub(*checked) < revealEvery) {
		return nil
	}
	// The workers store the visit within a second or two; the lookup waits
	// for them, so its answer has a visitor to go to.
	var runAt time.Time
	if !stored {
		runAt = now.Add(revealWait)
	}
	hash := s.ipHash(addr.Unmap().String())
	if l, err := cachedReveal(ctx, tx, ws, hash, now); err != nil {
		return err
	} else if l != nil {
		if _, err := tx.Exec(ctx, `UPDATE site_visitors SET reveal_checked_at = $3, network = $4 WHERE workspace_id = $1 AND anonymous_id = $2`, ws, anon, now, l.Network); err != nil {
			return err
		}
		if !l.Found {
			return nil
		}
		return s.applyReveal(ctx, tx, ws, set, anon, *l, "", now)
	}
	conn, err := s.revealConnection(ctx, tx, ws, set)
	if err != nil || conn == "" {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE site_visitors SET reveal_checked_at = $3 WHERE workspace_id = $1 AND anonymous_id = $2`, ws, anon, now); err != nil {
		return err
	}
	sealed := s.box.Seal([]byte(addr.Unmap().String()), []byte(ws))
	_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: jobVisitorReveal, WorkspaceID: &ws, MaxAttempts: 4, RunAt: runAt,
		IdempotencyKey: "reveal:" + ws + ":" + anon + ":" + now.UTC().Format("2006-01-02"),
		Payload:        map[string]string{"anonymous_id": anon, "integration_id": conn, "address": base64.RawStdEncoding.EncodeToString(sealed)}})
	return err
}

// visitorRevealJob looks one address up with the workspace's provider and
// adds the company it names to the visitor.
func (s *Server) visitorRevealJob(ctx context.Context, job jobs.Job) error {
	var p struct {
		AnonymousID   string `json:"anonymous_id"`
		IntegrationID string `json:"integration_id"`
		Address       string `json:"address"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil || s.box == nil || !validUUID(p.IntegrationID) {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	sealed, err := base64.RawStdEncoding.DecodeString(p.Address)
	if err != nil {
		return fmt.Errorf("%w: bad address", jobs.ErrPermanent)
	}
	plain, err := s.box.Open(sealed, []byte(ws))
	if err != nil {
		// Sealed with a key the server no longer has; nothing to retry.
		return nil
	}
	ip := string(plain)
	hash := s.ipHash(ip)
	now := time.Now().UTC()
	set, err := visitors.Load(ctx, s.pool, ws)
	if err != nil {
		return err
	}
	if !set.RevealCompanies {
		return nil
	}
	// Companies found for visitors who haven't been back in 90 days, and
	// that nobody added to the CRM, are dropped.
	if _, err := s.pool.Exec(ctx, `UPDATE site_visitors SET company_domain = '', company_name = '',
			identified_by = CASE WHEN identified_by = 'company_ip' THEN '' ELSE identified_by END
		WHERE workspace_id = $1 AND account_id IS NULL AND company_domain <> '' AND last_seen_at < $2`, ws, now.Add(-revealForgetAfter)); err != nil {
		return err
	}
	var done bool
	var today int
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM visitor_ip_lookups WHERE workspace_id = $1 AND ip_hash = $2 AND checked_at > $3),
			(SELECT count(*) FROM visitor_ip_lookups WHERE workspace_id = $1 AND checked_at >= date_trunc('day', $4::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')`,
		ws, hash, now.Add(-revealCacheFor), now).Scan(&done, &today); err != nil {
		return err
	}
	if !done && today >= set.RevealDailyLimit {
		return nil
	}
	var r connectors.Revealed
	conn := ""
	if !done {
		o, err := s.integrations.Open(ctx, s.pool, ws, p.IntegrationID)
		if errors.Is(err, connectors.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		rv, ok := o.Connector.(connectors.CompanyRevealer)
		if !ok {
			return nil
		}
		conn = o.Connector.Definition().ID
		if r, err = rv.RevealCompany(ctx, o.Client, o.Creds, ip); err != nil {
			if k := connectors.KindOf(err); k == connectors.ErrAuth || k == connectors.ErrQuota {
				_ = s.integrations.Checked(ctx, s.pool, ws, p.IntegrationID, nil, err)
				return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
			}
			return err
		}
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		l := revealLookup{}
		if done {
			cached, err := cachedReveal(ctx, tx, ws, hash, now)
			if err != nil || cached == nil {
				return err
			}
			l = *cached
		} else {
			l = revealLookup{Found: r.Found && r.Network == connectors.NetworkBusiness && r.Company.Domain != "", Network: r.Network, Domain: r.Company.Domain, Name: r.Company.Name}
			if l.Found && visitors.Matches(strings.ToLower(l.Domain), set.NotCompanies) {
				l.Found, l.Network = false, connectors.NetworkISP
			}
			if !l.Found {
				l.Domain, l.Name = "", ""
			}
			if _, err := tx.Exec(ctx, `INSERT INTO visitor_ip_lookups (workspace_id, ip_hash, found, network, domain, name, integration_id, checked_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
				ON CONFLICT (workspace_id, ip_hash) DO UPDATE SET found = EXCLUDED.found, network = EXCLUDED.network, domain = EXCLUDED.domain,
					name = EXCLUDED.name, integration_id = EXCLUDED.integration_id, checked_at = EXCLUDED.checked_at`,
				ws, hash, l.Found, r.Network, l.Domain, cutUTF8(l.Name, 200), p.IntegrationID, now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE site_visitors SET network = $3 WHERE workspace_id = $1 AND anonymous_id = $2`, ws, p.AnonymousID, l.Network); err != nil {
			return err
		}
		if !l.Found {
			return nil
		}
		return s.applyReveal(ctx, tx, ws, set, p.AnonymousID, l, conn, now)
	})
}

// applyReveal puts a looked-up company on an unknown visitor: its domain
// and name, and the CRM's company with that domain when there is one.
// Lookups never make CRM records: someone adds the company (addVisitorCompany).
// The workspace's excluded domains and domains marked as not a company are
// left alone.
func (s *Server) applyReveal(ctx context.Context, tx pgx.Tx, ws string, set visitors.Settings, anon string, l revealLookup, connector string, now time.Time) error {
	domain := strings.TrimPrefix(strings.ToLower(l.Domain), "www.")
	if visitors.Matches(domain, set.ExcludeDomains) || visitors.Matches(domain, set.NotCompanies) {
		return nil
	}
	acct, err := crmCompany(ctx, tx, ws, domain)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE site_visitors SET company_domain = $3, company_name = $4, account_id = $5,
			identified_by = CASE WHEN identified_by = '' THEN 'company_ip' ELSE identified_by END
		WHERE workspace_id = $1 AND anonymous_id = $2 AND record_id IS NULL AND account_id IS NULL`, ws, anon, domain, cutUTF8(l.Name, 200), acct)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "visitor.company_revealed",
		TargetType: "workspace", TargetID: ws, RequestID: RequestID(ctx),
		Data:  map[string]any{"record_id": acct, "in_crm": acct != nil, "connector": connector},
		Props: map[string]any{"in_crm": acct != nil, "connector": connector}}); err != nil {
		return err
	}
	if acct == nil {
		return nil
	}
	return s.alertSubjects(ctx, tx, ws, set, []hotSubject{{id: *acct, kind: "company"}}, now)
}

// crmCompany is the CRM company with a domain, if any.
func crmCompany(ctx context.Context, tx pgx.Tx, ws, domain string) (*string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT r.id::text FROM record_identities i JOIN records r ON r.id = i.record_id
		WHERE i.workspace_id = $1 AND i.namespace = 'domain' AND i.value = $2 AND i.valid_to IS NULL
		  AND r.record_type = 'account' AND r.merged_into IS NULL AND r.archived_at IS NULL
		ORDER BY i.verified DESC, i.created_at LIMIT 1`, ws, domain).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}

var errNoVisitorCompany = errors.New("no visitor at that domain")

// addVisitorCompany adds a company found only from visitors' addresses to
// the CRM, marked as from a third party (origin prospect, its domain from
// visitor_reveal), and puts it on those visitors.
func (s *Server) addVisitorCompany(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var in struct {
		Domain string `json:"domain"`
	}
	if !decode(w, r, &in) {
		return
	}
	domain, ok := tracking.ValidDomain(in.Domain)
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "domain must be a domain such as northpeak.example, as the Visitors page lists it")
		return
	}
	domain = strings.TrimPrefix(domain, "www.")
	var id string
	created := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var name string
		var brand *string
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*), COALESCE(max(company_name), ''), (array_agg(brand_id::text))[1] FROM site_visitors
			WHERE workspace_id = $1 AND company_domain = $2`, a.WorkspaceID, domain).Scan(&n, &name, &brand); err != nil {
			return err
		}
		if n == 0 {
			return errNoVisitorCompany
		}
		if !a.writes(nil, brand) {
			brand = nil
		}
		acct, err := crmCompany(ctx, tx, a.WorkspaceID, domain)
		if err != nil {
			return err
		}
		if acct != nil {
			id = *acct
		} else {
			if name == "" {
				name = domain
			}
			// The tool whose lookup named it, as its source.
			via := "visitor_reveal"
			if err := tx.QueryRow(ctx, `SELECT i.connector FROM visitor_ip_lookups l JOIN integrations i ON i.id = l.integration_id
				WHERE l.workspace_id = $1 AND l.domain = $2 ORDER BY l.checked_at DESC LIMIT 1`, a.WorkspaceID, domain).Scan(&via); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO records (workspace_id, record_type, brand_id, name, origin, acquired_via, created_by) VALUES ($1, 'account', $2, $3, 'prospect', $4, $5) RETURNING id::text`,
				a.WorkspaceID, brand, name, via, nullIfEmpty(a.ActorID)).Scan(&id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO record_identities (workspace_id, record_id, namespace, value, verified, source, created_by)
				VALUES ($1, $2, 'domain', $3, false, 'visitor_reveal', $4)`, a.WorkspaceID, id, domain, nullIfEmpty(a.ActorID)); err != nil {
				return err
			}
			created = true
		}
		if _, err := tx.Exec(ctx, `UPDATE site_visitors SET account_id = $3 WHERE workspace_id = $1 AND company_domain = $2 AND record_id IS NULL AND account_id IS NULL`,
			a.WorkspaceID, domain, id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "visitor.company_added", "record", id, map[string]any{"record_id": id, "created": created, "browsers": n})
	})
	if err != nil {
		var bad errInvalid
		switch {
		case errors.As(err, &bad):
			writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		case errors.Is(err, errNoVisitorCompany):
			writeError(w, r, http.StatusNotFound, "not_found", "no visitor was found at "+domain+": add a company the Visitors page lists")
		default:
			s.internal(w, r, err)
		}
		return
	}
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	writeJSON(w, code, map[string]any{"record_id": id, "created": created})
}

// revealConnections lists the workspace's connections that can look up
// companies, for the settings.
func (s *Server) revealConnections(ctx context.Context, ws string) ([]map[string]string, error) {
	out := []map[string]string{}
	if s.integrations == nil {
		return out, nil
	}
	list, err := s.integrations.List(ctx, s.pool, ws, "")
	if err != nil {
		return nil, err
	}
	for _, i := range list {
		if d, _ := s.integrations.Registry.Definition(i.Connector); d.Has(connectors.CapRevealCompany) {
			out = append(out, map[string]string{"id": i.ID, "name": i.Name, "connector": i.Connector, "status": i.Status})
		}
	}
	slices.SortStableFunc(out, func(a, b map[string]string) int { return strings.Compare(a["connector"], b["connector"]) })
	return out, nil
}

// cleanNotCompanies takes domains just marked as not a company off the
// visitors they were found for: their lookups name nobody from now on, the
// visitors count as on an ISP's network, and company records that only the
// lookups made, with nothing else on them, are archived.
func cleanNotCompanies(ctx context.Context, tx pgx.Tx, ws string, domains []string) error {
	match := func(col string) string {
		return `(` + col + ` = ANY($2) OR EXISTS (SELECT 1 FROM unnest($2::text[]) x WHERE ` + col + ` LIKE '%.' || x))`
	}
	if _, err := tx.Exec(ctx, `UPDATE visitor_ip_lookups SET found = false, network = 'isp', domain = '', name = ''
		WHERE workspace_id = $1 AND found AND `+match("domain"), ws, domains); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE site_visitors SET company_domain = '', company_name = '', network = 'isp',
			identified_by = CASE WHEN identified_by = 'company_ip' THEN '' ELSE identified_by END
		WHERE workspace_id = $1 AND record_id IS NULL AND company_domain <> '' AND `+match("company_domain"), ws, domains); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT i.record_id::text FROM record_identities i
		WHERE i.workspace_id = $1 AND i.namespace = 'domain' AND i.valid_to IS NULL AND `+match("i.value"), ws, domains)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(ids) == 0 {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE site_visitors SET account_id = NULL, network = 'isp',
			identified_by = CASE WHEN identified_by = 'company_ip' THEN '' ELSE identified_by END
		WHERE workspace_id = $1 AND account_id = ANY($2::uuid[]) AND identified_by IN ('company_ip', '') AND record_id IS NULL`, ws, ids); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE records r SET archived_at = now(), version = version + 1, updated_at = now()
		WHERE r.workspace_id = $1 AND r.id = ANY($2::uuid[]) AND r.origin = 'prospect' AND r.archived_at IS NULL
		  AND EXISTS (SELECT 1 FROM record_identities i WHERE i.record_id = r.id AND i.source = 'visitor_reveal' AND i.valid_to IS NULL)
		  AND NOT EXISTS (SELECT 1 FROM record_identities i WHERE i.record_id = r.id AND i.source <> 'visitor_reveal' AND i.valid_to IS NULL)
		  AND NOT EXISTS (SELECT 1 FROM record_relationships rr WHERE (rr.from_id = r.id OR rr.to_id = r.id) AND rr.valid_to IS NULL)
		  AND NOT EXISTS (SELECT 1 FROM site_visitors sv WHERE sv.account_id = r.id)`, ws, ids)
	return err
}
