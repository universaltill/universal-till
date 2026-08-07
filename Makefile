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

.PHONY: build run test e2e e2e-seed docs-shots

build:
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BIN) .

run: build
	./bin/$(BIN)
	
test:
	go test ./...

e2e-seed:
	UT_DB_PATH=./data/e2e.db go run ./scripts/e2e_seed/main.go

# Regenerates the user manual's screenshots (web/help/img/<locale>/<id>.png,
# one per routed help topic per shipped locale) against a real till, then
# rewrites web/help/img/manifest.json — the freshness record
# scripts/ci/guard-docs-shots.sh checks in CI. Run this and commit the result
# whenever a screen or a routed topic changes.
docs-shots:
	cd e2e && npm ci && npx playwright install --with-deps chromium && npx playwright test --config=playwright.docs.config.ts && node tests-docs/write-manifest.js

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
