#!/usr/bin/env bash
set -uo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root" || exit 2

tmp="$(mktemp -d "${TMPDIR:-/tmp}/vendorguard.XXXXXX")" || exit 2
trap 'rm -rf "$tmp"' EXIT

{
	if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
		git -c core.quotePath=false ls-files --cached --others --exclude-standard
	fi
	find . -path ./.git -prune -o \( -type f -o -type l \) -print | sed 's|^\./||'
} | sort -u >"$tmp/files"

awk '
{
	p = $0
	lp = tolower(p)
	if (lp ~ /^\.github\// && lp ~ /\.ya?ml$/) next
	n = split(lp, seg, "/")
	base = seg[n]
	dir = "/" lp
	r = ""
	if (base ~ /\.js$/) r = "*.js"
	else if (base ~ /\.css$/) r = "*.css"
	else if (base ~ /\.html$/) r = "*.html"
	else if (base ~ /\.png$/) r = "*.png"
	else if (base ~ /\.jpg$/) r = "*.jpg"
	else if (base ~ /\.svg$/) r = "*.svg"
	else if (base ~ /\.ico$/) r = "*.ico"
	else if (base ~ /\.ttf$/) r = "*.ttf"
	else if (base ~ /\.woff/) r = "*.woff*"
	else if (dir ~ /\/site\//) r = "path segment site/"
	else if (dir ~ /\/assets\//) r = "path segment assets/"
	else if (dir ~ /\/img\//) r = "path segment img/"
	else if (dir ~ /\/lang\//) r = "path segment lang/"
	else if (dir ~ /\/tools\/research\//) r = "path segment tools/research/"
	else if (base ~ /^cfg.*\.json$/) r = "cfg*.json"
	else if (base ~ /^prod.*\.json$/) r = "prod*.json"
	else if (base ~ /^research-.*\.json$/) r = "research-*.json"
	else if (base == "sensor.json" || base == "ref.json" || base == "history.json" || base == "hr.json" || base == "hidkeys.json") r = base
	if (r != "") print p ": matches " r
}
' "$tmp/files" >"$tmp/offenders"

total="$(wc -l <"$tmp/files" | tr -d ' ')"
echo "vendorguard: checked $total paths for vendor file names and folders"

mirror="${ARCCTL_MIRROR:-}"
if [ -z "$mirror" ] || [ ! -d "$mirror" ]; then
	echo "vendorguard: mirror hash check skipped"
else
	if command -v sha256sum >/dev/null 2>&1; then
		sha=(sha256sum)
	elif command -v shasum >/dev/null 2>&1; then
		sha=(shasum -a 256)
	else
		echo "vendorguard: neither sha256sum nor shasum found" >&2
		exit 2
	fi

	: >"$tmp/mirror.sha"
	found=0
	for d in site tools/research docs; do
		if [ -d "$mirror/$d" ]; then
			found=1
			if ! find "$mirror/$d" -type f -size +0 -exec "${sha[@]}" {} + >>"$tmp/mirror.sha"; then
				echo "vendorguard: hashing $mirror/$d failed" >&2
				exit 2
			fi
		fi
	done
	if [ "$found" -eq 0 ]; then
		echo "vendorguard: warning: $mirror has none of site/, tools/research/ or docs/" >&2
	fi

	: >"$tmp/public"
	while IFS= read -r f; do
		if [ -f "$f" ] && [ -s "$f" ]; then
			printf '%s\n' "$f" >>"$tmp/public"
		fi
	done <"$tmp/files"

	: >"$tmp/public.sha"
	if [ -s "$tmp/public" ]; then
		if ! sed 's|^|./|' "$tmp/public" | tr '\n' '\0' | xargs -0 "${sha[@]}" >"$tmp/public.sha"; then
			echo "vendorguard: hashing public files failed" >&2
			exit 2
		fi
	fi

	awk '
		{ l = $0; sub(/^\\/, "", l); h = substr(l, 1, 64); f = substr(l, 67) }
		NR == FNR { m[h] = f; next }
		h in m { print f ": same SHA-256 as mirror file " m[h] }
	' "$tmp/mirror.sha" "$tmp/public.sha" >>"$tmp/offenders"

	mcount="$(wc -l <"$tmp/mirror.sha" | tr -d ' ')"
	pcount="$(wc -l <"$tmp/public.sha" | tr -d ' ')"
	echo "vendorguard: compared $pcount public files with $mcount mirror files by SHA-256"
fi

if [ -s "$tmp/offenders" ]; then
	echo "vendorguard: FAIL, vendor material in the public tree:" >&2
	sed 's/^/  /' "$tmp/offenders" >&2
	exit 1
fi
echo "vendorguard: ok"
