# Universal Till

Ultra-light, offline-first EPOS written in Go. Themeable HTML UI, addon system for payments and hardware, designed to run on Raspberry Pi and small ARM devices.

## Quick start (edge)

```bash
make run
# open http://localhost:8080
```

## Layout

- `cmd/edge` – device runtime entrypoint
- `internal/pos` – core POS logic (placeholder)
- `internal/httpx` – tiny HTTP helpers
- `web/` – HTML templates, static assets
- `proto/` – protobuf contracts (to be filled)
