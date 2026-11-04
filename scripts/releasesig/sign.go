package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"regexp"
	"strings"

	"github.com/universaltill/universal-till/internal/releasesig"
)

// sigLineRe mirrors the line format internal/releasesig.Verify accepts.
var sigLineRe = regexp.MustCompile(`^ed25519 ([0-9a-f]{16}) ([A-Za-z0-9+/]{86}==)$`)

// sign returns one signature line for data, signed with the 32-byte seed.
func sign(seed, data []byte) string {
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	sig := ed25519.Sign(priv, data)
	return "ed25519 " + releasesig.KeyID(pub) + " " + base64.StdEncoding.EncodeToString(sig)
}

// appendLine returns existingSig with line added, newline-terminated.
func appendLine(existingSig []byte, line string) []byte {
	out := append([]byte{}, existingSig...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	out = append(out, line...)
	return append(out, '\n')
}

// appendPruned is appendLine for a re-sign: it adds line, but first drops
// every existing line that is identical to it, and every existing line by
// pub's key that verifies none of keep (the checksums.txt bytes a till could
// still read while the new file is being uploaded). Re-running a release job
// therefore leaves at most len(keep)+1 lines per key instead of growing the
// file past the verifier's line cap. Lines by other keys are left alone.
func appendPruned(existingSig []byte, line string, pub ed25519.PublicKey, keep ...[]byte) []byte {
	id := releasesig.KeyID(pub)
	var kept []string
	for _, l := range strings.Split(string(existingSig), "\n") {
		if l == "" || l == line {
			continue
		}
		if m := sigLineRe.FindStringSubmatch(l); m != nil && m[1] == id {
			raw, err := base64.StdEncoding.DecodeString(m[2])
			ok := false
			for _, d := range keep {
				if err == nil && ed25519.Verify(pub, d, raw) {
					ok = true
				}
			}
			if !ok {
				continue
			}
		}
		kept = append(kept, l)
	}
	return appendLine([]byte(strings.Join(kept, "\n")), line)
}
