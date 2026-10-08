package releasenotes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
)

// The release-notes bundle (ut-docs#3940): every release attaches
// release-notes.json — all of web/release-notes as {"format":1,"files":
// {"<locale>/v<X.Y.Z>.md": "<raw file>"}} — so a till can show the notes of
// a version it is about to install, which by definition are not embedded in
// the binary it is running. The bundle is parsed by the same strict rules as
// the embedded notes (build), plus the shape checks a downloaded file needs.

// BundleFormat is the only bundle format this build reads.
const BundleFormat = 1

// maxBundleEntries bounds a downloaded bundle (locales × releases); far above
// any real release (a handful of locales × a few hundred versions).
const maxBundleEntries = 5000

type bundleDoc struct {
	Format int               `json:"format"`
	Files  map[string]string `json:"files"`
}

// Bundle serialises every note under root into the release asset. It refuses
// notes Load would refuse, so a release never ships a bundle no till can read.
func Bundle(fsys fs.FS, root string) ([]byte, error) {
	files, err := readSources(fsys, root)
	if err != nil {
		return nil, err
	}
	if _, err := build(files); err != nil {
		return nil, err
	}
	doc := bundleDoc{Format: BundleFormat, Files: make(map[string]string, len(files))}
	for _, f := range files {
		doc.Files[f.path()] = string(f.raw)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", " ")
	if err := enc.Encode(doc); err != nil { // map keys encode sorted: stable output
		return nil, err
	}
	return buf.Bytes(), nil
}

// LoadBundle parses a downloaded release-notes.json. It fails closed on any
// malformed input: unknown format, an unexpected field, trailing data, a key
// that is not exactly <locale>/<file>.md, too many entries, or any note the
// embedded loader would refuse.
func LoadBundle(b []byte) (*Library, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var doc bundleDoc
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("releasenotes: bundle: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("releasenotes: bundle: trailing data after the document")
	}
	if doc.Format != BundleFormat {
		return nil, fmt.Errorf("releasenotes: bundle: format %d, want %d", doc.Format, BundleFormat)
	}
	if len(doc.Files) == 0 {
		return nil, fmt.Errorf("releasenotes: bundle: no notes")
	}
	if len(doc.Files) > maxBundleEntries {
		return nil, fmt.Errorf("releasenotes: bundle: %d entries, more than %d", len(doc.Files), maxBundleEntries)
	}
	keys := make([]string, 0, len(doc.Files))
	for k := range doc.Files {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic error messages
	files := make([]sourceFile, 0, len(keys))
	for _, k := range keys {
		locale, name, ok := strings.Cut(k, "/")
		if !ok || strings.ContainsAny(name, `/\`) || strings.Contains(locale, `\`) ||
			name == "" || name == "." || name == ".." || !strings.HasSuffix(name, ".md") {
			return nil, fmt.Errorf("releasenotes: bundle: entry %q must be <locale>/v<MAJOR>.<MINOR>.<PATCH>.md", k)
		}
		// build checks the locale (localeRe: no dots, no slashes) and the
		// filename (v<X.Y.Z>.md) for both loaders.
		files = append(files, sourceFile{locale: locale, name: name, raw: []byte(doc.Files[k])})
	}
	return build(files)
}

// Incoming returns the notes for every version a till on running would
// receive by installing offered — running < v <= offered — newest first, in
// locale with the usual per-note English fallback. A running build that is
// not a plain release (dev, a nightly) gets just the offered version's note.
func (l *Library) Incoming(locale, running, offered string) []*Note {
	top := Tag(offered)
	if top == "" {
		return nil
	}
	from := Tag(running)
	if from == "" {
		if n, ok := l.Get(locale, top); ok {
			return []*Note{n}
		}
		return nil
	}
	var out []*Note
	for _, v := range l.versions { // newest first
		if semverLess(top, v) {
			continue // newer than what is offered
		}
		if !semverLess(from, v) {
			break // at or below the running version
		}
		if n, ok := l.Get(locale, v); ok {
			out = append(out, n)
		}
	}
	return out
}
