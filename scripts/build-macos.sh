#!/usr/bin/env bash
# Builds dist/Clipferry.app (universal: Apple silicon + Intel) and
# dist/Clipferry-<version>-macos.dmg. Run on macOS with Go and Xcode tools.
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(git describe --tags --always 2>/dev/null | sed 's/^v//' || echo 0.1.0)}"
[[ "$VERSION" =~ ^[0-9] ]] || VERSION="0.1.0"
export MACOSX_DEPLOYMENT_TARGET=11.0
export CGO_ENABLED=1
LDFLAGS="-s -w -X github.com/bkrdmrcioglu/clipferry/internal/core.Version=$VERSION"

APP=dist/Clipferry.app
rm -rf "$APP" dist/*.dmg
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources" dist/tmp

for arch in arm64 amd64; do
  GOARCH=$arch go build -trimpath -ldflags "$LDFLAGS" -o "dist/tmp/clipferry-$arch" ./cmd/clipferry
done
lipo -create -output "$APP/Contents/MacOS/Clipferry" dist/tmp/clipferry-arm64 dist/tmp/clipferry-amd64
rm -rf dist/tmp

sed "s/__VERSION__/$VERSION/g" build/darwin/Info.plist > "$APP/Contents/Info.plist"
cp build/darwin/icon.icns "$APP/Contents/Resources/icon.icns"

# Ad-hoc signature: required on Apple silicon. For distribution without the
# Gatekeeper warning, sign with a Developer ID and notarize (see README).
codesign --force --deep --sign "${CODESIGN_IDENTITY:--}" "$APP"

STAGE=$(mktemp -d)
cp -R "$APP" "$STAGE/"
ln -s /Applications "$STAGE/Applications"
hdiutil create -quiet -volname "Clipferry" -srcfolder "$STAGE" -ov -format UDZO "dist/Clipferry-$VERSION-macos.dmg"
rm -rf "$STAGE"
echo "Built $APP and dist/Clipferry-$VERSION-macos.dmg"
