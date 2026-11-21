package jserrors

import (
	"strings"
	"testing"
)

func TestFile(t *testing.T) {
	for in, want := range map[string]string{
		"https://northpeak.example/assets/famer-motion.config-B6vn08tg.js":   "famer-motion-config.js",
		"https://northpeak.example/assets/famer-motion.config-mZtfGkFb.js":   "famer-motion-config.js",
		"https://northpeak.example/_next/static/chunks/2ut3ert33vfh-.js":     ".js",
		"https://northpeak.example/assets/index-3f9a1c2d.js?v=1":             "index.js",
		"https://northpeak.example/static/js/main.8d2f1c4e9a7b3d21.chunk.js": "main-chunk.js",
		"/assets/app.js":           "app.js",
		"https://x.example/":       "",
		"webpack://app/./src/a.ts": "a.ts",
	} {
		if got := File(in); got != want {
			t.Errorf("File(%q) = %q, want %q", in, got, want)
		}
	}
}

// One bug is one group whatever the browser and the build.
func TestGroup(t *testing.T) {
	chrome := "TypeError: Cannot read properties of undefined (reading 'plan')\n    at rX (https://app.example/assets/app-B6vn08tg.js:1:47939)\n    at https://app.example/assets/vendor-aa11bb22.js:2:10"
	firefox := "rX@https://app.example/assets/app-mZtfGkFb.js:1:51000\n@https://app.example/assets/vendor-cc33dd44.js:2:11"
	msg := "Cannot read properties of undefined (reading 'plan')"
	if Group("TypeError", msg, chrome) != Group("TypeError", msg, firefox) {
		t.Fatal("Chrome and Firefox stacks of one bug in two builds are two groups")
	}
	if Group("TypeError", msg, chrome) == Group("TypeError", "x is not a function", chrome) {
		t.Fatal("two messages are one group")
	}
	if g := Group("TypeError", msg, chrome); len(g) != 16 || strings.Trim(g, "0123456789abcdef") != "" {
		t.Fatalf("group %q", g)
	}
	// Numbers and ids in the message don't split it.
	if Group("Error", "plan 42 not found", "") != Group("Error", "plan 7 not found", "") {
		t.Fatal("numbers split a group")
	}
	// A script that didn't arrive, in any browser and any file, is one issue.
	a := Group("TypeError", "Failed to fetch dynamically imported module: https://app.example/assets/famer-motion.config-B6vn08tg.js", "")
	b := Group("TypeError", "Importing a module script failed.", "")
	c := Group("TypeError", "error loading dynamically imported module: https://app.example/assets/x-mZtfGkFb.js", "")
	if a != b || b != c {
		t.Fatal("failed script loads are several groups")
	}
	// A minified React error is one group wherever it was thrown.
	r1 := Group("Error", "Minified React error #418; visit https://react.dev/errors/418?args[]=text&args[]= for the full message", chrome)
	r2 := Group("Error", "Minified React error #418; visit https://react.dev/errors/418?args[]=HTML&args[]= for the full message", firefox)
	if r1 != r2 {
		t.Fatal("one React error is two groups")
	}
}

func TestExpand(t *testing.T) {
	got := Expand("Minified React error #418; visit https://react.dev/errors/418?args[]=text&args[]= for the full message or use the non-minified dev environment")
	if !strings.HasPrefix(got, "Hydration failed because the server rendered text didn't match the client.") {
		t.Fatalf("React 418: %q", got)
	}
	if got := Expand("Minified Redux Toolkit error #0; visit https://redux-toolkit.js.org/Errors?code=0 for the full message"); got != "prepareAction did not return an object" {
		t.Fatalf("Redux Toolkit 0: %q", got)
	}
	if got := Expand("Minified Redux error #1; visit https://redux.js.org/Errors?code=1"); got == "" {
		t.Fatal("Redux 1 not expanded")
	}
	if Expand("plan 7 not found") != "" || Expand("Minified React error #999999") != "" {
		t.Fatal("expanded something that isn't a known code")
	}
}

func TestNoise(t *testing.T) {
	for _, c := range []struct{ msg, stack, source, url, want string }{
		{"x is undefined", "at f (chrome-extension://abcdef/content.js:1:2)", "", "https://app.example/", NoiseExtension},
		{"x is undefined", "f@webkit-masked-url://hidden/:1:2", "", "https://app.example/", NoiseExtension},
		{"Script failed", "", "chrome-extension://abcdef/inject.js", "https://app.example/", NoiseExtension},
		{"Object Not Found Matching Id:3, MethodName:update, ParamCount:4", "", "", "https://app.example/", NoiseLinkScanner},
		{"Cannot redefine property: ethereum", "", "", "https://app.example/", NoiseInjected},
		{"window.__firefox__.reader is undefined", "", "", "https://app.example/", NoiseInjected},
		{"x is undefined", "", "", "file:///Users/someone/Desktop/page.html", NoiseLocalFile},
		{"x is undefined", "", "", "http://203.0.113.7/pricing", NoiseIPHost},
		{"x is undefined", "at f (chrome-extension://a/b.js:1:2)\n at g (https://app.example/app.js:3:4)", "", "https://app.example/", ""},
		{"x is undefined", "", "", "https://app.example/", ""},
	} {
		if got := Noise(c.msg, c.stack, c.source, c.url); got != c.want {
			t.Errorf("Noise(%q, %q, %q, %q) = %q, want %q", c.msg, c.stack, c.source, c.url, got, c.want)
		}
	}
}
