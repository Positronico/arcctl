#!/usr/bin/env bash
# Vendor -> facts drift check. Needs the private mirror checkout in ARCCTL_MIRROR and
# skips without it: re-extracts the bundle tables, reruns the mirror's catalog front
# end into a temp dir and compares the result with internal/catalog/facts.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
mirror="${ARCCTL_MIRROR:-}"
if [ -z "$mirror" ]; then
	echo "drift: skipped (ARCCTL_MIRROR unset)"
	exit 0
fi
if [ ! -d "$mirror" ]; then
	echo "drift: skipped (ARCCTL_MIRROR not found: $mirror)"
	exit 0
fi
mirror="$(cd "$mirror" && pwd)"
go="${GO:-go}"

if command -v sha256sum >/dev/null 2>&1; then
	sha() { sha256sum "$1" | cut -d' ' -f1; }
else
	sha() { shasum -a 256 "$1" | cut -d' ' -f1; }
fi

tmp="$(mktemp -d "${TMPDIR:-/tmp}/arcctl-drift.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

shopt -s nullglob
bundles=("$mirror"/site/assets/index-*.js)
if [ "${#bundles[@]}" -ne 1 ]; then
	echo "drift: want one site/assets/index-*.js in $mirror, found ${#bundles[@]}" >&2
	exit 1
fi
bundle="site/assets/$(basename "${bundles[0]}")"
got="$(sha "$mirror/$bundle")"
want="$(python3 -c '
import json, sys
inputs = json.load(open(sys.argv[1]))["inputs"]
print(next((i["sha256"] for i in inputs if i["path"] == sys.argv[2]), ""))
' "$root/internal/catalog/facts/sources.json" "$bundle")"
status=0
if [ "$got" != "$want" ]; then
	echo "drift: $bundle has SHA-256 $got, facts/sources.json records ${want:-no entry for it}" >&2
	status=1
else
	echo "drift: bundle $bundle matches the SHA-256 in facts/sources.json"
fi

python3 "$mirror/tools/research/extract_bundle.py" --site "$mirror/site" --out "$tmp/extracts" >/dev/null
(cd "$mirror/tools/catalog" && "$go" run . --site "$mirror/site" --extracts "$tmp/extracts" --out "$tmp/pub" >/dev/null)
if ! diff -r -x SCHEMA.md -x schema.json "$tmp/pub/internal/catalog/facts" "$root/internal/catalog/facts"; then
	echo "drift: facts regenerated from the mirror differ from internal/catalog/facts" >&2
	status=1
fi
if [ "$status" -eq 0 ]; then
	echo "drift: ok, facts match the mirror"
fi
exit "$status"
