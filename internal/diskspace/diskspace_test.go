package diskspace

import (
	"testing"
)

// ut-docs#3121 AC 1: the floor is 500 MB or 10% of the disk, whichever is
// larger.
func TestFloor(t *testing.T) {
	for _, c := range []struct {
		total, want uint64
	}{
		{0, MinFreeBytes},           // unknown size: the absolute floor
		{4 << 30, MinFreeBytes},     // 4 GiB: 10% is 410 MiB, under 500 MiB
		{5000 << 20, MinFreeBytes},  // exactly 10% = 500 MiB
		{64 << 30, (64 << 30) / 10}, // 64 GiB: 10% wins
		{1 << 40, (1 << 40) / 10},   // 1 TiB
	} {
		if got := Floor(c.total); got != c.want {
			t.Errorf("Floor(%d) = %d, want %d", c.total, got, c.want)
		}
	}
}

func TestLow(t *testing.T) {
	const total = 64 << 30 // floor = 6.4 GiB
	floor := Floor(total)
	for _, c := range []struct {
		free uint64
		want bool
	}{
		{0, true},
		{floor - 1, true},
		{floor, false}, // at the floor is not below it
		{total, false},
	} {
		if got := Low(Usage{Free: c.free, Total: total}); got != c.want {
			t.Errorf("Low(free=%d) = %v, want %v", c.free, got, c.want)
		}
	}
	// A 2 GiB card: the 500 MiB absolute floor applies.
	if !Low(Usage{Free: 400 << 20, Total: 2 << 30}) {
		t.Error("400 MiB free on 2 GiB must be low (500 MiB floor)")
	}
	if Low(Usage{Free: 600 << 20, Total: 2 << 30}) {
		t.Error("600 MiB free on 2 GiB must not be low")
	}
}

// The real probe on a real directory: on every platform the till builds for
// it reports a non-zero disk with free space no larger than the disk.
func TestProbe_RealDirectory(t *testing.T) {
	u, err := Probe(t.TempDir())
	if err != nil {
		t.Skipf("no free-space probe on this platform: %v", err)
	}
	if u.Total == 0 || u.Free > u.Total {
		t.Fatalf("Probe = %+v, want 0 < Free <= Total", u)
	}
}

func TestProbe_MissingDirectoryIsAnError(t *testing.T) {
	if _, err := Probe("/nonexistent/ut-docs-3121/really-not-here"); err == nil {
		t.Fatal("Probe of a missing path returned no error")
	}
}
