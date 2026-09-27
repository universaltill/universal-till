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
var calledFromC = []any{utRecoveryWebProcessTerminated, utRecoveryLoadFailed, utRecoveryCommitted}

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

// linuxInstallWebKitRecovery wires recovery onto w's WebKitWebView and
// returns the stop func showWindow must run on the UI thread before
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
