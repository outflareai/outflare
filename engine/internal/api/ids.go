package api

import (
	"crypto/rand"
	"fmt"
)

// newID is a random UUID (version 4).
func newID() string {
	var b [16]byte
	rand.Read(b[:]) //nolint:errcheck // never fails
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
