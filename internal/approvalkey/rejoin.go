package approvalkey

// The till-rejoin proof (ADR-0222 §9, ut-docs#7098): a till that is paired
// again with the owner's new code proves it is the same device by signing its
// device id, its public key and the hash of the code, so the cloud keeps the
// same device_id instead of making a second register. Mirrors ut-cloud
// internal/approvalkey (rejoin.go); the golden vector testdata/rejoin_vector.json
// is a byte-identical copy of its file.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// RejoinCodeHashLen is the length of the code-hash line: lower-case hex SHA-256.
const RejoinCodeHashLen = 64

const (
	// rejoinCodeLen and rejoinCodeAlphabet mirror ut-cloud
	// internal/merchantauth/pairing_codes.go (PairingCodeLen, pairingAlphabet).
	rejoinCodeLen      = 8
	rejoinCodeAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
)

// errRejoinCodeHash: the code-hash line is not 64 lower-case hex characters.
var errRejoinCodeHash = errors.New("rejoin: the code hash is not 64 lower-case hex characters")

// RejoinMessage is the exact bytes signed: UTF-8, "\n"-joined, no trailing
// newline: the rejoin domain, device_id, public_key (base64url) and
// code_hash. Every field must be non-empty and single-line; codeHash must be
// exactly 64 lower-case hex characters.
func RejoinMessage(deviceID, publicKey, codeHash string) ([]byte, error) {
	if len(codeHash) != RejoinCodeHashLen {
		return nil, errRejoinCodeHash
	}
	for _, c := range codeHash {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return nil, errRejoinCodeHash
		}
	}
	return joinSigned(rejoinDomain, deviceID, publicKey, codeHash)
}

// RejoinCodeHash is the hex SHA-256 of the pairing code after the cloud's
// normalisation (ut-cloud merchantauth.normalisePairingCode): ASCII only
// (checked before upper-casing, so U+017F never becomes S), upper case, ' '
// and '-' removed, every character in the alphabet, exactly 8 of them.
// ok is false for a malformed code (the till then sends no rejoin).
func RejoinCodeHash(code string) (hash string, ok bool) {
	var b strings.Builder
	for _, r := range code {
		if r > 0x7f {
			return "", false
		}
		if r >= 'a' && r <= 'z' {
			r -= 'a' - 'A'
		}
		switch {
		case r == ' ' || r == '-':
			continue
		case !strings.ContainsRune(rejoinCodeAlphabet, r):
			return "", false
		}
		if b.Len() >= rejoinCodeLen {
			return "", false
		}
		b.WriteRune(r)
	}
	if b.Len() != rejoinCodeLen {
		return "", false
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:]), true
}

// SignRejoin signs RejoinMessage(deviceID, own public key, codeHash). It
// never creates a key, like SignRecover: a till with no usable key cannot
// prove anything (ADR-0222 §6).
func (s *Store) SignRejoin(deviceID, codeHash string) (kid, publicKey, sig string, err error) {
	return s.sign(func(pub string) ([]byte, error) {
		return RejoinMessage(deviceID, pub, codeHash)
	})
}
