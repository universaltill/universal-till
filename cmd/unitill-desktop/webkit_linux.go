//go:build desktop && linux

package main

/*
#cgo pkg-config: gtk+-3.0 webkit2gtk-4.1
#include <webkit2/webkit2.h>
#include <stdlib.h>

// ut_enable_persistent_cookies points the default WebKitWebContext's
// cookie manager at a SQLite file on disk. webkit_web_view_new() (used by
// the vendored webview_go engine,
// internal/thirdparty/webview_go/libs/webview/include/webview.h) always
// attaches to this same process-wide default context, so this must run
// before that call — there is no way to swap a WebView's context, or its
// cookie storage, once the view exists.
//
// gtk_init_check() first: every documented WebKitGTK usage initializes
// GTK before touching WebKit, and this is otherwise the first code in
// this product to reach into WebKit before GDK exists. Idempotent — the
// engine's own gtk_init_check() call right after this (webview.h) is then
// a no-op — so this costs nothing on the normal path and, on a
// genuinely display-less launch, lets this fail the same way webview.New()
// is about to fail anyway rather than constructing a context nothing will use.
static void ut_enable_persistent_cookies(const char *path) {
	if (!gtk_init_check(NULL, NULL)) {
		return;
	}
	WebKitWebContext *ctx = webkit_web_context_get_default();
	WebKitCookieManager *mgr = webkit_web_context_get_cookie_manager(ctx);
	webkit_cookie_manager_set_persistent_storage(mgr, path, WEBKIT_COOKIE_PERSISTENT_STORAGE_SQLITE);
}
*/
import "C"

import (
	"fmt"
	"os"
	"path/filepath"
	"unsafe"
)

// init overrides webview_fallback.go's no-op default with the real
// GTK/WebKit persistence setup (ut-docs#1233) — see that file's own doc
// comment on setupPersistentCookies for why only Linux needs an override:
// macOS's WKWebView (webkit_darwin.go, WKWebViewConfiguration's default
// WKWebsiteDataStore) and Windows's WebView2 both persist cookies to a
// per-app data store out of the box; webview_go's GTK/WebKit2 backend does
// not — webkit_web_view_new() attaches an in-memory-only cookie jar that
// dies with the process.
func init() { setupPersistentCookies = linuxPersistCookies }

// linuxPersistCookies points the default WebKitWebContext's cookie jar at
// a stable on-disk SQLite file so a language choice (the ut_lang cookie,
// internal/httpx.ResolveLocale) and the login session (auth.CookieName)
// survive an unitill-desktop restart or reboot — today's in-memory-only
// jar dies with the process (ut-docs#1233). Must be called before the
// first webview.New()/webkit_web_view_new(): both attach to this same
// process-wide default context, and there is no way to swap a WebView's
// context after construction — see ut_enable_persistent_cookies's own
// comment.
//
// Persists the whole jar, not just ut_lang, deliberately: this matches
// webkit_darwin.go's existing, already-shipped behavior (WKWebView's
// default data store persists every cookie, session cookie included, so
// macOS has shipped a login session that survives a restart since #609
// with no reported issue). This brings Linux to that same, already-
// accepted behavior rather than introducing a new one — the session
// itself still expires normally via auth.SessionTTL server-side; only the
// in-process cookie jar was the thing dying early.
//
// Non-fatal by design, same degrade-gracefully convention as
// fetchShellPrefs/reconcileAutostart elsewhere in this package: if the
// data directory can't be resolved or created, the caller logs and the
// shell continues with today's in-memory jar rather than failing to open.
func linuxPersistCookies() error {
	dir, err := webkitDataDir()
	if err != nil {
		return fmt.Errorf("resolve webkit data dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create webkit data dir: %w", err)
	}
	path := filepath.Join(dir, "cookies.sqlite")
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	C.ut_enable_persistent_cookies(cPath)
	return nil
}
