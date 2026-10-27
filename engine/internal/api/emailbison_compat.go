package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/mailbox"
)

// EmailBison-compatible API. Mailbox suppliers such as Cheap Inboxes push
// the mailboxes they sell into EmailBison through its API, and let you
// point that connection at a self-hosted EmailBison. This answers the part
// of EmailBison's API that pushing uses (sender emails, warm-up, tags), so
// those suppliers can push straight into an Outflare workspace:
//
//	base URL  https://<your Outflare>/emailbison/<workspace id>
//	API key   an Outflare API token with mailboxes.read and mailboxes.manage
//
// Pushed mailboxes go through the same checks as any import. Every call is
// logged without its values (GET /v1/workspaces/{ws}/emailbison-compat/calls)
// so a call we do not answer yet shows up instead of failing silently.

// compatSource marks mailboxes pushed through this API.
const compatSource = "emailbison_api"

func (s *Server) routesEmailBisonCompat(mux *http.ServeMux) {
	mux.HandleFunc("/emailbison/{ws}/{rest...}", s.inWorkspace(auth.ScopeMailboxesRead, s.emailBisonCompat))
	mux.HandleFunc("GET /v1/workspaces/{ws}/emailbison-compat/calls", s.inWorkspace(auth.ScopeMailboxesRead, s.listEmailBisonCalls))
}

// ebError answers like EmailBison (Laravel): {"message", "errors"}.
func ebError(w http.ResponseWriter, status int, msg string, field string) {
	body := map[string]any{"message": msg}
	if field != "" {
		body["errors"] = map[string][]string{field: {msg}}
	}
	writeJSON(w, status, body)
}

type ebStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *ebStatusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (s *Server) emailBisonCompat(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	rest := strings.Trim(r.PathValue("rest"), "/")
	rest = strings.TrimPrefix(strings.TrimPrefix(rest, "api/"), "api")
	rest = strings.Trim(rest, "/")
	parts := strings.Split(rest, "/")

	body, err := readEBBody(r)
	if err != nil {
		ebError(w, http.StatusBadRequest, err.Error(), "")
		return
	}
	sw := &ebStatusWriter{ResponseWriter: w, status: http.StatusOK}
	supported := s.ebRoute(sw, r, a, r.Method, parts, body)
	s.logEmailBisonCall(r, a.WorkspaceID, body, sw.status, supported)
}

// ebRoute answers one call and reports whether it is a call we support.
func (s *Server) ebRoute(w http.ResponseWriter, r *http.Request, a *access, method string, p []string, body map[string]any) bool {
	write := method != http.MethodGet && method != http.MethodHead
	if write && !a.has(auth.ScopeMailboxesManage) {
		ebError(w, http.StatusForbidden, "This API token needs the mailboxes.manage scope.", "")
		return true
	}
	is := func(m string, path ...string) bool {
		if method != m || len(p) != len(path) {
			return false
		}
		for i := range path {
			if path[i] != "*" && path[i] != p[i] {
				return false
			}
		}
		return true
	}
	switch {
	case is(http.MethodGet, "users"), is(http.MethodGet, "user"), is(http.MethodGet, "users", "me"):
		s.ebMe(w, r, a)
	case is(http.MethodGet, "sender-emails"), is(http.MethodGet, "warmup", "sender-emails"):
		s.ebList(w, r, a)
	case is(http.MethodGet, "sender-emails", "*"), is(http.MethodGet, "warmup", "sender-emails", "*"):
		s.ebShow(w, r, a, p[len(p)-1])
	case is(http.MethodPost, "sender-emails", "imap-smtp"), is(http.MethodPost, "sender-emails"):
		s.ebCreate(w, r, a, body)
	case is(http.MethodPost, "sender-emails", "bulk"), is(http.MethodPost, "sender-emails", "bulk-imap-smtp"),
		is(http.MethodPost, "sender-emails", "imap-smtp", "bulk"):
		s.ebBulk(w, r, a, body)
	case is(http.MethodPatch, "sender-emails", "*"), is(http.MethodPut, "sender-emails", "*"):
		s.ebUpdate(w, r, a, p[1], body)
	case is(http.MethodDelete, "sender-emails", "*"):
		s.ebDelete(w, r, a, p[1])
	case is(http.MethodPatch, "warmup", "sender-emails", "enable"), is(http.MethodPost, "warmup", "sender-emails", "enable"),
		is(http.MethodPatch, "warmup", "sender-emails", "disable"), is(http.MethodPost, "warmup", "sender-emails", "disable"):
		s.ebWarmup(w, r, a, p[2] == "enable", body)
	case is(http.MethodGet, "tags"):
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{}})
	case is(http.MethodPost, "tags"):
		// Tags are accepted and not kept: Outflare groups mailboxes by
		// project and team instead.
		name, _ := body["name"].(string)
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"id": 1, "name": name}})
	case is(http.MethodPost, "tags", "attach-to-sender-emails"), is(http.MethodPost, "tags", "remove-from-sender-emails"):
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"success": true, "message": "Tags are not kept in Outflare."}})
	default:
		ebError(w, http.StatusNotFound, "Outflare does not answer this EmailBison endpoint yet. The call was logged.", "")
		return false
	}
	return true
}

