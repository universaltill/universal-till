#!/usr/bin/env bash
# shellcheck disable=SC2015,SC2016
# Self-test for play-upload.sh (ut-docs#6410, #6405) against a local fake Play
# API: a fresh versionCode uploads and commits to `internal` only; the same
# bytes already on Play are a green no-op; different bytes under an existing
# versionCode fail and commit nothing.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
tmp="$(mktemp -d)"
trap 'kill "${srv:-0}" 2>/dev/null || true; rm -rf "$tmp"' EXIT
fails=0
ok() { echo "ok   - $1"; }
bad() { echo "FAIL - $1"; fails=$((fails + 1)); }

head -c 4096 /dev/urandom >"$tmp/a.aab"
sha="$(shasum -a 256 "$tmp/a.aab" | cut -d' ' -f1)"

cat >"$tmp/fake.py" <<'PY'
import json, os, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
log = os.environ["LOG"]; existing = os.environ.get("EXISTING_SHA", "")
got_vc = int(os.environ.get("GOT_VC", "7770005")); fail_on = os.environ.get("FAIL_ON", "")
class H(BaseHTTPRequestHandler):
    def _r(self, code, body):
        n = int(self.headers.get("Content-Length") or 0); self.rfile.read(n)
        with open(log, "a") as f: f.write(f"{self.command} {self.path.split('?')[0]}\n")
        if self.headers.get("Authorization") != "Bearer t":
            with open(log, "a") as f: f.write("NOAUTH\n")
        if fail_on and fail_on in self.path and code == 200: code = 500
        b = json.dumps(body).encode(); self.send_response(code)
        self.send_header("Content-Type", "application/json"); self.send_header("Content-Length", str(len(b)))
        self.end_headers(); self.wfile.write(b)
    def do_POST(self):
        if self.path.endswith("/edits"): return self._r(200, {"id": "e1"})
        if "/upload/" in self.path: return self._r(200, {"versionCode": got_vc})
        return self._r(200, {})
    def do_GET(self):
        self._r(200, {"bundles": [{"versionCode": 7770005, "sha256": existing}] if existing else []})
    do_PUT = do_DELETE = lambda self: self._r(200, {})
    def log_message(self, *a): pass
s = HTTPServer(("127.0.0.1", 0), H)
print(s.server_port, flush=True); s.serve_forever()
PY

run() { # $1 = existing sha ("" none); GOT_VC / FAIL_ON optional
  : >"$tmp/log"
  LOG="$tmp/log" EXISTING_SHA="$1" GOT_VC="${GOT_VC:-7770005}" FAIL_ON="${FAIL_ON:-}" python3 -I "$tmp/fake.py" >"$tmp/port" &
  srv=$!
  for _ in $(seq 50); do [ -s "$tmp/port" ] && break; sleep 0.1; done
  set +e
  PLAY_API_TEST_SERVER=1 PLAY_API_BASE="http://127.0.0.1:$(cat "$tmp/port")" PLAY_ACCESS_TOKEN=t PLAY_PACKAGE_NAME=com.example.app \
    AAB="$tmp/a.aab" VERSION_CODE=7770005 VERSION_NAME=7.77.0 bash "$ROOT/scripts/ci/play-upload.sh" >"$tmp/out" 2>&1
  rc=$?
  set -e
  kill "$srv"; wait "$srv" 2>/dev/null || true
}

