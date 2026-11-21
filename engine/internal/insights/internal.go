package insights

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// internalPeople is who a brand leaves out of its reports: its own team and
// its test accounts, by email address or email domain (the brand's
// internal_people row, when it is on). A report then counts no row of a
// known person with such an address. Visitors nobody has identified are
// never left out: there is nothing to tell them by.
type internalPeople struct {
	emails, domains []string
	// The placeholders of the two lists, added to a query the first time
	// it needs them.
	pEmails, pDomains string
}

// leaveOutInternal loads the brand's internal and test people into a, so
// every row source built with a leaves them out.
func (a *args) leaveOutInternal(ctx context.Context, q Querier, ws, brand string) error {
	// The person's and company's fields a query may read (person:<key>,
	// company:<key>), loaded with the rest of what every query needs.
	a.records = &recordFields{person: map[string]string{}, company: map[string]string{}}
	if BreakdownFields != nil {
		var err error
		if a.records.person, err = BreakdownFields(ctx, q, ws, brand, "person"); err != nil {
			return err
		}
		if a.records.company, err = BreakdownFields(ctx, q, ws, brand, "account"); err != nil {
			return err
		}
	}
	var in internalPeople
	err := q.QueryRow(ctx, `SELECT emails, domains FROM internal_people WHERE brand_id = $1 AND enabled`, brand).Scan(&in.emails, &in.domains)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	a.internal = in
	return nil
}

// notInternal is the condition that the person in column (a record id) is
// not one of the brand's internal or test people, or "" when the brand
// leaves nobody out.
func (a *args) notInternal(column string) string {
	in := &a.internal
	if len(in.emails) == 0 && len(in.domains) == 0 {
		return ""
	}
	if in.pEmails == "" {
		in.pEmails, in.pDomains = a.add(in.emails), a.add(in.domains)
	}
	return `NOT EXISTS (SELECT 1 FROM record_identities ri WHERE ri.record_id = ` + column + ` AND ri.namespace = 'email' AND ri.valid_to IS NULL
		AND (lower(ri.value) = ANY (` + in.pEmails + `::text[]) OR split_part(lower(ri.value), '@', 2) = ANY (` + in.pDomains + `::text[])))`
}