// readEBBody reads a JSON or form body into a map. Unknown fields are
// fine: clients send what EmailBison takes.
func readEBBody(r *http.Request) (map[string]any, error) {
	out := map[string]any{}
	if r.Body == nil || r.Method == http.MethodGet || r.Method == http.MethodHead {
		return out, nil
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return nil, errors.New("could not read the body")
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return out, nil
	}
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
		vals, err := url.ParseQuery(string(raw))
		if err != nil {
			return nil, errors.New("the form body could not be read")
		}
		for k, v := range vals {
			k = strings.TrimSuffix(k, "[]")
			if len(v) == 1 && !strings.HasSuffix(k, "_ids") {
				out[k] = v[0]
			} else {
				items := make([]any, len(v))
				for i := range v {
					items[i] = v[i]
				}
				out[k] = items
			}
		}
		return out, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, errors.New("the body is not valid JSON")
	}
	switch x := v.(type) {
	case map[string]any:
		return x, nil
	case []any:
		return map[string]any{"sender_emails": x}, nil
	}
	return nil, errors.New("the body must be a JSON object")
}

// ebSender is a mailbox as EmailBison shows a sender email.
type ebSender struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Email         string    `json:"email"`
	Type          string    `json:"type"`
	Status        string    `json:"status"`
	DailyLimit    int       `json:"daily_limit"`
	WarmupEnabled bool      `json:"warmup_enabled"`
	IMAPServer    string    `json:"imap_server"`
	IMAPPort      int       `json:"imap_port"`
	SMTPServer    string    `json:"smtp_server"`
	SMTPPort      int       `json:"smtp_port"`
	Tags          []any     `json:"tags"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	// Outflare's own id and status, for clients that know about them.
	OutflareID     string  `json:"outflare_id"`
	OutflareStatus string  `json:"outflare_status"`
	StatusReason   *string `json:"status_reason"`
}

func ebStatus(st string) string {
	switch st {
	case "active":
		return "Connected"
	case "verifying":
		return "Connecting"
	case "paused":
		return "Paused"
	}
	return "Disconnected"
}

const ebSelect = `
	SELECT c.id, m.display_name, m.email, m.auth_method, m.status, m.status_reason, m.daily_limit, m.warmup_enabled,
	       m.imap_host, m.imap_port, m.smtp_host, m.smtp_port, m.created_at, m.updated_at, m.id::text
	FROM mailboxes m JOIN emailbison_compat_ids c ON c.mailbox_id = m.id`

func scanEBSender(row pgx.CollectableRow) (ebSender, error) {
	var e ebSender
	var method string
	err := row.Scan(&e.ID, &e.Name, &e.Email, &method, &e.OutflareStatus, &e.StatusReason, &e.DailyLimit, &e.WarmupEnabled,
		&e.IMAPServer, &e.IMAPPort, &e.SMTPServer, &e.SMTPPort, &e.CreatedAt, &e.UpdatedAt, &e.OutflareID)
	e.Status, e.Tags = ebStatus(e.OutflareStatus), []any{}
	e.Type = "imap_smtp"
	if method == "oauth" {
		e.Type = "oauth"
	}
	return e, err
}

// ebNumber gives every mailbox in the workspace its EmailBison number.
func ebNumber(ctx context.Context, q pgx.Tx, ws string) error {
	_, err := q.Exec(ctx, `
		INSERT INTO emailbison_compat_ids (workspace_id, mailbox_id)
		SELECT workspace_id, id FROM mailboxes m WHERE workspace_id = $1
		  AND NOT EXISTS (SELECT 1 FROM emailbison_compat_ids c WHERE c.mailbox_id = m.id)
		ORDER BY created_at
		ON CONFLICT (mailbox_id) DO NOTHING`, ws)
	return err
}

// ebFind finds a mailbox by EmailBison number, email or Outflare id.
func (s *Server) ebFind(ctx context.Context, q pgx.Tx, ws, key string) (*ebSender, error) {
	if err := ebNumber(ctx, q, ws); err != nil {
		return nil, err
	}
	key, _ = url.PathUnescape(key)
	var where string
	var arg any
	switch n, err := strconv.ParseInt(key, 10, 64); {
	case err == nil:
		where, arg = "c.id = $2", n
	case strings.Contains(key, "@"):
		where, arg = "lower(m.email) = lower($2)", key
	case validUUID(key):
		where, arg = "m.id = $2::uuid", key
	default:
		return nil, nil
	}
	rows, err := q.Query(ctx, ebSelect+` WHERE m.workspace_id = $1 AND `+where, ws, arg)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, scanEBSender)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

func (s *Server) ebMe(w http.ResponseWriter, r *http.Request, a *access) {
	var name string
	if err := s.pool.QueryRow(r.Context(), `SELECT name FROM workspaces WHERE id = $1`, a.WorkspaceID).Scan(&name); err != nil {
		s.internal(w, r, err)
		return
	}
	team := map[string]any{"id": 1, "name": name, "outflare_workspace_id": a.WorkspaceID}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"id": 1, "name": name, "email": "", "team": team, "current_team": team}})
}

func (s *Server) ebList(w http.ResponseWriter, r *http.Request, a *access) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	page = max(page, 1)
	per, _ := strconv.Atoi(q.Get("per_page"))
	if per <= 0 || per > 100 {
		per = 15
	}
	search := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.ToLower(q.Get("search"))) + "%"
	var list []ebSender
	var total int
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if err := ebNumber(r.Context(), tx, a.WorkspaceID); err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM mailboxes WHERE workspace_id = $1 AND lower(email) LIKE $2`,
			a.WorkspaceID, search).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), ebSelect+` WHERE m.workspace_id = $1 AND lower(m.email) LIKE $2 ORDER BY c.id LIMIT $3 OFFSET $4`,
			a.WorkspaceID, search, per, (page-1)*per)
		if err != nil {
			return err
		}
		list, err = pgx.CollectRows(rows, scanEBSender)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []ebSender{}
	}
	last := max(int(math.Ceil(float64(total)/float64(per))), 1)
	link := func(n int) any {
		if n < 1 || n > last {
			return nil
		}
		u := *r.URL
		v := u.Query()
		v.Set("page", strconv.Itoa(n))
		u.RawQuery = v.Encode()
		return u.String()
	}
	from, to := (page-1)*per+1, (page-1)*per+len(list)
	if len(list) == 0 {
		from, to = 0, 0
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":  list,
		"links": map[string]any{"first": link(1), "last": link(last), "prev": link(page - 1), "next": link(page + 1)},
		"meta": map[string]any{"current_page": page, "last_page": last, "per_page": per, "total": total,
			"from": from, "to": to, "path": r.URL.Path},
	})
}

