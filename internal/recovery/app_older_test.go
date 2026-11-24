package recovery

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
)

func TestClassify_DatabaseNewerThanApp(t *testing.T) {
	err := fmt.Errorf("run migrations: %w", fmt.Errorf("%w (data at migration 115, this app has up to 107)", db.ErrDatabaseNewerThanApp))
	f, ok := Classify(err)
	if !ok {
		t.Fatal("must be recoverable")
	}
	if f.Kind != KindAppOlderThanData {
		t.Fatalf("Kind = %q, want %q", f.Kind, KindAppOlderThanData)
	}
	if f.RefCode == "" || f.Detail == "" {
		t.Fatalf("Detail/RefCode must still be set: %+v", f)
	}
}

func TestMessageKey_AppOlderThanData(t *testing.T) {
	if got := messageKey(KindAppOlderThanData); got != "recovery.app_older_than_data" {
		t.Fatalf("messageKey = %q", got)
	}
}

func appOlderPage(t *testing.T, kind Kind, detail string) string {
	t.Helper()
	addr := freeAddr(t)
	cfg := testConfig(t, addr)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_, _ = Serve(ctx, cfg, Failure{Kind: kind, Detail: detail, RefCode: "AAAA-BBBB"})
	}()
	waitListening(t, addr)
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestServe_AppOlderThanDataPage(t *testing.T) {
	detail := "run migrations: " + db.ErrDatabaseNewerThanApp.Error() + " (data at migration 115, this app has up to 107)"
	page := appOlderPage(t, KindAppOlderThanData, detail)
	msg := httpx.T("en", "recovery.app_older_than_data")
	if msg == "recovery.app_older_than_data" || msg == "" {
		t.Fatalf("en text for the key is missing: %q", msg)
	}
	if !strings.Contains(page, strings.ReplaceAll(msg, "'", "&#39;")) && !strings.Contains(page, msg) {
		t.Errorf("page lacks the app-older message\n%s", page)
	}
	if strings.Contains(page, `id="retry-btn"`) {
		t.Error("Retry must be hidden for app-older-than-data (it can only fail the same way)")
	}
	if strings.Contains(strings.ToLower(page), "delete the data directory") {
		t.Error("page must never advise deleting the data directory")
	}
	if !strings.Contains(page, "AAAA-BBBB") {
		t.Error("ref code missing")
	}
}

func TestServe_MigrationFailureStillHasRetry(t *testing.T) {
	page := appOlderPage(t, KindMigration, "exec migration 42: syntax error")
	if !strings.Contains(page, `id="retry-btn"`) {
		t.Error("Retry must remain for other kinds")
	}
}
