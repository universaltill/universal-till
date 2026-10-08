// Command wasmbench measures what one WASM plugin event costs on a real
// device under the till's own wazero version and runtime settings
// (ut-docs#3153, ADR-0121 §8): per-event instantiation of a WASI command
// versus calls into a resident reactor, under the interpreter and the
// compiler. Results go into ut-docs architecture/wasm-runtime.md.
//
//	wasmbench [-engines interpreter,compiler] [-n 30] [-calls 1000] name=module.wasm ...
//
// A module exporting "handle" is benched as a reactor, any other as a command.
// The measurement lives in package bench, shared with the iOS bench app
// (ios/ + mobilebench, ut-docs#3918).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/universaltill/universal-till/tools/wasmbench/bench"
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
	var modules []bench.Module
	for _, arg := range flag.Args() {
		name, path, ok := strings.Cut(arg, "=")
		if !ok {
			fail(fmt.Errorf("argument %q: want name=module.wasm", arg))
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			fail(err)
		}
		m, err := bench.NewModule(ctx, name, raw)
		if err != nil {
			fail(err)
		}
		modules = append(modules, m)
	}
	report := bench.Run(ctx, bench.Options{Engines: strings.Split(*engines, ","), N: *n, Calls: *calls}, modules)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
		return
	}
	for k, v := range report.Platform {
		fmt.Printf("%-14s %s\n", k, v)
	}
	fmt.Printf("\n%-16s %-11s %-8s %7s %10s %28s %28s\n", "module", "engine", "mode", "size_kb", "compile_ms", "instantiate_ms p50/p95/max", "call_ms p50/p95/max")
	for _, r := range report.Results {
		if e, failed := r["error"]; failed {
			fmt.Printf("%-16s %-11s FAILED: %v\n", r["module"], r["engine"], e)
			continue
		}
		inst, call := r["instantiate_ms"].(bench.Summary), "-"
		if c, ok := r["call_ms"].(bench.Summary); ok {
			call = fmt.Sprintf("%.3f / %.3f / %.1f", c.Median, c.P95, c.Max)
		}
		fmt.Printf("%-16s %-11s %-8s %7d %10.1f %28s %28s\n", r["module"], r["engine"], r["mode"], r["size_kb"], r["compile_ms"],
			fmt.Sprintf("%.1f / %.1f / %.1f", inst.Median, inst.P95, inst.Max), call)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "wasmbench:", err)
	os.Exit(1)
}
