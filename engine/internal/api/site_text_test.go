package api

import (
	"encoding/json"
	"testing"
)

func TestPgSafeJSON(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`{"a":"plain"}`, `{"a":"plain"}`},
		{`{"a":"café"}`, `{"a":"café"}`},
		{`{"title":"a\u0000b","n":1.50}`, `{"n":1.50,"title":"ab"}`},
		{`{"title":"half \ud83d here"}`, `{"title":"half ` + "�" + ` here"}`},
		{`{"k\u0000":["x\u0000",{"y":"\u0000"}]}`, `{"k":["x",{"y":""}]}`},
	} {
		got := string(pgSafeJSON(json.RawMessage(c.in)))
		if got != c.want {
			t.Errorf("pgSafeJSON(%s) = %s, want %s", c.in, got, c.want)
		}
	}
	if got := dropNULsIn([]string{"a\x00b", "c"}); got[0] != "ab" || got[1] != "c" {
		t.Errorf("dropNULsIn = %q", got)
	}
}