func (s *Server) ebShow(w http.ResponseWriter, r *http.Request, a *access, key string) {
	var e *ebSender
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		e, err = s.ebFind(r.Context(), tx, a.WorkspaceID, key)
		return err
	})
	switch {
	case err != nil:
		s.internal(w, r, err)
	case e == nil:
		ebError(w, http.StatusNotFound, "Sender email not found.", "")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"data": e})
	}
}

// ebRow builds an import row from an EmailBison sender-email body.
func ebRow(b map[string]any) (mailbox.Row, string) {
	str := func(keys ...string) string {
		for _, k := range keys {
			switch v := b[k].(type) {
			case string:
				if v = strings.TrimSpace(v); v != "" {
					return v
				}
			case float64:
				return strconv.FormatFloat(v, 'f', -1, 64)
			}
		}
		return ""
	}
	num := func(keys ...string) int {
		n, _ := strconv.Atoi(str(keys...))
		return n
	}
	email := strings.ToLower(str("email", "email_address", "from_email"))
	if email == "" {
		return mailbox.Row{}, "email is required"
	}
	password := str("password")
	row := mailbox.Row{
		Email: email, DisplayName: str("name", "from_name"),
		SMTPHost: str("smtp_server", "smtp_host"), SMTPPort: num("smtp_port"),
		SMTPUsername: orDefault(str("smtp_username"), email), SMTPPassword: orDefault(str("smtp_password"), password),
		IMAPHost: str("imap_server", "imap_host"), IMAPPort: num("imap_port"),
		IMAPUsername: orDefault(str("imap_username"), email), IMAPPassword: orDefault(str("imap_password"), password),
		DailyLimit: num("daily_limit", "max_emails_per_day"),
	}
	if row.SMTPPassword == "" || row.IMAPPassword == "" {
		return row, "password is required"
	}
	if row.DailyLimit < 0 || row.DailyLimit > 500 {
		return row, "daily_limit must be between 1 and 500"
	}
	return row, ""
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// ebImport checks and imports rows, as one import, and returns them as
// sender emails in order, with the errors of rows that failed.
func (s *Server) ebImport(ctx context.Context, a *access, rows []mailbox.Row) ([]ebSender, []mailbox.RowError, error) {
	for i := range rows {
		rows[i].Line = i + 1
	}
	good, bad := s.mail.Prepare(ctx, a.Test, rows)
	if bad == nil {
		bad = []mailbox.RowError{}
	}
	var out []ebSender
	if len(good) == 0 {
		return out, bad, nil
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := s.mail.Import(ctx, tx, a.WorkspaceID, compatSource, good)
		if err != nil {
			return err
		}
		for i, id := range res.IDs {
			e, err := s.ebFind(ctx, tx, a.WorkspaceID, id)
			if err != nil {
				return err
			}
			if e != nil {
				out = append(out, *e)
			}
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, "mailbox.imported", "mailbox", id,
				map[string]any{"email": good[i].Email, "source": compatSource}); err != nil {
				return err
			}
		}
		return nil
	})
	return out, bad, err
}

