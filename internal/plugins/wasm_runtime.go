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
	hasTCP     map[string]bool // plugin id → granted tcp:* permission (raw device transport)
	db         *sql.DB         // for host functions; set by Sync
	baseDir    string
	unsubGen   int // bumped per sync so stale handlers no-op
	// wg tracks the per-plugin event-channel drainer goroutines Sync starts,
	// so Close can wait for them to exit at shutdown (ut-docs#380).
	wg sync.WaitGroup
}

// exportTimeout is the deadline granted to the export/report event class
// (ut-docs#221) instead of the blanket w.timeout every other ".ask" event
// uses. Gathering real sales/tax/payment data and building an actual export
// file is legitimately slower than a small value-returning hook like
// tax.rate.ask (which keeps its existing 2s timeout, unchanged by this).
const exportTimeout = 30 * time.Second

// importTimeout is the deadline granted to import.requested.ask
// (ut-docs#599, review finding F1): a guest streaming a staged file through
// import_file_read is bounded by DISK throughput, not the small
// value-returning-hook shape exportTimeout was sized for. The one real
// input this card exists for is a 270MB speedy kasse .bkp backup — at the
// ~10MB/s chunked-read throughput measured against the real WASM guest
// fixture in review, that alone is ~27s on fast dev hardware, "far worse
// on Pi-class" (till hardware). exportTimeout (30s) would have made the
// mechanism this card built practically unusable at the size it was built
// for — a guest killed by WithCloseOnContextDone mid-stream, not a clean
// "too large" rejection. A generous fixed floor, not a size-derived one:
// simpler, and the size cap (maxImportFileSize, import_dispatch.go) is
// what actually bounds worst case, not this deadline.
const importTimeout = 5 * time.Minute

// isExportClassEvent reports whether eventType is in the export/report
// event class that needs exportTimeout rather than the default deadline.
func isExportClassEvent(eventType string) bool {
	return eventType == "export.requested.ask"
}

// isImportClassEvent reports whether eventType is the import dispatch event
// that needs importTimeout rather than the default deadline (ut-docs#599).
func isImportClassEvent(eventType string) bool {
	return eventType == "import.requested.ask"
}

// timeoutFor picks the deadline HandleEvent gives eventType for pluginID:
// the net:* permission widening applies as before, then the export/report
// or import class floor is applied on top (never narrows a wider net
// timeout, only ensures a data-transfer-class event gets at least its own
// floor — import's floor is far larger than export's, so these are checked
// as two separate floors rather than one shared "data transfer class").
func (w *WasmRuntime) timeoutFor(pluginID, eventType string) time.Duration {
	w.mu.Lock()
	timeout := w.timeout
	if w.hasNet[pluginID] || w.hasTCP[pluginID] {
		timeout = w.netTimeout // room for the http_request / tcp_* host calls
	}
	w.mu.Unlock()
	if isExportClassEvent(eventType) && timeout < exportTimeout {
		timeout = exportTimeout
	}
	if isImportClassEvent(eventType) && timeout < importTimeout {
		timeout = importTimeout
	}
	return timeout
}

