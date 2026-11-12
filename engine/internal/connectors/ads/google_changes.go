package ads

import (
	"context"
	"fmt"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Change events and auto-apply (plans/paid-ads/plan.md, sync and alerts):
// Google's change_event says what changed on an account, when, by whom
// and from where (the web client, the API, an automated rule, a
// recommendation Google applied on its own). Google keeps 30 days, wants
// a date filter inside them and a LIMIT of at most 10,000 rows, and shows
// a change within about three minutes. recommendation_subscription says
// which recommendation types the account lets Google apply by itself.

const (
	// changeEventLimit is Google's largest allowed LIMIT.
	changeEventLimit = 10000
	// changeEventDays is how far back Google keeps change events.
	changeEventDays = 30
)

// AdChangeEvents lists the account's changes since a time, oldest first.
// The user's email is hashed before it leaves this function; the address
// is never returned.
func (g GoogleAds) AdChangeEvents(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, since time.Time) ([]c.AdChangeEvent, error) {
	loc := googleLocation(acct.TimeZone)
	now := time.Now().In(loc)
	if oldest := now.AddDate(0, 0, -changeEventDays).Add(time.Hour); since.Before(oldest) {
		since = oldest
	}
	q := fmt.Sprintf("SELECT change_event.resource_name, change_event.change_date_time, change_event.user_email, change_event.client_type, "+
		"change_event.change_resource_type, change_event.resource_change_operation, change_event.changed_fields, change_event.campaign, "+
		"change_event.old_resource, change_event.new_resource FROM change_event "+
		"WHERE change_event.change_date_time >= '%s' AND change_event.change_date_time <= '%s' ORDER BY change_event.change_date_time LIMIT %d",
		since.In(loc).Format("2006-01-02 15:04:05"), now.Format("2006-01-02 15:04:05"), changeEventLimit)
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"], q)
	if err != nil {
		return nil, err
	}
	out := make([]c.AdChangeEvent, 0, len(rows))
	for _, r := range rows {
		e := r.ChangeEvent
		if e.ResourceName == "" {
			continue
		}
		at, err := time.ParseInLocation("2006-01-02 15:04:05", e.ChangeDateTime, loc)
		if err != nil {
			continue
		}
		ev := c.AdChangeEvent{ID: e.ResourceName, At: at.UTC(), UserHash: c.HashEmail(e.UserEmail), ClientType: e.ClientType,
			ResourceType: e.ChangeResourceType, Operation: e.ResourceChangeOperation, CampaignID: googleResourceTail(e.Campaign),
			Old: e.OldResource, New: e.NewResource}
		for _, f := range strings.Split(e.ChangedFields, ",") {
			if f = strings.TrimSpace(f); f != "" {
				ev.ChangedFields = append(ev.ChangedFields, f)
			}
		}
		out = append(out, ev)
	}
	return out, nil
}

// AutoApplied lists the recommendation types the account lets Google apply
// on its own.
func (g GoogleAds) AutoApplied(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]string, error) {
	rows, err := g.search(ctx, cl, creds, acct.ID, acct.Meta["login_customer_id"],
		"SELECT recommendation_subscription.type, recommendation_subscription.status FROM recommendation_subscription WHERE recommendation_subscription.status = 'ENABLED'")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if t := r.RecommendationSubscription.Type; t != "" {
			out = append(out, t)
		}
	}
	return out, nil
}

// googleLocation is an account's time zone, or UTC when it is unknown.
func googleLocation(tz string) *time.Location {
	if loc, err := time.LoadLocation(tz); err == nil && tz != "" {
		return loc
	}
	return time.UTC
}
