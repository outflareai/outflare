package connectors

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// A key in the query string must not survive into an error message, which
// is stored as last_error, returned by the API and logged.
func TestClientErrorsHideTheQuery(t *testing.T) {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:1/v2/validate?api_key=sk-secret&email=a%40b.c", nil)
	_, err := (&Client{}).Do(req)
	if err == nil {
		t.Fatal("expected a connection error")
	}
	if strings.Contains(err.Error(), "sk-secret") || strings.Contains(err.Error(), "a%40b.c") || !strings.Contains(err.Error(), "127.0.0.1:1/v2/validate") {
		t.Fatalf("error: %v", err)
	}
	_, err = DoJSON(context.Background(), &Client{}, "Test", req.Clone(context.Background()), nil)
	if err == nil || strings.Contains(err.Error(), "sk-secret") {
		t.Fatalf("DoJSON error: %v", err)
	}
}
