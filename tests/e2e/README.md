# Universal Till E2E (Playwright)

## Prerequisites

- Node.js 18+ (for Playwright)
- Go 1.25 (to run the app)

## Quick Start

1) Install E2E dependencies (terminal A):

```bash
cd tests/e2e
npm install
npx playwright install
```

2) Seed the database (terminal A):

```bash
UT_DB_PATH=./data/e2e.db go run ../../scripts/e2e_seed/main.go
```

3) Start the app (terminal B):

```bash
UT_STORE=sqlite UT_DB_PATH=./data/e2e.db UT_LISTEN_ADDR=:8080 go run .
```

4) Run the tests (terminal A):

```bash
npm run test:e2e
```

## Configuration

- `BASE_URL` (default: `http://localhost:8080`)

Create a local `.env` if needed:

```bash
BASE_URL=http://localhost:8080
```

## Notes

- Tests are deterministic: no hard waits and network-first expectations.
- Artifacts are saved on failure to `tests/e2e/test-results` and `tests/e2e/playwright-report`.
