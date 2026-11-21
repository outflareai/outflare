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
//
// With -additive it prints whether every added migration only adds to the
// schema, in ways the previous version of the code cannot notice. CI runs
// the previous version's tests on the new schema (the rollback check) only
// when this says false.
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
	onlyAdds := flag.Bool("additive", false, "print true when every added migration only adds to the schema (see additive), false otherwise, and check nothing else")
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
			was, err := exec.Command("git", "show", mergeBase+":./"+c.path).Output()
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
	if *onlyAdds {
		// Anything but new files (an edit, a comment reworded) gets the full
		// rollback check: this answers only for migrations that are added.
		ok := len(added) > 0 && len(added) == len(changes)
		for _, sql := range added {
			ok = ok && additive(sql)
		}
		fmt.Println(ok)
		return
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
// Paths are relative to the working directory (--relative, and ":./" in git
// show above), so the check also works where the engine is a folder of a
// larger repository and is run from that folder.
func changed(base, dir string) ([]change, error) {
	out, err := exec.Command("git", "diff", "--name-status", "--no-renames", "--relative", base+"...HEAD", "--", dir).Output()
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

// The statements a migration may hold and still count as only adding. Each
// is matched against one whole statement, comments and string contents
// removed, so anything else (a trigger, a function, a DO block, UPDATE, a
// constraint on a table that already exists) is not on the list.
var (
	createTable  = regexp.MustCompile(`(?is)^create\s+table\s+(?:if\s+not\s+exists\s+)?([a-z0-9_."]+)\s*\(.*\)$`)
	createIndex  = regexp.MustCompile(`(?is)^create\s+(unique\s+)?index\s+(?:concurrently\s+)?(?:if\s+not\s+exists\s+)?[a-z0-9_"]+\s+on\s+(?:only\s+)?([a-z0-9_."]+)\b`)
	alterTable   = regexp.MustCompile(`(?is)^alter\s+table\s+(?:if\s+exists\s+)?(?:only\s+)?([a-z0-9_."]+)\s+(.*)$`)
	commentOn    = regexp.MustCompile(`(?is)^comment\s+on\s+`)
	references   = regexp.MustCompile(`(?i)\breferences\b`)
	gentleDelete = regexp.MustCompile(`(?i)\bon\s+delete\s+(cascade|set\s+null)\b`)
)

// additive reports whether a migration only adds things the previous
// version of the code cannot notice, so that version's tests on the new
// schema would show nothing: new tables and plain indexes. It is deliberately strict, and says false
// whenever it cannot tell:
//
//   - CREATE TABLE, when every foreign key in it deletes or clears its rows
//     with the row it points at (ON DELETE CASCADE or SET NULL). Any other
//     foreign key would stop the old code deleting that row after a
//     rollback, once the new code has written to the table;
//   - CREATE INDEX on any table; CREATE UNIQUE INDEX only on a table this
//     migration creates (on an existing table it can refuse the old code's
//     writes);
//   - any ALTER TABLE on a table this migration creates, and COMMENT ON.
//
// A new column on an existing table is not on the list, however harmless it
// looks: the old code's queries can stop working when a join gains a second
// column of the same name.
//
// A migration-safety note marks a contract step, which is never additive.
func additive(sql []byte) bool {
	if safetyNote.Match(sql) {
		return false
	}
	code := blockComment.ReplaceAll(sql, nil)
	code = lineComment.ReplaceAll(code, nil)
	code = stringLit.ReplaceAll(code, []byte("''"))
	gentle := func(s string) bool {
		return len(references.FindAllString(s, -1)) == len(gentleDelete.FindAllString(s, -1))
	}
	created := map[string]bool{}
	seen := false
	for _, raw := range bytes.Split(code, []byte(";")) {
		stmt := strings.TrimSpace(string(raw))
		if stmt == "" {
			continue
		}
		seen = true
		if m := createTable.FindStringSubmatch(stmt); m != nil {
			if !gentle(stmt) {
				return false
			}
			created[strings.ToLower(m[1])] = true
			continue
		}
		if m := createIndex.FindStringSubmatch(stmt); m != nil {
			if m[1] != "" && !created[strings.ToLower(m[2])] {
				return false
			}
			continue
		}
		if commentOn.MatchString(stmt) {
			continue
		}
		// ALTER TABLE only on a table this migration creates. Even a nullable
		// column added to an existing table can break the old code: a query
		// that joins the table and names a column of the other side without
		// its table then finds two columns of that name.
		if m := alterTable.FindStringSubmatch(stmt); m == nil || !created[strings.ToLower(m[1])] {
			return false
		}
	}
	return seen
}
