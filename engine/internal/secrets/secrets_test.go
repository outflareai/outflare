package secrets

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func newKey() string {
	k := make([]byte, 32)
	rand.Read(k)
	return base64.StdEncoding.EncodeToString(k)
}

func TestRoundTripAndBinding(t *testing.T) {
	b, err := NewBox(newKey())
	if err != nil {
		t.Fatal(err)
	}
	ct := b.Seal([]byte("app-password"), []byte("mailbox:1"))
	pt, err := b.Open(ct, []byte("mailbox:1"))
	if err != nil || !bytes.Equal(pt, []byte("app-password")) {
		t.Fatalf("open: %q %v", pt, err)
	}
	if _, err := b.Open(ct, []byte("mailbox:2")); err == nil {
		t.Fatal("ciphertext opened under a different context")
	}
	other, _ := NewBox(newKey())
	if _, err := other.Open(ct, []byte("mailbox:1")); err == nil {
		t.Fatal("ciphertext opened with a different key")
	}
}

func TestBadKeys(t *testing.T) {
	for _, k := range []string{"", "not base64!!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if _, err := NewBox(k); err == nil {
			t.Errorf("key %q accepted", k)
		}
	}
}

func TestSign(t *testing.T) {
	a, _ := NewBox(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	k := make([]byte, 32)
	k[0] = 1
	b, _ := NewBox(base64.StdEncoding.EncodeToString(k))
	sig := a.Sign("unsubscribe", "abc")
	if !a.Verify("unsubscribe", "abc", sig) || a.Verify("unsubscribe", "abd", sig) || a.Verify("other", "abc", sig) || b.Verify("unsubscribe", "abc", sig) {
		t.Fatal("signature checks wrong")
	}
}

func TestRotation(t *testing.T) {
	oldKey, newKeyB64 := newKey(), newKey()
	old, _ := NewBox(oldKey)
	ct := old.Seal([]byte("api-key"), []byte("integration:1"))
	sig := old.Sign("unsubscribe", "abc")

	b, err := NewBox(newKeyB64, oldKey)
	if err != nil || !b.Rotating() {
		t.Fatal(err)
	}
	if pt, err := b.Open(ct, []byte("integration:1")); err != nil || string(pt) != "api-key" {
		t.Fatalf("old value: %q %v", pt, err)
	}
	if !b.Verify("unsubscribe", "abc", sig) {
		t.Fatal("old signature refused while the old key is listed")
	}
	fresh, changed, err := b.Reseal(ct, []byte("integration:1"))
	if err != nil || !changed {
		t.Fatalf("reseal: %v %v", changed, err)
	}
	if _, changed, _ := b.Reseal(fresh, []byte("integration:1")); changed {
		t.Fatal("resealed a value under the current key")
	}
	// Once the old key is dropped, the resealed value still opens.
	onlyNew, _ := NewBox(newKeyB64)
	if pt, err := onlyNew.Open(fresh, []byte("integration:1")); err != nil || string(pt) != "api-key" {
		t.Fatalf("after dropping the old key: %q %v", pt, err)
	}
	if onlyNew.Verify("unsubscribe", "abc", sig) {
		t.Fatal("old signature accepted after the old key was dropped")
	}
	if _, err := NewBox(newKeyB64, "not base64!!"); err == nil {
		t.Fatal("bad previous key accepted")
	}
}
