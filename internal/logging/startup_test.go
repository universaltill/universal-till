package logging

import (
	"strings"
	"testing"
)

// ut-docs#2720: the one line that would have diagnosed the field report —
// a till that never checked in with the cloud — names the version, OS, data
// dir, the pos.env actually loaded, the cloud host (host only, never the
// path/query/credentials) and the enrolment state with just the store id's
// last four characters.
func TestStartupInfoLine(t *testing.T) {
	info := StartupInfo{
		Version:     "v0.22.2",
		GOOS:        "windows",
		GOARCH:      "amd64",
		DataDir:     `C:\Users\shop\AppData\Local\UniversalTill`,
		EnvFile:     `C:\Program Files\UniversalTill\pos.env`,
		EndpointURL: "https://svc:pw@cloud.universaltill.com/api?x=1",
		Enrolled:    true,
		StoreID:     "5b0e2a39-8c1f-4d7a-9b6e-3f1a2c4d9c1d",
		Role:        "primary",
		LogFile:     `C:\Users\shop\AppData\Local\UniversalTill\logs\till.log`,
	}
	line := info.Line()
	for _, want := range []string{
		"version=v0.22.2",
		"os=windows/amd64",
		`data_dir="C:\\Users\\shop\\AppData\\Local\\UniversalTill"`,
		`pos_env="C:\\Program Files\\UniversalTill\\pos.env"`,
		"cloud_host=cloud.universaltill.com",
		"enrolled=yes",
		"store=…9c1d",
		"role=primary",
		`log_file="C:\\Users\\shop\\AppData\\Local\\UniversalTill\\logs\\till.log"`,
	} {
		if !strings.Contains(line, want) {
			t.Errorf("startup line missing %q:\n%s", want, line)
		}
	}
	for _, leak := range []string{"5b0e2a39", "svc", "pw@", "/api", "x=1"} {
		if strings.Contains(line, leak) {
			t.Errorf("startup line leaks %q:\n%s", leak, line)
		}
	}
	if Redact(line) != line {
		t.Errorf("the startup line itself must survive redaction intact:\n%s\n%s", line, Redact(line))
	}
}

// The unconfigured cases are spelled out, not left blank — "blank" is
// exactly the ambiguity that made the field report undiagnosable.
func TestStartupInfoLineUnconfigured(t *testing.T) {
	line := StartupInfo{Version: "dev", GOOS: "linux", GOARCH: "arm64", DataDir: "/var/lib/unitill"}.Line()
	for _, want := range []string{
		`pos_env="` + EnvFileNone + `"`,
		"cloud_host=none",
		"enrolled=no",
		"store=none",
		"role=unknown",
		`log_file="off"`,
	} {
		if !strings.Contains(line, want) {
			t.Errorf("startup line missing %q:\n%s", want, line)
		}
	}
	if got := (StartupInfo{StoreID: "ab"}).Line(); !strings.Contains(got, "store=…ab") {
		t.Errorf("short store id: %s", got)
	}
}

func TestEndpointHost(t *testing.T) {
	cases := map[string]string{
		"https://cloud.universaltill.com/api":     "cloud.universaltill.com",
		"http://127.0.0.1:8081/api":               "127.0.0.1:8081",
		"https://u:p@sql.universaltill.com:443/x": "sql.universaltill.com:443",
		"":          "none",
		"not a url": "invalid",
	}
	for in, want := range cases {
		if got := EndpointHost(in); got != want {
			t.Errorf("EndpointHost(%q) = %q, want %q", in, got, want)
		}
	}
}
