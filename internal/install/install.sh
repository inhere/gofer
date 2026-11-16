#!/bin/sh
# gofer worker installer — served by the gofer server at {{SERVER}}.
#
# It installs the gofer binary of this server's version, then asks the server to admit
# this machine as a worker: the script prints a pairing code, a person approves it in the
# console (Runners), and the worker is registered as a user-level service.
#
# This script carries no credential. Everything it downloads is verified against the
# sha256 the server publishes for it; a missing or wrong checksum stops the install.
set -eu

SERVER="{{SERVER}}"

fail() {
	echo "gofer install: $*" >&2
	exit 1
}

# A worker serves the user whose files and config it reads. As root it would register a
# service for root and run jobs as root.
if [ "$(id -u)" = "0" ]; then
	fail "do not run this as root: install and run the worker as the user it serves (a root worker would run jobs as root)"
fi

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) fail "unsupported system $(uname -s): this installer covers Linux and macOS (on Windows run install.ps1)" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) fail "unsupported architecture $(uname -m)" ;;
esac

command -v curl >/dev/null 2>&1 || fail "curl is required"
if command -v sha256sum >/dev/null 2>&1; then
	sum_cmd="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
	sum_cmd="shasum -a 256"
else
	fail "sha256sum or shasum is required to verify the download"
fi

tmp=$(mktemp -d 2>/dev/null || mktemp -d -t gofer-install) || fail "cannot create a temporary directory"
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

manifest_url="$SERVER/install/manifest?os=$os&arch=$arch"
manifest="$tmp/manifest.json"
curl -fsSL -o "$manifest" "$manifest_url" ||
	fail "cannot read $manifest_url (the server must publish a sha256 for its own version)"

# field is one string value of the flat JSON manifest, without needing jq.
field() {
	sed -n 's/.*"'"$1"'"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$manifest" | head -n 1
}
url=$(field url)
want=$(field sha256)
name=$(field name)
[ -n "$url" ] || fail "the manifest has no download url"
[ -n "$name" ] || fail "the manifest has no file name"
[ -n "$want" ] || fail "the manifest has no sha256 for $os/$arch: refusing to install an unverified download"

file="$tmp/$name"
curl -fsSL -o "$file" "$url" || fail "download $url failed"

# The published checksum is the binary's, so an archive is unpacked before hashing.
case "$name" in
*.tar.gz | *.tgz)
	tar -xzf "$file" -C "$tmp" || fail "cannot extract $name"
	bin=$(find "$tmp" -type f -name gofer | head -n 1)
	;;
*.zip)
	command -v unzip >/dev/null 2>&1 || fail "unzip is required to extract $name"
	unzip -q -o "$file" -d "$tmp/unpacked" || fail "cannot extract $name"
	bin=$(find "$tmp/unpacked" -type f -name gofer | head -n 1)
	;;
*) bin="$file" ;;
esac
[ -n "${bin:-}" ] && [ -f "$bin" ] || fail "the download $name contains no gofer binary"

got=$($sum_cmd "$bin" | cut -d' ' -f1 | tr 'A-F' 'a-f')
want=$(echo "$want" | tr 'A-F' 'a-f')
[ "$got" = "$want" ] || fail "sha256 mismatch for $name: got $got, want $want — the download is corrupt or the source serves another binary"

dir="$HOME/.local/bin"
mkdir -p "$dir" || fail "cannot create $dir"
target="$dir/gofer"
cp "$bin" "$target.new" && chmod 0755 "$target.new" && mv -f "$target.new" "$target" ||
	fail "cannot install to $target"
echo "installed $target"

# stdin is the script itself (curl … | sh), so the join must never read it.
"$target" worker join --server "$SERVER" --yes </dev/null ||
	fail "worker join failed (nothing was registered; rerun $target worker join --server $SERVER)"

"$target" worker service register --start ||
	fail "registered the worker, but the OS service could not be set up: run $target worker service register --start"

case ":$PATH:" in
*":$dir:"*) ;;
*) echo "note: $dir is not in PATH — add it to run gofer without the full path" ;;
esac
