package linkorders

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ThreadMessage is one email between the brand and a donor: sent from an
// Outreach mailbox, or received in the Inbox.
type ThreadMessage struct {
	ID uuid.UUID `json:"id"`
	// Direction is sent or received.
	Direction string    `json:"direction"`
	At        time.Time `json:"at"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Subject   string    `json:"subject"`
	// Body is the start of the message, at most maxThreadBody characters.
	Body string `json:"body"`
	// Kind is the send status (sent, unknown) for a sent message, and
	// reply, auto_reply, bounce or other for a received one.
	Kind string `json:"kind"`
	// Category is the Inbox label of a reply (interested, question, ...), or "".
	Category   string     `json:"category"`
	SequenceID *uuid.UUID `json:"sequence_id"`
	// Amounts are prices written in a received reply ("$150", "120 EUR"),
	// to help find the agreed price; they are read from the text, not
	// confirmed.
	Amounts []string `json:"amounts"`
}

// OrderThread is the conversation with an order's donor.
type OrderThread struct {
	OrderID  uuid.UUID       `json:"order_id"`
	Domain   string          `json:"domain"`
	Messages []ThreadMessage `json:"messages"`
	// Addresses are the stored contact addresses that were matched besides
	// the site's own domain (such as the intermediary's, from the notes).
	Addresses []string `json:"addresses"`
}

const maxThreadBody = 4000

// maxThreadMessages caps the newest messages returned.
const maxThreadMessages = 100

var amountInText = regexp.MustCompile(`(?i)(?:[$€£]\s?\d[\d\s.,]*\d|[$€£]\s?\d|\b\d[\d\s.,]*\s?(?:usd|eur|gbp|dollars?|euros?)\b)`)

// AmountsIn lists the prices written in a text, in order, without repeats.
func AmountsIn(text string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, m := range amountInText.FindAllString(text, 20) {
		m = strings.Join(strings.Fields(m), " ")
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// GetOrderThread returns the brand's emails with an order's donor, newest
// first: Outreach sends to, and Inbox mail from, an address at the site's
// domain (or a subdomain) or one of the donor's stored contacts. Only
// this brand's mail is read. It reads stored mail; it sends nothing.
func GetOrderThread(ctx context.Context, q Querier, workspaceID, brandID, orderID uuid.UUID) (*OrderThread, error) {
	o, err := GetOrder(ctx, q, workspaceID, brandID, orderID)
	if err != nil {
		return nil, err
	}
	contacts, err := FindDonorContacts(ctx, q, workspaceID, brandID, o.DonorProfileID)
	if err != nil {
		return nil, err
	}
	domain := strings.ToLower(contacts.Domain)
	out := &OrderThread{OrderID: orderID, Domain: domain, Messages: []ThreadMessage{}, Addresses: []string{}}
	emails := []string{}
	for _, c := range contacts.Contacts {
		emails = append(emails, c.Email)
		if !c.AtDomain {
			out.Addresses = append(out.Addresses, c.Email)
		}
	}
	rows, err := q.Query(ctx, `
		SELECT id, direction, at, from_email, to_email, subject, body, kind, category, sequence_id
		FROM (
			SELECT m.id, 'sent' AS direction, COALESCE(m.sent_at, m.created_at) AS at,
				m.from_email, m.to_email, m.subject, left(m.body, $5) AS body, m.status AS kind,
				'' AS category, m.sequence_id
			FROM messages m
			WHERE m.workspace_id = $1 AND m.brand_id = $2
			  AND m.status IN ('sent', 'unknown')
			  AND (lower(m.to_email) = ANY ($3)
			       OR lower(split_part(m.to_email, '@', 2)) = $4
			       OR lower(split_part(m.to_email, '@', 2)) LIKE '%.' || $4)
			UNION ALL
			SELECT i.id, 'received', i.received_at,
				i.from_email, i.to_email, i.subject, left(i.body, $5), i.kind,
				COALESCE(i.category, ''), i.sequence_id
			FROM inbound_messages i
			WHERE i.workspace_id = $1 AND i.brand_id = $2
			  AND (lower(i.from_email) = ANY ($3)
			       OR lower(split_part(i.from_email, '@', 2)) = $4
			       OR lower(split_part(i.from_email, '@', 2)) LIKE '%.' || $4)
		) t
		ORDER BY at DESC, id
		LIMIT $6`, workspaceID, brandID, emails, domain, maxThreadBody, maxThreadMessages)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var m ThreadMessage
		if err := rows.Scan(&m.ID, &m.Direction, &m.At, &m.From, &m.To, &m.Subject, &m.Body, &m.Kind, &m.Category, &m.SequenceID); err != nil {
			return nil, err
		}
		m.Amounts = []string{}
		if m.Direction == "received" {
			m.Amounts = AmountsIn(m.Subject + "\n" + m.Body)
		}
		out.Messages = append(out.Messages, m)
	}
	return out, rows.Err()
}
