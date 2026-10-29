package auth

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeClock is an injectable Service.now so lockout tests never sleep.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newLockoutService(t *testing.T) (*Service, *fakeClock) {
	t.Helper()
	db := openAuthTestDB(t)
	seedOperator(t, db, "alice", "cashier", "1122")
	svc := NewService(db)
	clk := &fakeClock{t: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
	svc.now = clk.now
	return svc, clk
}

// failRound makes maxFailedAttempts wrong guesses, tripping a lockout.
func failRound(t *testing.T, svc *Service) {
	t.Helper()
	for i := range maxFailedAttempts {
		if _, _, err := svc.Login(context.Background(), "9999"); err != ErrInvalidPIN {
			t.Fatalf("guess %d: err = %v, want ErrInvalidPIN", i, err)
		}
	}
}

// wantLockFor asserts the device is locked for exactly d: still locked one
// tick before, unlocked at the boundary.
func wantLockFor(t *testing.T, svc *Service, clk *fakeClock, d time.Duration) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := svc.Login(ctx, "1122"); err != ErrLockedOut {
		t.Fatalf("right after lockout: err = %v, want ErrLockedOut", err)
	}
	clk.advance(d - time.Second)
	if _, _, err := svc.Login(ctx, "1122"); err != ErrLockedOut {
		t.Fatalf("1s before %v: err = %v, want ErrLockedOut", d, err)
	}
	clk.advance(time.Second)
	if svc.locked() {
		t.Fatalf("at %v the lock should have expired", d)
	}
}

func TestLockoutEscalates(t *testing.T) {
	svc, clk := newLockoutService(t)
	for _, d := range []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute} {
		failRound(t, svc)
		wantLockFor(t, svc, clk, d)
	}
}

func TestLockoutCapped(t *testing.T) {
	svc, clk := newLockoutService(t)
	for range 12 {
		failRound(t, svc)
		clk.advance(maxLockoutDuration) // always expires within the cap
	}
	failRound(t, svc)
	wantLockFor(t, svc, clk, maxLockoutDuration)
	failRound(t, svc)
	wantLockFor(t, svc, clk, maxLockoutDuration)
	if svc.lockouts > 63 {
		t.Fatalf("lockouts level kept growing past the cap: %d", svc.lockouts)
	}
}

func TestLockoutLevelSurvivesSuccess(t *testing.T) {
	svc, clk := newLockoutService(t)
	ctx := context.Background()
	failRound(t, svc)
	wantLockFor(t, svc, clk, 30*time.Second)

	// A legitimate login resets the failure count but not the level.
	for range maxFailedAttempts - 1 {
		_, _, _ = svc.Login(ctx, "9999")
	}
	if _, _, err := svc.Login(ctx, "1122"); err != nil {
		t.Fatalf("legit login: %v", err)
	}
	for i := range maxFailedAttempts - 1 { // count restarted: 4 more must not lock
		if _, _, err := svc.Login(ctx, "9999"); err != ErrInvalidPIN {
			t.Fatalf("guess %d after success: err = %v, want ErrInvalidPIN", i, err)
		}
	}
	if _, _, err := svc.Login(ctx, "9999"); err != ErrInvalidPIN {
		t.Fatalf("5th guess: err = %v", err)
	}
	wantLockFor(t, svc, clk, time.Minute) // level kept: 60 s, not 30 s
}

func TestLockoutLevelDecaysAfterQuietHour(t *testing.T) {
	svc, clk := newLockoutService(t)
	failRound(t, svc)
	wantLockFor(t, svc, clk, 30*time.Second)
	failRound(t, svc)
	wantLockFor(t, svc, clk, time.Minute)

	clk.advance(lockoutQuietReset + time.Second)
	failRound(t, svc)
	wantLockFor(t, svc, clk, 30*time.Second)
}

func TestLockoutSharedByManagerAndPINChange(t *testing.T) {
	svc, clk := newLockoutService(t)
	ctx := context.Background()
	// Failures through different endpoints add up to one lockout.
	_, _, _ = svc.Login(ctx, "9999")
	_, _ = svc.AuthorizeManager(ctx, "9999")
	_ = svc.ChangeOwnPIN(ctx, "alice", "9999", "4321")
	_, _ = svc.AuthorizeManager(ctx, "9999")
	_ = svc.ChangeOwnPIN(ctx, "alice", "9999", "4321")
	if err := svc.ChangeOwnPIN(ctx, "alice", "1122", "4321"); err != ErrLockedOut {
		t.Fatalf("ChangeOwnPIN during lockout: err = %v, want ErrLockedOut", err)
	}
	if _, err := svc.AuthorizeManager(ctx, "1122"); err != ErrLockedOut {
		t.Fatalf("AuthorizeManager during lockout: err = %v, want ErrLockedOut", err)
	}
	clk.advance(lockoutDuration)
	if svc.locked() {
		t.Fatal("first lockout should last lockoutDuration")
	}
}

// Concurrent guesses must not all slip past the lockout check before any of
// them is counted: only maxFailedAttempts may reach the PIN comparison per
// window, however many arrive at once (ut-docs#5141 review).
func TestLockoutHoldsUnderConcurrentGuesses(t *testing.T) {
	db := openAuthTestDB(t)
	// :memory: is per connection; pin the pool to the one holding the schema.
	db.SetMaxOpenConns(1)
	seedOperator(t, db, "alice", "cashier", "1122")
	svc := NewService(db)
	const n = 40
	errs := make(chan error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, err := svc.Login(context.Background(), "9999")
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	invalid := 0
	for err := range errs {
		switch err {
		case ErrInvalidPIN:
			invalid++
		case ErrLockedOut:
		default:
			t.Fatalf("unexpected err %v", err)
		}
	}
	if invalid > maxFailedAttempts {
		t.Fatalf("%d concurrent guesses were checked, want at most %d", invalid, maxFailedAttempts)
	}
}
