package outcomes

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/jobs"
)

// Attribution kinds: how a meeting or deal was tied to outreach.
const (
	AttrLead    = "lead"    // an attendee or contact is a lead
	AttrCompany = "company" // an attendee shares a lead's company domain
	AttrManual  = "manual"  // a person or an agent said so
	AttrNone    = "none"    // not tied to outreach
)

// Match is what an outcome was attributed to.
type Match struct {
	Attribution string
	LeadID      *string
	SequenceID  *string
	InboundID   *string
}

// freeMail domains never tie two people to one company.
var freeMail = map[string]bool{
	"gmail.com": true, "googlemail.com": true, "outlook.com": true, "hotmail.com": true, "live.com": true,
	"msn.com": true, "yahoo.com": true, "ymail.com": true, "icloud.com": true, "me.com": true, "mac.com": true,
	"aol.com": true, "proton.me": true, "protonmail.com": true, "gmx.com": true, "gmx.net": true,
	"mail.com": true, "zoho.com": true, "yandex.com": true, "fastmail.com": true, "hey.com": true,
	"qq.com": true, "163.com": true, "bigpond.com": true, "optusnet.com.au": true,
}

// IsFreeMail reports whether domain is a personal mail provider.
func IsFreeMail(domain string) bool { return freeMail[strings.ToLower(domain)] }

// FreeMailDomains lists the personal mail providers IsFreeMail knows,
// sorted.
func FreeMailDomains() []string {
	out := make([]string, 0, len(freeMail))
	for d := range freeMail {
		out = append(out, d)
	}
	slices.Sort(out)
	return out
}

func domainOf(email string) string {
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return ""
	}
	return strings.ToLower(email[at+1:])
}

// External drops the workspace's own addresses (its mailboxes, its sending
// domains and the extra domains given, such as the company's main domain)
// from a list of attendees, lowercased and de-duplicated.
func External(ctx context.Context, q pgx.Tx, ws string, emails []string, ownDomains []string) ([]string, error) {
	own := map[string]bool{}
	for _, d := range ownDomains {
		own[strings.ToLower(strings.TrimSpace(d))] = true
	}
	rows, err := q.Query(ctx, `SELECT domain FROM sending_domains WHERE workspace_id = $1
		UNION SELECT domain FROM mailboxes WHERE workspace_id = $1`, ws)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			rows.Close()
			return nil, err
		}
		own[d] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []string{}
	for _, e := range emails {
		e = strings.ToLower(strings.TrimSpace(e))
		d := domainOf(e)
		if d == "" || own[d] || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return out, nil
}

// Attribute ties outside attendees to outreach. A lead whose address
// matches wins; otherwise a lead at the same company domain (never a
// personal mail domain). The sequence is the one that last emailed that
// lead before at, and the reply is the lead's last reply before at.
func Attribute(ctx context.Context, q jobs.Querier, ws string, emails []string, at time.Time) (Match, error) {
	m := Match{Attribution: AttrNone}
	if len(emails) == 0 {
		return m, nil
	}
	var lead string
	err := q.QueryRow(ctx, `
		SELECT l.id FROM leads l
		LEFT JOIN LATERAL (SELECT max(sent_at) AS last FROM messages WHERE lead_id = l.id AND status = 'sent' AND sent_at <= $3) x ON true
		WHERE l.workspace_id = $1 AND lower(l.email) = ANY($2)
		ORDER BY x.last DESC NULLS LAST LIMIT 1`, ws, emails, at).Scan(&lead)
	if err == nil {
		m.Attribution = AttrLead
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return m, err
	} else {
		domains := []string{}
		for _, e := range emails {
			if d := domainOf(e); d != "" && !IsFreeMail(d) {
				domains = append(domains, d)
			}
		}
		if len(domains) == 0 {
			return m, nil
		}
		// Only a lead that was actually emailed counts for a company match:
		// a company being on a list is not enough to claim the meeting.
		err = q.QueryRow(ctx, `
			SELECT l.id FROM leads l
			JOIN LATERAL (SELECT max(sent_at) AS last FROM messages WHERE lead_id = l.id AND status = 'sent' AND sent_at <= $3) x ON x.last IS NOT NULL
			WHERE l.workspace_id = $1 AND l.domain = ANY($2)
			ORDER BY x.last DESC LIMIT 1`, ws, domains, at).Scan(&lead)
		if errors.Is(err, pgx.ErrNoRows) {
			return m, nil
		}
		if err != nil {
			return m, err
		}
		m.Attribution = AttrCompany
	}
	m.LeadID = &lead
	if err := fillFromLead(ctx, q, ws, &m, at); err != nil {
		return m, err
	}
	return m, nil
}

// fillFromLead sets the sequence and reply for m.LeadID.
func fillFromLead(ctx context.Context, q jobs.Querier, ws string, m *Match, at time.Time) error {
	var seq, inbound *string
	err := q.QueryRow(ctx, `
		SELECT
			(SELECT sequence_id FROM messages WHERE workspace_id = $1 AND lead_id = $2 AND status = 'sent' AND sent_at <= $3
				AND sequence_id IS NOT NULL ORDER BY sent_at DESC LIMIT 1),
			(SELECT id FROM inbound_messages WHERE workspace_id = $1 AND lead_id = $2 AND kind = 'reply' AND received_at <= $3
				ORDER BY received_at DESC LIMIT 1)`, ws, *m.LeadID, at).Scan(&seq, &inbound)
	if err != nil {
		return err
	}
	if seq == nil {
		// Booked before any send was recorded (or the sends were imported):
		// fall back to the newest enrollment.
		err = q.QueryRow(ctx, `SELECT sequence_id FROM enrollments WHERE workspace_id = $1 AND lead_id = $2
			ORDER BY created_at DESC LIMIT 1`, ws, *m.LeadID).Scan(&seq)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	m.SequenceID, m.InboundID = seq, inbound
	return nil
}

// AttributeLead ties an outcome to a known lead, e.g. a CRM contact that
// matched a lead by email.
func AttributeLead(ctx context.Context, q jobs.Querier, ws, leadID string, at time.Time) (Match, error) {
	m := Match{Attribution: AttrLead, LeadID: &leadID}
	return m, fillFromLead(ctx, q, ws, &m, at)
}
