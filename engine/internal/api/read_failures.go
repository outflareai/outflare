package api

import (
	"errors"
	"fmt"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Why a daily read of a page (a review page, a public profile) failed, in
// a word: the error_code of its read_failed event, kept on the row as
// last_error_code. blocked, slow_page, page_not_found, invalid_address and the
// connector's own (connector_auth, connector_quota ...) are the site's or
// the workspace's; no_follower_count and not_a_review_page (the site
// answered with no bot check and the parser found nothing) and save_failed
// are Outflare's to fix, and alert (deploy/monitoring/alerts.yml).
const (
	readBlocked        = "blocked"
	readSlow           = "slow_page"
	readNotFound       = "page_not_found"
	readInvalidAddress = "invalid_address"
	readNoCount        = "no_follower_count"
	readSaveFailed     = "save_failed"
	readConnectorError = "connector_error"
)

// readFailure is a read that failed for a reason with its own code. Its
// kind is invalid: one page's refusal, never an outage of the connection.
type readFailure struct {
	code string
	err  *connectors.Error
}

func (e *readFailure) Error() string { return e.err.Error() }
func (e *readFailure) Unwrap() error { return e.err }

func readFailed(code, format string, a ...any) error {
	return &readFailure{code, connectors.Errf(connectors.ErrInvalid, "%s", fmt.Sprintf(format, a...))}
}

// pageFailed turns a page reader's *connectors.PageError (the site showed
// it a bot check, or the page was slower than it allows) into the page's
// own failure, tried again the same day; any other error is returned as it
// is.
func pageFailed(err error) error {
	var pe *connectors.PageError
	if !errors.As(err, &pe) {
		return err
	}
	if pe.Blocked {
		return readFailed(readBlocked, "the site showed a bot check instead of the page (%s); Outflare tries again later today", pe.Code)
	}
	return readFailed(readSlow, "the page took longer to read than the page reader allows (%s); Outflare tries again later today", pe.Code)
}

// readCode is a failed read's code.
func readCode(err error) string {
	var f *readFailure
	if errors.As(err, &f) {
		return f.code
	}
	return "connector_" + connectors.KindOf(err)
}

// readOutcome is the outcome label of a read metric: the code, with the
// connector's own codes as connector_error.
func readOutcome(code string) string {
	if len(code) > 10 && code[:10] == "connector_" {
		return readConnectorError
	}
	return code
}

// waitsForPerson says whether only a person can fix a failed read (the
// address is wrong), so trying again today would only fail the same way.
// Every other failure is tried again the same day.
func waitsForPerson(code string) bool {
	return code == readNotFound || code == readInvalidAddress || code == "connector_invalid"
}
