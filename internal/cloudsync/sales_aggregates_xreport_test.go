package cloudsync

import (
	"bytes"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#5798 AC12: an x_report_printed audit row never reaches the cloud
// sales aggregate.
func TestBuildSalesAggregate_IgnoresXReportPrinted(t *testing.T) {
	f := seedAggFixture(t, "agg-xreport.db")
	ctx := t.Context()
	repo := data.NewPOSRepo(f.db)
	k := data.SalesAggregateKey{BusinessDate: f.today, TillID: "reg-A"}
	a, err := buildSalesAggregate(ctx, repo, "store-1", k, "reg-A", true)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := repo.InsertAudit(ctx, nil, "u1", "shift", "sh-x", "x_report_printed",
			map[string]string{"register_id": "reg-A"}, aggAt(f.todayT.Add(time.Minute)), ""); err != nil {
			t.Fatal(err)
		}
	}
	b, err := buildSalesAggregate(ctx, repo, "store-1", k, "reg-A", true)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("aggregate changed:\n%s\n%s", a, b)
	}
}
