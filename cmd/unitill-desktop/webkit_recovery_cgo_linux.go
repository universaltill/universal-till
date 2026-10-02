//go:build desktop && linux

package main

/*
#cgo pkg-config: gtk+-3.0 webkit2gtk-4.1
#include <webkit2/webkit2.h>
#include <stdlib.h>

// Implemented in Go (webkit_recovery_linux.go, //export) — declarations
// only here; that file cannot hold C definitions because it exports.
extern void utRecoveryWebProcessTerminated(int reason);
extern int utRecoveryLoadFailed(char *uri, char *domain, int code, char *message);
extern void utRecoveryCommitted(void);
extern int utNavDecidePolicy(void *decision, char *uri, int newWindow);
extern void utNavCreate(char *uri);

// Signal trampolines for ut-docs#2991 (AC2). All run on the GTK main
// thread, where webview_go's loop dispatches every WebKit signal.
static void ut_on_web_process_terminated(WebKitWebView *view, WebKitWebProcessTerminationReason reason, gpointer data) {
	utRecoveryWebProcessTerminated((int)reason);
}

// Returning TRUE stops the signal's default handler, which is what paints
// WebKit's built-in error page ("WebKit encountered an internal error").
static gboolean ut_on_load_failed(WebKitWebView *view, WebKitLoadEvent event, gchar *uri, GError *error, gpointer data) {
	const char *domain = error ? g_quark_to_string(error->domain) : "";
	int code = error ? error->code : 0;
	const char *message = (error && error->message) ? error->message : "";
	return utRecoveryLoadFailed((char *)(uri ? uri : ""), (char *)(domain ? domain : ""), code, (char *)message) ? TRUE : FALSE;
}

static void ut_on_load_changed(WebKitWebView *view, WebKitLoadEvent event, gpointer data) {
	if (event == WEBKIT_LOAD_COMMITTED) {
		utRecoveryCommitted();
	}
}

// External-link routing (ut-docs#372; policy in webkit_navigation.go).
// Signature per webkit2gtk-4.1's WebKitWebViewClass.decide_policy:
// (WebKitWebView *, WebKitPolicyDecision *, WebKitPolicyDecisionType).
// Both NAVIGATION_ACTION and NEW_WINDOW_ACTION decisions are always a
// WebKitNavigationPolicyDecision. This covers a plain link and a
// target="_blank" link click — but NOT window.open() (DOMWindow::open
// never reaches decide-policy at all; WebKitGTK calls the "create" signal
// instead, handled separately by ut_on_create below — review of ut-docs#372
// found this gap live: window.open() silently did nothing without it).
// WebKitGTK's UI-process API exposes no main-frame flag on a navigation
// action, so NAVIGATION_ACTION covers subframes too — fine for the till
// today: its only iframe (plugin pages) is a sandboxed srcdoc, never an
// http(s) navigation. Returning FALSE lets WebKit's default handler decide
// (use), exactly as before this handler.
static gboolean ut_on_decide_policy(WebKitWebView *view, WebKitPolicyDecision *decision, WebKitPolicyDecisionType type, gpointer data) {
	if (type != WEBKIT_POLICY_DECISION_TYPE_NAVIGATION_ACTION && type != WEBKIT_POLICY_DECISION_TYPE_NEW_WINDOW_ACTION) {
		return FALSE;
	}
	if (!WEBKIT_IS_NAVIGATION_POLICY_DECISION(decision)) {
		return FALSE;
	}
	WebKitNavigationAction *action = webkit_navigation_policy_decision_get_navigation_action(WEBKIT_NAVIGATION_POLICY_DECISION(decision));
	if (action == NULL) {
		return FALSE;
	}
	WebKitURIRequest *request = webkit_navigation_action_get_request(action);
	const gchar *uri = request ? webkit_uri_request_get_uri(request) : NULL;
	if (uri == NULL) {
		return FALSE;
	}
	int newWindow = type == WEBKIT_POLICY_DECISION_TYPE_NEW_WINDOW_ACTION;
	return utNavDecidePolicy(decision, (char *)uri, newWindow) ? TRUE : FALSE;
}

static void ut_policy_ignore(void *decision) {
	webkit_policy_decision_ignore(WEBKIT_POLICY_DECISION(decision));
}

// ut_on_create handles window.open() (ut-docs#372 review finding): WebKit
// calls this to obtain a new WebKitWebView to open into, never decide-policy.
// This shell never creates a second native window (webview_go/this shell has
// nowhere to put one — same "no second window" rule as the NEW_WINDOW_ACTION
// case in ut_on_decide_policy), so this always returns NULL; it only exists
// to extract the target URI and route it (external → system browser,
// till-origin → load in the existing view) before refusing the popup.
static GtkWidget *ut_on_create(WebKitWebView *view, WebKitNavigationAction *action, gpointer data) {
	if (action == NULL) {
		return NULL;
	}
	WebKitURIRequest *request = webkit_navigation_action_get_request(action);
	const gchar *uri = request ? webkit_uri_request_get_uri(request) : NULL;
	if (uri != NULL) {
		utNavCreate((char *)uri);
	}
	return NULL;
}

// Opens uri with the desktop's default handler (the system browser for
// http/https). On failure returns a newly allocated message the caller
// frees; NULL on success.
static char *ut_launch_default_for_uri(const char *uri) {
	GError *err = NULL;
	if (g_app_info_launch_default_for_uri(uri, NULL, &err)) {
		return NULL;
	}
	char *msg = g_strdup(err && err->message ? err->message : "unknown error");
	if (err) {
		g_error_free(err);
	}
	return msg;
}

// ut_recovery_connect finds the WebKitWebView webview_go put in its
// toplevel GtkWindow (webview.h: gtk_container_add(m_window, m_webview) —
// the window's sole child) and connects the recovery signals. Returns the
// view, or NULL if the widget tree is not what we expect (then recovery is
// simply not installed). It also connects decide-policy for the external-
// link routing (ut-docs#372) — one widget lookup, one connect/disconnect
// lifetime for every handler the shell puts on the view.
static void *ut_recovery_connect(void *win) {
	if (win == NULL || !GTK_IS_BIN(win)) {
		return NULL;
	}
	GtkWidget *child = gtk_bin_get_child(GTK_BIN(win));
	if (child == NULL || !WEBKIT_IS_WEB_VIEW(child)) {
		return NULL;
	}
	g_signal_connect(child, "web-process-terminated", G_CALLBACK(ut_on_web_process_terminated), NULL);
	g_signal_connect(child, "load-failed", G_CALLBACK(ut_on_load_failed), NULL);
	g_signal_connect(child, "load-changed", G_CALLBACK(ut_on_load_changed), NULL);
	g_signal_connect(child, "decide-policy", G_CALLBACK(ut_on_decide_policy), NULL);
	g_signal_connect(child, "create", G_CALLBACK(ut_on_create), NULL);
	return child;
}

// ut_recovery_disconnect runs before webview_go destroys the view, so no
// trampoline fires during (or after) its teardown.
static void ut_recovery_disconnect(void *view) {
	if (view == NULL || !WEBKIT_IS_WEB_VIEW(view)) {
		return;
	}
	g_signal_handlers_disconnect_by_func(view, G_CALLBACK(ut_on_web_process_terminated), NULL);
	g_signal_handlers_disconnect_by_func(view, G_CALLBACK(ut_on_load_failed), NULL);
	g_signal_handlers_disconnect_by_func(view, G_CALLBACK(ut_on_load_changed), NULL);
	g_signal_handlers_disconnect_by_func(view, G_CALLBACK(ut_on_decide_policy), NULL);
	g_signal_handlers_disconnect_by_func(view, G_CALLBACK(ut_on_create), NULL);
}

static void ut_recovery_load_uri(void *view, const char *uri) {
	webkit_web_view_load_uri(WEBKIT_WEB_VIEW(view), uri);
}

// Borrowed string owned by the view; copy before the next main-loop turn.
static const char *ut_recovery_current_uri(void *view) {
	return webkit_web_view_get_uri(WEBKIT_WEB_VIEW(view));
}
*/
import "C"