func (s *Server) ebCreate(w http.ResponseWriter, r *http.Request, a *access, body map[string]any) {
	row, msg := ebRow(body)
	if msg != "" {
		ebError(w, http.StatusUnprocessableEntity, msg, strings.Fields(msg)[0])
		return
	}
	out, bad, err := s.ebImport(r.Context(), a, []mailbox.Row{row})
	switch {
	case err != nil:
		s.internal(w, r, err)
	case len(bad) > 0:
		ebError(w, http.StatusUnprocessableEntity, bad[0].Error, "email")
	case len(out) == 0:
		s.internal(w, r, errors.New("import returned no mailbox"))
	default:
		writeJSON(w, http.StatusOK, map[string]any{"data": out[0]})
	}
}

func (s *Server) ebBulk(w http.ResponseWriter, r *http.Request, a *access, body map[string]any) {
	items, _ := body["sender_emails"].([]any)
	if items == nil {
		items, _ = body["data"].([]any)
	}
	if len(items) == 0 || len(items) > 500 {
		ebError(w, http.StatusUnprocessableEntity, "Send 1 to 500 sender emails in sender_emails.", "sender_emails")
		return
	}
	var rows []mailbox.Row
	var bad []mailbox.RowError
	for i, it := range items {
		m, _ := it.(map[string]any)
		row, msg := ebRow(m)
		if msg != "" {
			bad = append(bad, mailbox.RowError{Line: i + 1, Email: row.Email, Error: msg})
			continue
		}
		rows = append(rows, row)
	}
	out, prepBad, err := s.ebImport(r.Context(), a, rows)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out == nil {
		out = []ebSender{}
	}
	for _, b := range prepBad {
		bad = append(bad, mailbox.RowError{Email: b.Email, Error: b.Error})
	}
	if bad == nil {
		bad = []mailbox.RowError{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "errors": bad})
}

