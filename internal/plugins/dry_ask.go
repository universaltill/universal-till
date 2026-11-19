package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"
)

// dryAskTimeout bounds the whole dry run (compile + one ask). The ask itself
// keeps its event-class deadline (2s for a tax.rate.ask); this only caps the
// compile of a large module on a slow device.
const dryAskTimeout = 15 * time.Second

// DryAskWasm loads the wasm module at wasmPath in a private, UNREGISTERED
// sandbox, sends it one ask of eventType with payload, and returns its raw
// answer (empty = it declined). The sandbox is a fresh WasmRuntime that is
// never Synced onto the shared event bus: the module subscribes to nothing,
// no handler is registered, no install or broken-state row is touched, and
// the runtime is closed before returning. It is the first-load check of
// ADR-0172 §3: a module that cannot load, traps, or times out returns an
// error, and the caller persists and activates nothing.
//
// db is only the host-function backing store (settings_get and friends for
// a plugin id that is not installed yet, so a fresh plugin sees its
// defaults); the dry run writes nothing of its own.
func DryAskWasm(ctx context.Context, db *sql.DB, pluginID, version, wasmPath, eventType string, payload any) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode dry ask payload: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, dryAskTimeout)
	defer cancel()

	w := NewWasmRuntime(filepath.Dir(wasmPath))
	w.mu.Lock()
	w.db = db
	w.mu.Unlock()
	defer func() {
		closeCtx := context.WithoutCancel(ctx)
		w.mu.Lock()
		rt := w.rt
		w.mu.Unlock()
		_ = rt.Close(closeCtx)
	}()

	if err := w.load(pluginID, version, wasmPath); err != nil {
		return nil, fmt.Errorf("load module: %w", err)
	}
	return w.HandleEvent(ctx, pluginID, Event{
		ID:        "first-load-check",
		Type:      eventType,
		Timestamp: time.Now(),
		Payload:   body,
	})
}
