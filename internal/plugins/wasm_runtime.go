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

// SharedBus returns the singleton event bus for this process.
func SharedBus(db *sql.DB) *EventBus {
	sharedBusOnce.Do(func() { sharedBus = NewEventBus(db) })
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
		// verdict (docs: wasm-runtime.md payment authorization).
		for _, evName := range events {
			if strings.HasSuffix(evName, ".authorize") {
				bus.SetEventMode(evName, Blocking)
			}
		}
		pluginID := row.ID
		handle := func(hctx context.Context, ev Event) error {
			w.mu.Lock()
			stale := gen != w.unsubGen
			w.mu.Unlock()
			if stale {
				return nil
			}
			if err := w.HandleEvent(hctx, pluginID, ev); err != nil {
				logging.L().Errorf("wasm %s handling %s: %v", pluginID, ev.Type, err)
				return err
			}
			return nil
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
				_ = handle(context.Background(), ev)
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
// event JSON on stdin, deadline-bound, stdout/stderr captured.
func (w *WasmRuntime) HandleEvent(ctx context.Context, pluginID string, ev Event) error {
	w.mu.Lock()
	compiled, ok := w.modules[pluginID]
	timeout := w.timeout
	if w.hasNet[pluginID] {
		timeout = w.netTimeout // room for the http_request host call
	}
	db := w.db
	w.mu.Unlock()
	if !ok {
		return fmt.Errorf("module not loaded: %s", pluginID)
	}

	in, err := json.Marshal(map[string]any{
		"id":        ev.ID,
		"type":      ev.Type,
		"timestamp": ev.Timestamp.UTC().Format(time.RFC3339),
		"payload":   json.RawMessage(ev.Payload),
	})
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
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
		return fmt.Errorf("wasm handler: %w", runErr)
	}
	if out := strings.TrimSpace(stdout.String()); out != "" {
		logging.L().Infof("[wasm:%s] result: %s", pluginID, out)
	}
	return nil
}
