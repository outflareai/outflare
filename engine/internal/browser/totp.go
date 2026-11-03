package browser

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TOTP is the current six-digit code for a base32 seed (RFC 6238, SHA-1,
// 30-second steps), as authenticator apps compute it. The worker types it
// for the model, which never sees the seed or the code.
func TOTP(seed string, at time.Time) (string, error) {
	seed = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(seed), " ", ""))
	seed = strings.TrimRight(seed, "=")
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(seed)
	if err != nil || len(key) == 0 {
		return "", errors.New("the TOTP seed is not base32")
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(at.Unix()/30))
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := (binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff) % 1000000
	return fmt.Sprintf("%06d", code), nil
}
