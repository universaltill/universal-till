package pihealth

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// These tests share package-level state (the atomic status, the seam vars)
// — none of them may use t.Parallel.

func resetState(t *testing.T) {
	t.Helper()
	state.Store(Status{})
	t.Cleanup(func() { state.Store(Status{}) })
}

// withPi points deviceTreeModelPath at a temp file so isPi() reports true
// without touching the real host device tree, restoring it afterward.
func withPi(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "model")
	if err := os.WriteFile(p, []byte("Raspberry Pi 5 Model B Rev 1.0\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := deviceTreeModelPath
	deviceTreeModelPath = p
	t.Cleanup(func() { deviceTreeModelPath = old })
}

// withoutPi points deviceTreeModelPath at a path that can't exist, so
// isPi() reports false the same way it does on a thin client/mac/Windows
// box with no device tree at all.
func withoutPi(t *testing.T) {
	t.Helper()
	old := deviceTreeModelPath
	deviceTreeModelPath = filepath.Join(t.TempDir(), "does-not-exist")
	t.Cleanup(func() { deviceTreeModelPath = old })
}

func withVcgencmd(t *testing.T, out string, err error) {
	t.Helper()
	old := runVcgencmd
	runVcgencmd = func(context.Context) (string, error) { return out, err }
	t.Cleanup(func() { runVcgencmd = old })
}

// withMaxCurrentMA points maxCurrentPath at a temp file holding the given
// milliamp value, big-endian uint32 — the same shape the real device-tree
// node uses. Pass -1 for "node absent" (no PD negotiation, e.g. Pi 4).
func withMaxCurrentMA(t *testing.T, ma int64) {
	t.Helper()
	if ma < 0 {
		old := maxCurrentPath
		maxCurrentPath = filepath.Join(t.TempDir(), "does-not-exist")
		t.Cleanup(func() { maxCurrentPath = old })
		return
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "max_current")
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(ma))
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	old := maxCurrentPath
	maxCurrentPath = p
	t.Cleanup(func() { maxCurrentPath = old })
}

func TestCurrentZeroBeforeFirstCheck(t *testing.T) {
	resetState(t)
	if got := Current(); got != (Status{}) {
		t.Fatalf("Current() before any check = %+v, want zero", got)
	}
}

func TestIsPi(t *testing.T) {
	withoutPi(t)
	if isPi() {
		t.Fatal("isPi() true with no device-tree model file present")
	}
	withPi(t)
	if !isPi() {
		t.Fatal("isPi() false with a Raspberry Pi model file present")
	}
}

func TestIsPiRejectsNonPiModel(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "model")
	if err := os.WriteFile(p, []byte("Some Other Board\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := deviceTreeModelPath
	deviceTreeModelPath = p
	t.Cleanup(func() { deviceTreeModelPath = old })
	if isPi() {
		t.Fatal("isPi() true for a device-tree model that isn't a Raspberry Pi")
	}
}

func TestThrottledIndicatesUnderpower(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want bool
	}{
		{"clear", "throttled=0x0\n", false},
		{"under-voltage now (bit 0)", "throttled=0x1\n", true},
		{"under-voltage since boot, sticky (bit 16)", "throttled=0x10000\n", true},
		{"under-voltage now AND since boot (bits 0+16)", "throttled=0x10001\n", true},
		{"under-voltage since boot + throttling occurred (ut-docs#1232's own worked example: bits 16+18)", "throttled=0x50000\n", true},
		{"currently throttled only, not our concern (bit 2)", "throttled=0x4\n", false},
		{"freq-capped only, not our concern (bit 17)", "throttled=0x20000\n", false},
		{"soft-temp-limit only, not our concern (bit 3)", "throttled=0x8\n", false},
		{"malformed, no '='", "garbage\n", false},
		{"malformed hex", "throttled=0xzz\n", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := throttledIndicatesUnderpower(c.out); got != c.want {
				t.Errorf("throttledIndicatesUnderpower(%q) = %v, want %v", c.out, got, c.want)
			}
		})
	}
}

func TestNegotiatedCurrentBelowThreshold(t *testing.T) {
	t.Run("below 5A (e.g. a 3A/15W PD supply)", func(t *testing.T) {
		withMaxCurrentMA(t, 3000)
		if !negotiatedCurrentBelowThreshold() {
			t.Fatal("expected true for a negotiated 3000mA (< 5000mA)")
		}
	})
	t.Run("exactly 5A", func(t *testing.T) {
		withMaxCurrentMA(t, 5000)
		if negotiatedCurrentBelowThreshold() {
			t.Fatal("expected false at exactly the 5000mA threshold, not below it")
		}
	})
	t.Run("above 5A", func(t *testing.T) {
		withMaxCurrentMA(t, 5000+1)
		if negotiatedCurrentBelowThreshold() {
			t.Fatal("expected false for a negotiated current above 5000mA")
		}
	})
	t.Run("zero value read as no signal, not a false positive", func(t *testing.T) {
		withMaxCurrentMA(t, 0)
		if negotiatedCurrentBelowThreshold() {
			t.Fatal("a 0mA reading is almost certainly a bad/unpopulated read, not a real negotiation outcome — must not report underpowered")
		}
	})
	t.Run("node absent (e.g. Pi 4, no USB-PD negotiation)", func(t *testing.T) {
		withMaxCurrentMA(t, -1)
		if negotiatedCurrentBelowThreshold() {
			t.Fatal("a missing device-tree node must read as 'no signal', not 'underpowered'")
		}
	})
}

