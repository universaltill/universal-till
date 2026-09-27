#!/usr/bin/env bash
#
# Launch smoke for the iOS till shell (ut-docs#3068), run by ios-ci.yml on a
# macOS runner after the unsigned simulator build. For each device type it:
# boots a simulator, installs and launches the app, waits until the Go till
# server INSIDE the app answers /healthz (the simulator shares the Mac's
# network stack, so the runner reaches the app's 0.0.0.0 bind on
# 127.0.0.1), checks the screenshot isn't a blank page, then backgrounds the
# app, brings it back and checks it is the SAME process and the till still
# answers. A compile-only gate can't see a server that never starts.
#
# Not exercised here: iOS reclaiming a suspended app's listening socket
# (Apple TN2277) doesn't happen on a simulator, so the shell's
# restart-on-resume path is covered only by the device check
# (ut-docs#3070).
#
# Usage: ios-sim-smoke.sh <path/to/UniversalTill.app> <screenshot-dir>
set -euo pipefail

app="${1:?usage: ios-sim-smoke.sh <app> <screenshot-dir>}"
shots="${2:?usage: ios-sim-smoke.sh <app> <screenshot-dir>}"
bundle_id="com.universaltill.pos"
mkdir -p "$shots"

# The till's default port and the window internal/listenport moves up
# through when 8080 is busy.
ports=$(seq 8080 8100)

# Something already answering would mask an app whose server never starts.
assert_nothing_answers() {
  local port
  for port in $ports; do
    if curl -fsS -m 1 "http://127.0.0.1:${port}/healthz" >/dev/null 2>&1; then
      echo "::error:::${port} already answers /healthz before the app launched — the smoke can't tell the app's server from it"
      exit 1
    fi
  done
}

# The pid `simctl launch` prints ("com.universaltill.pos: 14644").
launch_pid() {
  xcrun simctl launch "$1" "$bundle_id" | awk -F': ' '{print $2}'
}

wait_for_till() {
  local deadline=$((SECONDS + ${1:-180}))
  local port
  while [ "$SECONDS" -lt "$deadline" ]; do
    for port in $ports; do
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
  xcrun simctl boot "$udid" || true # already booted on a re-run; bootstatus gates
  xcrun simctl bootstatus "$udid" -b
  xcrun simctl install "$udid" "$app"
  assert_nothing_answers
  pid="$(launch_pid "$udid")"
  if ! wait_for_till 180; then
    echo "::error::the till server inside the $kind app never answered /healthz"
    xcrun simctl spawn "$udid" log show --last 5m --predicate "process == \"UniversalTill\"" | tail -100 || true
    exit 1
  fi
  sleep 8 # let the WebView render the first page before the screenshot
  xcrun simctl io "$udid" screenshot "$shots/${kind}-launch.png"
  swift "$(dirname "$0")/ios-screenshot-check.swift" "$shots/${kind}-launch.png"

  # Background (Settings to the front), wait, come back: the till must
  # still answer — as it was, or restarted by the shell's resume probe.
  xcrun simctl launch "$udid" com.apple.Preferences
  sleep 20
  again="$(launch_pid "$udid")"
  if [ "$again" != "$pid" ]; then
    echo "::error::the $kind app was relaunched (pid $pid -> $again) instead of resumed"
    exit 1
  fi
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
