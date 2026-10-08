package bench

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// event is what every timed call answers: the constant, network-free
// charge.policy.ask that ut-plugin-tax-uk also handles, so the real plugin
// and the bench guests do comparable work.
var event = []byte(`{"type":"charge.policy.ask","payload":{}}`)

// memoryLimitPages mirrors internal/plugins.wasmMemoryLimitPages so the bench
// runtime is configured like the till's.
const memoryLimitPages = 1024

// callDeadline mirrors the till's default per-event deadline: production
// always calls with a deadline context, which makes wazero start its
// close-on-cancel watcher for every call, so the bench does too.
const callDeadline = 2 * time.Second

// isAnswer reports whether out is what a handler returns: a JSON object.
// Anything else (an error message, empty output) must fail the run rather
// than be timed as a valid event.
func isAnswer(out []byte) bool {
	out = bytes.TrimSpace(out)
	return len(out) > 0 && out[0] == '{' && json.Valid(out)
}

func newRuntime(ctx context.Context, engine string) (wazero.Runtime, error) {
	var cfg wazero.RuntimeConfig
	switch engine {
	case "interpreter":
		cfg = wazero.NewRuntimeConfigInterpreter()
	case "compiler":
		cfg = wazero.NewRuntimeConfigCompiler()
	default:
		return nil, fmt.Errorf("unknown engine %q", engine)
	}
	rt := wazero.NewRuntimeWithConfig(ctx, cfg.WithCloseOnContextDone(true).WithMemoryLimitPages(memoryLimitPages))
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)
	if err := instantiateHostStubs(ctx, rt); err != nil {
		_ = rt.Close(ctx)
		return nil, err
	}
	return rt, nil
}

// instantiateHostStubs provides the "ut" imports a real plugin links against
// (ut-plugin-tax-uk needs these five to instantiate). The timed event calls
// none of them; each answers "not found".
func instantiateHostStubs(ctx context.Context, rt wazero.Runtime) error {
	b := rt.NewHostModuleBuilder("ut")
	b.NewFunctionBuilder().WithFunc(func(uint32, uint32) {}).Export("log_write")
	for _, name := range []string{"settings_get", "http_request", "storage_get", "storage_set"} {
		b.NewFunctionBuilder().WithFunc(func(uint32, uint32, uint32, uint32) int32 { return -1 }).Export(name)
	}
	_, err := b.Instantiate(ctx)
	return err
}

// CommandResult times a WASI command module: one instantiation per event,
// the model internal/plugins.WasmRuntime runs today.
type CommandResult struct {
	Name, Engine string
	Compile      time.Duration
	Samples      []time.Duration // instantiate + run + exit, per event
	Answer       []byte          // the last event's stdout
}

func benchCommand(ctx context.Context, engine, name string, raw []byte, n int) (*CommandResult, error) {
	rt, err := newRuntime(ctx, engine)
	if err != nil {
		return nil, err
	}
	defer rt.Close(ctx)

	start := time.Now()
	compiled, err := rt.CompileModule(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("compile %s: %w", name, err)
	}
	res := &CommandResult{Name: name, Engine: engine, Compile: time.Since(start)}

	for i := 0; i < n; i++ {
		var stdout, stderr bytes.Buffer
		cfg := wazero.NewModuleConfig().
			WithName("").
			WithStdin(bytes.NewReader(event)).
			WithStdout(&stdout).
			WithStderr(&stderr).
			WithArgs("plugin.wasm", "charge.policy.ask")
		cctx, cancel := context.WithTimeout(ctx, callDeadline)
		start := time.Now()
		mod, err := rt.InstantiateModule(cctx, compiled, cfg)
		elapsed := time.Since(start)
		cancel()
		// Kept in step with internal/plugins: under wazero v1.12 proc_exit(0)
		// already returns a nil error and a closed module, so teardown falls
		// inside the timed window and this ExitError(0) branch is defensive.
		var exit *sys.ExitError
		if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 0) {
			return nil, fmt.Errorf("%s event %d: %w", name, i, err)
		}
		if mod != nil {
			_ = mod.Close(ctx)
		}
		if !isAnswer(stdout.Bytes()) {
			return nil, fmt.Errorf("%s event %d: no answer on stdout (stderr: %q)", name, i, stderr.String())
		}
		res.Samples = append(res.Samples, elapsed)
		res.Answer = bytes.TrimSpace(stdout.Bytes())
	}
	return res, nil
}

