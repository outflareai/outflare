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
	{"rate_limited", []int{429}, "Too many requests. Wait for Retry-After, then retry.", true},
	{"internal", []int{500}, "A server error. Quote the request_id.", true},
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
	{"project_has_tokens", []int{409}, "The project still has API tokens. Revoke them first.", false},
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
	{"budget_reached", []int{402}, "The connection's monthly spending budget is used up.", false},
	{"platform_error", []int{502}, "The sending platform returned an error.", true},
	{"send_failed", []int{502}, "The email could not be sent through the mailbox.", true},
	{"bad_signature", []int{401}, "An inbound webhook's signature does not match.", false},
	{"expired", []int{410}, "The connect link has expired. Ask for a new one.", false},
	{"cancelled", []int{410}, "The connect link was cancelled. Ask for a new one.", false},
	{"used", []int{410}, "The link was already used.", false},
	{"mission_required", []int{403}, "This app or token makes changes only inside a mission. Start one, and once a person approves it send its id in the Outflare-Mission header. Reads need no mission.", false},
	{"not_a_member", []int{403}, "The link is for members of the workspace that made it. Sign in with the account you use there, or ask an admin to invite you.", false},

	// Sequences, mailboxes and infrastructure.
	{"blocked", []int{409}, "The policy blocks this, for example the mailbox or domain is paused. The message says why.", false},
	{"not_active", []int{409}, "The item is not in a state that allows this.", false},
	{"plan_not_ready", []int{409}, "The infrastructure plan is not ready for this step.", false},
	{"price_mismatch", []int{409}, "The amounts sent do not match the plan's quote. Send once_cents and monthly_cents as the quote shows them.", false},

	// Missions.
	{"mission_not_found", []int{404}, "The Outflare-Mission header names no mission in this workspace.", false},
	{"mission_paused", []int{409}, "The mission is paused, so calls inside it are refused.", false},
	{"mission_ended", []int{409}, "The mission is finished or stopped.", false},
	{"mission_state", []int{409}, "The mission is not in a state that allows this.", false},
	{"brief_changed", []int{409}, "The brief changed since you loaded it. Reload it and apply your edit again.", false},
	{"context_changed", []int{409}, "The company context changed since you loaded it. Reload it and apply your edit again.", false},
	{"plan_failed", []int{502}, "The AI model could not draft a plan. Reword the goal, or retry.", true},

	// Lifecycle email.
	{"no_email_provider", []int{409}, "No email provider is connected. Connect Amazon SES, Postmark or Resend under Lifecycle, Email providers.", false},
	{"email_provider_purpose", []int{409}, "The email provider is not set to send this purpose (transactional or lifecycle). Choose another provider or change its purposes.", false},
	{"domain_not_ready", []int{409}, "The provider hasn't sent the sending domain's DNS records yet. Try again in a few seconds.", true},
	{"email_domain_not_verified", []int{409}, "The from address's domain is not a verified sending domain for the provider. Add it under Lifecycle, Sending domains, and add its DNS records.", false},
	{"template_not_published", []int{409}, "The template has no published version. Publish it, or send with draft: true to test it.", false},
	{"template_variables_missing", []int{422}, "data is missing variables the template requires. The message names them.", false},
	{"email_not_cancellable", []int{409}, "Only queued or scheduled emails can be cancelled; this one was sent or finished.", false},
	// SEO sites and crawls.
	{"verification_failed", []int{409}, "No proof that you own the site was found: add the DNS record, meta tag or file the site's verification lists, then check again.", true},
	{"crawl_running", []int{409}, "A crawl of this site is already running. Stop it or wait for it to finish.", false},
	{"crawl_opted_out", []int{409}, "The site's owner asked not to be crawled from this Outflare server.", false},
	{"ai_run_running", []int{409}, "A run of this AI visibility cohort is already going. Stop it or wait for it to finish.", false},

	// AI models.
	{"ai_model_needed", []int{409}, "The workspace's AI model needs a choice first: a model, or its price. The message says which, under Settings, AI model.", false},
	{"ai_budget_reached", []int{409}, "AI calls reached the workspace's monthly limit. An admin can raise it under Settings, AI model.", false},

	// Customer records.
	{"identity_taken", []int{409}, "Another record already holds this verified identity, such as a verified email. The message names it when you can see it: change that record, or add the identity unverified.", false},
	{"merge_refused", []int{409}, "Both records have their own user id, so they are two accounts in your product and are never merged. Link them as the same human instead (POST .../records/duplicates/{candidate}/link).", false},
	{"record_changed", []int{409}, "The record changed since the version you sent. Read it again, check your change still makes sense, and send it with the new version.", false},
	{"version_required", []int{400}, "Agents (MCP, connected AI apps and missions) send the record's version with every change to it, so one agent never overwrites another's. Read the record and send its version.", false},

	// Query.
	{"query_invalid", []int{400}, "The SQL was refused or failed: the message is the database's own. Queries are one SELECT over the tables GET .../query/tables lists.", false},
	{"query_timeout", []int{400}, "The query ran too long and was stopped. Filter it more, or count and group instead of listing rows.", false},
	{"query_unavailable", []int{503}, "Query couldn't set up its read-only database role. The server's log says why; the database user needs to be able to create roles.", true},
}

func (s *Server) listErrorCodes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"data": errorCodes})
}
