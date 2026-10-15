package lantls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"time"
)

// Key rotation (ADR-0114 §7, ut-docs#4409). The main till creates the next
// key RotateEvery after the current one, announces its pin as next_pin in
// the link's hello, and switches to it once every linked peer has
// confirmed it (ConfirmNext) or RotationWindow has passed. The retired key
// is kept on disk — never served — for KeepPrevious.
//
// Every decision is taken at load (LoadOrCreate), so the served pin is
// fixed for a process lifetime: ConnContext, ServedPin, pairing and the
// primary proof never see it change under them. A confirmed rotation
// therefore switches at the next start.
//
// Rotation is never the cause of a failed request: any failure in it is
// logged and the current key keeps being served.

const (
	nextKeyFile  = "next-key.pem"
	nextCertFile = "next-cert.pem"
	prevKeyFile  = "prev-key.pem"
	prevCertFile = "prev-cert.pem"
	stateFile    = "rotation.json"

	// RotateEvery is the key's age at which the next key is created.
	RotateEvery = 4 * 365 * 24 * time.Hour
	// RotationWindow is the longest a next key stays pending: past it the
	// main till switches even without every peer's confirmation (a peer
	// that missed it re-proves, ut-docs#4410).
	RotationWindow = 14 * 24 * time.Hour
	// KeepPrevious is how long the retired key stays on disk after a
	// switch.
	KeepPrevious = 14 * 24 * time.Hour

	// maxConfirmed bounds the required set ConfirmNext tracks; with more
	// required tills a rotation only switches at RotationWindow.
	maxConfirmed = 256
)

// plausibleSince is the earliest time a LAN key of this software can have
// been created. A Pi with no RTC boots with its clock at 1970 until it is
// set: a time before this is a clock not yet set, never a key's real age,
// so no time-based step runs on it and a creation time before it restarts
// the count.
var plausibleSince = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// rotationState is rotation.json.
type rotationState struct {
	// KeyCreatedAt is when the current key was created.
	KeyCreatedAt time.Time `json:"key_created_at,omitzero"`
	// NextPin is the pending next key's pin ("" = none); confirmations
	// belong to it.
	NextPin       string    `json:"next_pin,omitempty"`
	NextCreatedAt time.Time `json:"next_created_at,omitzero"`
	// Confirmed holds the till ids that reported NextPin (sorted).
	Confirmed []string `json:"confirmed,omitempty"`
	// Ready: every required peer confirmed; the next load switches.
	Ready      bool      `json:"ready,omitempty"`
	SwitchedAt time.Time `json:"switched_at,omitzero"`
}

func (s *rotationState) clearNext() {
	s.NextPin, s.NextCreatedAt, s.Confirmed, s.Ready = "", time.Time{}, nil, false
}

// loadState reads rotation.json; a missing or unreadable one is empty.
func loadState(dir string) rotationState {
	var st rotationState
	b, err := os.ReadFile(filepath.Join(dir, stateFile))
	if errors.Is(err, fs.ErrNotExist) {
		return st
	}
	if err == nil {
		err = json.Unmarshal(b, &st)
	}
	if err != nil {
		log.Printf("[LAN TLS] rotation state unreadable, starting over: %v", err)
		return rotationState{}
	}
	return st
}

func saveState(dir string, st rotationState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("lantls: encode rotation state: %w", err)
	}
	return writeAtomic(filepath.Join(dir, stateFile), b, 0o600)
}

// pinOfKey is the pin a certificate on k has: the SHA-256 of its
// SubjectPublicKeyInfo, as PinOf.
func pinOfKey(k *ecdsa.PrivateKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		return "", fmt.Errorf("lantls: marshal public key: %w", err)
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:]), nil
}

// rotation runs the load-time rotation steps on dir.
type rotation struct {
	dir string
	now time.Time
	st  rotationState
}

func (r *rotation) path(f string) string { return filepath.Join(r.dir, f) }

func (r *rotation) save() {
	if err := saveState(r.dir, r.st); err != nil {
		log.Printf("[LAN TLS] could not save the rotation state: %v", err)
	}
}