// ReactorResult times a resident reactor: instantiation (runs _initialize)
// and then one exported call per event against the same instance.
type ReactorResult struct {
	Name, Engine string
	Compile      time.Duration
	Instantiate  []time.Duration
	Calls        []time.Duration // alloc + write + handle + read, per event
	Answer       []byte          // the last call's answer
}

func benchReactor(ctx context.Context, engine, name string, raw []byte, inst, calls int) (*ReactorResult, error) {
	return benchReactorWith(ctx, engine, name, raw, inst, calls, event)
}

func benchReactorWith(ctx context.Context, engine, name string, raw []byte, inst, calls int, ev []byte) (*ReactorResult, error) {
	if inst < 1 || calls < 1 {
		return nil, fmt.Errorf("%s: need at least one instantiation and one call", name)
	}
	rt, err := newRuntime(ctx, engine)
	if err != nil {
		return nil, err
	}
	defer rt.Close(ctx)

	start := time.Now()
	compiled, err := rt.CompileModule(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("compile %s: %w", name, err)
	}
	res := &ReactorResult{Name: name, Engine: engine, Compile: time.Since(start)}

	var mod api.Module
	for i := 0; i < inst; i++ {
		if mod != nil {
			_ = mod.Close(ctx)
		}
		cfg := wazero.NewModuleConfig().WithName("").WithStartFunctions("_initialize")
		start := time.Now()
		mod, err = rt.InstantiateModule(ctx, compiled, cfg)
		if err != nil {
			return nil, fmt.Errorf("%s instantiate %d: %w", name, i, err)
		}
		res.Instantiate = append(res.Instantiate, time.Since(start))
	}
	defer mod.Close(ctx)

	alloc, handle := mod.ExportedFunction("alloc"), mod.ExportedFunction("handle")
	if alloc == nil || handle == nil {
		return nil, fmt.Errorf("%s: reactor must export alloc and handle", name)
	}
	for i := 0; i < calls; i++ {
		cctx, cancel := context.WithTimeout(ctx, callDeadline)
		start := time.Now()
		answer, err := reactorCall(cctx, mod, alloc, handle, ev)
		elapsed := time.Since(start)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("%s call %d: %w", name, i, err)
		}
		if !isAnswer(answer) {
			return nil, fmt.Errorf("%s call %d: no answer", name, i)
		}
		res.Calls = append(res.Calls, elapsed)
		res.Answer = answer
	}
	return res, nil
}

func reactorCall(ctx context.Context, mod api.Module, alloc, handle api.Function, ev []byte) ([]byte, error) {
	r, err := alloc.Call(ctx, uint64(len(ev)))
	if err != nil {
		return nil, err
	}
	ptr := uint32(r[0])
	if !mod.Memory().Write(ptr, ev) {
		return nil, errors.New("event write out of range")
	}
	r, err = handle.Call(ctx, uint64(ptr), uint64(len(ev)))
	if err != nil {
		return nil, err
	}
	var packed [8]byte
	binary.BigEndian.PutUint64(packed[:], r[0])
	outPtr, outLen := binary.BigEndian.Uint32(packed[:4]), binary.BigEndian.Uint32(packed[4:])
	if outLen == 0 {
		return nil, nil
	}
	out, ok := mod.Memory().Read(outPtr, outLen)
	if !ok {
		return nil, errors.New("answer read out of range")
	}
	return append([]byte(nil), out...), nil
}

// Summary is in milliseconds.
type Summary struct {
	N                     int
	Min, Median, P95, Max float64
}

func summarize(ms []float64) Summary {
	s := append([]float64(nil), ms...)
	sort.Float64s(s)
	if len(s) == 0 {
		return Summary{}
	}
	at := func(q float64) float64 { return s[int(q*float64(len(s)-1)+0.5)] }
	return Summary{N: len(s), Min: s[0], Median: at(0.5), P95: at(0.95), Max: s[len(s)-1]}
}

func millis(ds ...time.Duration) []float64 {
	out := make([]float64, len(ds))
	for i, d := range ds {
		out[i] = float64(d.Microseconds()) / 1000
	}
	return out
}
