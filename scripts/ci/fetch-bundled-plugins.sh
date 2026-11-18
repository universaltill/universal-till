#!/usr/bin/env bash
#
# ADR-0194 / Apple guideline 2.5.2: fetch the first-party payment plugins that
# the App Store build embeds (internal/plugins/bundled, build tag
# `bundledplugins`). Run at BUILD time on a GitHub-hosted runner, never on a
# till. The set is exactly what internal/plugins/bundled/bundled.lock.json
# pins: each pinned release bundle is downloaded over HTTPS from the plugin's
# public GitHub release, and its sha256, the manifest's sha256, the wasm's
# sha256 and every pinned extra file's sha256 must match the lock. Anything
# else (a hash mismatch, an unexpected path, a malformed lock, a third-party
# id) fails closed and leaves nothing in the embed directory.
#
# Usage: fetch-bundled-plugins.sh [--lock FILE] [--dest DIR] [--from-dir DIR]
#   --lock      lock file (default internal/plugins/bundled/bundled.lock.json)
#   --dest      embed dir (default internal/plugins/bundled/embedded)
#   --from-dir  take <bundle_name> from DIR instead of downloading (tests, and
#               a local rebuild from release files already fetched by hand)
# Downloading is refused outside GitHub-hosted CI unless UT_FETCH_ALLOW_LOCAL=1
# (public repos: no token, no self-hosted runner, no secret is ever used).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LOCK="${ROOT_DIR}/internal/plugins/bundled/bundled.lock.json"
DEST="${ROOT_DIR}/internal/plugins/bundled/embedded"
FROM_DIR=""

while [ $# -gt 0 ]; do
  case "$1" in
    --lock) LOCK="$2"; shift 2 ;;
    --dest) DEST="$2"; shift 2 ;;
    --from-dir) FROM_DIR="$2"; shift 2 ;;
    *) echo "fetch-bundled-plugins: unknown argument $1" >&2; exit 2 ;;
  esac
done

if [ -z "${FROM_DIR}" ] && [ "${UT_FETCH_ALLOW_LOCAL:-}" != "1" ]; then
  if [ "${GITHUB_ACTIONS:-}" != "true" ] || [ "${RUNNER_ENVIRONMENT:-}" != "github-hosted" ]; then
    echo "fetch-bundled-plugins: refusing to download outside a GitHub-hosted runner (set UT_FETCH_ALLOW_LOCAL=1 to override locally)" >&2
    exit 1
  fi
fi

[ -f "${LOCK}" ] || { echo "fetch-bundled-plugins: lock ${LOCK} not found" >&2; exit 1; }

STAGE="$(mktemp -d)"
# Invoked indirectly via `trap ... EXIT` (SC2317 false positive).
# shellcheck disable=SC2317
cleanup() { rm -rf -- "${STAGE:?}"; }
trap cleanup EXIT

# 1. Validate the lock and download (or copy) every bundle into STAGE/dl.
mkdir -p "${STAGE}/dl" "${STAGE}/out"
LOCK="${LOCK}" STAGE="${STAGE}" FROM_DIR="${FROM_DIR}" python3 -I - <<'PY'
import hashlib, json, os, re, shutil, subprocess, sys, tarfile, urllib.parse

lock_path, stage, from_dir = os.environ["LOCK"], os.environ["STAGE"], os.environ["FROM_DIR"]
SHA = re.compile(r"^[0-9a-f]{64}$")
ID = re.compile(r"^com\.universaltill\.[a-z0-9][a-z0-9.-]*$")
VER = re.compile(r"^[0-9]+\.[0-9]+\.[0-9]+$")
REPO = re.compile(r"^universaltill/ut-plugin-[a-z0-9-]+$")
REL = re.compile(r"^[A-Za-z0-9_][A-Za-z0-9._-]*(/[A-Za-z0-9._-]+)*$")

def safe_rel(rel):
    # Same rule as the Go lock parser (bundled.ParseLock): a safe relative
    # name with no ".." segment.
    return bool(REL.match(rel)) and ".." not in rel.split("/")

def die(msg):
    print("fetch-bundled-plugins: " + msg, file=sys.stderr)
    sys.exit(1)

