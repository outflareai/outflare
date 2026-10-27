#!/usr/bin/env sh
# Packs what a self-hoster needs to run a release without cloning the repo:
# deploy/docker-compose.yml set to the published image instead of building
# from source, and deploy/.env.example. Used by .github/workflows/release.yml.
#
#   scripts/release-bundle.sh v0.3.0 dist   # -> dist/outflare-v0.3.0.tar.gz
set -eu

version=${1:?usage: release-bundle.sh <version tag> <out dir>}
out=${2:?usage: release-bundle.sh <version tag> <out dir>}
image=${IMAGE:-ghcr.io/outflareai/outflare}
root=$(cd "$(dirname "$0")/.." && pwd)
name=outflare-$version
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

mkdir -p "$work/$name" "$out"
# Swap "build: context: .." for the release image.
awk -v img="$image:$version" '
	/^    build:$/ { print "    image: ${OUTFLARE_IMAGE:-" img "}"; skip = 1; next }
	skip && /^      / { next }
	{ skip = 0; print }
' "$root/deploy/docker-compose.yml" |
	sed 's#^\# Self-host Outflare: docker compose -f deploy/docker-compose.yml up -d#\# Self-host Outflare '"$version"': docker compose up -d#' \
		>"$work/$name/docker-compose.yml"
grep -q "image: \${OUTFLARE_IMAGE:-$image:$version}" "$work/$name/docker-compose.yml" || {
	echo "release-bundle: couldn't set the image in docker-compose.yml" >&2
	exit 1
}
sed 's#deploy/\.env#.env#g' "$root/deploy/.env.example" >"$work/$name/.env.example"
cat >"$work/$name/README.txt" <<TXT
Outflare $version

  cp .env.example .env                                  # change POSTGRES_PASSWORD
  echo "OUTFLARE_SECRET_KEY=\$(openssl rand -base64 32)" >> .env
  docker compose up -d

Then open http://localhost:8080 (check it with: curl localhost:8080/readyz).
Upgrade: download the next release's bundle, keep your .env, docker compose up -d.
Docs: https://github.com/outflareai/outflare#self-host
TXT
tar -C "$work" -czf "$out/$name.tar.gz" "$name"
echo "$out/$name.tar.gz"
