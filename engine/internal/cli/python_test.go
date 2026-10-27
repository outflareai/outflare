package cli

import (
	"bytes"
	"os"
	"testing"

	spec "github.com/outflareai/outflare/api"
)

// The committed Python operations must match the API document, the way
// the TypeScript SDK's schema must.
func TestPythonSDKIsCurrent(t *testing.T) {
	want, err := GeneratePython(spec.OpenAPI)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("../../../sdk/python/src/outflare/_operations.py")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("sdk/python/src/outflare/_operations.py is out of date: run go run ./engine/cmd/sdkgen")
	}
}
