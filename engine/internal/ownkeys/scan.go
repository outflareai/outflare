package ownkeys

import (
	"bufio"
	"bytes"
	_ "embed"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A Rule is one line of an allowlist: something Outflare's own operations
// may read, and the only files that may read it.
//
//	env NAME|PREFIX*   path[,path...]  why
//	dynamic PATTERN    path[,path...]  why   (a read by computed name)
//	import PKG         path[,path...]  why   (an SDK that reads ambient credentials)
//
// A dynamic PATTERN is the name with each computed part as *, exactly as
// keycheck prints it: *CLIENT_ID for os.Getenv(prefix + "CLIENT_ID"),
// OUTFLARE_* for os.Getenv("OUTFLARE_" + name), * for os.Getenv(name),
// os.Environ, or os.Getenv when the function itself is handed on.
// A path is a file, or a directory when it ends in /, relative to the root
// that is scanned. Lines starting with # are comments.
type Rule struct {
	Kind   string // env, dynamic or import
	Name   string // the variable (PREFIX* for a prefix), the pattern or the package
	Paths  []string
	Reason string
}

// Matches reports whether an env rule covers the variable name.
func (r Rule) Matches(name string) bool {
	if r.Kind != "env" {
		return false
	}
	if p, ok := strings.CutSuffix(r.Name, "*"); ok {
		return strings.HasPrefix(name, p)
	}
	return r.Name == name
}

func (r Rule) covers(path string) bool {
	return slices.ContainsFunc(r.Paths, func(p string) bool {
		if strings.HasSuffix(p, "/") {
			return strings.HasPrefix(path, p)
		}
		return path == p
	})
}

//go:embed allowed.txt
var engineAllowed []byte

// Allowed is the engine's own allowlist (allowed.txt).
func Allowed() []Rule {
	rules, err := ParseRules(engineAllowed)
	if err != nil {
		panic(err)
	}
	return rules
}

// ParseRules reads an allowlist. Every line needs its reason.
func ParseRules(b []byte) ([]Rule, error) {
	var out []Rule
	sc := bufio.NewScanner(bytes.NewReader(b))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		r := Rule{Kind: f[0]}
		if !slices.Contains([]string{"env", "dynamic", "import"}, r.Kind) || len(f) < 4 {
			return nil, fmt.Errorf("allowlist line %d: want \"env|dynamic|import NAME paths why\"", n)
		}
		r.Name, r.Paths, r.Reason = f[1], strings.Split(f[2], ","), strings.Join(f[3:], " ")
		out = append(out, r)
	}
	return out, sc.Err()
}

// A Finding is code that could hand a workspace a key that isn't its own.
type Finding struct {
	File string
	Line int
	Kind string // env, dynamic or import
	Name string // the variable, the pattern (see Rule) or the package
	Why  string
}

func (f Finding) String() string {
	switch f.Kind {
	case "env":
		return fmt.Sprintf("%s:%d: reads %s (%s)", f.File, f.Line, f.Name, f.Why)
	case "import":
		return fmt.Sprintf("%s:%d: imports %s, which reads credentials from the environment", f.File, f.Line, f.Name)
	}
	return fmt.Sprintf("%s:%d: reads the environment by a computed name: dynamic %s", f.File, f.Line, f.Name)
}

// ambientSDKs are packages that pick up credentials from the environment
// or the machine unless told not to.
var ambientSDKs = []string{
	"github.com/anthropics/anthropic-sdk-go", "github.com/openai/openai-go", "golang.org/x/oauth2/google",
	"cloud.google.com/go", "google.golang.org/api", "google.golang.org/genai", "github.com/google/generative-ai-go",
	"github.com/aws/aws-sdk-go", "github.com/Azure/azure-sdk-for-go",
	"@anthropic-ai/sdk", "openai", "@google/genai", "@google/generative-ai", "@mistralai/mistralai", "@aws-sdk/", "@azure/",
}

func ambientSDK(pkg string) bool {
	return slices.ContainsFunc(ambientSDKs, func(p string) bool {
		return pkg == p || strings.HasPrefix(pkg, strings.TrimSuffix(p, "/")+"/")
	})
}

var skipDirs = []string{".git", "node_modules", "dist", "build", "upstream", "testdata", "vendor", ".astro", "coverage"}

// Scan reports every finding under root that rules don't allow.
func Scan(root string, rules []Rule, vendors []string) ([]Finding, error) {
	all, err := collect(root, vendors)
	var out []Finding
	for _, f := range all {
		if !allowed(f, rules) {
			out = append(out, f)
		}
	}
	return out, err
}

// collect finds every read under root that needs an allowlist entry.
func collect(root string, vendors []string) ([]Finding, error) {
	var out []Finding
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && slices.Contains(skipDirs, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		var found []Finding
		switch name := d.Name(); {
		case strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go"):
			found, err = scanGo(path, rel)
		case isScript(name):
			found, err = scanScript(path, rel)
		default:
			return nil
		}
		if err != nil {
			return err
		}
		for _, f := range found {
			if f.Kind == "env" {
				if f.Why = ThirdParty(f.Name, vendors); f.Why == "" {
					continue
				}
			}
			out = append(out, f)
		}
		return nil
	})
	return out, err
}