run ""
{ [ "$rc" = 0 ] && grep -q 'edits/e1:commit$' "$tmp/log" && grep -q 'PUT .*/tracks/internal$' "$tmp/log"; } && ok "fresh versionCode uploads to internal and commits" || bad "fresh upload ($(cat "$tmp/out"))"
run "$sha"
{ [ "$rc" = 0 ] && ! grep -q 'commit' "$tmp/log" && ! grep -q '/upload/' "$tmp/log"; } && ok "same bytes already on Play: green no-op" || bad "idempotent re-run"
run "$(printf 'f%.0s' $(seq 64))"
{ [ "$rc" != 0 ] && grep -q 'DIFFERENT sha256' "$tmp/out" && ! grep -q 'commit' "$tmp/log" && ! grep -q '/upload/' "$tmp/log"; } && ok "different bytes under an existing versionCode fail" || bad "changed hash refused"
# one case per protection ---------------------------------------------------
# Play reads another versionCode from the bundle: nothing is committed.
GOT_VC=77700050 run ""
{ [ "$rc" != 0 ] && ! grep -q 'commit' "$tmp/log" && ! grep -q 'tracks/' "$tmp/log"; } && ok "versionCode read by Play differs: refused, nothing committed" || bad "versionCode mismatch accepted"
# A failing API call fails the run (curl --fail-with-body) and commits nothing.
FAIL_ON="/bundles?uploadType" run ""
{ [ "$rc" != 0 ] && ! grep -q 'commit' "$tmp/log"; } && ok "HTTP error on upload fails the run" || bad "HTTP 500 swallowed"
FAIL_ON="/tracks/" run ""
{ [ "$rc" != 0 ] && ! grep -q 'commit' "$tmp/log"; } && ok "HTTP error on track update fails, no commit" || bad "track error swallowed"
# Every request carried the bearer token (header file works) ...
run ""
! grep -q NOAUTH "$tmp/log" && ok "every request is authenticated" || bad "a request had no bearer token"
# ... and the token is never on a command line, in a trace or in the output.
S="$ROOT/scripts/ci/play-upload.sh"
stray="$(grep -vE '^[[:space:]]*#' "$S" | grep 'PLAY_ACCESS_TOKEN' | grep -vE '^: "\$\{PLAY_ACCESS_TOKEN|^printf .Authorization: Bearer %s.n. "\$PLAY_ACCESS_TOKEN" >"\$work/hdr"' || true)"
[ -z "$stray" ] && ok "token only written to the header file" || bad "token used outside the header file: $stray"
grep -qE '(^|[[:space:];])set[[:space:]]+-[a-z]*x|set -o xtrace|bash -x' "$S" && bad "xtrace in play-upload.sh" || ok "no xtrace"
grep -q -- '--fail-with-body' "$S" && ok "curl uses --fail-with-body" || bad "curl lacks --fail-with-body"
grep -q 'PLAY_ACCESS_TOKEN\|Bearer' "$tmp/out" && bad "token in the output" || ok "token not in the output"
# The Play token must never be sent to another host (not even https, not a local server without the test flag).
for base in https://evil.example http://127.0.0.1:1@localhost:9 http://127.0.0.1:80/@evil.example https://androidpublisher.googleapis.com.evil.example; do
  if PLAY_API_TEST_SERVER=1 PLAY_API_BASE="$base" PLAY_ACCESS_TOKEN=t PLAY_PACKAGE_NAME=com.example.app AAB="$tmp/a.aab" VERSION_CODE=7770005 VERSION_NAME=7.77.0 \
    bash "$ROOT/scripts/ci/play-upload.sh" >"$tmp/out" 2>&1; then bad "PLAY_API_BASE=$base accepted"; else grep -q 'PLAY_API_BASE must be' "$tmp/out" && ok "PLAY_API_BASE=$base refused" || bad "refusal text for $base"; fi
done
# a local server without the self-test flag is refused too
if PLAY_API_BASE="http://127.0.0.1:1" PLAY_ACCESS_TOKEN=t PLAY_PACKAGE_NAME=com.example.app AAB="$tmp/a.aab" VERSION_CODE=7770005 VERSION_NAME=7.77.0 \
  bash "$ROOT/scripts/ci/play-upload.sh" >"$tmp/out" 2>&1; then bad "local server accepted without the test flag"; else ok "local server refused without the test flag"; fi
if grep -qE 'tracks/(production|beta|alpha)' "$ROOT/scripts/ci/play-upload.sh"; then bad "script names a non-internal track"; else ok "script names no non-internal track"; fi
# TLS verification stays on and redirects are never followed (the token must not leave the API host)
if grep -qE -- '(^|[[:space:]])(-k|--insecure|-L|--location|--location-trusted)([[:space:]]|$)' "$S"; then bad "curl insecure/redirect flag"; else ok "curl verifies TLS and does not follow redirects"; fi
[ "$fails" = 0 ]
