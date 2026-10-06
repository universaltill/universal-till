// Pure Win32 window-style arithmetic for the Windows window modes
// (ut-docs#610) — deliberately free of any OS build tag, like window_mode.go,
// so `go test ./...` exercises it on every OS. win32window_windows.go applies
// the result with user32.
package main

// Win32 window styles (WinUser.h). Spelled out here rather than taken from
// golang.org/x/sys/windows so this file builds on every OS.
const (
	wsCaption          = 0x00C00000
	wsThickFrame       = 0x00040000
	wsSysMenu          = 0x00080000
	wsMinimizeBox      = 0x00020000
	wsMaximizeBox      = 0x00010000
	wsVisible          = 0x10000000
	wsClipChildren     = 0x02000000
	wsOverlappedWindow = wsCaption | wsSysMenu | wsThickFrame | wsMinimizeBox | wsMaximizeBox

	wsExDlgModalFrame = 0x00000001
	wsExWindowEdge    = 0x00000100
	wsExClientEdge    = 0x00000200
	wsExStaticEdge    = 0x00020000
	wsExAppWindow     = 0x00040000
)

// win32FullscreenStyles strips the title bar, sizing border and edges — the
// same bits Chromium removes for its borderless fullscreen — and keeps
// everything else (visibility, clipping, the taskbar button). Sized to the
// monitor rect afterwards, such a window is what the Windows shell treats as
// fullscreen: the taskbar stays hidden while it has focus.
func win32FullscreenStyles(style, exStyle uint32) (uint32, uint32) {
	return style &^ (wsCaption | wsThickFrame),
		exStyle &^ (wsExDlgModalFrame | wsExWindowEdge | wsExClientEdge | wsExStaticEdge)
}

// win32DecoratedStyle is the normal/maximized style when there is no saved
// pre-fullscreen style to restore (the shell started already fullscreen):
// a full overlapped-window frame on top of whatever else is set.
func win32DecoratedStyle(style uint32) uint32 {
	return style | wsOverlappedWindow
}

// win32Rect mirrors the Win32 RECT layout (left, top, right, bottom).
type win32Rect struct{ Left, Top, Right, Bottom int32 }

// win32CenteredRect is where a normal window goes when there is no saved
// pre-fullscreen placement to restore (the shell launched straight into
// fullscreen): the shell's own default size (webview_fallback.go's SetSize),
// clamped to the monitor's work area and centred in it — never left the
// size of the whole monitor with a title bar bolted on.
func win32CenteredRect(work win32Rect, w, h int32) win32Rect {
	if ww := work.Right - work.Left; w > ww {
		w = ww
	}
	if wh := work.Bottom - work.Top; h > wh {
		h = wh
	}
	left := work.Left + (work.Right-work.Left-w)/2
	top := work.Top + (work.Bottom-work.Top-h)/2
	return win32Rect{Left: left, Top: top, Right: left + w, Bottom: top + h}
}

// win32FitRect keeps a restored normal rect where the operator left it when
// it fits inside work, and otherwise centres it there at its own size,
// clamped. A saved pre-fullscreen frame can be bigger than the screen: the
// shell's 1280x860 default, cascaded by Windows on a 1024x768 till, never
// fitted (ut-docs#610) — restoring it as is would hide the caption buttons
// off-screen.
func win32FitRect(r, work win32Rect) win32Rect {
	if r.Left >= work.Left && r.Top >= work.Top && r.Right <= work.Right && r.Bottom <= work.Bottom {
		return r
	}
	return win32CenteredRect(work, r.Right-r.Left, r.Bottom-r.Top)
}
