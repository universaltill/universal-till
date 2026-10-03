package cloudsync

import (
	"math"
	"strings"
)

// print_report (ut-docs#2537, ut-docs ADR-0095 Notes "print_report"): the
// owner prints a cloud report on one till. The cloud computed the report —
// it can span several tills and days this till's database does not hold —
// and sends it ready to print: every figure a pre-formatted, Latin-digit
// string. This till never recomputes or reformats; it lays the payload out
// as a print.Doc and prints it on its own receipt printer.
//
// Where the pieces live follows the existing directive architecture: this
// package owns the dispatch — the device check (printReportSkipReason,
// run by Tick before apply, like renameTillSkipReason), the payload decode
// (decodePrintReport) and the result post (apply's status/message, sent by
// Tick's postResult like every other type) — while the side effect is a
// Hooks func implemented in internal/pages (cloudPrintReport), which
// already owns the printer configuration (printerConfig), the store name
// and the receipt/EOD doc builders. That keeps internal/print and the
// settings reads out of this package, as for every other hook.
//
// Like rename_till it is device-targeted and any-till (not in
// mainTillOnlyTypes): the cloud serves it only to its target's own sync,
// and the till checks the target itself too (defence in depth). An older
// till that does not know the type fails it through apply's generic
// "unknown directive type" (the cloud's till_too_old).

// The payload caps, mirroring ut-cloud's claims.printReportMaxRows /
// printReportMaxKPIs (30 is internal/pages' eodArticlePrintCapDefault). The
// cloud already caps; the till re-applies them so a payload past them
// still prints a bounded slip.
const (
	printReportMaxRows = 30
	printReportMaxKPIs = 8
)

// PrintReport is a decoded print_report payload.
type PrintReport struct {
	DeviceID    string
	ReportKind  string
	PeriodLabel string
	KPIs        []PrintReportKPI
	Rows        []PrintReportRow
	// TruncatedCount is how many rows the cloud (or the till's own cap)
	// left out; the slip says "+N more".
	TruncatedCount int
}

// PrintReportKPI is one headline figure, printable as it stands.
type PrintReportKPI struct {
	Label string
	Value string
}

// PrintReportRow is one table row, printable as it stands.
type PrintReportRow struct {
	Label  string
	Values []string
}

// printReportSkipReason reports why this till must skip d, or "" to apply
// it: a print_report whose device_id is blank or another till's, or one
// that arrives before this till knows its own id. Any other type is not
// its business ("").
func printReportSkipReason(d directive) string {
	if d.Type != "print_report" {
		return ""
	}
	return ownDeviceSkipReason(d)
}

// decodePrintReport reads d's payload, refusing (a non-empty reason) any
// shape the cloud never sends: kpis/rows not lists of objects, a label or
// value that is not a string, a truncated_count that is not a whole
// non-negative number. Past the caps it truncates instead (see the caps).
func decodePrintReport(d directive) (PrintReport, string) {
	bad := func(what string) (PrintReport, string) { return PrintReport{}, "bad print_report payload: " + what }
	str := func(k string) string { v, _ := d.Payload[k].(string); return strings.TrimSpace(v) }
	r := PrintReport{DeviceID: str("device_id"), ReportKind: str("report_kind"), PeriodLabel: str("period_label")}

	objects := func(k string) ([]map[string]any, bool) {
		raw, present := d.Payload[k]
		if !present || raw == nil {
			return nil, true
		}
		list, ok := raw.([]any)
		if !ok {
			return nil, false
		}
		out := make([]map[string]any, 0, len(list))
		for _, v := range list {
			m, ok := v.(map[string]any)
			if !ok {
				return nil, false
			}
			out = append(out, m)
		}
		return out, true
	}

	kpis, ok := objects("kpis")
	if !ok {
		return bad("kpis")
	}
	for _, m := range kpis {
		label, lok := m["label"].(string)
		value, vok := m["value"].(string)
		if !lok || !vok {
			return bad("kpis")
		}
		if len(r.KPIs) < printReportMaxKPIs {
			r.KPIs = append(r.KPIs, PrintReportKPI{Label: label, Value: value})
		}
	}

	rows, ok := objects("rows")
	if !ok {
		return bad("rows")
	}
	extra := 0
	for _, m := range rows {
		label, lok := m["label"].(string)
		raw, vok := m["values"].([]any)
		if !lok || (!vok && m["values"] != nil) {
			return bad("rows")
		}
		values := make([]string, 0, len(raw))
		for _, v := range raw {
			s, ok := v.(string)
			if !ok {
				return bad("rows")
			}
			values = append(values, s)
		}
		if len(r.Rows) < printReportMaxRows {
			r.Rows = append(r.Rows, PrintReportRow{Label: label, Values: values})
		} else {
			extra++
		}
	}

	if raw, present := d.Payload["truncated_count"]; present && raw != nil {
		f, ok := raw.(float64)
		if !ok || f < 0 || f != math.Trunc(f) || f > math.MaxInt32 {
			return bad("truncated_count")
		}
		r.TruncatedCount = int(f)
	}
	r.TruncatedCount += extra
	return r, ""
}
