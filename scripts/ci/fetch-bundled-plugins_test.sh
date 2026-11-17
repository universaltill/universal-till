#!/usr/bin/env bash
#
# Regression test for fetch-bundled-plugins.sh (ADR-0194): offline, through
# --from-dir, with synthetic release bundles and a synthetic lock in a fresh
# mktemp dir. Proves it embeds a bundle whose hashes match the lock, and
# fails closed (writing nothing) on a bundle, manifest, wasm or extra-file
# hash mismatch, a missing pinned file, a path-traversal or non-regular
# archive entry, a malformed or third-party lock, and a download attempt
# outside GitHub-hosted CI.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT_DIR}/scripts/ci/fetch-bundled-plugins.sh"
FAIL_COUNT=0

WORK="$(mktemp -d)"
# Invoked indirectly via `trap ... EXIT` (SC2317 false positive).
# shellcheck disable=SC2317
cleanup() { rm -rf -- "${WORK:?}"; }
trap cleanup EXIT

# make_fixture <name>: bundles/ + lock.json under ${WORK}/<name>; the python
# helper takes a mutation keyword to break exactly one thing.
make_fixture() {
  local name="$1" mutation="${2:-none}"
  python3 -I - "${WORK}/${name}" "${mutation}" <<'PY'
import hashlib, io, json, os, sys, tarfile
root, mutation = sys.argv[1], sys.argv[2]
os.makedirs(os.path.join(root, "bundles"))
sha = lambda b: hashlib.sha256(b).hexdigest()
manifest = b'{"id":"com.universaltill.payment-fake","version":"1.0.0"}'
wasm = b"\x00asm-fake"
loc = b'{"a":"b"}'
files = {"manifest.json": manifest, "bin/plugin.wasm": wasm, "locales/en.json": loc}
extra_member = None
if mutation == "traversal":
    extra_member = ("../evil.txt", b"x")
if mutation == "absolute":
    extra_member = ("/etc/evil.txt", b"x")
buf = io.BytesIO()
with tarfile.open(fileobj=buf, mode="w:gz") as tf:
    for n, b in files.items():
        if mutation == "missing_locale" and n == "locales/en.json":
            continue
        ti = tarfile.TarInfo(n); ti.size = len(b); tf.addfile(ti, io.BytesIO(b))
    if extra_member:
        ti = tarfile.TarInfo(extra_member[0]); ti.size = len(extra_member[1]); tf.addfile(ti, io.BytesIO(extra_member[1]))
    if mutation == "symlink":
        ti = tarfile.TarInfo("link"); ti.type = tarfile.SYMTYPE; ti.linkname = "/etc/passwd"; tf.addfile(ti)
bundle = buf.getvalue()
name = "com.universaltill.payment-fake_1.0.0_universal.tar.gz"
open(os.path.join(root, "bundles", name), "wb").write(bundle)
entry = {"id": "com.universaltill.payment-fake", "version": "1.0.0", "repo": "universaltill/ut-plugin-payment-fake",
         "release_tag": "v1.0.0", "bundle_name": name, "bundle_sha256": sha(bundle),
         "manifest_sha256": sha(manifest), "wasm_path": "bin/plugin.wasm", "wasm_sha256": sha(wasm),
         "files": {"locales/en.json": sha(loc)}}
if mutation == "bundle_hash": entry["bundle_sha256"] = "0" * 64
if mutation == "manifest_hash": entry["manifest_sha256"] = "1" * 64
if mutation == "wasm_hash": entry["wasm_sha256"] = "2" * 64
if mutation == "file_hash": entry["files"]["locales/en.json"] = "3" * 64
if mutation == "third_party": entry["id"] = "com.example.pay"
if mutation == "foreign_repo": entry["repo"] = "someone/ut-plugin-payment-fake"
if mutation == "short_hash": entry["wasm_sha256"] = "abc"
lock = {"schema": 1, "plugins": [entry]}
if mutation == "wasm_path_dotdot": entry["wasm_path"] = "bin/../bin/plugin.wasm"
if mutation == "file_dotdot": entry["files"] = {"locales/../locales/en.json": sha(loc)}
if mutation == "bad_schema": lock["schema"] = 2
open(os.path.join(root, "lock.json"), "w").write(json.dumps(lock))
PY
}

