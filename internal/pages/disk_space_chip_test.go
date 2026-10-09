package pages

import (
	"net/http"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/diskspace"
)

// ut-docs#3121 AC 3: below the low-disk floor a manager sees a status-bar
// chip — never a modal, selling is never blocked — and a cashier sees
// nothing.

func newDiskSpaceChipMux(t *testing.T, u diskspace.Usage, err error) *http.ServeMux {
	t.Helper()
	chdirRoot(t)
	orig := diskSpaceChipProbe
	diskSpaceChipProbe = func(string) (diskspace.Usage, error) { return u, err }
	t.Cleanup(func() { diskSpaceChipProbe = orig })
	mux, _, d := newFullAuthDeps(t)
	registerDiskSpaceChip(mux, d)
	return mux
}

const diskLowLabel = "Storage almost full"

func TestDiskSpaceChip_EmptyAboveTheFloor(t *testing.T) {
	mux := newDiskSpaceChipMux(t, diskspace.Usage{Free: 40 << 30, Total: 64 << 30}, nil)
	rec := getAs(mux, "/ui/disk-space-chip", &mgrUser)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("roomy disk: code=%d body=%q, want an empty 200", rec.Code, rec.Body.String())
	}
}

func TestDiskSpaceChip_EmptyWhenFreeSpaceIsUnknown(t *testing.T) {
	mux := newDiskSpaceChipMux(t, diskspace.Usage{}, diskspace.ErrUnsupported)
	rec := getAs(mux, "/ui/disk-space-chip", &mgrUser)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("unknown free space: code=%d body=%q, want an empty 200", rec.Code, rec.Body.String())
	}
}

func TestDiskSpaceChip_ManagerSeesTheChipBelowTheFloor(t *testing.T) {
	mux := newDiskSpaceChipMux(t, diskspace.Usage{Free: 100 << 20, Total: 16 << 30}, nil)
	rec := getAs(mux, "/ui/disk-space-chip", &mgrUser)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, diskLowLabel) {
		t.Fatalf("code=%d body=%s, want %q", rec.Code, body, diskLowLabel)
	}
	if !strings.Contains(body, `data-testid="sb-disk-low"`) || !strings.Contains(body, `href="/settings#settings-backup"`) {
		t.Fatalf("chip must carry its testid and link to Settings → Backups: %s", body)
	}
	if !strings.Contains(body, `title="`) {
		t.Fatalf("chip must explain itself in its title: %s", body)
	}
	for _, modal := range []string{"<dialog", "showModal", `role="alertdialog"`} {
		if strings.Contains(body, modal) {
			t.Fatalf("chip carries %q — must never be a modal: %s", modal, body)
		}
	}
}

func TestDiskSpaceChip_CashierSeesNothing(t *testing.T) {
	mux := newDiskSpaceChipMux(t, diskspace.Usage{Free: 100 << 20, Total: 16 << 30}, nil)
	rec := getAs(mux, "/ui/disk-space-chip", &cashUser)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("cashier: code=%d body=%q, want an empty 200", rec.Code, rec.Body.String())
	}
}
