//go:build windows

// Native Win32 window modes for the Windows desktop shell (ut-docs#610).
// Tagged `windows` only — not `desktop` — so it is pure Go with no cgo: CI
// vets it with GOOS=windows and tests it on a real Windows runner against a
// plain CreateWindowEx window, without the mingw WebView2 build.
// window_mode_windows.go hands it the WebView2 shell's own HWND.
package main

import (
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                 = windows.NewLazySystemDLL("user32.dll")
	procGetWindowLongPtrW  = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtrW  = user32.NewProc("SetWindowLongPtrW")
	procGetWindowPlacement = user32.NewProc("GetWindowPlacement")
	procSetWindowPlacement = user32.NewProc("SetWindowPlacement")
	procMonitorFromWindow  = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW    = user32.NewProc("GetMonitorInfoW")
	procSetWindowPos       = user32.NewProc("SetWindowPos")
	procShowWindow         = user32.NewProc("ShowWindow")
	procIsZoomed           = user32.NewProc("IsZoomed")
	procIsWindow           = user32.NewProc("IsWindow")
	procGetWindowRect      = user32.NewProc("GetWindowRect")
)

const (
	gwlStyle   int32 = -16
	gwlExStyle int32 = -20

	swShowNormal = 1
	swMaximize   = 3
	swRestore    = 9

	swpNoSize               = 0x0001
	swpNoMove               = 0x0002
	swpNoZOrder             = 0x0004
	swpNoActivate           = 0x0010
	swpFrameChanged         = 0x0020
	swpNoOwnerZOrder        = 0x0200
	monitorDefaultToNearest = 2

	// The shell's own default window size (webview_fallback.go's SetSize).
	defaultShellWidth  = 1280
	defaultShellHeight = 860
)

type win32Point struct{ X, Y int32 }

type win32WindowPlacement struct {
	Length         uint32
	Flags          uint32
	ShowCmd        uint32
	MinPosition    win32Point
	MaxPosition    win32Point
	NormalPosition win32Rect
}

type win32MonitorInfo struct {
	Size    uint32
	Monitor win32Rect
	Work    win32Rect
	Flags   uint32
}

// win32SavedFrame is the window as it was before it went fullscreen, so
// leaving fullscreen puts back exactly the frame, size and position the
// operator had.
type win32SavedFrame struct {
	style, exStyle uint32
	placement      win32WindowPlacement
}

// win32Saved is keyed by HWND. Every caller runs on the shell's UI thread
// (the initial apply before Run, Dispatch for live changes); the mutex only
// keeps the map honest if that ever changes.
var (
	win32SavedMu sync.Mutex
	win32Saved   = map[uintptr]*win32SavedFrame{}
)

