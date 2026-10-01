package plugins

import (
	"testing"
)

// TestMarketplaceSignatureVerifies is a cross-repo contract test: the signed
// manifest fixture in testdata/ was produced by the marketplace signer
// (ut-market-place/internal/signing) using a fixed test key. This proves the
// real POS ManifestVerifier accepts marketplace-produced Ed25519 signatures —
// i.e. the canonical-JSON signing contract matches across the two repos.
//
// If this breaks, the marketplace signing.CanonicalManifest struct and this
// package's Manifest struct have drifted; regenerate the fixture from the
// marketplace and reconcile the structs.
const marketplaceTestPublicKeyHex = "79b5562e8fe654f94078b112e8a98ba7901f853ae695bed7e0e3910bad049664"

func TestMarketplaceSignatureVerifies(t *testing.T) {
	verifier, err := NewManifestVerifier(marketplaceTestPublicKeyHex)
	if err != nil {
		t.Fatalf("NewManifestVerifier: %v", err)
	}
	if !verifier.HasPublicKey() {
		t.Fatal("expected configured public key")
	}

	result, err := verifier.VerifyManifest("testdata/marketplace_signed_manifest.json")
	if err != nil {
		t.Fatalf("VerifyManifest returned errors: %v (%v)", err, result.Errors)
	}
	if !result.SignatureVerified {
		t.Fatal("marketplace signature did not verify with the real POS verifier")
	}
}

func TestMarketplaceSignatureRejectedByWrongKey(t *testing.T) {
	// A different (valid-length) key must reject the signature.
	wrong := "0000000000000000000000000000000000000000000000000000000000000000"
	verifier, err := NewManifestVerifier(wrong)
	if err != nil {
		t.Fatalf("NewManifestVerifier: %v", err)
	}
	result, err := verifier.VerifyManifest("testdata/marketplace_signed_manifest.json")
	// Verification failure surfaces as an error + SignatureVerified=false.
	if err == nil && result.SignatureVerified {
		t.Fatal("signature must not verify under the wrong public key")
	}
}

// abi3FixturePublicKeyHex verifies testdata/marketplace_signed_manifest_abi3.json:
// a manifest carrying every ADR-0121 ABI-3 field (ut-docs#3155), signed by
// ut-cloud's internal/signing.Signer with a deterministic test seed (bytes
// 31..62). It proves the till and the marketplace marshal those fields to
// the same bytes, including the zero values (keep_years 0, an absent
// jitter_s), which the source-level mirror test cannot.
const abi3FixturePublicKeyHex = "af3d20264f9c26ef085b5ce537f417d424037a0963a6386ff6d050e5bf773714"

func TestMarketplaceSignatureVerifiesABI3Fields(t *testing.T) {
	verifier, err := NewManifestVerifier(abi3FixturePublicKeyHex)
	if err != nil {
		t.Fatalf("NewManifestVerifier: %v", err)
	}
	result, err := verifier.VerifyManifest("testdata/marketplace_signed_manifest_abi3.json")
	if err != nil {
		t.Fatalf("VerifyManifest returned errors: %v (%v)", err, result.Errors)
	}
	if !result.SignatureVerified {
		t.Fatal("marketplace signature over the ABI-3 fields did not verify with the real POS verifier")
	}
	m := result.Manifest
	if m.Limits == nil || len(m.Schedules) != 2 || m.DB == nil || m.Retention == nil || len(m.ViewsUsed) != 2 || m.Entries[0].Slot != "reports.panels" {
		t.Fatalf("fixture lost ABI-3 fields: %+v", m)
	}
}
