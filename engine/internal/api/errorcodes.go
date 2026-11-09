package api

import "net/http"

// errorCode is one entry in the catalog of error codes. Codes are part of
// the contract: clients branch on them, so a code is only ever added, never
// renamed or reused for something else. Field errors are invalid_request
// with param naming the field; a new code is only for a case a client
// would handle differently.
type errorCode struct {
	Code        string `json:"code"`
	Statuses    []int  `json:"statuses"`
	Description string `json:"description"`
	Retry       bool   `json:"retry"`
}

// errorCodes lists every code the API answers. A test checks it against
// the codes the handlers use.
var errorCodes = []errorCode{
	// Any endpoint.
	{"invalid_request", []int{400, 413, 415}, "The request is invalid: a field is missing, malformed or out of range, the body is too large, or the content type is wrong. The message says why and param names the field.", false},
	{"invalid_cursor", []int{400}, "The cursor was not one this API gave out. Pass next_cursor from the previous page unchanged.", false},
	{"unauthenticated", []int{401}, "No credentials, or the token or session is not valid.", false},
	{"forbidden", []int{403}, "The token or member lacks the scope or role this needs. The message names it.", false},
	{"not_found", []int{404}, "Not found, including things in workspaces you cannot see.", false},
	{"conflict", []int{409}, "The change conflicts with the current state, such as a duplicate name. The message says what.", false},
	{"slug_taken", []int{409}, "Another workspace already uses this slug. GET .../slug-availability checks one before saving.", false},
	{"test_workspace", []int{409}, "Not possible in a test workspace, such as buying domains or mailboxes. Use a live workspace.", false},
	{"live_workspace", []int{409}, "Only possible in a test workspace, such as simulating a reply. Create one with POST /v1/workspaces and test: true.", false},
	{"idempotency_in_progress", []int{409}, "A request with this Idempotency-Key is still running. Retry shortly with the same key.", true},
	{"idempotency_key_reused", []int{422}, "This Idempotency-Key was already used for a different request.", false},
	{"already_suggested", []int{409}, "An answer to this question is already waiting for review, or a person rejected it.", false},
	{"already_decided", []int{409}, "Someone already approved or rejected this suggestion.", false},
	{"rate_limited", []int{429}, "Too many requests. Wait for Retry-After, then retry.", true},
	{"internal", []int{500}, "A server error. Quote the request_id.", true},
	{"client_closed", []int{499}, "Only in logs and events: the client closed the request before the answer, so no reply was sent.", false},
	{"unavailable", []int{503}, "A dependency is down or the server is starting. Retry later.", true},

	// Accounts and sign-in.
	{"invalid_credentials", []int{401}, "The email and password do not match.", false},
	{"already_set_up", []int{409}, "The server already has its first user.", false},
	{"last_owner", []int{409}, "A workspace must keep at least one owner.", false},
	{"invalid_invite", []int{404}, "The invite link is invalid, used or expired.", false},
	{"invalid_link", []int{404}, "The email link is invalid, used or expired. Ask for a new one.", false},
	{"wrong_account", []int{400}, "The invite is for a different email than the account you signed in with.", false},
	{"no_account", []int{404}, "No one has an account with that email yet.", false},
	{"email_failed", []int{502}, "The email could not be sent. Try again later.", true},
	{"passkeys_off", []int{404}, "Passkeys are off on this server until PUBLIC_URL is its https address.", false},
	{"passkey_expired", []int{400}, "The passkey ceremony took too long. Start again.", false},
	{"passkey_rejected", []int{400, 401}, "The passkey was not accepted.", false},
	{"too_many_passkeys", []int{409}, "The account already has the most passkeys allowed.", false},
	{"support_view_read_only", []int{403}, "You are viewing this account as staff (support access), which is read-only. End the view with DELETE /v1/me/support-view to make changes as yourself.", false},
	{"support_view_hidden", []int{403}, "Hidden while staff view this account (support access): inbox and email content never show in a view.", false},
	{"managed", []int{409}, "Usage stats are set by the server (an environment variable or the build), not here.", false},

	// Signing AI apps in with OAuth (/oauth/register, /oauth/token,
	// /oauth/revoke). These endpoints answer in OAuth's own shape,
	// {"error": code, "error_description": ...}, which OAuth clients expect.
	{"invalid_client", []int{400}, "OAuth: the app is not registered, or asked to send the person somewhere it did not register.", false},
	{"invalid_client_metadata", []int{400}, "OAuth: the app's registration is not valid JSON client metadata, or asks for grant types other than authorization_code and refresh_token.", false},
	{"invalid_redirect_uri", []int{400}, "OAuth: a redirect URI must use https, a loopback http address (localhost, 127.0.0.1) or an app's own scheme.", false},
	{"invalid_grant", []int{400}, "OAuth: the code or refresh token is not valid, was used already or expired, or the person who connected the app lost access. Connect the app again.", false},
	{"invalid_target", []int{400}, "OAuth: resource names another server than this one's /mcp.", false},
	{"unsupported_grant_type", []int{400}, "OAuth: grant_type must be authorization_code or refresh_token.", false},
	{"unsupported_response_type", []int{400}, "OAuth: response_type must be code.", false},
	{"access_denied", []int{400}, "OAuth: the person declined to connect the app.", false},
	{"server_error", []int{500}, "OAuth: a server error. Try again.", true},

	// Connectors and integrations.
	{"connector_auth", []int{422}, "The connected service refused the credentials. Reconnect it.", false},
	{"connector_invalid", []int{422}, "The connected service refused the request as invalid.", false},
	{"connector_quota", []int{429}, "The connected service's quota or rate limit is used up.", true},
	{"connector_unavailable", []int{502}, "The connected service could not be reached or failed.", true},
	{"usage_limit_reached", []int{409}, "A paid connection reached a daily or monthly usage limit set on its page under Connectors. The message says which; raise the limit or wait for the next day or month.", false},
	{"check_failed", []int{409}, "A story's release didn't pass the check before approval: a number without a sourced fact, a quote its expert hasn't approved, or no release. The story's last_check lists each problem.", false},
	{"connector_needed", []int{409}, "This needs a connector that is not connected yet. The message names it.", false},
	{"already_connected", []int{409}, "The connector is already connected in this workspace. Use the connection instead of asking for it again.", false},
	{"use_oauth", []int{400}, "This connector connects through its sign-in page, not a key.", false},
	{"built_in", []int{400}, "This connector is built in and needs no connection.", false},
	{"not_available", []int{400, 410}, "This connector is planned but cannot be connected yet, or the item is no longer available.", false},
	{"not_configured", []int{400, 404, 503}, "The server is not set up for this, such as an OAuth app or a background service.", false},
	{"not_importer", []int{400}, "This connection cannot import campaigns.", false},
	{"not_crm_importer", []int{400}, "This connection cannot import a CRM. Connect a CRM such as Attio.", false},
	{"not_a_supplier", []int{422}, "This connection is not a mailbox supplier.", false},
	{"push_unavailable", []int{422}, "The supplier cannot push mailboxes to this server: it does not support push, or the server has no public https address (PUBLIC_URL).", false},
	{"push_not_on", []int{409}, "Push is not on for this supplier. Turn it on with PATCH .../mailbox-sync {\"push\": true} and wait until push.state is on.", false},
	{"not_analytics", []int{400}, "This connection is not an analytics connection.", false},
	{"not_seo", []int{400}, "This connection is not an SEO connection.", false},
	{"pick_site", []int{400}, "The connection can read several sites; name one with site.", false},
	{"unknown_site", []int{400, 404}, "The connection cannot read that site.", false},
	{"no_domains", []int{400}, "The connection has no domains to snapshot.", false},
	{"quota_exceeded", []int{402}, "The workspace has used everything its plan allows of something (brands, mailboxes, tracking domains, customer records). The message says what and how to get more.", false},
	{"budget_reached", []int{402}, "An SEO connection reached its daily or monthly usage limit (its budget). Raise it in the SEO settings or on the connection's page under Connectors.", false},
	{"site_in_outreach", []int{409}, "Another link prospect on the same website is being pitched, or was declined recently (60 days). One site is pitched by one campaign at a time; pitch it later or pick another site.", false},
	{"platform_error", []int{502}, "The sending platform returned an error.", true},
	{"send_failed", []int{502}, "The email could not be sent through the mailbox.", true},
	{"bad_signature", []int{401}, "An inbound webhook's signature does not match.", false},
	{"link_expired", []int{410}, "The link for Outflare for desktop expired or was already used. Start linking again in the app.", false},
	{"app_signin_expired", []int{410}, "The sign-in to the Outflare desktop app expired or was already approved. Start again in the app.", false},
	{"app_signin_mismatch", []int{400}, "The code verifier doesn't match the sign-in's challenge: the code reached another app, or the app restarted. Start again in the app.", false},
	{"expired", []int{410}, "The connect link has expired. Ask for a new one.", false},
	{"cancelled", []int{410}, "The connect link was cancelled. Ask for a new one.", false},
	{"used", []int{410}, "The link was already used.", false},
	{"export_expired", []int{410}, "The privacy export expired (7 days after it was ready) and its file was deleted. Ask for a new one.", false},
	{"export_not_ready", []int{409}, "The privacy export is still being built, or failed (its error_code says why). Download it once its status is ready.", false},
	{"transaction_deleted", []int{409}, "A transaction with this source_id is in Recently deleted. Restore it there instead of sending it again; it can be sent again once it is purged.", false},
	{"nothing_to_restore", []int{409}, "Everything in this deletion was already restored. GET .../recently-deleted lists what can still be.", false},
	{"merge_not_reversible", []int{409}, "The merge was made before Outflare kept a record of what merges move, so it can't be undone. Split the records by hand.", false},
	{"merge_not_latest", []int{409}, "A later merge involves one of these records (the record kept was merged into another, or the merged one now leads elsewhere). Undo the later merge first.", false},
	{"merge_already_undone", []int{409}, "The merge was already undone.", false},
	{"batch_already_undone", []int{409}, "The batch was already undone. Restore it from Recently deleted to undo it again.", false},
	{"records_locked", []int{403}, "This workspace lets only owners and admins change customer records (Settings, Workspace). Ask one of them, or ask them to turn the setting off.", false},
	{"owner_only", []int{403}, "Only the workspace's owners, signed in, can do this (bulk edits and their undo). Preview it and ask an owner.", false},
	{"admin_only", []int{403}, "Only the workspace's owners and admins, signed in, can do this (publishing and changing query endpoints).", false},
	{"token_required", []int{403}, "This is called with an API token, not from a signed-in session (published query endpoints).", false},
	{"brand_token_required", []int{403}, "The API token must be made for this brand only: a published query endpoint answers only tokens limited to its brand.", false},
	{"endpoint_broken", []int{409}, "The saved query behind this query endpoint was archived. Ask an admin to publish it again.", false},
	{"undo_expired", []int{409}, "A bulk edit can be undone for 7 days; this one is older. Make a new bulk edit to change the records back.", false},
	{"batch_undo_running", []int{409}, "The batch is being undone in the background. GET the batch: its undo.status says when it is done.", false},
	{"deletion_expired", []int{410}, "The deletion is past its 7 days and is being purged for good; it can no longer be restored.", false},
	{"mission_required", []int{403}, "No longer answered: an app that makes changes only with a person's approval goes through the action layer now, and a change waits for a person (202). Kept for clients that know the code.", false},
	{"feature_locked", []int{403}, "This is part of Outflare Cloud, not the open-source engine. The message says what is available here instead.", false},
	{"not_a_member", []int{403}, "The link is for members of the workspace that made it. Sign in with the account you use there, or ask an admin to invite you.", false},

	// Sequences, mailboxes and infrastructure.
	{"blocked", []int{409}, "The policy blocks this, for example the mailbox or domain is paused. The message says why.", false},
	{"not_active", []int{409}, "The item is not in a state that allows this.", false},
	{"no_referral", []int{422}, "The reply doesn't name an email address to add. Add the person as a lead by hand.", false},
	{"mailbox_in_use", []int{409}, "The mailbox can't answer support: it sends for a sequence or campaign, or Outflare can't read it. The message says which.", false},
	{"nothing_to_answer", []int{409}, "The customer hasn't written anything to answer yet.", false},
	{"plan_not_ready", []int{409}, "The infrastructure plan is not ready for this step.", false},
	{"price_mismatch", []int{409}, "The amounts sent do not match the plan's quote. Send once_cents and monthly_cents as the quote shows them.", false},

	// Missions.
	{"action_refused", []int{403}, "A grant a person set for this agent token refuses this change. A person can change the token's grants (createActionGrant).", false},
	{"action_state", []int{409}, "The action is not held any more: someone already decided it, or it ran.", false},
	{"mission_not_found", []int{404}, "The Outflare-Mission header names no mission in this workspace.", false},
	{"mission_paused", []int{409}, "The mission is paused, so calls inside it are refused.", false},
	{"mission_ended", []int{409}, "The mission is finished or stopped.", false},
	{"mission_state", []int{409}, "The mission is not in a state that allows this.", false},
	{"route_moved", []int{410}, "This workspace route stopped working when brands became fully separate: brand data is under /v1/brands/{brand}/ now. The message names the route to call instead, with the same request and response.", false},
	{"mission_retired", []int{410}, "Missions are retired: what they did stays readable, and nothing changes one any more, including a change sent with the Outflare-Mission header. Make changes as your own API token on the action layer, or start the work as an agent run (POST /v1/workspaces/{ws}/playbooks/{id}/agent-runs, or the MCP tool run_playbook).", false},
	{"automation_state", []int{409}, "The playbook automation is not in a state that allows this: not approved yet, already running, or held by a limit (the message says which).", false},
	{"context_changed", []int{409}, "The company context changed since you loaded it. Reload it and apply your edit again.", false},
	{"plan_failed", []int{502}, "The AI model could not draft a plan. Reword the goal, or retry.", true},

	// Lifecycle email.
	{"no_email_provider", []int{409}, "No email provider is connected. Connect Amazon SES, Postmark or Resend under Lifecycle, Email providers.", false},
	{"email_provider_purpose", []int{409}, "The email provider is not set to send this purpose (transactional or lifecycle). Choose another provider or change its purposes.", false},
	{"domain_not_ready", []int{409}, "The provider hasn't sent the sending domain's DNS records yet. Try again in a few seconds.", true},
	{"nothing_to_add", []int{409}, "Every DNS record the brand needs is already in place, so there is nothing to add.", false},
	{"email_required", []int{400}, "This website chat asks for an email before the first message.", false},
	{"email_domain_not_verified", []int{409}, "The from address's domain is not a verified sending domain for the provider. Add it under Lifecycle, Sending domains, and add its DNS records.", false},
	{"template_not_published", []int{409}, "The template has no published version. Publish it, or send with draft: true to test it.", false},
	{"template_variables_missing", []int{422}, "data is missing variables the template requires. The message names them.", false},
	{"email_not_cancellable", []int{409}, "Only queued or scheduled emails can be cancelled; this one was sent or finished.", false},
	// SEO sites and crawls.
	{"verification_failed", []int{409}, "No proof that you own the site was found: add the DNS record, meta tag or file the site's verification lists, then check again.", true},
	{"crawl_running", []int{409}, "A crawl of this site is already running. Stop it or wait for it to finish.", false},
	{"website_import_running", []int{409}, "A website import is already running in this workspace. Wait for it to finish (GET .../knowledge/website-imports).", false},
	{"reply_learning_running", []int{409}, "Outflare is already learning from past replies in this workspace. Wait for it to finish (GET .../knowledge/reply-imports).", false},
	{"crawl_opted_out", []int{409}, "The site's owner asked not to be crawled from this Outflare server.", false},
	{"ai_run_running", []int{409}, "A run of this AI visibility cohort is already going. Stop it or wait for it to finish.", false},
	{"page_unreadable", []int{502}, "OutflareBot could not read the page: robots.txt forbids it, it didn't answer, or it had no text. The message says which.", true},

	// AI models.
	{"website_needed", []int{409}, "The brand needs a website first, so there is something to read.", false},
	{"ai_model_needed", []int{409}, "The workspace's AI model needs a choice first: a model, or its price. The message says which, under Settings, AI model.", false},
	{"ai_budget_reached", []int{409}, "AI calls reached the workspace's monthly limit. An admin can raise it under Settings, AI model.", false},
	{"ai_failed", []int{502}, "The workspace's AI model did not answer, or not usably. Retry; the message says more.", true},
	{"ai_timeout", []int{504}, "The workspace's AI model took too long to answer. Retry, or ask for less at once.", true},

	// Reddit campaigns.
	{"reddit_budget_reached", []int{409}, "The action would pass the brand's Reddit credit limit for the month or the day. A person can raise it in PUT .../reddit/settings/{brand}; GET .../reddit/budget shows what is left.", false},
	{"reddit_limit_reached", []int{409}, "The action would pass one of the brand's Reddit limits: actions per subreddit a day, or upvotes on one item. The message says which.", false},

	// Customer records.
	{"identity_taken", []int{409}, "Another record already holds this verified identity, such as a verified email. The message names it when you can see it: change that record, or add the identity unverified.", false},
	{"merge_refused", []int{409}, "Both records have their own user id, so they are two accounts in your product and are never merged. Link them as the same human instead (POST .../records/duplicates/{candidate}/link).", false},
	{"not_a_hand_edit", []int{409}, "Only a value someone set by hand can be pinned or unpinned. Set the value by hand first (Use this), then pin it.", false},
	{"field_empty", []int{409}, "The record has no value for this field, so there is nothing to pin.", false},
	{"record_changed", []int{409}, "The record changed since the version you sent. Read it again, check your change still makes sense, and send it with the new version.", false},
	{"event_name_in_other_brand", []int{409}, "No longer returned: each brand has its own event catalog, so two brands can define the same event name. Kept so older clients and dashboards that know the code still find it.", false},
	{"lead_in_other_brand", []int{409}, "This address is already a lead of another brand in this workspace. Each brand has its own leads, but until a later release an address is a lead in one brand at a time: add it in the brand that has it, or delete it there first.", false},
	{"version_required", []int{400}, "Agents (MCP, connected AI apps and missions) send the record's version with every change to it, so one agent never overwrites another's. Read the record and send its version.", false},

	// Query.
	{"query_invalid", []int{400}, "The SQL was refused or failed: the message is the database's own. Queries are one SELECT over the tables GET .../query/tables lists.", false},
	{"query_timeout", []int{400}, "The query ran too long and was stopped. Filter it more, or count and group instead of listing rows.", false},
	{"saved_query_archived", []int{409}, "The saved query is archived, so it can't be changed or run. Save its SQL as a new query.", false},
	{"query_unavailable", []int{503}, "Query couldn't set up its read-only database role. The server's log says why; the database user needs to be able to create roles.", true},

	// Search.
	{"data_network_unavailable", []int{409}, "The data network isn't available for this workspace. Everything else about the email finder works without it.", false},
	{"data_network_allowance", []int{429}, "The workspace has used this month's data network lookups. The email finder carries on without the network; joining gives unlimited lookups.", false},
	{"data_network_limit", []int{429}, "Today's data network lookups for this domain are used up. The email finder carries on without the network; try again tomorrow.", false},
	{"semantic_search_unavailable", []int{503}, "Meaning-based search needs the pgvector extension in the server's Postgres database, and it isn't there. Word search works without it.", false},
	{"semantic_search_off", []int{409}, "Meaning-based search is off in this workspace. An admin turns it on with PUT .../semantic-search.", false},

	// Partner portals.
	{"portal_email_unavailable", []int{409}, "The partner portal can't send sign-in emails: the program has no sender on a verified sending domain of the workspace's own email provider.", false},
	{"personal_email", []int{400}, "The partner program takes applications only from work email addresses, not free, disposable or relay ones (the portal's business_email_only).", false},

	// Shared dashboards (POST /v1/shared/dashboards/{token}).
	{"password_required", []int{401}, "This shared dashboard has a password. Send it as password, then reuse the key the answer gives.", false},
	{"wrong_password", []int{401}, "The password for this shared dashboard is wrong. Repeated tries are rate limited per link.", false},
}

func (s *Server) listErrorCodes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"data": errorCodes})
}
