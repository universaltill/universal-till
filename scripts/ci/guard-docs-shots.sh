#!/usr/bin/env bash
#
# Manual screenshot freshness guard (ut-docs#327): the user manual ships with
# the product, so its screenshots must never lag the screens they show.
# `make docs-shots` captures web/help/img/<locale>/<id>.png for every routed
# help topic and records content hashes in web/help/img/manifest.json; this
# guard recomputes the same hashes from the working tree and fails when they
# have drifted — i.e. when a topic's markdown, or any file of the app surface
# that could change what is on screen (web/ui/**, non-test
# internal/pages/**.go), changed after the screenshots were last taken.
#
# HASH ALGORITHM — must stay in lockstep with e2e/tests-docs/lib.js (the
# generation side), where it is spelled out in full:
#   surface_sha256 = sha256 of the concatenation of
#       "<sha256(file)>  <relpath>\n"   (two spaces, sha256sum format)
#   over the sorted (lexicographic, ASCII paths) repo-relative fileset:
#   every regular file under web/ui/ + web/public/ (templates AND the CSS/JS
#   that actually paints them — a theme/app.css change is exactly as visible
#   in a screenshot as a template change) + every non-_test.go *.go under
#   internal/pages/. Topic hash = sha256 of web/help/<locale>/<id>.md,
#   falling back to web/help/en/<id>.md.
#
#   web/locales/**/*.json is DELIBERATELY excluded: every i18n key touches
#   every locale file, which would force a 40-screenshot regen on almost any
#   string change. Accepted gap — a copy change can go stale in a screenshot
#   without tripping this guard.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

MANIFEST="web/help/img/manifest.json"
[ -f "$MANIFEST" ] || {
  echo "guard-docs-shots: $MANIFEST missing — run \`make docs-shots\` and commit the result" >&2
  exit 1
}

python3 - <<'PY'
import glob, hashlib, json, os, re, sys

LOCALES = ["en", "fa", "ar", "tr"]  # must match e2e/tests-docs/lib.js

def sha256_file(path):
    with open(path, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()

# --- routed topics, from the same tiny front-matter subset internal/manual
# and e2e/tests-docs/lib.js parse ------------------------------------------
def routed_topics():
    out = []
    for path in sorted(glob.glob("web/help/en/*.md")):
        text = open(path, encoding="utf-8").read().replace("\r\n", "\n")
        if not text.startswith("---\n"):
            continue
        end = text.find("\n---", 4)
        if end < 0:
            continue
        tid, routes = None, None
        for line in text[4:end].split("\n"):
            m = re.match(r"^([A-Za-z_]+):\s*(.*)$", line.strip())
            if not m:
                continue
            if m.group(1) == "id":
                tid = m.group(2).strip()
            if m.group(1) == "routes":
                routes = [
                    s.strip().strip("\"'")
                    for s in m.group(2).lstrip("[").rstrip("]").split(",")
                    if s.strip().strip("\"'")
                ]
        if tid and routes:
            out.append(tid)
    return out

# --- surface hash ----------------------------------------------------------
def surface_files():
    files = []
    for base, want_go in (("web/ui", False), ("web/public", False), ("internal/pages", True)):
        for root, _, names in os.walk(base):
            for n in names:
                p = os.path.join(root, n).replace(os.sep, "/")
                if want_go and (not p.endswith(".go") or p.endswith("_test.go")):
                    continue
                files.append(p)
    return sorted(files)

def surface_hash():
    lines = "".join(f"{sha256_file(p)}  {p}\n" for p in surface_files())
    return hashlib.sha256(lines.encode("utf-8")).hexdigest()

def topic_hash(locale, tid):
    p = f"web/help/{locale}/{tid}.md"
    if not os.path.isfile(p):
        p = f"web/help/en/{tid}.md"
    return sha256_file(p)

manifest = json.load(open("web/help/img/manifest.json", encoding="utf-8"))
topics = routed_topics()
stale, missing_png = [], []

cur_surface = surface_hash()
surface_stale = manifest.get("surface_sha256") != cur_surface

recorded = manifest.get("topics", {})
for tid in topics:
    entry = recorded.get(tid, {})
    for locale in LOCALES:
        if not os.path.isfile(f"web/help/img/{locale}/{tid}.png"):
            missing_png.append(f"{locale}/{tid}")
        if entry.get(locale) != topic_hash(locale, tid):
            stale.append(f"{locale}/{tid}")

# A manifest entry for a topic that no longer declares routes is stale too —
# the manifest must describe exactly the current routed set.
orphans = sorted(set(recorded) - set(topics))

fail = False
if surface_stale:
    fail = True
    print("guard-docs-shots: the app surface (web/ui/** or internal/pages/**.go) "
          "changed since the manual's screenshots were last taken")
if stale:
    fail = True
    print("guard-docs-shots: topic markdown changed since its screenshot was taken (locale/topic):")
    for s in stale:
        print("  -", s)
if missing_png:
    fail = True
    print("guard-docs-shots: routed topics with no screenshot (locale/topic):")
    for s in missing_png:
        print("  -", s)
if orphans:
    fail = True
    print("guard-docs-shots: manifest entries for topics that are no longer routed:")
    for s in orphans:
        print("  -", s)

if fail:
    print("guard-docs-shots: run `make docs-shots` and commit the result")
    sys.exit(1)
print(f"✓ docs-shots guard: {len(topics)} routed topics × {len(LOCALES)} locales "
      f"screenshotted and fresh (surface {cur_surface[:12]}…)")
PY
