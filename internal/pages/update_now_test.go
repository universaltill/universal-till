package pages

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#2945 (LAN half): the main till's Tills page "Update now" sends a
// fleet frame; the replica installs its main till's version at the next
// safe moment even when automatic updates are switched off — once per press.

func TestFollowDecision_Forced(t *testing.T) {
	base := followInputs{Replica: true, This: "1.3.0", Target: "1.4.0", Supported: true, ChecksOn: true, Forced: true}
	with := func(f func(*followInputs)) followInputs { in := base; f(&in); return in }
	for _, c := range []struct {
		name string
		in   followInputs
		act  bool
		why  string
	}{
		{"pressed: installs", base, true, "update_now"},
		{"pressed with automatic updates off: still installs", with(func(in *followInputs) { in.AutoEnabled = "false" }), true, "update_now"},
		{"pressed after this run already tried: tries again", with(func(in *followInputs) { in.AttemptedThisRun = "1.4.0" }), true, "update_now"},
		{"pressed after an attempt without effect: tries again", with(func(in *followInputs) { in.LastAttempted, in.LastError = "1.4.0", "" }), true, "update_now"},
		{"pressed after a failure: tries again", with(func(in *followInputs) {
			in.LastAttempted, in.LastError, in.AttemptedThisRun = "1.4.0", "failed:network", "1.4.0"
		}), true, "update_now"},
		{"still waits for the open sale", with(func(in *followInputs) { in.Busy = true }), false, "busy"},
		{"still never on a main till", with(func(in *followInputs) { in.Replica = false }), false, "not_replica"},
		{"still never a dev build", with(func(in *followInputs) { in.This = "dev" }), false, "not_release"},
		{"still never a dev target", with(func(in *followInputs) { in.Target = "dev" }), false, "not_release"},
		{"still never a downgrade", with(func(in *followInputs) { in.Target = "1.2.0" }), false, "not_newer"},
		{"still nothing when equal", with(func(in *followInputs) { in.Target = "1.3.0" }), false, "not_newer"},
		{"UT_UPDATE_CHECK off still wins", with(func(in *followInputs) { in.ChecksOn = false }), false, "off"},
		{"an install that can't replace itself still can't", with(func(in *followInputs) { in.Supported = false }), false, "unsupported"},
	} {
		t.Run(c.name, func(t *testing.T) {
			act, why := followDecision(c.in)
			if act != c.act || why != c.why {
				t.Fatalf("followDecision(%+v) = (%v, %q), want (%v, %q)", c.in, act, why, c.act, c.why)
			}
		})
	}
}

