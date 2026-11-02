package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// One way to page every list: a page answers next_cursor, null on the
// last page, and passing it back as cursor gives the next page. There is
// no other paging parameter, so callers, the SDK and the CLI's --all only
// ever need to know this one rule.
//
// Underneath, a list pages in one of three ways: before an id or time
// (newest first), after an id (oldest first), or by offset. A cursor is
// that kind and the position, base64url encoded, so a list refuses a
// cursor another kind of list gave out. Callers must not build or read
// them; the encoding may change.

// pageKind is how a list pages underneath.
type pageKind string

const (
	pageBefore pageKind = "before"
	pageAfter  pageKind = "after"
	pageOffset pageKind = "offset"
)

const badCursor = "cursor is not one this API gave out: pass next_cursor from the previous page unchanged"

// errCursor is a cursor the list cannot read, for helpers that return an
// error: badQuery answers it with 400 invalid_cursor.
var errCursor = errors.New(badCursor)

func encodeCursor(kind pageKind, value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(string(kind) + ":" + value))
}

// readCursor decodes ?cursor=: its kind and position, or "" and "" when
// there is none (the first page).
func readCursor(r *http.Request) (pageKind, string, bool) {
	c := r.URL.Query().Get("cursor")
	if c == "" {
		return "", "", true
	}
	raw, err := base64.RawURLEncoding.DecodeString(c)
	key, value, ok := strings.Cut(string(raw), ":")
	switch k := pageKind(key); {
	case err != nil || !ok || value == "":
		return "", "", false
	case k == pageBefore || k == pageAfter || k == pageOffset:
		return k, value, true
	}
	return "", "", false
}

// pageCursor reads ?cursor= for a list that pages by kind: the position,
// or "" for the first page. A cursor this kind of list did not give out
// is answered with 400 invalid_cursor, and ok is false.
func pageCursor(w http.ResponseWriter, r *http.Request, kind pageKind) (string, bool) {
	k, v, ok := readCursor(r)
	if !ok || (k != "" && k != kind) {
		writeError(w, r, http.StatusBadRequest, "invalid_cursor", badCursor)
		return "", false
	}
	return v, true
}

// pageOffsetParam reads ?cursor= for a list that pages by offset: 0 for
// the first page.
func pageOffsetParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	v, ok := pageCursor(w, r, pageOffset)
	if !ok || v == "" {
		return 0, ok
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_cursor", badCursor)
		return 0, false
	}
	return n, true
}

// invalidCursor answers a cursor whose position the list cannot read.
func invalidCursor(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusBadRequest, "invalid_cursor", badCursor)
}

// nextCursor is a page's next_cursor: the next page's position for a list
// that pages by kind, or nil (the last page) when v is nil, a nil pointer
// or "".
func nextCursor(kind pageKind, v any) any {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" || string(b) == `""` {
		return nil
	}
	s := string(b)
	if strings.HasPrefix(s, `"`) {
		_ = json.Unmarshal(b, &s)
	}
	return encodeCursor(kind, s)
}
