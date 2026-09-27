#!/usr/bin/env bash
# Builds THIRD_PARTY_NOTICES: the license text of every third-party component a
# release binary (CGO_ENABLED=0, no build tags) contains. BSD 3-Clause (Go,
# usbhid) and Apache-2.0 (purego) require these notices in binary
# distributions, so release archives and the Homebrew formula ship the file.
#   notices.sh          fail if THIRD_PARTY_NOTICES is out of date
#   notices.sh --write  rewrite it
set -euo pipefail

go=${GO:-go}
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

write=0
case "${1:-}" in
"") ;;
--write) write=1 ;;
*) echo "usage: $0 [--write]" >&2; exit 2 ;;
esac

goroot="$("$go" env GOROOT)"
golicense=""
for f in "$goroot/LICENSE" "$goroot/../LICENSE"; do
	if [ -f "$f" ]; then golicense="$f"; break; fi
done
if [ -z "$golicense" ]; then
	echo "notices: no LICENSE next to GOROOT $goroot" >&2
	exit 1
fi

section() {
	printf '\n================================================================================\n%s\n================================================================================\n\n' "$1"
	cat "$2"
}

tmp="$(mktemp "${TMPDIR:-/tmp}/notices.XXXXXX")"
trap 'rm -f "$tmp"' EXIT
{
	echo "arcctl release binaries contain the third-party software below. Its"
	echo "licenses ask for these notices in binary distributions. Builds with the"
	echo "hidapi tag also contain github.com/sstallion/go-hid and the HIDAPI C"
	echo "library it bundles; their licenses come with their source."
	section "The Go standard library and runtime (https://go.dev)" "$golicense"
	section "rafaelmartins.com/p/usbhid, vendored in internal/third_party/usbhid" internal/third_party/usbhid/LICENSE
	for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64; do
		CGO_ENABLED=0 GOOS="${target%/*}" GOARCH="${target#*/}" "$go" list -deps \
			-f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}}	{{.Dir}}{{end}}{{end}}' ./cmd/arcctl
	done |
		sort -u | while IFS='	' read -r mod dir; do
			lic=""
			for f in "$dir"/LICENSE "$dir"/LICENSE.* "$dir"/COPYING; do
				if [ -f "$f" ]; then lic="$f"; break; fi
			done
			if [ -z "$lic" ]; then
				echo "notices: no license file in $dir" >&2
				exit 1
			fi
			section "$mod" "$lic"
			if [ -f "$dir/NOTICE" ]; then
				echo
				cat "$dir/NOTICE"
			fi
		done
} >"$tmp"

if [ "$write" -eq 1 ]; then
	cp "$tmp" THIRD_PARTY_NOTICES
	echo "notices: wrote THIRD_PARTY_NOTICES"
elif ! diff -u THIRD_PARTY_NOTICES "$tmp"; then
	echo "notices: THIRD_PARTY_NOTICES is out of date; run scripts/notices.sh --write" >&2
	exit 1
else
	echo "notices: THIRD_PARTY_NOTICES is current"
fi
