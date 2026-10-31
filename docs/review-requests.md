# Review requests

Select a brand and open **Reviews** to configure email invitations. **Settings** holds one review link per platform, sender defaults, and a shared frequency policy (90 days by default, once ever, or no additional review limit; across the brand or per platform).

**Campaigns** supports a one-time customer list, rule audience, or selected records, and ongoing workflow configurations. Choose a starter email, edit the message, select destinations, preview eligibility, send a test, and launch now or schedule. One email is sent per request; there are no reminders. A scheduled campaign captures its audience at launch. Request content and destination URLs are snapshotted; changes affect future workflow requests. Existing lifecycle providers, consent, suppressions, caps, quiet hours, unsubscribe handling, and sending policy apply. Every third-party connection belongs to the workspace.

For automation, activate a workflow campaign, then add **Request a review** to an existing journey and choose that campaign. Configure triggers, delays, approvals and conditions in the existing workflow. A journey and campaign must belong to the same brand. Skipped actions retain a reason. New sentiment analysis is outside this feature.

Pause stops pending invitations; emails already being handed to the provider cannot be recalled. Resume continues remaining invitations. Archive stops pending requests and retains history. Duplicate a launched one-time campaign to change its audience or message. Pause a workflow campaign before editing its future requests.

**Requests** shows customer, campaign, destinations, send state, skip/failure reasons and review-link clicks. Date and campaign filters also apply to the report. Only clicks on configured review links count; unsubscribe links and likely security scanners are excluded. Counts are requests with a click, while destination reports count distinct customers per platform. A click does not prove a review was submitted. Existing public review monitoring remains available for Google, Trustpilot, G2 and Capterra; current integrations do not provide reliable request-to-review attribution.

The catalog covers 20 destinations. Yelp discourages solicitation; Zocdoc, OpenTable, Booking.com and Expedia require platform eligibility flows. Those destinations can be saved for reference but cannot be selected for email invitations. The UI distinguishes invitation support from available monitoring. Invite genuine customers consistently, using neutral wording and the destination's rules.

API operations live under `/v1/brands/{brand}/review-requests`: settings, campaigns, campaign preview/test/action, and paginated request history with an aggregate report. Reads require `emails.read`, configuration requires `emails.manage`, and activation, pause/resume/archive/duplicate and test sends require `emails.send`. Brand access is enforced independently.
