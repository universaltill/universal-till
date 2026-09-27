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

// ut_recovery_connect finds the WebKitWebView webview_go put in its
// toplevel GtkWindow (webview.h: gtk_container_add(m_window, m_webview) —
// the window's sole child) and connects the recovery signals. Returns the
// view, or NULL if the widget tree is not what we expect (then recovery is
// simply not installed).
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

func recoveryCurrentURI(view unsafe.Pointer) string {
	p := C.ut_recovery_current_uri(view)
	if p == nil {
		return ""
	}
	return C.GoString(p)
}
