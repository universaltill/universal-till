package pages

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/selfupdate"
	"github.com/universaltill/universal-till/internal/updates"
)

// Replica follows its main till's version (ut-docs#2738, ADR-0114 §5).
//
// #2726 left a replica's nightly update off: "latest" could run it ahead of
// a main till that can't update itself. Instead, a replica installs EXACTLY
// its main till's version, from GitHub (never from the main till), at the
// first 30 s scheduler tick with no open sale. The target is the version in
// the live link's hello, else the one GET /api/sync/ping last answered with
// (sync.main_version), so a polling replica follows too. The existing ticker
// gives "at start", "on a new hello/ping" and the periodic re-check for free.
// Main and standalone tills never get here: their nightly path is unchanged.
//
// Offline-first: this runs on the scheduler goroutine, never in a sale; a
// GitHub outage is just a failed attempt, tried again at the next start.

const (
	keyFollowAttempted = data.UpdateFollowAttemptedSettingsKey // per-till: the target last tried
	keyFollowError     = data.UpdateFollowErrorSettingsKey     // per-till: "", followApplying or "failed:<code>"
	keyMainVersion     = "sync.main_version"                   // per-till (sync.*): the version the main till's ping answered with
)

// followApplying marks an attempt in progress. A till that restarts with it
// still set was interrupted mid-download, so it tries again.
const followApplying = "applying"

// autoUpdateApplyVersion is a seam so tests never call the real
// selfupdate.ApplyVersion (which would re-exec the test binary).
var autoUpdateApplyVersion = selfupdate.ApplyVersionWhenIdle

// followRun is the target this process has already attempted: one attempt
// per target per run, so a failing release never loops. Kept in memory on
// purpose — a restart is the retry.
var followRun followAttempt

type followAttempt struct {
	mu     sync.Mutex
	target string
}

func (a *followAttempt) get() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.target
}

func (a *followAttempt) set(v string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.target = v
}

// followForced is the target an operator's "Update now" asked for on the
// main till's Tills page (ut-docs#2945): a fleet frame naming the version
// the main till's own hello names. In memory on purpose — one attempt per
// press: followTick clears it when the attempt starts, and a restart
// forgets an unspent press.
var followForced followAttempt

// followKick asks the auto-update scheduler to run a tick now instead of at
// its next 30 s tick (capacity 1: presses coalesce). The scheduler is the
// only goroutine that ever applies an update, so a press can never start a
// second, concurrent install.
var followKick = make(chan struct{}, 1)

// onFleetUpdate takes a fleet frame from the main till (ADR-0114 §5). It is
// accepted only as "update now" to exactly the version the main till's
// hello names (mainVersion) — the frame cannot point this till at any
// other release (§7: frames can do little). Everything else the follow
// rule still decides: replica, newer release, UT_UPDATE_CHECK, an install
// that can replace itself, no open sale. Never blocks the link loop.
func onFleetUpdate(f fleetlink.FleetPayload, mainVersion string) {
	target := normVersion(f.Target)
	if !f.Now || !releaseVersion(target) || target != normVersion(mainVersion) {
		logging.L().Infof("auto-update: ignored an update request for %q (main till runs %q)", f.Target, mainVersion)
		return
	}
	followForced.set(target)
	logging.L().Infof("auto-update: the main till asked this till to update to v%s now", target)
	select {
	case followKick <- struct{}{}:
	default: // a kick is already pending
	}
}

// followInputs is everything the follow rule reads, gathered by
// followInputsOf so the rule itself stays pure and table-tested.
type followInputs struct {
	Replica          bool   // sync.primary_url is set
	This, Target     string // this build's version; the main till's
	AutoEnabled      string // update.auto_enabled as replicated from the main till
	ChecksOn         bool   // updates.Enabled(): UT_UPDATE_CHECK not switched off
	Supported        bool   // selfupdate.Supported()
	Busy             bool   // an open basket, kiosk basket or table session
	AttemptedThisRun string // followRun
	LastAttempted    string // keyFollowAttempted
	LastError        string // keyFollowError
	Forced           bool   // an "Update now" press names Target (followForced)
}

// normVersion drops the optional leading v (updates.Newer compares bare
// dotted numbers, and would read "v1.4.0" as 0).
func normVersion(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }

// followSettled reports whether the last run's attempt at target ended in a
// way that must not simply repeat: failed, or staged without the till ever
// arriving on target (a release whose binary doesn't report its tag would
// otherwise restart the till every 30 s).
func followSettled(in followInputs) (failed, noEffect bool) {
	if normVersion(in.LastAttempted) != normVersion(in.Target) {
		return false, false
	}
	return strings.HasPrefix(in.LastError, "failed:"), in.LastError == ""
}

