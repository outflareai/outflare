package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Field selection: ?fields=id,email,status on any GET.
//
// A list answers only those fields of each item in data (its paging
// fields stay), and a single object answers only those of its own. id is
// always kept. Agents and scripts that read a few fields of a long list
// send and parse a fraction of the bytes. Every field is always present
// in an answer (nullable ones as null), so a name that is in no item is
// a mistake, and answers 400 with the fields that exist.

var fieldName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func withFields(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		spec := strings.Join(r.URL.Query()["fields"], ",") // fields=a,b or fields=a&fields=b
		if spec == "" || r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		var want []string
		for _, f := range strings.Split(spec, ",") {
			f = strings.TrimSpace(f)
			if !fieldName.MatchString(f) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "fields must be a comma-separated list of field names, such as fields=id,email,status")
				return
			}
			want = append(want, f)
		}
		rec := &bufferedWriter{header: w.Header(), status: http.StatusOK}
		next.ServeHTTP(rec, r)
		body := rec.buf.Bytes()
		if rec.status/100 == 2 && strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			out, unknown, ok := selectFields(body, want)
			if unknown != "" {
				writeError(w, r, http.StatusBadRequest, "invalid_request", unknown)
				return
			}
			if ok {
				body = out
			}
		}
		w.WriteHeader(rec.status)
		w.Write(body) //nolint:errcheck
	})
}

// selectFields filters an answer to the wanted fields. It reports a
// field that no object has as unknown, and ok=false when the answer is
// not an object (an export, a bare list), which passes through as is.
func selectFields(body []byte, want []string) (out []byte, unknown string, ok bool) {
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return nil, "", false
	}
	keep := func(objs []map[string]json.RawMessage) string {
		have := map[string]bool{}
		for _, o := range objs {
			for k := range o {
				have[k] = true
			}
		}
		if len(objs) > 0 {
			for _, f := range want {
				if !have[f] {
					names := make([]string, 0, len(have))
					for k := range have {
						names = append(names, k)
					}
					sort.Strings(names)
					return "fields cannot include " + f + ": no such field in this answer, which has " + strings.Join(names, ", ")
				}
			}
		}
		for _, o := range objs {
			for k := range o {
				if k != "id" && !slices.Contains(want, k) {
					delete(o, k)
				}
			}
		}
		return ""
	}
	if raw, isList := top["data"]; isList {
		var items []map[string]json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return nil, "", false // data is not a list of objects
		}
		if msg := keep(items); msg != "" {
			return nil, msg, false
		}
		b, _ := json.Marshal(items)
		top["data"] = b
	} else if msg := keep([]map[string]json.RawMessage{top}); msg != "" {
		return nil, msg, false
	}
	b, err := json.Marshal(top)
	if err != nil {
		return nil, "", false
	}
	return append(b, '\n'), "", true
}

// bufferedWriter holds an answer so withFields can filter it before it
// is sent. It shares the real writer's headers.
type bufferedWriter struct {
	header http.Header
	status int
	buf    bytes.Buffer
}

func (b *bufferedWriter) Header() http.Header         { return b.header }
func (b *bufferedWriter) WriteHeader(code int)        { b.status = code }
func (b *bufferedWriter) Write(p []byte) (int, error) { return b.buf.Write(p) }
