package brands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Address is a brand's postal address, in the same shape as a customer
// record's location attribute. Country is an ISO 3166-1 alpha-2 code.
type Address struct {
	Line1      string `json:"line1"`
	Line2      string `json:"line2"`
	City       string `json:"city"`
	Region     string `json:"region"`
	PostalCode string `json:"postal_code"`
	Country    string `json:"country"`
}

// Business is who stands behind a brand: the legal name, postal address
// and tax or company number it is entered once under "About your company"
// and that email footers, help centres, cookie policies and agents reuse.
type Business struct {
	LegalName string  `json:"legal_name"`
	TaxID     string  `json:"tax_id"`
	Address   Address `json:"address"`
}

// Field limits.
const (
	MaxLegalName = 200
	MaxTaxID     = 60
	MaxAddress   = 200
)

// Clean trims every part and upper-cases the country.
func (a *Address) Clean() {
	for _, p := range a.parts() {
		*p = strings.TrimSpace(*p)
	}
	a.Country = strings.ToUpper(a.Country)
}

func (a *Address) parts() []*string {
	return []*string{&a.Line1, &a.Line2, &a.City, &a.Region, &a.PostalCode, &a.Country}
}

// Check says what is wrong with a cleaned address, or nil. An empty
// address is fine: it clears it.
func (a Address) Check() error {
	for _, p := range a.parts() {
		if len(*p) > MaxAddress {
			return fmt.Errorf("each part of the address must be at most %d characters", MaxAddress)
		}
	}
	if a.Empty() {
		return nil
	}
	if a.Country == "" || CountryName(a.Country) == "" {
		return errors.New("the address needs a country, as a two-letter ISO 3166 code such as AU")
	}
	if a.Line1 == "" || a.City == "" {
		return errors.New("the address needs at least line1 (the street or PO box) and city")
	}
	return nil
}

// Empty reports whether no part is set.
func (a Address) Empty() bool {
	return !slices.ContainsFunc(a.parts(), func(p *string) bool { return *p != "" })
}

// Lines is the address as it is written on an envelope: the street lines,
// then the city, region and postcode, then the country's name.
func (a Address) Lines() []string {
	if a.Empty() {
		return nil
	}
	var out []string
	for _, s := range []string{a.Line1, a.Line2} {
		if s != "" {
			out = append(out, s)
		}
	}
	var place []string
	for _, s := range []string{a.City, a.Region, a.PostalCode} {
		if s != "" {
			place = append(place, s)
		}
	}
	if len(place) > 0 {
		out = append(out, strings.Join(place, " "))
	}
	if n := CountryName(a.Country); n != "" {
		out = append(out, n)
	}
	return out
}

// String is the address on one line, such as "Level 2, 10 Example St,
// Sydney NSW 2000, Australia", or "" when there is none.
func (a Address) String() string { return strings.Join(a.Lines(), ", ") }

// Name is the legal name, or the fallback (the brand's name) when none is
// set.
func (b Business) Name(fallback string) string {
	if b.LegalName != "" {
		return b.LegalName
	}
	return fallback
}

// Footer is the line an email or page footer carries: the legal name (or
// the fallback) and the address, or "" when the brand has no address.
func (b Business) Footer(fallback string) string {
	addr := b.Address.String()
	if addr == "" {
		return ""
	}
	if n := b.Name(fallback); n != "" {
		return n + ", " + addr
	}
	return addr
}

// CountryName is the English name of an ISO 3166 code, or "" when it is
// not one.
func CountryName(code string) string {
	for _, c := range Countries {
		if c[0] == code {
			return c[1]
		}
	}
	return ""
}

// Querier is what Load reads with: a pool or a transaction.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Load reads a brand's business details and its name. A brand that never
// saved any has empty ones; an unknown brand answers pgx.ErrNoRows.
func Load(ctx context.Context, q Querier, workspaceID, brandID string) (Business, string, error) {
	var b Business
	var name string
	var raw []byte
	err := q.QueryRow(ctx, `
		SELECT b.name, COALESCE(c.legal_name, ''), COALESCE(c.tax_id, ''), COALESCE(c.address, '{}')
		FROM brands b LEFT JOIN brand_context c ON c.brand_id = b.id
		WHERE b.workspace_id = $1 AND b.id = $2`, workspaceID, brandID).Scan(&name, &b.LegalName, &b.TaxID, &raw)
	if err != nil {
		return b, "", err
	}
	if err := json.Unmarshal(raw, &b.Address); err != nil {
		return b, name, err
	}
	return b, name, nil
}

// Vars adds brand_address and brand_legal_name to a sequence's variables
// when the brand has them, so a step that uses one waits (as with any
// missing variable) until the brand's address is filled in.
func Vars(ctx context.Context, q Querier, workspaceID string, brandID *string, vars map[string]string) error {
	if brandID == nil {
		return nil
	}
	b, name, err := Load(ctx, q, workspaceID, *brandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if a := b.Address.String(); a != "" {
		vars["brand_address"] = a
	}
	if n := b.Name(name); n != "" {
		vars["brand_legal_name"] = n
	}
	return nil
}
