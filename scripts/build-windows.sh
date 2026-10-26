#!/usr/bin/env bash
# Builds dist/Clipferry.exe (portable) and, when makensis is available,
# dist/Clipferry-<version>-windows-setup.exe. Works on Windows (Git Bash),
# macOS and Linux — the Windows build needs no C compiler.
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(git describe --tags --always 2>/dev/null | sed 's/^v//' || echo 0.1.0)}"
[[ "$VERSION" =~ ^[0-9] ]] || VERSION="0.1.0"
NUMERIC=$(echo "$VERSION" | sed -E 's/^([0-9]+\.[0-9]+\.[0-9]+).*/\1/')
mkdir -p dist

# Embed the icon and an application manifest (DPI awareness, common controls).
if ! go run github.com/tc-hib/go-winres@v0.3.3 simply \
  --icon build/windows/icon.ico --manifest gui \
  --product-name Clipferry --file-description "Clipferry" \
  --product-version "$NUMERIC" --file-version "$NUMERIC" \
  --copyright "MIT License" --arch amd64,arm64 --out cmd/clipferry/rsrc; then
  echo "warning: go-winres failed; building without an embedded icon" >&2
fi

for arch in amd64 arm64; do
  GOOS=windows GOARCH=$arch CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -H=windowsgui -X github.com/bkrdmrcioglu/clipferry/internal/core.Version=$VERSION" \
    -o "dist/Clipferry-$arch.exe" ./cmd/clipferry
done
cp dist/Clipferry-amd64.exe dist/Clipferry.exe

# makensis is a native Windows program: give it native paths.
winpath() { if command -v cygpath >/dev/null; then cygpath -w "$1"; else echo "$1"; fi; }

if command -v makensis >/dev/null; then
  ls -la dist
  makensis -V2 -DVERSION="$VERSION" -DNUMERIC="$NUMERIC" -DSRC="$(winpath "$PWD/dist/Clipferry-amd64.exe")" \
    -DOUT="$(winpath "$PWD/dist/Clipferry-$VERSION-windows-setup.exe")" -DICON="$(winpath "$PWD/build/windows/icon.ico")" \
    "$(winpath "$PWD/build/windows/installer.nsi")"
  echo "Built dist/Clipferry-$VERSION-windows-setup.exe"
else
  echo "makensis not found; built the portable dist/Clipferry.exe only"
fi
