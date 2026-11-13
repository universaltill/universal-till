#!/usr/bin/env bash
#
# Self-test for ios-sim-wait-drawn.sh (ut-docs#5201): the launch smoke polls
# for a drawn WebView instead of sleeping a fixed 8 s. Against generated PNG
# fixtures it proves that
#   - a page that draws late passes once it draws;
#   - a page that never draws (a blank PNG) still fails, within the
#     deadline, with the check's own "looks blank" message printed once;
#   - a page drawn at once passes on the first screenshot without sleeping.
# The polling cases use a Python stand-in of the check's rule (< 16 colours
# or < 2% non-background on an 8 px grid = blank): it runs in a fixed,
# short time, so the deadlines below hold on any runner. On macOS the real
# ios-screenshot-check.swift is also run against both fixtures (blank must
# fail, drawn must pass); ios-ci sets UT_REQUIRE_IOS_SWIFT=1, so a missing
# swift fails there.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=scripts/ci/ios-sim-wait-drawn.sh
source "$HERE/ios-sim-wait-drawn.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# Fixtures: a flat white page and a page with many coloured tiles.
python3 - "$tmp" <<'EOF'
import struct, sys, zlib
def png(path, w, h, px):
    raw = b"".join(b"\0" + bytes(c for x in range(w) for c in px(x, y)) for y in range(h))
    def chunk(t, d):
        return struct.pack(">I", len(d)) + t + d + struct.pack(">I", zlib.crc32(t + d) & 0xFFFFFFFF)
    with open(path, "wb") as f:
        f.write(b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0))
                + chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b""))
png(sys.argv[1] + "/blank.png", 160, 160, lambda x, y: (255, 255, 255))
png(sys.argv[1] + "/drawn.png", 160, 160,
    lambda x, y: (255, 255, 255) if x < 16 and y < 16 else ((x // 16) * 25, (y // 16) * 25, 128))
EOF

check_png() {
  python3 - "$1" <<'EOF'
import struct, sys, zlib
data = open(sys.argv[1], "rb").read()
w, h = struct.unpack(">II", data[16:24])
idat, i = b"", 8
while i < len(data):
    n = struct.unpack(">I", data[i:i + 4])[0]
    if data[i + 4:i + 8] == b"IDAT":
        idat += data[i + 8:i + 8 + n]
    i += 12 + n
raw = zlib.decompress(idat)
px = lambda x, y: raw[y * (w * 3 + 1) + 1 + x * 3:y * (w * 3 + 1) + 4 + x * 3]
bg = px(0, 0)
samples = [px(x, y) for y in range(0, h, 8) for x in range(0, w, 8)]
colours = len(set(samples))
share = sum(p != bg for p in samples) / len(samples)
print(f"{sys.argv[1]}: {w}x{h}, {colours} distinct colours, {int(share * 100)}% non-background")
if colours < 16 or share < 0.02:
    print(f"::error::{sys.argv[1]} looks blank — the WebView probably never showed the till")
    sys.exit(1)
EOF
}

# The fake simulator: blank until screenshot number $draw_at, drawn after.
shots_file="$tmp/shots"
draw_at=0
shoot_fake() {
  local n
  n=$(($(cat "$shots_file") + 1))
  echo "$n" >"$shots_file"
  if [ "$draw_at" -gt 0 ] && [ "$n" -ge "$draw_at" ]; then
    cp "$tmp/drawn.png" "$1"
  else
    cp "$tmp/blank.png" "$1"
  fi
}

fail=0
LAST_OUT=""
export TMPDIR="$tmp/sys"
mkdir "$TMPDIR"
run_case() { # <name> <draw_at> <deadline> <want rc> <want text>
  local name="$1" out rc start
  draw_at="$2"
  echo 0 >"$shots_file"
  start=$SECONDS
  rc=0
  out="$(wait_for_drawn shoot_fake check_png "$tmp/launch.png" "$3" 1)" || rc=$?
  if [ "$rc" -ne "$4" ]; then
    echo "FAIL: $name: exit $rc, want $4"; printf '%s\n' "$out"; fail=1; return
  fi
  if ! grep -qF "$5" <<<"$out"; then
    echo "FAIL: $name: output lacks '$5'"; printf '%s\n' "$out"; fail=1; return
  fi
  echo "ok: $name ($(cat "$shots_file") screenshot(s), $((SECONDS - start)) s)"
  LAST_OUT="$out"
}

run_case "draws on the 3rd screenshot" 3 30 0 "drawn after 3 screenshot(s)"

run_case "never draws" 0 3 1 "looks blank"
if [ "$(grep -c 'looks blank' <<<"$LAST_OUT")" -ne 1 ]; then
  echo "FAIL: never draws: the earlier attempts' errors were printed too"; fail=1
fi
# A 3 s deadline polled every 1 s: more than one try, and it stops (at most
# one screenshot per second plus the last one at the deadline).
n="$(cat "$shots_file")"
if [ "$n" -lt 2 ] || [ "$n" -gt 5 ]; then
  echo "FAIL: never draws: took $n screenshot(s), want 2-5 for a 3 s deadline"; fail=1
fi

run_case "drawn at once" 1 30 0 "drawn after 1 screenshot(s)"

# A screenshot that keeps failing is a failed attempt with its own error,
# not a silent exit of the smoke.
shoot_broken() { echo "simctl: device not booted" >&2; return 1; }
rc=0
out="$(wait_for_drawn shoot_broken check_png "$tmp/launch.png" 1 1)" || rc=$?
if [ "$rc" -ne 1 ] || ! grep -qF "taking the screenshot" <<<"$out" || ! grep -qF "device not booted" <<<"$out"; then
  echo "FAIL: broken screenshot: exit $rc, want 1 with the simctl error"; printf '%s\n' "$out"; fail=1
else
  echo "ok: broken screenshot fails with its error"
fi
# Every run above cleaned up its check log (wait_for_drawn's mktemp lands
# in this TMPDIR).
if compgen -G "$TMPDIR/*" >/dev/null; then
  echo "FAIL: check log left behind:"; ls "$TMPDIR"; fail=1
fi

# The real check on macOS: the blank fixture must fail, the drawn one pass.
if [ "$(uname)" = Darwin ] && command -v swift >/dev/null 2>&1; then
  if swift "$HERE/ios-screenshot-check.swift" "$tmp/blank.png" >"$tmp/swift.log" 2>&1 || ! grep -q 'looks blank' "$tmp/swift.log"; then
    echo "FAIL: ios-screenshot-check.swift did not call the blank fixture blank"; cat "$tmp/swift.log"; fail=1
  else
    echo "ok: real check calls the blank fixture blank"
  fi
  if ! swift "$HERE/ios-screenshot-check.swift" "$tmp/drawn.png" >"$tmp/swift.log" 2>&1; then
    echo "FAIL: ios-screenshot-check.swift called the drawn fixture blank"; cat "$tmp/swift.log"; fail=1
  else
    echo "ok: real check passes the drawn fixture"
  fi
elif [ "${UT_REQUIRE_IOS_SWIFT:-}" = 1 ]; then
  echo "FAIL: UT_REQUIRE_IOS_SWIFT=1 but swift is not available here"; fail=1
else
  echo "skip: real ios-screenshot-check.swift (no swift on $(uname); ios-ci runs it)"
fi

if [ "$fail" -ne 0 ]; then
  echo "FAIL: ios-sim-wait-drawn self-test"
  exit 1
fi
echo "✓ ios-sim-wait-drawn: a late draw passes, a blank page still fails within the deadline"
