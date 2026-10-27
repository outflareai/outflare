package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Idempotency-Key makes a workspace write safe to retry: the first request
// runs, and a repeat with the same key (from the same caller, within 24
// hours) gets the first answer back instead of running again. Agents and
// scripts retry after timeouts, and without this a retried create makes
// two leads, records spend twice or buys twice.

const (
	idempotencyHeader = "Idempotency-Key" // kept for 24 hours (the SQL below)
	// Answers larger than this are not kept: the key is released and a
	// retry runs again. No write answers anything near this size.
	idempotencyMaxBody = 1 << 20
)

// idempotent wraps a workspace write that carries an Idempotency-Key.
func (s *Server) idempotent(w http.ResponseWriter, r *http.Request, a *access, next http.HandlerFunc) {
	key := r.Header.Get(idempotencyHeader)
	if key == "" || r.Method == http.MethodGet || r.Method == http.MethodHead {
		next(w, r)
		return
	}
	if len(key) > 255 || strings.TrimSpace(key) != key {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Idempotency-Key must be 1 to 255 characters without surrounding spaces")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<20))
	if err != nil {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "request body too large")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	h := sha256.New()
	h.Write([]byte(r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery + "\n"))
	h.Write(body)
	hash := h.Sum(nil)

	ctx := r.Context()
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO idempotency_keys (workspace_id, actor_id, key, method, path, request_hash)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (workspace_id, actor_id, key) DO UPDATE
		   SET method = EXCLUDED.method, path = EXCLUDED.path, request_hash = EXCLUDED.request_hash,
		       status = NULL, content_type = NULL, body = NULL, created_at = now()
		 WHERE idempotency_keys.created_at < now() - interval '24 hours'`,
		a.WorkspaceID, a.ActorID, key, r.Method, r.URL.Path, hash)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		s.replayIdempotent(w, r, a, key, hash)
		return
	}

	rec := &teeRecorder{ResponseWriter: w, status: http.StatusOK}
	completed := false
	defer func() {
		// A panic or a server error releases the key, so a retry runs.
		if !completed {
			s.releaseIdempotencyKey(ctx, a, key)
		}
	}()
	next(rec, r)
	if rec.status >= 500 || rec.overflow {
		return
	}
	_, err = s.pool.Exec(context.WithoutCancel(ctx), `
		UPDATE idempotency_keys SET status = $4, content_type = $5, body = $6
		 WHERE workspace_id = $1 AND actor_id = $2 AND key = $3`,
		a.WorkspaceID, a.ActorID, key, rec.status, rec.Header().Get("Content-Type"), rec.buf.Bytes())
	if err != nil {
		s.log.Error("idempotency: store answer", "err", err, "request_id", RequestID(ctx))
		return
	}
	completed = true
}

func (s *Server) replayIdempotent(w http.ResponseWriter, r *http.Request, a *access, key string, hash []byte) {
	var (
		method, path, contentType string
		storedHash, body          []byte
		status                    *int
	)
	err := s.pool.QueryRow(r.Context(), `
		SELECT method, path, request_hash, status, coalesce(content_type, ''), body
		  FROM idempotency_keys WHERE workspace_id = $1 AND actor_id = $2 AND key = $3`,
		a.WorkspaceID, a.ActorID, key).Scan(&method, &path, &storedHash, &status, &contentType, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusConflict, "idempotency_in_progress", "a request with this Idempotency-Key just finished failing; retry")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !bytes.Equal(storedHash, hash) {
		writeError(w, r, http.StatusUnprocessableEntity, "idempotency_key_reused",
			"this Idempotency-Key was used for a different request ("+method+" "+path+"); use a new key for each distinct request")
		return
	}
	if status == nil {
		writeError(w, r, http.StatusConflict, "idempotency_in_progress", "a request with this Idempotency-Key is still running; retry in a moment")
		return
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Idempotent-Replayed", "true")
	w.WriteHeader(*status)
	w.Write(body) //nolint:errcheck
}

func (s *Server) releaseIdempotencyKey(ctx context.Context, a *access, key string) {
	_, err := s.pool.Exec(context.WithoutCancel(ctx), `
		DELETE FROM idempotency_keys WHERE workspace_id = $1 AND actor_id = $2 AND key = $3 AND status IS NULL`,
		a.WorkspaceID, a.ActorID, key)
	if err != nil {
		s.log.Error("idempotency: release key", "err", err)
	}
}

// PruneIdempotencyKeys deletes keys older than a day. The engine runs it
// hourly.
func (s *Server) PruneIdempotencyKeys(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE created_at < now() - interval '24 hours'`)
	return tag.RowsAffected(), err
}

// teeRecorder passes a response through while keeping a copy.
type teeRecorder struct {
	http.ResponseWriter
	status   int
	buf      bytes.Buffer
	overflow bool
}

func (t *teeRecorder) WriteHeader(code int) {
	t.status = code
	t.ResponseWriter.WriteHeader(code)
}

func (t *teeRecorder) Write(b []byte) (int, error) {
	if !t.overflow {
		if t.buf.Len()+len(b) > idempotencyMaxBody {
			t.overflow = true
			t.buf.Reset()
		} else {
			t.buf.Write(b)
		}
	}
	return t.ResponseWriter.Write(b)
}
