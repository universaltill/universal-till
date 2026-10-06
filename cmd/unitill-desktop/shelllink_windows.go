//go:build windows

// Startup-folder shortcut for Windows autostart (ut-docs#610,
// reference/desktop-kiosk-overlay-macos-windows.md "Windows: a
// Startup-folder shortcut"). Tagged `windows` only, like
// win32window_windows.go, so CI can vet it and round-trip a real .lnk on a
// Windows runner without the mingw WebView2 build. The .lnk is written by
// the Windows shell's own IShellLink, the same COM object NSIS's
// CreateShortcut uses for the installer's shortcuts.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/universaltill/universal-till/internal/logging"
)

// autostartShortcutName matches installer.nsi's "$SMSTARTUP\${APPNAME}.lnk",
// so the shell and the installer own the same file.
const autostartShortcutName = "Universal Till.lnk"

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")

	clsidShellLink  = windows.GUID{Data1: 0x00021401, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIShellLinkW  = windows.GUID{Data1: 0x000214F9, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIPersistFile = windows.GUID{Data1: 0x0000010B, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
)

const (
	clsctxInprocServer = 0x1
	coinitApartment    = 0x2
	sFalse             = syscall.Errno(1)
	rpcEChangedMode    = syscall.Errno(0x80010106)

	// IUnknown, IShellLinkW and IPersistFile vtable slots (ShObjIdl_core.h,
	// ObjIdl.h).
	vtRelease             = 2
	vtQueryInterface      = 0
	vtShellLinkGetPath    = 3
	vtShellLinkSetWorkDir = 9
	vtShellLinkSetPath    = 20
	vtPersistFileLoad     = 5
	vtPersistFileSave     = 6
)

// comObject is a COM interface pointer: its first word points at the vtable.
type comObject struct{ vtbl *[32]uintptr }

func (o *comObject) call(slot int, args ...uintptr) error {
	r, _, _ := syscall.SyscallN(o.vtbl[slot], append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)...)
	if int32(r) < 0 {
		return fmt.Errorf("HRESULT %#x", uint32(r))
	}
	return nil
}

func (o *comObject) release() { _ = o.call(vtRelease) }

// withCOM runs fn on its own OS thread inside its own single-threaded
// apartment, so it never depends on (or disturbs) the COM state WebView2
// set up on the shell's UI thread.
func withCOM(fn func() error) error {
	done := make(chan error, 1)
	go func() {
		defer logging.RecoverAndLog("desktop.withCOM")
		// Sent by a defer, so a panic in fn (logged and recovered above)
		// still answers the caller instead of leaving it blocked forever.
		err := errors.New("COM worker panicked")
		defer func() { done <- err }()
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		switch e := windows.CoInitializeEx(0, coinitApartment); {
		case e == nil, errors.Is(e, sFalse):
			defer windows.CoUninitialize()
		case errors.Is(e, rpcEChangedMode):
			// This thread is already in a multithreaded apartment someone
			// else owns; IShellLink works there too. Not ours to uninitialize.
		default:
			err = fmt.Errorf("CoInitializeEx: %w", e)
			return
		}
		err = fn()
	}()
	return <-done
}

// withShellLink creates an IShellLinkW plus its IPersistFile and hands both
// to fn; must run inside withCOM.
func withShellLink(fn func(link, file *comObject) error) error {
	var link *comObject
	if r, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidShellLink)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIShellLinkW)), uintptr(unsafe.Pointer(&link))); int32(r) < 0 {
		return fmt.Errorf("CoCreateInstance(ShellLink): HRESULT %#x", uint32(r))
	}
	defer link.release()
	var file *comObject
	if err := link.call(vtQueryInterface, uintptr(unsafe.Pointer(&iidIPersistFile)), uintptr(unsafe.Pointer(&file))); err != nil {
		return fmt.Errorf("QueryInterface(IPersistFile): %w", err)
	}
	defer file.release()
	return fn(link, file)
}

// writeShellLink saves a .lnk at lnkPath that starts target in workDir.
func writeShellLink(lnkPath, target, workDir string) error {
	return withCOM(func() error {
		return withShellLink(func(link, file *comObject) error {
			t, err := windows.UTF16PtrFromString(target)
			if err != nil {
				return err
			}
			if err := link.call(vtShellLinkSetPath, uintptr(unsafe.Pointer(t))); err != nil {
				return fmt.Errorf("IShellLink.SetPath: %w", err)
			}
			wd, err := windows.UTF16PtrFromString(workDir)
			if err != nil {
				return err
			}
			if err := link.call(vtShellLinkSetWorkDir, uintptr(unsafe.Pointer(wd))); err != nil {
				return fmt.Errorf("IShellLink.SetWorkingDirectory: %w", err)
			}
			p, err := windows.UTF16PtrFromString(lnkPath)
			if err != nil {
				return err
			}
			if err := file.call(vtPersistFileSave, uintptr(unsafe.Pointer(p)), 1); err != nil {
				return fmt.Errorf("IPersistFile.Save(%s): %w", lnkPath, err)
			}
			return nil
		})
	})
}

// readShellLinkTarget loads the .lnk at lnkPath and returns its target path.
func readShellLinkTarget(lnkPath string) (string, error) {
	var target string
	err := withCOM(func() error {
		return withShellLink(func(link, file *comObject) error {
			p, err := windows.UTF16PtrFromString(lnkPath)
			if err != nil {
				return err
			}
			if err := file.call(vtPersistFileLoad, uintptr(unsafe.Pointer(p)), 0); err != nil {
				return fmt.Errorf("IPersistFile.Load(%s): %w", lnkPath, err)
			}
			buf := make([]uint16, 32767) // the longest path Windows allows
			// SLGP_RAWPATH (4): the stored path, environment strings unexpanded.
			if err := link.call(vtShellLinkGetPath, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 4); err != nil {
				return fmt.Errorf("IShellLink.GetPath: %w", err)
			}
			target = windows.UTF16ToString(buf)
			return nil
		})
	})
	return target, err
}

// reconcileStartupShortcut makes dir hold a shortcut to exe exactly when
// enabled. Rewritten on every call, so a moved install or a shortcut the
// user deleted by hand is put right on the next launch — the same
// reconcile-every-launch shape as autostart_linux.go.
func reconcileStartupShortcut(dir string, enabled bool, exe string) error {
	path := filepath.Join(dir, autostartShortcutName)
	if !enabled {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove startup shortcut: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create startup folder: %w", err)
	}
	return writeShellLink(path, exe, filepath.Dir(exe))
}
