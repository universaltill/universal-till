// Command release-notes-bundle writes release-notes.json, the release asset
// that lets a till show an incoming version's notes before installing it
// (ut-docs#3940): every web/release-notes/<locale>/v<X.Y.Z>.md, validated by
// the same rules the till applies when it loads the bundle.
//
//	go run ./scripts/release-notes-bundle -o build/release-notes.json
//
// -dir reads the notes from a directory instead of the copy embedded in this
// build (the e2e harness adds a synthetic newer release that way).
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/universaltill/universal-till/internal/releasenotes"
	"github.com/universaltill/universal-till/web"
)

func main() {
	out := flag.String("o", "", "output file (required)")
	dir := flag.String("dir", "", "read notes from this directory (default: the embedded web/release-notes)")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "usage: release-notes-bundle -o <file> [-dir <release-notes dir>]")
		os.Exit(2)
	}
	var (
		fsys fs.FS = web.ReleaseNotesFS
		root       = releasenotes.Root
	)
	if *dir != "" {
		fsys, root = os.DirFS(*dir), "."
	}
	b, err := bundle(fsys, root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d bytes)\n", *out, len(b))
}

// bundle serialises every <root>/<locale>/*.md note into the release asset
// (other files and nested directories are ignored, as releasenotes.Load
// ignores them), then loads the result back with releasenotes.LoadBundle — the
// exact code a till runs — so a release never ships a bundle no till can read.
// It lives in this tool rather than internal/releasenotes so the till binary
// carries no writer it never calls (guard-deadcode-baseline.sh).
func bundle(fsys fs.FS, root string) ([]byte, error) {
	locales, err := fs.ReadDir(fsys, root)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", root, err)
	}
	files := map[string]string{}
	for _, ld := range locales {
		if !ld.IsDir() {
			continue
		}
		dir := path.Join(root, ld.Name())
		entries, err := fs.ReadDir(fsys, dir)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			raw, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
			if err != nil {
				return nil, err
			}
			files[ld.Name()+"/"+e.Name()] = string(raw)
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", " ")
	doc := struct {
		Format int               `json:"format"`
		Files  map[string]string `json:"files"`
	}{releasenotes.BundleFormat, files}
	if err := enc.Encode(doc); err != nil { // map keys encode sorted: stable output
		return nil, err
	}
	if _, err := releasenotes.LoadBundle(buf.Bytes()); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
