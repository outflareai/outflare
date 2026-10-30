// Command checkmigrations checks the migrations a branch adds against its
// base, so every deploy can be rolled back and can run while the previous
// version is still serving (docs/migrations.md):
//
//   - shipped migrations are never edited, renamed or deleted, except their
//     comments: a change that leaves every statement as it was runs nothing
//     new (migrations are recorded by name), and a release's review can ask
//     for a comment to be reworded;
//
//   - every new file is NNNN_name.sql, with a number no other file has
//     (a few early ones share numbers; they stay as shipped);
//
//   - a new migration only expands the schema. Statements that break the
//     version still serving (dropping or renaming a table or column, changing
//     a type, a new NOT NULL column without a default, TRUNCATE, ...) need a
//     "-- migration-safety: <reason>" line saying why they are safe now,
//     which is the contract step of an expand/contract change.
//
//     go run ./scripts/checkmigrations -base origin/main -dir engine/internal/db/migrations
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func main() {
	base := flag.String("base", "origin/main", "git ref the branch will merge into")
	dir := flag.String("dir", "engine/internal/db/migrations", "migrations directory, relative to the repository root")
	flag.Parse()

	changes, err := changed(*base, *dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "checkmigrations:", err)
		os.Exit(2)
	}
	files, err := filepath.Glob(filepath.Join(*dir, "*.sql"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "checkmigrations:", err)
		os.Exit(2)
	}
	added := map[string][]byte{}
	commentOnly := map[string]bool{}
	mergeBase := ""
	for _, c := range changes {
		if c.status == "M" {
			if mergeBase == "" {
				mb, err := exec.Command("git", "merge-base", *base, "HEAD").Output()
				if err != nil {
					fmt.Fprintln(os.Stderr, "checkmigrations: merge-base:", err)
					os.Exit(2)
				}
				mergeBase = strings.TrimSpace(string(mb))
			}
			was, err := exec.Command("git", "show", mergeBase+":"+c.path).Output()
			now, err2 := os.ReadFile(c.path)
			if err == nil && err2 == nil && sameStatements(was, now) {
				commentOnly[c.path] = true
			}
		}
		if c.status == "A" {
			b, err := os.ReadFile(c.path)
			if err != nil {
				fmt.Fprintln(os.Stderr, "checkmigrations:", err)
				os.Exit(2)
			}
			added[c.path] = b
		}
	}
	problems := check(changes, files, added, commentOnly)
	for _, p := range problems {
		fmt.Println("::error::" + p)
	}
	if len(problems) > 0 {
		fmt.Printf("\n%d migration problem(s). See docs/migrations.md.\n", len(problems))
		os.Exit(1)
	}
	fmt.Printf("Migrations OK (%d added).\n", len(added))
}

type change struct{ status, path string }

// changed lists the files under dir that differ from the merge base with base.
func changed(base, dir string) ([]change, error) {
	out, err := exec.Command("git", "diff", "--name-status", "--no-renames", base+"...HEAD", "--", dir).Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		return nil, fmt.Errorf("git diff against %s: %v %s", base, err, stderr)
	}
	var cs []change
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 {
			cs = append(cs, change{f[0][:1], f[1]})
		}
	}
	return cs, nil
}

var fileName = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

// check returns every problem, given the changed files, every migration
// file now in the directory and the contents of the added ones.
func check(changes []change, files []string, added map[string][]byte, commentOnly map[string]bool) []string {
	var problems []string
	for _, c := range changes {
		if c.status != "A" && !(c.status == "M" && commentOnly[c.path]) {
			problems = append(problems, fmt.Sprintf("%s: shipped migrations are append-only; add a new migration instead of changing this one", c.path))
		}
	}
	byNumber := map[string][]string{}
	newNumber := map[string]bool{}
	for _, f := range files {
		m := fileName.FindStringSubmatch(filepath.Base(f))
		if m == nil {
			if _, isNew := added[f]; isNew {
				problems = append(problems, fmt.Sprintf("%s: name it NNNN_snake_case.sql", f))
			}
			continue
		}
		byNumber[m[1]] = append(byNumber[m[1]], filepath.Base(f))
		if _, isNew := added[f]; isNew {
			newNumber[m[1]] = true
		}
	}
	numbers := make([]string, 0, len(byNumber))
	for n := range newNumber {
		numbers = append(numbers, n)
	}
	sort.Strings(numbers)
	for _, n := range numbers {
		if names := byNumber[n]; len(names) > 1 {
			problems = append(problems, fmt.Sprintf("migration number %s is used by %s; take the next free number", n, strings.Join(names, " and ")))
		}
	}
	paths := make([]string, 0, len(added))
	for p := range added {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		for _, u := range unsafe(added[p]) {
			problems = append(problems, fmt.Sprintf("%s: %s breaks the version still serving during a deploy and after a rollback. Expand now and contract in a later release; if this is that later step, add a line \"-- migration-safety: <why nothing running uses it>\"", p, u))
		}
	}
	return problems
}

