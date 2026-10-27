// Package outcomes ties outreach to what it produced and what it cost:
// meetings booked, deals in the CRM, and a ledger of spend. The ledger
// records cost; it never bills. Customers pay their providers directly.
package outcomes

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/jobs"
)

// Spend categories.
const (
	CategoryData         = "data"         // search, enrichment, email finding
	CategoryVerification = "verification" // email verification credits
	CategoryMailboxes    = "mailboxes"    // mailbox seats
	CategoryDomains      = "domains"      // registrations and renewals
	CategorySending      = "sending"      // sending infrastructure, warm-up tools
	CategorySoftware     = "software"     // other subscriptions
	CategoryPeople       = "people"       // SDR or agency time
	CategoryAds          = "ads"
	CategoryOther        = "other"
)

// Categories lists every spend category, in display order.
var Categories = []string{CategoryData, CategoryVerification, CategoryMailboxes, CategoryDomains,
	CategorySending, CategorySoftware, CategoryPeople, CategoryAds, CategoryOther}

// Spend sources.
const (
	SourceAuto   = "auto"   // recorded by the engine or a connector
	SourceManual = "manual" // typed in by a person or an agent
	SourceImport = "import" // from a CSV
)

// Entry is one charge in the ledger. Set AmountCents when the money is
// known; otherwise set Units and UnitName and reports price them with the
// workspace's rates. Set Ref to make recording idempotent.
type Entry struct {
	ID            string    `json:"id"`
	WorkspaceID   string    `json:"-"`
	ProjectID     *string   `json:"project_id"`
	SequenceID    *string   `json:"sequence_id"`
	MissionID     *string   `json:"mission_id"`
	MailboxID     *string   `json:"mailbox_id"`
	Domain        string    `json:"domain"`
	Category      string    `json:"category"`
	Provider      string    `json:"provider"`
	IntegrationID *string   `json:"integration_id"`
	Units         *float64  `json:"units"`
	UnitName      string    `json:"unit_name"`
	AmountCents   *int64    `json:"amount_cents"`
	Currency      string    `json:"currency"`
	OccurredAt    time.Time `json:"occurred_at"`
	Source        string    `json:"source"`
	Ref           *string   `json:"ref"`
	Note          string    `json:"note"`
	ActorID       *string   `json:"actor_id"`
	CreatedAt     time.Time `json:"created_at"`
}

// ErrInvalid wraps a validation failure worded for the person who sent it.
type ErrInvalid struct{ Msg string }

func (e *ErrInvalid) Error() string { return e.Msg }

func invalid(format string, a ...any) error { return &ErrInvalid{Msg: fmt.Sprintf(format, a...)} }

// IsInvalid reports whether err is a validation failure.
func IsInvalid(err error) bool {
	var e *ErrInvalid
	return errors.As(err, &e)
}

var currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)

func validCategory(c string) bool {
	for _, x := range Categories {
		if x == c {
			return true
		}
	}
	return false
}

// normalize checks an entry and fills defaults.
func (e *Entry) normalize() error {
	e.Category = strings.TrimSpace(e.Category)
	if !validCategory(e.Category) {
		return invalid("category must be one of %s", strings.Join(Categories, ", "))
	}
	e.Provider = strings.TrimSpace(e.Provider)
	e.UnitName = strings.TrimSpace(e.UnitName)
	e.Note = strings.TrimSpace(e.Note)
	e.Domain = strings.ToLower(strings.TrimSpace(e.Domain))
	if len(e.Provider) > 200 || len(e.UnitName) > 50 || len(e.Note) > 2000 {
		return invalid("provider, unit_name or note is too long")
	}
	if e.AmountCents == nil && e.Units == nil {
		return invalid("give amount_cents, or units with a unit_name")
	}
	if e.AmountCents != nil && *e.AmountCents < 0 {
		return invalid("amount_cents cannot be negative; record a refund as a note on the original entry")
	}
	if e.Units != nil {
		if *e.Units < 0 {
			return invalid("units cannot be negative")
		}
		if e.UnitName == "" {
			return invalid("unit_name is required with units, e.g. credit")
		}
	}
	if e.Currency == "" {
		e.Currency = "USD"
	}
	e.Currency = strings.ToUpper(e.Currency)
	if !currencyRe.MatchString(e.Currency) {
		return invalid("currency must be a three-letter code such as USD")
	}
	if e.Source == "" {
		e.Source = SourceManual
	}
	if e.Source != SourceAuto && e.Source != SourceManual && e.Source != SourceImport {
		return invalid("source must be auto, manual or import")
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now()
	}
	if e.OccurredAt.After(time.Now().Add(24 * time.Hour)) {
		return invalid("occurred_at is in the future")
	}
	if e.Ref != nil {
		r := strings.TrimSpace(*e.Ref)
		if r == "" {
			e.Ref = nil
		} else if len(r) > 200 {
			return invalid("ref is too long")
		} else {
			e.Ref = &r
		}
	}
	return nil
}

