#!/bin/sh
# Runs one part of the Go tests, so CI can run the parts side by side, each
# with its own database. Parts: "api 1 of N" .. "api N of N" split the API
# package's tests (the slowest package by far) by name; "rest" runs every
# other package. Every test runs in exactly one part.
#
#   scripts/test-shard.sh api 1 2
#   scripts/test-shard.sh rest
#   CHANGED="$(git diff --name-only main...)" scripts/test-shard.sh light
#
# "light" is a pull request's check (the merge queue's batch runs every
# part): the packages holding the files in $CHANGED, the packages that import
# them, and the bring-your-own-keys scan.
#
# RACE (default -race) is the race detector flag. CI runs the race detector
# nightly on main and leaves it off elsewhere: it makes the tests two to three
# times slower and finds the same failures only in rare cases.
#
# Packages whose tests use TEST_DATABASE_URL share one database, so they run
# one at a time (-p 1); the others run in parallel.
#
# A test that fails is run once more on its own. If it passes then, the part
# passes and the test's name is written to $FLAKY_FILE (when set) so CI can
# file it; a second failure, a build failure or a panic fails the part.
set -eu
cd "$(dirname "$0")/.."
api=./engine/internal/api
log=$(mktemp)
trap 'rm -f "$log" "$log.rc"' EXIT
race=${RACE--race}

# run_tests <go test args...>: go test, retrying failed tests once. Half the
# API package under -race runs close to go test's default 10-minute timeout,
# so a slow runner would panic a part with no test failing; allow 25.
run_tests() {
	(
		if go test $race -count=1 -timeout 25m "$@" 2>&1; then echo 0 >"$log.rc"; else echo $? >"$log.rc"; fi
	) | tee "$log"
	[ "$(cat "$log.rc")" = 0 ] && return 0
	# Top-level failed tests, grouped by the package line that follows them.
	failed=$(awk '/^--- FAIL: /{t = t (t ? "|" : "") $3} /^FAIL\t/{ if (t) print $2, t; t = "" }' "$log")
	if [ -z "$failed" ] || grep -qE '^panic:|\[build failed\]|\[setup failed\]' "$log"; then
		return 1
	fi
	echo "Re-running the failed tests once:" >&2
	echo "$failed" >&2
	echo "$failed" | while read -r pkg tests; do
		go test $race -count=1 -timeout 25m -p 1 -run "^($tests)\$" "$pkg" || exit 1
	done || return 1
	if [ -n "${FLAKY_FILE:-}" ]; then
		echo "$failed" >>"$FLAKY_FILE"
	fi
	echo "The failed tests passed when re-run; reported as flaky." >&2
}

# split_db <packages...>: runs the packages whose tests use TEST_DATABASE_URL
# one at a time, and the others in parallel.
split_db() {
	db=$(grep -rl TEST_DATABASE_URL --include='*_test.go' engine scripts |
		xargs -n1 dirname | sort -u | sed 's|^|./|' || true)
	mine=$(printf '%s\n' "$@" | awk -v db="$db" 'BEGIN { n = split(db, a, "\n"); for (k = 1; k <= n; k++) d[a[k]] = 1 } d[$0]')
	others=$(printf '%s\n' "$@" | awk -v db="$db" 'BEGIN { n = split(db, a, "\n"); for (k = 1; k <= n; k++) d[a[k]] = 1 } $0 != "" && !d[$0]')
	if [ -n "$mine" ]; then
		# shellcheck disable=SC2086
		run_tests -p 1 $mine
	fi
	if [ -n "$others" ]; then
		# shellcheck disable=SC2086
		run_tests $others
	fi
}

case "${1:?usage: scripts/test-shard.sh api <i> <n> | rest | light}" in
api)
	i="${2:?shard number}"
	n="${3:?shard count}"
	# The same flags as the run, so the listing reuses its build.
	tests=$(go test $race -list '.*' "$api" | grep -E '^(Test|Example|Fuzz)' | sort |
		awk -v i="$i" -v n="$n" '(NR - 1) % n == i - 1' | paste -sd '|' -)
	if [ -z "$tests" ]; then
		echo "api part $i of $n has no tests" >&2
		exit 1
	fi
	run_tests -run "^($tests)\$" "$api"
	;;
rest)
	db=$(grep -rl TEST_DATABASE_URL --include='*_test.go' engine scripts |
		xargs -n1 dirname | sort -u | sed 's|^|./|' | grep -vxF "$api" || true)
	others=$(go list ./... | sed "s|^$(go list -m)|.|" | grep -vxF "$api" |
		awk -v db="$db" 'BEGIN { n = split(db, a, "\n"); for (k = 1; k <= n; k++) skip[a[k]] = 1 } !skip[$0]')
	if [ -n "$db" ]; then
		# shellcheck disable=SC2086
		run_tests -p 1 $db
	fi
	# shellcheck disable=SC2086
	run_tests $others
	;;
light)
	mod=$(go list -m)
	# Every package as "<dir relative to here> <import path>".
	all=$(go list -f '{{.Dir}} {{.ImportPath}}' ./... | sed "s|^$(pwd)/||; s|^$(pwd) |. |")
	# The package each changed file belongs to: the nearest folder up with Go
	# code, so data a package embeds (integrations/, playbooks/, api/) counts.
	# The engine's dashboards and alert rules are checked by the metrics tests.
	changed=$(printf '%s\n' "${CHANGED:-}" | while read -r f; do
		[ -n "$f" ] || continue
		case "$f" in deploy/monitoring/*) echo "$mod/engine/internal/metrics"; continue ;; esac
		d=$(dirname "$f")
		while :; do
			p=$(printf '%s\n' "$all" | awk -v d="$d" '$1 == d { print $2 }')
			if [ -n "$p" ]; then echo "$p"; break; fi
			[ "$d" != . ] || break
			d=$(dirname "$d")
		done
	done | sort -u)
	# And the packages that import them, tests included.
	importers=$(go list -f '{{.ImportPath}}{{range .Imports}} {{.}}{{end}}{{range .TestImports}} {{.}}{{end}}{{range .XTestImports}} {{.}}{{end}}' ./... |
		awk -v c="$changed" 'BEGIN { n = split(c, a, "\n"); for (k = 1; k <= n; k++) want[a[k]] = 1 } { for (i = 2; i <= NF; i++) if (want[$i]) { print $1; next } }')
	pkgs=$(printf '%s\n%s\n%s\n' "$changed" "$importers" "$mod/engine/internal/ownkeys" | sed '/^$/d' | sort -u | sed "s|^$mod|.|")
	echo "Testing the changed packages and their importers:" >&2
	echo "$pkgs" >&2
	# shellcheck disable=SC2086
	split_db $pkgs
	;;
*)
	echo "unknown part: $1" >&2
	exit 2
	;;
esac