expect_ok() {
  local name="$1"
  if bash "${SCRIPT}" --lock "${WORK}/${name}/lock.json" --dest "${WORK}/${name}/dest" --from-dir "${WORK}/${name}/bundles" >"${WORK}/out" 2>&1 \
    && [ -f "${WORK}/${name}/dest/com.universaltill.payment-fake/bin/plugin.wasm" ] \
    && [ -f "${WORK}/${name}/dest/com.universaltill.payment-fake/locales/en.json" ] \
    && [ -f "${WORK}/${name}/dest/com.universaltill.payment-fake/manifest.json" ]; then
    echo "✓ embedded a bundle whose hashes match the lock"
  else
    echo "❌ FAIL: a matching bundle was not embedded" >&2; cat "${WORK}/out" >&2; FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

expect_fail() { # <fixture> <what>
  local name="$1"
  if bash "${SCRIPT}" --lock "${WORK}/${name}/lock.json" --dest "${WORK}/${name}/dest" --from-dir "${WORK}/${name}/bundles" >"${WORK}/out" 2>&1; then
    echo "❌ FAIL: expected a refusal for $2, but the script passed" >&2; FAIL_COUNT=$((FAIL_COUNT + 1))
  elif [ -e "${WORK}/${name}/dest" ] && [ -n "$(find "${WORK}/${name}/dest" -type f 2>/dev/null | head -1)" ]; then
    echo "❌ FAIL: $2 was refused but left files in the embed dir" >&2; FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ refused $2 and wrote nothing"
  fi
}

make_fixture ok;                    expect_ok ok
for m in bundle_hash manifest_hash wasm_hash file_hash missing_locale traversal absolute symlink \
         third_party foreign_repo short_hash bad_schema wasm_path_dotdot file_dotdot; do
  make_fixture "${m}" "${m}"
  expect_fail "${m}" "${m}"
done

# Downloading outside GitHub-hosted CI is refused (no network is attempted).
make_fixture nodl
if env -u GITHUB_ACTIONS -u RUNNER_ENVIRONMENT -u UT_FETCH_ALLOW_LOCAL \
  bash "${SCRIPT}" --lock "${WORK}/nodl/lock.json" --dest "${WORK}/nodl/dest" >"${WORK}/out" 2>&1; then
  echo "❌ FAIL: a local download was not refused" >&2; FAIL_COUNT=$((FAIL_COUNT + 1))
elif grep -q "GitHub-hosted" "${WORK}/out"; then
  echo "✓ refused a download outside a GitHub-hosted runner"
else
  echo "❌ FAIL: the local download refusal has no explanation" >&2; cat "${WORK}/out" >&2; FAIL_COUNT=$((FAIL_COUNT + 1))
fi

# The committed lock itself is well-formed to this script's validator (the
# script stops at the download/--from-dir step for a missing bundle dir).
if bash "${SCRIPT}" --dest "${WORK}/real" --from-dir "${WORK}/nodl/bundles" >"${WORK}/out" 2>&1; then
  echo "❌ FAIL: the committed lock accepted a foreign bundle dir" >&2; FAIL_COUNT=$((FAIL_COUNT + 1))
elif grep -q "not found in" "${WORK}/out"; then
  echo "✓ the committed lock passes validation (stops at the missing bundle)"
else
  echo "❌ FAIL: the committed lock failed validation" >&2; cat "${WORK}/out" >&2; FAIL_COUNT=$((FAIL_COUNT + 1))
fi

if [ "${FAIL_COUNT}" -ne 0 ]; then echo "${FAIL_COUNT} test(s) failed" >&2; exit 1; fi
echo "✓ fetch-bundled-plugins regression test passed"
