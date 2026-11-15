package releasesig

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
)

// testSeed returns a deterministic seed for fixture key n.
func testSeed(n byte) []byte { return bytes.Repeat([]byte{n}, ed25519.SeedSize) }

func testPub(n byte) ed25519.PublicKey {
	return ed25519.NewKeyFromSeed(testSeed(n)).Public().(ed25519.PublicKey)
}

func testPubHex(n byte) string { return hex.EncodeToString(testPub(n)) }

func testKey(n byte) Key { return Key{ID: KeyID(testPub(n)), Pub: testPub(n)} }

// testSign is a test-only signer producing the wire format Verify accepts.
func testSign(seed, data []byte) string {
	priv := ed25519.NewKeyFromSeed(seed)
	return "ed25519 " + KeyID(priv.Public().(ed25519.PublicKey)) + " " + base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data))
}