func (s *Server) ebUpdate(w http.ResponseWriter, r *http.Request, a *access, key string, body map[string]any) {
	ctx := r.Context()
	var e *ebSender
	if err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		e, err = s.ebFind(ctx, tx, a.WorkspaceID, key)
		return err
	}); err != nil {
		s.internal(w, r, err)
		return
	}
	if e == nil {
		ebError(w, http.StatusNotFound, "Sender email not found.", "")
		return
	}
	// New credentials (a supplier fixing a broken connection) re-import the
	// mailbox, which checks them again.
	if _, ok := body["password"]; ok {
		merged := map[string]any{"email": e.Email, "name": e.Name, "smtp_server": e.SMTPServer, "smtp_port": float64(e.SMTPPort),
			"imap_server": e.IMAPServer, "imap_port": float64(e.IMAPPort), "daily_limit": float64(e.DailyLimit)}
		for k, v := range body {
			merged[k] = v
		}
		merged["email"] = e.Email
		row, msg := ebRow(merged)
		if msg != "" {
			ebError(w, http.StatusUnprocessableEntity, msg, strings.Fields(msg)[0])
			return
		}
		out, bad, err := s.ebImport(ctx, a, []mailbox.Row{row})
		switch {
		case err != nil:
			s.internal(w, r, err)
		case len(bad) > 0:
			ebError(w, http.StatusUnprocessableEntity, bad[0].Error, "password")
		case len(out) == 0:
			s.internal(w, r, errors.New("import returned no mailbox"))
		default:
			writeJSON(w, http.StatusOK, map[string]any{"data": out[0]})
		}
		return
	}
	limit, hasLimit := body["daily_limit"]
	name, hasName := body["name"].(string)
	var n int
	if hasLimit {
		var err error
		n, err = strconv.Atoi(strings.TrimSpace(fmt.Sprint(limit)))
		if err != nil || n < 1 || n > 500 {
			ebError(w, http.StatusUnprocessableEntity, "daily_limit must be between 1 and 500", "daily_limit")
			return
		}
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if hasLimit || hasName {
			if _, err := tx.Exec(ctx, `
				UPDATE mailboxes SET daily_limit = CASE WHEN $3 THEN $4 ELSE daily_limit END,
					display_name = CASE WHEN $5 THEN $6 ELSE display_name END, updated_at = now()
				WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, e.OutflareID, hasLimit, n, hasName, strings.TrimSpace(name)); err != nil {
				return err
			}
			data := map[string]any{"source": compatSource}
			if hasLimit {
				data["daily_limit"] = n
			}
			if hasName {
				data["display_name"] = name
			}
			if err := record(ctx, tx, &a.principal, a.WorkspaceID, "mailbox.updated", "mailbox", e.OutflareID, data); err != nil {
				return err
			}
		}
		var err error
		e, err = s.ebFind(ctx, tx, a.WorkspaceID, e.OutflareID)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": e})
}

// ebDelete pauses the mailbox rather than deleting it: a supplier removing
// a sender must not erase its sending history here.
func (s *Server) ebDelete(w http.ResponseWriter, r *http.Request, a *access, key string) {
	ctx := r.Context()
	found := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		e, err := s.ebFind(ctx, tx, a.WorkspaceID, key)
		if err != nil || e == nil {
			return err
		}
		found = true
		if _, err := tx.Exec(ctx, `
			UPDATE mailboxes SET status = 'paused', status_reason = 'Removed by an EmailBison API client', updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, e.OutflareID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "mailbox.paused", "mailbox", e.OutflareID,
			map[string]any{"source": compatSource, "reason": "removed through the EmailBison-compatible API"})
	})
	switch {
	case err != nil:
		s.internal(w, r, err)
	case !found:
		ebError(w, http.StatusNotFound, "Sender email not found.", "")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"success": true,
			"message": "The mailbox is paused in Outflare. Delete it on the Mailboxes page to remove it."}})
	}
}

