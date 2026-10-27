// Package verifiers connects email verification services. Each one checks
// one address at a time and maps its answer onto the four shared statuses:
// deliverable, undeliverable, risky and unknown.
//
// The request shapes follow each provider's public API reference. Base URLs
// can be pointed at a fake in tests through connectors.Client.BaseURLs.
package verifiers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// All returns every available verification connector.
func All() []c.Connector {
	return []c.Connector{
		Builtin{}, ZeroBounce{}, NeverBounce{}, MillionVerifier{}, Bouncer{}, Reoon{}, Emailable{}, DeBounce{}, Hunter{},
	}
}

// Planned lists verification services we intend to add.
func Planned() []c.Definition {
	p := func(id, name, site, summary string) c.Definition {
		return c.Definition{ID: id, Name: name, Category: c.CategoryVerification, Website: site, Summary: summary,
			Auth: c.AuthAPIKey, Capabilities: []string{c.CapVerify}}
	}
	return []c.Definition{
		p("kickbox", "Kickbox", "https://kickbox.com", "Email verification with a Sendex quality score."),
		p("clearout", "Clearout", "https://clearout.io", "Real-time and bulk email verification."),
		p("bounceban", "BounceBan", "https://bounceban.com", "Verifies catch-all addresses other tools mark unknown."),
		p("scrubby", "Scrubby", "https://scrubby.io", "Catch-all and unknown address validation."),
		p("briteverify", "BriteVerify", "https://www.validity.com/briteverify/", "Email verification from Validity."),
		p("cheapverifier", "Cheap Verifier", "https://cheapinboxes.com", "Low-cost verification from the Cheap Inboxes team."),
	}
}

func verifyCaps() []string { return []string{c.CapVerify} }

func apiKeyField(help string) []c.Field {
	return []c.Field{{Name: "api_key", Label: "API key", Secret: true, Help: help}}
}

// getJSON sends req and decodes a JSON body into v. A non-2xx status is
// turned into a connectors.Error.
func getJSON(ctx context.Context, cl *c.Client, name string, req *http.Request, v any) (int, error) {
	req = req.WithContext(ctx)
	req.Header.Set("Accept", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		return 0, c.Errf(c.ErrUnavailable, "could not reach %s: %v", name, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, c.Errf(c.ErrUnavailable, "reading the %s response: %v", name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, c.HTTPError(name, resp.StatusCode, string(body))
	}
	if v != nil {
		if err := json.Unmarshal(body, v); err != nil {
			return resp.StatusCode, c.Errf(c.ErrUnavailable, "%s returned something that is not JSON", name)
		}
	}
	return resp.StatusCode, nil
}

func get(rawURL string, q url.Values) *http.Request {
	req, _ := http.NewRequest(http.MethodGet, rawURL+"?"+q.Encode(), nil)
	return req
}

// flex reads JSON values some providers send as strings ("true", "12").
type flex string

func (f *flex) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flex(s)
		return nil
	}
	*f = flex(strings.Trim(string(b), `"`))
	return nil
}

func (f flex) bool() bool {
	switch strings.ToLower(string(f)) {
	case "true", "1", "yes":
		return true
	}
	return false
}

func (f flex) float() *float64 {
	v, err := strconv.ParseFloat(string(f), 64)
	if err != nil {
		return nil
	}
	return &v
}

func ptr[T any](v T) *T { return &v }

func score(v float64) *int {
	n := int(v + 0.5)
	if n < 0 || n > 100 {
		return nil
	}
	return &n
}

// ---- ZeroBounce ----

// ZeroBounce: https://www.zerobounce.net/docs/email-validation-api-quickstart/
type ZeroBounce struct{}

func (ZeroBounce) Definition() c.Definition {
	return c.Definition{
		ID: "zerobounce", Name: "ZeroBounce", Category: c.CategoryVerification,
		Summary:     "Email validation with spam-trap, abuse and catch-all detection.",
		Description: "Checks each address before it is emailed and flags spam traps, known complainers and catch-all domains.",
		Website:     "https://www.zerobounce.net", DocsURL: "https://www.zerobounce.net/docs/email-validation-api-quickstart/",
		KeyURL: "https://www.zerobounce.net/members/API", Auth: c.AuthAPIKey,
		Fields:       apiKeyField("ZeroBounce → API → API Keys."),
		Capabilities: verifyCaps(), Pricing: "Pay per credit",
	}
}

