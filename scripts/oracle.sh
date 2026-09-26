#!/usr/bin/env bash
# Oracle rerun. Needs the private mirror checkout in ARCCTL_MIRROR and skips without
# it: regenerates every testdata/oracle file with the seed and case count recorded in
# it, into a temp dir, and compares the result with the committed files.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
mirror="${ARCCTL_MIRROR:-}"
if [ -z "$mirror" ]; then
	echo "oracle: skipped (ARCCTL_MIRROR unset)"
	exit 0
fi
if [ ! -d "$mirror" ]; then
	echo "oracle: skipped (ARCCTL_MIRROR not found: $mirror)"
	exit 0
fi
node="${NODE:-node}"
run="$mirror/tools/oracle/run.mjs"

tmp="$(mktemp -d "${TMPDIR:-/tmp}/arcctl-oracle.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

if ! "$node" "$run" --verify --out "$root" >"$tmp/verify" 2>&1; then
	grep -v '^same ' "$tmp/verify" >&2 || true
	echo "oracle: run.mjs --verify failed" >&2
	exit 1
fi
echo "oracle: --verify reports $(grep -c '^same ' "$tmp/verify") files unchanged"

"$node" -e '
const groups = new Map();
for (const f of process.argv.slice(1)) {
  const j = JSON.parse(require("fs").readFileSync(f, "utf8"));
  const k = `${j.seed} ${j.cases.length}`;
  groups.set(k, [...(groups.get(k) || []), j.area]);
}
for (const [k, areas] of groups) console.log(`${k} ${areas.join(",")}`);
' "$root"/testdata/oracle/*.json >"$tmp/groups"

while read -r seed cases areas; do
	"$node" "$run" --seed "$seed" --cases "$cases" --areas "$areas" --out "$tmp" >/dev/null
done <"$tmp/groups"

if ! diff -r -x README.md "$tmp/testdata/oracle" "$root/testdata/oracle" >"$tmp/diff"; then
	head -40 "$tmp/diff" >&2
	echo "oracle: regenerated vectors differ from testdata/oracle" >&2
	exit 1
fi
echo "oracle: ok, $(wc -l <"$tmp/groups" | tr -d ' ') run(s) reproduce every file in testdata/oracle"
