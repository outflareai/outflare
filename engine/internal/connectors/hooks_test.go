package connectors

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func mac(secret, msg string) []byte {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(msg))
	return m.Sum(nil)
}

func TestVerifyHMAC(t *testing.T) {
	body := []byte(`{"event":"batch_completed"}`)
	keys := []string{"whsec_test_a", "whsec_test_b"}
	ok := map[string]struct{ sig, ts string }{
		"hex":             {hex.EncodeToString(mac("whsec_test_b", string(body))), ""},
		"prefixed":        {"sha256=" + hex.EncodeToString(mac("whsec_test_a", string(body))), ""},
		"base64":          {base64.StdEncoding.EncodeToString(mac("whsec_test_a", string(body))), ""},
		"timestamped":     {hex.EncodeToString(mac("whsec_test_a", "1761566400."+string(body))), "1761566400"},
		"t and v1 header": {"t=1761566400,v1=" + hex.EncodeToString(mac("whsec_test_b", "1761566400."+string(body))), ""},
	}
	for name, c := range ok {
		if err := VerifyHMAC(keys, c.sig, c.ts, body); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	bad := map[string]string{
		"missing":       "",
		"other secret":  hex.EncodeToString(mac("whsec_test_c", string(body))),
		"other body":    hex.EncodeToString(mac("whsec_test_a", `{"event":"crawl_completed"}`)),
		"not a digest":  "zzz",
		"empty secrets": hex.EncodeToString(mac("", string(body))),
	}
	for name, sig := range bad {
		k := keys
		if name == "empty secrets" {
			k = []string{""}
		}
		if err := VerifyHMAC(k, sig, "", body); KindOf(err) != ErrAuth {
			t.Errorf("%s: %v, want an auth error", name, err)
		}
	}
}
