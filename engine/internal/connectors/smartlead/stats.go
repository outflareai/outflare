package smartlead

import (
	"context"
	"net/url"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// MailboxDays reads each email account's campaign emails sent, replies
// and bounces (GET /analytics/mailbox/name-wise-health-metrics). Smartlead
// adds a date range up into one row per account, so each day is asked for
// on its own. Warm-up is not in it.
func (Smartlead) MailboxDays(ctx context.Context, cl *c.Client, cr c.Creds, emails []string, from, to time.Time) ([]c.MailboxDay, error) {
	want := map[string]bool{}
	for _, e := range emails {
		want[strings.ToLower(e)] = true
	}
	out := []c.MailboxDay{}
	for d := from.UTC().Truncate(24 * time.Hour); !d.After(to.UTC()); d = d.AddDate(0, 0, 1) {
		day := d.Format(time.DateOnly)
		var r struct {
			Data struct {
				Rows []struct {
					Email   string  `json:"email_account"`
					Sent    flexInt `json:"sent"`
					Replied flexInt `json:"replied"`
					Bounced flexInt `json:"bounced"`
				} `json:"email_health_metrics"`
			} `json:"data"`
		}
		q := url.Values{"start_date": {day}, "end_date": {day}, "timezone": {"UTC"}, "full_data": {"true"}}
		if err := get(ctx, cl, cr, "/analytics/mailbox/name-wise-health-metrics", q, &r); err != nil {
			return nil, err
		}
		for _, x := range r.Data.Rows {
			email := strings.ToLower(strings.TrimSpace(x.Email))
			if !want[email] {
				continue
			}
			out = append(out, c.MailboxDay{Email: email, Day: day, Sent: int(x.Sent), Replies: int(x.Replied), Bounced: int(x.Bounced)})
		}
	}
	return out, nil
}
