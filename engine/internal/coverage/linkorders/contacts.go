package linkorders

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/leads"
)

// Where a stored donor contact came from.
const (
	// ContactSourcePerson is a person at the site's company, such as a
	// contact a link-building check found or someone added by hand.
	ContactSourcePerson = "person"
	// ContactSourceLinkCheck is an address a link-building check read on
	// the site's own pages (the page, a contact page, or the author).
	ContactSourceLinkCheck = "link_check"
	// ContactSourceLead is an Outreach lead with an address at the domain.
	ContactSourceLead = "lead"
	// ContactSourceNotes is an address written in the donor's notes.
	ContactSourceNotes = "notes"
)

// DonorContact is an email address already stored for a donor's site.
type DonorContact struct {
	Email string `json:"email"`
	Name  string `json:"name"`
	// Source is person, link_check, lead or notes; the strongest wins
	// when one address is stored in several places.
	Source   string     `json:"source"`
	RecordID *uuid.UUID `json:"record_id"`
	LeadID   *uuid.UUID `json:"lead_id"`
	// AtDomain is whether the address is at the site's own domain (an
	// intermediary's address in the notes is not).
	AtDomain bool `json:"at_domain"`
	// Suppressed is whether the brand must not email it.
	Suppressed bool `json:"suppressed"`
	// Contacted is whether it was ever added to an Outreach sequence.
	Contacted bool `json:"contacted"`
}

// DonorContacts is what "Find email" shows for one catalog row.
type DonorContacts struct {
	DonorID  uuid.UUID      `json:"donor_id"`
	Domain   string         `json:"domain"`
	Contacts []DonorContact `json:"contacts"`
	// Searched names the stored data that was looked through, so an empty
	// answer says where nothing was found.
	Searched []string `json:"searched"`
}

