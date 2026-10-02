//go:build desktop && linux

package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"fmt"
	"net/url"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/universaltill/universal-till/internal/logging"

	webview "github.com/webview/webview_go"
)

// WebKit crash / main-frame load-failure recovery on Linux (ut-docs#2991,
// AC2). A Pi 5 shell was found sitting on WebKit's built-in "WebKit
// encountered an internal error" page after its web process died — a dead
// end on a till with no browser chrome. Now: web-process-terminated reloads
// the page the view was on, a till-origin load-failed reloads the failing
// URI (returning TRUE so WebKit never paints its own error page), and a
// commit resets the backoff. Timing/collapsing/lifetime rules live in the
// pure, tested webkit_recovery.go; this file only routes WebKit's signals
// (webkit_recovery_cgo_linux.go) into them. All GTK calls stay on the UI
// thread via w.Dispatch.

func init() { installWebKitRecovery = linuxInstallWebKitRecovery }

// calledFromC lists the //export callbacks below. Only C (the signal
// trampolines in webkit_recovery_cgo_linux.go) calls them, which the
// deadcode baseline guard (scripts/ci/guard-deadcode-baseline.sh) cannot
// see — referencing them here keeps them, and everything they call,
// visibly reachable instead of growing that baseline.
var calledFromC = []any{utRecoveryWebProcessTerminated, utRecoveryLoadFailed, utRecoveryCommitted, utNavDecidePolicy, utNavCreate}

type webkitRecovery struct {
	w       webview.WebView
	view    unsafe.Pointer // the WebKitWebView (C memory, owned by GTK)
	baseURL string
	sched   *reloadScheduler
}

// activeRecovery is the one window's recovery state the exported
// callbacks route to — one shell process, one webview. nil when none is
// installed (or it was stopped), which turns every callback into a no-op.
var activeRecovery atomic.Pointer[webkitRecovery]

// linuxInstallWebKitRecovery wires recovery — and the external-link
// routing (ut-docs#372, utNavDecidePolicy below), which shares its signal
// lookup and lifetime — onto w's WebKitWebView and returns the stop func showWindow must run on the UI thread before
// w.Destroy(): it closes the scheduler (no timer can Dispatch onto the
// freed webview afterwards) and disconnects the signals.
func linuxInstallWebKitRecovery(w webview.WebView, baseURL string) func() {
	view := recoveryConnect(w.Window())
	if view == nil {
		logging.L().Warnf("webkit recovery not installed: no WebKitWebView under the shell window (ut-docs#2991)")
		return func() {}
	}
	r := &webkitRecovery{
		w:       w,
		view:    view,
		baseURL: baseURL,
		sched: newReloadScheduler(func(d time.Duration, f func()) {
			time.AfterFunc(d, f)
		}, time.Now),
	}
	activeRecovery.Store(r)
	return func() {
		r.sched.close()
		activeRecovery.CompareAndSwap(r, nil)
		recoveryDisconnect(view)
	}
}

// scheduleReload asks the scheduler for a backed-off reload and logs one
// line for it; target is resolved on the UI thread at load time. A trigger
// that arrives while a reload is already pending (or after stop) is
// collapsed silently — one log line per attempt, so a till whose server is
// down for an hour logs about every 30s, not in a tight loop.
func (r *webkitRecovery) scheduleReload(why string, target func() string) {
	d, ok := r.sched.request(func() {
		r.w.Dispatch(func() {
			if r.sched.isClosed() {
				return
			}
			recoveryLoadURI(r.view, target())
		})
	})
	if ok {
		logging.L().Warnf("webkit recovery: %s — reloading in %s (ut-docs#2991)", why, d)
	}
}

// webProcessTerminationReason names WebKitWebProcessTerminationReason.
func webProcessTerminationReason(reason int) string {
	switch reason {
	case 0:
		return "crashed"
	case 1:
		return "exceeded memory limit"
	case 2:
		return "terminated by API"
	}
	return "unknown reason"
}

