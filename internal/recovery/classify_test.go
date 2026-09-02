package recovery

import (
	"errors"
	"fmt"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

func TestClassify_DataDirLockedIsNotRecoverable(t *testing.T) {
	// ut-docs#1097's own contract (internal/db/lock.go, db.ErrDataDirLocked's
	// doc comment): "Call sites are expected to treat ErrDataDirLocked as
	// fatal ... never to retry a different address." Recovery mode must not
	// weaken that — a second real process racing for the same data
	// directory still gets refused outright, unchanged from today, not
	// shown a Retry button that could plausibly encourage retrying against
	// a live owner. internal/app.Run never even calls Classify for this
	// error (it stays on the existing fatal path) — this test guards the
	// classifier itself in case a future caller wires it in by mistake.
	_, recoverable := Classify(fmt.Errorf("acquire data directory lock: %w", db.ErrDataDirLocked))
	if recoverable {
		t.Fatal("Classify treated db.ErrDataDirLocked as recoverable — ADR-0075 leaves #1097's fatal contract untouched")
	}
}

func TestClassify_MigrationFailureIsRecoverable(t *testing.T) {
	f, recoverable := Classify(fmt.Errorf("run migrations: %w", errors.New("exec migration 42 (add_widgets): near \"WIDGT\": syntax error")))
	if !recoverable {
		t.Fatal("Classify treated a migration failure as unrecoverable — this is exactly the ut-docs#1412 case recovery mode exists for")
	}
	if f.Kind != KindMigration {
		t.Fatalf("Kind = %q, want %q", f.Kind, KindMigration)
	}
	if f.RefCode == "" {
		t.Fatal("RefCode is empty — the recovery page needs a short reference code for a support phone call")
	}
}

func TestClassify_GenericDBOpenFailureIsRecoverable(t *testing.T) {
	f, recoverable := Classify(fmt.Errorf("open sqlite: %w", errors.New("unable to open database file")))
	if !recoverable {
		t.Fatal("Classify treated a generic DB-open failure as unrecoverable")
	}
	if f.Kind != KindDBOpen {
		t.Fatalf("Kind = %q, want %q", f.Kind, KindDBOpen)
	}
}

func TestClassify_DiskFullIsItsOwnKind(t *testing.T) {
	f, recoverable := Classify(fmt.Errorf("run migrations: %w", errors.New("write /data/unitill-pos.db: no space left on device")))
	if !recoverable {
		t.Fatal("Classify treated a disk-full failure as unrecoverable")
	}
	if f.Kind != KindDiskFull {
		t.Fatalf("Kind = %q, want %q (disk-full should be distinguished from a generic migration failure so the recovery page's message is actually actionable)", f.Kind, KindDiskFull)
	}
}

func TestClassify_RestoreApplyFailureIsRecoverable(t *testing.T) {
	// "staged restore failing to apply" is one of the realistic root causes
	// named on ut-docs#1415/#1436 itself.
	f, recoverable := Classify(fmt.Errorf("apply pending restore: %w", errors.New("staged backup is corrupt")))
	if !recoverable {
		t.Fatal("Classify treated a failed restore-apply as unrecoverable")
	}
	if f.Kind != KindDBOpen {
		t.Fatalf("Kind = %q, want %q", f.Kind, KindDBOpen)
	}
}

func TestClassify_NilErrorIsNotRecoverable(t *testing.T) {
	if _, recoverable := Classify(nil); recoverable {
		t.Fatal("Classify treated a nil error as recoverable")
	}
}

func TestClassify_TwoCallsProduceDifferentRefCodes(t *testing.T) {
	// Not a strict uniqueness guarantee (a support call just needs "the
	// code shown on screen right now" to be distinguishable in practice),
	// but two back-to-back classifications producing the identical code
	// would make the ref code useless for correlating a specific incident.
	f1, _ := Classify(fmt.Errorf("run migrations: %w", errors.New("boom")))
	f2, _ := Classify(fmt.Errorf("run migrations: %w", errors.New("boom")))
	if f1.RefCode == f2.RefCode {
		t.Fatalf("two Classify calls produced the same RefCode %q", f1.RefCode)
	}
}