func (z ZeroBounce) base(cl *c.Client) string {
	return cl.Base("zerobounce", "https://api.zerobounce.net")
}

func (z ZeroBounce) Test(ctx context.Context, cl *c.Client, cr c.Creds) (c.Account, error) {
	var r struct {
		Credits flex `json:"Credits"`
	}
	if _, err := getJSON(ctx, cl, "ZeroBounce", get(z.base(cl)+"/v2/getcredits", url.Values{"api_key": {cr["api_key"]}}), &r); err != nil {
		return c.Account{}, err
	}
	credits := r.Credits.float()
	if credits == nil || *credits < 0 {
		return c.Account{}, c.Errf(c.ErrAuth, "ZeroBounce did not accept the API key")
	}
	return c.Account{Credits: credits}, nil
}

func (z ZeroBounce) Verify(ctx context.Context, cl *c.Client, cr c.Creds, email string) (c.Verification, error) {
	var r struct {
		Status     string `json:"status"`
		SubStatus  string `json:"sub_status"`
		FreeEmail  flex   `json:"free_email"`
		DidYouMean string `json:"did_you_mean"`
		Error      string `json:"error"`
	}
	q := url.Values{"api_key": {cr["api_key"]}, "email": {email}, "ip_address": {""}}
	if _, err := getJSON(ctx, cl, "ZeroBounce", get(z.base(cl)+"/v2/validate", q), &r); err != nil {
		return c.Verification{}, err
	}
	if r.Error != "" {
		kind := c.ErrInvalid
		if strings.Contains(strings.ToLower(r.Error), "key") || strings.Contains(strings.ToLower(r.Error), "credits") {
			kind = c.ErrQuota
		}
		return c.Verification{}, c.Errf(kind, "ZeroBounce: %s", r.Error)
	}
	v := c.Verification{Email: email, Reason: r.SubStatus, Free: r.FreeEmail.bool(), Suggestion: r.DidYouMean,
		ProviderStatus: strings.TrimSuffix(r.Status+"/"+r.SubStatus, "/")}
	switch r.Status {
	case "valid":
		v.Status = c.Deliverable
	case "invalid", "spamtrap", "abuse":
		v.Status = c.Undeliverable
	case "catch-all":
		v.Status, v.CatchAll = c.Risky, true
	case "do_not_mail":
		switch r.SubStatus {
		case "role_based", "role_based_catch_all":
			v.Status, v.Role = c.Risky, true
		case "disposable":
			v.Status, v.Disposable = c.Undeliverable, true
		default:
			v.Status = c.Undeliverable
		}
	default:
		v.Status = c.Unknown
	}
	if v.Reason == "" {
		v.Reason = r.Status
	}
	return v, nil
}

// ---- NeverBounce ----

// NeverBounce: https://developers.neverbounce.com/docs/verifying-an-email
type NeverBounce struct{}

func (NeverBounce) Definition() c.Definition {
	return c.Definition{
		ID: "neverbounce", Name: "NeverBounce", Category: c.CategoryVerification,
		Summary:     "Real-time email verification with a deliverability guarantee.",
		Description: "Verifies each address and flags disposable and catch-all domains.",
		Website:     "https://neverbounce.com", DocsURL: "https://developers.neverbounce.com/docs/verifying-an-email",
		KeyURL: "https://app.neverbounce.com/apps", Auth: c.AuthAPIKey,
		Fields:       apiKeyField("NeverBounce → Apps → create a Custom Integration and copy its v4 API key (starts with secret_)."),
		Capabilities: verifyCaps(), Pricing: "Pay per credit",
	}
}

func (n NeverBounce) base(cl *c.Client) string {
	return cl.Base("neverbounce", "https://api.neverbounce.com")
}

type nbStatus struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

func (s nbStatus) err() error {
	switch s.Status {
	case "success", "":
		return nil
	case "auth_failure":
		return c.Errf(c.ErrAuth, "NeverBounce refused the API key: %s", s.Message)
	case "throttle_triggered":
		return c.Errf(c.ErrQuota, "NeverBounce rate limit: %s", s.Message)
	}
	return c.Errf(c.ErrInvalid, "NeverBounce: %s %s", s.Status, s.Message)
}