// followDecision is the follow rule. It acts only when the main till runs a
// newer release than this one (never a downgrade, never past it), the shop
// hasn't switched updates off, this install can replace itself, no sale is
// open, and this target hasn't already been tried — once per run, and not
// again after an attempt that took effect without moving the version.
//
// An operator's "Update now" (Forced, ut-docs#2945) overrides only the
// shop's automatic-updates switch and the once-per-run / no-effect guards —
// a press is one more deliberate attempt. It never overrides a dev build,
// a downgrade, UT_UPDATE_CHECK, an install that can't replace itself, or
// an open sale.
func followDecision(in followInputs) (act bool, why string) {
	switch {
	case !in.Replica:
		return false, "not_replica"
	case !releaseVersion(in.This) || !releaseVersion(in.Target):
		return false, "not_release" // a dev build never self-updates; an unknown target is no target
	case !updates.Newer(normVersion(in.Target), normVersion(in.This)):
		return false, "not_newer"
	case !in.ChecksOn, in.AutoEnabled == "false" && !in.Forced:
		return false, "off"
	case !in.Supported:
		return false, "unsupported"
	case !in.Forced && normVersion(in.AttemptedThisRun) == normVersion(in.Target):
		return false, "attempted"
	}
	if _, noEffect := followSettled(in); noEffect && !in.Forced {
		return false, "no_effect"
	}
	if in.Busy {
		return false, "busy"
	}
	if in.Forced {
		return true, "update_now"
	}
	return true, "follow"
}

// followBehind reports whether this till is a replica running an older
// release than its main till.
func followBehind(in followInputs) bool {
	return in.Replica && releaseVersion(in.This) && releaseVersion(in.Target) &&
		updates.Newer(normVersion(in.Target), normVersion(in.This))
}

// followReportState is the update_state this till reports to its main
// till (ADR-0114 §2's states only; the Tills page roster, ut-docs#2945):
// "downloading" while this run's attempt at the target is in progress,
// "waiting-safe-moment" while that attempt (or one that would start now)
// waits for an open sale to end, "failed:<code>" when the last attempt at
// it failed, and "failed:unsupported" when it is behind and can't replace
// itself (portable Windows zip, Android, unwritable install) — the roster
// reads that one as "needs the installer".
func followReportState(in followInputs) string {
	if !followBehind(in) {
		return "idle"
	}
	target := normVersion(in.Target)
	if normVersion(in.LastAttempted) == target {
		switch {
		case in.LastError == followApplying && normVersion(in.AttemptedThisRun) == target:
			// ApplyVersionWhenIdle downloads, then holds the restart until
			// no sale is open: with one open, say it is waiting.
			if in.Busy {
				return "waiting-safe-moment"
			}
			return "downloading"
		case strings.HasPrefix(in.LastError, "failed:"):
			return in.LastError
		}
	}
	if !in.Supported {
		return "failed:unsupported"
	}
	if _, why := followDecision(in); why == "busy" {
		return "waiting-safe-moment"
	}
	return "idle"
}

// followCanInstall is what the chip says about a newer target: true when
// this till will install it by itself (now, or once the sale is done),
// false when someone has to — Android/portable Windows zip/unwritable, switched off, or
// this target's attempt failed or didn't take.
func followCanInstall(in followInputs) bool {
	if !in.Supported || in.AutoEnabled == "false" || !in.ChecksOn {
		return false
	}
	failed, noEffect := followSettled(in)
	return !failed && !noEffect
}

// followLine is what Settings → Software update says on an additional
// till (ut-docs#2949, #4031, #4053): "follows" only when this till will
// install the main till's version by itself. Otherwise the reason, in
// followDecision's order: "main_off" (automatic updates off on the main
// till), "checks_off" (UT_UPDATE_CHECK switched off on this till),
// "manual" (behind, and this install can't replace itself) or "failed"
// (behind, and the last attempt at this target failed or didn't take).
func followLine(in followInputs) string {
	switch {
	case in.AutoEnabled == "false":
		return "main_off"
	case !in.ChecksOn:
		return "checks_off"
	case !followBehind(in):
		return "follows"
	case !in.Supported:
		return "manual"
	}
	if failed, noEffect := followSettled(in); failed || noEffect {
		return "failed"
	}
	return "follows"
}

// followTargetOf picks the target: the live link's hello version, else the
// last pinged one.
func followTargetOf(hasClient bool, st fleetlink.ClientStatus, stored string) string {
	if hasClient && st.Linked && strings.TrimSpace(st.MainVersion) != "" {
		return strings.TrimSpace(st.MainVersion)
	}
	return strings.TrimSpace(stored)
}