//export utRecoveryWebProcessTerminated
func utRecoveryWebProcessTerminated(reason C.int) {
	r := activeRecovery.Load()
	if r == nil {
		return
	}
	why := "web process " + webProcessTerminationReason(int(reason))
	r.scheduleReload(why, func() string {
		return recoveryTarget(r.baseURL, recoveryCurrentURI(r.view))
	})
}

//export utRecoveryLoadFailed
func utRecoveryLoadFailed(uri, domain *C.char, code C.int, message *C.char) C.int {
	r := activeRecovery.Load()
	if r == nil {
		return 0
	}
	goURI, goDomain := C.GoString(uri), C.GoString(domain)
	if !shouldRecoverLoadFailure(r.baseURL, goURI, goDomain, int(code)) {
		return 0
	}
	// Path only in the log: a query string could carry a token (review of
	// ut-docs#2991, nit 6).
	logged := goURI
	if u, err := url.Parse(goURI); err == nil {
		logged = u.Path
	}
	why := fmt.Sprintf("load of %s failed: %s %d %s", logged, goDomain, int(code), C.GoString(message))
	r.scheduleReload(why, func() string { return goURI })
	return 1
}

//export utRecoveryCommitted
func utRecoveryCommitted() {
	if r := activeRecovery.Load(); r != nil {
		r.sched.committed()
	}
}

// navRoute performs the shared action for navOpenExternal/navLoadInView —
// launch the system browser, or load till-origin same-origin in the
// existing view on a later main-loop turn (never from inside WebKit's own
// signal emission; skipped if showWindow's stop ran first and the view may
// already be gone — same re-check as scheduleReload). Shared by
// utNavDecidePolicy (link clicks) and utNavCreate (window.open — ut-docs#372
// review found window.open never reaches decide-policy at all). Reports
// whether disp was one of those two (the caller still needs to know whether
// it "handled" the request).
func navRoute(r *webkitRecovery, goURI string, disp navDisposition) bool {
	switch disp {
	case navOpenExternal:
		// Scheme+host only in the log: a path or query could carry a
		// token (same rule as utRecoveryLoadFailed).
		logged := goURI
		if u, err := url.Parse(goURI); err == nil {
			logged = u.Scheme + "://" + u.Host
		}
		if err := navLaunchDefault(goURI); err != nil {
			logging.L().Warnf("external link %s not opened in the default browser: %v (ut-docs#372)", logged, err)
		} else {
			logging.L().Infof("external link %s opened in the default browser (ut-docs#372)", logged)
		}
		return true
	case navLoadInView:
		r.w.Dispatch(func() {
			if activeRecovery.Load() != r {
				return
			}
			recoveryLoadURI(r.view, goURI)
		})
		return true
	}
	return false
}

// utNavDecidePolicy is WebKit's decide-policy for a navigation (newWindow
// == 0) or new-window request to uri, routed through the pure
// navigationDisposition (webkit_navigation.go, ut-docs#372). Returns 1
// when it decided (the decision is ignored and the request rerouted), 0 to
// leave it to WebKit's default handler. Runs on the GTK main thread.
//
//export utNavDecidePolicy
func utNavDecidePolicy(decision unsafe.Pointer, uri *C.char, newWindow C.int) C.int {
	r := activeRecovery.Load()
	if r == nil {
		return 0
	}
	goURI := C.GoString(uri)
	disp := navigationDisposition(r.baseURL, goURI, newWindow != 0)
	if !navRoute(r, goURI, disp) {
		return 0
	}
	navPolicyIgnore(decision)
	return 1
}

// utNavCreate is WebKitGTK's "create" signal for window.open() — the one
// new-window path decide-policy never sees (ut-docs#372 review: confirmed
// live that window.open() bypasses decide-policy entirely). The C side
// (ut_on_create) always returns NULL — this shell never creates a second
// native window — so this only needs to route the target, same policy as
// a target="_blank" link click. Runs on the GTK main thread.
//
//export utNavCreate
func utNavCreate(uri *C.char) {
	r := activeRecovery.Load()
	if r == nil {
		return
	}
	goURI := C.GoString(uri)
	navRoute(r, goURI, navigationDisposition(r.baseURL, goURI, true))
}
