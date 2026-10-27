package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
)

// One way to page every list: pass next_cursor back as cursor.
//
// Lists page in one of three ways underneath (before an id or time, newest
// first; after an id, oldest first; or an offset), and each keeps its own
// parameter and next_ field. On top of that, every list takes an opaque
// cursor and answers next_cursor, so callers, the SDK and the CLI's --all
// only ever need to know one rule. A cursor is the underlying parameter
// and value, base64url encoded; callers must not build or read them.

// pageKeys are the underlying paging parameters, each with its next_ field.
var pageKeys = []string{"before", "after", "offset"}

func encodeCursor(key, value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(key + ":" + value))
}

// withCursorParam turns ?cursor= back into the list's own parameter, so
// handlers only read before, after or offset.
func withCursorParam(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		c := q.Get("cursor")
		if c == "" || r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		raw, err := base64.RawURLEncoding.DecodeString(c)
		key, value, ok := strings.Cut(string(raw), ":")
		known := false
		for _, k := range pageKeys {
			known = known || k == key
		}
		if err != nil || !ok || !known {
			writeError(w, r, http.StatusBadRequest, "invalid_cursor", "cursor is not one this API gave out: pass next_cursor from the previous page unchanged")
			return
		}
		q.Del("cursor")
		q.Set(key, value)
		r2 := r.Clone(r.Context())
		r2.URL.RawQuery = q.Encode()
		next.ServeHTTP(w, r2)
	})
}

// addNextCursor sets next_cursor beside a list's next_before, next_after
// or next_offset: null when that is null, so null always means the end.
func addNextCursor(body map[string]any) {
	if _, done := body["next_cursor"]; done {
		return
	}
	for _, k := range pageKeys {
		v, ok := body["next_"+k]
		if !ok {
			continue
		}
		body["next_cursor"] = cursorFor(k, v)
		return
	}
}

func cursorFor(key string, v any) any {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return nil
	}
	s := string(b)
	if strings.HasPrefix(s, `"`) {
		_ = json.Unmarshal(b, &s)
	}
	return encodeCursor(key, s)
}
