#!/usr/bin/env bash
# Builds the bench guests and the wasmbench binary for each device the
# ut-docs#3153 measurements run on. Output: tools/wasmbench/out/ (gitignored).
#   bash tools/wasmbench/build.sh [path/to/real-plugin.wasm]
# TinyGo guests are built only when `tinygo` is on PATH.
set -euo pipefail
# Resolve the plugin path before the cd below, so a relative one works.
plugin=${1:+$(cd "$(dirname "$1")" && pwd)/$(basename "$1")}
cd "$(dirname "$0")"
out=out
mkdir -p "$out"

GOOS=wasip1 GOARCH=wasm go build -o "$out/go-command.wasm" ./guests/command
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o "$out/go-reactor.wasm" ./guests/reactor
if command -v tinygo >/dev/null; then
	tinygo build -target=wasip1 -no-debug -o "$out/tinygo-command.wasm" ./guests/command
	tinygo build -target=wasip1 -no-debug -buildmode=c-shared -o "$out/tinygo-reactor.wasm" ./guests/reactor
else
	echo "build.sh: tinygo not on PATH; skipping TinyGo guests" >&2
fi
if [[ -n $plugin ]]; then
	cp "$plugin" "$out/real-plugin.wasm"
fi

# Pure Go host binaries: no cgo, so android/arm64 needs no NDK and runs from
# `adb shell` in /data/local/tmp.
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o "$out/wasmbench-linux-arm64" .
CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build -o "$out/wasmbench-android-arm64" .
go build -o "$out/wasmbench-host" .
ls -l "$out"
