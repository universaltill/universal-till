#!/usr/bin/env bash
#
# Manual screenshot freshness guard (ut-docs#327): the user manual ships with
# the product, so its screenshots must never lag the screens they show.
# `make docs-shots` captures web/help/img/<locale>/<id>.png for every routed
# help topic and records content hashes in web/help/img/manifest.json; this
# guard recomputes the same hashes from the working tree and fails when they
# have drifted — i.e. when a topic's markdown, or any file of the app surface
# that could change what is on screen (web/ui/**, web/public/**, non-test
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
#   internal/pages/ EXCEPT a file whose ONLY registered mux routes are all
#   unscreenshotted (ut-docs#620) — i.e. it registers at least one route
#   (mux.HandleFunc("[METHOD ]<route>", ...) / mux.Handle(...) literal) but
#   none of them is any topic's routes[0], the one URL docs-shots.spec.ts
#   actually visits for that topic. This is deliberately narrower than "no
#   screenshotted route reachable from this file, so exclude it": a file
#   with ZERO route registrations (a shared helper like init.go's baseMenu,
#   common/state.go's BuildMenu, or themes.go's availableThemes) is kept —
#   proven necessary in review: such a file can still feed a screenshotted
#   page's template data (e.g. the menu tile list rendered by every page,
#   or /settings' theme picker) without registering any route itself, so
#   excluding it on "no route at all" would silently miss a real pixel
#   change. Only a file that positively identifies itself as owning a
#   *different*, unscreenshotted page (import_page.go registers /import,
#   which is catalog's routes[1], not any topic's routes[0]) is excluded —
#   that's the actual bug this guard used to have (a purely backend fix
#   inside import_page.go's mergeTakeawayOverrides, nowhere near its own
#   httpx.Render call, still failed this guard for zero pixel reason).
#   A file that registers ANY screenshotted route is hashed as a WHOLE FILE
#   (no attempt to isolate which function inside it actually renders — see
#   the code review for why that finer split was rejected as unsafe).
#   Known residual gap, accepted: the route-extraction regex only matches
#   literal `mux.HandleFunc(...)`/`mux.Handle(...)` calls — every real
#   non-test registration in this package uses the `mux` receiver name
#   today (verified during review), but a future file using a differently-
#   named ServeMux variable would silently register zero routes and so
#   would be *kept* (falls into the safe "zero registrations" bucket above,
#   not excluded) — this asymmetry means the residual risk is only ever
#   over-inclusion, never a new false negative. Topic hash = sha256 of
#   web/help/<locale>/<id>.md, falling back to web/help/en/<id>.md.
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
# Returns (topic_id, first_route) pairs — first_route is the one URL
# docs-shots.spec.ts actually screenshots for that topic.
def routed_topics_with_routes():
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
            out.append((tid, routes[0]))
    return out

# --- surface hash ----------------------------------------------------------
# ServeMux route registrations, e.g. mux.HandleFunc("GET /reports", ...) or
# the method-prefix-less mux.HandleFunc("/backoffice", ...) form — both are
# in real use in this package. Must stay in lockstep with
# e2e/tests-docs/lib.js's own copy of this regex.
ROUTE_CALL_RE = re.compile(r'mux\.(?:HandleFunc|Handle)\(\s*"(?:[A-Z]+\s+)?([^"]*)"')

def registered_routes(path):
    with open(path, encoding="utf-8") as f:
        return set(ROUTE_CALL_RE.findall(f.read()))

def surface_files(screenshotted_routes):
    files = []
    for base, want_go in (("web/ui", False), ("web/public", False), ("internal/pages", True)):
        for root, dirs, names in os.walk(base):
            # Skip dotfiles/dot-dirs so the surface hash is a property of the
            # REPO, not of the machine computing it. macOS drops `.DS_Store`
            # into any browsed directory; it is gitignored, so it exists in a
            # developer's tree but never in CI's checkout — hashing it made a
            # Mac-generated manifest fail here with "the app surface changed"
            # and no visible cause in the diff (ut-docs#659). Must stay in
            # lockstep with e2e/tests-docs/lib.js's walkFiles.
            dirs[:] = [d for d in dirs if not d.startswith(".")]
            for n in names:
                if n.startswith("."):
                    continue
                p = os.path.join(root, n).replace(os.sep, "/")
                if want_go:
                    if not p.endswith(".go") or p.endswith("_test.go"):
                        continue
                    routes = registered_routes(p)
                    # Exclude ONLY a file that registers routes and none of
                    # them is screenshotted (e.g. import_page.go: /import).
                    # A file with NO route registrations at all is kept —
                    # it may be a shared helper (init.go, common/*.go,
                    # themes.go) feeding a screenshotted page's template
                    # data without registering any route itself.
                    if routes and not (routes & screenshotted_routes):
                        continue
                files.append(p)
    return sorted(files)

def surface_hash(screenshotted_routes):
    lines = "".join(f"{sha256_file(p)}  {p}\n" for p in surface_files(screenshotted_routes))
    return hashlib.sha256(lines.encode("utf-8")).hexdigest()

def topic_hash(locale, tid):
    p = f"web/help/{locale}/{tid}.md"
    if not os.path.isfile(p):
        p = f"web/help/en/{tid}.md"
    return sha256_file(p)

manifest = json.load(open("web/help/img/manifest.json", encoding="utf-8"))
topic_routes = routed_topics_with_routes()
topics = [tid for tid, _ in topic_routes]
screenshotted_routes = {route for _, route in topic_routes}
stale, missing_png = [], []

cur_surface = surface_hash(screenshotted_routes)
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
    print("guard-docs-shots: the app surface (web/ui/**, web/public/**, or "
          "internal/pages/**.go) changed since the manual's screenshots were "
          "last taken")
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
