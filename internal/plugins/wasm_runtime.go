package plugins

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// sharedBus is the process-wide event bus. Publishers and the wasm runtime
// must share one instance: subscriptions are in-memory, so a bus constructed
// per-publish can never have subscribers.
var (
	sharedBus     *EventBus
	sharedBusOnce sync.Once
)

// SharedBus returns the singleton event bus for this process, rebound to the
// caller's live db handle (one db per process in production; tests open and
// close several, and hook/permission checks must not hit a closed one).
func SharedBus(db *sql.DB) *EventBus {
	sharedBusOnce.Do(func() { sharedBus = NewEventBus(db) })
	if db != nil {
		sharedBus.SetDB(db)
	}
	return sharedBus
}

// WasmRuntime executes runtime:"wasm" plugins in-process (ADR-0001, spec in
// docs architecture/wasm-runtime.md). Modules are WASI commands: compiled
// once at load, instantiated per event with the event JSON on stdin.
type WasmRuntime struct {
	mu         sync.Mutex
	rt         wazero.Runtime
	modules    map[string]wazero.CompiledModule // plugin id → compiled module
	versions   map[string]string                // plugin id → compiled version
	timeout    time.Duration
	netTimeout time.Duration   // wider deadline for plugins holding net:*
	hasNet     map[string]bool // plugin id → granted net:* permission
	db         *sql.DB         // for host functions; set by Sync
	baseDir    string
	unsubGen   int // bumped per sync so stale handlers no-op
}

// exportTimeout is the deadline granted to the export/report event class
// (ut-docs#221) instead of the blanket w.timeout every other ".ask" event
// uses. Gathering real sales/tax/payment data and building an actual export
// file is legitimately slower than a small value-returning hook like
// tax.rate.ask (which keeps its existing 2s timeout, unchanged by this).
const exportTimeout = 30 * time.Second

// isExportClassEvent reports whether eventType is in the export/report
// event class that needs exportTimeout rather than the default deadline.
func isExportClassEvent(eventType string) bool {
	return eventType == "export.requested.ask"
}

// timeoutFor picks the deadline HandleEvent gives eventType for pluginID:
// the net:* permission widening applies as before, then the export/report
// class floor is applied on top (never narrows a wider net timeout, only
// ensures export-class events get at least exportTimeout).
func (w *WasmRuntime) timeoutFor(pluginID, eventType string) time.Duration {
	w.mu.Lock()
	timeout := w.timeout
	if w.hasNet[pluginID] {
		timeout = w.netTimeout // room for the http_request host call
	}
	w.mu.Unlock()
	if isExportClassEvent(eventType) && timeout < exportTimeout {
		timeout = exportTimeout
	}
	return timeout
}

// NewWasmRuntime creates the runtime; baseDir is the plugin install root
// (e.g. ./data/plugins).
func NewWasmRuntime(baseDir string) *WasmRuntime {
	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)
	if err := instantiateHostModule(ctx, rt); err != nil {
		// Modules that import "ut" will fail to instantiate; log, don't crash.
		logging.L().Errorf("wasm host module: %v", err)
	}
	return &WasmRuntime{
		rt:         rt,
		modules:    map[string]wazero.CompiledModule{},
		versions:   map[string]string{},
		timeout:    2 * time.Second,
		netTimeout: 10 * time.Second,
		hasNet:     map[string]bool{},
		baseDir:    baseDir,
	}
}

