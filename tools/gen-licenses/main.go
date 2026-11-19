package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const outFile = "web/public/licenses.txt"

func main() {
	check := flag.Bool("check", false, "exit 1 when "+outFile+" is stale instead of rewriting it")
	flag.Parse()
	if err := run(*check); err != nil {
		fmt.Fprintln(os.Stderr, "gen-licenses:", err)
		os.Exit(1)
	}
}

func run(check bool) error {
	gomod, err := os.ReadFile("go.mod")
	if err != nil {
		return fmt.Errorf("run from the repository root: %w", err)
	}
	mods := ParseRequires(string(gomod))
	dirs, err := moduleDirs()
	if err != nil {
		return err
	}
	text, err := Render(mods, func(m Module) ([]LicenceFile, error) {
		dir := dirs[m.Path]
		if dir == "" { // not in the module cache yet: the one network step
			if out, err := exec.Command("go", "mod", "download", m.Path+"@"+m.Version).CombinedOutput(); err != nil {
				return nil, fmt.Errorf("go mod download %s: %v: %s", m.Path, err, out)
			}
			var err error
			if dirs, err = moduleDirs(); err != nil {
				return nil, err
			}
			dir = dirs[m.Path]
		}
		return readFromDir(dir)
	})
	if err != nil {
		return err
	}
	files, err := vendoredFiles(vendorDir)
	if err != nil {
		return err
	}
	if unc, stale := UncoveredVendored(files, OtherComponents); len(unc) > 0 || len(stale) > 0 {
		return fmt.Errorf("components.go is out of step with %s: add a licence entry for %v; remove entries for %v", vendorDir, unc, stale)
	}
	other, err := RenderComponents(OtherComponents, readRepoFile)
	if err != nil {
		return err
	}
	text += other
	if check {
		have, err := os.ReadFile(outFile)
		if err != nil {
			return fmt.Errorf("%s is missing: run `make licenses`", outFile)
		}
		if !bytes.Equal(have, []byte(text)) {
			return fmt.Errorf("%s is stale (go.mod changed): run `make licenses` and commit the result", outFile)
		}
		fmt.Printf("%s is up to date (%d modules)\n", outFile, len(mods))
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(outFile), 0o755); err != nil {
		return err
	}
	return os.WriteFile(outFile, []byte(text), 0o644)
}

// moduleDirs maps module path to its source directory (the replace target
// for a replaced module), from the module cache.
func moduleDirs() (map[string]string, error) {
	out, err := exec.Command("go", "list", "-m", "-json", "all").Output()
	if err != nil {
		return nil, fmt.Errorf("go list -m: %w", err)
	}
	dirs := map[string]string{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var m struct {
			Path    string
			Dir     string
			Replace *struct{ Dir string }
		}
		if err := dec.Decode(&m); err != nil {
			return nil, err
		}
		d := m.Dir
		if m.Replace != nil && m.Replace.Dir != "" {
			d = m.Replace.Dir
		}
		dirs[m.Path] = strings.TrimSpace(d)
	}
	return dirs, nil
}
