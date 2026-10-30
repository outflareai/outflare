package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestUnsafe(t *testing.T) {
	cases := []struct {
		sql  string
		want []string
	}{
		{`CREATE TABLE a (id uuid PRIMARY KEY);`, nil},
		{`ALTER TABLE a ADD COLUMN b text;`, nil},
		{`ALTER TABLE a ADD COLUMN b text NOT NULL DEFAULT '';`, nil},
		{`ALTER TABLE a ADD COLUMN b boolean NOT NULL DEFAULT false, ADD COLUMN c text;`, nil},
		{`ALTER TABLE a ADD COLUMN b text NOT NULL;`, []string{"a NOT NULL column without a DEFAULT"}},
		{`ALTER TABLE a DROP COLUMN b;`, []string{"DROP COLUMN"}},
		{`DROP TABLE a;`, []string{"DROP TABLE"}},
		{`ALTER TABLE a RENAME COLUMN b TO c;`, []string{"RENAME"}},
		{`ALTER TABLE a ALTER COLUMN b TYPE bigint;`, []string{"a column type change"}},
		{`ALTER TABLE a ALTER COLUMN b SET NOT NULL;`, []string{"SET NOT NULL"}},
		{`ALTER TABLE a ALTER COLUMN b DROP NOT NULL;`, nil},
		{"-- we never DROP TABLE here\nCREATE TABLE a (note text DEFAULT 'drop table');", nil},
		{"/* DROP COLUMN */ CREATE INDEX ON a (b);", nil},
		{"-- migration-safety: nothing reads b since 0042\nALTER TABLE a DROP COLUMN b;", nil},
		{`CREATE TABLE a (id uuid PRIMARY KEY, name text NOT NULL);`, nil},
	}
	for _, c := range cases {
		if got := unsafe([]byte(c.sql)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("unsafe(%q) = %v, want %v", c.sql, got, c.want)
		}
	}
}

func TestCheck(t *testing.T) {
	d := "m"
	files := []string{d + "/0001_init.sql", d + "/0002_a.sql", d + "/0002_b.sql", d + "/0003_Bad-Name.sql"}
	changes := []change{{"M", d + "/0001_init.sql"}, {"A", d + "/0002_b.sql"}, {"A", d + "/0003_Bad-Name.sql"}}
	added := map[string][]byte{d + "/0002_b.sql": []byte("DROP TABLE x;"), d + "/0003_Bad-Name.sql": nil}
	got := strings.Join(check(changes, files, added, nil), "\n")
	for _, want := range []string{"0001_init.sql: shipped migrations are append-only", "number 0002 is used by 0002_a.sql and 0002_b.sql", "0003_Bad-Name.sql: name it", "0002_b.sql: DROP TABLE"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// Every migration already shipped passes, so the rules match how this
// repository has always written them.
func TestShippedMigrationsPass(t *testing.T) {
	files, _ := filepath.Glob("../../engine/internal/db/migrations/*.sql")
	if len(files) == 0 {
		t.Skip("no migrations")
	}
	added := map[string][]byte{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		added[f] = b
	}
	// Only the statement rules: some early files share a number, and
	// renaming them now would run them again.
	var p []string
	for _, f := range files {
		for _, u := range unsafe(added[f]) {
			p = append(p, f+": "+u)
		}
	}
	if len(p) > 0 {
		t.Errorf("shipped migrations fail the check:\n%s", strings.Join(p, "\n"))
	}
}

func TestCommentOnlyEdits(t *testing.T) {
	d := "m"
	files := []string{d + "/0001_init.sql", d + "/0002_a.sql"}
	changes := []change{{"M", d + "/0001_init.sql"}, {"M", d + "/0002_a.sql"}}
	got := check(changes, files, nil, map[string]bool{d + "/0001_init.sql": true})
	if len(got) != 1 || !strings.Contains(got[0], "0002_a.sql: shipped migrations are append-only") {
		t.Errorf("check = %v", got)
	}
	cases := []struct {
		a, b string
		same bool
	}{
		{"-- why\nCREATE TABLE a (id int);", "-- a better why\nCREATE TABLE a (id int);", true},
		{"CREATE TABLE a (id int); /* x */", "CREATE  TABLE a\n(id int);", true},
		{"CREATE TABLE a (id int);", "CREATE TABLE b (id int);", false},
		{"INSERT INTO a VALUES ('x -- y');", "INSERT INTO a VALUES ('x -- z');", false},
		{"INSERT INTO a VALUES ('it''s');", "INSERT INTO a VALUES ('it''s'); -- note", true},
	}
	for _, c := range cases {
		if got := sameStatements([]byte(c.a), []byte(c.b)); got != c.same {
			t.Errorf("sameStatements(%q, %q) = %v", c.a, c.b, got)
		}
	}
}