// Sync loads modules for the given active wasm plugins and subscribes each to
// the trigger_events its own entries declare. Called from Manager.Reload;
// failures are logged, never fatal — checkout must not depend on a plugin.
func (w *WasmRuntime) Sync(ctx context.Context, db *sql.DB) {
	if w == nil {
		return
	}
	repo := data.NewPluginRepo(db)
	rows, err := repo.ListInstalledPlugins(ctx)
	if err != nil {
		logging.L().Errorf("wasm sync: list plugins: %v", err)
		return
	}

	w.mu.Lock()
	w.unsubGen++
	gen := w.unsubGen
	w.db = db // host functions resolve storage/permissions through this
	// Drop compiled modules for plugins that are gone/disabled.
	active := map[string]bool{}
	for _, row := range rows {
		if row.Runtime == "wasm" && row.IsActive {
			active[row.ID] = true
		}
	}
	for id, mod := range w.modules {
		if !active[id] {
			_ = mod.Close(context.Background())
			delete(w.modules, id)
			delete(w.versions, id)
			delete(w.hasNet, id)
		}
	}
	for id := range active {
		w.hasNet[id] = pluginHasNetPermission(ctx, db, id)
	}
	w.mu.Unlock()

	bus := SharedBus(db)
	bus.ResetSubscribers()

	for _, row := range rows {
		if row.Runtime != "wasm" || !row.IsActive {
			continue
		}
		modPath := filepath.Join(w.baseDir, row.ID, row.Version,
			strings.TrimPrefix(row.Entrypoint, "./"))
		if err := w.load(row.ID, row.Version, modPath); err != nil {
			logging.L().Errorf("wasm load %s: %v", row.ID, err)
			continue
		}
		events, err := repo.ListPluginHookEvents(ctx, row.ID)
		if err != nil || len(events) == 0 {
			continue
		}
		// Authorization events run BLOCKING: the tender waits for the
		// verdict (docs: wasm-runtime.md payment authorization). ".ask"
		// events are the generic value-returning hook (EventBus.Ask) — also
		// blocking, since the caller is waiting on the plugin's answer.
		for _, evName := range events {
			if strings.HasSuffix(evName, ".authorize") || strings.HasSuffix(evName, ".ask") {
				bus.SetEventMode(evName, Blocking)
			}
		}
		pluginID := row.ID
		handle := func(hctx context.Context, ev Event) (json.RawMessage, error) {
			w.mu.Lock()
			stale := gen != w.unsubGen
			w.mu.Unlock()
			if stale {
				return nil, nil
			}
			resp, err := w.HandleEvent(hctx, pluginID, ev)
			if err != nil {
				logging.L().Errorf("wasm %s handling %s: %v", pluginID, ev.Type, err)
				return nil, err
			}
			return resp, nil
		}
		// The handler runs synchronously for Blocking events; for the default
		// non-blocking mode events land on the channel, so drain it too.
		ch, err := bus.SubscribeWithHandler(ctx, pluginID, events, handle)
		if err != nil {
			logging.L().Errorf("wasm subscribe %s: %v", pluginID, err)
			continue
		}
		go func() {
			for ev := range ch {
				_, _ = handle(context.Background(), ev)
			}
		}()
		logging.L().Infof("wasm plugin %s loaded, handling %v", pluginID, events)
	}
}

func (w *WasmRuntime) load(pluginID, version, path string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Same version already compiled → keep it. A DIFFERENT version means
	// the plugin was updated: recompile, or the till keeps running the old
	// code until restart (bug found live on the 1.0.0→1.1.0 update).
	if _, ok := w.modules[pluginID]; ok && w.versions[pluginID] == version {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read module: %w", err)
	}
	compiled, err := w.rt.CompileModule(context.Background(), raw)
	if err != nil {
		return fmt.Errorf("compile module: %w", err)
	}
	if old, ok := w.modules[pluginID]; ok {
		_ = old.Close(context.Background())
	}
	w.modules[pluginID] = compiled
	w.versions[pluginID] = version
	return nil
}

// HandleEvent runs one event through the plugin's module: fresh instance,
// event JSON on stdin, deadline-bound, stdout/stderr captured. The plugin's
// trimmed stdout is returned as-is — most handlers write nothing (or plain
// log text, which the caller happens to have already logged too); an "ask"
// style handler (EventBus.Ask) writes its JSON answer here instead, and
// interpreting that JSON is entirely up to that caller, not this generic
// runtime.
func (w *WasmRuntime) HandleEvent(ctx context.Context, pluginID string, ev Event) (json.RawMessage, error) {
	w.mu.Lock()
	compiled, ok := w.modules[pluginID]
	db := w.db
	w.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("module not loaded: %s", pluginID)
	}
	timeout := w.timeoutFor(pluginID, ev.Type)

	in, err := json.Marshal(map[string]any{
		"id":        ev.ID,
		"type":      ev.Type,
		"timestamp": ev.Timestamp.UTC().Format(time.RFC3339),
		"payload":   json.RawMessage(ev.Payload),
	})
	if err != nil {
		return nil, fmt.Errorf("encode event: %w", err)
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Host functions ("ut" module) resolve the caller through this state.
	cctx = withHostState(cctx, &hostState{pluginID: pluginID, db: db})

	var stdout, stderr bytes.Buffer
	cfg := wazero.NewModuleConfig().
		WithName(""). // anonymous: parallel instantiations must not collide
		WithStdin(bytes.NewReader(in)).
		WithStdout(&stdout).
		WithStderr(&stderr).
		WithArgs("plugin.wasm", ev.Type)

	_, runErr := w.rt.InstantiateModule(cctx, compiled, cfg)
	if out := strings.TrimSpace(stderr.String()); out != "" {
		logging.L().Infof("[wasm:%s] %s", pluginID, out)
	}
	if runErr != nil {
		// A WASI command exiting 0 surfaces as ExitError(0) — that's success.
		if exitErr, isExit := runErr.(*sys.ExitError); isExit && exitErr.ExitCode() == 0 {
			runErr = nil
		}
	}
	if runErr != nil {
		return nil, fmt.Errorf("wasm handler: %w", runErr)
	}
	out := strings.TrimSpace(stdout.String())
	if out != "" {
		logging.L().Infof("%s", wasmResultLogLine(pluginID, ev.Type, out))
	}
	return json.RawMessage(out), nil
}

