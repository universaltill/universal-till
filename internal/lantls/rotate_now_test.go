package lantls

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// "Rotate now" (ADR-0114 §7's on-demand trigger, ut-docs#4423): the
// manager starts the same rotation the 4-year step does, at runtime.

func TestRotateNow_CreatesNextKeyAndKeepsServingCurrent(t *testing.T) {
	dir := t.TempDir()
	c, err := loadOrCreate(dir, t0)
	if err != nil {
		t.Fatal(err)
	}
	old := c.Pin()
	pin, started, err := c.rotateNow(t0.Add(time.Hour))
	if err != nil || !started {
		t.Fatalf("rotateNow = %q, %v, %v; want a new rotation", pin, started, err)
	}
	if pin == "" || pin == old {
		t.Fatalf("next pin %q; want a new key's pin (current %s)", pin, old)
	}
	if c.Pin() != old || c.NextPin() != pin {
		t.Fatalf("pin %s next %s; want %s next %s", c.Pin(), c.NextPin(), old, pin)
	}
	for _, f := range []string{nextKeyFile, nextCertFile, stateFile} {
		if !exists(t, filepath.Join(dir, f)) {
			t.Errorf("%s missing", f)
		} else if runtime.GOOS != "windows" {
			if m := mode(t, filepath.Join(dir, f)); m != 0o600 {
				t.Errorf("%s mode = %o, want 600", f, m)
			}
		}
	}
	// A restart takes it as the pending rotation, exactly like a
	// 4-year one: same served pin, same next pin.
	c2, err := loadOrCreate(dir, t0.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if c2.Pin() != old || c2.NextPin() != pin {
		t.Fatalf("restart: pin %s next %s; want %s next %s", c2.Pin(), c2.NextPin(), old, pin)
	}
}

func TestRotateNow_PendingRotationIsKept(t *testing.T) {
	dir := t.TempDir()
	c, err := loadOrCreate(dir, t0)
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := c.rotateNow(t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ConfirmNext("till-a", first, []string{"till-a", "till-b"}); err != nil {
		t.Fatal(err)
	}
	again, started, err := c.rotateNow(t0.Add(2 * time.Hour))
	if err != nil || started || again != first {
		t.Fatalf("second rotateNow = %q, %v, %v; want the pending %q, not started", again, started, err, first)
	}
	if c.NeedsConfirm("till-a", first) {
		t.Fatal("a second press dropped till-a's confirmation")
	}
	// A 4-year-pending rotation is kept the same way.
	dir2, _, next, at := pendingRotation(t)
	c3, err := loadOrCreate(dir2, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got, started, err := c3.rotateNow(at.Add(time.Hour)); err != nil || started || got != next {
		t.Fatalf("rotateNow over a pending 4-year rotation = %q, %v, %v; want %q", got, started, err, next)
	}
}

func TestRotateNow_ConfirmedSwitchesAtNextLoad(t *testing.T) {
	dir := t.TempDir()
	c, err := loadOrCreate(dir, t0)
	if err != nil {
		t.Fatal(err)
	}
	at := t0.Add(time.Hour)
	pin, _, err := c.rotateNow(at)
	if err != nil {
		t.Fatal(err)
	}
	if !c.NeedsConfirm("till-a", pin) {
		t.Fatal("a fresh rotation needs no confirmation")
	}
	if err := c.ConfirmNext("till-a", pin, []string{"till-a"}); err != nil {
		t.Fatal(err)
	}
	c2, err := loadOrCreate(dir, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if c2.Pin() != pin || c2.NextPin() != "" {
		t.Fatalf("after confirmation: pin %s next %q; want %s and none pending", c2.Pin(), c2.NextPin(), pin)
	}
	if !exists(t, filepath.Join(dir, prevKeyFile)) {
		t.Fatal("the retired key was not kept")
	}
}

func TestRotateNow_UnconfirmedSwitchesAfterRotationWindow(t *testing.T) {
	dir := t.TempDir()
	c, err := loadOrCreate(dir, t0)
	if err != nil {
		t.Fatal(err)
	}
	at := t0.Add(time.Hour)
	pin, _, err := c.rotateNow(at)
	if err != nil {
		t.Fatal(err)
	}
	early, err := loadOrCreate(dir, at.Add(RotationWindow-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if early.NextPin() != pin {
		t.Fatalf("switched before RotationWindow: pin %s next %q", early.Pin(), early.NextPin())
	}
	late, err := loadOrCreate(dir, at.Add(RotationWindow))
	if err != nil {
		t.Fatal(err)
	}
	if late.Pin() != pin || late.NextPin() != "" {
		t.Fatalf("at RotationWindow: pin %s next %q; want %s", late.Pin(), late.NextPin(), pin)
	}
}

func TestRotateNow_NoTLS(t *testing.T) {
	var nilCert *Cert
	if _, _, err := nilCert.RotateNow(); !errors.Is(err, ErrNoTLS) {
		t.Fatalf("nil cert: err %v, want ErrNoTLS", err)
	}
	if _, _, err := (&Cert{}).RotateNow(); !errors.Is(err, ErrNoTLS) {
		t.Fatalf("cert with no dir: err %v, want ErrNoTLS", err)
	}
}

// The pin a hello announces can now change while the link serves: the
// reads are race-free (go test -race).
func TestRotateNow_ConcurrentReads(t *testing.T) {
	c, err := loadOrCreate(t.TempDir(), t0)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				p := c.NextPin()
				_ = c.NeedsConfirm("till-a", p)
			}
		}()
	}
	if _, _, err := c.rotateNow(t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
}

func TestRotateNow_UnusableNextKeyIsNotReplaced(t *testing.T) {
	dir, _, _, at := pendingRotation(t)
	np := filepath.Join(dir, nextKeyFile)
	junk := []byte("not a key")
	if err := os.WriteFile(np, junk, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := loadOrCreate(dir, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, started, err := c.rotateNow(at.Add(time.Hour)); err == nil || started {
		t.Fatalf("rotateNow over an unusable next key: started %v err %v; want a refusal", started, err)
	}
	if got, _ := os.ReadFile(np); string(got) != string(junk) {
		t.Fatal("the unusable next key was overwritten")
	}
	if c.NextPin() != "" {
		t.Fatalf("announced %s", c.NextPin())
	}
}