def sha(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()

try:
    lock = json.load(open(lock_path))
except Exception as e:
    die("lock is not valid JSON: %s" % e)
if lock.get("schema") != 1 or not isinstance(lock.get("plugins"), list) or not lock["plugins"]:
    die("lock has no schema 1 plugin list")

seen = set()
for e in lock["plugins"]:
    pid = e.get("id", "")
    if not ID.match(pid) or pid in seen:
        die("bad or repeated plugin id %r" % pid)
    seen.add(pid)
    if not VER.match(e.get("version", "")):
        die("%s: bad version" % pid)
    if not REPO.match(e.get("repo", "")):
        die("%s: repo is not a universaltill plugin repo" % pid)
    if e.get("release_tag") != "v" + e["version"]:
        die("%s: release_tag must be v<version>" % pid)
    name = e.get("bundle_name", "")
    if not name or "/" in name or "\\" in name or ".." in name:
        die("%s: bad bundle_name" % pid)
    for k in ("bundle_sha256", "manifest_sha256", "wasm_sha256"):
        if not SHA.match(e.get(k, "")):
            die("%s: %s is not a lowercase sha256" % (pid, k))
    if not safe_rel(e.get("wasm_path", "")):
        die("%s: bad wasm_path" % pid)
    for rel, h in (e.get("files") or {}).items():
        if not safe_rel(rel) or rel == "manifest.json" or rel == e["wasm_path"] or not SHA.match(h):
            die("%s: bad pinned file %r" % (pid, rel))

    bundle = os.path.join(stage, "dl", name)
    if from_dir:
        src = os.path.join(from_dir, name)
        if not os.path.isfile(src):
            die("%s: %s not found in %s" % (pid, name, from_dir))
        shutil.copyfile(src, bundle)
    else:
        url = "https://github.com/%s/releases/download/%s/%s" % (e["repo"], e["release_tag"], urllib.parse.quote(name))
        r = subprocess.run(["curl", "--fail", "--silent", "--show-error", "--location", "--proto", "=https", "--proto-redir", "=https",
                            "--tlsv1.2", "--max-time", "300", "--retry", "3", "--output", bundle, url])
        if r.returncode != 0:
            die("%s: download failed (%s)" % (pid, url))
    if sha(bundle) != e["bundle_sha256"]:
        die("%s: bundle sha256 does not match the lock" % pid)

    # Extract only regular files with safe relative names, into a private dir.
    ex = os.path.join(stage, "x-" + pid)
    os.makedirs(ex)
    with tarfile.open(bundle, "r:gz") as tf:
        for m in tf.getmembers():
            n = m.name[2:] if m.name.startswith("./") else m.name
            if m.isdir():
                continue
            if not m.isreg() or n.startswith("/") or not safe_rel(n):
                die("%s: unexpected archive entry %r" % (pid, m.name))
            target = os.path.join(ex, n)
            os.makedirs(os.path.dirname(target), exist_ok=True)
            with tf.extractfile(m) as src, open(target, "wb") as dst:
                shutil.copyfileobj(src, dst)

    want = {"manifest.json": e["manifest_sha256"], e["wasm_path"]: e["wasm_sha256"]}
    want.update(e.get("files") or {})
    out = os.path.join(stage, "out", pid)
    for rel, h in want.items():
        p = os.path.join(ex, rel)
        if not os.path.isfile(p) or sha(p) != h:
            die("%s: %s is missing or does not match the lock" % (pid, rel))
        t = os.path.join(out, rel)
        os.makedirs(os.path.dirname(t), exist_ok=True)
        shutil.copyfile(p, t)
    print("verified %s %s" % (pid, e["version"]))
PY

# 2. Everything verified: replace the embed dir contents (keeping .gitkeep).
mkdir -p "${DEST}"
for d in "${STAGE}"/out/*/; do
  id="$(basename "${d}")"
  rm -rf -- "${DEST:?}/${id:?}"
  cp -R "${d%/}" "${DEST}/${id}"
done
echo "✓ fetch-bundled-plugins: embedded the pinned first-party plugins into ${DEST}"
