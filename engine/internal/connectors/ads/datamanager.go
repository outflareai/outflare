package ads

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Google's Data Manager API (https://developers.google.com/data-manager/api)
// is where conversions and Customer Match members go since 2026:
// events:ingest sends conversions to an UPLOAD_CLICKS conversion action
// (up to 2,000 a request), audienceMembers:ingest and :remove fill a
// Customer Match list (up to 10,000 a request), and requestStatus:retrieve
// says how a request was processed. It needs no developer token: the
// workspace's own Google sign-in with the datamanager scope is enough, and
// every identifier is SHA-256 hashed before it leaves (encoding HEX).
//
// The feed takes a request whole or refuses it whole (its "fast fail"): a
// 400 names the rows that failed in its BadRequest details. The sender
// reads those out and sends the rest again; when it cannot tell which row
// is bad, it halves the batch and tries each half, down to the one row,
// so a bad row never holds the others back.

const (
	dataManagerBase  = "https://datamanager.googleapis.com"
	dataManagerV     = "v1"
	scopeDataManager = "https://www.googleapis.com/auth/datamanager"
	// dmEventsMax and dmMembersMax are Google's limits per request.
	dmEventsMax  = 2000
	dmMembersMax = 10000
)

func (GoogleAds) dmBase(cl *c.Client) string {
	return cl.Base("google_ads.datamanager", dataManagerBase) + "/" + dataManagerV
}

// dmError is the Data Manager API's error body (google.rpc.Status): the
// reason from google.rpc.ErrorInfo and the rows google.rpc.BadRequest
// names.
type dmError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
		Details []struct {
			Type            string            `json:"@type"`
			Reason          string            `json:"reason"`
			Domain          string            `json:"domain"`
			Metadata        map[string]string `json:"metadata"`
			FieldViolations []struct {
				Field       string `json:"field"`
				Description string `json:"description"`
				Reason      string `json:"reason"`
			} `json:"fieldViolations"`
		} `json:"details"`
	} `json:"error"`
}

// reason is the ErrorInfo reason, else the status.
func (e dmError) reason() string {
	for _, d := range e.Error.Details {
		if d.Reason != "" && strings.HasSuffix(d.Type, "google.rpc.ErrorInfo") {
			return d.Reason
		}
	}
	return e.Error.Status
}

// dmRowRE finds the row a field violation is about: events[3] or
// audience_members[3] (the API names fields in snake case, with the request
// field first).
var dmRowRE = regexp.MustCompile(`(?:^|\.)(?:events|audience_members|audienceMembers)\[(\d+)\]`)

// rows maps the rows the violations name to their reasons.
func (e dmError) rows() map[int]string {
	out := map[int]string{}
	for _, d := range e.Error.Details {
		for _, v := range d.FieldViolations {
			m := dmRowRE.FindStringSubmatch(v.Field)
			if m == nil {
				continue
			}
			i, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			msg := v.Reason
			if v.Description != "" {
				msg = v.Reason + ": " + v.Description
			}
			if out[i] == "" {
				out[i] = strings.TrimPrefix(msg, ": ")
			}
		}
	}
	return out
}

// dmUnusable are the reasons that mean the destination itself cannot be
// used yet, not that a row is bad: the sender then falls back to the Ads
// API upload while Google still takes it.
var dmUnusable = map[string]bool{
	"NOT_ALLOWLISTED": true, "INVALID_DESTINATION": true, "INVALID_CONVERSION_ACTION_ID": true, "INVALID_CONVERSION_ACTION_TYPE": true,
	"TERMS_AND_CONDITIONS_NOT_SIGNED": true, "DESTINATION_ACCOUNT_NOT_ENABLED_ENHANCED_CONVERSIONS_FOR_LEADS": true,
	"DESTINATION_ACCOUNT_DATA_POLICY_PROHIBITS_ENHANCED_CONVERSIONS": true, "DESTINATION_ACCOUNT_ENHANCED_CONVERSIONS_TERMS_NOT_SIGNED": true,
	"DESTINATION_ACCOUNT_TYPE_MISMATCH": true, "OPERATING_ACCOUNT_LOGIN_ACCOUNT_MISMATCH": true, "SERVICE_DISABLED": true, "PERMISSION_DENIED": true,
	"ACCESS_TOKEN_SCOPE_INSUFFICIENT": true, "INVALID_USER_LIST_ID": true, "INVALID_USER_LIST_TYPE": true, "CONVERSION_ACTION_TOO_RECENTLY_CREATED": true,
}

