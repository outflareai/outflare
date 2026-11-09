package appsignin

import (
	"encoding/base64"
	"testing"
)

// The S256 challenge is what RFC 7636 appendix B computes for its verifier.
func TestChallenge(t *testing.T) {
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const want = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := Challenge(verifier); got != want {
		t.Fatalf("challenge: %s, want %s", got, want)
	}
	b, err := DecodeChallenge(want)
	if err != nil || len(b) != 32 {
		t.Fatalf("decode: %v %d", err, len(b))
	}
	for _, bad := range []string{"", "not base64!", base64.RawURLEncoding.EncodeToString([]byte("short")), want + "="} {
		if _, err := DecodeChallenge(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
