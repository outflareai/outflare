package linkorders

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Sharing a catalog: one brand's donors, with their metrics, prices and
// flags, written into another brand of the same workspace. A broker's
// database is the same for every brand, so it is imported once and copied,
// instead of importing the file into each brand. The copy is the target
// brand's own: each brand keeps its own publishers, orders, notes on the
// relationship and history, and nothing reads across brands afterwards.

// MaxCopyPage is the most donors one CopyCatalog call writes.
const MaxCopyPage = 500

// CopyResult is what one CopyCatalog call did.
type CopyResult struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
	// Next is where the next call starts; nil when the source is done.
	Next *Cursor `json:"-"`
}

// CopyCatalog writes up to limit of the source brand's live (not
// archived) donors into the target brand, newest first from after, the
// same way an import does: a donor already in the target is updated, its
// prices replaced, its metrics merged. Both brands must be in workspaceID.
func CopyCatalog(ctx context.Context, tx pgx.Tx, workspaceID, from, to uuid.UUID, after *Cursor, limit int) (CopyResult, error) {
	var out CopyResult
	if from == to {
		return out, fmt.Errorf("%w: copy to another brand", ErrInvalid)
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM brands WHERE workspace_id = $1 AND id = ANY($2)`,
		workspaceID, []uuid.UUID{from, to}).Scan(&n); err != nil {
		return out, err
	}
	if n != 2 {
		return out, ErrNotFound
	}
	if limit <= 0 || limit > MaxCopyPage {
		limit = MaxCopyPage
	}
	list, err := ListDonors(ctx, tx, workspaceID, from, DonorListFilter{}, limit+1, after)
	if err != nil {
		return out, err
	}
	more := len(list) > limit
	if more {
		list = list[:limit]
	}
	ids := make([]uuid.UUID, len(list))
	for i, d := range list {
		ids[i] = d.ID
	}
	// The stored metrics as they are, so every key carries over.
	raw := map[uuid.UUID][]byte{}
	rows, err := tx.Query(ctx, `SELECT id, metrics FROM coverage_donor_profiles
		WHERE workspace_id = $1 AND brand_id = $2 AND id = ANY($3)`, workspaceID, from, ids)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var id uuid.UUID
		var m []byte
		if err := rows.Scan(&id, &m); err != nil {
			rows.Close()
			return out, err
		}
		raw[id] = m
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	for _, d := range list {
		w, err := copyWrite(d, raw[d.ID])
		if err != nil {
			return out, err
		}
		_, created, err := UpsertDonor(ctx, tx, workspaceID, to, w)
		if err != nil {
			return out, fmt.Errorf("copy %s: %w", d.Domain, err)
		}
		if created {
			out.Created++
		} else {
			out.Updated++
		}
	}
	if more && len(list) > 0 {
		last := list[len(list)-1]
		out.Next = &Cursor{At: last.CreatedAt, ID: last.ID}
	}
	return out, nil
}

// copyWrite is the write that gives the target brand the same donor.
func copyWrite(d Donor, metrics []byte) (DonorWrite, error) {
	source := d.Source
	if source != SourceManual && source != SourceCSV && source != SourceCompetitorGap {
		source = SourceCSV
	}
	opt := func(s string) **string {
		if s == "" {
			return nil
		}
		return ptrPtr(s)
	}
	pbn, best := d.PBN, d.BestLink
	langs, niches, rel, slots, prices := d.Languages, d.Niches, d.RelOffered, d.SlotTypes, d.Prices
	casino := d.CasinoAllowed
	w := DonorWrite{
		Domain: d.Domain, Source: source, PBN: &pbn, BestLink: &best,
		Country: opt(d.Country), Languages: &langs, Niches: &niches, CasinoAllowed: ptrPtr(casino),
		RelOffered: &rel, SlotTypes: &slots, Duration: opt(d.Duration),
		VendorID: opt(d.VendorID), MetricsSource: opt(d.MetricsSource), Notes: opt(d.Notes),
		Intermediary: opt(d.Intermediary), Theme: opt(d.Theme), Prices: &prices,
	}
	if d.DurationDays != nil {
		w.DurationDays = ptrPtr(*d.DurationDays)
	}
	if d.TATDays != nil {
		w.TATDays = ptrPtr(*d.TATDays)
	}
	if d.GuaranteeDays != nil {
		w.GuaranteeDays = ptrPtr(*d.GuaranteeDays)
	}
	if d.DomainRating != nil {
		w.DomainRating = ptrPtr(*d.DomainRating)
	}
	if d.Traffic != nil {
		w.Traffic = ptrPtr(*d.Traffic)
	}
	if d.SpamScore != nil {
		w.SpamScore = ptrPtr(*d.SpamScore)
	}
	if d.MetricsAt != nil {
		w.MetricsAt = ptrPtr(*d.MetricsAt)
	}
	if d.SiteSince != nil {
		t, err := time.Parse("2006-01-02", *d.SiteSince)
		if err == nil {
			w.SiteSince = ptrPtr(t)
		}
	}
	if len(metrics) > 0 {
		var m map[string]any
		if err := json.Unmarshal(metrics, &m); err != nil {
			return w, err
		}
		// Only keys a write accepts; anything older stays behind.
		for k := range m {
			if !IsMetric(k) {
				delete(m, k)
			}
		}
		if len(m) > 0 {
			w.Metrics = m
		}
	}
	return w, nil
}
