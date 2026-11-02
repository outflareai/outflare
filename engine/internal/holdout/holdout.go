// Package holdout picks who is held out of a send: a share of the
// audience, at random but the same way every time for the same send and
// person, so a retry or a re-run never changes a person's side.
package holdout

import (
	"crypto/sha256"
	"encoding/binary"
)

// Max is the largest share of an audience that can be held out, in percent.
const Max = 50

// In reports whether the person (by record or lead id) is held out of the
// send (a broadcast's or sequence's id) at percent, 0 to Max.
func In(send, person string, percent int) bool {
	if percent <= 0 {
		return false
	}
	sum := sha256.Sum256([]byte(send + "/" + person))
	return binary.BigEndian.Uint64(sum[:8])%100 < uint64(percent)
}
