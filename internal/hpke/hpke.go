// Package hpke is a standard-library-only implementation of the one RFC 9180
// HPKE configuration Universal Till uses: mode_base, KEM 0x0020
// DHKEM(X25519, HKDF-SHA256), KDF 0x0001 HKDF-SHA256, AEAD 0x0001
// AES-128-GCM, single-shot (sequence number 0 only).
//
// It exists for the till-user PIN directives (ADR-0115 amendment
// 2026-09-25, ut-docs reference/till-user-directives.md §2): ut-cloud seals
// a PIN to the main till's X25519 key, the till opens it. Both repos pin
// RFC 9180 Appendix A.1.1 and the shared cross-repo vector
// (ut-docs reference/contracts/till-user-pin-v1.json). A standard-library
// HPKE package may replace this once both repos' Go versions have one, if
// it passes the same vectors.
package hpke

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// Suite identifiers (RFC 9180 §7).
const (
	KEMID  uint16 = 0x0020 // DHKEM(X25519, HKDF-SHA256)
	KDFID  uint16 = 0x0001 // HKDF-SHA256
	AEADID uint16 = 0x0001 // AES-128-GCM

	// EncSize is Nenc for X25519: the encapsulated key is the ephemeral
	// public key, 32 bytes.
	EncSize = 32

	nSecret = 32 // Nsecret, DHKEM(X25519)
	nk      = 16 // Nk, AES-128-GCM
	nn      = 12 // Nn, AES-128-GCM

	modeBase byte = 0x00
)

var (
	kemSuiteID  = []byte{'K', 'E', 'M', byte(KEMID >> 8), byte(KEMID)}
	hpkeSuiteID = []byte{'H', 'P', 'K', 'E',
		byte(KEMID >> 8), byte(KEMID), byte(KDFID >> 8), byte(KDFID), byte(AEADID >> 8), byte(AEADID)}
)

// ErrOpen is returned for every failure to open a ciphertext, whatever the
// cause (bad enc, wrong key, wrong info/aad, tampered ciphertext): callers
// get no oracle to tell them apart.
var ErrOpen = errors.New("hpke: open failed")

// Schedule is the key-schedule output of a base-mode setup: the KEM
// shared_secret, then the AEAD key and base_nonce. Exposed so tests can
// name the first value that diverges from a vector.
type Schedule struct {
	SharedSecret []byte
	Key          []byte
	BaseNonce    []byte
}

func labeledExtract(suiteID, salt []byte, label string, ikm []byte) ([]byte, error) {
	labeled := make([]byte, 0, 7+len(suiteID)+len(label)+len(ikm))
	labeled = append(labeled, "HPKE-v1"...)
	labeled = append(labeled, suiteID...)
	labeled = append(labeled, label...)
	labeled = append(labeled, ikm...)
	return hkdf.Extract(sha256.New, labeled, salt)
}

func labeledExpand(suiteID, prk []byte, label string, info []byte, length int) ([]byte, error) {
	labeled := make([]byte, 2, 2+7+len(suiteID)+len(label)+len(info))
	binary.BigEndian.PutUint16(labeled, uint16(length))
	labeled = append(labeled, "HPKE-v1"...)
	labeled = append(labeled, suiteID...)
	labeled = append(labeled, label...)
	labeled = append(labeled, info...)
	return hkdf.Expand(sha256.New, prk, string(labeled), length)
}

// extractAndExpand is DHKEM's ExtractAndExpand (RFC 9180 §4.1).
func extractAndExpand(dh, kemContext []byte) ([]byte, error) {
	prk, err := labeledExtract(kemSuiteID, nil, "eae_prk", dh)
	if err != nil {
		return nil, err
	}
	return labeledExpand(kemSuiteID, prk, "shared_secret", kemContext, nSecret)
}

