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
	if got := backupSaveFailureKey(err); got != "settings.backup.save_failed_use_picker" {
		t.Fatalf("key = %q", got)
	}
}

// ut-docs#5619: the Android app's save picker is wired from the Settings
// page: the button names its backup and the bridge result is translated.
func TestSettingsBackupPickerWiring(t *testing.T) {
	b, err := os.ReadFile("web/ui/pages/settings.html") // TestMain chdirs to the repo root
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`data-backup-save="{{ .Name }}"`, "AndroidKiosk", "saveBackupCopy", "utBackupSaveResult",
		"/api/backup/copied/", `"settings.backup.picker_saved"`, `"settings.backup.picker_cancelled"`,
		`"settings.backup.picker_failed"`, `"settings.backup.picker_no_space"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("settings.html lacks %q", want)
		}
	}
}