func (s *Server) ebWarmup(w http.ResponseWriter, r *http.Request, a *access, on bool, body map[string]any) {
	ctx := r.Context()
	keys, _ := body["sender_email_ids"].([]any)
	if keys == nil {
		keys, _ = body["ids"].([]any)
	}
	if len(keys) == 0 {
		ebError(w, http.StatusUnprocessableEntity, "sender_email_ids is required", "sender_email_ids")
		return
	}
	var ids []string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, k := range keys {
			e, err := s.ebFind(ctx, tx, a.WorkspaceID, strings.TrimSpace(fmt.Sprint(k)))
			if err != nil {
				return err
			}
			if e != nil {
				ids = append(ids, e.OutflareID)
			}
		}
		if len(ids) == 0 {
			return nil
		}
		sort.Strings(ids)
		set := warmupSettings{Enabled: &on}
		if _, err := s.resolveWarmup(ctx, tx, a.WorkspaceID, &set, ids); err != nil {
			return err
		}
		if _, err := set.apply(ctx, tx, a.WorkspaceID, ids); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "mailbox.warmup_changed", "workspace", a.WorkspaceID,
			map[string]any{"mailbox_ids": ids, "enabled": on, "source": compatSource})
	})
	var bad errInvalid
	if errors.As(err, &bad) {
		ebError(w, http.StatusUnprocessableEntity, string(bad), "sender_email_ids")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"success": true, "updated": len(ids)}})
}

// keysOf lists a map's keys, and one level into objects and lists of
// objects ("sender_emails[].email"), never the values.
func keysOf(m map[string]any) []string {
	out := []string{}
	for k, v := range m {
		out = append(out, k)
		switch x := v.(type) {
		case map[string]any:
			for kk := range x {
				out = append(out, k+"."+kk)
			}
		case []any:
			if len(x) > 0 {
				if o, ok := x[0].(map[string]any); ok {
					for kk := range o {
						out = append(out, k+"[]."+kk)
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func (s *Server) logEmailBisonCall(r *http.Request, ws string, body map[string]any, status int, supported bool) {
	q := []string{}
	for k := range r.URL.Query() {
		q = append(q, k)
	}
	sort.Strings(q)
	path := "/" + strings.Trim(r.PathValue("rest"), "/")
	ua := r.UserAgent()
	if len(ua) > 200 {
		ua = ua[:200]
	}
	ctx := context.WithoutCancel(r.Context())
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO emailbison_compat_calls (workspace_id, method, path, query_keys, body_keys, status, supported, user_agent)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, ws, r.Method, path, q, keysOf(body), status, supported, ua); err != nil {
		s.log.Warn("could not log an EmailBison-compatible call", "err", err)
		return
	}
	// Keep the latest 1,000 per workspace.
	_, _ = s.pool.Exec(ctx, `
		DELETE FROM emailbison_compat_calls WHERE workspace_id = $1 AND id < (
			SELECT id FROM emailbison_compat_calls WHERE workspace_id = $1 ORDER BY id DESC OFFSET 999 LIMIT 1)`, ws)
}

type emailBisonCall struct {
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	QueryKeys []string  `json:"query_keys"`
	BodyKeys  []string  `json:"body_keys"`
	Status    int       `json:"status"`
	Supported bool      `json:"supported"`
	UserAgent string    `json:"user_agent"`
	CreatedAt time.Time `json:"created_at"`
}

// listEmailBisonCalls shows what clients asked of the EmailBison-compatible
// API, newest first: method, path, which fields they sent and our answer.
func (s *Server) listEmailBisonCalls(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT method, path, query_keys, body_keys, status, supported, user_agent, created_at
		FROM emailbison_compat_calls WHERE workspace_id = $1 ORDER BY id DESC LIMIT $2`, a.WorkspaceID, limit)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[emailBisonCall])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []emailBisonCall{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}
