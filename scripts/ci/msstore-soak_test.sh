#!/usr/bin/env bash
# Test for msstore-soak.sh against a local server that redirects like GitHub.
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
S="${ROOT_DIR}/scripts/ci/msstore-soak.sh"
W="$(mktemp -d)"
SRV=""
# shellcheck disable=SC2317
cleanup() { [ -n "$SRV" ] && kill "$SRV" 2>/dev/null || true; rm -rf -- "${W:?}"; }
trap cleanup EXIT
command -v python3 >/dev/null || { echo "skip: python3 missing"; exit 0; }
head -c 5000 /dev/urandom >"$W/a.bin"; head -c 5000 /dev/urandom >"$W/b.bin"
echo a >"$W/mode"; echo 0 >"$W/n"
cat >"$W/srv.py" <<'PY'
import http.server, sys, os
W = sys.argv[1]
def rd(n): return open(os.path.join(W, n)).read().strip()
class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def _go(self, body):
        if self.path.startswith('/dl/'):
            n = int(rd('n')) + 1; open(os.path.join(W, 'n'), 'w').write(str(n))
            mode = rd('mode')
            tgt = '/obj/x.exe?sig=%d' % n
            if mode == 'redir' and n > 3: tgt = '/other/x.exe?sig=%d' % n
            self.send_response(302); self.send_header('Location', tgt); self.end_headers(); return
        mode = rd('mode'); n = int(rd('n'))
        data = open(os.path.join(W, 'b.bin' if (mode == 'swap' and n > 3) else 'a.bin'), 'rb').read()
        if mode == 'norange': pass
        rng = self.headers.get('Range')
        if rng and mode != 'norange':
            data = data[:1024]; self.send_response(206)
        else: self.send_response(200)
        self.send_header('Content-Length', str(len(data))); self.end_headers()
        if body: self.wfile.write(data)
    def do_GET(self): self._go(True)
    def do_HEAD(self): self._go(False)
s = http.server.ThreadingHTTPServer(('127.0.0.1', 0), H)
open(os.path.join(W, 'port'), 'w').write(str(s.server_address[1])); s.serve_forever()
PY
python3 "$W/srv.py" "$W" & SRV=$!
for _ in $(seq 1 50); do [ -s "$W/port" ] && break; sleep 0.1; done
URL="http://127.0.0.1:$(cat "$W/port")/dl/x.exe"
SHA="$(shasum -a 256 "$W/a.bin" | awk '{print $1}')"
export MSSTORE_SOAK_ALLOW_HTTP=1
fails=0
t() { # t <want-rc> <name> <mode> [args...]
  local want=$1 name=$2; echo "$3" >"$W/mode"; echo 0 >"$W/n"; shift 3
  local rc=0; bash "$S" "$URL" "$@" >"$W/out" 2>&1 || rc=$?
  if [ "$rc" = "$want" ]; then echo "ok   - $name"; else echo "FAIL - $name (rc $rc)" >&2; cat "$W/out" >&2; fails=$((fails+1)); fi
}
t 0 "stable URL passes 3 checks" a "$SHA" 5000 3 0
t 1 "redirect target change fails" redir "$SHA" 5000 5 0
t 1 "bytes changing mid-window fail" swap "$SHA" 5000 5 0
t 1 "wrong expected sha fails" a "$(printf '0%.0s' $(seq 1 64))" 5000 2 0
t 1 "wrong expected size fails" a "$SHA" 4999 2 0
t 1 "no range support fails" norange "$SHA" 5000 2 0
t 2 "bad checks arg is a usage error" a "$SHA" 5000 0 0
[ "$fails" = 0 ] || exit 1
echo "✓ msstore-soak_test: all cases passed"