const entryCols = `id, project_id, sequence_id, mission_id, mailbox_id, domain, category, provider, integration_id,
	units::float8, unit_name, amount_cents, currency, occurred_at, source, ref, note, actor_id, created_at`

func scanEntry(row pgx.Row, ws string) (Entry, error) {
	e := Entry{WorkspaceID: ws}
	err := row.Scan(&e.ID, &e.ProjectID, &e.SequenceID, &e.MissionID, &e.MailboxID, &e.Domain, &e.Category, &e.Provider,
		&e.IntegrationID, &e.Units, &e.UnitName, &e.AmountCents, &e.Currency, &e.OccurredAt, &e.Source, &e.Ref, &e.Note,
		&e.ActorID, &e.CreatedAt)
	return e, err
}

// Record adds a charge to the ledger and returns it. With a Ref already in
// the ledger it changes nothing and returns the existing entry, so callers
// can record from a retried job without double counting.
//
// When ProjectID is empty it is filled from the sequence, mailbox or
// domain, in that order.
//
// Pass the transaction that made the change that cost money, so the charge
// commits or rolls back with it.
func Record(ctx context.Context, q jobs.Querier, e Entry) (Entry, bool, error) {
	if e.WorkspaceID == "" {
		return Entry{}, false, errors.New("outcomes: Record needs a workspace")
	}
	if err := e.normalize(); err != nil {
		return Entry{}, false, err
	}
	created, err := scanEntry(q.QueryRow(ctx, `
		INSERT INTO spend_entries (workspace_id, project_id, sequence_id, mission_id, mailbox_id, domain, category, provider,
			integration_id, units, unit_name, amount_cents, currency, occurred_at, source, ref, note, actor_id)
		SELECT $1, COALESCE($2::uuid,
				(SELECT project_id FROM sequences WHERE id = $3 AND workspace_id = $1),
				(SELECT project_id FROM mailboxes WHERE id = $5 AND workspace_id = $1),
				(SELECT project_id FROM sending_domains WHERE domain = $6 AND workspace_id = $1)),
			$3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18
		WHERE ($2::uuid IS NULL OR EXISTS (SELECT 1 FROM projects WHERE id = $2 AND workspace_id = $1))
		  AND ($3::uuid IS NULL OR EXISTS (SELECT 1 FROM sequences WHERE id = $3 AND workspace_id = $1))
		  AND ($5::uuid IS NULL OR EXISTS (SELECT 1 FROM mailboxes WHERE id = $5 AND workspace_id = $1))
		  AND ($9::uuid IS NULL OR EXISTS (SELECT 1 FROM integrations WHERE id = $9 AND workspace_id = $1))
		ON CONFLICT (workspace_id, ref) WHERE ref IS NOT NULL DO NOTHING
		RETURNING `+entryCols,
		e.WorkspaceID, e.ProjectID, e.SequenceID, e.MissionID, e.MailboxID, e.Domain, e.Category, e.Provider,
		e.IntegrationID, e.Units, e.UnitName, e.AmountCents, e.Currency, e.OccurredAt, e.Source, e.Ref, e.Note, e.ActorID), e.WorkspaceID)
	if err == nil {
		return created, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, false, err
	}
	if e.Ref != nil {
		existing, err := scanEntry(q.QueryRow(ctx, `SELECT `+entryCols+` FROM spend_entries WHERE workspace_id = $1 AND ref = $2`,
			e.WorkspaceID, *e.Ref), e.WorkspaceID)
		if err == nil {
			return existing, false, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Entry{}, false, err
		}
	}
	return Entry{}, false, invalid("the project, sequence, mailbox or integration is not in this workspace")
}

func isUniqueViolation(err error) bool {
	var pg interface{ SQLState() string }
	return errors.As(err, &pg) && pg.SQLState() == "23505"
}
