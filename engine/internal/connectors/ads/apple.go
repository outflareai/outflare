package ads

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// AppleAds reads Apple Ads (formerly Apple Search Ads) through the Apple
// Ads Platform API (v1), which replaces the Campaign Management API v5.
// The workspace connects its own API user: it uploads a public key in
// Apple Ads (Account Settings, API) and pastes the client id, team id and
// key id Apple shows, plus the private key. Outflare signs a client secret
// (an ES256 JWT) with that key and trades it for an access token with the
// client credentials grant.
//
// It only reads. Apple Ads sends people to the App Store, and installs are
// measured in the app through AdServices, so there is nothing to send back.
type AppleAds struct{}

const (
	appleAPIBase   = "https://api.ads.apple.com"
	appleTokenBase = "https://appleid.apple.com"
	appleAudience  = "https://appleid.apple.com"
	appleScope     = "searchadsorg"
	// appleReportDays is the longest range a DAILY report takes.
	appleReportDays = 90
	// applePageSize is the most a campaign query (1000) and a report
	// (5000) both take.
	applePageSize = 1000
)

func (AppleAds) Definition() c.Definition {
	d := def("apple_ads", "Apple Ads", "https://ads.apple.com",
		"https://developer.apple.com/documentation/apple-ads-platform-api", "https://ads.apple.com",
		"Spend, impressions, taps and installs per campaign from your Apple Ads (formerly Apple Search Ads) accounts.",
		"Uses your own Apple Ads API user. In Apple Ads, invite a user with an API role (API Account Read Only is enough), sign in as that user, upload a public key under Account Settings, API, and paste the client id, team id and key id Apple shows there, with the private key. Read-only: installs are measured in your app, so there is nothing to send back.")
	d.Auth = c.AuthAPIKey
	d.Fields = []c.Field{
		{Name: "client_id", Label: "Client ID", Placeholder: "SEARCHADS.…", Help: "From Account Settings, API in Apple Ads, after uploading the public key."},
		{Name: "team_id", Label: "Team ID", Placeholder: "SEARCHADS.…"},
		{Name: "key_id", Label: "Key ID"},
		{Name: "private_key", Label: "Private key (PEM)", Secret: true, Placeholder: "-----BEGIN EC PRIVATE KEY-----",
			Help: "The P-256 key whose public half you uploaded (openssl ecparam -genkey -name prime256v1 -noout -out private-key.pem). Paste the whole file."},
	}
	return d
}

func (AppleAds) base(cl *c.Client) string { return cl.Base("apple_ads", appleAPIBase) + "/v1" }

// ---- access token ----

type appleToken struct {
	token   string
	expires time.Time
}

// appleTokens caches access tokens in memory by a hash of the credentials,
// until shortly before they expire. They are never stored.
var appleTokens sync.Map

func appleCacheKey(creds c.Creds) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{creds["client_id"], creds["team_id"], creds["key_id"], creds["private_key"]}, "\n")))
	return hex.EncodeToString(sum[:])
}

var pemArmor = regexp.MustCompile(`-----(BEGIN|END)[A-Z ]*-----`)

// appleKey reads the pasted private key. It takes a PEM file (SEC 1 or
// PKCS #8) and copes with the line breaks a single-line field removes.
func appleKey(s string) (*ecdsa.PrivateKey, error) {
	body := pemArmor.ReplaceAllString(s, "")
	body = strings.Join(strings.Fields(body), "")
	der, err := base64.StdEncoding.DecodeString(body)
	if err != nil || len(der) == 0 {
		return nil, c.Errf(c.ErrInvalid, "the Apple Ads private key is not readable: paste the whole private-key.pem file, including its BEGIN and END lines")
	}
	if k, err := x509.ParseECPrivateKey(der); err == nil {
		if k.Curve != elliptic.P256() {
			return nil, c.Errf(c.ErrInvalid, "the Apple Ads private key must be a P-256 (prime256v1) key")
		}
		return k, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, c.Errf(c.ErrInvalid, "the Apple Ads private key is not readable: paste the whole private-key.pem file, including its BEGIN and END lines")
	}
	k, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || k.Curve != elliptic.P256() {
		return nil, c.Errf(c.ErrInvalid, "the Apple Ads private key must be a P-256 (prime256v1) key")
	}
	return k, nil
}