// NewWasmRuntime creates the runtime; baseDir is the plugin install root
// (e.g. ./data/plugins).
func NewWasmRuntime(baseDir string) *WasmRuntime {
	ctx := context.Background()
	// WithCloseOnContextDone(true) (ut-docs#504 review finding): wazero
	// disables this by default, meaning HandleEvent's per-call
	// context.WithTimeout (timeoutFor) was computed but never actually
	// enforced — a guest stuck in a CPU-bound loop (buggy or malicious
	// plugin.wasm) ran forever regardless of the deadline. That was always
	// latent, but ut-docs#504's fix (EventBus.publish holds eb.mu.RLock
	// across a Blocking handler's call, closing the shutdown/Reload
	// channel-close race) turned "one wedged handler hangs one publish
	// call" into "one wedged handler wedges the entire bus" — including
	// HasSubscribers/Generation on the checkout tax-rate-ask path
	// (internal/pages/tax_hook.go) and every future ResetSubscribers
	// (Manager.Reload/Close), since Go's RWMutex blocks new readers once a
	// writer is pending. Enabling this makes the timeout this code already
	// computes and applies actually terminate the guest module, bounding
	// that hold — matching wazero's own documented guidance for untrusted
	// guests.
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithCloseOnContextDone(true))
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
		hasTCP:     map[string]bool{},
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
			delete(w.hasTCP, id)
			// A dropped plugin must never leak an open device socket.
			tcpConns.CloseAll(id)
			// ... nor a staged import file — CloseAll here also removes
			// the temp file from disk (ut-docs#599).
			importFiles.CloseAll(id)
		}
	}
	for id := range active {
		w.hasNet[id] = pluginHasNetPermission(ctx, db, id)
		w.hasTCP[id] = pluginHasTCPPermission(ctx, db, id)
	}
	w.mu.Unlock()

	bus := SharedBus(db)
	bus.ResetSubscribers()

	// ut-docs#368: a load failure used to `continue` silently — a joined
	// replica whose plugins row arrived in the snapshot with no file on disk
	// looked healthy while tax silently fell back to base rates. Failures
	// are now tallied, flipped to install_state='broken' (visible on the
	// plugins page and to the tax fail-closed check), and flipped back to
	// 'installed' on a later successful load — self-heal must be visible.
	var loaded, failed []string
	stateChanged := false

	for _, row := range rows {
		if row.Runtime != "wasm" || !row.IsActive {
			continue
		}
		modPath := filepath.Join(w.baseDir, row.ID, row.Version,
			strings.TrimPrefix(row.Entrypoint, "./"))
		if err := w.load(row.ID, row.Version, modPath); err != nil {
			logging.L().Errorf("wasm load %s: %v", row.ID, err)
			failed = append(failed, row.ID)
			if markBroken(ctx, repo, row) {
				stateChanged = true
			}
			continue
		}
		loaded = append(loaded, row.ID)
		if row.InstallState == data.PluginStateBroken {
			if markHealed(ctx, repo, row) {
				stateChanged = true
			}
		}
		events, err := repo.ListPluginHookEvents(ctx, row.ID)
		if err != nil || len(events) == 0 {
			continue
		}
		// Authorization events run BLOCKING: the tender waits for the
		// verdict (docs: wasm-runtime.md payment authorization). ".ask"
		// events are the generic value-returning hook (EventBus.Ask) — also
		// blocking, since the caller is waiting on the plugin's answer.
		// ".refund" events block the same way: a refund's payment leg
		// (blockingPaymentEventWithResponse in internal/pages/refund_page.go)
		// waits on the plugin's decline/approve answer before letting the
		// refund proceed (ut-docs#434).
		for _, evName := range events {
			if strings.HasSuffix(evName, ".authorize") || strings.HasSuffix(evName, ".ask") || strings.HasSuffix(evName, ".refund") {
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
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			for ev := range ch {
				_, _ = handle(context.Background(), ev)
			}
		}()
		logging.L().Infof("wasm plugin %s loaded, handling %v", pluginID, events)
	}

	if stateChanged {
		// Install-state flips change what cached ".ask" answers (and the
		// tax fail-closed broken check, tax_hook.go) may assume — bump AFTER
		// the DB writes so no caller can cache pre-flip state under the
		// post-flip generation.
		bus.BumpGeneration()
	}
	if len(failed) > 0 {
		// Recovery differs by provenance (ut-docs#368 round-2 review): a
		// marketplace-installed plugin (it has an install-status record,
		// keyed by listing) is re-fetched automatically by the multi-till
		// sync loop, while a file-imported one has NO listing to re-fetch
		// from and stays broken until someone re-imports the plugin file on
		// THIS till — the tally must say which is which, not promise
		// self-heal for both.
		selfHeal, reimport := partitionBrokenByRecovery(ctx, db, failed)
		note := ""
		if len(selfHeal) > 0 {
			note += fmt.Sprintf("; %v will be re-fetched from the marketplace automatically by the sync loop", selfHeal)
		}
		if len(reimport) > 0 {
			// Number-agnostic phrasing: the slice may hold one ID or several,
			// so no bare plural verb ("have") that reads wrong for one.
			note += fmt.Sprintf("; %v: no marketplace listing to re-fetch from (file import) — re-import the plugin file on this till to recover", reimport)
		}
		logging.L().Errorf("wasm sync: %d plugins loaded, %d failed %v — failed plugins are marked 'broken' until their files are restored%s",
			len(loaded), len(failed), failed, note)
	} else if len(loaded) > 0 {
		logging.L().Infof("wasm sync: %d plugins loaded (0 failed)", len(loaded))
	}
}

