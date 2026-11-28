package plugins

import (
	"context"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// Review M2 (ut-docs#3697): the outbox sweep and the rejected-records
// Problem run on every till from housekeeping, not only inside a replica's
// push step: a promoted or unjoined till (no sync.primary_url) still
// expires, trims and reports its rows.
func TestRecordsHousekeeping_SweepsAndReportsOnAStandaloneTill_3697(t *testing.T) {
	d := hostfnTestDB(t) // no sync.primary_url: not a replica
	ctx := context.Background()
	now := time.Now().UTC()
	repo := data.NewPluginRecordsRepo(d)
	if _, err := repo.Enqueue(ctx, "p.hk", "entries", "old", []byte(`{}`), now.Add(-31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	key := RecordsProblemKey("p.hk")
	logging.ResolveProblems(key)
	RecordsHousekeeping(ctx, d, now)
	var st, reason string
	if err := d.QueryRow(`SELECT status, reason FROM plugin_record_outbox WHERE record_id = 'old'`).Scan(&st, &reason); err != nil {
		t.Fatal(err)
	}
	if st != "rejected" || reason != data.RecordReasonExpired {
		t.Fatalf("old row = %s/%s, want rejected/expired", st, reason)
	}
	if !logging.HasOpenProblem(key) {
		t.Fatal("a rejected row must raise the keyed Problem without a push step")
	}
	if _, err := d.Exec(`DELETE FROM plugin_record_outbox`); err != nil {
		t.Fatal(err)
	}
	RecordsHousekeeping(ctx, d, now)
	if logging.HasOpenProblem(key) {
		t.Fatal("the Problem must resolve once no rejected row remains")
	}
}

func TestRecordsHousekeeping_NilDBIsANoOp_3697(t *testing.T) {
	RecordsHousekeeping(context.Background(), nil, time.Now())
}
