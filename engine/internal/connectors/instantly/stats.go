package instantly

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// statsBatch is how many accounts one daily analytics call may name.
const statsBatch = 200

// MailboxDays reads each account's campaign emails sent, replies and
// bounces per day (GET /accounts/analytics/daily). Warm-up is not in it.
func (Instantly) MailboxDays(ctx context.Context, cl *c.Client, cr c.Creds, emails []string, from, to time.Time) ([]c.MailboxDay, error) {
	out := []c.MailboxDay{}
	for len(emails) > 0 {
		n := min(len(emails), statsBatch)
		q := url.Values{"start_date": {from.UTC().Format(time.DateOnly)}, "end_date": {to.UTC().Format(time.DateOnly)}, "emails": emails[:n]}
		emails = emails[n:]
		var rows []struct {
			Date    string `json:"date"`
			Email   string `json:"email_account"`
			Sent    int    `json:"sent"`
			Bounced int    `json:"bounced"`
			Replies int    `json:"unique_replies"`
		}
		if err := call(ctx, cl, cr, http.MethodGet, "/accounts/analytics/daily", q, nil, &rows); err != nil {
			return nil, err
		}
		for _, r := range rows {
			if _, err := time.Parse(time.DateOnly, r.Date); err != nil || r.Email == "" {
				continue
			}
			out = append(out, c.MailboxDay{Email: strings.ToLower(r.Email), Day: r.Date, Sent: r.Sent, Replies: r.Replies, Bounced: r.Bounced})
		}
	}
	return out, nil
}
