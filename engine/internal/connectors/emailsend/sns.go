package emailsend

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// snsMessage is what SNS POSTs to an HTTPS subscription.
type snsMessage struct {
	Type             string `json:"Type"`
	MessageID        string `json:"MessageId"`
	Token            string `json:"Token"`
	TopicArn         string `json:"TopicArn"`
	Subject          string `json:"Subject"`
	Message          string `json:"Message"`
	Timestamp        string `json:"Timestamp"`
	SignatureVersion string `json:"SignatureVersion"`
	Signature        string `json:"Signature"`
	SigningCertURL   string `json:"SigningCertURL"`
	SubscribeURL     string `json:"SubscribeURL"`
}

// Only certificates and confirmations from SNS itself are trusted.
var snsHost = regexp.MustCompile(`^sns\.[a-z0-9-]+\.amazonaws\.com(\.cn)?$`)

func trustedSNS(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && snsHost.MatchString(u.Hostname())
}

var (
	certMu    sync.Mutex
	certCache = map[string]*x509.Certificate{}
)

// SkipSNSSignature turns off signature checks, for tests only.
var SkipSNSSignature = false

// verifySNS checks that body is a genuine SNS message, signed with the
// certificate SNS names.
func verifySNS(ctx context.Context, cl *c.Client, body []byte) (snsMessage, error) {
	var m snsMessage
	if err := json.Unmarshal(body, &m); err != nil || m.Type == "" {
		return m, c.Errf(c.ErrInvalid, "not an SNS message")
	}
	if SkipSNSSignature {
		return m, nil
	}
	if !trustedSNS(m.SigningCertURL) {
		return m, c.Errf(c.ErrAuth, "the SNS signing certificate is not from amazonaws.com")
	}
	cert, err := snsCert(ctx, cl, m.SigningCertURL)
	if err != nil {
		return m, err
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return m, c.Errf(c.ErrAuth, "the SNS signing certificate has no RSA key")
	}
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil {
		return m, c.Errf(c.ErrAuth, "the SNS signature is not base64")
	}
	msg := snsStringToSign(m)
	switch m.SignatureVersion {
	case "1":
		h := sha1.Sum([]byte(msg))
		err = rsa.VerifyPKCS1v15(pub, crypto.SHA1, h[:], sig)
	case "2":
		h := sha256.Sum256([]byte(msg))
		err = rsa.VerifyPKCS1v15(pub, crypto.SHA256, h[:], sig)
	default:
		return m, c.Errf(c.ErrAuth, "unknown SNS signature version %q", m.SignatureVersion)
	}
	if err != nil {
		return m, c.Errf(c.ErrAuth, "the SNS signature does not match")
	}
	return m, nil
}

// snsStringToSign builds the text SNS signs: chosen fields, each name then
// value on its own line, in a fixed order.
func snsStringToSign(m snsMessage) string {
	var b strings.Builder
	add := func(k, v string) { b.WriteString(k + "\n" + v + "\n") }
	add("Message", m.Message)
	add("MessageId", m.MessageID)
	if m.Type == "Notification" {
		if m.Subject != "" {
			add("Subject", m.Subject)
		}
		add("Timestamp", m.Timestamp)
		add("TopicArn", m.TopicArn)
		add("Type", m.Type)
		return b.String()
	}
	add("SubscribeURL", m.SubscribeURL)
	add("Timestamp", m.Timestamp)
	add("Token", m.Token)
	add("TopicArn", m.TopicArn)
	add("Type", m.Type)
	return b.String()
}

func snsCert(ctx context.Context, cl *c.Client, raw string) (*x509.Certificate, error) {
	certMu.Lock()
	cert, ok := certCache[raw]
	certMu.Unlock()
	if ok {
		return cert, nil
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	resp, err := cl.Do(req)
	if err != nil {
		return nil, c.Errf(c.ErrUnavailable, "could not fetch the SNS signing certificate: %v", err)
	}
	defer resp.Body.Close()
	pemBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, c.Errf(c.ErrUnavailable, "the SNS signing certificate is not PEM")
	}
	cert, err = x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, c.Errf(c.ErrUnavailable, "the SNS signing certificate does not parse")
	}
	certMu.Lock()
	certCache[raw] = cert
	certMu.Unlock()
	return cert, nil
}

// confirmSNS visits the subscription's confirmation link, which is how an
// HTTPS endpoint accepts a new SNS subscription.
func confirmSNS(ctx context.Context, cl *c.Client, subscribeURL string) error {
	if !SkipSNSSignature && !trustedSNS(subscribeURL) {
		return c.Errf(c.ErrAuth, "the SNS confirmation link is not on amazonaws.com")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, subscribeURL, nil)
	resp, err := cl.Do(req)
	if err != nil {
		return c.Errf(c.ErrUnavailable, "could not confirm the SNS subscription: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return c.Errf(c.ErrUnavailable, "SNS refused the subscription confirmation (HTTP %d)", resp.StatusCode)
	}
	return nil
}
