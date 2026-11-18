package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Component is a shipped piece of third-party software that is NOT a go.mod
// module: the Go runtime and standard library compiled into every binary, the
// front-end code vendored under web/public/vendor, and the category icon
// set. The list is maintained by hand and VERIFIED by -check: every file under
// web/public/vendor must be Covered by an entry, and every licence file must
// exist, so a newly vendored library cannot ship without a licence line here.
type Component struct {
	Name, Version, Source, Licence string
	// Covers lists the files under web/public/vendor this entry accounts for
	// (slash paths relative to web/public/vendor). Empty for non-vendored
	// components.
	Covers []string
	// LicenceFiles are repo-relative paths of the licence texts.
	LicenceFiles []string
}

const vendorDir = "web/public/vendor"

// OtherComponents is the maintained list. Versions and licences were checked
// against the shipped bytes: htmx.min.js and alpine.min.js are byte-identical
// (sha256) to npm htmx.org@1.9.12 dist/htmx.min.js and alpinejs@3.14.1
// dist/cdn.min.js; the other vendored files carry their own README and licence
// files in their directory.
var OtherComponents = []Component{
	{
		Name: "Go runtime and standard library", Version: "the Go toolchain the release is built with (go.mod)",
		Source: "https://go.dev", Licence: "BSD-3-Clause",
		LicenceFiles: []string{"tools/gen-licenses/other/go.LICENSE"},
	},
	{
		Name: "htmx", Version: "1.9.12", Source: "https://htmx.org", Licence: "Zero-Clause BSD (0BSD)",
		Covers:       []string{"htmx.min.js"},
		LicenceFiles: []string{"tools/gen-licenses/other/htmx.LICENSE"},
	},
	{
		Name: "Alpine.js", Version: "3.14.1", Source: "https://alpinejs.dev", Licence: "MIT",
		Covers:       []string{"alpine.min.js"},
		LicenceFiles: []string{"tools/gen-licenses/other/alpine.LICENSE"},
	},
	{
		Name: "barcode-detector (ponyfill)", Version: "3.2.2", Source: "npm barcode-detector, see web/public/vendor/barcode-detector/README.md", Licence: "MIT",
		Covers:       []string{"barcode-detector/ponyfill.js"},
		LicenceFiles: []string{vendorDir + "/barcode-detector/LICENSE.barcode-detector"},
	},
	{
		Name: "zxing-wasm", Version: "3.1.3", Source: "npm zxing-wasm, see web/public/vendor/barcode-detector/README.md", Licence: "MIT",
		Covers:       []string{"barcode-detector/zxing_reader.wasm"},
		LicenceFiles: []string{vendorDir + "/barcode-detector/LICENSE.zxing-wasm"},
	},
	{
		Name: "zxing-cpp (compiled into zxing_reader.wasm)", Version: "a17fd9dc65d6aa0dd2f660fdfca7a6a6613d938f", Source: "https://github.com/zxing-cpp/zxing-cpp", Licence: "Apache-2.0",
		LicenceFiles: []string{vendorDir + "/barcode-detector/LICENSE.zxing-cpp"},
	},
	{
		Name: "ONNX Runtime Web", Version: "1.30.0", Source: "npm onnxruntime-web, see web/public/vendor/bg-removal/README.md", Licence: "MIT",
		Covers:       []string{"bg-removal/ort.wasm.bundle.min.mjs", "bg-removal/ort-wasm-simd-threaded.wasm"},
		LicenceFiles: []string{vendorDir + "/bg-removal/LICENSE.onnxruntime-web"},
	},
	{
		Name: "U2-Net-P model weights (u2netp.onnx)", Version: "rembg model release v0.0.0", Source: "https://github.com/xuebinqin/U-2-Net", Licence: "Apache-2.0",
		Covers:       []string{"bg-removal/u2netp.onnx"},
		LicenceFiles: []string{vendorDir + "/bg-removal/LICENSE.u2net"},
	},
	{
		Name: "Lucide icons (category icons)", Version: "see internal/catimport/iconsrc/lucide", Source: "https://lucide.dev", Licence: "ISC",
		LicenceFiles: []string{"internal/catimport/iconsrc/lucide/LICENSE"},
	},
}

// vendoredFiles lists the shipped files under dir that need a licence line:
// everything except README.md and LICENSE* files. Slash paths relative to dir.
func vendoredFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		n := d.Name()
		if n == "README.md" || strings.HasPrefix(n, "LICENSE") {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return out, err
}

// UncoveredVendored returns vendored files no component accounts for, and
// Covers entries naming a file that no longer exists (stale manifest).
func UncoveredVendored(files []string, comps []Component) (uncovered, stale []string) {
	have := map[string]bool{}
	for _, f := range files {
		have[f] = true
	}
	covered := map[string]bool{}
	for _, c := range comps {
		for _, f := range c.Covers {
			covered[f] = true
			if !have[f] {
				stale = append(stale, f)
			}
		}
	}
	for _, f := range files {
		if !covered[f] {
			uncovered = append(uncovered, f)
		}
	}
	return uncovered, stale
}

// RenderComponents builds the "Other components" section. read returns a
// licence file's text; a missing file is an error.
func RenderComponents(comps []Component, read func(path string) (string, error)) (string, error) {
	var b strings.Builder
	b.WriteString("\n" + strings.Repeat("#", 72) + "\n")
	fmt.Fprintf(&b, "Other components (not Go modules): %d\n", len(comps))
	b.WriteString("Maintained in tools/gen-licenses/components.go and checked against web/public/vendor by `make licenses-check`.\n")
	b.WriteString("Not yet listed here: the Android (Gradle/AndroidX) and iOS native libraries of the mobile apps.\n")
	for _, c := range comps {
		b.WriteString("\n" + strings.Repeat("=", 72) + "\n")
		fmt.Fprintf(&b, "%s | %s | %s\n", c.Name, c.Version, c.Licence)
		fmt.Fprintf(&b, "Source: %s\n", c.Source)
		for _, p := range c.LicenceFiles {
			txt, err := read(p)
			if err != nil {
				return "", fmt.Errorf("licence file %s for %s: %w", p, c.Name, err)
			}
			fmt.Fprintf(&b, "%s\n%s [%s]\n%s\n", strings.Repeat("-", 72), c.Name, filepath.Base(p), strings.Repeat("-", 72))
			b.WriteString(strings.TrimSpace(strings.ReplaceAll(txt, "\r\n", "\n")) + "\n")
		}
	}
	return b.String(), nil
}

func readRepoFile(p string) (string, error) {
	b, err := os.ReadFile(p)
	return string(b), err
}
