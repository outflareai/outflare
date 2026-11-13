package linkorders

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
)

// DonorWrite is the fields a catalog create or update may set.
type DonorWrite struct {
	Domain        string
	Name          string
	PBN           *bool
	Country       **string
	Languages     *[]string
	Niches        *[]string
	CasinoAllowed **bool
	RelOffered    *[]string
	SlotTypes     *[]string
	Duration      **string
	DurationDays  **int
	TATDays       **int
	GuaranteeDays **int
	VendorID      **string
	Source        string
	DomainRating  **int
	Traffic       **int64
	SpamScore     **float64
	MetricsAt     **time.Time
	MetricsSource **string
	Notes         **string
	Intermediary  **string
	Theme         **string
	// SiteSince sets site_since; a nil inner pointer clears it.
	SiteSince **time.Time
	// Metrics merges into the stored metrics: a key with a value sets
	// it, a key with nil clears it, a missing key is kept.
	Metrics  map[string]any
	Archived *bool
	Prices   *[]Price
}

// EnsurePublisher finds or creates the coverage publisher for domain on
// this brand, and the customers company it points at. Domain is normalized
// the same way as other coverage records.
func EnsurePublisher(ctx context.Context, tx pgx.Tx, workspaceID, brandID uuid.UUID, domain, name string) (uuid.UUID, string, error) {
	domain = customers.CompanyDomain(domain)
	if domain == "" {
		return uuid.Nil, "", fmt.Errorf("%w: domain is not a valid website host", ErrInvalid)
	}
	if name == "" {
		name = domain
	}
	ws, brand := workspaceID.String(), brandID.String()
	companies, err := customers.EnsureCompanies(ctx, tx, ws, &brand, "link_orders", []customers.Company{{
		Domain: domain,
		Name:   name,
	}})
	if err != nil {
		return uuid.Nil, "", err
	}
	recordID, ok := companies[domain]
	if !ok {
		return uuid.Nil, "", fmt.Errorf("ensure company for %s: missing id", domain)
	}
	var id uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO coverage_publishers (
			workspace_id, brand_id, record_id, domain, status
		) VALUES ($1, $2, $3, $4, 'active')
		ON CONFLICT (workspace_id, brand_id, domain) DO UPDATE
			SET updated_at = now()
		RETURNING id
	`, workspaceID, brandID, recordID, domain).Scan(&id)
	if err != nil {
		return uuid.Nil, "", err
	}
	return id, domain, nil
}

// UpsertDonor writes one catalog row for a publisher, creating it on first
// write. Prices, when provided, replace the current published rates for
// those currencies (older published rates are expired).
func UpsertDonor(ctx context.Context, tx pgx.Tx, workspaceID, brandID uuid.UUID, in DonorWrite) (*Donor, bool, error) {
	publisherID, domain, err := EnsurePublisher(ctx, tx, workspaceID, brandID, in.Domain, in.Name)
	if err != nil {
		return nil, false, err
	}
	in.Domain = domain
	source := in.Source
	if source == "" {
		source = SourceManual
	}
	if source != SourceManual && source != SourceCSV && source != SourceCompetitorGap {
		return nil, false, fmt.Errorf("%w: source must be manual, csv, or competitor_gap", ErrInvalid)
	}

	var existingID uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT id FROM coverage_donor_profiles
		WHERE workspace_id = $1 AND brand_id = $2 AND publisher_id = $3
	`, workspaceID, brandID, publisherID).Scan(&existingID)
	created := err == pgx.ErrNoRows
	if err != nil && !created {
		return nil, false, err
	}

	if created {
		pbn := false
		if in.PBN != nil {
			pbn = *in.PBN
		}
		metrics, err := metricsJSON(in.Metrics)
		if err != nil {
			return nil, false, err
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO coverage_donor_profiles (
				workspace_id, brand_id, publisher_id, pbn,
				country, languages, niches, casino_allowed,
				rel_offered, slot_types, duration, duration_days,
				tat_days, guarantee_days, vendor_id, source,
				domain_rating, traffic, spam_score, metrics_at,
				metrics_source, notes, intermediary, theme,
				site_since, metrics
			) VALUES (
				$1, $2, $3, $4,
				$5, $6, $7, $8,
				$9, $10, $11, $12,
				$13, $14, $15, $16,
				$17, $18, $19, $20,
				$21, $22, $23, $24,
				$25, jsonb_strip_nulls($26::jsonb)
			)
			RETURNING id
		`,
			workspaceID, brandID, publisherID, pbn,
			strOrEmpty(in.Country), strsOrEmpty(in.Languages), strsOrEmpty(in.Niches), boolOrFalse(in.CasinoAllowed),
			strsOrEmpty(in.RelOffered), strsOrEmpty(in.SlotTypes), strOrEmpty(in.Duration), derefInt(in.DurationDays),
			derefInt(in.TATDays), derefInt(in.GuaranteeDays), strOrEmpty(in.VendorID), source,
			derefInt(in.DomainRating), derefInt64(in.Traffic), derefFloat(in.SpamScore), derefTime(in.MetricsAt),
			strOrEmpty(in.MetricsSource), strOrEmpty(in.Notes), strOrEmpty(in.Intermediary), strOrEmpty(in.Theme),
			derefTime(in.SiteSince), metrics,
		).Scan(&existingID)
		if err != nil {
			return nil, false, err
		}
	} else if err := patchDonor(ctx, tx, existingID, source, in); err != nil {
		return nil, false, err
	}

	if in.Prices != nil {
		if err := replacePrices(ctx, tx, workspaceID, brandID, publisherID, *in.Prices); err != nil {
			return nil, false, err
		}
	}

	d, err := GetDonor(ctx, tx, workspaceID, brandID, existingID)
	return d, created, err
}

func patchDonor(ctx context.Context, tx pgx.Tx, id uuid.UUID, source string, in DonorWrite) error {
	// Build a single UPDATE that only touches provided fields, always
	// refreshing source and updated_at so a CSV re-import is visible.
	sets := []string{"source = $2", "updated_at = now()"}
	args := []any{id, source}
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if in.PBN != nil {
		add("pbn", *in.PBN)
	}
	if in.Country != nil {
		add("country", strOrEmpty(in.Country))
	}
	if in.Languages != nil {
		add("languages", *in.Languages)
	}
	if in.Niches != nil {
		add("niches", *in.Niches)
	}
	if in.CasinoAllowed != nil {
		v := false
		if *in.CasinoAllowed != nil {
			v = **in.CasinoAllowed
		}
		add("casino_allowed", v)
	}
	if in.RelOffered != nil {
		add("rel_offered", *in.RelOffered)
	}
	if in.SlotTypes != nil {
		add("slot_types", *in.SlotTypes)
	}
	if in.Duration != nil {
		add("duration", strOrEmpty(in.Duration))
	}
	if in.DurationDays != nil {
		add("duration_days", *in.DurationDays)
	}
	if in.TATDays != nil {
		add("tat_days", *in.TATDays)
	}
	if in.GuaranteeDays != nil {
		add("guarantee_days", *in.GuaranteeDays)
	}
	if in.VendorID != nil {
		add("vendor_id", strOrEmpty(in.VendorID))
	}
	if in.DomainRating != nil {
		add("domain_rating", *in.DomainRating)
	}
	if in.Traffic != nil {
		add("traffic", *in.Traffic)
	}
	if in.SpamScore != nil {
		add("spam_score", *in.SpamScore)
	}
	if in.MetricsAt != nil {
		add("metrics_at", *in.MetricsAt)
	}
	if in.MetricsSource != nil {
		add("metrics_source", strOrEmpty(in.MetricsSource))
	}
	if in.Notes != nil {
		add("notes", strOrEmpty(in.Notes))
	}
	if in.Intermediary != nil {
		add("intermediary", strOrEmpty(in.Intermediary))
	}
	if in.Theme != nil {
		add("theme", strOrEmpty(in.Theme))
	}
	if in.SiteSince != nil {
		add("site_since", *in.SiteSince)
	}
	if len(in.Metrics) > 0 {
		m, err := metricsJSON(in.Metrics)
		if err != nil {
			return err
		}
		args = append(args, m)
		sets = append(sets, fmt.Sprintf("metrics = jsonb_strip_nulls(metrics || $%d::jsonb)", len(args)))
	}
	if in.Archived != nil {
		if *in.Archived {
			add("archived_at", time.Now().UTC())
		} else {
			add("archived_at", nil)
		}
	}
	_, err := tx.Exec(ctx, `UPDATE coverage_donor_profiles SET `+strings.Join(sets, ", ")+` WHERE id = $1`, args...)
	return err
}

// metricsJSON encodes a metrics patch; nil values stay as JSON null so a
// merge clears them before the nulls are stripped.
func metricsJSON(m map[string]any) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	for k := range m {
		if !IsMetric(k) {
			return "", fmt.Errorf("%w: %s is not a known metric", ErrInvalid, k)
		}
	}
	b, err := json.Marshal(m)
	return string(b), err
}

func replacePrices(ctx context.Context, tx pgx.Tx, workspaceID, brandID, publisherID uuid.UUID, prices []Price) error {
	seen := map[string]bool{}
	for _, p := range prices {
		if err := validCurrency(p.Currency); err != nil {
			return err
		}
		if p.AmountMinor < 0 {
			return fmt.Errorf("%w: amount_minor must be >= 0", ErrInvalid)
		}
		if seen[p.Currency] {
			return fmt.Errorf("%w: duplicate currency %s in prices", ErrInvalid, p.Currency)
		}
		seen[p.Currency] = true
		if _, err := tx.Exec(ctx, `
			UPDATE coverage_rates
			SET status = 'expired', updated_at = now()
			WHERE workspace_id = $1 AND brand_id = $2 AND publisher_id = $3
			  AND currency = $4 AND status = 'published'
		`, workspaceID, brandID, publisherID, p.Currency); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO coverage_rates (
				workspace_id, brand_id, publisher_id,
				title, unit, status, currency, amount_minor
			) VALUES ($1, $2, $3, $4, 'placement', 'published', $5, $6)
		`, workspaceID, brandID, publisherID, "Link order "+p.Currency, p.Currency, p.AmountMinor); err != nil {
			return err
		}
	}
	return nil
}

