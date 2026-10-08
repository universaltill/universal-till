package bench

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/tetratelabs/wazero"
)

// Module is one guest to measure. A module exporting "handle" is benched as
// a reactor, any other as a command.
type Module struct {
	Name    string
	Raw     []byte
	Reactor bool
}

// NewModule detects the module's mode.
func NewModule(ctx context.Context, name string, raw []byte) (Module, error) {
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigInterpreter())
	defer rt.Close(ctx)
	compiled, err := rt.CompileModule(ctx, raw)
	if err != nil {
		return Module{}, fmt.Errorf("%s: %w", name, err)
	}
	_, ok := compiled.ExportedFunctions()["handle"]
	return Module{Name: name, Raw: raw, Reactor: ok}, nil
}

// Options: N timed instantiations per module (after 3 warm-up ones) and
// Calls timed reactor calls (after 10 warm-up ones). GOOS is the platform
// whose engines apply; empty means runtime.GOOS (tests set it).
type Options struct {
	Engines  []string
	N, Calls int
	GOOS     string
}

// Report is the JSON document architecture/wasm-runtime.md's raw result
// files hold.
type Report struct {
	Platform map[string]string `json:"platform"`
	Results  []map[string]any  `json:"results"`
}

// Run measures every module under every engine the platform supports.
func Run(ctx context.Context, o Options, modules []Module) Report {
	rep := Report{Platform: platform()}
	goos := o.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	for _, engine := range o.Engines {
		if engine == "compiler" && !compilerSupported(goos) {
			// wazero has no compiler for this GOOS and would fall back to
			// the interpreter; a "compiler" row would mislabel it.
			continue
		}
		for _, m := range modules {
			row, err := benchOne(ctx, engine, m, o.N, o.Calls)
			if err != nil {
				// A module that breaks the till's per-event deadline (or
				// answers wrongly) is a result, not a reason to drop the
				// other modules' numbers.
				row = map[string]any{"module": m.Name, "engine": engine, "error": err.Error()}
			}
			row["size_kb"] = len(m.Raw) / 1024
			rep.Results = append(rep.Results, row)
		}
	}
	return rep
}

func compilerSupported(goos string) bool { return goos != "android" && goos != "ios" }

func benchOne(ctx context.Context, engine string, m Module, n, calls int) (map[string]any, error) {
	row := map[string]any{"module": m.Name, "engine": engine}
	if m.Reactor {
		r, err := benchReactor(ctx, engine, m.Name, m.Raw, n+3, calls+10)
		if err != nil {
			return nil, err
		}
		row["mode"] = "reactor"
		row["compile_ms"] = millis(r.Compile)[0]
		row["instantiate_ms"] = summarize(millis(r.Instantiate[3:]...))
		row["call_ms"] = summarize(millis(r.Calls[10:]...))
		return row, nil
	}
	r, err := benchCommand(ctx, engine, m.Name, m.Raw, n+3)
	if err != nil {
		return nil, err
	}
	row["mode"] = "command"
	row["compile_ms"] = millis(r.Compile)[0]
	row["instantiate_ms"] = summarize(millis(r.Samples[3:]...))
	return row, nil
}

// platform records what each number must carry: device model, OS and
// wazero version.
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
	case "ios":
		// No exec on iOS: the app sandbox forbids spawning processes.
		p["model"], p["os"] = iosDevice()
	}
	return p
}
