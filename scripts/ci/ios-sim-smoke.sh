#!/usr/bin/env bash
#
# Launch smoke for the iOS till shell (ut-docs#3068), run by ios-ci.yml on a
# macOS runner after the unsigned simulator build. For each device type it:
# boots a simulator, installs and launches the app, waits until the Go till
# server INSIDE the app answers /healthz (the simulator shares the Mac's
# network stack, so the runner reaches the app's 0.0.0.0 bind on
# 127.0.0.1), saves a screenshot, then backgrounds the app, brings it back
# and checks the till still answers (the shell's resume path — Apple
# TN2277). A compile-only gate can't see a server that never starts.
#
# Usage: ios-sim-smoke.sh <path/to/UniversalTill.app> <screenshot-dir>
set -euo pipefail

app="${1:?usage: ios-sim-smoke.sh <app> <screenshot-dir>}"
shots="${2:?usage: ios-sim-smoke.sh <app> <screenshot-dir>}"
bundle_id="com.universaltill.pos"
mkdir -p "$shots"

# Polls the till's default port and the few above it that
# internal/listenport moves to when 8080 is busy on the runner.
wait_for_till() {
  local deadline=$((SECONDS + ${1:-180}))
  while [ "$SECONDS" -lt "$deadline" ]; do
    for port in 8080 8081 8082 8083 8084 8085; do
      if curl -fsS -m 2 "http://127.0.0.1:${port}/healthz" >/dev/null 2>&1; then
        echo "till answers on :${port}"
        return 0
      fi
    done
    sleep 2
  done
  return 1
}

# First available simulator whose name starts with $1 ("iPhone", "iPad").
device_for() {
  xcrun simctl list devices available -j |
    python3 -c 'import json,sys
want=sys.argv[1]
for rt, devs in json.load(sys.stdin)["devices"].items():
    if "iOS" not in rt: continue
    for d in devs:
        if d["name"].startswith(want):
            print(d["udid"]); sys.exit(0)
sys.exit(1)' "$1"
}

for kind in iPhone iPad; do
  udid="$(device_for "$kind")" || { echo "::error::no available $kind simulator"; exit 1; }
  echo "== $kind ($udid)"
  xcrun simctl boot "$udid"
  xcrun simctl bootstatus "$udid" -b
  xcrun simctl install "$udid" "$app"
  xcrun simctl launch "$udid" "$bundle_id"
  if ! wait_for_till 180; then
    echo "::error::the till server inside the $kind app never answered /healthz"
    xcrun simctl spawn "$udid" log show --last 5m --predicate "process == \"UniversalTill\"" | tail -100 || true
    exit 1
  fi
  sleep 8 # let the WebView render the first page before the screenshot
  xcrun simctl io "$udid" screenshot "$shots/${kind}-launch.png"

  # Background (Settings to the front), wait, come back: the till must
  # still answer — as it was, or restarted by the shell's resume probe.
  xcrun simctl launch "$udid" com.apple.Preferences
  sleep 20
  xcrun simctl launch "$udid" "$bundle_id"
  if ! wait_for_till 60; then
    echo "::error::the $kind till did not answer after returning from the background"
    exit 1
  fi
  sleep 5
  xcrun simctl io "$udid" screenshot "$shots/${kind}-resumed.png"

  xcrun simctl terminate "$udid" "$bundle_id" || true
  xcrun simctl shutdown "$udid"
done
echo "iOS launch smoke passed"
