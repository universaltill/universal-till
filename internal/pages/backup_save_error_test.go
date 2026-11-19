package pages

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"testing"
)

// ut-docs#5619: "Could not save the backup copy." named no reason, so an
// owner whose Downloads folder was unreachable (Android storage on USB-C)
// could not tell what to do. The reason now picks a specific, translated key.
func TestBackupSaveFailureKey(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"disk full", &os.PathError{Op: "write", Path: "x", Err: syscall.ENOSPC}, "settings.backup.save_failed_space"},
		{"permission", &os.PathError{Op: "mkdir", Path: "x", Err: fs.ErrPermission}, "settings.backup.save_failed_permission"},
		{"read-only", &os.PathError{Op: "create", Path: "x", Err: syscall.EROFS}, "settings.backup.save_failed_permission"},
		{"missing folder", &os.PathError{Op: "stat", Path: "x", Err: fs.ErrNotExist}, "settings.backup.save_failed_unreachable"},
		{"other", errors.New("boom"), "settings.backup.save_failed"},
	}
	for _, c := range cases {
		if got := backupSaveFailureKey(c.err); got != c.want {
			t.Errorf("%s: key = %q, want %q", c.name, got, c.want)
		}
	}
}

// On Android the till process cannot write to the shared Downloads folder
// (scoped storage; storage may be a USB drive); the page saves through the
// native picker instead, so a direct save is refused with its own reason.
func TestSaveBackupToDownloadsRefusedOnAndroid(t *testing.T) {
	old := hostIsAndroid
	hostIsAndroid = true
	defer func() { hostIsAndroid = old }()
	_, err := saveBackupToDownloads("src", "unitill-pos-20260716-120000.db")
	if !errors.Is(err, errUseNativePicker) {
		t.Fatalf("err = %v, want errUseNativePicker", err)
	}
	if got := backupSaveFailureKey(err); got != "settings.backup.save_failed_picker_unavailable" {
		t.Fatalf("key = %q", got)
	}
}

// ut-docs#5619 / #6550: the Android save picker is wired from the shell, not a
// per-visit page script (e2e/tests/backup-save-picker-6550.spec.ts is the real
// regression test). This is only a secondary string check that the pieces exist.
func TestSettingsBackupPickerWiring(t *testing.T) {
	page, err := os.ReadFile("web/ui/pages/settings.html") // TestMain chdirs to the repo root
	if err != nil {
		t.Fatal(err)
	}
	js, err := os.ReadFile("web/public/inline-actions.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`data-backup-save="{{ .Name }}"`, `data-backup-native="1"`, `$.backupNativePicker`,
		`data-picker-saved="{{ T "settings.backup.picker_saved" }}"`,
		`data-picker-cancelled="{{ T "settings.backup.picker_cancelled" }}"`,
		`data-picker-failed="{{ T "settings.backup.picker_failed" }}"`,
		`data-picker-no-space="{{ T "settings.backup.picker_no_space" }}"`,
		`data-picker-unavailable="{{ T "settings.backup.save_failed_picker_unavailable" }}"`,
	} {
		if !strings.Contains(string(page), want) {
			t.Errorf("settings.html lacks %q", want)
		}
	}
	// The old per-visit inline script (killed by dropPageListeners) must not return.
	if strings.Contains(string(page), "window.utBackupSaveResult") {
		t.Error("settings.html defines utBackupSaveResult again; it belongs in inline-actions.js")
	}
	for _, want := range []string{"saveBackupCopy", "utBackupSaveResult", "/api/backup/copied/", "data-backup-save"} {
		if !strings.Contains(string(js), want) {
			t.Errorf("inline-actions.js lacks %q", want)
		}
	}
}

// ut-docs#6550: the page marks the Save button for the Android app from the
// real host flag, so a missing bridge there is answered on the client instead
// of by the server's dead-end message.
func TestSettingsPageFlagsNativePickerFromHost(t *testing.T) {
	src := readRepoFile(t, "internal/pages/settings_page.go")
	if !strings.Contains(src, `"backupNativePicker": hostIsAndroid`) {
		t.Error(`settings_page.go must pass "backupNativePicker": hostIsAndroid to the template`)
	}
}
