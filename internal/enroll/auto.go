package enroll

import (
	"context"
	"errors"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/logging"
)

// Automatic registration (ADR-0200, ut-docs#6559; supersedes the lazy-only
// rule of ADR-0015 and the wizard opt-in of ADR-0071).
//
// A set-up till registers itself from ONE background goroutine, never on the
// sale, setup or start path (ADR-0003): it retries with capped exponential
// backoff and jitter until it succeeds or the cloud refuses it, and it re-asks
// the honesty guards before every attempt so a till that is stopped, held or
// not yet told about the data it sends never sends anything.
//
// NoticeVersion names the owner-approved notice text
// (legal-reviews/2026-11-17-auto-registration-6559.md section 5). Nothing here
// sends before the notice has been shown.

// Settings keys of the registration notice and stop switch. The notice
// keys are per-till (a replica never checks in itself); KeyStopped is
// shop-wide (ut-docs#6618).
const (
	// KeyNoticeShownAt (RFC 3339) is when this till first showed the notice;
	// KeyNoticeVersion is the NoticeVersion it showed.
	KeyNoticeShownAt = "registration.notice_shown_at"
	KeyNoticeVersion = "registration.notice_version"
	// KeyStopped is "true" while the shop has switched "Send usage facts" off.
	KeyStopped = "registration.stopped"
	// KeyLastReportedVersion is the software version last compared for a
	// version event (first ever start: stored, no event).
	KeyLastReportedVersion = "registration.last_reported_version"
	// KeyLastCheckinAt (RFC 3339) is the last time the cloud answered this
	// till's sync POST with a 2xx or 402; the Settings card shows it.
	KeyLastCheckinAt = "registration.last_checkin_at"

	// KeyAutoRegistered is "true" while this till's cloud identity was made by
	// the automatic (anonymous) register and no operator action (claim code,
	// pairing, a named register) has happened since. While it is set, an
	// unpaid till's check-ins carry no shop name and no till name, and its
	// start-up check-in obeys the same honesty guards as register (owner
	// decision, ADR-0200).
	KeyAutoRegistered = "registration.auto_registered"

	keySetupCompleted = "setup.completed"
)

// NoticeVersion is the version of the registration notice text the till shows
// (owner-approved text checkin-notice-v2; the legal draft v1 was a different text). A material change of what
// is sent bumps it, which re-shows the notice before anything more is sent.
const NoticeVersion = "checkin-notice-v2"

// DenyCountries are the shop countries a till never registers or reports for.
// It must equal ut-cloud's default CLOUD_REFUSED_COUNTRIES (IR): a test pins
// the value and the cloud side is documented in the wire contract. Compiled in
// on purpose (ADR-0145: no services for Iran, nothing crosses planes).
var DenyCountries = map[string]bool{"IR": true} // core-neutral:allow ADR-0145 sanctions refusal compiled in on purpose; must equal ut-cloud CLOUD_REFUSED_COUNTRIES (ut-docs#6559)

// Why a till does not register or report, for logs and tests.
const (
	ReasonDevBuild   = "dev_build"
	ReasonDemo       = "demo"
	ReasonNever      = "never_register"
	ReasonStopped    = "stopped"
	ReasonSetup      = "setup_incomplete"
	ReasonNotice     = "notice_not_shown"
	ReasonCountry    = "country_denied"
	ReasonReplica    = "replica"
	ReasonHeld       = "held"
	ReasonBlocked    = "blocked"
	ReasonRegistered = "registered"
)

