// Command wasmbench measures what one WASM plugin event costs on a real
// device under the till's own wazero version and runtime settings
// (ut-docs#3153, ADR-0121 §8): per-event instantiation of a WASI command
// versus calls into a resident reactor, under the interpreter and the
// compiler. Results go into ut-docs architecture/wasm-runtime.md.
//
//	wasmbench [-engines interpreter,compiler] [-n 30] [-calls 1000] name=module.wasm ...
//
// A module exporting "handle" is benched as a reactor, any other as a command.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/tetratelabs/wazero"
)

func main() {
	engines := flag.String("engines", "interpreter,compiler", "comma-separated engines to run")
	n := flag.Int("n", 30, "timed instantiations per module (after 3 warm-up ones)")
	calls := flag.Int("calls", 1000, "timed reactor calls (after 10 warm-up ones)")
	asJSON := flag.Bool("json", false, "print one JSON document instead of a table")
	flag.Parse()
	if flag.NArg() == 0 || *n < 1 || *calls < 1 {
		flag.Usage()
		os.Exit(2)
	}

	ctx := context.Background()
	type module struct {
		name    string
		raw     []byte
		reactor bool
	}
	var modules []module
	for _, arg := range flag.Args() {
		name, path, ok := strings.Cut(arg, "=")
		if !ok {
			fail(fmt.Errorf("argument %q: want name=module.wasm", arg))
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			fail(err)
		}
		reactor, err := exportsHandle(ctx, raw)
		if err != nil {
			fail(err)
		}
		modules = append(modules, module{name, raw, reactor})
	}

	report := map[string]any{"platform": platform()}
	var rows []map[string]any
	for _, engine := range strings.Split(*engines, ",") {
		if engine == "compiler" && (runtime.GOOS == "android" || runtime.GOOS == "ios") {
			// wazero has no compiler for these GOOS values and would fall
			// back to the interpreter; a "compiler" row would mislabel it.
			continue
		}
		for _, m := range modules {
			row, err := benchOne(ctx, engine, m.name, m.raw, m.reactor, *n, *calls)
			if err != nil {
				// A module that breaks the till's per-event deadline (or
				// answers wrongly) is a result, not a reason to drop the
				// other modules' numbers.
				row = map[string]any{"module": m.name, "engine": engine, "error": err.Error()}
			}
			row["size_kb"] = len(m.raw) / 1024
			rows = append(rows, row)
		}
	}
	report["results"] = rows

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
		return
	}
	for k, v := range report["platform"].(map[string]string) {
		fmt.Printf("%-14s %s\n", k, v)
	}
	fmt.Printf("\n%-16s %-11s %-8s %7s %10s %28s %28s\n", "module", "engine", "mode", "size_kb", "compile_ms", "instantiate_ms p50/p95/max", "call_ms p50/p95/max")
	for _, r := range rows {
		if e, failed := r["error"]; failed {
			fmt.Printf("%-16s %-11s FAILED: %v\n", r["module"], r["engine"], e)
			continue
		}
		inst, call := r["instantiate_ms"].(Summary), "-"
		if c, ok := r["call_ms"].(Summary); ok {
			call = fmt.Sprintf("%.3f / %.3f / %.1f", c.Median, c.P95, c.Max)
		}
		fmt.Printf("%-16s %-11s %-8s %7d %10.1f %28s %28s\n", r["module"], r["engine"], r["mode"], r["size_kb"], r["compile_ms"],
			fmt.Sprintf("%.1f / %.1f / %.1f", inst.Median, inst.P95, inst.Max), call)
	}
}

func benchOne(ctx context.Context, engine, name string, raw []byte, reactor bool, n, calls int) (map[string]any, error) {
	row := map[string]any{"module": name, "engine": engine}
	if reactor {
		r, err := benchReactor(ctx, engine, name, raw, n+3, calls+10)
		if err != nil {
			return nil, err
		}
		row["mode"] = "reactor"
		row["compile_ms"] = millis(r.Compile)[0]
		row["instantiate_ms"] = summarize(millis(r.Instantiate[3:]...))
		row["call_ms"] = summarize(millis(r.Calls[10:]...))
		return row, nil
	}
	r, err := benchCommand(ctx, engine, name, raw, n+3)
	if err != nil {
		return nil, err
	}
	row["mode"] = "command"
	row["compile_ms"] = millis(r.Compile)[0]
	row["instantiate_ms"] = summarize(millis(r.Samples[3:]...))
	return row, nil
}

func exportsHandle(ctx context.Context, raw []byte) (bool, error) {
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigInterpreter())
	defer rt.Close(ctx)
	compiled, err := rt.CompileModule(ctx, raw)
	if err != nil {
		return false, err
	}
	_, ok := compiled.ExportedFunctions()["handle"]
	return ok, nil
}

// platform records what the AC asks each number to carry: device model, OS
// and wazero version.
func platform() map[string]string {
	p := map[string]string{
		"goos_goarch": runtime.GOOS + "/" + runtime.GOARCH,
		"go":          runtime.Version(),
		"cpus":        fmt.Sprint(runtime.NumCPU()),
		"wazero":      "unknown",
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == "github.com/tetratelabs/wazero" {
				p["wazero"] = d.Version
			}
		}
	}
	read := func(name string, args ...string) string {
		out, err := exec.Command(name, args...).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(strings.ReplaceAll(string(out), "\x00", ""))
	}
	switch runtime.GOOS {
	case "android":
		p["model"] = read("getprop", "ro.product.model") + " (" + read("getprop", "ro.soc.model") + ")"
		p["os"] = "Android " + read("getprop", "ro.build.version.release") + " (API " + read("getprop", "ro.build.version.sdk") + ")"
	case "linux":
		p["model"] = read("cat", "/proc/device-tree/model")
		p["os"] = read("sh", "-c", ". /etc/os-release && echo \"$PRETTY_NAME\"") + ", kernel " + read("uname", "-r")
	case "darwin":
		p["model"] = read("sysctl", "-n", "machdep.cpu.brand_string")
		p["os"] = "macOS " + read("sw_vers", "-productVersion")
	}
	return p
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "wasmbench:", err)
	os.Exit(1)
}
