package discovery

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// openTestSettings mirrors internal/data/settings_repo_test.go's minimal
// in-memory setup — this package only ever touches the settings table via
// *data.SettingsRepo's own Get/Set, never raw SQL, so a real schema for the
// rest of the DB is unnecessary here.
func openTestSettings(t *testing.T) *data.SettingsRepo {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if _, err := sqlDB.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`); err != nil {
		t.Fatalf("create settings: %v", err)
	}
	return data.NewSettingsRepo(sqlDB)
}

// openFileBackedTestSettings is openTestSettings's real-multi-connection
// counterpart: a ":memory:" DSN gives every pooled connection its OWN
// isolated database (see internal/data's TestAddBarcodeConcurrentRace
// comment, ut-docs#304), so it can't exercise real cross-connection locking
// at all. Needed specifically for the concurrency test below.
func openFileBackedTestSettings(t *testing.T) *data.SettingsRepo {
	t.Helper()
	dbh, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbh.Close() })
	return data.NewSettingsRepo(dbh.DB)
}

func TestTillID_CreatesAndPersistsOnFirstCall(t *testing.T) {
	settings := openTestSettings(t)
	ctx := context.Background()

	id, err := TillID(ctx, settings)
	if err != nil {
		t.Fatalf("TillID: %v", err)
	}
	if id == "" {
		t.Fatal("expected a non-empty generated till id")
	}

	stored, ok, err := settings.Get(ctx, TillIDSettingKey)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || stored != id {
		t.Fatalf("expected the generated id %q persisted under %q, got %q (ok=%v)", id, TillIDSettingKey, stored, ok)
	}
}

func TestTillID_IsStableAcrossCalls(t *testing.T) {
	settings := openTestSettings(t)
	ctx := context.Background()

	first, err := TillID(ctx, settings)
	if err != nil {
		t.Fatalf("TillID (first): %v", err)
	}
	second, err := TillID(ctx, settings)
	if err != nil {
		t.Fatalf("TillID (second): %v", err)
	}
	if first != second {
		t.Fatalf("expected the same id on every call (get-or-create), got %q then %q", first, second)
	}
}

func TestTillID_ReturnsExistingValueWithoutOverwriting(t *testing.T) {
	settings := openTestSettings(t)
	ctx := context.Background()

	const preSeeded = "till-already-here"
	if err := settings.Set(ctx, TillIDSettingKey, preSeeded); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := TillID(ctx, settings)
	if err != nil {
		t.Fatalf("TillID: %v", err)
	}
	if got != preSeeded {
		t.Fatalf("expected the pre-existing id %q preserved, got %q", preSeeded, got)
	}
}

// ut-docs#271: on a genuinely fresh DB, two concurrent first callers (e.g.
// the advertiser's tick and an incoming pairing request racing at boot) must
// converge on the same till id, not each mint and persist their own with
// last-write-wins silently stranding one of them.
func TestTillID_ConcurrentCallersOnFreshDBConverge(t *testing.T) {
	ctx := context.Background()
	settings := openFileBackedTestSettings(t)

	// TillID always keys off the one constant TillIDSettingKey, so unlike a
	// parameterized get-or-create there's no fresh-key-per-round trick
	// available — clear that one row between rounds instead (reopening the
	// whole DB per round, migrations and all, drowned out the race window
	// entirely: 15 rounds of that never reproduced it once). A single round
	// can still miss the race window by chance, so repeat and assert every
	// time, same approach as TestAddBarcodeConcurrentRace /
	// TestSettingsRepo_GetOrCreate_ConcurrentCallersConverge.
	const rounds = 30
	const callersPerRound = 8
	for round := 0; round < rounds; round++ {
		if err := settings.Delete(ctx, TillIDSettingKey); err != nil {
			t.Fatalf("round %d: Delete: %v", round, err)
		}
		ids := make([]string, callersPerRound)
		errs := make([]error, callersPerRound)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(callersPerRound)
		for i := 0; i < callersPerRound; i++ {
			go func(i int) {
				defer wg.Done()
				<-start // released together to maximise the race window
				ids[i], errs[i] = TillID(ctx, settings)
			}(i)
		}
		close(start)
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d: TillID[%d]: %v", round, i, err)
			}
		}
		want := ids[0]
		if want == "" {
			t.Fatalf("round %d: expected a non-empty generated till id", round)
		}
		for i, got := range ids {
			if got != want {
				t.Fatalf("round %d: caller %d got id %q, caller 0 got %q — concurrent boot callers disagree on this till's id", round, i, got, want)
			}
		}

		stored, ok, err := settings.Get(ctx, TillIDSettingKey)
		if err != nil {
			t.Fatalf("round %d: Get: %v", round, err)
		}
		if !ok || stored != want {
			t.Fatalf("round %d: persisted till id %q (ok=%v) doesn't match what every caller agreed on (%q)", round, stored, ok, want)
		}
	}
}

func TestStoreNameOrDefault_FallsBackWhenUnset(t *testing.T) {
	settings := openTestSettings(t)
	ctx := context.Background()

	if got := storeNameOrDefault(ctx, settings); got != "this shop" {
		t.Fatalf(`expected fallback "this shop", got %q`, got)
	}

	if err := settings.Set(ctx, "store.name", "Task Runner"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := storeNameOrDefault(ctx, settings); got != "Task Runner" {
		t.Fatalf("expected the configured store name, got %q", got)
	}
}

// TestRoleCheckFromSettings_* covers the ACTUAL role-check logic wired into
// production (internal/app.Run passes this to discovery.NewAdvertiser) —
// not just Advertiser's tick logic against a synthetic injected bool
// (that's advertiser_test.go's job). Before this, the "empty
// sync.primary_url means primary" rule was duplicated as an inline closure
// in app.go with zero test coverage of its own: nothing would fail if that
// closure's condition were accidentally inverted. This exercises the real
// settings-backed rule directly, against a real *data.SettingsRepo, the
// same way app.go wires it.
func TestRoleCheckFromSettings_TrueWhenPrimaryURLUnset(t *testing.T) {
	settings := openTestSettings(t)
	check := RoleCheckFromSettings(settings)

	if !check(context.Background()) {
		t.Fatal("expected primary role (true) when sync.primary_url is unset")
	}
}

func TestRoleCheckFromSettings_FalseWhenPrimaryURLSet(t *testing.T) {
	settings := openTestSettings(t)
	ctx := context.Background()
	if err := settings.Set(ctx, "sync.primary_url", "https://primary.example"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	check := RoleCheckFromSettings(settings)

	if check(ctx) {
		t.Fatal("expected replica role (false) when sync.primary_url is set")
	}
}

func TestRoleCheckFromSettings_TrueWhenPrimaryURLWhitespaceOnly(t *testing.T) {
	settings := openTestSettings(t)
	ctx := context.Background()
	if err := settings.Set(ctx, "sync.primary_url", "   "); err != nil {
		t.Fatalf("Set: %v", err)
	}
	check := RoleCheckFromSettings(settings)

	if !check(ctx) {
		t.Fatal("expected primary role (true) when sync.primary_url is whitespace-only")
	}
}

// fakeFirstBootChecker stands in for *auth.Service in these tests — the
// gate only ever calls NeedsFirstBoot, so that's all this needs to
// implement (same narrowed-seam pattern as RoleCheck and mdnsServer).
type fakeFirstBootChecker struct {
	needsFirstBoot bool
	err            error
}

func (f fakeFirstBootChecker) NeedsFirstBoot(context.Context) (bool, error) {
	return f.needsFirstBoot, f.err
}

// TestGateOnFirstBoot_AdvertisesOnlyWhenPrimaryAndSetupComplete pins the
// four-way truth table (ut-docs#1263): a fresh/unconfigured till reads as
// "primary" by RoleCheckFromSettings' own rule (empty sync.primary_url) —
// this gate is what stops it also advertising itself as a join target
// before a human has ever opened the setup wizard.
func TestGateOnFirstBoot_AdvertisesOnlyWhenPrimaryAndSetupComplete(t *testing.T) {
	cases := []struct {
		name           string
		inner          bool
		needsFirstBoot bool
		want           bool
	}{
		{"primary, setup complete: advertise", true, false, true},
		{"primary, setup NOT complete: withhold (the bug this closes)", true, true, false},
		{"replica, setup complete: withhold (already gated by inner)", false, false, false},
		{"replica, setup NOT complete: withhold", false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner := func(context.Context) bool { return tc.inner }
			gated := GateOnFirstBoot(inner, fakeFirstBootChecker{needsFirstBoot: tc.needsFirstBoot})

			if got := gated(context.Background()); got != tc.want {
				t.Fatalf("GateOnFirstBoot(inner=%v, needsFirstBoot=%v) = %v, want %v", tc.inner, tc.needsFirstBoot, got, tc.want)
			}
		})
	}
}

// TestGateOnFirstBoot_FailsClosedOnCheckerError: if the first-boot check
// itself errors, this must not fall back to advertising blind — a till
// that can't confirm its own setup state is not something anyone else
// should be discovering as a join target.
func TestGateOnFirstBoot_FailsClosedOnCheckerError(t *testing.T) {
	inner := func(context.Context) bool { return true }
	gated := GateOnFirstBoot(inner, fakeFirstBootChecker{err: errors.New("db unavailable")})

	if gated(context.Background()) {
		t.Fatal("expected withhold (false) when the first-boot checker errors, got advertise (true)")
	}
}

// TestGateOnFirstBoot_ShortCircuitsWithoutCallingCheckerWhenNotPrimary
// confirms the gate doesn't pay for (or depend on) a first-boot check at
// all on the already-common replica path — same short-circuit shape as
// Advertiser.tick's own primary/replica switch.
func TestGateOnFirstBoot_ShortCircuitsWithoutCallingCheckerWhenNotPrimary(t *testing.T) {
	inner := func(context.Context) bool { return false }
	called := false
	checker := firstBootCheckerFunc(func(context.Context) (bool, error) {
		called = true
		return false, nil
	})
	gated := GateOnFirstBoot(inner, checker)

	if gated(context.Background()) {
		t.Fatal("expected withhold (false) when inner reports not-primary")
	}
	if called {
		t.Fatal("expected the first-boot checker not to be called when inner already reports not-primary")
	}
}

// firstBootCheckerFunc adapts a plain func to FirstBootChecker, mirroring
// the standard http.HandlerFunc adapter shape, for the one test above that
// needs to observe whether it was called rather than just its return value.
type firstBootCheckerFunc func(context.Context) (bool, error)

func (f firstBootCheckerFunc) NeedsFirstBoot(ctx context.Context) (bool, error) { return f(ctx) }
