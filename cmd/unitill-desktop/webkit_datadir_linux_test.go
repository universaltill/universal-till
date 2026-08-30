//go:build linux

package main

import (
	"path/filepath"
	"testing"
)

func TestWebkitDataDir_HonorsXDGDataHome(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)

	got, err := webkitDataDir()
	if err != nil {
		t.Fatalf("webkitDataDir: %v", err)
	}
	want := filepath.Join(dataHome, "universal-till", "webkit")
	if got != want {
		t.Errorf("webkitDataDir() = %q, want %q", got, want)
	}
}

func TestWebkitDataDir_FallsBackToHomeLocalShare(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := webkitDataDir()
	if err != nil {
		t.Fatalf("webkitDataDir: %v", err)
	}
	want := filepath.Join(home, ".local", "share", "universal-till", "webkit")
	if got != want {
		t.Errorf("webkitDataDir() = %q, want %q", got, want)
	}
}
