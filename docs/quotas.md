# Quotas

A quota lets an extension refuse work a workspace isn't allowed more of, before the engine does it. A server without one, such as every self-hosted server, allows everything, and nothing on this page applies to it.

## What is checked

Before each of these, the engine asks the quota whether the workspace may have `n` more of a resource:

| Resource | Asked before | `n` | When refused |
| --- | --- | --- | --- |
| `brands` | `POST .../brands` | 1 | `402 quota_exceeded`; the brand isn't added |
| `mailboxes` | adding mailboxes (`POST .../mailboxes`, imports, admin connections, the EmailBison-compatible API) and a mailbox sign-in | the rows sent, the most that could be added | `402 quota_exceeded`; a sign-in stays connected and shows the reason, and signing in again adds the mailbox once it may |
| `tracking_domains` | `POST .../tracking-domains` | 1 | `402 quota_exceeded`; the domain isn't added |
| `records` | `POST .../records`, and each page of a CRM import | 1, or the page's size | `402 quota_exceeded`; an import ends as failed with the reason and error code `quota_exceeded` |
| `emails_sent` | each sequence email, after its sending window | 1 | the email waits an hour and is tried again; it is never failed or blocked, and the enrollment's status reason is `quota_exceeded` meanwhile |
| `tracked_events` | storing events: `POST .../customer-events`, `POST /v1/brands/{brand}/events` and the website snippet (`POST /v1/site/events`) | the events in the request | `402 quota_exceeded`; none of the request's events are stored, and the website snippet drops them |
| `tracker_clicks` | writing a batch of [redirect link](campaign-links.md#redirect-links) clicks, a moment after the redirects | the batch's clicks for the workspace, leaving out likely bots | the person was already sent on; the clicks aren't stored |

Records the engine makes on its own to keep data linked (a person from a website event, a deal from a reply) are not checked, so no activity is lost. Events are checked once, as they arrive: an event the brand inbox already took is stored even if the quota changes before a worker reaches it, and replaying held events isn't checked again. Outflare's own product events (the operator's events workspace) aren't checked.

The `402` body is the usual error with code `quota_exceeded` and the quota's message, which says what stopped and how to get more.

## For extensions

```go
type plans struct{ /* ... */ }

func (p *plans) Allow(ctx context.Context, workspaceID, resource string, n int64) error {
	if over(workspaceID, resource) {
		return &outflare.QuotaError{Resource: resource, Message: "This workspace can't add more " + resource + " on its plan. Upgrade under Settings."}
	}
	return nil
}

func (e Ext) Setup(ctx context.Context, h *outflare.Host) error {
	h.SetQuota(&plans{})
	return nil
}
```

- Return nil to allow, a `*outflare.QuotaError` to refuse, or any other error when the check itself fails. A failed check is logged, recorded as a `quota.check_failed` event and allowed: a broken checker never stops a workspace's work.
- It is asked often (every email sent), so answer from memory or one quick query.
- The resources are `outflare.QuotaBrands`, `QuotaMailboxes`, `QuotaTrackingDomains`, `QuotaRecords`, `QuotaEmailsSent`, `QuotaTrackedEvents` and `QuotaTrackerClicks`.

## Monitoring

`outflare_quota_checks_total` (by `resource` and `outcome`: `allowed`, `refused`, `error`) on the Quotas row of the dashboard. Alerts: checks failing for 15 minutes (warning), and over 1,000 refusals an hour for one resource (info) ([monitoring.md](monitoring.md)).
