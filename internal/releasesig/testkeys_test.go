package releasesig

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
)

// testSeed returns a deterministic seed for fixture key n.
func testSeed(n byte) []byte { return bytes.Repeat([]byte{n}, ed25519.SeedSize) }

func testPub(n byte) ed25519.PublicKey {
	return ed25519.NewKeyFromSeed(testSeed(n)).Public().(ed25519.PublicKey)
}

func testPubHex(n byte) string { return hex.EncodeToString(testPub(n)) }

func testKey(n byte) Key { return Key{ID: KeyID(testPub(n)), Pub: testPub(n)} }