// What the replica reports to its main till's Tills page.
func TestFollowReportState(t *testing.T) {
	base := followInputs{Replica: true, This: "1.3.0", Target: "1.4.0", Supported: true, ChecksOn: true}
	with := func(f func(*followInputs)) followInputs { in := base; f(&in); return in }
	for _, c := range []struct {
		name string
		in   followInputs
		want string
	}{
		{"behind and about to follow: idle", base, "idle"},
		{"up to date", with(func(in *followInputs) { in.Target = "1.3.0" }), "idle"},
		{"main or standalone till", with(func(in *followInputs) { in.Replica = false }), "idle"},
		{"downloading now", with(func(in *followInputs) {
			in.LastAttempted, in.LastError, in.AttemptedThisRun = "1.4.0", followApplying, "1.4.0"
		}), "downloading"},
		{"interrupted last run (not downloading in this one)", with(func(in *followInputs) {
			in.LastAttempted, in.LastError = "1.4.0", followApplying
		}), "idle"},
		{"failed at this target", with(func(in *followInputs) { in.LastAttempted, in.LastError = "1.4.0", "failed:checksum" }), "failed:checksum"},
		{"failed at an older target", with(func(in *followInputs) { in.LastAttempted, in.LastError = "1.3.5", "failed:checksum" }), "idle"},
		{"behind, can't install itself", with(func(in *followInputs) { in.Supported = false }), "failed:unsupported"},
		{"downloaded, restart held for an open sale", with(func(in *followInputs) {
			in.LastAttempted, in.LastError, in.AttemptedThisRun, in.Busy = "1.4.0", followApplying, "1.4.0", true
		}), "waiting-safe-moment"},
		{"would follow but a sale is open", with(func(in *followInputs) { in.Busy = true }), "waiting-safe-moment"},
		{"switched off and a sale open: nothing to wait for", with(func(in *followInputs) { in.Busy, in.AutoEnabled = true, "false" }), "idle"},
		{"pressed Update now, switched off, a sale open: waiting", with(func(in *followInputs) {
			in.Busy, in.AutoEnabled, in.Forced = true, "false", true
		}), "waiting-safe-moment"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := followReportState(c.in); got != c.want {
				t.Fatalf("followReportState(%+v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// drainFollowKick empties the scheduler's kick channel before and after a test.
func drainFollowKick(t *testing.T) {
	t.Helper()
	drain := func() {
		select {
		case <-followKick:
		default:
		}
	}
	drain()
	t.Cleanup(func() { drain(); followForced.set("") })
	followForced.set("")
}

// A fleet frame is accepted only as "update now" to the version the main
// till's own hello names — a frame can't point this till at another
// release (ADR-0114 §7).
func TestOnFleetUpdate_AcceptsOnlyTheMainTillsVersion(t *testing.T) {
	drainFollowKick(t)
	kicked := func() bool {
		select {
		case <-followKick:
			return true
		default:
			return false
		}
	}
	for _, c := range []struct {
		name   string
		f      fleetlink.FleetPayload
		main   string
		accept bool
	}{
		{"now, the hello's version", fleetlink.FleetPayload{Target: "1.4.0", Now: true}, "1.4.0", true},
		{"v-prefix either side", fleetlink.FleetPayload{Target: "v1.4.0", Now: true}, "1.4.0", true},
		{"not now", fleetlink.FleetPayload{Target: "1.4.0"}, "1.4.0", false},
		{"another version than the hello's", fleetlink.FleetPayload{Target: "1.9.0", Now: true}, "1.4.0", false},
		{"no hello version", fleetlink.FleetPayload{Target: "1.4.0", Now: true}, "", false},
		{"not a release", fleetlink.FleetPayload{Target: "dev", Now: true}, "dev", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			followForced.set("")
			onFleetUpdate(c.f, c.main)
			if got := followForced.get() != ""; got != c.accept {
				t.Fatalf("forced = %q, want accepted=%v", followForced.get(), c.accept)
			}
			if got := kicked(); got != c.accept {
				t.Fatalf("scheduler kicked = %v, want %v", got, c.accept)
			}
		})
	}
}

// Pressed with automatic updates off: the next tick installs the main
// till's version; the press is spent, so a later tick does not repeat it.
func TestFollowTick_UpdateNowInstallsOnceWithUpdatesOff(t *testing.T) {
	drainFollowKick(t)
	dp := newFollowReplicaDeps(t, "1.4.0")
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateEnabled, "false")
	applied := stubFollowSeams(t, "1.3.0", true, nil)

	autoUpdateTick(t.Context(), dp, tickNow)
	if len(*applied) != 0 {
		t.Fatalf("switched off and not pressed: applied %v", *applied)
	}
	onFleetUpdate(fleetlink.FleetPayload{Target: "1.4.0", Now: true}, "1.4.0")
	autoUpdateTick(t.Context(), dp, tickNow.Add(30*time.Second))
	autoUpdateTick(t.Context(), dp, tickNow.Add(60*time.Second))
	if len(*applied) != 1 || (*applied)[0] != "1.4.0" {
		t.Fatalf("applied %v, want exactly one ApplyVersion(1.4.0)", *applied)
	}
	if followForced.get() != "" {
		t.Fatalf("the press is still pending after its attempt: %q", followForced.get())
	}
}

// A failed attempt that a press retries: this run already tried, a press
// tries once more.
func TestFollowTick_UpdateNowRetriesAfterAFailure(t *testing.T) {
	drainFollowKick(t)
	dp := newFollowReplicaDeps(t, "1.4.0")
	applied := stubFollowSeams(t, "1.3.0", true, errors.New("download: HTTP 503"))
	autoUpdateTick(t.Context(), dp, tickNow)
	autoUpdateTick(t.Context(), dp, tickNow.Add(30*time.Second))
	if len(*applied) != 1 {
		t.Fatalf("applied %v, want one attempt before the press", *applied)
	}
	onFleetUpdate(fleetlink.FleetPayload{Target: "1.4.0", Now: true}, "1.4.0")
	autoUpdateTick(t.Context(), dp, tickNow.Add(60*time.Second))
	if len(*applied) != 2 {
		t.Fatalf("applied %v, want the press to retry once", *applied)
	}
}

// A press during a sale waits: the flag stays, and the first tick after the
// sale installs.
func TestFollowTick_UpdateNowWaitsForTheOpenSale(t *testing.T) {
	drainFollowKick(t)
	dp := newFollowReplicaDeps(t, "1.4.0")
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateEnabled, "false")
	applied := stubFollowSeams(t, "1.3.0", true, nil)
	dp.Engine.AddLineWithModifiers(pos.BasketLine{SKU: "sku-1", Name: "Coffee", Qty: 1, PriceCents: 250}, 1, nil)

	onFleetUpdate(fleetlink.FleetPayload{Target: "1.4.0", Now: true}, "1.4.0")
	autoUpdateTick(t.Context(), dp, tickNow)
	if len(*applied) != 0 {
		t.Fatalf("installed mid-sale: %v", *applied)
	}
	if in := followInputsOf(t.Context(), dp); followReportState(in) != "waiting-safe-moment" {
		t.Fatalf("report state = %q mid-sale after a press, want waiting-safe-moment", followReportState(in))
	}
	dp.Engine = pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000}, nil) // the sale is done
	autoUpdateTick(t.Context(), dp, tickNow.Add(30*time.Second))
	if len(*applied) != 1 {
		t.Fatalf("applied %v once the sale cleared, want one attempt", *applied)
	}
}

// The scheduler acts on a kick at once rather than at its next 30 s tick.
func TestAutoUpdateScheduler_RunsATickOnKick(t *testing.T) {
	drainFollowKick(t)
	dp := newFollowReplicaDeps(t, "1.4.0")
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateEnabled, "false")
	done := make(chan string, 1)
	stubFollowSeams(t, "1.3.0", true, nil)
	autoUpdateApplyVersion = func(_ context.Context, v string, _ func() bool) error {
		done <- v
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	StartAutoUpdateScheduler(ctx, dp, &wg)
	defer func() { cancel(); wg.Wait() }()

	onFleetUpdate(fleetlink.FleetPayload{Target: "1.4.0", Now: true}, "1.4.0")
	select {
	case v := <-done:
		if v != "1.4.0" {
			t.Fatalf("applied %q, want 1.4.0", v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a kick did not run the follow before the 30 s tick")
	}
}
