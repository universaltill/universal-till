package lantls

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

// Key rotation (ADR-0114 §7, ut-docs#4409): every decision is taken at
// load, so the served pin is fixed for a process lifetime.

func exists(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Stat(p)
	return err == nil
}

// pendingRotation returns a dir whose key is due for rotation, loaded at
// t0+RotateEvery: a next key is pending.
func pendingRotation(t *testing.T) (dir string, old, next string, at time.Time) {
	t.Helper()
	dir = t.TempDir()
	c0, err := loadOrCreate(dir, t0)
	if err != nil {
		t.Fatal(err)
	}
	at = t0.Add(RotateEvery)
	c1, err := loadOrCreate(dir, at)
	if err != nil {
		t.Fatal(err)
	}
	if c1.NextPin() == "" {
		t.Fatal("no next key after RotateEvery")
	}
	return dir, c0.Pin(), c1.NextPin(), at
}

func TestRotation_FourYearsCreatesNextKeyAndKeepsServingCurrent(t *testing.T) {
	dir := t.TempDir()
	c0, err := loadOrCreate(dir, t0)
	if err != nil {
		t.Fatal(err)
	}
	if c0.NextPin() != "" {
		t.Fatalf("fresh key has a pending next pin %s", c0.NextPin())
	}
	early, err := loadOrCreate(dir, t0.Add(RotateEvery-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if early.NextPin() != "" {
		t.Fatal("next key created before RotateEvery")
	}
	c1, err := loadOrCreate(dir, t0.Add(RotateEvery))
	if err != nil {
		t.Fatal(err)
	}
	if c1.Pin() != c0.Pin() {
		t.Fatalf("creating the next key changed the served pin: %s → %s", c0.Pin(), c1.Pin())
	}
	if c1.NextPin() == "" || c1.NextPin() == c0.Pin() {
		t.Fatalf("next pin = %q; want a new key's pin", c1.NextPin())
	}
	if runtime.GOOS != "windows" {
		for _, f := range []string{nextKeyFile, nextCertFile, stateFile} {
			if m := mode(t, filepath.Join(dir, f)); m != 0o600 {
				t.Errorf("%s mode = %o, want 600", f, m)
			}
		}
	}
	// A restart keeps the same pending key rather than minting another.
	c2, err := loadOrCreate(dir, t0.Add(RotateEvery+time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if c2.NextPin() != c1.NextPin() || c2.Pin() != c0.Pin() {
		t.Fatalf("restart: pin %s next %s; want %s next %s", c2.Pin(), c2.NextPin(), c0.Pin(), c1.NextPin())
	}
}

func TestRotation_AllRequiredConfirmedSwitchesAtNextLoad(t *testing.T) {
	dir, old, next, at := pendingRotation(t)
	c, err := loadOrCreate(dir, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	required := []string{"till-a", "till-b"}
	// A wrong or empty pin, or a till outside the required set, confirms
	// nothing.
	for _, bad := range [][2]string{{"till-a", old}, {"till-a", ""}, {"till-x", next}} {
		if err := c.ConfirmNext(bad[0], bad[1], required); err != nil {
			t.Fatal(err)
		}
	}
	if !c.NeedsConfirm("till-a", next) {
		t.Fatal("till-a confirmed by a wrong pin or another till")
	}
	if err := c.ConfirmNext("till-a", next, required); err != nil {
		t.Fatal(err)
	}
	if c.NeedsConfirm("till-a", next) {
		t.Fatal("till-a still needs to confirm after confirming")
	}
	if c.NeedsConfirm("till-a", old) {
		t.Fatal("a pin other than the pending one needs confirming")
	}
	mid, err := loadOrCreate(dir, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if mid.Pin() != old || mid.NextPin() != next {
		t.Fatalf("switched with one of two tills confirmed: pin %s next %s", mid.Pin(), mid.NextPin())
	}
	if err := c.ConfirmNext("till-b", next, required); err != nil {
		t.Fatal(err)
	}
	sw, err := loadOrCreate(dir, at.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if sw.Pin() != next {
		t.Fatalf("served pin after all confirmed = %s; want the next pin %s", sw.Pin(), next)
	}
	if sw.NextPin() != "" {
		t.Fatalf("a next pin %s is still pending after the switch", sw.NextPin())
	}
	if !exists(t, filepath.Join(dir, prevKeyFile)) || !exists(t, filepath.Join(dir, prevCertFile)) {
		t.Fatal("previous key not kept after the switch")
	}
	if exists(t, filepath.Join(dir, nextKeyFile)) {
		t.Fatal("next key file left behind after the switch")
	}
	prev, err := loadOrCreateKey(filepath.Join(dir, prevKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := pinOfKey(prev); p != old {
		t.Fatalf("prev key pin = %s; want the retired %s", p, old)
	}
	// The served certificate is issued on the new key.
	if PinOf(sw.leaf) != next {
		t.Fatal("served certificate is not on the new key")
	}
	// The new key is not due again for RotateEvery.
	again, err := loadOrCreate(dir, at.Add(RotateEvery-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if again.NextPin() != "" {
		t.Fatal("a next key was created right after a switch")
	}
}

func TestRotation_ConfirmedSetIsBoundToRequired(t *testing.T) {
	dir, _, next, at := pendingRotation(t)
	c, err := loadOrCreate(dir, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ConfirmNext("till-a", next, []string{"till-a", "till-b"}); err != nil {
		t.Fatal(err)
	}
	// till-b was revoked meanwhile: till-a's earlier confirmation plus
	// none needed from till-b makes it ready.
	if err := c.ConfirmNext("till-c", next, []string{"till-a", "till-c"}); err != nil {
		t.Fatal(err)
	}
	st := loadState(dir)
	if !st.Ready || !slices.Equal(st.Confirmed, []string{"till-a", "till-c"}) {
		t.Fatalf("state = %+v; want ready with till-a, till-c", st)
	}
}

func TestRotation_SwitchesAfterRotationWindowWithoutConfirmations(t *testing.T) {
	dir, old, next, at := pendingRotation(t)
	before, err := loadOrCreate(dir, at.Add(RotationWindow-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if before.Pin() != old {
		t.Fatal("switched before the rotation window ended")
	}
	after, err := loadOrCreate(dir, at.Add(RotationWindow))
	if err != nil {
		t.Fatal(err)
	}
	if after.Pin() != next || after.NextPin() != "" {
		t.Fatalf("after %v: pin %s next %q; want %s and none pending", RotationWindow, after.Pin(), after.NextPin(), next)
	}
}

func TestRotation_PreviousKeyDeletedAfterKeepPrevious(t *testing.T) {
	dir, _, next, at := pendingRotation(t)
	switchAt := at.Add(RotationWindow)
	if _, err := loadOrCreate(dir, switchAt); err != nil {
		t.Fatal(err)
	}
	c, err := loadOrCreate(dir, switchAt.Add(KeepPrevious-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !exists(t, filepath.Join(dir, prevKeyFile)) {
		t.Fatal("previous key deleted before KeepPrevious")
	}
	c, err = loadOrCreate(dir, switchAt.Add(KeepPrevious))
	if err != nil {
		t.Fatal(err)
	}
	if exists(t, filepath.Join(dir, prevKeyFile)) || exists(t, filepath.Join(dir, prevCertFile)) {
		t.Fatal("previous key still on disk after KeepPrevious")
	}
	if c.Pin() != next {
		t.Fatalf("pin = %s; want %s", c.Pin(), next)
	}
}

// The previous key is never served: the TLS config carries only the
// current certificate.
func TestRotation_PreviousKeyIsNotServed(t *testing.T) {
	dir, _, next, at := pendingRotation(t)
	c, err := loadOrCreate(dir, at.Add(RotationWindow))
	if err != nil {
		t.Fatal(err)
	}
	cfg := c.TLSConfig()
	if len(cfg.Certificates) != 1 || PinOf(cfg.Certificates[0].Leaf) != next {
		t.Fatal("TLS config serves something other than the one current key")
	}
}

func TestRotation_CorruptNextKeyKeepsServingCurrent(t *testing.T) {
	dir, old, _, at := pendingRotation(t)
	np := filepath.Join(dir, nextKeyFile)
	junk := []byte("not a key")
	if err := os.WriteFile(np, junk, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := loadOrCreate(dir, at.Add(RotationWindow+time.Hour))
	if err != nil {
		t.Fatalf("a corrupt next key turned TLS off: %v", err)
	}
	if c.Pin() != old {
		t.Fatalf("pin = %s; want the current %s", c.Pin(), old)
	}
	if c.NextPin() != "" {
		t.Fatalf("a corrupt next key is announced as %s", c.NextPin())
	}
	if got, _ := os.ReadFile(np); string(got) != string(junk) {
		t.Fatal("corrupt next key was overwritten")
	}
}

func TestRotation_KeyCreatedAtFallsBackToKeyFileMtime(t *testing.T) {
	dir := t.TempDir()
	c0, err := loadOrCreate(dir, t0)
	if err != nil {
		t.Fatal(err)
	}
	// A key from a build before rotation: no state file, old mtime.
	if err := os.Remove(filepath.Join(dir, stateFile)); err != nil {
		t.Fatal(err)
	}
	old := t0.Add(-RotateEvery)
	if err := os.Chtimes(filepath.Join(dir, privateKeyFile), old, old); err != nil {
		t.Fatal(err)
	}
	c1, err := loadOrCreate(dir, t0)
	if err != nil {
		t.Fatal(err)
	}
	if c1.Pin() != c0.Pin() || c1.NextPin() == "" {
		t.Fatalf("pin %s next %q; want %s with a next key (key file is 4 years old)", c1.Pin(), c1.NextPin(), c0.Pin())
	}
	if st := loadState(dir); !st.KeyCreatedAt.Equal(old) {
		t.Fatalf("key_created_at = %v; want the key file's mtime %v", st.KeyCreatedAt, old)
	}
}

// A power cut between moving the next key into place and updating the
// state: the next load finishes the switch instead of rotating again.
func TestRotation_InterruptedSwitchIsFinished(t *testing.T) {
	dir, _, next, at := pendingRotation(t)
	for _, mv := range [][2]string{{nextKeyFile, privateKeyFile}, {nextCertFile, certFile}} {
		if err := os.Rename(filepath.Join(dir, mv[0]), filepath.Join(dir, mv[1])); err != nil {
			t.Fatal(err)
		}
	}
	c, err := loadOrCreate(dir, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if c.Pin() != next || c.NextPin() != "" {
		t.Fatalf("pin %s next %q; want %s and nothing pending", c.Pin(), c.NextPin(), next)
	}
	st := loadState(dir)
	if !st.KeyCreatedAt.Equal(at) || st.NextPin != "" {
		t.Fatalf("state = %+v; want key_created_at %v and no next", st, at)
	}
}

// A leftover next key equal to the current one (a copy instead of a move)
// is cleaned up, never announced.
func TestRotation_LeftoverNextEqualToCurrentIsRemoved(t *testing.T) {
	dir := t.TempDir()
	c0, err := loadOrCreate(dir, t0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, privateKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, nextKeyFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
	c1, err := loadOrCreate(dir, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if c1.Pin() != c0.Pin() || c1.NextPin() != "" {
		t.Fatalf("pin %s next %q; want %s and nothing pending", c1.Pin(), c1.NextPin(), c0.Pin())
	}
	if exists(t, filepath.Join(dir, nextKeyFile)) {
		t.Fatal("leftover next key equal to the current one was kept")
	}
}

func TestRotation_PendingNextCertIsRenewedOnItsKey(t *testing.T) {
	dir, _, next, at := pendingRotation(t)
	if err := os.Remove(filepath.Join(dir, nextCertFile)); err != nil {
		t.Fatal(err)
	}
	c, err := loadOrCreate(dir, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if c.NextPin() != next {
		t.Fatalf("next pin = %s; want %s", c.NextPin(), next)
	}
	key, err := loadOrCreateKey(filepath.Join(dir, nextKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if loadLeaf(filepath.Join(dir, nextCertFile), &key.PublicKey) == nil {
		t.Fatal("missing next certificate not re-issued on the next key")
	}
}

func TestServing(t *testing.T) {
	c, err := loadOrCreate(t.TempDir(), t0)
	if err != nil {
		t.Fatal(err)
	}
	restore := SetServing(c)
	if Serving() != c {
		t.Fatal("Serving() is not the cert set")
	}
	restore()
	if Serving() == c {
		t.Fatal("restore did not put the previous value back")
	}
}

// A Pi with no RTC can create its key while the clock still reads 1970.
// That age is not real: it must not rotate the key the moment the clock is
// set, so an implausible creation time restarts the count from now.
func TestRotation_ImplausibleKeyCreationTimeRestartsTheCount(t *testing.T) {
	dir := t.TempDir()
	epoch := time.Unix(0, 0).UTC()
	c0, err := loadOrCreate(dir, epoch)
	if err != nil {
		t.Fatal(err)
	}
	c1, err := loadOrCreate(dir, t0)
	if err != nil {
		t.Fatal(err)
	}
	if c1.Pin() != c0.Pin() || c1.NextPin() != "" {
		t.Fatalf("pin %s next %q; want %s and no rotation", c1.Pin(), c1.NextPin(), c0.Pin())
	}
	if st := loadState(dir); !st.KeyCreatedAt.Equal(t0) {
		t.Fatalf("key_created_at = %v; want restarted at %v", st.KeyCreatedAt, t0)
	}
}

// More required tills than ConfirmNext tracks never make the rotation
// ready (ut-docs#4409 review): it falls back to the RotationWindow ceiling
// instead of switching on a truncated set's confirmations.
func TestRotation_TooManyRequiredNeverReady(t *testing.T) {
	dir, old, next, at := pendingRotation(t)
	c, err := loadOrCreate(dir, at)
	if err != nil {
		t.Fatal(err)
	}
	required := make([]string, maxConfirmed+1)
	for i := range required {
		required[i] = fmt.Sprintf("till-%03d", i)
	}
	for _, id := range required {
		if err := c.ConfirmNext(id, next, required); err != nil {
			t.Fatal(err)
		}
	}
	if st := loadState(dir); st.Ready {
		t.Fatal("ready with more required tills than maxConfirmed")
	}
	if len(loadState(dir).Confirmed) > maxConfirmed {
		t.Fatalf("confirmed set grew to %d; want ≤ %d", len(loadState(dir).Confirmed), maxConfirmed)
	}
	again, err := loadOrCreate(dir, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if again.Pin() != old {
		t.Fatal("switched before the rotation window with an untracked required set")
	}
}

// A power cut between moving the next key and the next certificate leaves
// next-cert.pem with no next key: it is removed, not left to be mistaken
// for a pending rotation's certificate later.
func TestRotation_OrphanNextCertIsRemoved(t *testing.T) {
	dir, _, next, at := pendingRotation(t)
	if err := os.Rename(filepath.Join(dir, nextKeyFile), filepath.Join(dir, privateKeyFile)); err != nil {
		t.Fatal(err)
	}
	c, err := loadOrCreate(dir, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if c.Pin() != next {
		t.Fatalf("pin = %s; want %s", c.Pin(), next)
	}
	if exists(t, filepath.Join(dir, nextCertFile)) {
		t.Fatal("orphan next-cert.pem left with no next key")
	}
	// …and an orphan with no rotation in the state at all.
	if err := os.WriteFile(filepath.Join(dir, nextCertFile), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreate(dir, at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if exists(t, filepath.Join(dir, nextCertFile)) {
		t.Fatal("orphan next-cert.pem left with no next key and no rotation pending")
	}
}
