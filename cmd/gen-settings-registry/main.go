// Command gen-settings-registry writes the till settings registry artefact
// (docs/till-settings-registry.json) and the shop-template classes
// artefact (docs/shop-template-classes.json) that ut-cloud pins (ut-docs#5349,
// ADR-0175 amendment 13). Run it from the repo root via
// `make gen-settings-registry`; a data test fails when the committed file
// is stale.
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	out := settingsRegistryJSONPath
	if len(os.Args) > 1 {
		out = os.Args[1]
	} else if _, err := os.Stat("go.mod"); err != nil {
		fmt.Fprintln(os.Stderr, "gen-settings-registry: run from the repo root (make gen-settings-registry) or pass an output path")
		os.Exit(1)
	}
	b, err := settingsRegistryJSON()
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
	if len(os.Args) > 1 {
		return // explicit registry path: the classes artefact is repo-root only
	}
	if err := writeClassesJSON(shopTemplateClassesJSONPath); err != nil {
		fmt.Fprintln(os.Stderr, "gen-settings-registry:", err)
		os.Exit(1)
	}
}

// writeClassesJSON renders the classes artefact and writes it to path,
// creating the directory first.
func writeClassesJSON(path string) error {
	cb, err := shopTemplateClassesJSON()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, cb, 0o644)
}
