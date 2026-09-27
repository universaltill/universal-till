package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot is where `go tool go-winres` resolves (go.mod's tool directive).
const repoRoot = "../../.."

// buildHello cross-compiles testdata/hello for windows/amd64, first copying
// syso (if non-empty) next to its main.go, and returns the exe path.
func buildHello(t *testing.T, syso string) string {
	t.Helper()
	dir := t.TempDir()
	src, err := os.ReadFile("testdata/hello/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module hello\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if syso != "" {
		b, err := os.ReadFile(syso)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "rsrc_windows_amd64.syso"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	exe := filepath.Join(dir, "hello.exe")
	cmd := exec.Command("go", "build", "-o", exe, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=-mod=mod")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return exe
}

// winres generates the .syso for one of packaging/windows/winres/*.json —
// the real release config, so a broken config fails here too.
func winres(t *testing.T, kind string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("cross-compiles Windows binaries")
	}
	in, err := filepath.Abs(filepath.Join(repoRoot, "packaging/windows/winres", kind+".json"))
	if err != nil {
		t.Fatal(err)
	}
	outPrefix := filepath.Join(t.TempDir(), "rsrc")
	cmd := exec.Command("go", "tool", "go-winres", "make", "--in", in, "--arch", "amd64",
		"--out", outPrefix, "--product-version", "1.2.3", "--file-version", "1.2.3")
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go-winres: %v\n%s", err, out)
	}
	return outPrefix + "_windows_amd64.syso"
}

func TestCheckAcceptsReleaseConfigs(t *testing.T) {
	for _, kind := range []string{"server", "desktop"} {
		t.Run(kind, func(t *testing.T) {
			exe := buildHello(t, winres(t, kind))
			if err := check(exe); err != nil {
				t.Fatalf("check(%s exe) = %v, want nil", kind, err)
			}
		})
	}
}

func TestCheckRejectsExeWithoutResources(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-compiles Windows binaries")
	}
	exe := buildHello(t, "")
	err := check(exe)
	if err == nil {
		t.Fatal("check(exe without resources) = nil, want an error")
	}
	if !strings.Contains(err.Error(), "RT_GROUP_ICON") {
		t.Fatalf("error %q does not name the missing RT_GROUP_ICON", err)
	}
}

func TestCheckRejectsNonPE(t *testing.T) {
	f := filepath.Join(t.TempDir(), "not.exe")
	if err := os.WriteFile(f, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := check(f); err == nil {
		t.Fatal("check(non-PE file) = nil, want an error")
	}
}
