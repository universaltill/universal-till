package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ut-docs#2720: a real boot writes <data dir>/logs/till.log, and it carries
// the startup line naming the pos.env actually loaded (absolute path), the
// data dir, the cloud HOST only, and the enrolment state — while a secret
// from that pos.env never reaches the file.
func TestRun_WritesLogFileWithStartupLine(t *testing.T) {
	dataDir := t.TempDir()
	envDir := t.TempDir()
	envFile := filepath.Join(envDir, "pos.env")
	const secret = "s3cr3t-Merchant-T0ken"
	if err := os.WriteFile(envFile, []byte("UT_MARKETPLACE_ENDPOINT_URL=https://cloud.example.test/api\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UT_DATA_DIR", dataDir)
	t.Setenv("UT_ENV_FILE", envFile)
	t.Setenv("UT_MARKETPLACE_ENDPOINT_URL", "") // pos.env must supply it
	t.Setenv("UT_MARKETPLACE_MERCHANT_TOKEN", secret)
	t.Setenv("UT_LOG_FILE", "")
	t.Setenv("UT_AUTH", "off")
	t.Setenv("UT_OPEN_BROWSER", "false")
	t.Setenv("UT_LISTEN_ADDR", "127.0.0.1:0")
	_ = os.Unsetenv("UT_MARKETPLACE_ENDPOINT_URL")

	bootReachedPagesInit := observePagesInit(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx) }()
	select {
	case <-bootReachedPagesInit:
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not reach pages.Init within 30s")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return within 30s of ctx cancellation")
	}

	b, err := os.ReadFile(filepath.Join(dataDir, "logs", "till.log"))
	if err != nil {
		t.Fatalf("no log file after boot: %v", err)
	}
	out := string(b)
	var startup string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "startup: version=") {
			startup = l
		}
	}
	if startup == "" {
		t.Fatalf("no startup line in till.log:\n%s", out)
	}
	for _, want := range []string{
		`pos_env="` + strings.ReplaceAll(envFile, `\`, `\\`) + `"`,
		`data_dir="` + strings.ReplaceAll(dataDir, `\`, `\\`) + `"`,
		"cloud_host=cloud.example.test ",
		"enrolled=",
		"role=primary",
	} {
		if !strings.Contains(startup, want) {
			t.Errorf("startup line missing %q:\n%s", want, startup)
		}
	}
	if strings.Contains(out, secret) {
		t.Fatalf("a secret reached till.log:\n%s", out)
	}
}
