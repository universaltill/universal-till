#!/usr/bin/env bash
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
class H(BaseHTTPRequestHandler):
    def _r(self, code, body):
        n = int(self.headers.get("Content-Length") or 0); self.rfile.read(n)
        with open(log, "a") as f: f.write(f"{self.command} {self.path.split('?')[0]}\n")
        b = json.dumps(body).encode(); self.send_response(code)
        self.send_header("Content-Type", "application/json"); self.send_header("Content-Length", str(len(b)))
        self.end_headers(); self.wfile.write(b)
    def do_POST(self):
        if self.path.endswith("/edits"): return self._r(200, {"id": "e1"})
        if "/upload/" in self.path: return self._r(200, {"versionCode": 7770005})
        return self._r(200, {})
    def do_GET(self):
        self._r(200, {"bundles": [{"versionCode": 7770005, "sha256": existing}] if existing else []})
    do_PUT = do_DELETE = lambda self: self._r(200, {})
    def log_message(self, *a): pass
s = HTTPServer(("127.0.0.1", 0), H)
print(s.server_port, flush=True); s.serve_forever()
PY

run() { # $1 = existing sha ("" none)
  : >"$tmp/log"
  LOG="$tmp/log" EXISTING_SHA="$1" python3 -I "$tmp/fake.py" >"$tmp/port" &
  srv=$!
  for _ in $(seq 50); do [ -s "$tmp/port" ] && break; sleep 0.1; done
  set +e
  PLAY_API_BASE="http://127.0.0.1:$(cat "$tmp/port")" PLAY_ACCESS_TOKEN=t PLAY_PACKAGE_NAME=com.example.app \
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
if grep -qE 'tracks/(production|beta|alpha)' "$ROOT/scripts/ci/play-upload.sh"; then bad "script names a non-internal track"; else ok "script names no non-internal track"; fi
[ "$fails" = 0 ]
