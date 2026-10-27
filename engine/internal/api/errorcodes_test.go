package api_test

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every error code a handler answers is in GET /v1/error-codes, with the
// status it is sent with, so clients can rely on the catalog.
func TestErrorCodeCatalogIsComplete(t *testing.T) {
	e := freshEnv(t)
	anon := &client{t: t, h: e.h}
	listed := map[string]map[int]bool{}
	for _, c := range anon.must(200, "GET", "/v1/error-codes", nil)["data"].([]any) {
		c := c.(map[string]any)
		listed[c["code"].(string)] = map[int]bool{}
		for _, s := range c["statuses"].([]any) {
			listed[c["code"].(string)][int(s.(float64))] = true
		}
		if c["description"] == "" {
			t.Errorf("%s has no description", c["code"])
		}
	}
	statuses := map[string]int{}
	for code := 100; code < 600; code++ {
		if txt := http.StatusText(code); txt != "" {
			statuses[strings.NewReplacer(" ", "", "-", "", "'", "").Replace(txt)] = code
		}
	}
	statuses["RequestEntityTooLarge"] = 413
	used := regexp.MustCompile(`http\.Status([A-Za-z]+), "([a-z_]+)"`)
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "errorcodes.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range used.FindAllStringSubmatch(string(src), -1) {
			status, ok := statuses[m[1]]
			if !ok {
				t.Fatalf("%s: unknown status %s", f, m[1])
			}
			if !listed[m[2]][status] {
				t.Errorf("%s answers %d %s, which GET /v1/error-codes does not list", f, status, m[2])
			}
		}
	}
	for _, kind := range []string{"auth", "invalid", "quota", "unavailable"} {
		if listed["connector_"+kind] == nil {
			t.Errorf("connector_%s is not listed", kind)
		}
	}
}
