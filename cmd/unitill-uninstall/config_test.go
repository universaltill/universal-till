package main

import (
	"os"
	"path/filepath"
	"testing"
)

// clearConfigEnv unsets the env vars these tests (and the pos.env files they
// load — godotenv SETS process env) touch, restoring originals afterwards,
// so tests neither read nor leak ambient state.
func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"UT_DATA_DIR", "UT_DB_PATH", "UT_STORE_NAME", "UT_DEFAULT_LOCALE"} {
		orig, had := os.LookupEnv(k)
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if had {
				_ = os.Setenv(k, orig)
			} else {
				_ = os.Unsetenv(k)
			}
		})
	}
}

// A pos.env that overrides UT_DATA_DIR/UT_DB_PATH must be honoured exactly
// the way unitill-pos.service honours it (EnvironmentFile wins over the
// unit's own Environment=UT_DATA_DIR default).
func TestLoadServiceConfigHonoursPosEnvOverrides(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	envFile := filepath.Join(dir, "pos.env")
	content := "# admin moved the data\n" +
		"\n" +
		"UT_DATA_DIR=" + filepath.Join(dir, "moved") + "\n" +
		"UT_DB_PATH=" + filepath.Join(dir, "moved", "custom.db") + "\n"
	if err := os.WriteFile(envFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadServiceConfig(envFile, filepath.Join(dir, "unit-default"))
	if err != nil {
		t.Fatalf("loadServiceConfig: %v", err)
	}
	if cfg.DataDir != filepath.Join(dir, "moved") {
		t.Errorf("DataDir = %q, want pos.env override", cfg.DataDir)
	}
	if cfg.DBPath != filepath.Join(dir, "moved", "custom.db") {
		t.Errorf("DBPath = %q, want pos.env override", cfg.DBPath)
	}
}

// With no override in pos.env, the CLI must land on the same default the
// systemd unit pins (Environment=UT_DATA_DIR=/opt/unitill/data) — NOT the
// per-user default a bare config.Init would pick.
func TestLoadServiceConfigDefaultsToUnitDataDir(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	envFile := filepath.Join(dir, "pos.env")
	if err := os.WriteFile(envFile, []byte("UT_STORE_NAME=Task Runner\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unitDefault := filepath.Join(dir, "opt-data")

	cfg, err := loadServiceConfig(envFile, unitDefault)
	if err != nil {
		t.Fatalf("loadServiceConfig: %v", err)
	}
	if cfg.DataDir != unitDefault {
		t.Errorf("DataDir = %q, want unit default %q", cfg.DataDir, unitDefault)
	}
	if want := filepath.Join(unitDefault, "unitill-pos.db"); cfg.DBPath != want {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, want)
	}
}

// A missing pos.env (hand-built box, file deleted) is not an error — the
// unit-default data dir still resolves.
func TestLoadServiceConfigMissingPosEnv(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	unitDefault := filepath.Join(dir, "opt-data")

	cfg, err := loadServiceConfig(filepath.Join(dir, "does-not-exist.env"), unitDefault)
	if err != nil {
		t.Fatalf("loadServiceConfig: %v", err)
	}
	if cfg.DataDir != unitDefault {
		t.Errorf("DataDir = %q, want unit default", cfg.DataDir)
	}
}

// Comment and blank lines in pos.env must be skipped, and values may
// contain '=' after the first split.
func TestLoadServiceConfigSkipsCommentsAndSplitsOnFirstEquals(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	envFile := filepath.Join(dir, "pos.env")
	content := "# comment line\n\nUT_DB_PATH=" + filepath.Join(dir, "a=b.db") + "\n"
	if err := os.WriteFile(envFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadServiceConfig(envFile, filepath.Join(dir, "opt-data"))
	if err != nil {
		t.Fatalf("loadServiceConfig: %v", err)
	}
	if want := filepath.Join(dir, "a=b.db"); cfg.DBPath != want {
		t.Errorf("DBPath = %q, want %q (split on FIRST '=')", cfg.DBPath, want)
	}
}
