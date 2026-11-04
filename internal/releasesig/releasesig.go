// Package releasesig signs and verifies a till release's checksums.txt
// (ADR-0178). It is pure (no I/O) and shared by the till's self-updater and
// by CI (scripts/releasesig).
//
// A signature file holds one line per signer:
//
//	ed25519 <key id> <base64 signature over the exact checksums.txt bytes>
//
// where key id is hex(sha256(public key))[:16]. A file may carry several
// lines so a key rotation (and the macOS dmg re-sign) keeps older signers.
package releasesig

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// SigAsset is the release asset that carries the signature lines.
const SigAsset = "checksums.txt.sig"

const (
	maxSigBytes = 16 << 10
	maxLines    = 8
	maxKeys     = 4
)

var (
	ErrMalformed    = errors.New("malformed release signature")
	ErrUnknownKey   = errors.New("release signature is by no trusted key")
	ErrBadSignature = errors.New("release signature does not match checksums.txt")
	ErrNoKeys       = errors.New("no release keys configured")
)

// Key is a trusted release public key and its id.
type Key struct {
	ID  string
	Pub ed25519.PublicKey
}

var (
	pubHexRe = regexp.MustCompile(`^[0-9a-f]{64}$`)
	lineRe   = regexp.MustCompile(`^ed25519 ([0-9a-f]{16}) ([A-Za-z0-9+/]{86}==)$`)
)

// KeyID is hex(sha256(pub))[:16].
func KeyID(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:])[:16]
}

// ParseKeys parses a build stamp: "" (no keys) or a comma list of 1..4
// distinct lowercase 64-hex Ed25519 public keys.
func ParseKeys(stamp string) ([]Key, error) {
	if stamp == "" {
		return nil, nil
	}
	parts := strings.Split(stamp, ",")
	if len(parts) > maxKeys {
		return nil, fmt.Errorf("more than %d release keys", maxKeys)
	}
	seen := map[string]bool{}
	keys := make([]Key, 0, len(parts))
	for _, p := range parts {
		if !pubHexRe.MatchString(p) {
			return nil, errors.New("release key is not 64 lowercase hex characters")
		}
		if seen[p] {
			return nil, errors.New("duplicate release key")
		}
		seen[p] = true
		raw, err := hex.DecodeString(p)
		if err != nil {
			return nil, err
		}
		pub := ed25519.PublicKey(raw)
		keys = append(keys, Key{ID: KeyID(pub), Pub: pub})
	}
	return keys, nil
}

// Sign returns one signature line for data, signed with the 32-byte seed.
func Sign(seed, data []byte) string {
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	sig := ed25519.Sign(priv, data)
	return "ed25519 " + KeyID(pub) + " " + base64.StdEncoding.EncodeToString(sig)
}

// Append returns existingSig with line added, newline-terminated.
func Append(existingSig []byte, line string) []byte {
	out := append([]byte{}, existingSig...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	out = append(out, line...)
	return append(out, '\n')
}

// AppendPruned is Append for a re-sign: it adds line, but first drops every
// existing line that is identical to it, and every existing line by pub's key
// that verifies none of keep (the checksums.txt bytes a till could still read
// while the new file is being uploaded). Re-running a release job therefore
// leaves at most len(keep)+1 lines per key instead of growing the file past
// the verifier's line cap. Lines by other keys are left alone.
func AppendPruned(existingSig []byte, line string, pub ed25519.PublicKey, keep ...[]byte) []byte {
	id := KeyID(pub)
	var kept []string
	for _, l := range strings.Split(string(existingSig), "\n") {
		if l == "" || l == line {
			continue
		}
		if m := lineRe.FindStringSubmatch(l); m != nil && m[1] == id {
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
	return Append([]byte(strings.Join(kept, "\n")), line)
}

// Verify checks that sig holds a valid signature over data by one of keys and
// returns that key's id. Any malformed content refuses the whole file.
func Verify(keys []Key, data, sig []byte) (string, error) {
	if len(keys) == 0 {
		return "", ErrNoKeys
	}
	if len(sig) > maxSigBytes {
		return "", fmt.Errorf("%w: file too large", ErrMalformed)
	}
	var lines []string
	for _, l := range strings.Split(string(sig), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 || len(lines) > maxLines {
		return "", fmt.Errorf("%w: want 1..%d lines", ErrMalformed, maxLines)
	}
	type entry struct {
		id  string
		sig []byte
	}
	var entries []entry
	for _, l := range lines {
		m := lineRe.FindStringSubmatch(l)
		if m == nil {
			return "", fmt.Errorf("%w: bad line", ErrMalformed)
		}
		raw, err := base64.StdEncoding.DecodeString(m[2])
		if err != nil || len(raw) != ed25519.SignatureSize {
			return "", fmt.Errorf("%w: bad signature encoding", ErrMalformed)
		}
		entries = append(entries, entry{m[1], raw})
	}
	known := false
	for _, e := range entries {
		for _, k := range keys {
			if k.ID != e.id {
				continue
			}
			known = true
			if ed25519.Verify(k.Pub, data, e.sig) {
				return k.ID, nil
			}
		}
	}
	if known {
		return "", ErrBadSignature
	}
	return "", ErrUnknownKey
}
