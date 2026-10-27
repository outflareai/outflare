#!/bin/sh
# Runs one part of the Go tests, so CI can run the parts side by side, each
# with its own database. Parts: "api 1 of N" .. "api N of N" split the API
# package's tests (the slowest package by far) by name; "rest" runs every
# other package. Every test runs in exactly one part.
#
#   scripts/test-shard.sh api 1 2
#   scripts/test-shard.sh rest
#
# Packages whose tests use TEST_DATABASE_URL share one database, so they run
# one at a time (-p 1); the others run in parallel.
set -eu
cd "$(dirname "$0")/.."
api=./engine/internal/api

case "${1:?usage: scripts/test-shard.sh api <i> <n> | rest}" in
api)
	i="${2:?shard number}"
	n="${3:?shard count}"
	# -race so the listing reuses the build the run needs.
	tests=$(go test -race -list '.*' "$api" | grep -E '^(Test|Example|Fuzz)' | sort |
		awk -v i="$i" -v n="$n" '(NR - 1) % n == i - 1' | paste -sd '|' -)
	if [ -z "$tests" ]; then
		echo "api part $i of $n has no tests" >&2
		exit 1
	fi
	go test -race -count=1 -run "^($tests)\$" "$api"
	;;
rest)
	db=$(grep -rl TEST_DATABASE_URL --include='*_test.go' engine scripts |
		xargs -n1 dirname | sort -u | sed 's|^|./|' | grep -vxF "$api" || true)
	others=$(go list ./... | sed "s|^$(go list -m)|.|" | grep -vxF "$api" |
		awk -v db="$db" 'BEGIN { n = split(db, a, "\n"); for (k = 1; k <= n; k++) skip[a[k]] = 1 } !skip[$0]')
	if [ -n "$db" ]; then
		# shellcheck disable=SC2086
		go test -race -count=1 -p 1 $db
	fi
	# shellcheck disable=SC2086
	go test -race -count=1 $others
	;;
*)
	echo "unknown part: $1" >&2
	exit 2
	;;
esac
