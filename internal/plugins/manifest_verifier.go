package plugins

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/logging"
)

// ErrManifestUnsigned is wrapped into VerifyManifest's returned error when a
// public key is configured but the manifest carries no signature — the one
// validation failure with an actionable, distinct fix (get a signed bundle)
// rather than "the file is malformed." Callers use errors.Is against this to
// show operators that specific reason instead of a generic import failure
// (ut-docs#2132).
var ErrManifestUnsigned = errors.New("plugin manifest is unsigned")

// ErrNoMarketplaceKey is wrapped into the error when a plugin is refused
// because the till has no marketplace signing key to verify it with
// (ADR-0158): without a key no plugin runs, unless the binary was built with
// `-tags devplugins`.
var ErrNoMarketplaceKey = errors.New("no marketplace signing key configured")

// ManifestVerifier validates plugin manifests and artifact signatures
type ManifestVerifier struct {
	publicKey ed25519.PublicKey
	// allowUnsigned: with no key, check structure only. Set from
	// allowUnsignedPlugins (true only in a -tags devplugins build, ADR-0158).
	allowUnsigned bool
}

func (mv *ManifestVerifier) HasPublicKey() bool {
	return mv != nil && len(mv.publicKey) == ed25519.PublicKeySize
}

// VerificationResult contains the outcome of manifest verification
type VerificationResult struct {
	Manifest          *Manifest
	ChecksumVerified  bool
	SignatureVerified bool
	Errors            []string
}

// NewManifestVerifier creates a new manifest verifier.
// publicKeyHex is the marketplace's Ed25519 public key in hex format, as the
// caller's effective config holds it (env or persisted/fetched key).
//
// ADR-0158: when a key is compiled into the binary
// (buildinfo.MarketplacePublicKey) it is used instead, whatever the caller
// passed, and an invalid compiled key is an error. With no key at all the
// verifier refuses every manifest (ErrNoMarketplaceKey), except in a
// `-tags devplugins` build, where it checks structure only.
func NewManifestVerifier(publicKeyHex string) (*ManifestVerifier, error) {
	if compiled := buildinfo.MarketplacePublicKey; compiled != "" {
		if publicKeyHex != "" && !strings.EqualFold(publicKeyHex, compiled) {
			warnIgnoredKeyOnce.Do(func() {
				logging.L().Warnf("[Verifier] configured marketplace public key differs from the compiled-in key; using the compiled key (ADR-0158)")
			})
		}
		mv, err := parseManifestVerifierKey(compiled)
		if err != nil {
			return nil, fmt.Errorf("compiled marketplace public key: %w", err)
		}
		return mv, nil
	}
	if publicKeyHex == "" {
		return &ManifestVerifier{allowUnsigned: allowUnsignedPlugins}, nil
	}
	return parseManifestVerifierKey(publicKeyHex)
}

// warnIgnoredKeyOnce limits the "configured key ignored" warning to one log
// line per process: NewManifestVerifier runs on every plugin action.
var warnIgnoredKeyOnce sync.Once

func parseManifestVerifierKey(publicKeyHex string) (*ManifestVerifier, error) {
	pubKeyBytes, err := hex.DecodeString(publicKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid public key hex: %w", err)
	}

	if len(pubKeyBytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key size: expected %d, got %d", ed25519.PublicKeySize, len(pubKeyBytes))
	}

	return &ManifestVerifier{
		publicKey: ed25519.PublicKey(pubKeyBytes),
	}, nil
}

// VerifyArtifact verifies a downloaded plugin artifact against its expected
// checksum.
//
// Called from installBundleFile (installer_marketplace.go), the function
// shared by both the direct marketplace-install path and the staged
// download->install path (DownloadToStore -> GetStoreDownload ->
// InstallFromStore), immediately before extraction. For the direct path
// this is a cheap, redundant defense-in-depth re-check — DownloadManager.
// Download (download_manager.go) already hashed the bytes as they streamed
// into the .part file and fails closed with errChecksumMismatch before
// anything is extracted. For the staged path it is load-bearing: the
// bundle may have sat on disk since download with only an os.Stat
// re-check, so this is what actually catches a file swapped in place
// before install (ut-docs#2241 — previously an unwired duplicate of
// Download's own check, flagged but left in place by ut-docs#1566's
// `internal/plugins` dead-code slice for exactly this follow-up).
func (mv *ManifestVerifier) VerifyArtifact(artifactPath, expectedChecksum string) error {
	log := logging.L()

	actualChecksum, err := ComputeSHA256(artifactPath)
	if err != nil {
		// ComputeSHA256 wraps an open failure as "open file: %w" and a read
		// failure as "hash file: %w" (see its own doc comment/tests) — key
		// off that stable prefix, not a fresh os.Stat, so every open
		// failure (missing file, permission denied, too many open files,
		// ...) maps back to VerifyArtifact's own pre-existing "failed to
		// open artifact" wording, not just the not-found case a Stat retry
		// would happen to reproduce.
		if strings.HasPrefix(err.Error(), "open file:") {
			return fmt.Errorf("failed to open artifact: %w", errors.Unwrap(err))
		}
		return fmt.Errorf("failed to hash artifact: %w", err)
	}

	if actualChecksum != expectedChecksum {
		log.Warnf("[Verifier] Checksum mismatch: expected %s, got %s", expectedChecksum, actualChecksum)
		return fmt.Errorf("artifact checksum mismatch: expected %s, got %s", expectedChecksum, actualChecksum)
	}

	log.Infof("[Verifier] Artifact checksum verified: %s", actualChecksum)
	return nil
}