// GetDonor returns one catalog row, or ErrNotFound.
func GetDonor(ctx context.Context, q Querier, workspaceID, brandID, id uuid.UUID) (*Donor, error) {
	list, err := listDonorsQuery(ctx, q, workspaceID, brandID, DonorListFilter{ID: &id, IncludeArchived: true}, 1, nil)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return &list[0], nil
}

type DonorListFilter struct {
	ID    *uuid.UUID
	Q     string
	PBN   *bool
	Niche string
	// Intermediary matches the broker or contact name, ignoring case.
	Intermediary string
	// DirectContact keeps only direct-contact recommendations (true) or
	// only the rest (false).
	DirectContact   *bool
	IncludeArchived bool
	OnlyArchived    bool
}

// ListDonors returns catalog rows, newest first, limited to limit+1 so the
// caller can tell if there is another page. before is a (created_at, id)
// cursor from the previous page.
func ListDonors(ctx context.Context, q Querier, workspaceID, brandID uuid.UUID, f DonorListFilter, limit int, before *Cursor) ([]Donor, error) {
	return listDonorsQuery(ctx, q, workspaceID, brandID, f, limit, before)
}

type Cursor struct {
	At time.Time
	ID uuid.UUID
}

func listDonorsQuery(ctx context.Context, q Querier, workspaceID, brandID uuid.UUID, f DonorListFilter, limit int, before *Cursor) ([]Donor, error) {
	where := []string{"p.workspace_id = $1", "p.brand_id = $2"}
	args := []any{workspaceID, brandID}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.Replace(cond, "?", fmt.Sprintf("$%d", len(args)), 1))
	}
	if f.ID != nil {
		add("p.id = ?", *f.ID)
	}
	if f.Q != "" {
		// Search covers the site, its intermediary and its theme, so a
		// broker's name finds every site bought through them.
		args = append(args, "%"+likeEscape(f.Q)+"%")
		n := len(args)
		where = append(where, fmt.Sprintf("(pub.domain ILIKE $%d OR p.intermediary ILIKE $%d OR p.theme ILIKE $%d)", n, n, n))
	}
	if f.PBN != nil {
		add("p.pbn = ?", *f.PBN)
	}
	if f.Niche != "" {
		add("? = ANY (p.niches)", f.Niche)
	}
	if f.Intermediary != "" {
		add("lower(p.intermediary) = lower(?)", f.Intermediary)
	}
	if f.DirectContact != nil {
		where = append(where, directContactCond(*f.DirectContact))
	}
	switch {
	case f.OnlyArchived:
		where = append(where, "p.archived_at IS NOT NULL")
	case !f.IncludeArchived:
		where = append(where, "p.archived_at IS NULL")
	}
	if before != nil {
		args = append(args, before.At, before.ID)
		where = append(where, fmt.Sprintf("(p.created_at, p.id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	args = append(args, limit)
	sql := `
		SELECT p.id, p.publisher_id, pub.domain, p.pbn, p.country, p.languages, p.niches,
			p.casino_allowed, p.rel_offered, p.slot_types, p.duration, p.duration_days,
			p.tat_days, p.guarantee_days, p.vendor_id, p.source, p.domain_rating,
			p.traffic, p.spam_score, p.metrics_at, p.metrics_source, p.notes, p.archived_at,
			p.created_at, p.updated_at, p.intermediary, p.theme, p.site_since, p.metrics,
			COALESCE((
				SELECT jsonb_agg(jsonb_build_object('currency', r.currency, 'amount_minor', r.amount_minor) ORDER BY r.currency)
				FROM coverage_rates r
				WHERE r.publisher_id = p.publisher_id
				  AND r.workspace_id = p.workspace_id
				  AND r.brand_id = p.brand_id
				  AND r.status = 'published'
			), '[]'::jsonb)
		FROM coverage_donor_profiles p
		JOIN coverage_publishers pub ON pub.id = p.publisher_id
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY p.created_at DESC, p.id DESC
		LIMIT $` + fmt.Sprint(len(args))
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Donor
	for rows.Next() {
		var d Donor
		var prices, metrics []byte
		var since *time.Time
		if err := rows.Scan(
			&d.ID, &d.PublisherID, &d.Domain, &d.PBN, &d.Country, &d.Languages, &d.Niches,
			&d.CasinoAllowed, &d.RelOffered, &d.SlotTypes, &d.Duration, &d.DurationDays,
			&d.TATDays, &d.GuaranteeDays, &d.VendorID, &d.Source, &d.DomainRating,
			&d.Traffic, &d.SpamScore, &d.MetricsAt, &d.MetricsSource, &d.Notes, &d.ArchivedAt,
			&d.CreatedAt, &d.UpdatedAt, &d.Intermediary, &d.Theme, &since, &metrics, &prices,
		); err != nil {
			return nil, err
		}
		if since != nil {
			s := since.Format("2006-01-02")
			y := SiteAgeYears(*since, time.Now())
			d.SiteSince, d.SiteAgeYears = &s, &y
		}
		if len(metrics) > 0 {
			if err := json.Unmarshal(metrics, &d.Metrics); err != nil {
				return nil, err
			}
		}
		if d.Languages == nil {
			d.Languages = []string{}
		}
		if d.Niches == nil {
			d.Niches = []string{}
		}
		if d.RelOffered == nil {
			d.RelOffered = []string{}
		}
		if d.SlotTypes == nil {
			d.SlotTypes = []string{}
		}
		d.Prices = []Price{}
		if len(prices) > 0 {
			if err := json.Unmarshal(prices, &d.Prices); err != nil {
				return nil, err
			}
			if d.Prices == nil {
				d.Prices = []Price{}
			}
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if err := fillDirectContact(ctx, q, workspaceID, brandID, out); err != nil {
		return nil, err
	}
	return out, nil
}

// likeEscape escapes the LIKE wildcards in a search term.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func strOrEmpty(p **string) string {
	if p == nil || *p == nil {
		return ""
	}
	return **p
}

func strsOrEmpty(p *[]string) []string {
	if p == nil || *p == nil {
		return []string{}
	}
	return *p
}

func boolOrFalse(p **bool) bool {
	if p == nil || *p == nil {
		return false
	}
	return **p
}
func derefStr(p **string) *string {
	if p == nil {
		return nil
	}
	return *p
}
func derefBool(p **bool) *bool {
	if p == nil {
		return nil
	}
	return *p
}
func derefInt(p **int) *int {
	if p == nil {
		return nil
	}
	return *p
}
func derefInt64(p **int64) *int64 {
	if p == nil {
		return nil
	}
	return *p
}
func derefFloat(p **float64) *float64 {
	if p == nil {
		return nil
	}
	return *p
}
func derefTime(p **time.Time) *time.Time {
	if p == nil {
		return nil
	}
	return *p
}
func derefUUID(p **uuid.UUID) *uuid.UUID {
	if p == nil {
		return nil
	}
	return *p
}
func derefStrs(p *[]string) []string {
	if p == nil {
		return nil
	}
	return *p
}

func validCurrency(c string) error {
	if c != CurrencyUSD && c != CurrencyEUR {
		return fmt.Errorf("%w: currency must be USD or EUR", ErrInvalid)
	}
	return nil
}