func allowed(f Finding, rules []Rule) bool {
	return slices.ContainsFunc(rules, func(r Rule) bool {
		if r.Kind != f.Kind || !r.covers(f.File) {
			return false
		}
		switch r.Kind {
		case "env":
			return r.Matches(f.Name)
		case "import":
			return f.Name == r.Name || strings.HasPrefix(f.Name, strings.TrimSuffix(r.Name, "/")+"/")
		}
		return f.Name == r.Name
	})
}

func isScript(name string) bool {
	for _, ext := range []string{".ts", ".tsx", ".js", ".mjs", ".cjs", ".astro"} {
		if strings.HasSuffix(name, ext) {
			base := strings.TrimSuffix(name, ext)
			return !strings.HasSuffix(base, ".test") && !strings.HasSuffix(base, ".spec") && !strings.HasSuffix(base, ".d")
		}
	}
	return false
}

// envFuncs are the calls that read one variable: os.Getenv and friends,
// and helpers with the same names (config's getenv, cli's Env.Getenv).
var envFuncs = []string{"Getenv", "LookupEnv", "getenv", "lookupEnv", "envOr"}

func scanGo(path, rel string) ([]Finding, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var out []Finding
	at := func(n ast.Node) int { return fset.Position(n.Pos()).Line }
	osName := ""
	for _, im := range file.Imports {
		pkg, _ := strconv.Unquote(im.Path.Value)
		if ambientSDK(pkg) {
			out = append(out, Finding{File: rel, Line: at(im), Kind: "import", Name: pkg})
		}
		if pkg == "os" || pkg == "syscall" {
			osName = "os"
			if im.Name != nil {
				osName = im.Name.Name
			}
		}
	}
	isOS := func(e ast.Expr) bool {
		id, ok := e.(*ast.Ident)
		return ok && (id.Name == osName || id.Name == "syscall")
	}
	called := map[ast.Node]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		var fn string
		switch f := call.Fun.(type) {
		case *ast.Ident:
			fn = f.Name
		case *ast.SelectorExpr:
			fn = f.Sel.Name
			called[f] = true
			if isOS(f.X) && fn == "Environ" {
				out = append(out, Finding{File: rel, Line: at(call), Kind: "dynamic", Name: "os.Environ"})
				return true
			}
		}
		if !slices.Contains(envFuncs, fn) || len(call.Args) == 0 {
			return true
		}
		if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
			name, _ := strconv.Unquote(lit.Value)
			out = append(out, Finding{File: rel, Line: at(call), Kind: "env", Name: name})
		} else {
			out = append(out, Finding{File: rel, Line: at(call), Kind: "dynamic", Name: pattern(call.Args[0])})
		}
		return true
	})
	// os.Getenv handed on as a value reads whatever its holder asks for.
	ast.Inspect(file, func(n ast.Node) bool {
		if s, ok := n.(*ast.SelectorExpr); ok && !called[s] && isOS(s.X) && slices.Contains(envFuncs, s.Sel.Name) {
			out = append(out, Finding{File: rel, Line: at(s), Kind: "dynamic", Name: "os." + s.Sel.Name})
		}
		return true
	})
	return out, nil
}

// pattern is a computed name with its fixed parts kept and the rest as *.
func pattern(e ast.Expr) string {
	var p string
	switch e := e.(type) {
	case *ast.BasicLit:
		if s, err := strconv.Unquote(e.Value); err == nil {
			return s
		}
	case *ast.ParenExpr:
		return pattern(e.X)
	case *ast.BinaryExpr:
		if e.Op == token.ADD {
			p = pattern(e.X) + pattern(e.Y)
		}
	}
	if p == "" {
		return "*"
	}
	return strings.ReplaceAll(p, "**", "*")
}

var (
	envTmpl    = regexp.MustCompile("process\\.env\\[\\s*`([^`]*)`\\s*\\]")
	tmplPart   = regexp.MustCompile(`\$\{[^}]*\}`)
	envDot     = regexp.MustCompile(`(?:process\.env|import\.meta\.env|Bun\.env)\.([A-Za-z_][A-Za-z0-9_]*)`)
	envIndex   = regexp.MustCompile(`(?:process\.env|import\.meta\.env|Bun\.env)\[\s*["']([A-Za-z0-9_]+)["']\s*\]`)
	envAny     = regexp.MustCompile(`process\.env\b|import\.meta\.env\b|Bun\.env\b|Deno\.env\b`)
	scriptPkgs = regexp.MustCompile(`(?:from\s+|import\s+|require\(\s*|import\(\s*)["']([^"']+)["']`)
)

func scanScript(path, rel string) ([]Finding, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Finding
	for i, line := range strings.Split(string(b), "\n") {
		n := i + 1
		for _, m := range scriptPkgs.FindAllStringSubmatch(line, -1) {
			if ambientSDK(m[1]) {
				out = append(out, Finding{File: rel, Line: n, Kind: "import", Name: m[1]})
			}
		}
		named := 0
		for _, re := range []*regexp.Regexp{envDot, envIndex} {
			for _, m := range re.FindAllStringSubmatch(line, -1) {
				out = append(out, Finding{File: rel, Line: n, Kind: "env", Name: m[1]})
				named++
			}
		}
		for _, m := range envTmpl.FindAllStringSubmatch(line, -1) {
			out = append(out, Finding{File: rel, Line: n, Kind: "dynamic", Name: tmplPart.ReplaceAllString(m[1], "*")})
			named++
		}
		for range len(envAny.FindAllString(line, -1)) - named {
			out = append(out, Finding{File: rel, Line: n, Kind: "dynamic", Name: "*"})
		}
	}
	return out, nil
}
