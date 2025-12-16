# IPC Contract: Minimal Plugin IPC (POS Core MVP)

Purpose
- Define a minimal, well-scoped IPC contract for host ⇄ plugin communication used by integration tests and the MVP plugin surface.

Scope
- Simple request/response event delivery for event hooks (e.g., `sale.completed`).
- Acknowledgement model only (plugin returns ack/err). No streaming or complex RPCs in MVP.

Transport
- TCP socket (host opens and plugin connects), or plugin may open a listening TCP socket per manifest `entrypoint` semantics. For MVP tests we use TCP sockets with JSON messages.
- Messages are newline-delimited JSON for ease of testing.

Message Shapes
- Event delivery (host -> plugin)
  {
    "type": "event",
    "name": "sale.completed",
    "payload": { /* event-specific object */ }
  }

- Acknowledgement (plugin -> host)
  {
    "type": "ack",
    "status": "ok"
  }

- Error (plugin -> host)
  {
    "type": "ack",
    "status": "error",
    "message": "human-friendly error"
  }

Timeouts & retries
- Host should wait up to 2s for an ack before treating the plugin as unresponsive for the event and proceeding (logging + audit). Tests will use a 2s dial/ack timeout.

Security model
- MVP: no auth on IPC (local-only). Production: recommend signed manifests, socket permissions, or local TLS.

Best practices
- Plugins must never access host DB files directly. All host interactions must go through the documented IPC contract.
- The host is responsible for sanitizing payloads and enforcing capability checks before dispatching events.

Notes for integration tests
- Tests will start a plugin process (binary or `go run`) with an env var `PLUGIN_PORT` indicating where to connect for the test. The host test harness will connect, send an `event` JSON, and expect an `ack` JSON within 2s.
