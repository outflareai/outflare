package emailfind

import (
	"context"
	"encoding/json"
	"strings"
)

// logCheck appends one check to the training corpus (email_checks). It is
// best-effort labelled data, never on the hot path's critical result, so a
// write failure is returned for the caller to log but does not fail the find.
// detail carries signals only (shape, provider status, score, cost); never a
// name or message content.
func logCheck(ctx context.Context, q Querier, ws, email, checkType, result string, confidence *int, detail map[string]any) error {
	email = strings.ToLower(strings.TrimSpace(email))
	at := strings.LastIndexByte(email, '@')
	if at < 1 {
		return nil
	}
	domain := email[at+1:]
	if detail == nil {
		detail = map[string]any{}
	}
	dj, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `
		INSERT INTO email_checks (workspace_id, email, domain, check_type, result, confidence, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		ws, email, domain, checkType, result, confidence, dj)
	return err
}

func intPtr(v int) *int { return &v }