var safetyNote = regexp.MustCompile(`(?im)^\s*--\s*migration-safety:\s*\S`)

// rules are statements the previous version of the code may not survive.
var rules = []struct {
	what string
	re   *regexp.Regexp
}{
	{"DROP TABLE", regexp.MustCompile(`(?i)\bdrop\s+table\b`)},
	{"DROP COLUMN", regexp.MustCompile(`(?i)\bdrop\s+column\b`)},
	{"DROP VIEW/TYPE/FUNCTION/SCHEMA", regexp.MustCompile(`(?i)\bdrop\s+(materialized\s+view|view|type|function|schema)\b`)},
	{"RENAME", regexp.MustCompile(`(?i)\brename\b`)},
	{"a column type change", regexp.MustCompile(`(?i)\balter\s+column\s+\S+\s+(set\s+data\s+)?type\b`)},
	{"SET NOT NULL", regexp.MustCompile(`(?i)\bset\s+not\s+null\b`)},
	{"TRUNCATE", regexp.MustCompile(`(?i)\btruncate\b`)},
	{"a NOT NULL column without a DEFAULT", regexp.MustCompile(`(?i)\badd\s+column\s+(if\s+not\s+exists\s+)?[^,;]*\bnot\s+null\b`)},
}

var (
	lineComment  = regexp.MustCompile(`--[^\n]*`)
	blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	stringLit    = regexp.MustCompile(`'(?:[^']|'')*'`)
	hasDefault   = regexp.MustCompile(`(?i)\bdefault\b|\bgenerated\b`)
)

// sameStatements reports whether two versions of a migration differ only in
// their comments and whitespace. Text inside quotes counts as code.
func sameStatements(a, b []byte) bool {
	return statements(a) == statements(b)
}

func statements(sql []byte) string {
	var out strings.Builder
	space := false
	for i := 0; i < len(sql); i++ {
		switch {
		case sql[i] == '\'':
			j := i + 1 // the closing quote; '' inside is an escaped quote
			for j < len(sql) {
				if sql[j] == '\'' {
					if j+1 < len(sql) && sql[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			if space && out.Len() > 0 {
				out.WriteByte(' ')
			}
			space = false
			out.Write(sql[i:min(j+1, len(sql))])
			i = j
		case sql[i] == '-' && i+1 < len(sql) && sql[i+1] == '-':
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			space = true
		case sql[i] == '/' && i+1 < len(sql) && sql[i+1] == '*':
			end := bytes.Index(sql[i+2:], []byte("*/"))
			if end < 0 {
				i = len(sql)
			} else {
				i += end + 3
			}
			space = true
		case sql[i] == ' ' || sql[i] == '\t' || sql[i] == '\n' || sql[i] == '\r':
			space = true
		default:
			if space && out.Len() > 0 {
				out.WriteByte(' ')
			}
			space = false
			out.WriteByte(sql[i])
		}
	}
	return out.String()
}

// unsafe lists the risky statements in one migration, unless it carries a
// migration-safety note.
func unsafe(sql []byte) []string {
	if safetyNote.Match(sql) {
		return nil
	}
	code := blockComment.ReplaceAll(sql, nil)
	code = lineComment.ReplaceAll(code, nil)
	code = stringLit.ReplaceAll(code, []byte("''"))
	var found []string
	for _, stmt := range bytes.Split(code, []byte(";")) {
		for _, r := range rules {
			m := r.re.Find(stmt)
			if m == nil {
				continue
			}
			if r.what == "a NOT NULL column without a DEFAULT" && hasDefault.Match(stmt[bytes.Index(stmt, m):]) {
				continue
			}
			found = append(found, r.what)
		}
	}
	return found
}
