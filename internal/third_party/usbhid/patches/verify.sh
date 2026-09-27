#!/usr/bin/env bash
# Rebuilds internal/third_party/usbhid from the pinned upstream module: the
# patches in this directory are applied in order, the import path is rewritten
# to the package's place in arcctl's module, and go.mod and go.sum are dropped,
# since the copy is a package of arcctl's module rather than a module of its own.
# Fails if the result differs from the committed copy.
#   verify.sh          check only
#   verify.sh --write  replace the copy with the rebuilt tree (re-vendoring)
#   verify.sh --test   also vet and test the package (stubbed IOKit, no device access)
set -euo pipefail

module=rafaelmartins.com/p/usbhid
version=v0.0.0-20260903160318-2edd824d3b06
sum=h1:3ZExkkFFPDk5U0oh678sOgB5mAepL9r1uJzUivdsam8=
local=github.com/positronico/arcctl/internal/third_party/usbhid
go=${GO:-go}

write=0
test=0
for arg in "$@"; do
	case "$arg" in
	--write) write=1 ;;
	--test) test=1 ;;
	*) echo "usage: $0 [--write] [--test]" >&2; exit 2 ;;
	esac
done

here="$(cd "$(dirname "$0")/.." && pwd)"
root="$(cd "$here/../../.." && pwd)"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/usbhid.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

mkdir "$tmp/mod"
(cd "$tmp/mod" && "$go" mod init verify >/dev/null 2>&1)
json="$(cd "$tmp/mod" && GOFLAGS=-mod=mod "$go" mod download -json "$module@$version")"
field() { printf '%s\n' "$json" | sed -n "s/^[[:space:]]*\"$1\": \"\(.*\)\",\{0,1\}$/\1/p" | head -1; }
dir="$(field Dir)"
got="$(field Sum)"
if [ "$got" != "$sum" ]; then
	echo "usbhid: $module@$version has sum $got, want $sum" >&2
	exit 1
fi

cp -R "$dir" "$tmp/usbhid"
chmod -R u+w "$tmp/usbhid"
for p in "$here"/patches/*.patch; do
	patch -s -p1 -d "$tmp/usbhid" <"$p"
done
find "$tmp/usbhid" -name '*.orig' -delete
rm "$tmp/usbhid/go.mod" "$tmp/usbhid/go.sum"
find "$tmp/usbhid" -name '*.go' -exec sed -i.bak "s#\"$module\"#\"$local\"#" {} +
find "$tmp/usbhid" -name '*.go.bak' -delete

if [ "$write" -eq 1 ]; then
	find "$here" -mindepth 1 -maxdepth 1 ! -name NOTICE ! -name patches -exec rm -rf {} +
	cp -R "$tmp/usbhid/." "$here/"
	echo "usbhid: rewrote $here from $module@$version and $(ls "$here"/patches/*.patch | wc -l | tr -d ' ') patches"
elif ! diff -r -x NOTICE -x patches "$tmp/usbhid" "$here"; then
	echo "usbhid: the vendored copy differs from $module@$version plus patches/" >&2
	exit 1
else
	echo "usbhid: vendored copy matches $module@$version plus patches/"
fi

if [ "$test" -eq 1 ]; then
	(cd "$root" && "$go" vet ./internal/third_party/usbhid && "$go" test -race -count=1 ./internal/third_party/usbhid)
fi
