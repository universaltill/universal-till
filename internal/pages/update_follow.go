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
func followDecision(in followInputs) (act bool, why string) {
	switch {
	case !in.Replica:
		return false, "not_replica"
	case !releaseVersion(in.This) || !releaseVersion(in.Target):
		return false, "not_release" // a dev build never self-updates; an unknown target is no target
	case !updates.Newer(normVersion(in.Target), normVersion(in.This)):
		return false, "not_newer"
	case in.AutoEnabled == "false" || !in.ChecksOn:
		return false, "off"
	case !in.Supported:
		return false, "unsupported"
	case normVersion(in.AttemptedThisRun) == normVersion(in.Target):
		return false, "attempted"
	}
	if _, noEffect := followSettled(in); noEffect {
		return false, "no_effect"
	}
	if in.Busy {
		return false, "busy"
	}
	return true, "follow"
}

// followCanInstall is what the chip says about a newer target: true when
// this till will install it by itself (now, or once the sale is done),
// false when someone has to — Windows/Android/unwritable, switched off, or
// this target's attempt failed or didn't take.
func followCanInstall(in followInputs) bool {
	if !in.Supported || in.AutoEnabled == "false" || !in.ChecksOn {
		return false
	}
	failed, noEffect := followSettled(in)
	return !failed && !noEffect
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
func autoUpdateBusy(d *common.Deps) bool {
	return d.Engine.Basket().ItemCount() > 0 ||
		(d.KioskEngine != nil && d.KioskEngine.Basket().ItemCount() > 0) ||
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
	// Supported() probes the disk (a temp file in the exe dir and the cwd)
	// and the chip polls every 5 s on every till: only ask when a replica is
	// actually behind its main till (ut-docs#2738 review).
	if in.Replica && releaseVersion(in.This) && releaseVersion(in.Target) &&
		updates.Newer(normVersion(in.Target), normVersion(in.This)) {
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
