#!/usr/bin/env bash
# Checks what a release binary is made of: the darwin builds link only
# libSystem and libresolv, no hardware-test code gets in, and the builds with
# the hidapi and hwtest tags compile. It needs otool, so it runs on macOS only.
set -euo pipefail

go=${GO:-go}
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

if [ "$(uname -s)" != Darwin ]; then
	echo "release-check: skipped (needs macOS)"
	exit 0
fi

# Symbols only a -tags hwtest build has: the hwtest package, its CLI command
# and host, the unguarded opens of the raw path in hidio and the emulator,
# and wherever they live, H7's raw factory reset and H5's drill checkpoint.
hwtest_symbols='hwtest|cli\.\(?\*?hwHost\)?\.|cli\.\(\*runner\)\.hwSummary|hidio\.OpenRaw|emu\.\(\*Bus\)\.OpenRaw|\.hwReset|\.hwCheckpoint'

tmp="$(mktemp -d "${TMPDIR:-/tmp}/release-check.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

fail=0
for arch in arm64 amd64; do
	bin="$tmp/arcctl-$arch"
	CGO_ENABLED=0 GOOS=darwin GOARCH=$arch "$go" build -trimpath -ldflags "-s -w -X main.version=release-check" -o "$bin" ./cmd/arcctl
	extra="$(otool -L "$bin" | tail -n +2 | awk '{print $1}' | grep -v -x -e /usr/lib/libSystem.B.dylib -e /usr/lib/libresolv.9.dylib || true)"
	if [ -n "$extra" ]; then
		echo "release-check: darwin/$arch also links $extra" >&2
		fail=1
	fi
	CGO_ENABLED=0 GOOS=darwin GOARCH=$arch "$go" build -trimpath -o "$bin.syms" ./cmd/arcctl
	if "$go" tool nm "$bin.syms" | grep -iE "$hwtest_symbols"; then
		echo "release-check: darwin/$arch contains hardware-test code" >&2
		fail=1
	fi
	echo "release-check: darwin/$arch links $(otool -L "$bin" | tail -n +2 | awk '{print $1}' | tr '\n' ' ')"
done

"$go" vet -tags hidapi ./...
"$go" build -tags hidapi -o "$tmp/arcctl-hidapi" ./cmd/arcctl
"$go" vet -tags hwtest ./...
"$go" build -tags hwtest -o "$tmp/arcctl-hwtest" ./cmd/arcctl
echo "release-check: the hidapi and hwtest builds compile"
exit "$fail"
