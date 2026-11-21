package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// Merging issues (docs/error-tracking.md): when the same bug still lands
// in two issues, a person puts them together. The errors of each merged
// issue then count as the issue they were merged into, its status holds
// for all of them, and links to a merged issue open the one it is part
// of. A merge can be taken back; nothing stored changes.

// errorMerge is an issue merged into another.
type errorMerge struct {
	Fingerprint string    `json:"fingerprint"`
	MergedAt    time.Time `json:"merged_at"`
	MergedBy    *string   `json:"merged_by"`
	// Its latest error's type and message, in the last 90 days.
	Type    string `json:"type"`
	Message string `json:"message"`
}

// errorMergedFrom is the issues a person merged into fp.
func (s *Server) errorMergedFrom(ctx context.Context, brand, fp string) ([]errorMerge, error) {
	rows, err := s.pool.Query(ctx, `SELECT m.fingerprint, m.merged_at, a.display_name FROM error_issue_merges m LEFT JOIN actors a ON a.id = m.merged_by
		WHERE m.brand_id = $1 AND m.into_fingerprint = $2 AND m.kind = 'hand' ORDER BY m.merged_at DESC LIMIT 200`, brand, fp)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (errorMerge, error) {
		var m errorMerge
		return m, row.Scan(&m.Fingerprint, &m.MergedAt, &m.MergedBy)
	})
	if err != nil {
		return nil, err
	}
	for k := range out {
		_ = s.pool.QueryRow(ctx, `SELECT left(coalesce(properties->>'type', ''), 200), left(coalesce(properties->>'message', ''), 500) FROM customer_events
			WHERE brand_id = $1 AND name = $2 AND occurred_at >= $3 AND `+errorGroupOf+` = $4 ORDER BY occurred_at DESC LIMIT 1`,
			brand, errorEvent, time.Now().Add(-errorHistory), out[k].Fingerprint).Scan(&out[k].Type, &out[k].Message)
	}
	if out == nil {
		out = []errorMerge{}
	}
	return out, nil
}

// mergeErrorIssues puts issues together into one of them.
func (s *Server) mergeErrorIssues(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		Fingerprints []string `json:"fingerprints"`
		Into         string   `json:"into"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !errorFingerprintRe.MatchString(in.Into) || len(in.Fingerprints) == 0 || len(in.Fingerprints) > 200 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "into is the issue to keep, and fingerprints lists 1 to 200 issues to merge into it")
		return
	}
	for _, fp := range in.Fingerprints {
		if !errorFingerprintRe.MatchString(fp) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "a fingerprint is 1 to 64 lowercase letters and digits")
			return
		}
	}
	ctx := r.Context()
	into, err := s.errorTarget(ctx, a.Brand, in.Into)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var fps []string
	for _, fp := range in.Fingerprints {
		t, err := s.errorTarget(ctx, a.Brand, fp)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if t != into && !slices.Contains(fps, t) {
			fps = append(fps, t)
		}
	}
	known, err := s.knownErrorIssues(ctx, a.WorkspaceID, a.Brand, append([]string{into}, fps...))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !slices.Contains(known, into) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such error in the last 90 days")
		return
	}
	merged := []string{}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, fp := range fps {
			if !slices.Contains(known, fp) {
				continue
			}
			// What was merged into it moves along.
			if _, err := tx.Exec(ctx, `UPDATE error_issue_merges SET into_fingerprint = $3 WHERE brand_id = $1 AND into_fingerprint = $2 AND fingerprint <> $3`, a.Brand, fp, into); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO error_issue_merges (workspace_id, brand_id, fingerprint, into_fingerprint, kind, merged_by)
				VALUES ($1, $2, $3, $4, 'hand', $5) ON CONFLICT (brand_id, fingerprint) DO UPDATE SET into_fingerprint = EXCLUDED.into_fingerprint,
				kind = 'hand', merged_by = EXCLUDED.merged_by, merged_at = now()`, a.WorkspaceID, a.Brand, fp, into, nullableString(a.ActorID)); err != nil {
				return err
			}
			if err := recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "error_issue.merged", "error_issue", fp,
				map[string]any{"brand_id": a.Brand, "fingerprint": fp, "into": into}, map[string]any{"count": len(fps)}); err != nil {
				return err
			}
			merged = append(merged, fp)
		}
		return nil
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"into": into, "merged": merged, "skipped": len(in.Fingerprints) - len(merged)})
}

// splitErrorIssue takes back a merge: the merged issue is its own again.
func (s *Server) splitErrorIssue(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	fp, merged := r.PathValue("fingerprint"), r.PathValue("merged")
	if !errorFingerprintRe.MatchString(fp) || !errorFingerprintRe.MatchString(merged) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such merge")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM error_issue_merges WHERE workspace_id = $1 AND brand_id = $2 AND fingerprint = $3 AND into_fingerprint = $4 AND kind = 'hand'`,
			a.WorkspaceID, a.Brand, merged, fp)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errorReply{http.StatusNotFound, "not_found", "no such merge"}
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "error_issue.split", "error_issue", merged,
			map[string]any{"brand_id": a.Brand, "fingerprint": merged, "from": fp})
	})
	if err != nil {
		var e errorReply
		if errors.As(err, &e) {
			writeError(w, r, e.status, e.code, e.message)
			return
		}
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"fingerprint": merged, "from": fp})
}