func (n NeverBounce) Test(ctx context.Context, cl *c.Client, cr c.Creds) (c.Account, error) {
	var r struct {
		nbStatus
		Credits struct {
			Paid float64 `json:"paid_credits_remaining"`
			Free float64 `json:"free_credits_remaining"`
		} `json:"credits_info"`
	}
	if _, err := getJSON(ctx, cl, "NeverBounce", get(n.base(cl)+"/v4/account/info", url.Values{"key": {cr["api_key"]}}), &r); err != nil {
		return c.Account{}, err
	}
	if err := r.err(); err != nil {
		return c.Account{}, err
	}
	return c.Account{Credits: ptr(r.Credits.Paid + r.Credits.Free)}, nil
}

func (n NeverBounce) Verify(ctx context.Context, cl *c.Client, cr c.Creds, email string) (c.Verification, error) {
	var r struct {
		nbStatus
		Result              string   `json:"result"`
		Flags               []string `json:"flags"`
		SuggestedCorrection string   `json:"suggested_correction"`
	}
	q := url.Values{"key": {cr["api_key"]}, "email": {email}, "timeout": {"20"}}
	if _, err := getJSON(ctx, cl, "NeverBounce", get(n.base(cl)+"/v4/single/check", q), &r); err != nil {
		return c.Verification{}, err
	}
	if err := r.err(); err != nil {
		return c.Verification{}, err
	}
	v := c.Verification{Email: email, Reason: r.Result, Suggestion: r.SuggestedCorrection, ProviderStatus: r.Result}
	for _, f := range r.Flags {
		switch f {
		case "free_email_host":
			v.Free = true
		case "role_account":
			v.Role = true
		}
	}
	switch r.Result {
	case "valid":
		v.Status = c.Deliverable
	case "invalid":
		v.Status = c.Undeliverable
	case "disposable":
		v.Status, v.Disposable = c.Undeliverable, true
	case "catchall":
		v.Status, v.CatchAll = c.Risky, true
	default:
		v.Status = c.Unknown
	}
	return v, nil
}

// ---- MillionVerifier ----

// MillionVerifier: https://developer.millionverifier.com
type MillionVerifier struct{}

func (MillionVerifier) Definition() c.Definition {
	return c.Definition{
		ID: "millionverifier", Name: "MillionVerifier", Category: c.CategoryVerification,
		Summary:     "Low-cost email verification popular with cold email teams.",
		Description: "Verifies each address and marks catch-all, disposable and role addresses.",
		Website:     "https://www.millionverifier.com", DocsURL: "https://developer.millionverifier.com",
		KeyURL: "https://app.millionverifier.com/api", Auth: c.AuthAPIKey,
		Fields:       apiKeyField("MillionVerifier → API → copy the API key."),
		Capabilities: verifyCaps(), Pricing: "Pay per credit",
	}
}

func (m MillionVerifier) base(cl *c.Client) string {
	return cl.Base("millionverifier", "https://api.millionverifier.com")
}

func mvErr(msg string) error {
	l := strings.ToLower(msg)
	switch {
	case strings.Contains(l, "key"):
		return c.Errf(c.ErrAuth, "MillionVerifier refused the API key: %s", msg)
	case strings.Contains(l, "credit"):
		return c.Errf(c.ErrQuota, "MillionVerifier: %s", msg)
	}
	return c.Errf(c.ErrInvalid, "MillionVerifier: %s", msg)
}

func (m MillionVerifier) Test(ctx context.Context, cl *c.Client, cr c.Creds) (c.Account, error) {
	var r struct {
		Credits flex   `json:"credits"`
		Error   string `json:"error"`
	}
	if _, err := getJSON(ctx, cl, "MillionVerifier", get(m.base(cl)+"/api/v3/credits", url.Values{"api": {cr["api_key"]}}), &r); err != nil {
		return c.Account{}, err
	}
	if r.Error != "" {
		return c.Account{}, mvErr(r.Error)
	}
	return c.Account{Credits: r.Credits.float()}, nil
}

