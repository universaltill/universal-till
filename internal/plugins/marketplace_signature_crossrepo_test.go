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
