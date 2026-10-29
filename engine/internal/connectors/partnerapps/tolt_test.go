package partnerapps

import (
	"errors"
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

func TestDecodeToltList(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		n          int
		more       bool
	}{
		{"flat", `{"data": [{"id": "a"}, {"id": "b"}], "has_more": true}`, 2, true},
		{"nested", `{"success": true, "data": {"data": [{"id": "a"}], "has_more": "false"}, "total_count": 1}`, 1, false},
		{"bare", `[{"id": "a"}, 7]`, 1, false},
		{"named", `{"customers": [{"id": "a"}], "has_more": 1}`, 1, true},
		{"empty", `{"success": true, "data": null, "has_more": false, "next_cursor": ""}`, 0, false},
	} {
		l, err := decodeToltList("customers", 200, "application/json", []byte(tc.body))
		if err != nil || len(l.Data) != tc.n || (l.HasMore != nil && *l.HasMore) != tc.more {
			t.Errorf("%s: %d objects, more %v, err %v", tc.name, len(l.Data), l.HasMore, err)
		}
	}
	_, err := decodeToltList("customers", 200, "text/html; charset=utf-8", []byte("<html>ana@acme.example</html>"))
	var ue *c.UnreadableError
	if !errors.As(err, &ue) || ue.Message != "reading customers from Tolt (HTTP 200, text/html): the answer is not JSON" {
		t.Errorf("html: %v", err)
	}
	_, err = decodeToltList("customers", 200, "application/json", []byte(`{"success": false, "message": "program not found", "code": 4}`))
	if !errors.As(err, &ue) || !strings.Contains(ue.Message, "keys code: number, message: string, success: boolean") ||
		!strings.HasSuffix(ue.Message, "Tolt says program not found") {
		t.Errorf("no list: %v", err)
	}
	if s := toltSnippet([]byte("<p>write to ana@acme.example</p>")); strings.Contains(s, "@") {
		t.Errorf("snippet = %q", s)
	}
}
