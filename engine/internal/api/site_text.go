package api

import (
	"bytes"
	"encoding/json"
	"strings"
)

// pgSafeJSON returns a browser's JSON in a form Postgres stores. jsonb
// refuses a NUL character (\u0000) and half of a surrogate pair (\ud83d
// alone), so a page whose title or URL holds one made every batch from that
// visitor fail the same way. Such JSON is decoded and written again: NULs
// are dropped and a lone surrogate becomes U+FFFD. Anything else is
// returned as it came.
func pgSafeJSON(raw json.RawMessage) json.RawMessage {
	if !bytes.Contains(raw, []byte(`\u`)) {
		return raw
	}
	lower := bytes.ToLower(raw)
	if !bytes.Contains(lower, []byte(`\u0000`)) && !bytes.Contains(lower, []byte(`\ud`)) {
		return raw
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return raw
	}
	out, err := json.Marshal(dropNULs(v))
	if err != nil {
		return raw
	}
	return out
}

func dropNULs(v any) any {
	switch t := v.(type) {
	case string:
		return dropNUL(t)
	case []any:
		for i := range t {
			t[i] = dropNULs(t[i])
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[dropNUL(k)] = dropNULs(x)
		}
		return out
	}
	return v
}

// dropNUL removes NUL characters, which Postgres text refuses.
func dropNUL(s string) string {
	if strings.IndexByte(s, 0) < 0 {
		return s
	}
	return strings.ReplaceAll(s, "\x00", "")
}

// dropNULsIn removes NUL characters from every name in a list.
func dropNULsIn(names []string) []string {
	for i, n := range names {
		names[i] = dropNUL(n)
	}
	return names
}
