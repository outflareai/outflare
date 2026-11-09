package outflare

import (
	"strings"
	"testing"
)

func TestAPIGuideListsWorkspaceOperations(t *testing.T) {
	g := APIGuide()
	if !strings.Contains(g, "GET /sequences: ") || !strings.Contains(g, "POST /memories: ") {
		t.Fatalf("guide lacks workspace operations:\n%.500s", g)
	}
	if strings.Contains(g, "/v1/workspaces") {
		t.Fatal("guide paths should be relative to the workspace")
	}
	d := APIDocs("/memories")
	if !strings.Contains(d, "operationId: createBrandMemory") || !strings.Contains(d, "Memory:") {
		t.Fatalf("docs for /memories lack the operation or its schema:\n%.800s", d)
	}
}
