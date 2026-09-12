BIN=unitill-pos
VERSION?=0.1.0
# internal/buildinfo.Version is the symbol the app actually reads (see that
# package's doc comment) — "main.version" doesn't exist anywhere in this
# codebase, so `-X main.version=...` used to be a silent no-op: `go build`
# does not error on an -X target that isn't a real symbol. Every `make
# build` binary reported Version="dev", which internal/updates.Newer treats
# as older than every release — with auto-update on, the till would
# silently replace a freshly built/deployed binary with the latest GitHub
# release minutes later (ut-docs#369, found deploying a field hotfix).
LDFLAGS=-s -w -X github.com/universaltill/universal-till/internal/buildinfo.Version=$(VERSION)

.PHONY: build run test test-race-plugins test-race-pages e2e e2e-seed docs-shots prune-worktrees

build:
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BIN) .

run: build
	./bin/$(BIN)

test:
	go test ./...

# internal/pages' own -race runtime sits with no margin against (or past)
# the plain `go test` 600s per-package default (ut-docs#648) — this package
# is DB-heavy and -race instrumentation multiplies that cost, unlike the
# package's plain (non-race) runtime, which `make test` above already
# covers comfortably. -race isn't run in CI (ci.yml deliberately never
# uses it, see its own comment on the internal/plugins step) — this target
# is the safe way to run it by hand (e.g. during Reviewer/Tester gate
# verification) without an unqualified `go test ./internal/pages/... -race`
# risking the bare default timeout. Same shape as the internal/plugins fix
# (ut-docs#643): a longer, explicit timeout instead of raising it globally,
# so a genuine hang elsewhere still fails within the normal default.
#
# The 30m timeout this target originally shipped with (ut-docs#1003) already
# had no real margin left: ut-docs#1034/#1119 measured a clean run at
# 1531s (~25.5min) — ~85% of that budget — after the TestFiscalSignAsk_*
# WASM-plugin suite (each subtest loads a real wazero module) grew the
# package further. 60m gives ~2.4x that measured runtime, matching the
# generous-margin shape of the internal/plugins precedent above (that
# package's own ~85-90s runtime against a 20m/1200s budget) instead of
# re-creating the same near-the-wire margin one test class later.
test-race-pages:
	go test -race -timeout 60m ./internal/pages/...

# internal/plugins under -race hits the same shape as internal/pages above,
# for the same reason: this package is heavy with real wazero WASM-module
# tests (each compiles/instantiates and runs actual guest bytecode), which
# -race instrumentation multiplies significantly — not a deadlock, just no
# margin against the bare default (ut-docs#2156, found via a go test -race
# timeout's goroutine dump naming a "surviving" plugins/tax-tr/okc/sim
# goroutine that was never the actual cause: two independent full runs
# confirmed the package genuinely completes, no hang, no failure — first
# a 900s-timeout run killed mid-flight while still legitimately loading a
# real wazero module (not blocked on anything), then a clean pass at
# 1078.827s (~18min)). 45m gives ~2.5x that measured runtime, the same
# generous-margin shape as test-race-pages above (~2.4x) and the original
# internal/plugins precedent (ut-docs#643, ~85-90s against a 20m budget).
# -race isn't run in CI for this package either (ci.yml's own comment on
# the internal/plugins step) — this is the safe way to run it by hand.
test-race-plugins:
	go test -race -timeout 45m ./internal/plugins/...

e2e-seed:
	UT_DB_PATH=./data/e2e.db go run ./scripts/e2e_seed/main.go

# Removes stale agent-created worktrees under .claude/worktrees/ that are
# already fully merged into origin/main and past the retention window
# (default 3 days) — see scripts/prune-stale-worktrees.sh (ut-docs#1567).
# Never touches a worktree that still holds unmerged or uncommitted work.
prune-worktrees:
	bash scripts/prune-stale-worktrees.sh

# Regenerates the user manual's screenshots (web/help/img/<locale>/<id>.png,
# one per routed help topic per shipped locale) against a real till, then
# rewrites web/help/img/manifest.json — the freshness record
# scripts/ci/guard-docs-shots.sh checks in CI. Run this and commit the result
# whenever a screen or a routed topic changes.
#
# Delegates to e2e/scripts/docs-shots.sh, which reuses a pre-installed
# Chromium when one is smoke-tested launchable (ut-docs#622) instead of
# always running `playwright install --with-deps chromium` — a cold cloud
# pipeline session can't download a browser, so without this the target was
# simply unrunnable there.
docs-shots:
	bash e2e/scripts/docs-shots.sh

e2e:
	@set -e; \
	UT_STORE=sqlite UT_DB_PATH=./data/e2e.db UT_LISTEN_ADDR=:8080 go run . & \
	E2E_PID=$$!; \
	trap 'kill $$E2E_PID' EXIT; \
	for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30; do \
		if curl -sf http://127.0.0.1:8080/ >/dev/null; then echo \"App is up\"; break; fi; \
		sleep 1; \
		if [ $$i -eq 30 ]; then echo \"App did not start in time\" >&2; exit 1; fi; \
	done; \
	(cd tests/e2e && BASE_URL=http://127.0.0.1:8080 npm run test:e2e)