// VerifyManifest validates and parses a plugin manifest file
func (mv *ManifestVerifier) VerifyManifest(manifestPath string) (*VerificationResult, error) {
	log := logging.L()

	result := &VerificationResult{
		Errors: []string{},
	}

	// Read manifest file
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest: %w", err)
	}

	// Parse JSON
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse manifest JSON: %w", err)
	}

	result.Manifest = &manifest

	// Validate required fields
	if manifest.ID == "" {
		result.Errors = append(result.Errors, "manifest missing required field: id")
	}
	if manifest.Name == "" {
		result.Errors = append(result.Errors, "manifest missing required field: name")
	}
	if manifest.Version == "" {
		result.Errors = append(result.Errors, "manifest missing required field: version")
	}
	if manifest.ID != "" {
		if err := validatePluginID(manifest.ID); err != nil {
			result.Errors = append(result.Errors, err.Error())
		}
	}
	if manifest.Version != "" {
		if err := validatePluginVersion(manifest.Version); err != nil {
			result.Errors = append(result.Errors, err.Error())
		}
	}
	if manifest.CanonicalType == "" {
		result.Errors = append(result.Errors, "manifest missing required field: canonical_type")
	}
	// Asset-only plugins (runtime "none", e.g. themes) ship no executable;
	// entrypoint is the canonical field, executable a legacy alias.
	if manifest.Executable == "" && manifest.Entrypoint == "" && manifest.Runtime != "none" {
		result.Errors = append(result.Errors, "manifest missing required field: executable or entrypoint")
	}

	// Validate canonical type
	if !isValidCanonicalType(manifest.CanonicalType) {
		result.Errors = append(result.Errors, fmt.Sprintf("invalid canonical_type: %s", manifest.CanonicalType))
	}

	// Validate device architecture
	if manifest.DeviceArch == "" {
		result.Errors = append(result.Errors, "manifest missing required field: device_arch")
	}
	// ADR-0129 closed set, same as ParseManifest: this is the marketplace
	// install path, which never goes through ParseManifest (ut-docs#3176).
	if err := validateProvidesAndMarkets(&manifest); err != nil {
		result.Errors = append(result.Errors, err.Error())
	}
	// ADR-0121 §2, same as ParseManifest (ut-docs#3155).
	if err := validateABI3Fields(&manifest); err != nil {
		result.Errors = append(result.Errors, err.Error())
	}
	// Validation-data grants (ut-docs#3226): same refusal as ParseManifest,
	// since marketplace install and import verify through here.
	if err := validateValidationPermissions(&manifest); err != nil {
		result.Errors = append(result.Errors, err.Error())
	}
	// Setting-bound grants, same as ParseManifest (ut-docs#3514).
	if err := validateSettingBoundPermissions(&manifest); err != nil {
		result.Errors = append(result.Errors, err.Error())
	}
	// Setting-type enum, same as ParseManifest (ADR-0082, ut-docs#3514).
	for _, s := range manifest.Settings {
		if !isValidSettingType(s.Type) {
			result.Errors = append(result.Errors, fmt.Sprintf("manifest setting %q has invalid type %q (allowed: %s)",
				s.Key, s.Type, allowedSettingTypes()))
		}
	}
	// Settings-form metadata, same as ParseManifest (ut-docs#2832,
	// ut-docs#4156).
	if err := validateSettingForm(&manifest); err != nil {
		result.Errors = append(result.Errors, err.Error())
	}
	if err := validateSettingsV2(&manifest); err != nil {
		result.Errors = append(result.Errors, err.Error())
	}
	// Endpoint-typed setting defaults, same as ParseManifest (ut-docs#3552):
	// this path persists DefaultValue via PersistManifest too, so it needs
	// the same gate.
	if err := validateSettingDefaults(&manifest); err != nil {
		result.Errors = append(result.Errors, err.Error())
	}
	// Permission allow-list, same as ParseManifest (ut-docs#3328).
	if err := validatePermissions(&manifest); err != nil {
		result.Errors = append(result.Errors, err.Error())
	}

	// Checksum verification (if provided in manifest)
	result.ChecksumVerified = true // Default to true if no checksum in manifest

	// Signature verification. With no key, only a devplugins build
	// (allowUnsigned) treats the manifest as verified; every other build
	// refuses it outright (ADR-0158).
	result.SignatureVerified = mv.publicKey == nil && mv.allowUnsigned
	noKey := mv.publicKey == nil && !mv.allowUnsigned
	if noKey {
		result.Errors = append(result.Errors, "no marketplace signing key configured, so the plugin cannot be verified")
	}

	// Fail closed: with a public key configured, an unsigned manifest must be
	// rejected, not silently skipped (every marketplace-published manifest is
	// signed; a missing signature only ever means tampered/hand-built input).
	// No key at all is handled above (noKey).
	unsigned := mv.publicKey != nil && manifest.Signature == ""
	if unsigned {
		result.Errors = append(result.Errors, "manifest missing required field: signature (public key is configured)")
	}

	if mv.publicKey != nil && manifest.Signature != "" {
		// Verify Ed25519 signature
		sigBytes, err := hex.DecodeString(manifest.Signature)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("invalid signature hex: %v", err))
		} else {
			// Canonical form: signature and both ADR-0165 content fields
			// emptied — byte-identical to the form before them.
			canonicalBytes, err := legacySignedBytes(manifest)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("failed to create canonical manifest: %v", err))
			} else {
				if ed25519.Verify(mv.publicKey, canonicalBytes, sigBytes) {
					result.SignatureVerified = true
					log.Infof("[Verifier] Manifest signature verified for plugin %s", manifest.ID)
				} else {
					result.Errors = append(result.Errors, "manifest signature verification failed")
					log.Warnf("[Verifier] Signature verification failed for plugin %s", manifest.ID)
				}
			}
		}
	}

	// ADR-0165 (ut-docs#4546): with a key, the signature must cover every
	// file in the package too, not only manifest.json. A missing digest is
	// refused rather than treated as legacy — stripping both fields would
	// otherwise turn any signed manifest back into one covering nothing.
	if mv.publicKey != nil {
		// A missing content field reads as unsigned to the operator: a
		// pre-ADR-0165 bundle (an export of an older install) needs the
		// "install it from the Plugin Store" message, not a generic failure.
		if manifest.ContentDigest == "" || manifest.ContentSignature == "" {
			unsigned = true
		}
		if errs := mv.verifyContent(&manifest, filepath.Dir(manifestPath)); len(errs) > 0 {
			result.Errors = append(result.Errors, errs...)
			result.SignatureVerified = false
		}
	}

	if len(result.Errors) > 0 {
		// Name the actual problem(s), not just a count (ut-docs#2132) — "1
		// errors" told an operator nothing actionable; the caller (and the
		// server log via LogAndLocalizedError) now gets the real reason(s).
		err := fmt.Errorf("manifest validation failed (%d error(s)): %s", len(result.Errors), strings.Join(result.Errors, "; "))
		if unsigned {
			err = fmt.Errorf("%w: %w", ErrManifestUnsigned, err)
		}
		if noKey {
			err = fmt.Errorf("%w: %w", ErrNoMarketplaceKey, err)
		}
		return result, err
	}

	// Applied only AFTER the signature check: the signed canonical form is
	// the manifest as published, runtime field absent included. A manifest
	// without a runtime gets the sandbox, never the process runtime
	// (ut-docs#2891) — the marketplace installer would otherwise treat ""
	// as a native executable.
	if manifest.Runtime == "" {
		manifest.Runtime = defaultManifestRuntime
		warnDefaultRuntimeOnce(manifest.ID)
	}

	return result, nil
}

