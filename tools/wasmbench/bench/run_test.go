package bench

import (
	"context"
	"encoding/json"
	"testing"
)

// Run is what both the CLI and the in-app iOS bench (mobilebench) call, so
// one module set must come back as one row per engine and module, with the
// platform facts the wasm-runtime.md tables need.
func TestRunReportsEveryModuleAndPlatform(t *testing.T) {
	ctx := context.Background()
	cmd, err := NewModule(ctx, "go-command", buildGuest(t, "command"))
	if err != nil {
		t.Fatal(err)
	}
	re, err := NewModule(ctx, "go-reactor", buildGuest(t, "reactor", "-buildmode=c-shared"))
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Reactor || !re.Reactor {
		t.Fatalf("mode detection: command reactor=%v, reactor reactor=%v", cmd.Reactor, re.Reactor)
	}
	rep := Run(ctx, Options{Engines: []string{"interpreter"}, N: 2, Calls: 5}, []Module{cmd, re})
	if len(rep.Results) != 2 {
		t.Fatalf("want 2 rows, got %d: %+v", len(rep.Results), rep.Results)
	}
	for _, r := range rep.Results {
		if e, failed := r["error"]; failed {
			t.Fatalf("%v failed: %v", r["module"], e)
		}
	}
	if rep.Results[1]["mode"] != "reactor" || rep.Results[1]["call_ms"] == nil {
		t.Fatalf("reactor row lacks call timings: %+v", rep.Results[1])
	}
	for _, k := range []string{"goos_goarch", "go", "cpus", "wazero"} {
		if rep.Platform[k] == "" {
			t.Errorf("platform %q missing", k)
		}
	}
	// The JSON shape is what ut-docs architecture/wasm-runtime.md's raw
	// result files already use: {"platform":{...},"results":[...]}.
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(b, &shape); err != nil || shape["platform"] == nil || shape["results"] == nil {
		t.Fatalf("JSON shape changed: %s", b)
	}
}

// A compiler row on a GOOS wazero can't compile for would silently be an
// interpreter run under the wrong label.
func TestRunSkipsCompilerWhereUnsupported(t *testing.T) {
	if !compilerSupported("linux") || compilerSupported("ios") || compilerSupported("android") {
		t.Fatal("compilerSupported is wrong")
	}
}

// silentCommand is a WASI command whose _start returns without writing an
// answer: the smallest module that must come back as an error row.
var silentCommand = []byte{
	0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00, // magic, version
	0x01, 0x04, 0x01, 0x60, 0x00, 0x00, // type: () -> ()
	0x03, 0x02, 0x01, 0x00, // func 0 has type 0
	0x07, 0x0a, 0x01, 0x06, '_', 's', 't', 'a', 'r', 't', 0x00, 0x00, // export _start
	0x0a, 0x04, 0x01, 0x02, 0x00, 0x0b, // body: end
}

// A module that fails is an error row carrying its size, and the other
// modules' rows are still measured.
func TestRunKeepsOtherModulesWhenOneFails(t *testing.T) {
	ctx := context.Background()
	bad, err := NewModule(ctx, "silent", silentCommand)
	if err != nil {
		t.Fatal(err)
	}
	good, err := NewModule(ctx, "go-command", buildGuest(t, "command"))
	if err != nil {
		t.Fatal(err)
	}
	rep := Run(ctx, Options{Engines: []string{"interpreter"}, N: 1, Calls: 1}, []Module{bad, good})
	if len(rep.Results) != 2 {
		t.Fatalf("want 2 rows, got %+v", rep.Results)
	}
	if rep.Results[0]["error"] == nil || rep.Results[0]["size_kb"] == nil {
		t.Fatalf("failing module needs an error row with its size: %+v", rep.Results[0])
	}
	if rep.Results[1]["error"] != nil || rep.Results[1]["size_kb"] == nil {
		t.Fatalf("the other module must still be measured: %+v", rep.Results[1])
	}
}

// Run itself must drop compiler rows on a GOOS without wazero's compiler.
func TestRunDropsCompilerRowsOnIOS(t *testing.T) {
	ctx := context.Background()
	m, err := NewModule(ctx, "silent", silentCommand)
	if err != nil {
		t.Fatal(err)
	}
	rep := Run(ctx, Options{Engines: []string{"interpreter", "compiler"}, N: 1, Calls: 1, GOOS: "ios"}, []Module{m})
	if len(rep.Results) != 1 || rep.Results[0]["engine"] != "interpreter" {
		t.Fatalf("want one interpreter row, got %+v", rep.Results)
	}
}