// followTarget is the main till's version as this till knows it now.
func followTarget(ctx context.Context, d *common.Deps) string {
	stored, _, _ := d.Settings.Get(ctx, keyMainVersion)
	var st fleetlink.ClientStatus
	if d.LinkClient != nil {
		st = d.LinkClient.Status()
	}
	return followTargetOf(d.LinkClient != nil, st, stored)
}

// autoUpdateBusy: an unattended restart would destroy an open basket. Both
// engines (ut-docs#449) and table-QR sessions (ADR-0103, ut-docs#2261);
// d.KioskEngine is nil in some test harnesses, HasItems is nil-safe.
// HasItemsOrByHand(), not ItemCount() (ut-docs#3596): a resumed kiosk
// pay-at-counter order can be live with zero priced lines and only "add by
// hand" ones (ut-docs#3586) — its held_sales row is already gone, so an
// unattended restart that treats it as idle loses the order outright.
func autoUpdateBusy(d *common.Deps) bool {
	return d.Engine.HasItemsOrByHand() ||
		(d.KioskEngine != nil && d.KioskEngine.HasItemsOrByHand()) ||
		d.SelfOrderSessions.HasItems()
}

// autoUpdateIdle is the restart gate for an unattended update: the release
// can take minutes to download, during trading hours on a replica, so the
// restart waits until no sale is open (ut-docs#2738 review).
func autoUpdateIdle(d *common.Deps) func() bool {
	return func() bool { return !autoUpdateBusy(d) }
}

// followInputsOf reads the inputs for this till now.
func followInputsOf(ctx context.Context, d *common.Deps) followInputs {
	get := func(key string) string {
		v, _, _ := d.Settings.Get(ctx, key)
		return strings.TrimSpace(v)
	}
	in := followInputs{
		Replica:          get("sync.primary_url") != "",
		This:             autoUpdateBuildVersion(),
		Target:           followTarget(ctx, d),
		AutoEnabled:      get(keyAutoUpdateEnabled),
		ChecksOn:         updates.Enabled(),
		Busy:             d.Engine != nil && autoUpdateBusy(d),
		AttemptedThisRun: followRun.get(),
		LastAttempted:    get(keyFollowAttempted),
		LastError:        get(keyFollowError),
	}
	if forced := followForced.get(); forced != "" {
		if forced == normVersion(in.Target) {
			in.Forced = true
		} else {
			// The main till has moved on since the press: that target is
			// gone, so the press is spent rather than kept forever.
			followForced.set("")
		}
	}
	// Supported() probes the disk (a temp file in the exe dir and the cwd)
	// and the chip polls every 5 s on every till: only ask when a replica is
	// actually behind its main till (ut-docs#2738 review).
	if followBehind(in) {
		in.Supported = autoUpdateSupported()
	}
	return in
}

// followTick runs one follow decision; autoUpdateTick sends a replica here
// instead of the nightly path. The attempt is recorded BEFORE applying (in
// memory and per-till), so neither a failure nor a crash mid-download can
// turn into a download loop.
func followTick(ctx context.Context, d *common.Deps) {
	in := followInputsOf(ctx, d)
	if act, _ := followDecision(in); !act {
		return
	}
	target := normVersion(in.Target)
	if in.Forced {
		followForced.set("") // one attempt per press
	}
	followRun.set(target)
	_ = d.Settings.Set(ctx, keyFollowAttempted, target)
	_ = d.Settings.Set(ctx, keyFollowError, followApplying)
	logging.L().Infof("auto-update: following the main till to v%s", target)
	if err := autoUpdateApplyVersion(ctx, target, autoUpdateIdle(d)); err != nil {
		_ = d.Settings.Set(ctx, keyFollowError, "failed:"+followErrorCode(err))
		logging.L().Errorf("auto-update: following the main till to v%s: %v", target, err)
		return
	}
	_ = d.Settings.Set(ctx, keyFollowError, "")
}

// followErrorCode shortens an ApplyVersion error to the code stored as
// failed:<code> (the full error goes to the log).
func followErrorCode(err error) string {
	if errors.Is(err, selfupdate.ErrUnsupported) {
		return "unsupported"
	}
	var netErr *url.Error
	msg := err.Error()
	switch {
	case errors.As(err, &netErr):
		return "network"
	case strings.Contains(msg, "already being applied"):
		return "busy"
	case strings.Contains(msg, "checksum"):
		return "checksum"
	case strings.Contains(msg, "no release archive"), strings.Contains(msg, "no macOS .dmg"):
		return "no_archive"
	case strings.HasPrefix(msg, "releases API"):
		return "release"
	case strings.HasPrefix(msg, "download"):
		return "download"
	}
	return "install"
}