import (
	"errors"
	"unsafe"
)

// cgo side of webkit_recovery_linux.go: the calls into WebKitGTK. Kept in
// its own file because the //export-ing file may only declare C, not
// define it (cgo rule).

func recoveryConnect(win unsafe.Pointer) unsafe.Pointer {
	return C.ut_recovery_connect(win)
}

func recoveryDisconnect(view unsafe.Pointer) {
	C.ut_recovery_disconnect(view)
}

func recoveryLoadURI(view unsafe.Pointer, uri string) {
	cURI := C.CString(uri)
	defer C.free(unsafe.Pointer(cURI))
	C.ut_recovery_load_uri(view, cURI)
}

func navPolicyIgnore(decision unsafe.Pointer) {
	C.ut_policy_ignore(decision)
}

// navLaunchDefault opens uri in the desktop's default browser.
func navLaunchDefault(uri string) error {
	cURI := C.CString(uri)
	defer C.free(unsafe.Pointer(cURI))
	if msg := C.ut_launch_default_for_uri(cURI); msg != nil {
		defer C.g_free(C.gpointer(msg))
		return errors.New(C.GoString(msg))
	}
	return nil
}

func recoveryCurrentURI(view unsafe.Pointer) string {
	p := C.ut_recovery_current_uri(view)
	if p == nil {
		return ""
	}
	return C.GoString(p)
}
