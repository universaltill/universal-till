BIN=unitill-pos
VERSION?=0.1.0
LDFLAGS=-s -w -X main.version=$(VERSION)

.PHONY: build run test e2e e2e-seed

build:
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BIN) .

run: build
	./bin/$(BIN)
	
test:
	go test ./...

e2e-seed:
	UT_DB_PATH=./data/e2e.db go run ./scripts/e2e_seed/main.go

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