func (m MillionVerifier) Verify(ctx context.Context, cl *c.Client, cr c.Creds, email string) (c.Verification, error) {
	var r struct {
		Result     string `json:"result"`
		Subresult  string `json:"subresult"`
		Free       flex   `json:"free"`
		Role       flex   `json:"role"`
		DidYouMean string `json:"didyoumean"`
		Error      string `json:"error"`
	}
	q := url.Values{"api": {cr["api_key"]}, "email": {email}, "timeout": {"20"}}
	if _, err := getJSON(ctx, cl, "MillionVerifier", get(m.base(cl)+"/api/v3/", q), &r); err != nil {
		return c.Verification{}, err
	}
	if r.Error != "" && r.Result != "error" {
		return c.Verification{}, mvErr(r.Error)
	}
	v := c.Verification{Email: email, Reason: r.Subresult, Free: r.Free.bool(), Role: r.Role.bool(),
		Suggestion: r.DidYouMean, ProviderStatus: r.Result}
	switch r.Result {
	case "ok":
		v.Status = c.Deliverable
	case "invalid":
		v.Status = c.Undeliverable
	case "disposable":
		v.Status, v.Disposable = c.Undeliverable, true
	case "catch_all":
		v.Status, v.CatchAll = c.Risky, true
	case "error":
		if r.Error != "" {
			return c.Verification{}, mvErr(r.Error)
		}
		v.Status = c.Unknown
	default:
		v.Status = c.Unknown
	}
	if v.Reason == "" {
		v.Reason = r.Result
	}
	return v, nil
}

// ---- Bouncer ----

// Bouncer: https://docs.usebouncer.com
type Bouncer struct{}

func (Bouncer) Definition() c.Definition {
	return c.Definition{
		ID: "bouncer", Name: "Bouncer", Category: c.CategoryVerification,
		Summary:     "EU-hosted email verification with toxicity checks.",
		Description: "Verifies each address and reports catch-all, disposable and role accounts, with a 0–100 score.",
		Website:     "https://www.usebouncer.com", DocsURL: "https://docs.usebouncer.com",
		KeyURL: "https://app.usebouncer.com/api", Auth: c.AuthAPIKey,
		Fields:       apiKeyField("Bouncer → API → create an API key."),
		Capabilities: verifyCaps(), Pricing: "Pay per credit",
	}
}

func (b Bouncer) base(cl *c.Client) string { return cl.Base("bouncer", "https://api.usebouncer.com") }

func (b Bouncer) req(cl *c.Client, cr c.Creds, path string, q url.Values) *http.Request {
	req := get(b.base(cl)+path, q)
	req.Header.Set("x-api-key", cr["api_key"])
	return req
}

func (b Bouncer) Test(ctx context.Context, cl *c.Client, cr c.Creds) (c.Account, error) {
	var r struct {
		Credits flex `json:"credits"`
	}
	if _, err := getJSON(ctx, cl, "Bouncer", b.req(cl, cr, "/v1.1/credits", url.Values{}), &r); err != nil {
		return c.Account{}, err
	}
	return c.Account{Credits: r.Credits.float()}, nil
}

func (b Bouncer) Verify(ctx context.Context, cl *c.Client, cr c.Creds, email string) (c.Verification, error) {
	var r struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
		Domain struct {
			AcceptAll  flex `json:"acceptAll"`
			Disposable flex `json:"disposable"`
			Free       flex `json:"free"`
		} `json:"domain"`
		Account struct {
			Role flex `json:"role"`
		} `json:"account"`
		DidYouMean string   `json:"didYouMean"`
		Score      *float64 `json:"score"`
	}
	if _, err := getJSON(ctx, cl, "Bouncer", b.req(cl, cr, "/v1.1/email/verify", url.Values{"email": {email}, "timeout": {"20"}}), &r); err != nil {
		return c.Verification{}, err
	}
	v := c.Verification{Email: email, Reason: r.Reason, CatchAll: r.Domain.AcceptAll.bool(), Disposable: r.Domain.Disposable.bool(),
		Free: r.Domain.Free.bool(), Role: r.Account.Role.bool(), Suggestion: r.DidYouMean, ProviderStatus: r.Status}
	if r.Score != nil {
		v.Score = score(*r.Score)
	}
	switch r.Status {
	case "deliverable":
		v.Status = c.Deliverable
	case "undeliverable":
		v.Status = c.Undeliverable
	case "risky":
		v.Status = c.Risky
	default:
		v.Status = c.Unknown
	}
	return v, nil
}

// ---- Reoon ----

// Reoon: https://www.reoon.com/articles/api-documentation-of-reoon-email-verifier/
type Reoon struct{}