var emailInText = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`)

var contactRank = map[string]int{ContactSourcePerson: 0, ContactSourceLinkCheck: 1, ContactSourceLead: 2, ContactSourceNotes: 3}

// FindDonorContacts returns the email addresses already stored for a
// donor's site: people at its company record, contacts a link-building
// check found there, Outreach leads at the domain, and addresses in the
// donor's or publisher's notes. It reads only stored data: it never looks
// an address up on the web or with a paid provider, so it costs nothing,
// and an empty list means nothing is stored yet.
func FindDonorContacts(ctx context.Context, q Querier, workspaceID, brandID, donorID uuid.UUID) (*DonorContacts, error) {
	var domain, notes string
	var companyID uuid.UUID
	err := q.QueryRow(ctx, `
		SELECT p.domain, p.record_id, d.notes || E'\n' || p.notes
		FROM coverage_donor_profiles d
		JOIN coverage_publishers p ON p.id = d.publisher_id
		WHERE d.id = $1 AND d.workspace_id = $2 AND d.brand_id = $3`, donorID, workspaceID, brandID).Scan(&domain, &companyID, &notes)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	out := &DonorContacts{DonorID: donorID, Domain: domain, Contacts: []DonorContact{},
		Searched: []string{"people at the site's company", "contacts from link-building checks", "Outreach leads at " + domain, "the donor's notes"}}
	byEmail := map[string]int{}
	add := func(c DonorContact) {
		c.Email = strings.ToLower(strings.TrimSpace(c.Email))
		if c.Email == "" || !strings.Contains(c.Email, "@") {
			return
		}
		c.AtDomain = AtDomain(c.Email, domain)
		if i, ok := byEmail[c.Email]; ok {
			cur := &out.Contacts[i]
			if contactRank[c.Source] < contactRank[cur.Source] {
				cur.Source = c.Source
			}
			if cur.Name == "" {
				cur.Name = c.Name
			}
			if cur.RecordID == nil {
				cur.RecordID = c.RecordID
			}
			if cur.LeadID == nil {
				cur.LeadID = c.LeadID
			}
			return
		}
		byEmail[c.Email] = len(out.Contacts)
		out.Contacts = append(out.Contacts, c)
	}

	// The company records for the domain: the publisher's own and any
	// other with the same domain identity (a link prospect's website).
	// People linked to them, and people whose address is at the domain.
	rows, err := q.Query(ctx, `
		WITH companies AS (
			SELECT $3::uuid AS id
			UNION
			SELECT i.record_id FROM record_identities i
			WHERE i.workspace_id = $1 AND i.namespace = 'domain' AND i.value = $2 AND i.valid_to IS NULL
		), people AS (
			SELECT rr.from_id AS id FROM record_relationships rr
			WHERE rr.workspace_id = $1 AND rr.to_id IN (SELECT id FROM companies) AND rr.kind = 'companies' AND rr.valid_to IS NULL
			UNION
			SELECT i.record_id FROM record_identities i
			WHERE i.workspace_id = $1 AND i.namespace = 'email' AND i.valid_to IS NULL
				AND (split_part(i.value, '@', 2) = $2 OR split_part(i.value, '@', 2) LIKE '%.' || $2)
		)
		SELECT r.id, r.name, i.value,
			EXISTS (SELECT 1 FROM link_prospect_contacts c WHERE c.workspace_id = $1 AND c.record_id = r.id)
		FROM records r
		JOIN record_identities i ON i.record_id = r.id AND i.namespace = 'email' AND i.valid_to IS NULL
		WHERE r.workspace_id = $1 AND r.id IN (SELECT id FROM people) AND r.merged_into IS NULL
			AND (r.brand_id IS NULL OR r.brand_id = $4)
		ORDER BY i.verified DESC, i.created_at
		LIMIT 50`, workspaceID, domain, companyID, brandID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uuid.UUID
		var name, email string
		var checked bool
		if err := rows.Scan(&id, &name, &email, &checked); err != nil {
			rows.Close()
			return nil, err
		}
		src := ContactSourcePerson
		if checked {
			src = ContactSourceLinkCheck
		}
		add(DonorContact{Email: email, Name: name, Source: src, RecordID: &id})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// The brand's Outreach leads at the domain.
	rows, err = q.Query(ctx, `
		SELECT l.id, l.email, trim(l.first_name || ' ' || l.last_name), l.record_id
		FROM leads l
		WHERE l.workspace_id = $1 AND l.brand_id IS NOT DISTINCT FROM $3 AND (l.domain = $2 OR l.domain LIKE '%.' || $2)
		ORDER BY l.created_at
		LIMIT 50`, workspaceID, domain, brandID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id uuid.UUID
		var rec *uuid.UUID
		var email, name string
		if err := rows.Scan(&id, &email, &name, &rec); err != nil {
			rows.Close()
			return nil, err
		}
		add(DonorContact{Email: email, Name: name, Source: ContactSourceLead, LeadID: &id, RecordID: rec})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Addresses written in the notes, such as the intermediary's.
	for _, e := range emailInText.FindAllString(notes, 20) {
		add(DonorContact{Email: e, Source: ContactSourceNotes})
	}

	if len(out.Contacts) == 0 {
		return out, nil
	}
	emails := make([]string, len(out.Contacts))
	for i, c := range out.Contacts {
		emails[i] = c.Email
	}
	// Suppressed for this brand (its own entries and every brand's), and
	// ever added to a sequence: the same checks Outreach makes.
	rows, err = q.Query(ctx, `
		SELECT e,
			EXISTS (`+leads.SuppressedInSQL("$1", "$2::uuid", "e")+`),
			EXISTS (SELECT 1 FROM leads l JOIN enrollments n ON n.lead_id = l.id
				WHERE l.workspace_id = $1 AND l.brand_id IS NOT DISTINCT FROM $2 AND lower(l.email) = e)
		FROM unnest($3::text[]) AS e`, workspaceID, brandID, emails)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e string
		var sup, sent bool
		if err := rows.Scan(&e, &sup, &sent); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := byEmail[e]; ok {
			out.Contacts[i].Suppressed, out.Contacts[i].Contacted = sup, sent
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The site's own addresses first, then by how sure the source is.
	slices.SortStableFunc(out.Contacts, func(a, b DonorContact) int {
		if a.AtDomain != b.AtDomain {
			if a.AtDomain {
				return -1
			}
			return 1
		}
		return contactRank[a.Source] - contactRank[b.Source]
	})
	return out, nil
}

// AtDomain reports whether email is at domain or one of its subdomains.
func AtDomain(email, domain string) bool {
	at := strings.LastIndex(email, "@")
	if at < 0 || domain == "" {
		return false
	}
	host := strings.ToLower(email[at+1:])
	domain = strings.ToLower(domain)
	return host == domain || strings.HasSuffix(host, "."+domain)
}