// applyWin32WindowMode turns windowModeFlags into user32 calls on hwnd.
// Fullscreen is Chromium's borderless fullscreen: strip the frame and size
// the window to its monitor's full rect, which the Windows shell treats as
// a fullscreen app (the taskbar stays hidden while it has focus). Leaving it
// restores the saved frame, then maximizes or restores as asked.
func applyWin32WindowMode(hwnd uintptr, f windowModeFlags) error {
	if hwnd == 0 {
		return fmt.Errorf("no native window")
	}
	win32SavedMu.Lock()
	defer win32SavedMu.Unlock()

	if r, _, _ := procIsWindow.Call(hwnd); r == 0 {
		return fmt.Errorf("window %#x no longer exists", hwnd)
	}
	style := getWindowLong(hwnd, gwlStyle)
	exStyle := getWindowLong(hwnd, gwlExStyle)

	if f.Fullscreen {
		if win32Saved[hwnd] == nil {
			saved := &win32SavedFrame{style: style, exStyle: exStyle}
			saved.placement.Length = uint32(unsafe.Sizeof(saved.placement))
			if r, _, e := procGetWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&saved.placement))); r == 0 {
				return fmt.Errorf("GetWindowPlacement: %w", e)
			}
			win32Saved[hwnd] = saved
		}
		// A maximized window keeps its maximized state under the new style
		// and Windows re-applies the work-area size on the next activation,
		// so restore it first, as Chromium does.
		if r, _, _ := procIsZoomed.Call(hwnd); r != 0 {
			procShowWindow.Call(hwnd, swRestore)
		}
		fs, fex := win32FullscreenStyles(style, exStyle)
		setWindowLong(hwnd, gwlStyle, fs)
		setWindowLong(hwnd, gwlExStyle, fex)
		mi, err := monitorInfo(hwnd)
		if err != nil {
			return err
		}
		m := mi.Monitor
		return setWindowPos(hwnd, m.Left, m.Top, m.Right-m.Left, m.Bottom-m.Top,
			swpNoZOrder|swpNoOwnerZOrder|swpNoActivate|swpFrameChanged)
	}

	saved := win32Saved[hwnd]
	delete(win32Saved, hwnd)
	newStyle, newEx := win32DecoratedStyle(style), exStyle
	if saved != nil {
		newStyle, newEx = win32DecoratedStyle(saved.style), saved.exStyle
	}
	setWindowLong(hwnd, gwlStyle, newStyle)
	setWindowLong(hwnd, gwlExStyle, newEx)
	if err := setWindowPos(hwnd, 0, 0, 0, 0,
		swpNoMove|swpNoSize|swpNoZOrder|swpNoOwnerZOrder|swpNoActivate|swpFrameChanged); err != nil {
		return err
	}
	if saved != nil {
		p := saved.placement
		p.ShowCmd = swShowNormal
		if r, _, e := procSetWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&p))); r == 0 {
			return fmt.Errorf("SetWindowPlacement: %w", e)
		}
	} else {
		procShowWindow.Call(hwnd, swRestore)
	}

	// Now a restored window: make sure its normal rect sits inside its own
	// monitor's work area, in screen coordinates (WINDOWPLACEMENT's are
	// workspace-relative to the primary monitor, so they are not compared).
	// Launched straight into fullscreen there is no frame to go back to: the
	// default size, centred — never a monitor-sized window with a title bar.
	// A saved frame can be bigger than the screen (the 1280x860 default,
	// cascaded on a 1024x768 till), so it is fitted too. Done before a
	// maximize, so the caption's Restore button lands on this rect as well.
	mi, err := monitorInfo(hwnd)
	if err != nil {
		return err
	}
	var cur win32Rect
	if r, _, e := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&cur))); r == 0 {
		return fmt.Errorf("GetWindowRect: %w", e)
	}
	target := win32FitRect(cur, mi.Work)
	if saved == nil {
		target = win32CenteredRect(mi.Work, defaultShellWidth, defaultShellHeight)
	}
	if target != cur {
		if err := setWindowPos(hwnd, target.Left, target.Top, target.Right-target.Left, target.Bottom-target.Top,
			swpNoZOrder|swpNoOwnerZOrder|swpNoActivate); err != nil {
			return err
		}
	}
	if f.Maximize {
		procShowWindow.Call(hwnd, swMaximize)
	}
	return nil
}

// getWindowLong and setWindowLong are only called on a handle IsWindow
// just accepted, where they cannot fail; 0 is a legitimate style (the
// ex-style is 0 for the whole fullscreen session), so no last-error
// bookkeeping that a stray Win32 call in between could falsify.
func getWindowLong(hwnd uintptr, index int32) uint32 {
	r, _, _ := procGetWindowLongPtrW.Call(hwnd, uintptr(index))
	return uint32(r)
}

func setWindowLong(hwnd uintptr, index int32, v uint32) {
	procSetWindowLongPtrW.Call(hwnd, uintptr(index), uintptr(v))
}

func setWindowPos(hwnd uintptr, x, y, w, h int32, flags uintptr) error {
	if r, _, e := procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(h), flags); r == 0 {
		return fmt.Errorf("SetWindowPos: %w", e)
	}
	return nil
}

func monitorInfo(hwnd uintptr) (win32MonitorInfo, error) {
	var mi win32MonitorInfo
	mon, _, e := procMonitorFromWindow.Call(hwnd, monitorDefaultToNearest)
	if mon == 0 {
		return mi, fmt.Errorf("MonitorFromWindow: %w", e)
	}
	mi.Size = uint32(unsafe.Sizeof(mi))
	if r, _, e := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); r == 0 {
		return mi, fmt.Errorf("GetMonitorInfo: %w", e)
	}
	return mi, nil
}
