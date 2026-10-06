package main

import "testing"

// The style arithmetic behind the Windows window modes (ut-docs#610) is
// untagged so plain `go test ./...` covers it on every OS; the user32 calls
// that apply it live in win32window_windows.go.
func TestWin32FullscreenStylesStripChromeOnly(t *testing.T) {
	style := uint32(wsOverlappedWindow | wsVisible | wsClipChildren)
	ex := uint32(wsExWindowEdge | wsExClientEdge | wsExStaticEdge | wsExDlgModalFrame | wsExAppWindow)

	gotStyle, gotEx := win32FullscreenStyles(style, ex)

	if gotStyle&(wsCaption|wsThickFrame) != 0 {
		t.Errorf("fullscreen style %#x still has a caption or sizing border", gotStyle)
	}
	if gotStyle&(wsVisible|wsClipChildren) != wsVisible|wsClipChildren {
		t.Errorf("fullscreen style %#x dropped unrelated bits", gotStyle)
	}
	if gotEx&(wsExWindowEdge|wsExClientEdge|wsExStaticEdge|wsExDlgModalFrame) != 0 {
		t.Errorf("fullscreen ex-style %#x still has an edge", gotEx)
	}
	if gotEx&wsExAppWindow == 0 {
		t.Errorf("fullscreen ex-style %#x dropped WS_EX_APPWINDOW (the taskbar button)", gotEx)
	}
}

func TestWin32DecoratedStyleRestoresAFullFrame(t *testing.T) {
	// A window that never went fullscreen in this process (no saved style)
	// but is asked for normal/maximized must still end up with a title bar,
	// sizing border and the min/max/close buttons.
	stripped, _ := win32FullscreenStyles(wsOverlappedWindow|wsVisible, 0)
	got := win32DecoratedStyle(stripped)
	if got&wsOverlappedWindow != wsOverlappedWindow {
		t.Errorf("decorated style %#x lacks WS_OVERLAPPEDWINDOW", got)
	}
	if got&wsVisible == 0 {
		t.Errorf("decorated style %#x dropped WS_VISIBLE", got)
	}
}

func TestWin32CenteredRect(t *testing.T) {
	cases := []struct {
		name string
		work win32Rect
		w, h int32
		want win32Rect
	}{
		{"fits", win32Rect{0, 0, 1920, 1040}, 1280, 860, win32Rect{320, 90, 1600, 950}},
		{"clamped to a small work area", win32Rect{0, 0, 1024, 560}, 1280, 860, win32Rect{0, 0, 1024, 560}},
		{"second monitor offset", win32Rect{1920, 0, 3840, 1040}, 1280, 860, win32Rect{2240, 90, 3520, 950}},
	}
	for _, c := range cases {
		if got := win32CenteredRect(c.work, c.w, c.h); got != c.want {
			t.Errorf("%s: win32CenteredRect = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestWin32FitRect(t *testing.T) {
	work := win32Rect{0, 0, 1024, 720}
	cases := []struct {
		name string
		r    win32Rect
		want win32Rect
	}{
		{"fits: kept as the operator left it", win32Rect{100, 50, 900, 650}, win32Rect{100, 50, 900, 650}},
		// Seen on a 1024x768 Windows VM (ut-docs#610): the shell's 1280x860
		// default, cascaded to 156,156 — bigger than the screen.
		{"too big: centred and clamped", win32Rect{156, 156, 1200, 944}, win32Rect{0, 0, 1024, 720}},
		{"fits in size but hangs off the edge: pulled back in", win32Rect{600, 400, 1400, 1000}, win32Rect{112, 60, 912, 660}},
	}
	for _, c := range cases {
		if got := win32FitRect(c.r, work); got != c.want {
			t.Errorf("%s: win32FitRect = %+v, want %+v", c.name, got, c.want)
		}
	}
}
