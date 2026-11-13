// Package androidupdate reports an Android in-app update refusal to the
// fleet roster (ut-docs#5446, ADR-0179).
//
// MainActivity checks a downloaded update APK (package, version, signer pin)
// before it reaches the system installer. A refusal used to be visible only
// on the tablet (logcat and the page's retry text), so a fleet pointed at a
// wrong or foreign release asset looked healthy from the cloud. Native code
// now pushes the refusal's CLASS through mobile.ReportAndroidUpdateRefused;
// it becomes one keyed Problem (ProblemKeyRefused), which rides the heartbeat
// `problems` payload to the admin store detail like any other
// (selfupdate.update_failed is the desktop precedent).
//
// Only the class crosses: never a digest, a foreign package name, a version
// number or exception text, because those are attacker-influenced strings
// that must not reach the fleet view. The class is validated against a
// closed set; anything else is reported as check_error and never echoed.
//
// Only the Android shell calls this (via the mobile package); desktop, Pi and
// service builds never do.
package androidupdate

import (
	"sync"

	"github.com/universaltill/universal-till/internal/logging"
)

// ProblemKeyRefused is the keyed Problem a refusal raises.
const ProblemKeyRefused = "androidupdate.refused"

// classes maps the wire class to the English operator-facing text. The keys
// are the closed set mirrored by RefuseClass in
// android/app/src/main/java/com/universaltill/pos/UpdateLogic.kt;
// TestClassesAreTheKotlinSet pins them.
var classes = map[string]string{
	"unreadable_apk":    "downloaded file is not a readable APK",
	"wrong_package":     "APK is a different app",
	"not_newer":         "APK is not newer than the installed version",
	"signer_unreadable": "APK signer could not be read",
	"multiple_signers":  "APK has more than one signer",
	"signer_not_pinned": "APK signer is not a pinned Universal Till certificate",
	"check_error":       "APK check failed",
}

var (
	mu        sync.Mutex
	lastClass string
)

// ReportRefused records that the in-app update APK was refused for class. An
// unknown class is reported as check_error. A repeat of the open class logs
// at INFO so retries do not flood the Problems ring; a different class
// replaces the open one so the fleet line never shows a stale class.
func ReportRefused(class string) {
	if _, ok := classes[class]; !ok {
		class = "check_error"
	}
	text := classes[class]
	mu.Lock()
	defer mu.Unlock()
	if logging.HasOpenProblem(ProblemKeyRefused) && class == lastClass {
		logging.L().Infof("Android update refused again: %s (%s)", text, class)
		return
	}
	logging.ResolveProblems(ProblemKeyRefused)
	logging.L().WarnProblemf(ProblemKeyRefused, "Android update refused: %s (%s)", text, class)
	lastClass = class
}

// ReportAccepted resolves an earlier refusal: a later update APK passed the
// check (pinned or dormant).
func ReportAccepted() {
	mu.Lock()
	defer mu.Unlock()
	lastClass = ""
	if logging.ResolveProblems(ProblemKeyRefused) > 0 {
		logging.L().Infof("Android update APK passed the signer check; earlier refusal resolved")
	}
}
