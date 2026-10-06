//go:build windows

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// These run on a real Windows runner (ci.yml's windows-shell job,
// ut-docs#610) against a plain CreateWindowEx window — the same user32
// calls the WebView2 shell's window gets, without the mingw build.

var (
	procCreateWindowExW = user32.NewProc("CreateWindowExW")
	procDestroyWindow   = user32.NewProc("DestroyWindow")
)

func newTestWindow(t *testing.T, style uint32, x, y, w, h int32) uintptr {
	t.Helper()
	// Window messages and SetWindowPos belong to the creating thread.
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	class, _ := windows.UTF16PtrFromString("STATIC") // predefined class, nothing to register
	title, _ := windows.UTF16PtrFromString("unitill #610 test")
	hwnd, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)),
		uintptr(style), uintptr(x), uintptr(y), uintptr(w), uintptr(h), 0, 0, 0, 0)
	if hwnd == 0 {
		t.Fatalf("CreateWindowEx: %v", err)
	}
	t.Cleanup(func() { procDestroyWindow.Call(hwnd) })
	return hwnd
}

func windowRect(t *testing.T, hwnd uintptr) win32Rect {
	t.Helper()
	var r win32Rect
	if ok, _, err := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
		t.Fatalf("GetWindowRect: %v", err)
	}
	return r
}

func windowStyle(t *testing.T, hwnd uintptr) uint32 {
	t.Helper()
	return getWindowLong(hwnd, gwlStyle)
}

func mustApply(t *testing.T, hwnd uintptr, mode string) {
	t.Helper()
	if err := applyWin32WindowMode(hwnd, flagsForWindowMode(mode)); err != nil {
		t.Fatalf("apply %s: %v", mode, err)
	}
}

func TestWin32FullscreenCoversMonitorAndNormalRestores(t *testing.T) {
	hwnd := newTestWindow(t, wsOverlappedWindow|wsVisible, 100, 100, 800, 600)
	before := windowRect(t, hwnd)

	mustApply(t, hwnd, "fullscreen")
	if s := windowStyle(t, hwnd); s&(wsCaption|wsThickFrame) != 0 {
		t.Errorf("fullscreen style %#x still has a frame", s)
	}
	mi, err := monitorInfo(hwnd)
	if err != nil {
		t.Fatal(err)
	}
	if got := windowRect(t, hwnd); got != mi.Monitor {
		t.Errorf("fullscreen rect = %+v, want the whole monitor %+v (taskbar covered)", got, mi.Monitor)
	}

	// Re-applying fullscreen (a second live toggle) must not overwrite the
	// saved frame with the fullscreen one.
	mustApply(t, hwnd, "kiosk")

	mustApply(t, hwnd, "normal")
	if s := windowStyle(t, hwnd); s&wsOverlappedWindow != wsOverlappedWindow {
		t.Errorf("normal style %#x lacks the frame", s)
	}
	if got := windowRect(t, hwnd); got != before {
		t.Errorf("normal rect = %+v, want the pre-fullscreen %+v", got, before)
	}
}

func TestWin32FullscreenThenMaximized(t *testing.T) {
	hwnd := newTestWindow(t, wsOverlappedWindow|wsVisible, 50, 50, 640, 480)
	mustApply(t, hwnd, "fullscreen")
	mustApply(t, hwnd, "maximized")
	if s := windowStyle(t, hwnd); s&wsOverlappedWindow != wsOverlappedWindow {
		t.Errorf("maximized style %#x lacks the frame", s)
	}
	if z, _, _ := procIsZoomed.Call(hwnd); z == 0 {
		t.Error("maximized window is not zoomed")
	}
}