func (Reoon) Definition() c.Definition {
	return c.Definition{
		ID: "reoon", Name: "Reoon Email Verifier", Category: c.CategoryVerification,
		Summary:     "Low-cost verification with a power mode that checks the mailbox over SMTP.",
		Description: "Verifies each address in power mode and flags catch-all, disposable, role and full mailboxes.",
		Website:     "https://www.reoon.com/email-verifier/", DocsURL: "https://www.reoon.com/articles/api-documentation-of-reoon-email-verifier/",
		KeyURL: "https://emailverifier.reoon.com/api-settings/", Auth: c.AuthAPIKey,
		Fields:       apiKeyField("Reoon → API & Integrations → create an API key."),
		Capabilities: verifyCaps(), Pricing: "Pay per credit; unknown results are refunded",
	}
}

func (r Reoon) base(cl *c.Client) string { return cl.Base("reoon", "https://emailverifier.reoon.com") }

func (r Reoon) Test(ctx context.Context, cl *c.Client, cr c.Creds) (c.Account, error) {
	var b struct {
		Status    string  `json:"status"`
		Reason    string  `json:"reason"`
		APIStatus string  `json:"api_status"`
		Daily     float64 `json:"remaining_daily_credits"`
		Instant   float64 `json:"remaining_instant_credits"`
	}
	if _, err := getJSON(ctx, cl, "Reoon", get(r.base(cl)+"/api/v1/check-account-balance/", url.Values{"key": {cr["api_key"]}}), &b); err != nil {
		return c.Account{}, err
	}
	if b.Status != "success" {
		return c.Account{}, c.Errf(c.ErrAuth, "Reoon refused the API key: %s", b.Reason)
	}
	return c.Account{Credits: ptr(b.Daily + b.Instant), Details: map[string]string{"api_status": b.APIStatus}}, nil
}

func (r Reoon) Verify(ctx context.Context, cl *c.Client, cr c.Creds, email string) (c.Verification, error) {
	var b struct {
		Status       string   `json:"status"`
		Reason       string   `json:"reason"`
		OverallScore *float64 `json:"overall_score"`
		Disposable   bool     `json:"is_disposable"`
		Role         bool     `json:"is_role_account"`
		CatchAll     bool     `json:"is_catch_all"`
		Free         bool     `json:"is_free_email"`
	}
	q := url.Values{"key": {cr["api_key"]}, "email": {email}, "mode": {"power"}}
	if _, err := getJSON(ctx, cl, "Reoon", get(r.base(cl)+"/api/v1/verify", q), &b); err != nil {
		return c.Verification{}, err
	}
	if b.Status == "error" {
		kind := c.ErrInvalid
		if strings.Contains(strings.ToLower(b.Reason), "credit") {
			kind = c.ErrQuota
		} else if strings.Contains(strings.ToLower(b.Reason), "key") {
			kind = c.ErrAuth
		}
		return c.Verification{}, c.Errf(kind, "Reoon: %s", b.Reason)
	}
	v := c.Verification{Email: email, Reason: b.Status, Disposable: b.Disposable, Role: b.Role, CatchAll: b.CatchAll,
		Free: b.Free, ProviderStatus: b.Status}
	if b.OverallScore != nil {
		v.Score = score(*b.OverallScore)
	}
	switch b.Status {
	case "safe", "valid":
		v.Status = c.Deliverable
	case "invalid", "disabled", "spamtrap":
		v.Status = c.Undeliverable
	case "disposable":
		v.Status, v.Disposable = c.Undeliverable, true
	case "catch_all":
		v.Status, v.CatchAll = c.Risky, true
	case "role_account":
		v.Status, v.Role = c.Risky, true
	case "inbox_full":
		v.Status = c.Risky
	default:
		v.Status = c.Unknown
	}
	return v, nil
}

// ---- Emailable ----

// Emailable: https://emailable.com/docs/api
type Emailable struct{}

func (Emailable) Definition() c.Definition {
	return c.Definition{
		ID: "emailable", Name: "Emailable", Category: c.CategoryVerification,
		Summary:     "Email verification with a quality score for every address.",
		Description: "Verifies each address and returns a state, a reason and a 0–100 score.",
		Website:     "https://emailable.com", DocsURL: "https://emailable.com/docs/api",
		KeyURL: "https://app.emailable.com/api", Auth: c.AuthAPIKey,
		Fields:       apiKeyField("Emailable → API → create a private API key."),
		Capabilities: verifyCaps(), Pricing: "Pay per credit",
	}
}

func (e Emailable) base(cl *c.Client) string {
	return cl.Base("emailable", "https://api.emailable.com")
}

