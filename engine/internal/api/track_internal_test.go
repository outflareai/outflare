package api

import (
	"net/http/httptest"
	"testing"
)

func TestTrackedLeavesOutPageLoads(t *testing.T) {
	for _, c := range []struct {
		method, route string
		status        int
		want          bool
	}{
		{"GET", "GET /cloud/", 200, false}, // an extension's page
		{"GET", "GET /cloud/", 500, true},
		{"GET", "GET /healthz", 200, false},
		{"GET", "GET /v1/workspaces/{ws}/leads", 200, true}, // no session: an API read
		{"POST", "POST /cloud/signup", 201, true},
	} {
		r := httptest.NewRequest(c.method, "/x", nil)
		if got := tracked(r, c.status, c.route); got != c.want {
			t.Errorf("%s %s %d: tracked = %v", c.method, c.route, c.status, got)
		}
	}
}

func TestErrorCodeReadsAPIAndJSONRPCErrors(t *testing.T) {
	for _, c := range []struct {
		status      int
		body        string
		code, param string
	}{
		{400, `{"error":{"code":"invalid_request","param":"email","message":"email is required"}}`, "invalid_request", "email"},
		{403, `{"error":{"code":"forbidden","message":"missing scope leads.manage"}}`, "forbidden", "leads.manage"},
		// The hosted MCP server's refusals carry the API's code in data.
		{400, `{"jsonrpc":"2.0","error":{"code":-32000,"message":"Start a session with an initialize request.","data":{"code":"invalid_request","param":"tools/list"}},"id":null}`, "invalid_request", "tools/list"},
		{400, `{"jsonrpc":"2.0","error":{"code":-32000,"message":"Bad Request"},"id":null}`, "http_400", ""},
		{502, `bad gateway`, "http_502", ""},
		{200, `{}`, "", ""},
	} {
		rec := &trackRecorder{ResponseWriter: httptest.NewRecorder()}
		rec.WriteHeader(c.status)
		rec.Write([]byte(c.body)) //nolint:errcheck
		if code, param := rec.errorCode(); code != c.code || param != c.param {
			t.Errorf("%s: got %q %q, want %q %q", c.body, code, param, c.code, c.param)
		}
	}
}
