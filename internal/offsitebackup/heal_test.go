package offsitebackup

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/credheal"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
)

// ADR-0217 T3: the upload's 401 feeds the heal engine, and a heal ends the
// refusal wait and starts an upload (ut-docs#7067 FU-3).

type healKV struct{}

func (healKV) Get(context.Context, string) (string, bool, error)          { return "", false, nil }
func (healKV) Set(context.Context, string, string) error                  { return nil }
func (healKV) GetOrCreate(_ context.Context, _, d string) (string, error) { return d, nil }

func engineWithProbe(t *testing.T) *atomic.Int32 {
	t.Helper()
	var probes atomic.Int32
	credheal.SetHooks(credheal.Hooks{Probe: func(context.Context) credheal.ProbeResult {
		probes.Add(1)
		return credheal.ProbeResult{OK: true} // a healthy credential: nothing is rotated
	}})
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	credheal.Start(ctx, credheal.Deps{Cfg: &config.Config{}, KV: healKV{}, Pause: func(context.Context, time.Duration) {},
		CanRecover: func(context.Context) (bool, string) { return true, "" },
		Recover:    func(context.Context) (enroll.RecoverOutcome, error) { return enroll.RecoverOutcome{Healed: true}, nil }}, &wg)
	t.Cleanup(func() { cancel(); wg.Wait(); credheal.SetHooks(credheal.Hooks{}) })
	return &probes
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Each credential refusal reports once per round; a 403 or a refusal that is
// not about the credential does not.
func TestUploadRefusalReportsToTheEngine(t *testing.T) {
	probes := engineWithProbe(t)
	e := newTill(t)
	e.snapshot()
	e.cloud.authStatus, e.cloud.authCode = 401, "unauthorized"
	e.round(false)
	waitUntil(t, "the 401 to reach the engine (probed)", func() bool { return probes.Load() == 1 })
}

func TestUpload403AndOtherRefusalsDoNotReport(t *testing.T) {
	probes := engineWithProbe(t)
	for _, c := range []struct {
		status int
		code   string
	}{{403, "device_mismatch"}, {403, "service_unavailable"}, {409, "key_exists"}, {402, "plan_required"}} {
		e := newTill(t)
		e.snapshot()
		e.cloud.authStatus, e.cloud.authCode = c.status, c.code
		e.round(false)
	}
	time.Sleep(50 * time.Millisecond)
	if probes.Load() != 0 {
		t.Fatalf("probes = %d, want 0", probes.Load())
	}
}

// After a heal the refusal wait is cleared and an upload starts at once, not
// at the next check-in or after 24 h.
func TestHealClearsTheRefusalWaitAndStartsAnUpload(t *testing.T) {
	e := newTill(t)
	e.snapshot()
	e.cloud.authStatus, e.cloud.authCode = 401, "unauthorized"
	e.round(false)
	if e.get(data.OffsiteRefusedReasonKey) != ReasonPairAgain || e.get(data.OffsiteNextAttemptAtKey) == "" {
		t.Fatalf("setup: refusal not recorded (%q)", e.get(data.OffsiteRefusedReasonKey))
	}
	// The cloud accepts the till again (the heal gave it a new credential).
	e.cloud.authStatus = 0
	svc := &Service{Uploader: e.up, Scheduler: &Scheduler{U: e.up, First: time.Hour, Interval: time.Hour}, Settings: e.set}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { svc.Scheduler.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	svc.OnHealed(context.Background())
	if e.get(data.OffsiteRefusedReasonKey) != "" || e.get(data.OffsiteNextAttemptAtKey) != "" {
		t.Fatal("the refusal wait survived a heal")
	}
	waitUntil(t, "the upload", func() bool {
		e.cloud.mu.Lock()
		defer e.cloud.mu.Unlock()
		return len(e.cloud.backups) == 1
	})
}

// A heal does not touch an unrelated wait (a needed key link, the owner's
// approval): only a credential refusal ends.
func TestHealLeavesOtherRefusalsAlone(t *testing.T) {
	e := newTill(t)
	_ = e.set.Set(context.Background(), data.OffsiteRefusedReasonKey, ReasonNeedsLink)
	_ = e.set.Set(context.Background(), data.OffsiteNextAttemptAtKey, "2099-01-01T00:00:00Z")
	svc := &Service{Uploader: e.up, Scheduler: &Scheduler{U: e.up}, Settings: e.set}
	svc.OnHealed(context.Background())
	if e.get(data.OffsiteRefusedReasonKey) != ReasonNeedsLink || e.get(data.OffsiteNextAttemptAtKey) == "" {
		t.Fatal("a heal ended a wait that is not about the credential")
	}
}
