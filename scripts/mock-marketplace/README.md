# Mock Marketplace Server

Local HTTP server simulating the Universal Till plugin marketplace for development and testing.

## Usage

```bash
go run ./scripts/mock-marketplace/main.go
```

Server starts on `:8081` with these endpoints:

- `GET /health` - Health check
- `GET /v1/catalog/plugins` - List available plugins (returns 3 sample plugins)
- `POST /v1/download/token` - Issue download token (for future use)
- `GET /artifacts/{filename}` - Download plugin packages (currently returns empty bodies)

## Sample Plugins

1. **sales-report** v1.0.0 (verified, type: page)
2. **loyalty-card** v2.1.3 (untrusted, type: payment)
3. **inventory-sync** v0.9.0 (verified, type: background)

See `../specs/007-plugin-host/quickstart.md` for full testing guide.
