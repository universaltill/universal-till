package listenport

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// allFree / busy are bindable stand-ins, so these tests never depend on
// which real ports happen to be free on the machine running them.
func allFree(int) bool { return true }

func busy(ports ...int) func(int) bool {
	return func(p int) bool {
		for _, b := range ports {
			if p == b {
				return false
			}
		}
		return true
	}
}

func TestChoose_NoSavedPort_UsesDefault(t *testing.T) {
	dir := t.TempDir()
	if got := Choose(dir, 8080, allFree); got != 8080 {
		t.Fatalf("Choose with nothing saved = %d, want the default 8080", got)
	}
}

// ut-docs#2722: the port a main till bound last time is what its paired
// replicas stored — it must be reused on the next launch, not re-randomised.
func TestChoose_PrefersSavedPortOverDefault(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, 8093); err != nil {
		t.Fatal(err)
	}
	if got := Choose(dir, 8080, allFree); got != 8093 {
		t.Fatalf("Choose = %d, want the saved port 8093", got)
	}
}

func TestChoose_SavedPortBusy_FallsBackToNextFree(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, 8093); err != nil {
		t.Fatal(err)
	}
	if got := Choose(dir, 8080, busy(8093, 8094)); got != 8095 {
		t.Fatalf("Choose = %d, want 8095 (first free after the busy saved port)", got)
	}
}

func TestChoose_EverythingNearbyBusy_ReturnsZeroForOSPick(t *testing.T) {
	dir := t.TempDir()
	if got := Choose(dir, 8080, func(int) bool { return false }); got != 0 {
		t.Fatalf("Choose = %d, want 0 (caller lets the OS pick)", got)
	}
}

func TestChoose_IgnoresCorruptOrOutOfRangeSavedValue(t *testing.T) {
	for _, raw := range []string{"not-a-port", "0", "80", "70000", ""} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, FileName), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := Choose(dir, 8080, allFree); got != 8080 {
			t.Errorf("saved %q: Choose = %d, want the default 8080", raw, got)
		}
	}
}

func TestSave_CreatesMissingDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "data")
	if err := Save(dir, 8081); err != nil {
		t.Fatalf("Save into a not-yet-created dir: %v", err)
	}
	if got := Saved(dir); got != 8081 {
		t.Fatalf("Saved = %d, want 8081", got)
	}
}

func TestSave_RejectsInvalidPort(t *testing.T) {
	if err := Save(t.TempDir(), 0); err == nil {
		t.Fatal("Save(0) should be refused — 0 is 'let the OS pick', never a port to reuse")
	}
}

func TestBindable_RealSocket(t *testing.T) {
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Skipf("cannot bind in this sandbox: %v", err)
	}
	defer ln.Close()
	_, ps, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(ps)
	if Bindable(p) {
		t.Fatalf("Bindable(%d) = true while this test holds it", p)
	}
}
