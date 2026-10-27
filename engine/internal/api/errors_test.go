package api

import "testing"

func TestErrorParam(t *testing.T) {
	for msg, want := range map[string]string{
		"url must be an absolute http(s) URL":       "url",
		"name is required":                          "name",
		`invalid JSON: json: unknown field "emial"`: "emial",
		"invalid JSON: json: cannot unmarshal number into Go struct field leadInput.email of type string": "email",
		"a team with that name already exists":                 "",
		"send a JSON body with Content-Type: application/json": "",
	} {
		if got := errorParam(400, msg); got != want {
			t.Errorf("errorParam(%q) = %q, want %q", msg, got, want)
		}
	}
	if errorParam(404, "lead must exist") != "" {
		t.Error("only 400s name a param")
	}
}
