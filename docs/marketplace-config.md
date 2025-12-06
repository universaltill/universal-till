# Marketplace Configuration Guide

## Quick Reference

Universal Till can connect to different marketplace endpoints for plugin installation:

| Environment | Port | Endpoint | Use Case |
|-------------|------|----------|----------|
| **Mock** | 8082 | http://localhost:8082 | Local development & testing |
| **Production** | 8081 | http://localhost:8081 or https://marketplace.universaltill.com | Production use |
| **Custom** | Any | Custom URL | Self-hosted marketplace |

# Marketplace Configuration Guide

## Quick Reference

Universal Till is a **client application** that connects to a marketplace for plugin installation. Configuration is stored in a single file: **`pos.env`**

| Environment | Config File | Use Case |
|-------------|-------------|----------|
| **Production** | `pos.env` | Created during installation |
| **Development** | `pos.env.dev` | Local development (not committed) |
| **Testing** | `pos.env.test` | Automated tests |

## Configuration Methods

### Method 1: Installation Config (Production/Client Use)

On first installation, Universal Till creates `pos.env`:

```bash
# First-time setup
cp pos.env.example pos.env
nano pos.env  # Edit with your settings

# Run
./bin/unitill-pos
# Loads: pos.env
```

**For different marketplace endpoints:**

```bash
# Production marketplace
UT_MARKETPLACE_ENDPOINT_URL=https://marketplace.universaltill.com

# Local/self-hosted marketplace
UT_MARKETPLACE_ENDPOINT_URL=http://localhost:8081

# Mock marketplace (development)
UT_MARKETPLACE_ENDPOINT_URL=http://localhost:8082
```

### Method 2: Development Override

For development, use a separate config file:

```bash
# Use development config
UT_ENV_FILE=pos.env.dev ./bin/unitill-pos

# Or use the dev script (automatically sets UT_ENV_FILE)
./scripts/dev.sh
```

### Method 3: Environment-Specific Files (Recommended)

Edit `pos.env.dev`:

```bash
# For mock marketplace (local testing)
UT_MARKETPLACE_ENDPOINT_URL=http://localhost:8082
UT_MARKETPLACE_CLIENT_ID=pos-client
UT_MARKETPLACE_CLIENT_SECRET=dev-secret

# For production marketplace
UT_MARKETPLACE_ENDPOINT_URL=https://marketplace.universaltill.com
UT_MARKETPLACE_CLIENT_ID=your-client-id
UT_MARKETPLACE_CLIENT_SECRET=your-client-secret
```

Then run with:
```bash
./scripts/dev.sh
```

### Method 2: Environment Variables (One-time)

```bash
# Mock marketplace
export UT_MARKETPLACE_ENDPOINT_URL=http://localhost:8082
export UT_MARKETPLACE_CLIENT_ID=pos-client
export UT_MARKETPLACE_CLIENT_SECRET=dev-secret

# Run
./bin/unitill-pos
```

### Method 3: Command Line (Quick Testing)

```bash
UT_MARKETPLACE_ENDPOINT_URL=http://localhost:8082 \
UT_MARKETPLACE_CLIENT_ID=pos-client \
UT_MARKETPLACE_CLIENT_SECRET=dev-secret \
./bin/unitill-pos
```

## Development Workflow

### Testing with Mock Marketplace

1. **Start mock marketplace:**
   ```bash
   go run scripts/mock-marketplace/main.go
   ```
   Mock marketplace runs on `:8082` with 3 sample plugins

2. **Start POS (in another terminal):**
   ```bash
   ./scripts/dev.sh
   ```
   POS runs on `:8080`, connects to mock on `:8082`

3. **Access:**
   - POS: http://localhost:8080
   - Plugin Store: http://localhost:8080/plugins/store
   - Mock Marketplace: http://localhost:8082

### Testing with Real Marketplace

1. **Update configuration:**
   ```bash
   # In pos.env.dev
   UT_MARKETPLACE_ENDPOINT_URL=http://localhost:8081  # or production URL
   ```

2. **Start your marketplace server on :8081**

3. **Start POS:**
   ```bash
   ./scripts/dev.sh
   ```

## OAuth2 Configuration

### Mock Marketplace (Development)

Uses simple client credentials flow:
- **Client ID**: `pos-client`
- **Client Secret**: `dev-secret`
- **Grant Type**: `client_credentials`
- **Token Endpoint**: `http://localhost:8082/oauth2/token`

### Production Marketplace

Contact marketplace provider for:
- Production Client ID
- Production Client Secret
- Token endpoint URL
- API documentation

## Troubleshooting

### "Connection Refused" Errors

```
dial tcp 127.0.0.1:8081: connect: connection refused
```

**Solutions:**
1. Check if marketplace is running: `nc -z localhost 8081`
2. Verify `UT_MARKETPLACE_ENDPOINT_URL` points to correct port
3. For mock testing, ensure mock is running on :8082
4. Check firewall/network settings

### Environment Not Loading

If POS doesn't pick up `pos.env.dev`:
```bash
# Load manually
export $(grep -v '^#' pos.env.dev | grep -v '^$' | xargs)
make build && ./bin/unitill-pos

# Or use helper script
./scripts/dev.sh
```

### Wrong Port Configuration

If you see `127.0.0.1:8081` in logs but want to use mock:
```bash
# Check what's loaded
echo $UT_MARKETPLACE_ENDPOINT_URL

# Should output: http://localhost:8082 (for mock)

# If not, reload environment
source <(grep -v '^#' pos.env.dev | grep -v '^$' | sed 's/^/export /')
```

## Port Assignments

| Service | Default Port | Configurable Via |
|---------|--------------|------------------|
| POS Application | 8080 | `UT_LISTEN_ADDR` |
| Production Marketplace | 8081 | N/A (external) |
| Mock Marketplace | 8082 | Hardcoded in `scripts/mock-marketplace/main.go` |

## Security Notes

- **Development**: Mock uses simple OAuth2 with hardcoded credentials
- **Production**: Use secure credentials, HTTPS, and proper OAuth2 flows
- **Never commit** production credentials to version control
- Use environment-specific `.env` files (`.gitignore`'d)

## Advanced: Dev Mode Override

Enable dev mode to use a local marketplace override:

```bash
UT_DEV_MODE=true
UT_MARKETPLACE_DEV_OVERRIDE_URL=http://localhost:9000
```

This overrides the production endpoint only when dev mode is active.
