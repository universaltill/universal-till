#!/usr/bin/env bash
#
# ADR-0194 §2.3 (ut-docs#6388, review S7): a Google Play Android build must be
# stamped installChannel=play, or it ships with plan purchase and self-update
# ON. Two checks, both static:
#   1. android/app/build.gradle.kts keeps its task-graph assertion (a
#      assemble/bundle/publish/package task naming "Play" fails unless
#      installChannel == play).
#   2. Every workflow line that runs a gradle task naming "Play" also passes
#      -PinstallChannel=play on that line or the continuation lines of the
#      same command.
# Usage: guard-android-play-channel.sh [gradle-file] [workflows-dir]
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT}"
GRADLE="${1:-android/app/build.gradle.kts}"
WF="${2:-.github/workflows}"
[ -f "${GRADLE}" ] || { echo "guard-android-play-channel: ${GRADLE} missing" >&2; exit 1; }

exec python3 -I - "${GRADLE}" "${WF}" <<'PY'
import glob, re, sys
gradle, wf = sys.argv[1], sys.argv[2]
fails = 0
g = open(gradle, encoding="utf-8").read()
if not (re.search(r"taskGraph\.whenReady", g) and re.search(r"installChannel\s*!=\s*\"play\"", g) and "GradleException" in g):
    print(f"::error file={gradle}::the Play-build channel assertion (taskGraph.whenReady ... installChannel != \"play\" -> GradleException) is gone")
    fails += 1
TASK = re.compile(r"(?:assemble|bundle|publish|package)\w*Play", re.I)
for f in sorted(glob.glob(wf + "/*.yml") + glob.glob(wf + "/*.yaml")):
    lines = open(f, encoding="utf-8").read().split("\n")
    for i, line in enumerate(lines):
        if not TASK.search(line):
            continue
        # the whole shell command: this line plus backslash continuations on both sides
        a = i
        while a > 0 and lines[a - 1].rstrip().endswith("\\"):
            a -= 1
        b = i
        while lines[b].rstrip().endswith("\\") and b + 1 < len(lines):
            b += 1
        cmd = "\n".join(lines[a:b + 1])
        if not re.search(r"-PinstallChannel=play\b", cmd):
            print(f"::error file={f},line={i+1}::a Play gradle task runs without -PinstallChannel=play (ADR-0194 §2.3)")
            fails += 1
if fails:
    sys.exit(1)
print("guard-android-play-channel: ok")
PY