// markBroken flips a wasm plugin whose module failed to load to
// install_state='broken' (ut-docs#368). It deliberately does NOT touch the
// plugin's install-status record: that record's State tracks the INSTALL
// lifecycle (Requested/Downloading/Installing/Active/Failed — Failed means
// "the install attempt itself failed", and the store page renders it as
// not-installed with a retry affordance), while a plugin that installed
// fine and later broke at LOAD time is still installed. Demoting the record
// to Failed also broke convergePluginSet's prune loop (sync_admin.go),
// which only prunes Active records — a broken plugin removed on the primary
// became permanently un-uninstallable on its replicas (round-2 review
// BLOCKER). plugins.install_state='broken' is the single source of truth
// for the broken condition: the plugins-page chip and the tax fail-closed
// check (HasBrokenActivePluginForEvent) both read it directly. Reports
// whether any state actually changed (an already-broken plugin is left
// alone).
func markBroken(ctx context.Context, repo *data.PluginRepo, row data.InstalledPluginRow) bool {
	if row.InstallState == data.PluginStateBroken {
		return false
	}
	if err := repo.UpdatePluginInstallState(ctx, row.ID, data.PluginStateBroken); err != nil {
		logging.L().Errorf("wasm sync: mark %s broken: %v", row.ID, err)
		return false
	}
	return true
}

// markHealed is markBroken's inverse: a previously-broken plugin whose
// module just loaded flips back to 'installed' — recovery must be as
// visible as the failure was. Like markBroken, it leaves the install-status
// record alone (markBroken never changed it).
func markHealed(ctx context.Context, repo *data.PluginRepo, row data.InstalledPluginRow) bool {
	if err := repo.UpdatePluginInstallState(ctx, row.ID, data.PluginStateInstalled); err != nil {
		logging.L().Errorf("wasm sync: heal %s: %v", row.ID, err)
		return false
	}
	logging.L().Infof("wasm sync: %s recovered — its module loaded again, install_state flipped back to 'installed'", row.ID)
	return true
}

// partitionBrokenByRecovery splits the failed plugin IDs by how they can
// recover: a plugin with an install-status record (marketplace install,
// keyed by listing) is re-fetched automatically by the sync loop; one
// without (file import) has no listing to re-fetch from and needs a manual
// re-import on this till. On a read error everything is reported in the
// self-heal bucket — the common case — rather than wrongly telling an
// operator to re-import a store plugin.
func partitionBrokenByRecovery(ctx context.Context, db *sql.DB, failed []string) (selfHeal, reimport []string) {
	records, err := NewInstallStatusStore(db).List(ctx)
	if err != nil {
		logging.L().Errorf("wasm sync: list install-status records: %v", err)
		return failed, nil
	}
	listed := map[string]bool{}
	for _, rec := range records {
		if rec.PluginID != "" {
			listed[rec.PluginID] = true
		}
	}
	for _, id := range failed {
		if listed[id] {
			selfHeal = append(selfHeal, id)
		} else {
			reimport = append(reimport, id)
		}
	}
	return selfHeal, reimport
}

