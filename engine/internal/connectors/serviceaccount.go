package connectors

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ServiceAccountField is what Google connectors ask for when someone
// connects with a service account key instead of signing in.
var ServiceAccountField = Field{
	Name: "service_account_json", Label: "Service account key (JSON)", Secret: true,
	Placeholder: `{"type": "service_account", ...}`,
	Help:        "In Google Cloud: IAM & Admin, Service accounts, Keys, Add key, JSON. Then give the service account's email read access in the Google product.",
}

type serviceAccountKey struct {
	Type        string `json:"type"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

type cachedToken struct {
	token   string
	expires time.Time
}

var saTokens sync.Map // key hash + scopes -> cachedToken

// ServiceAccountCreds signs in as a Google service account (the JWT bearer
// grant) and returns creds with a short-lived access_token beside the key,
// plus service_account_email. Tokens are cached in memory until shortly
// before they expire, never stored.
func ServiceAccountCreds(ctx context.Context, cl *Client, d Definition, creds Creds) (Creds, error) {
	scopes := []string{}
	if d.OAuth != nil {
		for _, s := range d.OAuth.Scopes {
			if strings.HasPrefix(s, "https://") {
				scopes = append(scopes, s)
			}
		}
	}
	tok, err := ServiceAccountToken(ctx, cl, creds["service_account_json"], scopes, "")
	if err != nil {
		return nil, err
	}
	out := Creds{}
	for k, v := range creds {
		out[k] = v
	}
	out["service_account_email"] = tok.ClientEmail
	out["access_token"] = tok.AccessToken
	out["expires_at"] = strconv.FormatInt(tok.Expires.Unix(), 10)
	return out, nil
}

// SAToken is an access token from a Google service account.
type SAToken struct {
	AccessToken string
	ClientEmail string
	Expires     time.Time
}

// ServiceAccountToken gets an access token for a Google service account
// key with scopes. With subject it acts as that user of the organisation
// through domain-wide delegation (a Google Workspace mailbox). Tokens are
// cached in memory until shortly before they expire, never stored.
func ServiceAccountToken(ctx context.Context, cl *Client, keyJSON string, scopes []string, subject string) (SAToken, error) {
	var key serviceAccountKey
	if err := json.Unmarshal([]byte(keyJSON), &key); err != nil || key.Type != "service_account" || key.ClientEmail == "" || key.PrivateKey == "" {
		return SAToken{}, Errf(ErrInvalid, "that is not a Google service account key: paste the whole JSON file Google Cloud downloaded")
	}
	subject = strings.ToLower(strings.TrimSpace(subject))
	sum := sha256.Sum256([]byte(keyJSON + "\n" + strings.Join(scopes, " ") + "\n" + subject))
	cacheKey := hex.EncodeToString(sum[:])
	if t, ok := saTokens.Load(cacheKey); ok && time.Until(t.(cachedToken).expires) > 5*time.Minute {
		return SAToken{AccessToken: t.(cachedToken).token, ClientEmail: key.ClientEmail, Expires: t.(cachedToken).expires}, nil
	}
	block, _ := pem.Decode([]byte(key.PrivateKey))
	if block == nil {
		return SAToken{}, Errf(ErrInvalid, "the service account key's private_key is not readable")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		if parsed, err = x509.ParsePKCS1PrivateKey(block.Bytes); err != nil {
			return SAToken{}, Errf(ErrInvalid, "the service account key's private_key is not readable")
		}
	}
	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return SAToken{}, Errf(ErrInvalid, "the service account key is not an RSA key")
	}
	tokenURI := key.TokenURI
	if tokenURI == "" {
		tokenURI = "https://oauth2.googleapis.com/token"
	}
	now := time.Now()
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	claims := map[string]any{
		"iss": key.ClientEmail, "scope": strings.Join(scopes, " "), "aud": tokenURI,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	}
	if subject != "" {
		claims["sub"] = subject
	}
	unsigned := enc(map[string]string{"alg": "RS256", "typ": "JWT"}) + "." + enc(claims)
	digest := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA256, digest[:])
	if err != nil {
		return SAToken{}, Errf(ErrInvalid, "could not sign with the service account key")
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, cl.Base("google.token", tokenURI), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var t tokenResponse
	if _, err := DoJSON(ctx, cl, "Google", req, &t); err != nil {
		if KindOf(err) == ErrAuth || KindOf(err) == ErrInvalid {
			if subject != "" {
				return SAToken{}, Errf(ErrAuth, "Google refused the service account for %s. Check the key is still active and has domain-wide delegation for its scopes in the Google Admin console.", subject)
			}
			return SAToken{}, Errf(ErrAuth, "Google refused the service account key. Check it was not deleted or disabled in Google Cloud.")
		}
		return SAToken{}, err
	}
	if t.AccessToken == "" {
		return SAToken{}, Errf(ErrAuth, "Google did not return a token for the service account")
	}
	exp := now.Add(time.Duration(t.ExpiresIn) * time.Second)
	if t.ExpiresIn == 0 {
		exp = now.Add(time.Hour)
	}
	saTokens.Store(cacheKey, cachedToken{token: t.AccessToken, expires: exp})
	return SAToken{AccessToken: t.AccessToken, ClientEmail: key.ClientEmail, Expires: exp}, nil
}
