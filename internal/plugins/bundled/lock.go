// Package bundled is the App Store build's first-party plugin set
// (ADR-0194, Apple guideline 2.5.2): the Stripe and SumUp payment plugins ship
// INSIDE the app and are switched on and off, because that channel may not
// download code. The set is exactly the plugins we own, pinned in
// bundled.lock.json (id, version, release, sha256 of the bundle, manifest and
// wasm). The wasm is not committed: scripts/ci/fetch-bundled-plugins.sh
// downloads the pinned release at build time and verifies it against the
// lock, and the `bundledplugins` build tag embeds it. At boot Sync verifies
// the embedded bytes against the lock compiled into the binary again before
// anything is persisted (ADR-0178 style), and never crashes on a mismatch.
//
// This package must never import net/http or the marketplace client: bundled
// code arrives by build, not by network (scripts/ci/guard-appstore-no-code-fetch.sh).
package bundled

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

//go:embed bundled.lock.json
var lockJSON []byte

// Entry is one pinned first-party plugin release.
type Entry struct {
	ID             string            `json:"id"`
	Version        string            `json:"version"`
	Repo           string            `json:"repo"`
	ReleaseTag     string            `json:"release_tag"`
	BundleName     string            `json:"bundle_name"`
	BundleSHA256   string            `json:"bundle_sha256"`
	ManifestSHA256 string            `json:"manifest_sha256"`
	WasmPath       string            `json:"wasm_path"`
	WasmSHA256     string            `json:"wasm_sha256"`
	Files          map[string]string `json:"files"` // other embedded files (locales) -> sha256
}

// Lock is the parsed bundled.lock.json.
type Lock struct {
	Schema  int     `json:"schema"`
	Plugins []Entry `json:"plugins"`
}

var (
	sha256Re  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	idRe      = regexp.MustCompile(`^com\.universaltill\.[a-z0-9][a-z0-9.-]*$`)
	versionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	repoRe    = regexp.MustCompile(`^universaltill/ut-plugin-[a-z0-9-]+$`)
	relPathRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*(/[A-Za-z0-9._-]+)*$`)
)

// ParseLock parses and validates a lock: first-party ids only, strict hex
// sha256 values, safe relative paths. A malformed lock is an error, never a
// partial set.
func ParseLock(raw []byte) (*Lock, error) {
	var l Lock
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, fmt.Errorf("bundled lock: %w", err)
	}
	if l.Schema != 1 {
		return nil, fmt.Errorf("bundled lock: unsupported schema %d", l.Schema)
	}
	seen := map[string]bool{}
	for _, e := range l.Plugins {
		if !idRe.MatchString(e.ID) {
			return nil, fmt.Errorf("bundled lock: %q is not a first-party plugin id", e.ID)
		}
		if seen[e.ID] {
			return nil, fmt.Errorf("bundled lock: %s listed twice", e.ID)
		}
		seen[e.ID] = true
		if !versionRe.MatchString(e.Version) {
			return nil, fmt.Errorf("bundled lock: %s: bad version %q", e.ID, e.Version)
		}
		if !repoRe.MatchString(e.Repo) {
			return nil, fmt.Errorf("bundled lock: %s: repo %q is not a universaltill plugin repo", e.ID, e.Repo)
		}
		if e.ReleaseTag != "v"+e.Version || e.BundleName == "" || strings.ContainsAny(e.BundleName, "/\\") {
			return nil, fmt.Errorf("bundled lock: %s: release_tag/bundle_name inconsistent", e.ID)
		}
		for name, h := range map[string]string{"bundle_sha256": e.BundleSHA256, "manifest_sha256": e.ManifestSHA256, "wasm_sha256": e.WasmSHA256} {
			if !sha256Re.MatchString(h) {
				return nil, fmt.Errorf("bundled lock: %s: %s is not a lowercase sha256", e.ID, name)
			}
		}
		if !relPathRe.MatchString(e.WasmPath) || strings.Contains(e.WasmPath, "..") {
			return nil, fmt.Errorf("bundled lock: %s: bad wasm_path %q", e.ID, e.WasmPath)
		}
		for f, h := range e.Files {
			if !relPathRe.MatchString(f) || strings.Contains(f, "..") || f == "manifest.json" || f == e.WasmPath {
				return nil, fmt.Errorf("bundled lock: %s: bad file %q", e.ID, f)
			}
			if !sha256Re.MatchString(h) {
				return nil, fmt.Errorf("bundled lock: %s: file %s: not a lowercase sha256", e.ID, f)
			}
		}
	}
	return &l, nil
}

// compiledLock is the lock baked into this binary. A malformed one yields an
// empty set (logged by Sync) so a bad edit cannot crash a boot.
func compiledLock() (*Lock, error) { return ParseLock(lockJSON) }
