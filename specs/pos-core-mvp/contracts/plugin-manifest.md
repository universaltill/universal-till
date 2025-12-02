# Contract: Plugin Manifest (pos-core-mvp)

This file defines the minimal manifest contract plugins must provide. The manifest is stored in `plugin_catalog` and verified at install time (SHA256).

Example manifest (JSON):

```json
{
  "id": "stripe-payment",
  "version": "1.0.0",
  "name": "Stripe Payment",
  "runtime": "local", 
  "entrypoint": "./stripe-plugin",
  "sha256": "...",
  "author": "Example Dev",
  "website": "https://example.com",
  "modes": {
    "local": {
      "requires": ["stripe_api_key"],
      "features": ["basic_payment", "refunds"]
    },
    "cloud": {
      "requires": ["ut_cloud_token"],
      "features": ["basic_payment","refunds","fraud_detection","auto_reconciliation"],
      "pricing": "included_in_cloud_plan"
    }
  }
}
```

Fields
- `id` (string): unique plugin id
- `version` (string): semver
- `name` (string): human name
- `runtime` (string): how to run (e.g., `local`, `docker`)
- `entrypoint` (string): command/binary path
- `sha256` (string): checksum of package/binary
- `modes` (object): describes `local` and optional `cloud` modes and their required settings

Install-time checks
- Verify `sha256` matches downloaded package.
- Validate `modes.local` exists and supports basic features required by host.
- Store manifest metadata in `plugin_catalog` and create `plugins` row on install.

Runtime expectations
- Host will spawn plugin processes for `local` mode using `runtime` and `entrypoint`.
- Plugin and host establish a versioned handshake over IPC/gRPC, exchange declared capabilities, and negotiate allowed hooks.

Failure modes
- If a plugin attempts a privileged action without a declared capability, host denies action and records an `audit_log` entry and returns a clear error to the caller.
