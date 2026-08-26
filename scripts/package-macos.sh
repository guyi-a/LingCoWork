#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WEB_DIR="$ROOT_DIR/web"
ELECTRON_DIR="$ROOT_DIR/electron"
RESOURCE_DIR="$ELECTRON_DIR/package-resources"
BACKEND_DIR="$RESOURCE_DIR/backend"
WEB_RESOURCE_DIR="$RESOURCE_DIR/web"
RELEASE_DIR="$ROOT_DIR/release"

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "macOS packaging must run on macOS." >&2
  exit 1
fi

if ! command -v go >/dev/null 2>&1; then
  echo "Go is required to build the backend." >&2
  exit 1
fi

if ! command -v pnpm >/dev/null 2>&1; then
  echo "pnpm is required to build the desktop application." >&2
  exit 1
fi

pnpm --dir "$WEB_DIR" install --frozen-lockfile
pnpm --dir "$ELECTRON_DIR" install --frozen-lockfile

rm -rf "$RESOURCE_DIR"
mkdir -p "$BACKEND_DIR" "$WEB_RESOURCE_DIR" "$RELEASE_DIR"

pnpm --dir "$WEB_DIR" build

CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
  go build -trimpath -ldflags="-s -w" \
  -o "$BACKEND_DIR/lingcowork-api" \
  "$ROOT_DIR/cmd/api"

cp -R "$WEB_DIR/dist/." "$WEB_RESOURCE_DIR/"

pnpm --dir "$ELECTRON_DIR" build
pnpm --dir "$ELECTRON_DIR" dist:mac

echo "LingCoWork macOS artifacts:"
ls -1 "$RELEASE_DIR"