// Close stops the per-plugin event-channel drainer goroutines Sync started —
// their channels are only ever closed by EventBus.ResetSubscribers, which
// nothing called at process shutdown before ut-docs#380, leaving each drainer
// blocked forever on `range ch` — and waits (bounded by ctx) for them to
// exit. In production SubscribeWithHandler/ResetSubscribers on the shared bus
// are used only by Sync, so resetting here is safe. A wedged drainer must not
// hang shutdown forever: on ctx expiry this logs loudly and returns anyway.
func (w *WasmRuntime) Close(ctx context.Context) {
	if w == nil {
		return
	}
	w.mu.Lock()
	db := w.db
	w.mu.Unlock()
	if db != nil {
		SharedBus(db).ResetSubscribers() // closes every open channel — drainers exit
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.wg.Wait()
	}()
	select {
	case <-done:
	case <-ctx.Done():
		// The internal wg.Wait goroutine is intentionally leaked — it only
		// blocks on Wait and exits whenever the wedged drainer eventually does.
		logging.L().Errorf("shutdown: wasm event drainer goroutines still running when shutdown context expired — continuing anyway")
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
		// ut-docs#606 item 2: Sync's own module-drop loop only force-closes
		// TCP handles for a plugin that went inactive (!active[id]) — an
		// updated-but-still-active plugin reaches this branch instead, and
		// until now its OLD compiled module's open sockets were never
		// force-closed, leaking (bounded at maxTCPHandlesPerPlugin) until the
		// plugin was next disabled or the process exited. A version bump
		// recompiles the module fresh, so the old module's handles are
		// meaningless to it regardless — close them here, at the one place
		// that actually knows a reload (not just a drop) just happened.
		tcpConns.CloseAll(pluginID)
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
// result. Non-".ask"/".authorize"/".refund" events (e.g. sale.completed)
// keep the original, unredacted format — out of scope for ut-docs#202.
// ".ask" events (the generic value-returning hook, EventBus.Ask) go through
// safeAskResultForLog: export.requested.ask can answer with a full
// exported dataset (base64 in content_b64), and that must never reach the
// log verbatim ("no secrets in logs", GDPR-adjacent given the
// customer-erasure endpoint sits right next to the export one).
// ".authorize" events (ut-docs#245) get the same treatment: payment
// authorization plugins answer these (PublishAuthorize/Blocking hooks —
// see Sync's ".authorize"/".ask" branch above), and their responses are
// arguably the most credential-adjacent plugin output in the system —
// transaction/auth tokens, card-present metadata, depending on the
// integration. ".refund" events (ut-docs#385) are the same blocking,
// value-returning hook shape published by blockingPaymentEventWithResponse
// (refund_page.go) for a refund's payment leg — a refund plugin's response
// is just as likely to carry a gateway transaction token as an authorize
// response, so it goes through the identical redaction path.
func wasmResultLogLine(pluginID, eventType, out string) string {
	if !strings.HasSuffix(eventType, ".ask") && !strings.HasSuffix(eventType, ".authorize") && !strings.HasSuffix(eventType, ".refund") {
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

// looksLikeSensitiveFieldName reports whether key conventionally names a
// base64-encoded blob or a credential-shaped value — export.requested.ask's
// content_b64, or a payment-authorize response's auth token/secret, and any
// future field following the same naming — redacted regardless of size.
// Byte-size alone doesn't catch every risk here: a SMALL export can still
// carry real customer PII (e.g. a name/email on one receipt line), and a
// SMALL token is exactly as much of a credential as a long one, so both are
// name-matched rather than relying on the size cap alone (ut-docs#202,
// widened for ".authorize" responses by ut-docs#245).
func looksLikeSensitiveFieldName(key string) bool {
	lower := strings.ToLower(key)
	for _, marker := range [...]string{"b64", "token", "secret"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// maxAskNestingDepth caps how many levels deep redactField recurses into a
// field's own nested objects/arrays before it stops descending and falls
// back to judging the field only by its own size/name. Payment-gateway SDKs
// nest a couple of levels (e.g. answer.provider.auth_token); this is
// generous headroom for that while still bounding recursion against a
// pathological or malformed plugin response.
const maxAskNestingDepth = 8

// safeAskResultForLog returns out unchanged when it's already small and
// every field is safe to log as-is; otherwise any field whose raw JSON
// value exceeds maxAskFieldBytes, or whose name looks sensitive (a base64
// blob or a token/secret — looksLikeSensitiveFieldName), is replaced with
// an "<omitted: N bytes>" placeholder — the event's shape and small fields
// (ok/error/message) stay visible without ever logging the risky value
// itself. This check applies recursively (ut-docs#384): a nested object or
// array-of-objects is walked the same way, so a credential the top level
// doesn't name directly — e.g. a payment-gateway response shaped like
// {"approved":true,"provider":{"auth_token":"…"}} — is still caught, not
// just a top-level field. Non-object output, or an object that's still too
// large after redaction, falls back to hard (rune-safe) truncation. Despite
// the name, this is also wasmResultLogLine's redaction path for
// ".authorize" responses (ut-docs#245) — kept as one shared function rather
// than a second near-duplicate, since both are the same "blocking, value-
// returning hook" shape.
func safeAskResultForLog(out string) string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &fields); err != nil {
		return truncateForLog(out)
	}

	redacted := false
	for k, v := range fields {
		if red, changed := redactField(k, v, 0); changed {
			fields[k] = red
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

// redactField judges a single field the same way safeAskResultForLog's
// original top-level loop did — oversized or sensitively-named by key — and
// additionally recurses into a nested JSON object or array-of-objects
// (up to maxAskNestingDepth) so a credential doesn't have to sit at the top
// level to be caught. key is "" for an array element, which
// looksLikeSensitiveFieldName never matches — array elements are still
// covered by the size check and by recursing into their own fields.
// Returns the (possibly unchanged) value and whether anything was redacted.
func redactField(key string, v json.RawMessage, depth int) (json.RawMessage, bool) {
	if len(v) > maxAskFieldBytes || looksLikeSensitiveFieldName(key) {
		placeholder, _ := json.Marshal(fmt.Sprintf("<omitted: %d bytes>", len(v)))
		return placeholder, true
	}
	if depth >= maxAskNestingDepth {
		// Fail closed, not open: a value still shaped like an object/array
		// this deep can't be inspected further, so redact it wholesale by
		// size rather than let it through un-redacted just because
		// recursion stopped looking. A scalar this deep (already proven
		// safe by the size/name check above) is left alone.
		if len(v) > 0 && (v[0] == '{' || v[0] == '[') {
			placeholder, _ := json.Marshal(fmt.Sprintf("<omitted: %d bytes>", len(v)))
			return placeholder, true
		}
		return v, false
	}

	var obj map[string]json.RawMessage
	if json.Unmarshal(v, &obj) == nil && obj != nil {
		changed := false
		for k, nested := range obj {
			if red, didChange := redactField(k, nested, depth+1); didChange {
				obj[k] = red
				changed = true
			}
		}
		if !changed {
			return v, false
		}
		if b, err := json.Marshal(obj); err == nil {
			return b, true
		}
		return v, false
	}

	var arr []json.RawMessage
	if json.Unmarshal(v, &arr) == nil && arr != nil {
		changed := false
		for i, elem := range arr {
			if red, didChange := redactField("", elem, depth+1); didChange {
				arr[i] = red
				changed = true
			}
		}
		if !changed {
			return v, false
		}
		if b, err := json.Marshal(arr); err == nil {
			return b, true
		}
		return v, false
	}

	// v isn't an object or array -- check whether it's a JSON *string* whose
	// own content is itself JSON, one encoding layer removed from the
	// object/array case above. Some payment/gateway SDKs proxy a raw
	// upstream response by stashing it as an embedded JSON string (e.g.
	// {"provider":"{\"auth_token\":\"…\"}"}) rather than a nested object,
	// and json.Unmarshal(v, &obj) fails for that shape (a string isn't a
	// map), so without this branch the recursion above never triggers and
	// the embedded credential reaches the log verbatim (ut-docs#393, one
	// encoding removed from ut-docs#384's nested-object fix).
	//
	// Deliberately recurse unconditionally here rather than pre-checking
	// the decoded shape (object/array vs. scalar) before descending:
	// review of an earlier draft found that gating on shape stops after
	// exactly one unwrap, so a credential wrapped in TWO layers of
	// JSON-string encoding (a string containing a string containing an
	// object -- plausible if a payload passes through more than one layer
	// of proxying/serialization) still leaked, well within maxAskFieldBytes
	// at every layer. Recursing unconditionally lets each layer's own
	// object/array/string/scalar branch decide what to do with its own
	// content, cascading through however many layers actually exist; a
	// scalar payload (a JSON string containing just "42" or "\"x\"") still
	// self-terminates as a no-op two calls deeper, at negligible extra
	// cost given the 200-byte field cap this only ever runs under.
	// maxAskNestingDepth bounds the whole chain against a pathological or
	// malformed plugin response, same as it already bounds object/array
	// recursion.
	var strVal string
	if json.Unmarshal(v, &strVal) == nil {
		inner := json.RawMessage(strVal)
		if !json.Valid(inner) {
			return v, false
		}
		if red, didChange := redactField(key, inner, depth+1); didChange {
			// Re-encode the redacted content back into a JSON string so the
			// field's outer type (string) is preserved for the caller.
			if reencoded, err := json.Marshal(string(red)); err == nil {
				return reencoded, true
			}
		}
	}

	return v, false
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
