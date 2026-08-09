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
	// Shutdown support (ut-docs#380). drainWg tracks the per-plugin
	// event-channel drainer goroutines Sync spawns: Add(1) under w.mu
	// (guarded by !closed) before each spawn, Done() as the drainer's first
	// defer. Across repeated live Syncs the accounting stays correct on its
	// own: ResetSubscribers closes the old generation's channels (their
	// drainers exit and Done), new drainers Add for the new generation, and
	// nothing Waits in between. bus is the shared EventBus of the last
	// Sync, kept so Shutdown can close every subscriber channel. closed
	// flips once in Shutdown; every Add is guarded by it under w.mu, so no
	// Add can start after Shutdown's Wait has begun (the WaitGroup
	// "Add-after-Wait-observes-zero" rule).
	drainWg sync.WaitGroup
	bus     *EventBus
	closed  bool
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
	// A Sync after Shutdown would re-subscribe channels nothing will ever
	// drain again. Best-effort early exit; the authoritative guard is the
	// per-spawn closed check further down, under the same w.mu Shutdown
	// flips closed under.
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()
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
	w.mu.Lock()
	w.bus = bus // remembered so Shutdown can close the subscriptions
	w.mu.Unlock()
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
		// The Add is guarded by closed under w.mu (see drainWg's doc): a
		// Sync racing Shutdown must not register a drainer after Shutdown
		// has started waiting for them. The subscription above already
		// exists in the bus at this point with nothing left to drain it —
		// harmless: Publish's non-blocking path just audits a "dropped"
		// send on a full buffer rather than panicking or hanging, and the
		// process is on its way out anyway.
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			continue
		}
		w.drainWg.Add(1)
		w.mu.Unlock()
		go func() {
			defer w.drainWg.Done()
			for ev := range ch {
				_, _ = handle(context.Background(), ev)
			}
		}()
		logging.L().Infof("wasm plugin %s loaded, handling %v", pluginID, events)
	}
}

// Shutdown stops the per-plugin event-channel drainer goroutines Sync
// spawned and waits — bounded — for them to exit (ut-docs#380). Their
// channels are otherwise only closed by the NEXT Sync/reload's
// ResetSubscribers, never at process end, so without this every drainer
// leaked forever on shutdown, invisible to app.Run's drain.
//
// Unlike every other background service app.Run joins, this is NOT called
// as a wg member racing bgCtx.Done(): ResetSubscribers here closes the same
// subscriber channels EventBus.publish sends on, and publish releases its
// read lock before that send — closing the channel while a publisher (an
// in-flight checkout, a live cloudsync tick) could still be mid-send is a
// real "send on closed channel" panic (confirmed during review, not
// hypothetical). app.Run instead calls this from its own deferred cleanup,
// AFTER drainBackgroundServices has already joined every wg-registered
// publisher — see app.go's Run for the exact sequencing and its caveat
// about what happens if that drain itself times out.
//
// timeout is a parameter (not a constant read here) so tests can exercise
// the timeout branch without a real multi-second wait.
func (w *WasmRuntime) Shutdown(timeout time.Duration) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.closed = true // no new drainers may be registered from here on
	bus := w.bus
	w.mu.Unlock()
	if bus != nil {
		// Closes every subscriber channel (see EventBus.ResetSubscribers),
		// which ends each drainer's `for ev := range ch` loop.
		bus.ResetSubscribers()
	}

	// Bounded join, mirroring app.drainBackgroundServices: a wedged drainer
	// (a handler that never returns) is a bug to fix, not a reason to never
	// shut down — log loudly and return anyway. The inner Wait goroutine is
	// intentionally leaked on timeout (harmless; it exits whenever the
	// wedged drainer does).
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.drainWg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		logging.L().Errorf("wasm shutdown: event drainer goroutines still running %s after unsubscribe — continuing shutdown anyway", timeout)
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
