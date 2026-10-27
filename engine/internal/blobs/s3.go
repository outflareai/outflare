// Package blobs stores files in an S3-compatible bucket: Cloudflare R2,
// Amazon S3, MinIO and the like (docs/adr/0010-analytics-storage.md). It
// holds what doesn't belong in Postgres, such as archived product events
// and session recordings. Requests are signed with AWS Signature Version
// 4 from the standard library, so no SDK reads credentials from anywhere
// but the settings it is given.
package blobs

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ErrNotFound is returned for a key the bucket doesn't have.
var ErrNotFound = errors.New("blobs: not found")

// Store is one bucket.
type Store struct {
	endpoint  *url.URL
	region    string
	bucket    string
	accessKey string
	secretKey string
	http      *http.Client
	now       func() time.Time
}

// Config says where the bucket is.
type Config struct {
	Endpoint  string // https://<account>.r2.cloudflarestorage.com, https://s3.<region>.amazonaws.com
	Region    string // "auto" for R2
	Bucket    string
	AccessKey string
	SecretKey string
}

// New makes a store. Requests use path-style addressing
// (endpoint/bucket/key), which R2, MinIO and S3 all accept.
func New(c Config) (*Store, error) {
	u, err := url.Parse(strings.TrimRight(c.Endpoint, "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("blobs: bad endpoint %q", c.Endpoint)
	}
	if c.Bucket == "" || c.AccessKey == "" || c.SecretKey == "" {
		return nil, errors.New("blobs: bucket and keys are required")
	}
	region := c.Region
	if region == "" {
		region = "auto"
	}
	return &Store{endpoint: u, region: region, bucket: c.Bucket, accessKey: c.AccessKey, secretKey: c.SecretKey,
		http: &http.Client{Timeout: 5 * time.Minute}, now: time.Now}, nil
}

// Put writes body under key.
func (s *Store) Put(ctx context.Context, key, contentType string, body []byte) error {
	h := http.Header{}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	resp, err := s.do(ctx, "PUT", key, nil, h, body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Get reads key.
func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	resp, err := s.do(ctx, "GET", key, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// Delete removes key. A missing key is not an error.
func (s *Store) Delete(ctx context.Context, key string) error {
	resp, err := s.do(ctx, "DELETE", key, nil, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// List returns the keys under prefix, up to limit.
func (s *Store) List(ctx context.Context, prefix string, limit int) ([]string, error) {
	var keys []string
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		resp, err := s.do(ctx, "GET", "", q, nil, nil)
		if err != nil {
			return nil, err
		}
		var out struct {
			Contents []struct {
				Key string `xml:"Key"`
			} `xml:"Contents"`
			Truncated bool   `xml:"IsTruncated"`
			Next      string `xml:"NextContinuationToken"`
		}
		err = xml.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("blobs: list: %w", err)
		}
		for _, c := range out.Contents {
			keys = append(keys, c.Key)
			if limit > 0 && len(keys) >= limit {
				return keys, nil
			}
		}
		if !out.Truncated || out.Next == "" {
			return keys, nil
		}
		token = out.Next
	}
}

// Check confirms the bucket can be written, read and deleted.
func (s *Store) Check(ctx context.Context) error {
	key := ".outflare-check"
	if err := s.Put(ctx, key, "text/plain", []byte("ok")); err != nil {
		return err
	}
	if _, err := s.Get(ctx, key); err != nil {
		return err
	}
	return s.Delete(ctx, key)
}

func (s *Store) do(ctx context.Context, method, key string, query url.Values, h http.Header, body []byte) (*http.Response, error) {
	if key != "" && (!keyRe.MatchString(key) || strings.Contains(key, "..") || strings.Contains(key, "//")) {
		return nil, fmt.Errorf("blobs: key %q has characters keys may not use", key)
	}
	u := *s.endpoint
	u.Path = strings.TrimRight(u.Path, "/") + "/" + s.bucket
	if key != "" {
		u.Path += "/" + key
	}
	u.RawQuery = canonicalQuery(query)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range h {
		req.Header[k] = v
	}
	s.sign(req, body)
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("blobs: %s: %w", method, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, ErrNotFound
	}
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1000))
		resp.Body.Close()
		return nil, fmt.Errorf("blobs: %s %s: %s: %s", method, key, resp.Status, strings.TrimSpace(string(msg)))
	}
	return resp, nil
}

// sign adds an AWS Signature Version 4 to req.
func (s *Store) sign(req *http.Request, body []byte) {
	now := s.now().UTC()
	amzDate := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	payload := sha256Hex(body)
	req.Header.Set("Host", req.URL.Host)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payload)

	names := make([]string, 0, len(req.Header))
	for k := range req.Header {
		names = append(names, strings.ToLower(k))
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, n := range names {
		v := req.Header.Get(n)
		if n == "host" {
			v = req.URL.Host
		}
		canonHeaders.WriteString(n + ":" + strings.TrimSpace(v) + "\n")
	}
	signed := strings.Join(names, ";")
	canonical := strings.Join([]string{req.Method, req.URL.EscapedPath(), req.URL.RawQuery, canonHeaders.String(), signed, payload}, "\n")
	scope := day + "/" + s.region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonical))
	key := hmacSHA256([]byte("AWS4"+s.secretKey), day)
	key = hmacSHA256(key, s.region)
	key = hmacSHA256(key, "s3")
	key = hmacSHA256(key, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(key, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.accessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
	req.Header.Del("Host") // net/http sends req.Host
}

// keyRe is what a key may hold, so its path needs no escaping to be signed.
var keyRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9/_.=-]{0,500}$`)

// canonicalQuery encodes query sorted, with %20 for spaces, as SigV4 signs it.
func canonicalQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, awsEscape(k)+"="+awsEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

func awsEscape(s string) string {
	return strings.NewReplacer("+", "%20", "%7E", "~").Replace(url.QueryEscape(s))
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}
