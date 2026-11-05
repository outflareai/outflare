package inbox

import (
	"errors"
	"net"
	"os"
	"testing"

	"github.com/outflareai/outflare/engine/internal/mailbox"
)

// A failed command after sign-in is at the mailbox stage, names the
// command, keeps the error's own text and still tells a timeout apart.
func TestMailboxErrNamesCommand(t *testing.T) {
	timeout := &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
	err := mailboxErr("fetch", timeout)
	var ce *mailbox.ConnError
	if !errors.As(err, &ce) || ce.Stage != mailbox.StageMailbox || ce.Protocol != "IMAP" {
		t.Fatalf("conn error: %#v", err)
	}
	var oe *opError
	if !errors.As(err, &oe) || oe.op != "fetch" || oe.Error() != timeout.Error() {
		t.Fatalf("op error: %#v", oe)
	}
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatal("timeout not seen")
	}
}