// verifyContent checks the ADR-0165 content fields of m against the package
// directory dir: both present, content_signature valid over the content
// form, and dir's ComputeContentDigest equal to content_digest. Returns the
// failures, if any.
func (mv *ManifestVerifier) verifyContent(m *Manifest, dir string) []string {
	if m.ContentDigest == "" || m.ContentSignature == "" {
		return []string{"manifest missing required field: content_digest/content_signature (public key is configured; the release must be re-signed by the marketplace)"}
	}
	var errs []string
	sigBytes, err := hex.DecodeString(m.ContentSignature)
	if err != nil {
		errs = append(errs, fmt.Sprintf("invalid content_signature hex: %v", err))
	} else if contentBytes, err := contentSignedBytes(*m); err != nil {
		errs = append(errs, fmt.Sprintf("failed to create canonical content manifest: %v", err))
	} else if !ed25519.Verify(mv.publicKey, contentBytes, sigBytes) {
		errs = append(errs, "manifest content signature verification failed")
		logging.L().Warnf("[Verifier] Content signature verification failed for plugin %s", m.ID)
	}
	actual, err := ComputeContentDigest(dir)
	switch {
	case err != nil:
		errs = append(errs, err.Error())
	case actual != m.ContentDigest:
		errs = append(errs, fmt.Sprintf("content digest mismatch: package files do not match the signed manifest (expected %s, got %s)", m.ContentDigest, actual))
		logging.L().Warnf("[Verifier] Content digest mismatch for plugin %s: expected %s, got %s", m.ID, m.ContentDigest, actual)
	}
	return errs
}