func (e Emailable) Test(ctx context.Context, cl *c.Client, cr c.Creds) (c.Account, error) {
	var r struct {
		OwnerEmail string  `json:"owner_email"`
		Credits    float64 `json:"available_credits"`
	}
	if _, err := getJSON(ctx, cl, "Emailable", get(e.base(cl)+"/v1/account", url.Values{"api_key": {cr["api_key"]}}), &r); err != nil {
		return c.Account{}, err
	}
	return c.Account{Email: r.OwnerEmail, Credits: ptr(r.Credits)}, nil
}

func (e Emailable) Verify(ctx context.Context, cl *c.Client, cr c.Creds, email string) (c.Verification, error) {
	var r struct {
		State      string   `json:"state"`
		Reason     string   `json:"reason"`
		Score      *float64 `json:"score"`
		Disposable bool     `json:"disposable"`
		Role       bool     `json:"role"`
		Free       bool     `json:"free"`
		AcceptAll  bool     `json:"accept_all"`
		DidYouMean *string  `json:"did_you_mean"`
	}
	q := url.Values{"api_key": {cr["api_key"]}, "email": {email}, "timeout": {"20"}}
	status, err := getJSON(ctx, cl, "Emailable", get(e.base(cl)+"/v1/verify", q), &r)
	if err != nil {
		return c.Verification{}, err
	}
	if status == 249 {
		// Still verifying: the queue retries the job later.
		return c.Verification{}, c.Errf(c.ErrUnavailable, "Emailable is still verifying %s", email)
	}
	v := c.Verification{Email: email, Reason: r.Reason, Disposable: r.Disposable, Role: r.Role, Free: r.Free,
		CatchAll: r.AcceptAll, ProviderStatus: r.State}
	if r.DidYouMean != nil {
		v.Suggestion = *r.DidYouMean
	}
	if r.Score != nil {
		v.Score = score(*r.Score)
	}
	switch r.State {
	case "deliverable":
		v.Status = c.Deliverable
	case "undeliverable":
		v.Status = c.Undeliverable
	case "risky":
		v.Status = c.Risky
	default:
		v.Status = c.Unknown
	}
	return v, nil
}

// ---- DeBounce ----

// DeBounce: https://developers.debounce.io
type DeBounce struct{}

func (DeBounce) Definition() c.Definition {
	return c.Definition{
		ID: "debounce", Name: "DeBounce", Category: c.CategoryVerification,
		Summary:     "Email verification that marks addresses safe to send, risky or invalid.",
		Description: "Verifies each address and flags spam traps, disposable, catch-all and role addresses.",
		Website:     "https://debounce.io", DocsURL: "https://developers.debounce.io",
		KeyURL: "https://app.debounce.io/api", Auth: c.AuthAPIKey,
		Fields:       apiKeyField("DeBounce → API → create an API key."),
		Capabilities: verifyCaps(), Pricing: "Pay per credit",
	}
}

func (d DeBounce) base(cl *c.Client) string { return cl.Base("debounce", "https://api.debounce.io") }

func (d DeBounce) Test(ctx context.Context, cl *c.Client, cr c.Creds) (c.Account, error) {
	var r struct {
		Balance flex `json:"balance"`
	}
	if _, err := getJSON(ctx, cl, "DeBounce", get(d.base(cl)+"/v1/balance/", url.Values{"api": {cr["api_key"]}}), &r); err != nil {
		return c.Account{}, err
	}
	credits := r.Balance.float()
	if credits == nil {
		return c.Account{}, c.Errf(c.ErrAuth, "DeBounce did not accept the API key")
	}
	return c.Account{Credits: credits}, nil
}