// run takes the current key (created reports it was just generated,
// keyMtime is its file's mtime otherwise) and returns the key to serve and
// the pending next key, if any. It never fails: a step that can't be
// completed is logged and the current key kept.
func (r *rotation) run(cur *ecdsa.PrivateKey, created bool, keyMtime time.Time) (serve, next *ecdsa.PrivateKey) {
	curPin, err := pinOfKey(cur)
	if err != nil {
		log.Printf("[LAN TLS] key rotation skipped: %v", err)
		return cur, nil
	}
	dirty := false

	// A switch interrupted after the next key was moved into place.
	if r.st.NextPin != "" && r.st.NextPin == curPin {
		r.finishSwitch()
		dirty = true
		log.Printf("[LAN TLS] finished an interrupted key rotation: serving the new key")
	}
	if created {
		r.st = rotationState{KeyCreatedAt: r.now}
		dirty = true
	} else if r.st.KeyCreatedAt.IsZero() {
		r.st.KeyCreatedAt = keyMtime
		if keyMtime.IsZero() {
			r.st.KeyCreatedAt = r.now
		}
		dirty = true
	}

	// With the clock not set, only a confirmed (ready) switch runs.
	clockOK := !r.now.Before(plausibleSince)
	if clockOK && r.st.KeyCreatedAt.Before(plausibleSince) {
		r.st.KeyCreatedAt = r.now
		dirty = true
	}

	next, broken := r.loadNext(curPin)
	if next != nil {
		nextPin, err := pinOfKey(next)
		if err != nil {
			log.Printf("[LAN TLS] next key unusable, not switching: %v", err)
			next, broken = nil, true
		} else if r.st.NextPin != nextPin {
			// A next key the state doesn't know (a power cut right after
			// creating it): it is pending since its file was written.
			r.st.NextPin, r.st.Confirmed, r.st.Ready = nextPin, nil, false
			r.st.NextCreatedAt = r.now
			if fi, err := os.Stat(r.path(nextKeyFile)); err == nil {
				r.st.NextCreatedAt = fi.ModTime()
			}
			dirty = true
		}
		if clockOK && next != nil && r.st.NextCreatedAt.Before(plausibleSince) {
			r.st.NextCreatedAt = r.now
			dirty = true
		}
	} else if !broken && r.st.NextPin != "" {
		r.st.clearNext() // its key is gone: nothing pending
		dirty = true
	}

	switch {
	case next != nil && (r.st.Ready || clockOK && !r.now.Before(r.st.NextCreatedAt.Add(RotationWindow))):
		if r.switchTo() {
			cur, next = next, nil
			dirty = true
		}
	case next == nil && !broken && clockOK && !r.now.Before(r.st.KeyCreatedAt.Add(RotateEvery)):
		if k := r.createNext(); k != nil {
			next = k
			dirty = true
		}
	}

	if r.prunePrev() {
		dirty = true
	}
	if dirty {
		r.save()
	}
	return cur, next
}

