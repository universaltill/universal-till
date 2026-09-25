package pages

import (
	"context"
	"html"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/logging"
)

type mapKV map[string]string

func (m mapKV) Get(_ context.Context, k string) (string, bool, error) {
	v, ok := m[k]
	return v, ok, nil
}

// ut-docs#2720: the role printed in the startup line follows the same rule
// cloudsync's heartbeat reports (primary unless sync.primary_url is set,
// backoffice display mode wins).
func TestTillRole(t *testing.T) {
	cases := []struct {
		kv   mapKV
		want string
	}{
		{mapKV{}, "primary"},
		{mapKV{"sync.primary_url": "http://10.0.0.2:8080"}, "replica"},
		{mapKV{"sync.primary_url": "http://10.0.0.2:8080", "display.mode": "backoffice"}, "backoffice"},
		{mapKV{"sync.primary_url": "   "}, "primary"},
	}
	for _, c := range cases {
		if got := tillRole(t.Context(), c.kv); got != c.want {
			t.Errorf("tillRole(%v) = %q, want %q", c.kv, got, c.want)
		}
	}
}

// The Settings diagnostics card shows the log folder and the copyable
// summary (ut-docs#2720's "Show log folder" / "Copy diagnostics"), to a
// manager only.
func TestSettingsPage_DiagnosticsCardShowsLogFolder(t *testing.T) {
	mux, _ := newDiagnosticsDeps(t, "https://cloud.example.test/api")
	logPath := filepath.Join(t.TempDir(), "logs", "till.log")
	if err := logging.AttachFile(logPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(logging.DetachFile)
	prev := logging.RememberedStartup()
	logging.RememberStartup(logging.StartupInfo{EnvFile: "/opt/unitill/pos.env"})
	t.Cleanup(func() { logging.RememberStartup(prev) })

	body := getAs(mux, "/settings", &mgrUser).Body.String()
	for _, want := range []string{
		`data-testid="diagnostics-log-folder"`,
		html.EscapeString(filepath.Dir(logPath)),
		`data-testid="diagnostics-copy-log-folder"`,
		`data-testid="diagnostics-copy-summary"`,
		"cloud_host=cloud.example.test",
		html.EscapeString(`pos_env="/opt/unitill/pos.env"`),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("manager /settings missing %q", want)
		}
	}
	if strings.Contains(getAs(mux, "/settings", &cashUser).Body.String(), `diagnostics-log-folder`) {
		t.Fatal("cashier /settings leaks the log folder")
	}

	logging.DetachFile()
	body = getAs(mux, "/settings", &mgrUser).Body.String()
	if !strings.Contains(body, `data-testid="diagnostics-log-off"`) {
		t.Fatal("with no log file the card must say so instead of showing a folder")
	}
}