func TestCheckOnceNonPiNeverShellsOut(t *testing.T) {
	resetState(t)
	withoutPi(t)
	calledVcgencmd := false
	old := runVcgencmd
	runVcgencmd = func(context.Context) (string, error) { calledVcgencmd = true; return "", nil }
	t.Cleanup(func() { runVcgencmd = old })

	CheckNow(context.Background())

	if calledVcgencmd {
		t.Fatal("non-Pi platform must never shell out to vcgencmd")
	}
	if got := Current(); got.Underpowered {
		t.Fatalf("non-Pi platform reported Underpowered=true: %+v", got)
	}
}

func TestCheckOnceUnderpoweredFromThrottledBit(t *testing.T) {
	resetState(t)
	withPi(t)
	withVcgencmd(t, "throttled=0x50000\n", nil)
	withMaxCurrentMA(t, 5000) // healthy — isolates the throttled-bit signal

	got := CheckNow(context.Background())
	if !got.Underpowered {
		t.Fatalf("CheckNow = %+v, want Underpowered=true from the throttled bit", got)
	}
}

func TestCheckOnceUnderpoweredFromNegotiatedCurrentOnly(t *testing.T) {
	resetState(t)
	withPi(t)
	// vcgencmd reports clear — exactly ut-docs#1232's own observed case,
	// where no load had yet tripped the ADC-measured under-voltage bits.
	withVcgencmd(t, "throttled=0x0\n", nil)
	withMaxCurrentMA(t, 3000) // negotiated a 3A supply, below the 5A a Pi 5 wants

	got := CheckNow(context.Background())
	if !got.Underpowered {
		t.Fatalf("CheckNow = %+v, want Underpowered=true from the negotiated-current signal alone", got)
	}
}

func TestCheckOnceHealthy(t *testing.T) {
	resetState(t)
	withPi(t)
	withVcgencmd(t, "throttled=0x0\n", nil)
	withMaxCurrentMA(t, 5000)

	got := CheckNow(context.Background())
	if got.Underpowered {
		t.Fatalf("CheckNow = %+v, want Underpowered=false", got)
	}
}

func TestCheckOnceVcgencmdFailureFallsBackToNegotiatedCurrent(t *testing.T) {
	resetState(t)
	withPi(t)
	withVcgencmd(t, "", errors.New("vcgencmd: not found"))
	withMaxCurrentMA(t, 3000)

	got := CheckNow(context.Background())
	if !got.Underpowered {
		t.Fatalf("CheckNow = %+v, want Underpowered=true — vcgencmd failing must not suppress the negotiated-current signal", got)
	}
}

// Once latched true, later healthy-looking reads must not clear it — the
// chip is meant to stay up until the till restarts on a proper supply
// (review finding, ut-docs#1232): a transient read failure or the rail
// recovering under lighter load must not make an already-shown warning
// silently disappear while the PSU is still the wrong one.
func TestCheckOnceLatchesOnceTrue(t *testing.T) {
	resetState(t)
	withPi(t)
	withVcgencmd(t, "throttled=0x10000\n", nil)
	withMaxCurrentMA(t, 5000)

	if got := CheckNow(context.Background()); !got.Underpowered {
		t.Fatalf("first check = %+v, want Underpowered=true", got)
	}

	// Now every signal reports healthy — a naive re-derive-from-scratch
	// implementation would flip this back to false.
	withVcgencmd(t, "throttled=0x0\n", nil)
	withMaxCurrentMA(t, 5000)

	if got := CheckNow(context.Background()); !got.Underpowered {
		t.Fatalf("second check = %+v, want Underpowered to stay true (latched)", got)
	}
}

// Start on a non-Pi platform must be a complete no-op: wg never gets a
// goroutine registered against it, so Wait() returns immediately.
func TestStartNonPiIsNoop(t *testing.T) {
	withoutPi(t)
	var wg sync.WaitGroup
	Start(context.Background(), &wg)
	if !waitWithin(&wg, 20*time.Millisecond) {
		t.Fatal("Start registered a goroutine on a non-Pi platform")
	}
}

// On a Pi, the checker goroutine must actually be running (blocked in its
// own select) until ctx is cancelled, and must join wg promptly afterward.
func TestStartPiRunsUntilCancelled(t *testing.T) {
	withPi(t)
	withVcgencmd(t, "throttled=0x0\n", nil)
	withMaxCurrentMA(t, 5000)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	Start(ctx, &wg)
	if waitWithin(&wg, 150*time.Millisecond) {
		t.Fatal("wg.Wait() returned before ctx was even cancelled — checker goroutine not tracked")
	}
	cancel()
	if !waitWithin(&wg, 2*time.Second) {
		t.Fatal("Start's checker goroutine did not join wg within 2s of ctx cancel")
	}
}

func waitWithin(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}
