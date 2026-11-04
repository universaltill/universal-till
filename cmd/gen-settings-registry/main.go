// Command gen-settings-registry writes the till settings registry artefact
// (docs/till-settings-registry.json) that ut-cloud pins (ut-docs#5349,
// ADR-0175 amendment 13). Run it from the repo root via
// `make gen-settings-registry`; a data test fails when the committed file
// is stale.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/universaltill/universal-till/internal/data"
)

func main() {
	out := data.SettingsRegistryJSONPath
	if len(os.Args) > 1 {
		out = os.Args[1]
	} else if _, err := os.Stat("go.mod"); err != nil {
		fmt.Fprintln(os.Stderr, "gen-settings-registry: run from the repo root (make gen-settings-registry) or pass an output path")
		os.Exit(1)
	}
	b, err := data.SettingsRegistryJSON()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen-settings-registry:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "gen-settings-registry:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(out, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "gen-settings-registry:", err)
		os.Exit(1)
	}
}