// legacySignedBytes is what `signature` covers: the manifest with signature,
// content_digest and content_signature emptied. The two content fields are
// omitempty, so these are the bytes a till without ADR-0165 computes.
func legacySignedBytes(m Manifest) ([]byte, error) {
	m.Signature, m.ContentDigest, m.ContentSignature = "", "", ""
	return json.Marshal(m)
}

// contentSignedBytes is what `content_signature` covers: the manifest with
// signature and content_signature emptied, content_digest kept (ADR-0165 §3).
func contentSignedBytes(m Manifest) ([]byte, error) {
	m.Signature, m.ContentSignature = "", ""
	return json.Marshal(m)
}

// VerifyExecutable checks that the executable file exists and is executable
func (mv *ManifestVerifier) VerifyExecutable(pluginDir, executableName string) error {
	execPath := filepath.Join(pluginDir, executableName)

	info, err := os.Stat(execPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("executable not found: %s", executableName)
		}
		return fmt.Errorf("failed to stat executable: %w", err)
	}

	if info.IsDir() {
		return fmt.Errorf("executable is a directory: %s", executableName)
	}

	// Check if file is executable (Unix permissions)
	mode := info.Mode()
	if mode&0111 == 0 {
		return fmt.Errorf("file is not executable: %s (permissions: %s)", executableName, mode.String())
	}

	return nil
}

// CanonicalTypes is the full plugin-type taxonomy. It mirrors the
// plugin_entries.type CHECK constraint in the schema — keep the two in
// sync. It also must mirror ut-docs/adr/0002-plugin-type-taxonomy.md's own
// taxonomy line, pinned for real by scripts/ci/guard-adr-plugin-taxonomy.sh
// (ut-docs#2134) — adding a type here without updating that ADR fails CI.
var CanonicalTypes = []string{
	"page", "button", "popup", "payment", "device", "integration",
	"report", "pricing", "tax", "import", "export", "hardware",
	"background_job", "scheduler", "receipt_template", "customer_facing",
	"auth", "notification", "delivery", "theme", "language", "layout",
}

var canonicalTypeSet = func() map[string]bool {
	m := make(map[string]bool, len(CanonicalTypes))
	for _, t := range CanonicalTypes {
		m[t] = true
	}
	return m
}()

// isValidCanonicalType checks if a canonical type is in the allowed list.
func isValidCanonicalType(canonicalType string) bool {
	return canonicalTypeSet[canonicalType]
}

// VerifyCompatibility checks if a plugin is compatible with the current system
func (mv *ManifestVerifier) VerifyCompatibility(manifest *Manifest, systemArch, posVersion string) error {
	// Check device architecture match
	if manifest.DeviceArch != systemArch && manifest.DeviceArch != "any" {
		return fmt.Errorf("incompatible architecture: plugin requires %s, system is %s", manifest.DeviceArch, systemArch)
	}

	// Check minimum POS version (if specified). Compare numerically per part —
	// a lexicographic string compare mis-orders any double-digit component
	// ("0.2.49" < "0.2.5", "0.10.0" < "0.9.0").
	if manifest.MinPOSVersion != "" {
		if compareVersions(manifest.MinPOSVersion, posVersion) > 0 {
			return fmt.Errorf("incompatible POS version: plugin requires >= %s, system is %s", manifest.MinPOSVersion, posVersion)
		}
	}

	return nil
}