// wasmResultLogLine builds the exact line logged for a handler's stdout
// result. Non-".ask" events (e.g. sale.completed) keep the original,
// unredacted format — out of scope for ut-docs#202. ".ask" events (the
// generic value-returning hook, EventBus.Ask) go through
// safeAskResultForLog: export.requested.ask can answer with a full
// exported dataset (base64 in content_b64), and that must never reach the
// log verbatim ("no secrets in logs", GDPR-adjacent given the
// customer-erasure endpoint sits right next to the export one).
func wasmResultLogLine(pluginID, eventType, out string) string {
	if !strings.HasSuffix(eventType, ".ask") {
		return fmt.Sprintf("[wasm:%s] result: %s", pluginID, out)
	}
	return fmt.Sprintf("[wasm:%s] result (%s, %d bytes): %s", pluginID, eventType, len(out), safeAskResultForLog(out))
}

// maxAskFieldBytes caps how much of a single JSON field's raw value in an
// ".ask" response reaches the log before safeAskResultForLog replaces it
// with a size-only placeholder. A handler like tax.rate.ask answers with a
// couple of small fields and is never affected.
const maxAskFieldBytes = 200

// maxAskLogBytes bounds the final logged line for an ".ask" result, after
// per-field redaction: covers a non-object/malformed answer, and a
// well-formed object that stays under maxAskFieldBytes on every individual
// field but sums well past anything useful for debugging (many small
// fields, or one very long key).
const maxAskLogBytes = 500

// looksLikeBlobFieldName reports whether key is conventionally a
// base64-encoded blob field (export.requested.ask's content_b64, and any
// future hook following the same naming) — redacted regardless of size.
// Byte-size alone doesn't catch every risk here: a SMALL export can still
// carry real customer PII (e.g. a name/email on one receipt line), so a
// small content_b64 must be redacted too, not just an oversized one.
func looksLikeBlobFieldName(key string) bool {
	return strings.Contains(strings.ToLower(key), "b64")
}

// safeAskResultForLog returns out unchanged when it's already small and
// every field is safe to log as-is; otherwise any field whose raw JSON
// value exceeds maxAskFieldBytes, or whose name looks like a base64 blob,
// is replaced with an "<omitted: N bytes>" placeholder — the event's shape
// and small fields (ok/error/message) stay visible without ever logging
// the risky value itself. Non-object output, or an object that's still too
// large after redaction, falls back to hard (rune-safe) truncation.
func safeAskResultForLog(out string) string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &fields); err != nil {
		return truncateForLog(out)
	}

	redacted := false
	for k, v := range fields {
		if len(v) > maxAskFieldBytes || looksLikeBlobFieldName(k) {
			placeholder, _ := json.Marshal(fmt.Sprintf("<omitted: %d bytes>", len(v)))
			fields[k] = placeholder
			redacted = true
		}
	}
	if !redacted && len(out) <= maxAskLogBytes {
		return out
	}
	b, err := json.Marshal(fields)
	if err != nil {
		return fmt.Sprintf("(redaction failed, %d bytes)", len(out))
	}
	return truncateForLog(string(b))
}

// truncateForLog hard-caps s at maxAskLogBytes, cutting at a valid UTF-8
// rune boundary so a multi-byte character (e.g. an RTL export filename)
// never splits into invalid UTF-8 in the log.
func truncateForLog(s string) string {
	if len(s) <= maxAskLogBytes {
		return s
	}
	cut := maxAskLogBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return fmt.Sprintf("%s...(truncated, %d bytes total)", s[:cut], len(s))
}
