package stories

import (
	"context"

	"github.com/outflareai/outflare/engine/internal/alerts"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// AlertQuoteDecided tells the team an expert decided on a quote through an
// approval link: approved it, suggested other words or declined.
const AlertQuoteDecided = "story_quote.decided"

// AlertQuoteEmailFailed tells the person who asked that the email asking
// the expert to approve a quote didn't go out.
const AlertQuoteEmailFailed = "story_quote.approval_failed"

func init() {
	alerts.Register(alerts.Kind{ID: AlertQuoteDecided, Title: "Expert decided on a quote",
		Description: "A PR expert approved a quote, suggested other words or declined it through an approval link. It goes to the person who asked, else the owners and admins.",
		Scope:       auth.ScopeSEORead, Describe: describeDecided})
	alerts.Register(alerts.Kind{ID: AlertQuoteEmailFailed, Title: "Quote approval email not sent",
		Description: "The email asking a PR expert to approve a quote couldn't go out: the send policy stopped it, or the mailbox couldn't deliver it. It goes to the person who asked, else the owners and admins.",
		Scope:       auth.ScopeSEORead, Describe: describeDecided})
}

func describeDecided(ctx context.Context, q jobs.Querier, a alerts.Row) (alerts.View, error) {
	var name, title string
	if err := q.QueryRow(ctx, `SELECT r.name, s.title FROM story_quotes sq
		JOIN stories s ON s.id = sq.story_id JOIN pr_experts x ON x.id = sq.expert_id JOIN records r ON r.id = x.record_id
		WHERE sq.id = $1 AND sq.workspace_id = $2`, a.TargetID, a.WorkspaceID).Scan(&name, &title); err != nil {
		return alerts.View{}, err
	}
	if name == "" {
		name = "Your expert"
	}
	v := alerts.View{Path: "stories"}
	if a.Kind == AlertQuoteEmailFailed {
		reason, _ := a.Data["reason"].(string)
		v.Title, v.Text = "The approval email to "+name+" didn't go out", "For \""+title+"\" ("+reason+"). Copy the link and send it yourself, or fix the sequence and ask again."
		return v, nil
	}
	switch decision, _ := a.Data["decision"].(string); decision {
	case "approved":
		v.Title, v.Text = name+" approved a quote", "For \""+title+"\". It can go in the release as it stands."
	case "changes_suggested":
		v.Title, v.Text = name+" suggested other words", "For \""+title+"\". Review their words, then ask them again."
	default:
		v.Title, v.Text = name+" declined a quote", "For \""+title+"\". Leave it out of the release, or draft another."
	}
	return v, nil
}