// appleClientSecret signs the client secret: an ES256 JWT with the key id
// in its header, the team id as issuer and the client id as subject.
func appleClientSecret(creds c.Creds, now time.Time) (string, error) {
	key, err := appleKey(creds["private_key"])
	if err != nil {
		return "", err
	}
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	unsigned := enc(map[string]string{"alg": "ES256", "kid": strings.TrimSpace(creds["key_id"])}) + "." + enc(map[string]any{
		"sub": strings.TrimSpace(creds["client_id"]), "iss": strings.TrimSpace(creds["team_id"]), "aud": appleAudience,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	digest := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", c.Errf(c.ErrInvalid, "could not sign with the Apple Ads private key")
	}
	// JWS takes the signature as r and s, 32 bytes each.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// token returns an access token for creds, from the cache while it lasts.
func (AppleAds) token(ctx context.Context, cl *c.Client, creds c.Creds) (string, error) {
	for _, f := range []string{"client_id", "team_id", "key_id", "private_key"} {
		if strings.TrimSpace(creds[f]) == "" {
			return "", c.Errf(c.ErrInvalid, "Apple Ads needs the client id, team id, key id and private key from Account Settings, API")
		}
	}
	cacheKey := appleCacheKey(creds)
	if t, ok := appleTokens.Load(cacheKey); ok && time.Until(t.(appleToken).expires) > 5*time.Minute {
		return t.(appleToken).token, nil
	}
	now := time.Now()
	secret, err := appleClientSecret(creds, now)
	if err != nil {
		return "", err
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {strings.TrimSpace(creds["client_id"])},
		"client_secret": {secret}, "scope": {appleScope}}
	req, _ := http.NewRequest(http.MethodPost, cl.Base("apple_ads.token", appleTokenBase)+"/auth/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if status, err := c.DoJSON(ctx, cl, "Apple Ads", req, &t); err != nil {
		// A wrong id, key or signature comes back as 400 invalid_client.
		if status == 400 || status == 401 || status == 403 {
			return "", c.Errf(c.ErrAuth, "Apple refused the Apple Ads API credentials (HTTP %d). Check the client id, team id and key id match the public key uploaded under Account Settings, API, and that the private key is its pair.", status)
		}
		return "", err
	}
	if t.AccessToken == "" {
		return "", c.Errf(c.ErrAuth, "Apple did not return an Apple Ads access token")
	}
	life := time.Duration(t.ExpiresIn) * time.Second
	if t.ExpiresIn <= 0 {
		life = time.Hour
	}
	appleTokens.Store(cacheKey, appleToken{token: t.AccessToken, expires: now.Add(life)})
	return t.AccessToken, nil
}

// apiError is the error an answer can carry beside or instead of result.
type appleError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// do sends one API call, scoped to an ad account when account is set.
func (a AppleAds) do(ctx context.Context, cl *c.Client, creds c.Creds, method, path, account string, body, out any) error {
	tok, err := a.token(ctx, cl, creds)
	if err != nil {
		return err
	}
	k := newCall(cl, "Apple Ads").bearer(tok)
	if account != "" {
		k.set("X-AP-Context", "adAccountId="+account)
	}
	var raw json.RawMessage
	if err := k.do(ctx, method, a.base(cl)+path, body, &raw); err != nil {
		return err
	}
	var env struct {
		Error *appleError `json:"error"`
	}
	if json.Unmarshal(raw, &env) == nil && env.Error != nil && (env.Error.Code != "" || env.Error.Message != "") {
		return c.Errf(c.ErrInvalid, "Apple Ads refused the request: %s %s", env.Error.Code, env.Error.Message)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return c.Errf(c.ErrUnavailable, "Apple Ads returned something unexpected")
		}
	}
	return nil
}

type appleID int64

func (i appleID) String() string { return strconv.FormatInt(int64(i), 10) }

type appleMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type applePagination struct {
	Offset     int `json:"offset"`
	PageSize   int `json:"pageSize"`
	TotalCount int `json:"totalCount"`
}

// more says whether another page follows one of n rows at offset.
func (p applePagination) more(offset, n int) bool {
	return n > 0 && offset+n < p.TotalCount
}

// ---- reading ----

func (a AppleAds) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var me struct {
		Result struct {
			UserID appleID `json:"userId"`
			OrgID  appleID `json:"orgId"`
		} `json:"result"`
	}
	if err := a.do(ctx, cl, creds, http.MethodGet, "/me", "", nil, &me); err != nil {
		return c.Account{}, err
	}
	accts, err := a.AdAccounts(ctx, cl, creds)
	if err != nil {
		return c.Account{}, err
	}
	acct := c.Account{ID: me.Result.OrgID.String(), Details: map[string]string{"accounts": fmt.Sprint(len(accts))}}
	var org struct {
		Result struct {
			Name string `json:"name"`
		} `json:"result"`
	}
	if a.do(ctx, cl, creds, http.MethodGet, "/orgs/"+esc(acct.ID), "", nil, &org) == nil {
		acct.Name = org.Result.Name
	}
	return acct, nil
}

