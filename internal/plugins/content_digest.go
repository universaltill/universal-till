package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// ComputeContentDigest returns the ADR-0165 §2 content digest of the plugin
// package rooted at dir (ut-docs#4546): "sha256:" + hex(SHA-256(listing)),
// where the listing has one `<sha256 hex>  <path>\n` line per regular file
// except the root manifest.json, sorted by path in byte order. Paths are
// relative, `/`-separated and cleaned. Directories are not covered (an
// empty one changes nothing), nor are file modes.
//
// Any symlink or other non-regular entry, and any path with a newline, a
// backslash, a `..` segment or that is not clean, is refused. ut-cloud
// computes the same digest over the bundle's tar entries
// (internal/signing) — keep the two in step.
func ComputeContentDigest(dir string) (string, error) {
	type entry struct{ path, sum string }
	var entries []entry
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			if !d.IsDir() {
				return fmt.Errorf("package root %s is not a directory", dir)
			}
			return nil
		}
		slashed := filepath.ToSlash(rel)
		switch mode := d.Type(); {
		case mode&fs.ModeSymlink != 0:
			return fmt.Errorf("symlink not allowed in a plugin package: %q", slashed)
		case mode.IsDir():
			return validateContentPath(slashed)
		case !mode.IsRegular():
			return fmt.Errorf("not a regular file in a plugin package: %q (%s)", slashed, mode)
		}
		if err := validateContentPath(slashed); err != nil {
			return err
		}
		if slashed == "manifest.json" {
			return nil
		}
		sum, err := ComputeSHA256(p)
		if err != nil {
			return err
		}
		entries = append(entries, entry{path: slashed, sum: sum})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("content digest: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	h := sha256.New()
	for i, e := range entries {
		// Can't happen on one filesystem, but the rule is the marketplace's
		// too (ADR-0165 §2), so it holds here as well.
		if i > 0 && entries[i-1].path == e.path {
			return "", fmt.Errorf("content digest: duplicate path %q", e.path)
		}
		fmt.Fprintf(h, "%s  %s\n", e.sum, e.path)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// validateContentPath applies ADR-0165 §2's path rules to one slash path
// relative to the package root.
func validateContentPath(p string) error {
	switch {
	case p == "" || p == ".":
		return errors.New("empty path in a plugin package")
	case strings.ContainsAny(p, "\n\\"):
		return fmt.Errorf("path with a newline or backslash in a plugin package: %q", p)
	case path.IsAbs(p):
		return fmt.Errorf("absolute path in a plugin package: %q", p)
	case path.Clean(p) != p:
		return fmt.Errorf("unclean path in a plugin package: %q", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return fmt.Errorf("path leaves the plugin package: %q", p)
		}
	}
	return nil
}
