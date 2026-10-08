// Package mobilebench is the gomobile-bind entry point of the iOS bench app
// (tools/wasmbench/ios, ut-docs#3918): iOS can't exec a CLI, so the app runs
// the wasmbench measurement in its own process, through the same gomobile
// toolchain the till's iOS app ships with.
package mobilebench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/universaltill/universal-till/tools/wasmbench/bench"
)

// RunJSON benches every *.wasm file in dir (named after the file, without
// the extension) under the interpreter, wazero's only engine on iOS, and
// returns the report as JSON. A failure comes back as {"error": "..."}.
func RunJSON(dir string, n, calls int) string {
	rep, err := run(dir, n, calls)
	if err != nil {
		b, _ := json.Marshal(map[string]string{"error": err.Error()})
		return string(b)
	}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(b)
}

func run(dir string, n, calls int) (bench.Report, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.wasm"))
	if err != nil {
		return bench.Report{}, err
	}
	if len(paths) == 0 {
		return bench.Report{}, fmt.Errorf("no .wasm modules in %s", dir)
	}
	sort.Strings(paths)
	ctx := context.Background()
	var modules []bench.Module
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return bench.Report{}, err
		}
		m, err := bench.NewModule(ctx, strings.TrimSuffix(filepath.Base(p), ".wasm"), raw)
		if err != nil {
			return bench.Report{}, err
		}
		modules = append(modules, m)
	}
	return bench.Run(ctx, bench.Options{Engines: []string{"interpreter"}, N: n, Calls: calls}, modules), nil
}
