#!/bin/sh
# Prints the section of CHANGELOG.md for a version tag (v0.3.0 reads the
# "## [0.3.0]" section), for the release notes. Fails when there is none, so a
# tag never ships without its changelog entry (docs/releasing.md).
set -eu
tag="${1:?usage: scripts/changelog-section.sh v0.3.0}"
version="${tag#v}"
notes=$(awk -v v="$version" '
	/^## \[/ { if (found) exit; found = index($0, "## [" v "]") == 1; next }
	found { print }
' "$(dirname "$0")/../CHANGELOG.md")
if [ -z "$(printf '%s' "$notes" | tr -d '[:space:]')" ]; then
	echo "CHANGELOG.md has no \"## [$version]\" section. Rename \"## [Unreleased]\" to it (docs/releasing.md) before tagging $tag." >&2
	exit 1
fi
printf '%s\n' "$notes"
