//go:build desktop && windows

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/paths"
)

// webview2ClientKey is the Evergreen WebView2 Runtime's EdgeUpdate client
// key; its "pv" value is the installed runtime version (Microsoft's
// documented detection method, "Detect if a WebView2 Runtime is already
// installed").
const webview2ClientKey = `SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`

// envWebView2UserDataFolder is Microsoft's override variable for the WebView2
// user data folder. The vendored webview library's built-in loader does not
// apply it on its own, so embed() reads it (webview.h, "universal-till
// patch", ut-docs#2761).
const envWebView2UserDataFolder = "WEBVIEW2_USER_DATA_FOLDER"

func init() {
	setupPersistentCookies = setupWebView2DataDir
	webViewRuntimeVersion = webview2RuntimeVersion
}

// setupWebView2DataDir pins WebView2's user data folder — where it keeps
// cookies, i.e. the till login and language choice — to this install's
// data directory (ut-docs#2761) instead of the library default,
// %APPDATA%\unitill-desktop.exe, which every install on the account shared.
// An operator-set WEBVIEW2_USER_DATA_FOLDER wins. It also logs the runtime
// version, so a later WebView2 failure in desktop.log can be matched to a
// runtime self-update.
func setupWebView2DataDir() error {
	logging.L().Infof("WebView2 runtime version=%q", webview2RuntimeVersion())
	if os.Getenv(envWebView2UserDataFolder) != "" {
		return nil
	}
	dir := filepath.Join(shellDataDir(os.Getenv("UT_DATA_DIR"), paths.Default()), "webview2")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create WebView2 user data folder %s: %w", dir, err)
	}
	logging.L().Infof("WebView2 user data folder %s", dir)
	return os.Setenv(envWebView2UserDataFolder, dir)
}

// webview2RuntimeVersion is the installed Evergreen WebView2 Runtime version,
// "" when none is registered. Per-machine installs register under the
// 32-bit registry view (WOW6432Node on 64-bit Windows), per-user ones under
// HKCU.
func webview2RuntimeVersion() string {
	for _, loc := range []struct {
		root   registry.Key
		access uint32
	}{
		{registry.LOCAL_MACHINE, registry.QUERY_VALUE | registry.WOW64_32KEY},
		{registry.CURRENT_USER, registry.QUERY_VALUE},
	} {
		k, err := registry.OpenKey(loc.root, webview2ClientKey, loc.access)
		if err != nil {
			continue
		}
		pv, _, err := k.GetStringValue("pv")
		k.Close()
		if err == nil && pv != "" && pv != "0.0.0.0" {
			return pv
		}
	}
	return ""
}
