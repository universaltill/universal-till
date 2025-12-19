# Marketplace Contract Tests (Pact)

These tests define the POS consumer contract with the marketplace service.

## Run

```bash
go test -tags=contract ./tests/contract
```

## Output

Pacts and logs are written to:

- `tests/contract/pacts/`
- `tests/contract/logs/`

## Publish (optional)

Use the Pact CLI to publish pacts to your broker after tests run.
