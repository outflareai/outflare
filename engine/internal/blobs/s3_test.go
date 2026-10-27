package blobs

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The expected signatures come from an independent implementation of
// AWS Signature Version 4, on the same fake keys and time.
func TestSignature(t *testing.T) {
	var (
		mu   sync.Mutex
		auth []string
		objs = map[string]string{}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		auth = append(auth, r.Header.Get("Authorization"))
		switch r.Method {
		case "PUT":
			b, _ := io.ReadAll(r.Body)
			objs[r.URL.Path] = string(b)
		case "GET":
			if r.URL.Query().Get("list-type") == "2" {
				io.WriteString(w, `<ListBucketResult><Contents><Key>events/ws/2026-10.ndjson.gz</Key></Contents><IsTruncated>false</IsTruncated></ListBucketResult>`) //nolint:errcheck
				return
			}
			v, ok := objs[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			io.WriteString(w, v) //nolint:errcheck
		case "DELETE":
			delete(objs, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	s, err := New(Config{Endpoint: srv.URL, Bucket: "bucket", AccessKey: "test_not_a_real_key", SecretKey: "test-not-a-real-secret"})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2026, 10, 27, 7, 15, 0, 0, time.UTC) }
	// Sign as if the host were storage.example.com, as the reference did.
	s.http = &http.Client{Transport: hostRewrite{to: strings.TrimPrefix(srv.URL, "http://")}}
	s.endpoint.Host = "storage.example.com"

	ctx := context.Background()
	if err := s.Put(ctx, "events/ws/2026-10.ndjson.gz", "application/gzip", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if want := "Signature=d1beea73eb47c209d0b9a8947c2e221fff68ef9eb4a86c4f78f7608639aac773"; !strings.HasSuffix(auth[0], want) {
		t.Fatalf("put signed %q", auth[0])
	}
	if !strings.Contains(auth[0], "Credential=test_not_a_real_key/20261027/auto/s3/aws4_request, SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date,") {
		t.Fatalf("put header %q", auth[0])
	}
	keys, err := s.List(ctx, "events/ws/", 0)
	if err != nil || len(keys) != 1 {
		t.Fatalf("list %v %v", keys, err)
	}
	if want := "Signature=efabc12edfe2b29317dc2eb8f1d551002ef1887b501fb7a454b21191011a05c6"; !strings.HasSuffix(auth[1], want) {
		t.Fatalf("list signed %q", auth[1])
	}
	if b, err := s.Get(ctx, "events/ws/2026-10.ndjson.gz"); err != nil || string(b) != "hello" {
		t.Fatalf("get %q %v", b, err)
	}
	if err := s.Delete(ctx, "events/ws/2026-10.ndjson.gz"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "events/ws/2026-10.ndjson.gz"); err != ErrNotFound {
		t.Fatalf("after delete: %v", err)
	}
	if err := s.Put(ctx, "../escape", "", nil); err == nil {
		t.Fatal("a key with .. was taken")
	}
}

type hostRewrite struct{ to string }

func (h hostRewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Host = h.to
	return http.DefaultTransport.RoundTrip(r)
}
