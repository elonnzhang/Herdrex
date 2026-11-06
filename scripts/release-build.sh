#!/bin/sh
# Build the release archives that install.sh downloads.
#   scripts/release-build.sh VERSION OUTDIR
# Writes OUTDIR/herdrex_VERSION_OS_ARCH.tar.gz for darwin and linux on amd64 and
# arm64, each holding the herdrex binary, and OUTDIR/checksums.txt.
set -eu

version=${1:?usage: release-build.sh VERSION OUTDIR}
out=${2:?usage: release-build.sh VERSION OUTDIR}
mkdir -p "$out"
out=$(cd "$out" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

for os in darwin linux; do
  for arch in amd64 arm64; do
    asset=herdrex_${version}_${os}_${arch}.tar.gz
    mkdir "$work/$os-$arch"
    CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=$version" \
      -o "$work/$os-$arch/herdrex" ./cmd/herdrex
    tar -czf "$out/$asset" -C "$work/$os-$arch" herdrex
    echo "built $asset"
  done
done

cd "$out"
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum herdrex_"${version}"_*.tar.gz > checksums.txt
else
  shasum -a 256 herdrex_"${version}"_*.tar.gz > checksums.txt
fi