// AdAccounts lists the ad accounts the API user can reach (GET /v1/acls),
// then reads each one for its currency, time zone and whether it runs App
// Store or Apple Maps campaigns.
func (a AppleAds) AdAccounts(ctx context.Context, cl *c.Client, creds c.Creds) ([]c.AdAccount, error) {
	var acls struct {
		Result struct {
			ACLs []struct {
				AdAccount struct {
					ID    appleID `json:"id"`
					Name  string  `json:"name"`
					OrgID appleID `json:"orgId"`
				} `json:"adAccount"`
			} `json:"acls"`
		} `json:"result"`
	}
	if err := a.do(ctx, cl, creds, http.MethodGet, "/acls", "", nil, &acls); err != nil {
		return nil, err
	}
	seen := map[appleID]bool{}
	var out []c.AdAccount
	for _, e := range acls.Result.ACLs {
		id := e.AdAccount.ID
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		var full struct {
			Result struct {
				Name            string   `json:"name"`
				Currency        string   `json:"currency"`
				Timezone        string   `json:"timezone"`
				SystemStatus    string   `json:"systemStatus"`
				ProductFeatures []string `json:"productFeatures"`
			} `json:"result"`
		}
		if err := a.do(ctx, cl, creds, http.MethodGet, "/ad-accounts/"+esc(id.String()), id.String(), nil, &full); err != nil {
			return nil, err
		}
		name := full.Result.Name
		if name == "" {
			name = e.AdAccount.Name
		}
		out = append(out, c.AdAccount{ID: id.String(), Name: name, Currency: strings.ToUpper(full.Result.Currency), TimeZone: full.Result.Timezone,
			Meta: map[string]string{"org_id": e.AdAccount.OrgID.String(), "reports": appleReports(full.Result.ProductFeatures)}})
	}
	return out, nil
}

// appleReports is the report family an account's campaigns are in:
// business-brands for Apple Maps, apps for the App Store.
func appleReports(features []string) string {
	for _, f := range features {
		if f == "BUSINESS_BRAND_MANUAL" {
			return "business-brands"
		}
	}
	return "apps"
}

// AdCampaigns queries the account's campaigns (POST /v1/campaigns/query),
// which leaves out deleted ones; the report still names those.
func (a AppleAds) AdCampaigns(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount) ([]c.AdCampaign, error) {
	var out []c.AdCampaign
	for page, offset := 0, 0; page < maxPages; page++ {
		var resp struct {
			Result []struct {
				ID            appleID `json:"id"`
				Name          string  `json:"name"`
				Status        string  `json:"status"`
				DisplayStatus string  `json:"displayStatus"`
				Deleted       bool    `json:"deleted"`
				DailyBudget   *struct {
					Value appleMoney `json:"value"`
				} `json:"dailyBudget"`
				BidStrategy struct {
					Goal string `json:"bidStrategyGoal"`
				} `json:"bidStrategy"`
			} `json:"result"`
			Pagination applePagination `json:"pagination"`
		}
		body := map[string]any{"pagination": map[string]any{"offset": offset, "pageSize": applePageSize, "fetchTotalCount": true}}
		if err := a.do(ctx, cl, creds, http.MethodPost, "/campaigns/query", acct.ID, body, &resp); err != nil {
			return nil, err
		}
		for _, cp := range resp.Result {
			word := cp.DisplayStatus
			if word == "" {
				word = cp.Status
			}
			st := appleStatus(cp.Status, cp.DisplayStatus, cp.Deleted)
			camp := c.AdCampaign{ID: cp.ID.String(), Name: cp.Name, Status: st, ProviderStatus: word, Objective: strings.ToLower(cp.BidStrategy.Goal)}
			if cp.DailyBudget != nil {
				if m := c.MicrosOf(cp.DailyBudget.Value.Amount); m > 0 {
					camp.DailyBudgetMicros = ptrI(m)
				}
			}
			out = append(out, camp)
		}
		if !resp.Pagination.more(offset, len(resp.Result)) {
			break
		}
		offset += len(resp.Result)
	}
	return out, nil
}

