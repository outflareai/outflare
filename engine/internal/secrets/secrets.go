// Package secrets encrypts credentials at rest (mailbox passwords, provider
// keys) with AES-256-GCM under the instance key from OUTFLARE_SECRET_KEY.
//
// Ciphertexts start with a version byte so the scheme can change later
// without ambiguity.
//
// To rotate the key, move the old one to OUTFLARE_SECRET_KEY_PREVIOUS and
// set a new OUTFLARE_SECRET_KEY: values sealed with either still open,
// new ones are sealed with the new key, and Reseal (run at start-up)
// re-encrypts what is stored. Old signatures keep verifying while the old
// key stays listed.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const version1 = 1

// Box seals and opens secrets.
type Box struct {
	aead   cipher.AEAD
	macKey []byte
	// Earlier keys, newest first: they open and verify, never seal or sign.
	previous []*Box
}

// ErrNoKey explains how to create a key.
var ErrNoKey = errors.New("OUTFLARE_SECRET_KEY is required: generate one with `openssl rand -base64 32` and keep it safe; losing it makes stored mailbox credentials unreadable")

// NewBox takes a base64-encoded 32-byte key, and optionally earlier keys
// that values may still be sealed with.
func NewBox(keyB64 string, previous ...string) (*Box, error) {
	b, err := newBox(keyB64, "OUTFLARE_SECRET_KEY")
	if err != nil {
		return nil, err
	}
	for _, p := range previous {
		if p = strings.TrimSpace(p); p == "" || p == strings.TrimSpace(keyB64) {
			continue
		}
		old, err := newBox(p, "OUTFLARE_SECRET_KEY_PREVIOUS")
		if err != nil {
			return nil, err
		}
		b.previous = append(b.previous, old)
	}
	return b, nil
}

// Rotating reports whether earlier keys are configured.
func (b *Box) Rotating() bool { return len(b.previous) > 0 }

func newBox(keyB64, name string) (*Box, error) {
	if keyB64 == "" {
		return nil, ErrNoKey
	}
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid base64: %w", name, err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("%s must decode to 32 bytes, got %d", name, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// A separate key for signing, so a signature never reveals anything
	// about the encryption key.
	m := hmac.New(sha256.New, key)
	m.Write([]byte("outflare signing key v1"))
	return &Box{aead: aead, macKey: m.Sum(nil)}, nil
}

// Seal encrypts plaintext. aad binds the ciphertext to its context (for
// example the mailbox id), so a ciphertext copied onto another row will not open.
func (b *Box) Seal(plaintext, aad []byte) []byte {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	out := append([]byte{version1}, nonce...)
	return b.aead.Seal(out, nonce, plaintext, aad)
}

// Open decrypts a value from Seal, with the current key or an earlier one.
func (b *Box) Open(ciphertext, aad []byte) ([]byte, error) {
	pt, _, err := b.open(ciphertext, aad)
	return pt, err
}

// open also reports whether the current key opened it.
func (b *Box) open(ciphertext, aad []byte) ([]byte, bool, error) {
	pt, err := b.openOne(ciphertext, aad)
	if err == nil {
		return pt, true, nil
	}
	for _, old := range b.previous {
		if pt, oerr := old.openOne(ciphertext, aad); oerr == nil {
			return pt, false, nil
		}
	}
	return nil, false, err
}

// Reseal returns ciphertext sealed with the current key, and whether it
// changed: false when the current key sealed it already.
func (b *Box) Reseal(ciphertext, aad []byte) ([]byte, bool, error) {
	pt, current, err := b.open(ciphertext, aad)
	if err != nil || current {
		return ciphertext, false, err
	}
	return b.Seal(pt, aad), true, nil
}

func (b *Box) openOne(ciphertext, aad []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(ciphertext) < 1+n || ciphertext[0] != version1 {
		return nil, errors.New("secrets: unknown ciphertext format")
	}
	pt, err := b.aead.Open(nil, ciphertext[1:1+n], ciphertext[1+n:], aad)
	if err != nil {
		return nil, errors.New("secrets: cannot decrypt (wrong key or tampered value)")
	}
	return pt, nil
}

// Sign returns a URL-safe signature of msg for purpose, for links that must
// not be forgeable, such as unsubscribe links.
func (b *Box) Sign(purpose, msg string) string {
	m := hmac.New(sha256.New, b.macKey)
	m.Write([]byte(purpose + "\x00" + msg))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:16])
}

// Verify checks a signature from Sign in constant time. Signatures made
// with an earlier key still pass, so links already sent keep working.
func (b *Box) Verify(purpose, msg, sig string) bool {
	if hmac.Equal([]byte(b.Sign(purpose, msg)), []byte(sig)) {
		return true
	}
	for _, old := range b.previous {
		if hmac.Equal([]byte(old.Sign(purpose, msg)), []byte(sig)) {
			return true
		}
	}
	return false
}