// dmCall posts body to the Data Manager API. A refusal comes back as a
// *dmRefusal with the parsed error, so the caller can read the rows it
// names; other failures are connectors errors as everywhere else.
func (g GoogleAds) dmCall(ctx context.Context, cl *c.Client, creds c.Creds, method, u string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return c.Errf(c.ErrInvalid, "bad Data Manager address: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+creds["access_token"])
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := cl.Do(req)
	if err != nil {
		return c.Errf(c.ErrUnavailable, "could not reach Google's Data Manager API: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return c.Errf(c.ErrUnavailable, "reading the Data Manager answer: %v", err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out != nil {
			if err := json.Unmarshal(raw, out); err != nil {
				return c.Errf(c.ErrUnavailable, "Google's Data Manager API returned something that is not JSON")
			}
		}
		return nil
	}
	var de dmError
	_ = json.Unmarshal(raw, &de)
	reason := de.reason()
	msg := de.Error.Message
	if msg == "" {
		msg = strings.TrimSpace(string(raw))
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
	}
	switch {
	case resp.StatusCode == 401:
		return c.Errf(c.ErrAuth, "Google refused the sign-in (HTTP 401): connect Google Ads again")
	case resp.StatusCode == 429 || reason == "RESOURCE_EXHAUSTED" || reason == "RATE_LIMIT_EXCEEDED":
		return c.Errf(c.ErrQuota, "Google's Data Manager API is rate limiting this project (HTTP %d)", resp.StatusCode)
	case resp.StatusCode >= 500:
		return c.Errf(c.ErrUnavailable, "Google's Data Manager API is not responding properly (HTTP %d)", resp.StatusCode)
	case resp.StatusCode == 403 || dmUnusable[reason]:
		why := reason
		if why == "" {
			why = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		kind := c.ErrInvalid
		if resp.StatusCode == 403 {
			kind = c.ErrAuth
		}
		return &c.FeedRefused{Reason: why, Err: c.Errf(kind, "Google's Data Manager API refused the destination (%s): %s", why, msg)}
	}
	return &dmRefusal{err: de, msg: c.Errf(c.ErrInvalid, "Google's Data Manager API refused the request (%s): %s", firstText(reason, "HTTP "+strconv.Itoa(resp.StatusCode)), msg)}
}

// dmRefusal is a 400 with the rows it names.
type dmRefusal struct {
	err dmError
	msg *c.Error
}

func (r *dmRefusal) Error() string { return r.msg.Error() }
func (r *dmRefusal) Unwrap() error { return r.msg }

// dmDestination is the account and conversion action or list a request
// goes to: the account that owns it, through the manager account the
// sign-in uses when there is one.
func dmDestination(acct c.AdAccount, productID string) map[string]any {
	d := map[string]any{
		"reference":            "outflare",
		"operatingAccount":     map[string]string{"accountType": "GOOGLE_ADS", "accountId": acct.ID},
		"productDestinationId": productID,
	}
	if login := acct.Meta["login_customer_id"]; login != "" && login != acct.ID {
		d["loginAccount"] = map[string]string{"accountType": "GOOGLE_ADS", "accountId": login}
	}
	return d
}

// dmConsent is the consent a person gave, as the API spells it. Nothing is
// said when it was not asked where the law allows sending without it.
func dmConsent(granted, personalization bool) map[string]string {
	if !granted {
		return nil
	}
	out := map[string]string{"adUserData": "CONSENT_GRANTED"}
	if personalization {
		out["adPersonalization"] = "CONSENT_GRANTED"
	}
	return out
}

// dmUserData is the hashed identifiers the API matches on (at most ten).
func dmUserData(emails, phones []string) map[string]any {
	var ids []map[string]string
	for _, e := range emails {
		ids = append(ids, map[string]string{"emailAddress": e})
	}
	for _, p := range phones {
		ids = append(ids, map[string]string{"phoneNumber": p})
	}
	if len(ids) > 10 {
		ids = ids[:10]
	}
	if len(ids) == 0 {
		return nil
	}
	return map[string]any{"userIdentifiers": ids}
}

// dmEvent builds one event, or the reason it cannot be sent.
func dmEvent(cv c.AdConversion) (map[string]any, []string, string) {
	ev := map[string]any{
		"eventTimestamp": cv.Time.UTC().Format(time.RFC3339),
		"transactionId":  cv.EventID,
		"eventSource":    "WEB",
	}
	var keys []string
	ads := map[string]string{}
	for _, k := range []string{"gclid", "gbraid", "wbraid"} {
		if v := cv.ClickIDs[k]; v != "" {
			ads[k] = v
			keys = append(keys, k)
			break
		}
	}
	if len(ads) == 0 {
		if v := gclidFromCookie(cv.ClickIDs["gcl_aw"]); v != "" {
			ads["gclid"] = v
			keys = append(keys, "gclid")
		}
	}
	if len(ads) > 0 {
		ev["adIdentifiers"] = ads
	}
	emails := cv.GoogleEmails
	if len(emails) == 0 {
		emails = cv.Emails
	}
	if ud := dmUserData(emails, cv.Phones); ud != nil {
		ev["userData"] = ud
		if len(emails) > 0 {
			keys = append(keys, "email")
		}
		if len(cv.Phones) > 0 {
			keys = append(keys, "phone")
		}
	}
	if len(keys) == 0 {
		return nil, nil, "no click id, email or phone to match on"
	}
	if cv.ValueMicros != nil {
		ev["conversionValue"] = float64(*cv.ValueMicros) / 1e6
		if cv.Currency != "" {
			ev["currency"] = cv.Currency
		}
	}
	if cs := dmConsent(cv.ConsentGranted, false); cs != nil {
		ev["consent"] = cs
	}
	switch cv.CustomerType {
	case c.CustomerNew:
		ev["userProperties"] = map[string]string{"customerType": "NEW"}
	case c.CustomerReturning:
		ev["userProperties"] = map[string]string{"customerType": "RETURNING"}
	}
	if cv.Restates && !cv.UpdatedAt.IsZero() {
		ev["lastUpdatedTimestamp"] = cv.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return ev, keys, ""
}

// SendConversionsFeed sends conversions to a conversion action through
// events:ingest, in requests of at most dmEventsMax. A refused request is
// narrowed down to its bad rows (see the package comment); a refused
// destination is a *FeedRefused.
func (g GoogleAds) SendConversionsFeed(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, target c.ConversionTarget, convs []c.AdConversion) (c.FeedResult, error) {
	out := c.FeedResult{Results: make([]c.ConversionResult, len(convs))}
	type row struct {
		i    int
		body map[string]any
	}
	var rows []row
	for i, cv := range convs {
		out.Results[i] = c.ConversionResult{Index: cv.Index}
		ev, keys, reason := dmEvent(cv)
		if reason != "" {
			out.Results[i].Skipped, out.Results[i].Reason = true, reason
			continue
		}
		out.Results[i].MatchKeys = keys
		rows = append(rows, row{i, ev})
	}
	dest := []any{dmDestination(acct, target.ID)}
	// send tries one batch; on a refusal that names rows, those fail and
	// the rest go again; on one that names none, each half goes alone.
	var send func(batch []row, depth int) error
	send = func(batch []row, depth int) error {
		if len(batch) == 0 {
			return nil
		}
		events := make([]any, len(batch))
		for j, r := range batch {
			events[j] = r.body
		}
		var resp struct {
			RequestID string `json:"requestId"`
		}
		err := g.dmCall(ctx, cl, creds, http.MethodPost, g.dmBase(cl)+"/events:ingest",
			map[string]any{"destinations": dest, "events": events, "encoding": "HEX", "validateOnly": false}, &resp)
		if err == nil {
			for _, r := range batch {
				out.Results[r.i].OK = true
			}
			if resp.RequestID != "" {
				out.RequestID = resp.RequestID
			}
			return nil
		}
		var ref *dmRefusal
		if !asRefusal(err, &ref) {
			return err
		}
		bad := ref.err.rows()
		if len(bad) == 0 {
			if len(batch) == 1 || depth > 12 {
				for _, r := range batch {
					out.Results[r.i].Reason = ref.Error()
				}
				return nil
			}
			half := len(batch) / 2
			if err := send(batch[:half], depth+1); err != nil {
				return err
			}
			return send(batch[half:], depth+1)
		}
		var rest []row
		for j, r := range batch {
			if msg, isBad := bad[j]; isBad {
				out.Results[r.i].Reason = msg
				continue
			}
			rest = append(rest, r)
		}
		if len(rest) == len(batch) {
			// It named rows outside this batch: count it on all.
			for _, r := range batch {
				out.Results[r.i].Reason = ref.Error()
			}
			return nil
		}
		return send(rest, depth+1)
	}
	for start := 0; start < len(rows); start += dmEventsMax {
		end := min(start+dmEventsMax, len(rows))
		if err := send(rows[start:end], 0); err != nil {
			return out, err
		}
	}
	return out, nil
}

func asRefusal(err error, target **dmRefusal) bool {
	for err != nil {
		if r, ok := err.(*dmRefusal); ok {
			*target = r
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// dmMember builds one audience member, or nil without identifiers.
func dmMember(m c.AudienceMember, withConsent bool) map[string]any {
	emails := m.GoogleEmails
	if len(emails) == 0 {
		emails = m.Emails
	}
	ud := dmUserData(emails, m.Phones)
	if ud == nil {
		return nil
	}
	out := map[string]any{"userData": ud}
	if withConsent {
		if cs := dmConsent(m.ConsentGranted, true); cs != nil {
			out["consent"] = cs
		}
	}
	return out
}

// IngestAudience adds members to a Customer Match list (audienceMembers:
// ingest) or takes them out (audienceMembers:remove), in requests of at
// most dmMembersMax. Google matches the hashed identifiers on its side.
// Adding says the Customer Match terms are accepted, as the API requires;
// the account accepts them in Google Ads. The last request's id is
// returned.
func (g GoogleAds) IngestAudience(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, audienceID, change string, members []c.AudienceMember) (string, error) {
	add := change != c.AudienceRemove
	var rows []any
	for _, m := range members {
		if r := dmMember(m, add); r != nil {
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		return "", nil
	}
	path, requestID := "/audienceMembers:ingest", ""
	if !add {
		path = "/audienceMembers:remove"
	}
	for start := 0; start < len(rows); start += dmMembersMax {
		end := min(start+dmMembersMax, len(rows))
		body := map[string]any{"destinations": []any{dmDestination(acct, audienceID)}, "audienceMembers": rows[start:end], "encoding": "HEX", "validateOnly": false}
		if add {
			body["termsOfService"] = map[string]string{"customerMatchTermsOfServiceStatus": "ACCEPTED"}
		}
		var resp struct {
			RequestID string `json:"requestId"`
		}
		if err := g.dmCall(ctx, cl, creds, http.MethodPost, g.dmBase(cl)+path, body, &resp); err != nil {
			return requestID, err
		}
		requestID = resp.RequestID
	}
	return requestID, nil
}

// FeedRequestStatus is how Google processed one request, per destination.
type FeedRequestStatus struct {
	// Status is PROCESSING, SUCCESS, PARTIAL_SUCCESS or FAILED.
	Status  string
	Records int64
	// Errors counts the records that failed by reason, with the rows.
	Errors []FeedRequestError
}

// FeedRequestError is one reason records failed.
type FeedRequestError struct {
	Reason  string
	Count   int64
	Indices []int
}

// RequestStatus asks how a request was processed (requestStatus:
// retrieve). Google answers from about 30 minutes after the request and
// for about 50 days.
func (g GoogleAds) RequestStatus(ctx context.Context, cl *c.Client, creds c.Creds, requestID string) (FeedRequestStatus, error) {
	var resp struct {
		PerDestination []struct {
			RequestStatus string `json:"requestStatus"`
			Events        *struct {
				RecordCount flexInt `json:"recordCount"`
			} `json:"eventsIngestionStatus"`
			Members *struct {
				UserData *struct {
					RecordCount flexInt `json:"recordCount"`
				} `json:"userDataIngestionStatus"`
			} `json:"audienceMembersIngestionStatus"`
			ErrorInfo struct {
				ErrorCounts []struct {
					Reason        string  `json:"reason"`
					RecordCount   flexInt `json:"recordCount"`
					RecordIndices []int   `json:"recordIndices"`
				} `json:"errorCounts"`
			} `json:"errorInfo"`
		} `json:"requestStatusPerDestination"`
	}
	err := g.dmCall(ctx, cl, creds, http.MethodGet, g.dmBase(cl)+"/requestStatus:retrieve?requestId="+esc(requestID), nil, &resp)
	if err != nil {
		return FeedRequestStatus{}, err
	}
	if len(resp.PerDestination) == 0 {
		return FeedRequestStatus{}, c.Errf(c.ErrUnavailable, "Google's Data Manager API knows nothing about request %s", requestID)
	}
	d := resp.PerDestination[0]
	st := FeedRequestStatus{Status: d.RequestStatus}
	if d.Events != nil {
		st.Records = int64(d.Events.RecordCount)
	} else if d.Members != nil && d.Members.UserData != nil {
		st.Records = int64(d.Members.UserData.RecordCount)
	}
	for _, e := range d.ErrorInfo.ErrorCounts {
		st.Errors = append(st.Errors, FeedRequestError{Reason: e.Reason, Count: int64(e.RecordCount), Indices: e.RecordIndices})
	}
	return st, nil
}

// SyncAudience adds the person to a Customer Match list, or removes them,
// through the Data Manager API (journeys' sync_audience step). Google
// processes the request on its own time and matches the hashed
// identifiers on its side.
func (g GoogleAds) SyncAudience(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, audienceID, change string, p c.AudienceMember) error {
	_, err := g.IngestAudience(ctx, cl, creds, acct, audienceID, change, []c.AudienceMember{p})
	return err
}

// ReconnectNeeded says when the sign-in predates the Data Manager scope,
// so conversions and the payers list cannot be sent until the workspace
// connects Google Ads again. A sign-in whose grant is unknown (made before
// Outflare kept it) is asked to reconnect too.
func (GoogleAds) ReconnectNeeded(creds c.Creds) string {
	if strings.Contains(creds["scope"], scopeDataManager) {
		return ""
	}
	return "Connect Google Ads again to send conversions and fill customer lists: Google's Data Manager API needs a permission this sign-in was made without."
}
