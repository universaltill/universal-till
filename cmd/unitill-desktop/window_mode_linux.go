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

// applyWindowMode turns the persisted display.window_mode preference into
// real GTK calls on the shell's own toplevel window (ut-docs#611). Applied
// once at launch (before w.Run()) — a live, no-relaunch toggle needs a
// cross-process control channel between this shell and the unitill-pos
// server it spawns, split off as ut-docs#882.
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
