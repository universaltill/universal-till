package catsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/catimport"
	"github.com/universaltill/universal-till/internal/data"
)

// priceEmbeddedCSV carries one price-embedded EAN-13 label: prefix 02, item
// code 12345, price 250 minor units. Its lookup key, 0212345000000, has a
// zeroed (so invalid) check digit.
const priceEmbeddedCSV = "Handle,SKU,Name,Category,Sold by weight,Price,Barcode,In stock\n" +
	"cheese,20001,Cheese,Deli,N,2.50,0212345002506,1\n"

var priceEmbeddedIDs = []string{"EAN13", "EAN13_PRICE_PREFIX02"}

func enableSymbologies(t *testing.T, sqlDB *sql.DB, ids []string) {
	t.Helper()
	if err := data.NewSettingsRepo(sqlDB).SetEnabledBarcodeSymbologies(context.Background(), ids); err != nil {
		t.Fatal(err)
	}
}

func storedBarcodeType(t *testing.T, sqlDB *sql.DB, code string) string {
	t.Helper()
	var typ string
	if err := sqlDB.QueryRow(`SELECT barcode_type FROM item_barcodes WHERE barcode = ?`, code).Scan(&typ); err != nil {
		t.Fatalf("barcode %s: %v", code, err)
	}
	return typ
}

// moduleRows parses csv with the shared module and marshals its ToRows
// output to the wire, as ut-cloud or a plugin would send it.
func moduleRows(t *testing.T, csv string, enabled []string) []string {
	t.Helper()
	res, err := catimport.Parse(strings.NewReader(csv), 2, enabled, false)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range catimport.ToRows(res) {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(b))
	}
	return out
}

// A price-embedded label decoded by the module reaches the catalogue as its
// zeroed key typed EAN13_PRICE_PREFIX02 — decoded once. Re-matching the key
// (its check digit is zero) finds no symbology under this shop's set, so
// before barcode_type the row was refused (ut-docs#936 double decode).
func TestRun_TypedPriceEmbeddedBarcodeIsNotDecodedTwice(t *testing.T) {
	sqlDB := openDB(t)
	enableSymbologies(t, sqlDB, priceEmbeddedIDs)
	wantCounts(t, run(t, sqlDB, true, moduleRows(t, priceEmbeddedCSV, priceEmbeddedIDs)...), 1, 0, 0, 0)
	if got := storedBarcodeType(t, sqlDB, "0212345000000"); got != "EAN13_PRICE_PREFIX02" {
		t.Fatalf("barcode_type = %q, want EAN13_PRICE_PREFIX02", got)
	}
	// The re-import is unchanged: same key, same type.
	wantCounts(t, run(t, sqlDB, true, moduleRows(t, priceEmbeddedCSV, priceEmbeddedIDs)...), 0, 0, 1, 0)
}

// A typed barcode is the shop's to accept: a symbology this till has not
// enabled is refused like an untyped code that matches nothing.
func TestRun_TypedBarcodeNeedsTheSymbologyEnabled(t *testing.T) {
	sqlDB := openDB(t)
	enableSymbologies(t, sqlDB, []string{"EAN13"})
	res := run(t, sqlDB, false, moduleRows(t, priceEmbeddedCSV, priceEmbeddedIDs)...)
	wantCounts(t, res, 0, 0, 0, 1)
	if res.Samples[0].IssueCode != IssueBarcodeNotAccepted {
		t.Fatalf("issue = %+v, want %s", res.Samples[0], IssueBarcodeNotAccepted)
	}
}

// barcode_type is checked against the barcode: an unknown type, a raw
// label sent as an embedded-data key, or a type with no barcode is an
// invalid barcode, never stored as claimed.
func TestRun_TypedBarcodeMustBeThatTypesKey(t *testing.T) {
	sqlDB := openDB(t)
	enableSymbologies(t, sqlDB, priceEmbeddedIDs)
	res := run(t, sqlDB, false,
		`{"external_id":"a","name":"A","price_minor":1,"barcode":"0212345002506","barcode_type":"EAN13_PRICE_PREFIX02"}`,
		`{"external_id":"b","name":"B","price_minor":1,"barcode":"5449000000996","barcode_type":"NOPE"}`,
		`{"external_id":"c","name":"C","price_minor":1,"barcode_type":"EAN13"}`,
		`{"external_id":"d","name":"D","price_minor":1,"barcode":"5449000000997","barcode_type":"EAN13"}`,
	)
	wantCounts(t, res, 0, 0, 0, 4)
	for _, s := range res.Samples {
		if s.IssueCode != IssueBarcodeInvalid {
			t.Errorf("row %s issue = %q, want %s", s.ExternalID, s.IssueCode, IssueBarcodeInvalid)
		}
	}
}
