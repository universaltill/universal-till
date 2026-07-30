package plugins

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/universaltill/universal-till/internal/logging"
)

// ManifestVerifier validates plugin manifests and artifact signatures
type ManifestVerifier struct {
	publicKey ed25519.PublicKey
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

// NewManifestVerifier creates a new manifest verifier
// publicKeyHex is the marketplace's Ed25519 public key in hex format
func NewManifestVerifier(publicKeyHex string) (*ManifestVerifier, error) {
	if publicKeyHex == "" {
		return &ManifestVerifier{}, nil // Allow operation without signature verification for dev mode
	}

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

// VerifyArtifact verifies a downloaded plugin artifact against its expected checksum
func (mv *ManifestVerifier) VerifyArtifact(artifactPath, expectedChecksum string) error {
	log := logging.L()

	// Calculate SHA256 of artifact
	f, err := os.Open(artifactPath)
	if err != nil {
		return fmt.Errorf("failed to open artifact: %w", err)
	}
	defer f.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return fmt.Errorf("failed to hash artifact: %w", err)
	}

	actualChecksum := hex.EncodeToString(hasher.Sum(nil))

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

	// Checksum verification (if provided in manifest)
	result.ChecksumVerified = true // Default to true if no checksum in manifest

	// Signature verification (if public key configured)
	result.SignatureVerified = mv.publicKey == nil // true if no verification configured

	// Fail closed: with a public key configured, an unsigned manifest must be
	// rejected, not silently skipped (every marketplace-published manifest is
	// signed; a missing signature only ever means tampered/hand-built input).
	// Dev mode — no key configured — is unaffected.
	if mv.publicKey != nil && manifest.Signature == "" {
		result.Errors = append(result.Errors, "manifest missing required field: signature (public key is configured)")
	}

	if mv.publicKey != nil && manifest.Signature != "" {
		// Verify Ed25519 signature
		sigBytes, err := hex.DecodeString(manifest.Signature)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("invalid signature hex: %v", err))
		} else {
			// Create canonical representation for signing (exclude signature field)
			manifestCopy := manifest
			manifestCopy.Signature = ""
			canonicalBytes, err := json.Marshal(manifestCopy)
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

	if len(result.Errors) > 0 {
		return result, fmt.Errorf("manifest validation failed: %d errors", len(result.Errors))
	}

	return result, nil
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
// plugin_entries.type CHECK constraint in the schema — keep the two in sync.
var CanonicalTypes = []string{
	"page", "button", "popup", "payment", "device", "integration",
	"report", "pricing", "tax", "import", "export", "hardware",
	"background_job", "scheduler", "receipt_template", "customer_facing",
	"auth", "notification", "delivery", "theme", "language",
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
