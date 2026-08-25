//go:build desktop && linux

package main

/*
#cgo pkg-config: gtk+-3.0
#include <gtk/gtk.h>

// ut_apply_window_flags applies the mode-derived flags to the toplevel GTK
// window webview_go already created — GTK is already linked in via
// webview_go itself (it requires gtk+-3.0 via pkg-config), so this adds no
// new runtime dependency. Safe to call before the window is realized/shown
// (GTK queues fullscreen/decoration requests either way, per its own docs
// for gtk_window_fullscreen).
static void ut_apply_window_flags(void *win, int fullscreen, int decorated, int maximize) {
	GtkWindow *w = GTK_WINDOW(win);
	gtk_window_set_decorated(w, decorated ? TRUE : FALSE);
	if (fullscreen) {
		gtk_window_fullscreen(w);
		return;
	}
	gtk_window_unfullscreen(w);
	if (maximize) {
		gtk_window_maximize(w);
	} else {
		gtk_window_unmaximize(w);
	}
}
*/
import "C"

import webview "github.com/webview/webview_go"

// init advertises the real applyWindowMode below (ADR-0064, ut-docs#1039):
// Linux is the only platform whose desktop shell genuinely applies window
// modes today, so only Linux builds claim ?control=live and run
// watchShellMode — see shellAppliesWindowMode's own doc comment
// (window_mode.go) for why a false claim would be served a kiosk mode the
// window could never leave.
func init() { shellAppliesWindowMode = true }

// applyWindowMode turns the persisted display.window_mode preference into
// real GTK calls on the shell's own toplevel window (ut-docs#611). Called
// once at launch (before w.Run()) with whatever's persisted right now, and
// again — dispatched onto the UI thread — whenever a live Settings toggle
// arrives over the shell's own cross-process control channel (ut-docs#882,
// webview_fallback.go).
func applyWindowMode(w webview.WebView, mode string) {
	flags := flagsForWindowMode(mode)
	var fullscreen, decorated, maximize C.int
	if flags.Fullscreen {
		fullscreen = 1
	}
	if flags.Decorated {
		decorated = 1
	}
	if flags.Maximize {
		maximize = 1
	}
	C.ut_apply_window_flags(w.Window(), fullscreen, decorated, maximize)
}