// appleStatus maps a campaign onto the shared statuses: deleted first,
// then the advertiser's own switch (ENABLED or PAUSED).
func appleStatus(st, display string, deleted bool) string {
	if deleted || strings.EqualFold(display, "DELETED") {
		return c.AdRemoved
	}
	if strings.EqualFold(display, "ON_HOLD") {
		return c.AdPaused
	}
	return status(st)
}

// appleMetrics are one row's or one day's numbers in either report.
type appleMetrics struct {
	Date          string     `json:"date"`
	LocalSpend    appleMoney `json:"localSpend"`
	Impressions   flexInt    `json:"impressions"`
	Taps          flexInt    `json:"taps"`
	TotalInstalls *flexNum   `json:"totalInstalls"`
}

// AdDays reads the campaigns report (POST /v1/reports/{apps|business-
// brands}/campaigns/query) by day, in the account's own time zone
// (ORTZ). A DAILY report covers at most 90 days, so longer ranges go in
// windows; a single day has no daily breakdown and comes in the row's
// totals. Taps are the clicks, and for App Store campaigns total installs
// (tap and view-through) are the conversions.
func (a AppleAds) AdDays(ctx context.Context, cl *c.Client, creds c.Creds, acct c.AdAccount, from, to string) ([]c.AdDay, error) {
	family := acct.Meta["reports"]
	if family != "business-brands" {
		family = "apps"
	}
	all := days(from, to)
	var out []c.AdDay
	for start := 0; start < len(all); start += appleReportDays {
		window := all[start:min(start+appleReportDays, len(all))]
		tr := map[string]any{"start": window[0], "end": window[len(window)-1], "timeZone": "ORTZ"}
		if len(window) > 1 {
			tr["granularity"] = "DAILY"
		}
		for page, offset := 0, 0; page < maxPages; page++ {
			var resp struct {
				Result struct {
					Rows []struct {
						Metadata struct {
							ID   appleID `json:"id"`
							Name string  `json:"name"`
						} `json:"metadata"`
						TotalMetrics    appleMetrics   `json:"totalMetrics"`
						GranularMetrics []appleMetrics `json:"granularMetrics"`
					} `json:"rows"`
				} `json:"result"`
				Pagination applePagination `json:"pagination"`
			}
			body := map[string]any{"timeRange": tr, "pagination": map[string]any{"offset": offset, "pageSize": applePageSize}}
			if err := a.do(ctx, cl, creds, http.MethodPost, "/reports/"+family+"/campaigns/query", acct.ID, body, &resp); err != nil {
				return nil, err
			}
			for _, row := range resp.Result.Rows {
				points := row.GranularMetrics
				if len(window) == 1 {
					t := row.TotalMetrics
					t.Date = window[0]
					points = []appleMetrics{t}
				}
				for _, p := range points {
					spend := c.MicrosOf(p.LocalSpend.Amount)
					if len(p.Date) < 10 || spend == 0 && p.Impressions == 0 && p.Taps == 0 {
						continue
					}
					day := c.AdDay{CampaignID: row.Metadata.ID.String(), CampaignName: row.Metadata.Name, Date: p.Date[:10],
						SpendMicros: spend, Impressions: int64(p.Impressions), Clicks: int64(p.Taps)}
					if family == "apps" && p.TotalInstalls != nil {
						day.Conversions = ptrF(float64(*p.TotalInstalls))
					}
					out = append(out, day)
				}
			}
			if !resp.Pagination.more(offset, len(resp.Result.Rows)) {
				break
			}
			offset += len(resp.Result.Rows)
		}
	}
	return out, nil
}