func (d DeBounce) Verify(ctx context.Context, cl *c.Client, cr c.Creds, email string) (c.Verification, error) {
	var r struct {
		Debounce struct {
			Code       flex   `json:"code"`
			Role       flex   `json:"role"`
			FreeEmail  flex   `json:"free_email"`
			Result     string `json:"result"`
			Reason     string `json:"reason"`
			DidYouMean string `json:"did_you_mean"`
			Error      string `json:"error"`
		} `json:"debounce"`
		Success flex `json:"success"`
	}
	if _, err := getJSON(ctx, cl, "DeBounce", get(d.base(cl)+"/v1/", url.Values{"api": {cr["api_key"]}, "email": {email}}), &r); err != nil {
		return c.Verification{}, err
	}
	db := r.Debounce
	if !r.Success.bool() {
		msg := db.Error
		if msg == "" {
			msg = db.Reason
		}
		kind := c.ErrInvalid
		if strings.Contains(strings.ToLower(msg), "key") {
			kind = c.ErrAuth
		} else if strings.Contains(strings.ToLower(msg), "credit") || strings.Contains(strings.ToLower(msg), "balance") {
			kind = c.ErrQuota
		}
		return c.Verification{}, c.Errf(kind, "DeBounce: %s", msg)
	}
	v := c.Verification{Email: email, Reason: db.Reason, Role: db.Role.bool(), Free: db.FreeEmail.bool(),
		Suggestion: db.DidYouMean, ProviderStatus: db.Result}
	switch string(db.Code) {
	case "5":
		v.Status = c.Deliverable
	case "1", "2", "6":
		v.Status = c.Undeliverable
	case "3":
		v.Status, v.Disposable = c.Undeliverable, true
	case "4":
		v.Status, v.CatchAll = c.Risky, true
	case "8":
		v.Status, v.Role = c.Risky, true
	default:
		v.Status = c.Unknown
	}
	return v, nil
}

// ---- Hunter ----

// Hunter: https://hunter.io/api-documentation/v2#email-verifier
type Hunter struct{}

func (Hunter) Definition() c.Definition {
	return c.Definition{
		ID: "hunter", Name: "Hunter", Category: c.CategoryVerification,
		Summary:     "Find and verify work emails with Hunter.",
		Description: "Verifies each address with SMTP checks and a confidence score.",
		Website:     "https://hunter.io", DocsURL: "https://hunter.io/api-documentation/v2#email-verifier",
		KeyURL: "https://hunter.io/api-keys", Auth: c.AuthAPIKey,
		Fields:       apiKeyField("Hunter → API → copy your secret key."),
		Capabilities: []string{c.CapVerify, c.CapEnrichPerson, c.CapFindEmail}, Pricing: "Monthly plans with search and verification credits",
	}
}

func (h Hunter) base(cl *c.Client) string { return cl.Base("hunter", "https://api.hunter.io") }

func (h Hunter) Test(ctx context.Context, cl *c.Client, cr c.Creds) (c.Account, error) {
	var r struct {
		Data struct {
			Email    string `json:"email"`
			PlanName string `json:"plan_name"`
			Requests struct {
				Verifications struct {
					Used      float64 `json:"used"`
					Available float64 `json:"available"`
				} `json:"verifications"`
			} `json:"requests"`
		} `json:"data"`
	}
	if _, err := getJSON(ctx, cl, "Hunter", get(h.base(cl)+"/v2/account", url.Values{"api_key": {cr["api_key"]}}), &r); err != nil {
		return c.Account{}, err
	}
	left := r.Data.Requests.Verifications.Available - r.Data.Requests.Verifications.Used
	return c.Account{Email: r.Data.Email, Credits: &left, Details: map[string]string{"plan": r.Data.PlanName}}, nil
}

func (h Hunter) Verify(ctx context.Context, cl *c.Client, cr c.Creds, email string) (c.Verification, error) {
	var r struct {
		Data struct {
			Status     string   `json:"status"`
			Result     string   `json:"result"`
			Score      *float64 `json:"score"`
			Disposable bool     `json:"disposable"`
			Webmail    bool     `json:"webmail"`
			AcceptAll  bool     `json:"accept_all"`
		} `json:"data"`
	}
	status, err := getJSON(ctx, cl, "Hunter", get(h.base(cl)+"/v2/email-verifier", url.Values{"api_key": {cr["api_key"]}, "email": {email}}), &r)
	if err != nil {
		return c.Verification{}, err
	}
	if status == http.StatusAccepted {
		return c.Verification{}, c.Errf(c.ErrUnavailable, "Hunter is still verifying %s", email)
	}
	d := r.Data
	v := c.Verification{Email: email, Reason: d.Status, Disposable: d.Disposable, Free: d.Webmail, CatchAll: d.AcceptAll,
		ProviderStatus: d.Status}
	if d.Score != nil {
		v.Score = score(*d.Score)
	}
	switch d.Status {
	case "valid", "webmail":
		v.Status = c.Deliverable
	case "invalid":
		v.Status = c.Undeliverable
	case "disposable":
		v.Status, v.Disposable = c.Undeliverable, true
	case "accept_all":
		v.Status, v.CatchAll = c.Risky, true
	default:
		v.Status = c.Unknown
	}
	return v, nil
}