// keySchedule is KeySchedule for mode_base with the default (empty) PSK
// and PSK id (RFC 9180 §5.1).
func keySchedule(sharedSecret, info []byte) (Schedule, error) {
	pskIDHash, err := labeledExtract(hpkeSuiteID, nil, "psk_id_hash", nil)
	if err != nil {
		return Schedule{}, err
	}
	infoHash, err := labeledExtract(hpkeSuiteID, nil, "info_hash", info)
	if err != nil {
		return Schedule{}, err
	}
	ctx := make([]byte, 0, 1+len(pskIDHash)+len(infoHash))
	ctx = append(ctx, modeBase)
	ctx = append(ctx, pskIDHash...)
	ctx = append(ctx, infoHash...)
	secret, err := labeledExtract(hpkeSuiteID, sharedSecret, "secret", nil)
	if err != nil {
		return Schedule{}, err
	}
	key, err := labeledExpand(hpkeSuiteID, secret, "key", ctx, nk)
	if err != nil {
		return Schedule{}, err
	}
	nonce, err := labeledExpand(hpkeSuiteID, secret, "base_nonce", ctx, nn)
	if err != nil {
		return Schedule{}, err
	}
	return Schedule{SharedSecret: sharedSecret, Key: key, BaseNonce: nonce}, nil
}

// SetupBaseS is the sender's SetupBaseS with a caller-supplied ephemeral
// key (the deterministic Encap RFC 9180's vectors use). Production senders
// pass a fresh random skE per message.
func SetupBaseS(pkR *ecdh.PublicKey, skE *ecdh.PrivateKey, info []byte) (enc []byte, s Schedule, err error) {
	if pkR == nil || skE == nil || pkR.Curve() != ecdh.X25519() || skE.Curve() != ecdh.X25519() {
		return nil, Schedule{}, errors.New("hpke: X25519 keys required")
	}
	dh, err := skE.ECDH(pkR) // rejects an all-zero output (RFC 9180 §7.1.4)
	if err != nil {
		return nil, Schedule{}, fmt.Errorf("hpke: encap: %w", err)
	}
	enc = skE.PublicKey().Bytes()
	kemContext := append(append([]byte{}, enc...), pkR.Bytes()...)
	ss, err := extractAndExpand(dh, kemContext)
	if err != nil {
		return nil, Schedule{}, err
	}
	s, err = keySchedule(ss, info)
	return enc, s, err
}

// SetupBaseR is the recipient's SetupBaseR.
func SetupBaseR(skR *ecdh.PrivateKey, enc, info []byte) (Schedule, error) {
	if skR == nil || skR.Curve() != ecdh.X25519() || len(enc) != EncSize {
		return Schedule{}, ErrOpen
	}
	pkE, err := ecdh.X25519().NewPublicKey(enc)
	if err != nil {
		return Schedule{}, ErrOpen
	}
	dh, err := skR.ECDH(pkE)
	if err != nil {
		return Schedule{}, ErrOpen
	}
	kemContext := append(append([]byte{}, enc...), skR.PublicKey().Bytes()...)
	ss, err := extractAndExpand(dh, kemContext)
	if err != nil {
		return Schedule{}, err
	}
	return keySchedule(ss, info)
}

func (s Schedule) aead() (cipher.AEAD, error) {
	block, err := aes.NewCipher(s.Key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// SealDeterministic is single-shot SealBase with a caller-supplied
// ephemeral key: it returns enc and the sequence-0 ciphertext. Used by
// tests and vectors on this side; ut-cloud's sealer reproduces it with a
// fresh ephemeral key per PIN.
func SealDeterministic(pkR *ecdh.PublicKey, skE *ecdh.PrivateKey, info, aad, pt []byte) (enc, ct []byte, err error) {
	enc, s, err := SetupBaseS(pkR, skE, info)
	if err != nil {
		return nil, nil, err
	}
	a, err := s.aead()
	if err != nil {
		return nil, nil, err
	}
	// Sequence number 0: the nonce is base_nonce XOR 0 = base_nonce.
	return enc, a.Seal(nil, s.BaseNonce, pt, aad), nil
}

// Open is single-shot OpenBase: it opens the sequence-0 ciphertext ct
// sealed to skR's public key with the given enc, info and aad. Every
// failure is ErrOpen.
func Open(skR *ecdh.PrivateKey, enc, info, aad, ct []byte) ([]byte, error) {
	s, err := SetupBaseR(skR, enc, info)
	if err != nil {
		return nil, ErrOpen
	}
	a, err := s.aead()
	if err != nil {
		return nil, ErrOpen
	}
	pt, err := a.Open(nil, s.BaseNonce, ct, aad)
	if err != nil {
		return nil, ErrOpen
	}
	return pt, nil
}
