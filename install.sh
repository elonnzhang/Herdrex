#!/bin/sh
# Install or update herdrex from its GitHub Releases:
#
#   curl -fsSL https://raw.githubusercontent.com/elonnzhang/Herdrex/main/install.sh | sh
#
# Run it again to update. Environment:
#   HERDREX_VERSION        release tag to install, such as v0.1.0 (default: the latest)
#   HERDREX_INSTALL_DIR    where to put the binary (default: the directory of the
#                          herdrex already on PATH, else /usr/local/bin if writable,
#                          else ~/.local/bin)
#   HERDREX_DOWNLOAD_BASE  URL of the directory that holds the archives and
#                          checksums.txt (default: the release's download URL)
set -eu

repo=elonnzhang/Herdrex

say() { printf '%s\n' "$*"; }
warn() { printf 'install.sh: %s\n' "$*" >&2; }
die() { warn "$*"; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }

need curl
need tar
need awk

case $(uname -s) in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) die "unsupported system $(uname -s); build from source: go install github.com/elonnzhang/herdrex/cmd/herdrex@latest" ;;
esac
case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) die "unsupported architecture $(uname -m)" ;;
esac

version=${HERDREX_VERSION:-}
if [ -z "$version" ]; then
  final=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest") ||
    die "cannot reach github.com"
  version=${final##*/}
  case $version in
    v[0-9]*) ;;
    *) die "$repo has no release yet; set HERDREX_VERSION, or build from source" ;;
  esac
fi

dir=${HERDREX_INSTALL_DIR:-}
if [ -z "$dir" ]; then
  found=$(command -v herdrex 2>/dev/null || true)
  case $found in
    "" | */Cellar/*) found="" ;;
  esac
  if [ -n "$found" ] && [ -w "$(dirname "$found")" ]; then
    dir=$(dirname "$found")
  elif [ -w /usr/local/bin ]; then
    dir=/usr/local/bin
  else
    dir=$HOME/.local/bin
  fi
fi
mkdir -p "$dir" 2>/dev/null || true
[ -w "$dir" ] || die "$dir is not writable; set HERDREX_INSTALL_DIR to a directory you can write, or run with sudo"

# Where install.sh records what it installed, so that herdrex update can run it again.
marker=${XDG_STATE_HOME:-$HOME/.local/state}/herdrex/install-path
record() {
  mkdir -p "$(dirname "$marker")" && printf '%s\n' "$dir/herdrex" >"$marker" ||
    warn "could not write $marker; herdrex update will ask for --channel"
}

if [ -x "$dir/herdrex" ] && "$dir/herdrex" --version 2>/dev/null | grep -F -q "$version"; then
  say "herdrex $version is already installed in $dir"
  record
  exit 0
fi

base=${HERDREX_DOWNLOAD_BASE:-https://github.com/$repo/releases/download/$version}
asset=herdrex_${version}_${os}_${arch}.tar.gz
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "downloading herdrex $version for $os/$arch"
curl -fsSL -o "$tmp/$asset" "$base/$asset" || die "cannot download $base/$asset"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || die "cannot download $base/checksums.txt"

want=$(awk -v f="$asset" '$2 == f { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || die "checksums.txt has no entry for $asset"
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/$asset" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
  got=$(shasum -a 256 "$tmp/$asset" | awk '{ print $1 }')
else
  die "sha256sum or shasum is required to verify the download"
fi
[ "$got" = "$want" ] || die "checksum mismatch for $asset (got $got, want $want)"

tar -xzf "$tmp/$asset" -C "$tmp" herdrex
chmod 755 "$tmp/herdrex"
# Copy beside the target and rename, so a running herdrex is never half-written.
cp "$tmp/herdrex" "$dir/.herdrex.new.$$"
mv -f "$dir/.herdrex.new.$$" "$dir/herdrex"
record

say "installed herdrex $version to $dir/herdrex"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) warn "$dir is not on PATH; add it, for example: export PATH=\"$dir:\$PATH\"" ;;
esac
other=$(command -v herdrex 2>/dev/null || true)
if [ -n "$other" ] && [ "$other" != "$dir/herdrex" ]; then
  warn "another herdrex comes first on PATH: $other"
fi
say "a herdrex that is already running keeps the old version until you restart it"
