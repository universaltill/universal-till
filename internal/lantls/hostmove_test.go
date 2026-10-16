package lantls

import (
	"bytes"
	"crypto/ecdsa"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Rotation when a data directory moves to different hardware (ADR-0114 §7,
// ut-docs#4450): the key travels only with a copy of the whole data
// directory, so a changed host id at load starts a rotation.

// onHost makes the loads that follow read source/raw as this machine's
// host id.
func onHost(t *testing.T, source, raw string) {
	t.Helper()
	prev := hostIdentity
	hostIdentity = func() (string, string) { return source, raw }
	t.Cleanup(func() { hostIdentity = prev })
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) })
	return &buf
}

func TestHostMove_DifferentHardwareStartsARotation(t *testing.T) {
	dir := t.TempDir()
	onHost(t, "machine-id", "aaaa")
	c0, err := loadOrCreate(dir, t0)
	if err != nil {
		t.Fatal(err)
	}
	logs := captureLog(t)
	onHost(t, "machine-id", "bbbb")
	c1, err := loadOrCreate(dir, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if c1.Pin() != c0.Pin() {
		t.Fatalf("the served pin changed inside the load: %s → %s", c0.Pin(), c1.Pin())
	}
	if c1.NextPin() == "" || c1.NextPin() == c0.Pin() {
		t.Fatalf("next pin = %q; want a new key pending after a hardware move", c1.NextPin())
	}
	if !strings.Contains(logs.String(), "different hardware") {
		t.Fatalf("log doesn't name the trigger:\n%s", logs)
	}
	// The move is recorded once: a restart on the new machine keeps the
	// same pending key rather than minting another.
	c2, err := loadOrCreate(dir, t0.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if c2.NextPin() != c1.NextPin() || c2.Pin() != c0.Pin() {
		t.Fatalf("restart on the new machine: pin %s next %s; want %s next %s", c2.Pin(), c2.NextPin(), c0.Pin(), c1.NextPin())
	}
}

func TestHostMove_SameUnknownOrNewSourceNeverRotates(t *testing.T) {
	cases := []struct {
		name         string
		source, raw  string
		thenS, thenR string
	}{
		{"same machine", "machine-id", "aaaa", "machine-id", "aaaa"},
		{"id now unknown", "machine-id", "aaaa", "", ""},
		{"id was unknown", "", "", "machine-id", "bbbb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			onHost(t, tc.source, tc.raw)
			if _, err := loadOrCreate(dir, t0); err != nil {
				t.Fatal(err)
			}
			onHost(t, tc.thenS, tc.thenR)
			c, err := loadOrCreate(dir, t0.Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if c.NextPin() != "" {
				t.Fatalf("rotation started: next pin %s", c.NextPin())
			}
		})
	}
}

func TestHostMove_SourceChangeIsAMoveAndNeverSwitchesToTheCopiedKey(t *testing.T) {
	// A Pi's data folder copied to a PC: dt-serial → machine-id.
	onHost(t, "dt-serial", "10000000abcdef12")
	dir, old, copied, at := pendingRotation(t)
	c, err := loadOrCreate(dir, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ConfirmNext("till-a", copied, []string{"till-a"}); err != nil {
		t.Fatal(err)
	}
	onHost(t, "machine-id", "aaaa")
	moved, err := loadOrCreate(dir, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if moved.Pin() != old {
		t.Fatalf("switched to the copied next key after a source change: pin %s", moved.Pin())
	}
	if moved.NextPin() == "" || moved.NextPin() == copied {
		t.Fatalf("next pin = %q; want a fresh key, not the copied %s", moved.NextPin(), copied)
	}
	if got := loadState(dir).HostID; got != hostTag("machine-id", "aaaa") {
		t.Fatalf("host id = %q; want the new machine's", got)
	}
}

func TestHostMove_UpgradedTillRecordsWithoutRotating(t *testing.T) {
	// A till from before this build has a key and a rotation.json with no
	// host id: the first load records it, it never rotates on it.
	dir := t.TempDir()
	onHost(t, "", "")
	c0, err := loadOrCreate(dir, t0)
	if err != nil {
		t.Fatal(err)
	}
	onHost(t, "machine-id", "aaaa")
	c1, err := loadOrCreate(dir, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if c1.NextPin() != "" {
		t.Fatal("first recording of the host id started a rotation")
	}
	if got := loadState(dir).HostID; got == "" {
		t.Fatal("host id not recorded")
	}
	onHost(t, "machine-id", "bbbb")
	c2, err := loadOrCreate(dir, t0.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if c2.Pin() != c0.Pin() || c2.NextPin() == "" {
		t.Fatalf("move after recording: pin %s next %q; want %s with a next key", c2.Pin(), c2.NextPin(), c0.Pin())
	}
}

func TestHostMove_CopiedPendingKeyIsReplacedNotSwitchedTo(t *testing.T) {
	// A ready next key was copied along with the directory: switching to
	// it would leave the old machine holding the new key too.
	onHost(t, "machine-id", "aaaa")
	dir, old, copied, at := pendingRotation(t)
	c, err := loadOrCreate(dir, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ConfirmNext("till-a", copied, []string{"till-a"}); err != nil {
		t.Fatal(err)
	}
	if !loadState(dir).Ready {
		t.Fatal("setup: rotation not ready")
	}
	onHost(t, "machine-id", "bbbb")
	moved, err := loadOrCreate(dir, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if moved.Pin() != old {
		t.Fatalf("switched to the copied next key on the new machine: pin %s", moved.Pin())
	}
	if moved.NextPin() == "" || moved.NextPin() == copied {
		t.Fatalf("next pin = %q; want a fresh key, not the copied %s", moved.NextPin(), copied)
	}
	st := loadState(dir)
	if st.Ready || len(st.Confirmed) != 0 {
		t.Fatalf("confirmations of the copied key carried over: %+v", st)
	}
	// The window counts from the fresh key, not the copied one.
	early, err := loadOrCreate(dir, at.Add(time.Hour+RotationWindow-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if early.Pin() != old {
		t.Fatal("fresh next key switched before its own rotation window")
	}
	// Its certificate is the fresh key's, not the copied one's.
	nk, err := readKey(filepath.Join(dir, nextKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	leaf := loadLeaf(filepath.Join(dir, nextCertFile), &nk.PublicKey)
	if leaf == nil || PinOf(leaf) != moved.NextPin() {
		t.Fatal("next certificate doesn't match the fresh next key")
	}
}

func TestHostMove_FailedReplacementNeverSwitchesToTheCopiedKey(t *testing.T) {
	onHost(t, "machine-id", "aaaa")
	dir, old, copied, at := pendingRotation(t)
	c, err := loadOrCreate(dir, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ConfirmNext("till-a", copied, []string{"till-a"}); err != nil {
		t.Fatal(err)
	}
	prev := writeNextKey
	writeNextKey = func(string, *ecdsa.PrivateKey) error { return errors.New("disk full") }
	t.Cleanup(func() { writeNextKey = prev })
	onHost(t, "machine-id", "bbbb")
	moved, err := loadOrCreate(dir, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if moved.Pin() != old {
		t.Fatalf("switched to the copied next key although it could not be replaced: pin %s", moved.Pin())
	}
	if got := loadState(dir).HostID; got != hostTag("machine-id", "aaaa") {
		t.Fatalf("host id = %q; want the old one kept so a later load retries", got)
	}
	writeNextKey = prev
	retry, err := loadOrCreate(dir, at.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if retry.Pin() != old || retry.NextPin() == "" || retry.NextPin() == copied {
		t.Fatalf("retry: pin %s next %q; want %s and a fresh next key", retry.Pin(), retry.NextPin(), old)
	}
}

func TestHostMove_UnusableNextKeyIsLeftAndMoveRetried(t *testing.T) {
	dir := t.TempDir()
	onHost(t, "machine-id", "aaaa")
	c0, err := loadOrCreate(dir, t0)
	if err != nil {
		t.Fatal(err)
	}
	junk := []byte("not a key")
	if err := os.WriteFile(filepath.Join(dir, nextKeyFile), junk, 0o600); err != nil {
		t.Fatal(err)
	}
	recorded := loadState(dir).HostID
	onHost(t, "machine-id", "bbbb")
	c1, err := loadOrCreate(dir, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if c1.Pin() != c0.Pin() || c1.NextPin() != "" {
		t.Fatalf("pin %s next %q; want the current key served and nothing pending", c1.Pin(), c1.NextPin())
	}
	if b, _ := os.ReadFile(filepath.Join(dir, nextKeyFile)); !bytes.Equal(b, junk) {
		t.Fatal("unusable next key was replaced")
	}
	if got := loadState(dir).HostID; got != recorded {
		t.Fatalf("host id updated to %q although no rotation started; a later load must retry", got)
	}
	// Once the unusable file is gone, the next load rotates.
	if err := os.Remove(filepath.Join(dir, nextKeyFile)); err != nil {
		t.Fatal(err)
	}
	c2, err := loadOrCreate(dir, t0.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if c2.NextPin() == "" {
		t.Fatal("move not retried once the next key could be created")
	}
}

func TestHostMove_StateHoldsNoRawID(t *testing.T) {
	dir := t.TempDir()
	const raw = "4c4c4544003910568034b3c04f4e3332"
	onHost(t, "machine-id", raw)
	if _, err := loadOrCreate(dir, t0); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, stateFile))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(raw)) {
		t.Fatalf("rotation.json carries the raw host id: %s", b)
	}
	got := loadState(dir).HostID
	if !strings.HasPrefix(got, "machine-id:") || len(got) != len("machine-id:")+64 {
		t.Fatalf("host_id = %q; want machine-id:<64 hex>", got)
	}
}

func TestHostTag(t *testing.T) {
	if hostTag("", "x") != "" || hostTag("machine-id", "") != "" {
		t.Fatal("an empty source or id must give no tag")
	}
	if hostTag("machine-id", "a") == hostTag("machine-id", "b") {
		t.Fatal("different ids give the same tag")
	}
	if hostTag("machine-id", "a") == hostTag("dt-serial", "a") {
		t.Fatal("the source isn't part of the tag")
	}
}

func TestCleanHostID(t *testing.T) {
	for in, want := range map[string]string{
		"10000000abcdef12\x00":                 "10000000abcdef12",
		" 6f1c...ab\n":                         "6f1c...ab",
		"":                                     "",
		"\x00":                                 "",
		"0000000000000000\x00":                 "",
		"uninitialized\n":                      "",
		"00000000-0000-0000-0000-000000000000": "",
	} {
		if got := cleanHostID(in); got != want {
			t.Errorf("cleanHostID(%q) = %q, want %q", in, got, want)
		}
	}
}