func TestWin32NormalWithoutSavedFrameIsCentredDefaultSize(t *testing.T) {
	// The shell launched straight into fullscreen: no saved frame. Normal
	// must give a framed window inside the work area, not a monitor-sized one.
	hwnd := newTestWindow(t, wsVisible, 0, 0, 400, 300)
	mi, err := monitorInfo(hwnd)
	if err != nil {
		t.Fatal(err)
	}
	mustApply(t, hwnd, "normal")
	if s := windowStyle(t, hwnd); s&wsOverlappedWindow != wsOverlappedWindow {
		t.Errorf("normal style %#x lacks the frame", s)
	}
	want := win32CenteredRect(mi.Work, defaultShellWidth, defaultShellHeight)
	if got := windowRect(t, hwnd); got != want {
		t.Errorf("normal rect = %+v, want centred default %+v", got, want)
	}
}

func TestReconcileStartupShortcutRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Startup")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	lnk := filepath.Join(dir, autostartShortcutName)

	if err := reconcileStartupShortcut(dir, true, exe); err != nil {
		t.Fatalf("enable: %v", err)
	}
	target, err := readShellLinkTarget(lnk)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	// os.Executable may answer with an 8.3 short path (RUNNER~1 on GitHub's
	// runners); the shortcut stores the long one. Compare the long forms.
	if !strings.EqualFold(longPath(t, target), longPath(t, exe)) {
		t.Errorf("shortcut target = %q, want %q", target, exe)
	}
	// Enabled again (every launch): rewritten, still valid.
	if err := reconcileStartupShortcut(dir, true, exe); err != nil {
		t.Fatalf("re-enable: %v", err)
	}

	if err := reconcileStartupShortcut(dir, false, exe); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := os.Stat(lnk); !os.IsNotExist(err) {
		t.Errorf("shortcut still present after disable: %v", err)
	}
	if err := reconcileStartupShortcut(dir, false, exe); err != nil {
		t.Errorf("disable with nothing to remove: %v", err)
	}
}

func TestWin32NormalRestoreFitsAnOversizedSavedFrame(t *testing.T) {
	// The shell's default window can be bigger than a small till screen
	// before it ever goes fullscreen; leaving fullscreen must not bring that
	// back with the caption buttons off-screen (seen on a 1024x768 VM).
	hwnd := newTestWindow(t, wsOverlappedWindow|wsVisible, 0, 0, 100, 100)
	mi, err := monitorInfo(hwnd)
	if err != nil {
		t.Fatal(err)
	}
	m := mi.Monitor
	if err := setWindowPos(hwnd, m.Left+156, m.Top+156, m.Right-m.Left+20, m.Bottom-m.Top+20, swpNoZOrder|swpNoActivate); err != nil {
		t.Fatal(err)
	}
	mustApply(t, hwnd, "fullscreen")
	mustApply(t, hwnd, "normal")
	got, w := windowRect(t, hwnd), mi.Work
	if got.Left < w.Left || got.Top < w.Top || got.Right > w.Right || got.Bottom > w.Bottom {
		t.Errorf("normal rect = %+v, not inside the work area %+v", got, w)
	}
}

func TestWin32MaximizedFromFullscreenRestoresToAFittedRect(t *testing.T) {
	// Leaving fullscreen for maximized must leave a sane restore rect behind:
	// the caption's Restore button must not produce a monitor-sized window.
	hwnd := newTestWindow(t, wsOverlappedWindow|wsVisible, 80, 60, 640, 480)
	before := windowRect(t, hwnd)
	mustApply(t, hwnd, "fullscreen")
	mustApply(t, hwnd, "maximized")
	procShowWindow.Call(hwnd, swRestore)
	if got := windowRect(t, hwnd); got != before {
		t.Errorf("restored rect after maximized = %+v, want the pre-fullscreen %+v", got, before)
	}
}

func longPath(t *testing.T, p string) string {
	t.Helper()
	in, err := windows.UTF16PtrFromString(p)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 32767)
	n, err := windows.GetLongPathName(in, &buf[0], uint32(len(buf)))
	if n == 0 {
		t.Fatalf("GetLongPathName(%q): %v", p, err)
	}
	return filepath.Clean(windows.UTF16ToString(buf[:n]))
}
