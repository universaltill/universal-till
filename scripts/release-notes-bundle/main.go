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
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

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
	b, err := releasenotes.Bundle(fsys, root)
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
