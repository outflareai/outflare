package api

import (
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v4"
)

// The versions install health compares against are the packages' own, and
// the script's.
func TestInstallHealthVersionsMatchPackages(t *testing.T) {
	read := func(path, pattern string) string {
		b, err := os.ReadFile("../../../" + path)
		if err != nil {
			t.Fatal(err)
		}
		m := regexp.MustCompile(pattern).FindSubmatch(b)
		if m == nil {
			t.Fatalf("%s: no version", path)
		}
		return string(m[1])
	}
	want := map[string]string{
		"sdk-ts/":          read("sdk/ts/src/index.ts", `SDK_VERSION = "([^"]+)"`),
		"mcp/":             read("mcp/src/server.ts", `VERSION = "([^"]+)"`),
		"outflare-python/": read("sdk/python/src/outflare/_version.py", `__version__ = "([^"]+)"`),
	}
	for _, c := range clientReleases {
		if c.latest != want[c.prefix] {
			t.Errorf("%s: install health says %q is newest, the package is %q", c.prefix, c.latest, want[c.prefix])
		}
		if semverLess(c.latest, c.min) {
			t.Errorf("%s: min %s is above latest %s", c.prefix, c.min, c.latest)
		}
	}
	if v := read("engine/internal/api/site.js", `VERSION = "([^"]+)"`); v != strconv.Itoa(siteScriptVersion) {
		t.Errorf("site.js is version %s, install health expects %d", v, siteScriptVersion)
	}
}

// Every operation the API marks deprecated says what to use instead.
func TestDeprecatedOperationsMatchContract(t *testing.T) {
	b, err := os.ReadFile("../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ops := range doc.Paths {
		for method, node := range ops {
			var op struct {
				OperationID string `yaml:"operationId"`
				Deprecated  bool   `yaml:"deprecated"`
			}
			if method == "parameters" || node.Decode(&op) != nil {
				continue
			}
			if op.Deprecated {
				got = append(got, op.OperationID)
			}
		}
	}
	slices.Sort(got)
	if strings.Join(got, ",") != strings.Join(deprecatedOps(), ",") {
		t.Fatalf("deprecated in api/openapi.yaml: %v; in deprecatedOperations: %v", got, deprecatedOps())
	}
}

func TestSemverLess(t *testing.T) {
	for _, c := range []struct {
		a, b string
		less bool
	}{{"0.0.9", "0.1.0", true}, {"0.1.0", "0.1.0", false}, {"1.2.10", "1.2.9", false}, {"0.1", "0.1.1", true}, {"dev", "0.1.0", false}, {"0.1.0-rc.1", "0.1.1", true}} {
		if semverLess(c.a, c.b) != c.less {
			t.Errorf("%s < %s: want %v", c.a, c.b, c.less)
		}
	}
}
