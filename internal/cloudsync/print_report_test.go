package cloudsync

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

// ut-docs#2537: print_report carries a report the cloud already computed
// ({device_id, report_kind, period_label, kpis, rows, truncated_count});
// the till only lays it out and prints it. Like rename_till it names one
// device: Tick skips it (no apply, no result post) unless it is this
// till's own, on the main till and a satellite alike.

func printPayload(device any) map[string]any {
	p := map[string]any{
		"report_kind":  "overview",
		"period_label": "2026-09-01",
		"kpis":         []any{map[string]any{"label": "Net sales", "value": "£130.00"}},
		"rows": []any{
			map[string]any{"label": "09:00", "values": []any{"4", "£128.40"}},
			map[string]any{"label": "14:00", "values": []any{"1", "£1.60"}},
		},
		"truncated_count": 0.0,
	}
	if device != nil {
		p["device_id"] = device
	}
	return p
}

func TestPrintReportSkipReason(t *testing.T) {
	orig := ownDeviceID
	t.Cleanup(func() { ownDeviceID = orig })
	ownDeviceID = func() string { return "dev-self" }
	d := func(dev any) directive { return directive{ID: "p1", Type: "print_report", Payload: printPayload(dev)} }

	for name, tc := range map[string]struct {
		d    directive
		want string
	}{
		"own device":         {d("dev-self"), ""},
		"own device, padded": {d(" dev-self "), ""},
		"other device":       {d("dev-other"), "addressed to another till"},
		"blank device":       {d(""), "addressed to another till"},
		"absent device":      {d(nil), "addressed to another till"},
		"non-string device":  {d(7.0), "addressed to another till"},
		"untargeted type":    {directive{Type: "set_setting", Payload: map[string]any{"device_id": "dev-other"}}, ""},
	} {
		if got := deviceTargetSkipReason(tc.d); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
	ownDeviceID = func() string { return "" }
	if got := deviceTargetSkipReason(d("dev-self")); got != "this till's own device id is not known yet" {
		t.Fatalf("own id unknown: %q", got)
	}
}

func TestPrintReportIsNotMainTillOnly(t *testing.T) {
	if mainTillOnlyTypes["print_report"] {
		t.Fatal("print_report must not be main-till only: a satellite's own printer is a valid target")
	}
}

func TestApplyPrintReport(t *testing.T) {
	ctx := context.Background()
	d := func(p map[string]any) directive { return directive{ID: "p1", Type: "print_report", Payload: p} }

	if status, msg := apply(ctx, d(printPayload("dev-1")), Hooks{}); status != "failed" || msg != "print_report is not supported on this till" {
		t.Fatalf("nil hook: %q %q", status, msg)
	}

	var got []PrintReport
	hooks := Hooks{PrintReport: func(_ context.Context, r PrintReport) (string, error) {
		got = append(got, r)
		return "printed", nil
	}}
	status, msg := apply(ctx, d(printPayload("dev-1")), hooks)
	if status != "applied" || msg != "printed" || len(got) != 1 {
		t.Fatalf("apply = %q %q, hook got %v", status, msg, got)
	}
	r := got[0]
	if r.ReportKind != "overview" || r.PeriodLabel != "2026-09-01" || r.TruncatedCount != 0 ||
		len(r.KPIs) != 1 || r.KPIs[0] != (PrintReportKPI{Label: "Net sales", Value: "£130.00"}) ||
		len(r.Rows) != 2 || r.Rows[0].Label != "09:00" || strings.Join(r.Rows[0].Values, "|") != "4|£128.40" {
		t.Fatalf("decoded = %+v", r)
	}

	// The print failing is the directive failing, with the reason — never
	// a silent "applied".
	failing := Hooks{PrintReport: func(context.Context, PrintReport) (string, error) {
		return "", errors.New("no receipt printer is configured on this till")
	}}
	if status, msg := apply(ctx, d(printPayload("dev-1")), failing); status != "failed" || msg != "no receipt printer is configured on this till" {
		t.Fatalf("print failure: %q %q", status, msg)
	}
}

func TestApplyPrintReportRefusesBadPayload(t *testing.T) {
	ctx := context.Background()
	ran := 0
	hooks := Hooks{PrintReport: func(context.Context, PrintReport) (string, error) { ran++; return "printed", nil }}
	mut := func(f func(p map[string]any)) map[string]any {
		p := printPayload("dev-1")
		f(p)
		return p
	}
	for name, p := range map[string]map[string]any{
		"kpis not a list":      mut(func(p map[string]any) { p["kpis"] = "x" }),
		"kpi value a number":   mut(func(p map[string]any) { p["kpis"] = []any{map[string]any{"label": "Net", "value": 1.0}} }),
		"row not an object":    mut(func(p map[string]any) { p["rows"] = []any{"09:00"} }),
		"row values numbers":   mut(func(p map[string]any) { p["rows"] = []any{map[string]any{"label": "a", "values": []any{1.0}}} }),
		"negative truncated":   mut(func(p map[string]any) { p["truncated_count"] = -1.0 }),
		"fractional truncated": mut(func(p map[string]any) { p["truncated_count"] = 1.5 }),
	} {
		status, msg := apply(ctx, directive{ID: "p1", Type: "print_report", Payload: p}, hooks)
		if status != "failed" || !strings.HasPrefix(msg, "bad print_report payload") {
			t.Errorf("%s: %q %q, want failed/bad payload", name, status, msg)
		}
	}
	if ran != 0 {
		t.Fatalf("hook ran %d times for invalid payloads", ran)
	}
}

// Defence in depth on the caps: a payload past them (an older or buggy
// cloud) still prints a bounded slip — the excess rows are counted into
// truncated_count, the excess KPIs dropped.
func TestDecodePrintReportCapsDefensively(t *testing.T) {
	p := printPayload("dev-1")
	var rows, kpis []any
	for i := 0; i < 40; i++ {
		rows = append(rows, map[string]any{"label": "r", "values": []any{"1"}})
	}
	for i := 0; i < 11; i++ {
		kpis = append(kpis, map[string]any{"label": "k", "value": "1"})
	}
	p["rows"], p["kpis"], p["truncated_count"] = rows, kpis, 3.0
	r, bad := decodePrintReport(directive{Type: "print_report", Payload: p})
	if bad != "" {
		t.Fatal(bad)
	}
	if len(r.Rows) != printReportMaxRows || r.TruncatedCount != 13 || len(r.KPIs) != printReportMaxKPIs {
		t.Fatalf("rows %d truncated %d kpis %d", len(r.Rows), r.TruncatedCount, len(r.KPIs))
	}
}

func TestTickPrintReportOnlyForOwnDevice(t *testing.T) {
	orig := ownDeviceID
	ownDeviceID = func() string { return "dev-self" }
	t.Cleanup(func() { ownDeviceID = orig })
	resetTargetSkipLog()
	t.Cleanup(resetTargetSkipLog)

	for _, role := range []struct {
		name    string
		primary string
	}{{"main till", ""}, {"satellite", "http://10.0.0.2:8080"}} {
		for _, tc := range []struct {
			name      string
			deviceID  any
			printErr  error
			wantRuns  int
			wantPosts int
			wantState string
		}{
			{"own device", "dev-self", nil, 1, 1, "applied"},
			{"own device, print fails", "dev-self", errors.New("printer offline"), 1, 1, "failed"},
			{"other device", "dev-other", nil, 0, 0, ""},
			{"absent device", nil, nil, 0, 0, ""},
		} {
			t.Run(role.name+"/"+tc.name, func(t *testing.T) {
				cloud := &fakeCloud{directives: []map[string]any{
					{"id": "p1", "type": "print_report", "payload": printPayload(tc.deviceID)},
				}}
				srv := httptest.NewServer(cloud.handler())
				defer srv.Close()
				db := testDB(t)
				if role.primary != "" {
					if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('sync.primary_url', ?)`, role.primary); err != nil {
						t.Fatal(err)
					}
				}
				ran := 0
				hooks := Hooks{PrintReport: func(context.Context, PrintReport) (string, error) {
					ran++
					if tc.printErr != nil {
						return "", tc.printErr
					}
					return "printed", nil
				}}
				if err := Tick(context.Background(), testCfg(srv.URL), db, hooks); err != nil {
					t.Fatalf("tick: %v", err)
				}
				if ran != tc.wantRuns || len(cloud.results) != tc.wantPosts {
					t.Fatalf("hook runs = %d, result posts = %+v; want %d runs, %d posts", ran, cloud.results, tc.wantRuns, tc.wantPosts)
				}
				if tc.wantPosts == 1 && cloud.results[0]["status"] != tc.wantState {
					t.Fatalf("result = %+v, want %s", cloud.results[0], tc.wantState)
				}
			})
		}
	}
}