// loadNext reads the pending next key. broken reports one that exists but
// can't be used: it is left as is, never switched to or replaced.
func (r *rotation) loadNext(curPin string) (next *ecdsa.PrivateKey, broken bool) {
	k, err := readKey(r.path(nextKeyFile))
	if errors.Is(err, fs.ErrNotExist) {
		// A next certificate is written after its key and moved after it
		// at the switch: one without a key is an orphan of a power cut.
		if err := os.Remove(r.path(nextCertFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("[LAN TLS] could not remove an orphan %s: %v", nextCertFile, err)
		}
		return nil, false
	}
	if err != nil {
		log.Printf("[LAN TLS] pending next key unusable, keeping the current key: %v", err)
		return nil, true
	}
	if p, err := pinOfKey(k); err == nil && p == curPin {
		// Left over from a switch: it is the current key already.
		for _, f := range []string{nextKeyFile, nextCertFile} {
			if err := os.Remove(r.path(f)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				log.Printf("[LAN TLS] could not remove leftover %s: %v", f, err)
			}
		}
		return nil, false
	}
	return k, false
}

// switchTo makes the next key current, keeping the current one as prev-*.
// The order keeps a current key on disk through a power cut at any point:
// copy current → prev, then rename next over current (atomic), then the
// state; an interruption after the rename is finished by the next load
// (finishSwitch), one before it simply switches again.
func (r *rotation) switchTo() bool {
	keyPEM, err := os.ReadFile(r.path(privateKeyFile))
	if err != nil {
		log.Printf("[LAN TLS] key rotation postponed: read current key: %v", err)
		return false
	}
	if err := writeAtomic(r.path(prevKeyFile), keyPEM, 0o600); err != nil {
		log.Printf("[LAN TLS] key rotation postponed: keep current key: %v", err)
		return false
	}
	if certPEM, err := os.ReadFile(r.path(certFile)); err == nil {
		if err := writeAtomic(r.path(prevCertFile), certPEM, 0o600); err != nil {
			log.Printf("[LAN TLS] could not keep the current certificate: %v", err)
		}
	}
	if err := os.Rename(r.path(nextKeyFile), r.path(privateKeyFile)); err != nil {
		log.Printf("[LAN TLS] key rotation postponed: %v", err)
		return false
	}
	// A missing or failed certificate move is re-issued on the new key.
	if err := os.Rename(r.path(nextCertFile), r.path(certFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("[LAN TLS] next certificate not moved (re-issued): %v", err)
	}
	syncDir(r.dir)
	reason := "every linked till confirmed it"
	if !r.st.Ready {
		reason = "the rotation window ended"
	}
	r.finishSwitch()
	log.Printf("[LAN TLS] switched to the next key (%s); the previous key stays on disk for %v, not served", reason, KeepPrevious)
	return true
}

func (r *rotation) finishSwitch() {
	r.st.KeyCreatedAt = r.st.NextCreatedAt
	if r.st.KeyCreatedAt.IsZero() {
		r.st.KeyCreatedAt = r.now
	}
	r.st.SwitchedAt = r.now
	r.st.clearNext()
}

// createNext creates the next key and its certificate.
func (r *rotation) createNext() *ecdsa.PrivateKey {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Printf("[LAN TLS] could not create the next key: %v", err)
		return nil
	}
	if err := writeKey(r.path(nextKeyFile), key); err != nil {
		log.Printf("[LAN TLS] could not create the next key: %v", err)
		return nil
	}
	pin, err := pinOfKey(key)
	if err != nil {
		log.Printf("[LAN TLS] could not create the next key: %v", err)
		return nil
	}
	r.st.NextPin, r.st.NextCreatedAt, r.st.Confirmed, r.st.Ready = pin, r.now, nil, false
	log.Printf("[LAN TLS] key is %v old: created the next key; announcing it to linked tills", RotateEvery)
	return key
}

// prunePrev deletes the retired key KeepPrevious after the switch.
func (r *rotation) prunePrev() (dirty bool) {
	if _, err := os.Stat(r.path(prevKeyFile)); err != nil {
		return false
	}
	if r.st.SwitchedAt.Before(plausibleSince) {
		// The state was lost, or the switch ran with the clock not set:
		// keep it KeepPrevious from now.
		r.st.SwitchedAt = r.now
		return true
	}
	if r.now.Before(r.st.SwitchedAt.Add(KeepPrevious)) {
		return false
	}
	for _, f := range []string{prevKeyFile, prevCertFile} {
		if err := os.Remove(r.path(f)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("[LAN TLS] could not remove the previous key's %s: %v", f, err)
			return false
		}
	}
	log.Printf("[LAN TLS] removed the previous key (%v after the switch)", KeepPrevious)
	return false
}

// NextPin is the pending next key's pin, announced in the link's hello
// (fleetlink.Hello.NextPin); "" when no rotation is pending.
func (c *Cert) NextPin() string { return c.nextPin }

// NeedsConfirm reports whether tillID reporting nextPin would be a new
// confirmation of the pending rotation — the cheap check before the
// caller reads which tills are required.
func (c *Cert) NeedsConfirm(tillID, nextPin string) bool {
	if c.nextPin == "" || nextPin != c.nextPin || tillID == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.st.Ready && !slices.Contains(c.st.Confirmed, tillID)
}

// ConfirmNext records that tillID reported the pending next pin. required
// is every till whose confirmation the switch waits for; once all of them
// have confirmed, the rotation is ready and the next load switches. A
// wrong or empty pin, or a till outside required, changes nothing. The
// state is persisted before returning.
func (c *Cert) ConfirmNext(tillID, nextPin string, required []string) error {
	if c.nextPin == "" || nextPin != c.nextPin || tillID == "" || c.dir == "" {
		return nil
	}
	if len(required) > maxConfirmed {
		// More tills than are tracked: never ready, so the rotation
		// waits for its RotationWindow ceiling.
		return nil
	}
	if !slices.Contains(required, tillID) {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.st.Ready && slices.Contains(c.st.Confirmed, tillID) {
		return nil
	}
	confirmed := []string{tillID}
	for _, id := range c.st.Confirmed {
		// Only tills still required count: a revoked one drops out.
		if id != tillID && slices.Contains(required, id) {
			confirmed = append(confirmed, id)
		}
	}
	slices.Sort(confirmed)
	ready := true
	for _, id := range required {
		if !slices.Contains(confirmed, id) {
			ready = false
			break
		}
	}
	st := c.st
	st.Confirmed, st.Ready = confirmed, ready
	if err := saveState(c.dir, st); err != nil {
		return err
	}
	c.st = st
	if ready {
		log.Printf("[LAN TLS] every linked till confirmed the next key; switching at the next start")
	}
	return nil
}

var serving atomic.Pointer[Cert]

// SetServing records the certificate this till's LAN port serves, for
// handlers that announce or confirm a rotation (the main-till link); nil
// when TLS is off. It returns a func that restores the previous one (for
// tests).
func SetServing(c *Cert) (restore func()) {
	prev := serving.Swap(c)
	return func() { serving.Store(prev) }
}

// Serving is the certificate this till's LAN port serves, or nil.
func Serving() *Cert { return serving.Load() }

// readKey parses an ECDSA P-256 key file; a missing file is
// fs.ErrNotExist (errors.Is).
func readKey(path string) (*ecdsa.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("lantls: read key: %w", err)
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, fmt.Errorf("lantls: %s is not PEM (left as is)", path)
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("lantls: parse key %s (left as is): %w", path, err)
	}
	key, ok := k.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, fmt.Errorf("lantls: %s is not an ECDSA P-256 key (left as is)", path)
	}
	return key, nil
}

func writeKey(path string, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("lantls: marshal key: %w", err)
	}
	return writeAtomic(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600)
}