var releaseVersionRe = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+$`)

// IsReleaseVersion reports whether v looks like a real release (x.y.z). A
// source build ("dev") or a pre-release stamp is not a real till.
func IsReleaseVersion(v string) bool { return releaseVersionRe.MatchString(strings.TrimSpace(v)) }

// e2eSeam is the test-only seam (UT_E2E_AUTO_REGISTER, ADR-0200): an e2e till
// that must really register against its in-process fake cloud. cfg only sets
// it for a loopback marketplace endpoint, and it is honoured here only on a
// source build (never a release-stamped binary), and it waives only the
// dev-build and never-register guards: demo, stopped, notice, country and
// replica still apply. Real, CI-smoke and device-test paths never set it.
func e2eSeam(cfg *config.Config) bool {
	return cfg != nil && cfg.E2EAutoRegister && !IsReleaseVersion(buildinfo.Version)
}

// reportingAllowedWith is the one honesty predicate shared by registration
// and change events: release build, not a demo, not a throwaway test till,
// not stopped, set up, notice shown, a served country, not a replica.
func reportingAllowedWith(cfg *config.Config, get func(string) string) (bool, string) {
	seam := e2eSeam(cfg)
	if !seam && !IsReleaseVersion(buildinfo.Version) {
		return false, ReasonDevBuild
	}
	if cfg != nil && cfg.Demo {
		return false, ReasonDemo
	}
	if cfg != nil && cfg.NeverRegister && !seam {
		return false, ReasonNever
	}
	if get(KeyStopped) == "true" {
		return false, ReasonStopped
	}
	if get(keySetupCompleted) != "true" {
		return false, ReasonSetup
	}
	if get(KeyNoticeShownAt) == "" || get(KeyNoticeVersion) != NoticeVersion {
		return false, ReasonNotice
	}
	if DenyCountries[countryCode(get(StoreCountrySettingsKey))] {
		return false, ReasonCountry
	}
	if strings.TrimSpace(get(keySyncPrimaryURL)) != "" {
		return false, ReasonReplica
	}
	return true, ""
}

// ReportingPolicy is the policy internal/data records change events under:
// the same guards as registration (the till reports nothing it would not
// register for). The return type is a plain func so data needs no import of
// this package.
func ReportingPolicy(cfg *config.Config) func(get func(key string) string) bool {
	return func(get func(string) string) bool {
		ok, _ := reportingAllowedWith(cfg, get)
		return ok
	}
}

func kvGetter(ctx context.Context, kv Settings) func(string) string {
	return func(key string) string {
		v, _, err := kv.Get(ctx, key)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(v)
	}
}

// AutoRegistered reports KeyAutoRegistered.
func AutoRegistered(ctx context.Context, kv Settings) bool {
	return kvGetter(ctx, kv)(KeyAutoRegistered) == "true"
}

func setAutoRegistered(ctx context.Context, kv Settings, on bool) {
	v := ""
	if on {
		v = "true"
	}
	if kvGetter(ctx, kv)(KeyAutoRegistered) == v {
		return
	}
	if err := kv.Set(ctx, KeyAutoRegistered, v); err != nil {
		logging.L().Warnf("enrolment: remember registration origin: %v", err)
	}
}

// MarkOperatorClaimed records that the operator took an explicit step (minted
// a claim code): the till is no longer treated as merely auto-registered.
func MarkOperatorClaimed(ctx context.Context, kv Settings) { setAutoRegistered(ctx, kv, false) }

// registrationStopped reports whether the shop switched "Send usage facts" off.
func registrationStopped(ctx context.Context, kv Settings) bool {
	return kvGetter(ctx, kv)(KeyStopped) == "true"
}

// ReportingAllowed is reportingAllowedWith over kv.
func ReportingAllowed(ctx context.Context, cfg *config.Config, kv Settings) (bool, string) {
	return reportingAllowedWith(cfg, kvGetter(ctx, kv))
}

// AutoRegisterAllowed reports whether the background loop may register this
// till right now, and why not. Local reads only; it never touches the network.
func AutoRegisterAllowed(ctx context.Context, cfg *config.Config, kv Settings) (bool, string) {
	if st := CurrentStatus(); st.Registered || st.ViaMainTill {
		return false, ReasonRegistered
	}
	if ok, why := ReportingAllowed(ctx, cfg, kv); !ok {
		return false, why
	}
	// A cloud pairing is held (my.-first wizard, ADR-0166, ut-docs#5127), or the
	// cloud said this device was recently live / refused its key (ut-docs#5145).
	if RegistrationHeld() || !RegisterBlockedUntil(ctx, kv).IsZero() || !KeyRefusedAt(ctx, kv).IsZero() {
		return false, ReasonHeld
	}
	return true, ""
}

// neverReports: guards that cannot change while the process runs, so the loop
// is not even started.
func neverReports(cfg *config.Config) (bool, string) {
	seam := e2eSeam(cfg)
	switch {
	case !seam && !IsReleaseVersion(buildinfo.Version):
		return true, ReasonDevBuild
	case cfg.Demo:
		return true, ReasonDemo
	case cfg.NeverRegister && !seam:
		return true, ReasonNever
	}
	return false, ""
}

// RegistrationState is what the Settings card shows.
type RegistrationState string

const (
	StateRegistered    RegistrationState = "registered"
	StateNotRegistered RegistrationState = "not_registered"
	StatePaused        RegistrationState = "paused"
)

// CardStatus is the status-only "Cloud record" card.
type CardStatus struct {
	State       RegistrationState
	LastCheckin time.Time // zero when none
}

// CurrentCardStatus reads the card's state (local reads only). A stopped till
// is "paused"; otherwise registered (itself or through its main till) or not.
func CurrentCardStatus(ctx context.Context, kv Settings) CardStatus {
	get := kvGetter(ctx, kv)
	cs := CardStatus{State: StateNotRegistered}
	if st := CurrentStatus(); st.Registered || st.ViaMainTill {
		cs.State = StateRegistered
	}
	if get(KeyStopped) == "true" {
		cs.State = StatePaused
	}
	if t, err := time.Parse(time.RFC3339, get(KeyLastCheckinAt)); err == nil {
		cs.LastCheckin = t
	}
	return cs
}

// NoticeShown reports whether the current notice version was shown on this till.
func NoticeShown(ctx context.Context, kv Settings) bool {
	get := kvGetter(ctx, kv)
	return get(KeyNoticeShownAt) != "" && get(KeyNoticeVersion) == NoticeVersion
}

// MarkNoticeShown stores that the notice was shown now (idempotent for the
// current version: the first time is the one kept).
func MarkNoticeShown(ctx context.Context, kv Settings, now time.Time) error {
	if NoticeShown(ctx, kv) {
		return nil
	}
	if err := kv.Set(ctx, KeyNoticeVersion, NoticeVersion); err != nil {
		return err
	}
	if err := kv.Set(ctx, KeyNoticeShownAt, now.UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	noticePending.Store(false)
	KickAutoRegister()
	return nil
}

// Stopped reports the stop switch.
func Stopped(ctx context.Context, kv Settings) bool { return kvGetter(ctx, kv)(KeyStopped) == "true" }

// noticePending drives the status-bar prompt on a till set up before the
// notice existed (published at start by PublishNoticePending).
var noticePending atomic.Bool

// NoticePending is true while a set-up till has not yet shown the notice.
func NoticePending() bool { return noticePending.Load() }

// PublishNoticePending sets NoticePending from the settings.
func PublishNoticePending(ctx context.Context, kv Settings) {
	get := kvGetter(ctx, kv)
	noticePending.Store(get(keySetupCompleted) == "true" && !NoticeShown(ctx, kv))
}

// --- the loop ---------------------------------------------------------------

const (
	autoBackoffBase = 30 * time.Second
	autoBackoffCap  = 10 * time.Minute
	// autoPollEvery is how often a till that is not allowed yet re-reads its
	// guards (local reads, no network); a kick wakes it sooner.
	autoPollEvery = time.Minute
	// autoAttemptTimeout bounds one registration attempt.
	autoAttemptTimeout = 30 * time.Second
)

var (
	autoKick = make(chan struct{}, 1)
	// autoWait and autoJitter are the loop's clock seams; tests replace them.
	autoWait   = waitOrKick
	autoJitter = func() float64 { return (rand.Float64()*2 - 1) * 0.2 } // +-20%

	registeredHook atomic.Pointer[func()]
)

// SetRegisteredHook wires fn to run once after the loop registers the till
// (pages uses it to open the operator check-in window, ADR-0148 section 2).
func SetRegisteredHook(fn func()) {
	if fn == nil {
		registeredHook.Store(nil)
		return
	}
	registeredHook.Store(&fn)
}

// KickAutoRegister wakes the loop (setup finished, notice shown, stop
// switched off). Never blocks.
func KickAutoRegister() {
	select {
	case autoKick <- struct{}{}:
	default:
	}
}

func waitOrKick(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-autoKick:
		return true
	case <-t.C:
		return true
	}
}

// autoBackoff is the n-th (1-based) retry delay: 30 s doubling to a 10 min
// cap, with +-20% jitter, never above the cap.
func autoBackoff(n int) time.Duration {
	d := autoBackoffBase
	for i := 1; i < n && d < autoBackoffCap; i++ {
		d *= 2
	}
	if d > autoBackoffCap {
		d = autoBackoffCap
	}
	d = time.Duration(float64(d) * (1 + autoJitter()))
	if d > autoBackoffCap {
		d = autoBackoffCap
	}
	return d
}

// startAutoRegister starts the loop when the process can ever report.
func startAutoRegister(ctx context.Context, cfg *config.Config, kv Settings, wg *sync.WaitGroup) {
	if never, why := neverReports(cfg); never {
		logging.L().Debugf("enrolment: automatic registration is off for this process (%s)", why)
		return
	}
	wg.Add(1)
	go func() {
		defer logging.RecoverAndLog("enroll.autoRegisterLoop")
		defer wg.Done()
		autoRegisterLoop(ctx, cfg, kv)
	}()
}

// autoRegisterLoop registers the till in the cloud, best-effort and bound to
// ctx. It runs until the till is registered, the cloud refuses it, or ctx ends.
func autoRegisterLoop(ctx context.Context, cfg *config.Config, kv Settings) {
	log := logging.L()
	failures := 0
	lastClass := ""
	logOnce := func(class, format string, args ...any) {
		if class == lastClass {
			return
		}
		lastClass = class
		log.Infof(format, args...)
	}
	for ctx.Err() == nil {
		ok, why := AutoRegisterAllowed(ctx, cfg, kv)
		if why == ReasonRegistered {
			return
		}
		if !ok {
			if !autoWait(ctx, autoPollEvery) {
				return
			}
			continue
		}
		attemptCtx, cancel := context.WithTimeout(withAnonymousRegister(ctx), autoAttemptTimeout)
		status, err := RegisterNow(attemptCtx, cfg, kv)
		cancel()
		if status.Registered || status.ViaMainTill {
			log.Infof("enrolment: this till registered itself with the cloud")
			if fn := registeredHook.Load(); fn != nil {
				(*fn)()
			}
			return
		}
		wait := autoPollEvery
		switch {
		case errors.Is(err, ErrStoreUnnamed):
			logOnce("unnamed", "enrolment: automatic registration waits until the shop is named")
		case IsServiceRefused(err):
			log.Infof("enrolment: the cloud does not serve this shop; automatic registration stops")
			return
		case err != nil:
			if _, live := IsDeviceLive(err); live {
				logOnce("device_live", "enrolment: another till is live with this identity; automatic registration waits")
				break
			}
			failures++
			wait = autoBackoff(failures)
			logOnce(errClass(err), "enrolment: automatic registration failed (retrying in the background): %v", err)
		default:
			failures++
			wait = autoBackoff(failures)
		}
		if !autoWait(ctx, wait) {
			return
		}
	}
}

// errClass groups failures so each class is logged once, not on every retry.
func errClass(err error) string {
	var he *RegisterHTTPError
	if errors.As(err, &he) {
		return "http_" + strconv.Itoa(he.Status)
	}
	return "transport"
}

// NoticePayloadKeys is every key a register request may carry, in step with
// NoticeVersion: a golden test fails when the payload gains a key that is not
// listed here, which forces a notice text review and a NoticeVersion bump.
var NoticePayloadKeys = []string{
	"store_name", "device_id", "device_name", "version", "platform",
	"country", "region", "till_id", "approval_key", "expect_store_id",
	"terms_version", "terms_locale",
}

// ReportVersionChange compares the running software version with the one last
// compared (KeyLastReportedVersion) and, when they differ, hands (from, to) to
// record, then stores the new one. The first ever start stores the version and
// records nothing (the register call already carries it); a downgrade counts.
// While the guards say no it records nothing but still stores the running
// version, so a till reports no change from before its notice was shown or
// while the shop had sending stopped. Local only; call once at start.
func ReportVersionChange(ctx context.Context, cfg *config.Config, kv Settings, record func(ctx context.Context, from, to string)) {
	running := strings.TrimSpace(buildinfo.Version)
	stored := kvGetter(ctx, kv)(KeyLastReportedVersion)
	if stored == running {
		return
	}
	// ut-docs#6618: the running version is remembered even while a guard says
	// no (stopped, notice not shown, ...), so a change made then is never
	// reported later, stamped with a later start. A source build ("dev") is
	// not remembered: it is not a release a change event could name.
	allowed, _ := ReportingAllowed(ctx, cfg, kv)
	if allowed && stored != "" && record != nil {
		record(ctx, stored, running)
	}
	if !allowed && !IsReleaseVersion(running) {
		return
	}
	if err := kv.Set(ctx, KeyLastReportedVersion, running); err != nil {
		logging.L().Warnf("enrolment: remember the reported version: %v", err)
	}
}

type anonymousRegisterKey struct{}

// withAnonymousRegister marks a register attempt as the automatic one: no shop
// name and no till name in the request (ADR-0200, owner-approved).
func withAnonymousRegister(ctx context.Context) context.Context {
	return context.WithValue(ctx, anonymousRegisterKey{}, true)
}

func isAnonymousRegister(ctx context.Context) bool {
	v, _ := ctx.Value(anonymousRegisterKey{}).(bool)
	return v
}
