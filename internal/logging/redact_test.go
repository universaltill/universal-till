package logging

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// secretSamples are token-looking strings that must never reach the log
// file (ut-docs#2720 AC: same no-secrets rule as the diagnostics stream).
var secretSamples = []struct{ line, secret string }{
	{"auth header Authorization: Bearer 9f8e7d6c5b4a39281706f5e4d3c2b1a0ffeeddcc", "9f8e7d6c5b4a39281706f5e4d3c2b1a0ffeeddcc"},
	{"calling cloud with bearer abc123DEF456ghi789", "abc123DEF456ghi789"},
	{"merchant_token=s3cr3t-Merchant-T0ken", "s3cr3t-Merchant-T0ken"},
	{`settings {"client_secret":"hunter2-is-the-pw"}`, "hunter2-is-the-pw"},
	{"UT_MARKETPLACE_CLIENT_SECRET: topsecretvalue", "topsecretvalue"},
	{"admin password = CorrectHorse", "CorrectHorse"},
	{"login pin=4821 accepted", "4821"},
	{"GET https://user:p4ssw0rd@cloud.example.com/api", "p4ssw0rd"},
	{"GET /v1/devices?store_id=abc&access_token=q1w2e3r4t5y6", "q1w2e3r4t5y6"},
	{"id token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U seen", "eyJhbGciOiJIUzI1NiJ9"},
	{"sync bearer 2b7e151628aed2a6abf7158809cf4f3c762e7160f38b4da56a784d9045190cfe issued", "2b7e151628aed2a6abf7158809cf4f3c762e7160f38b4da56a784d9045190cfe"},
	{"raw key Zm9vYmFyYmF6cXV4MTIzNDU2Nzg5MGFiY2RlZmdoaWo in body", "Zm9vYmFyYmF6cXV4MTIzNDU2Nzg5MGFiY2RlZmdoaWo"},
	{"Cookie: ut_session=Q2FzaGllclNlc3Npb24xMjM", "Q2FzaGllclNlc3Npb24xMjM"},
}

func TestRedactRemovesSecrets(t *testing.T) {
	for _, s := range secretSamples {
		got := Redact(s.line)
		if strings.Contains(got, s.secret) {
			t.Errorf("secret survived redaction:\n in: %s\nout: %s", s.line, got)
		}
		if !strings.Contains(got, redactedMark) {
			t.Errorf("no %s marker in %q", redactedMark, got)
		}
	}
}

// Redaction must not destroy what makes the log useful for diagnosis:
// ids, versions, paths, hosts and ordinary prose survive unchanged.
func TestRedactKeepsDiagnosticDetail(t *testing.T) {
	keep := []string{
		"startup: version=v0.22.2 os=windows/amd64 data_dir=\"C:\\Users\\shop\\AppData\\Local\\UniversalTill\" cloud_host=cloud.universaltill.com enrolled=yes store=…9c1d role=primary",
		"enrolment: till registered with marketplace as 5b0e2a39-8c1f-4d7a-9b6e-3f1a2c4d9c1d (device till-0f6a1b2c-3d4e-4f50-8a9b-0c1d2e3f4a5b)",
		"plugin ut-plugin-payment-sumup 1.4.2 installed from /Users/shop/Library/Application Support/UniversalTill/plugins/ut-plugin-payment-sumup/1.4.2",
		"cloudsync: heartbeat failed (will retry): Post \"https://cloud.universaltill.com/api/v1/heartbeat\": dial tcp: lookup cloud.universaltill.com: no such host",
		"Universal Till POS starting...",
		"shipping pinned items: 3",
		"data dir /tmp/TestRun_WritesLogFileWithStartupLine3162494016/001 ready",
		"plugin ut-plugin-fiscal-de-tse2-signing-adapter-v2 loaded",
	}
	for _, line := range keep {
		if got := Redact(line); got != line {
			t.Errorf("redaction changed a harmless line:\n in: %s\nout: %s", line, got)
		}
	}
}

// End to end: a token logged through the package logger (and through the
// stdlib log package, which much of the till still uses) is redacted in the
// file.
func TestAttachedFileNeverContainsToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "till.log")
	if err := AttachFile(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(DetachFile)

	const tok = "9f8e7d6c5b4a39281706f5e4d3c2b1a0ffeeddcc"
	L().Infof("cloud call Authorization: Bearer %s", tok)
	L().Warnf("merchant_token=%s rejected", "s3cr3t-Merchant-T0ken")
	log.Printf("sync bearer %s issued", tok)
	L().Infof("marker line")
	DetachFile()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	if !strings.Contains(out, "marker line") {
		t.Fatalf("log file missing lines: %q", out)
	}
	for _, secret := range []string{tok, "s3cr3t-Merchant-T0ken"} {
		if strings.Contains(out, secret) {
			t.Fatalf("token reached the log file: %q", out)
		}
	}
	if !strings.Contains(out, "sync bearer "+redactedMark) {
		t.Fatalf("stdlib log line not captured (redacted) in file: %q", out)
	}
}
