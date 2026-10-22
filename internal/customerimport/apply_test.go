package customerimport_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/customerimport"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ADR-0168 §5 / ut-docs#4748. Every customer below is invented.

const src = "file:test-shop"

func open(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(testsupport.MigratedDBFile(t, "customerimport.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.DB.Exec(`INSERT OR REPLACE INTO settings(key,value) VALUES('store.country','GB')`); err != nil {
		t.Fatalf("seed country: %v", err)
	}
	return d.DB
}

func exec(t *testing.T, d *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func apply(t *testing.T, d *sql.DB, rows ...customerimport.Row) customerimport.Result {
	t.Helper()
	res, err := customerimport.Apply(context.Background(), d, customerimport.Request{Source: src, Rows: rows}, "system")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return res
}

func outcomes(res customerimport.Result) []string {
	var o []string
	for _, r := range res.Rows {
		o = append(o, r.Outcome)
	}
	return o
}

func want(t *testing.T, res customerimport.Result, outs ...string) {
	t.Helper()
	got := outcomes(res)
	if strings.Join(got, ",") != strings.Join(outs, ",") {
		t.Fatalf("outcomes = %v, want %v", got, outs)
	}
}

func count(t *testing.T, d *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := d.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func field(t *testing.T, d *sql.DB, id, col string) string {
	t.Helper()
	var v sql.NullString
	if err := d.QueryRow(`SELECT `+col+` FROM customers WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v.String
}

func TestApply_CreatesNewCustomers(t *testing.T) {
	d := open(t)
	res := apply(t, d,
		customerimport.Row{Row: 2, Name: "Ada Quill", Email: "Ada.Quill@Example.test", Phone: "07700 900123", Address: "1 Test Lane", Postcode: "ZZ1 1ZZ"},
		customerimport.Row{Row: 3, Name: "Bo Marsh"},
	)
	want(t, res, customerimport.Created, customerimport.Created)
	if n := count(t, d, `SELECT COUNT(*) FROM customers`); n != 2 {
		t.Fatalf("customers = %d, want 2", n)
	}
	var id string
	if err := d.QueryRow(`SELECT id FROM customers WHERE name = 'Ada Quill'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if got := field(t, d, id, "postcode"); got != "ZZ1 1ZZ" {
		t.Fatalf("postcode = %q", got)
	}
	if got := field(t, d, id, "phone_e164"); got != "+447700900123" {
		t.Fatalf("phone_e164 = %q, want +447700900123", got)
	}
	if res.Counts[customerimport.Created] != 2 {
		t.Fatalf("counts = %v", res.Counts)
	}
}

func TestApply_MatchOrder(t *testing.T) {
	d := open(t)
	exec(t, d, `INSERT INTO customers(id,name,email,phone,phone_e164,postcode) VALUES
		('c-email','Cy Alder','cy@example.test',NULL,'',NULL),
		('c-phone','Di Fenn',NULL,'07700 900200','+447700900200',NULL),
		('c-np','Ed Gale',NULL,NULL,'','ZZ2 2ZZ')`)
	res := apply(t, d,
		// email, different case; the phone in the file is new for this person
		customerimport.Row{Row: 1, Name: "Cy Alder", Email: "CY@example.test", Phone: "07700 900201"},
		// phone written another way
		customerimport.Row{Row: 2, Name: "Di Fenn", Phone: "+44 7700 900200"},
		// name + postcode, folded
		customerimport.Row{Row: 3, Name: "ED GALE", Postcode: "zz2  2zz"},
		// nothing matches
		customerimport.Row{Row: 4, Name: "Fay Holt", Email: "fay@example.test"},
	)
	want(t, res, customerimport.Updated, customerimport.Unchanged, customerimport.Unchanged, customerimport.Created)
	if n := count(t, d, `SELECT COUNT(*) FROM customers`); n != 4 {
		t.Fatalf("customers = %d, want 3 existing + 1 new", n)
	}
	if got := field(t, d, "c-email", "phone"); got != "07700 900201" {
		t.Fatalf("the empty phone must be filled, got %q", got)
	}
}

func TestApply_NamePostcodeNeedsExactlyOne(t *testing.T) {
	d := open(t)
	exec(t, d, `INSERT INTO customers(id,name,postcode) VALUES ('a','Gil Ivy','ZZ3 3ZZ'),('b','Gil Ivy','ZZ3 3ZZ')`)
	res := apply(t, d, customerimport.Row{Row: 1, Name: "Gil Ivy", Postcode: "ZZ3 3ZZ"})
	want(t, res, customerimport.Created)
	if n := count(t, d, `SELECT COUNT(*) FROM customers`); n != 3 {
		t.Fatalf("customers = %d, want 3", n)
	}
}

func TestApply_AmbiguousAndConflictingNeverPick(t *testing.T) {
	d := open(t)
	exec(t, d, `INSERT INTO customers(id,name,email,phone,phone_e164) VALUES
		('a1','Hal Jay','shared@example.test',NULL,''),
		('a2','Hal Jay','shared@example.test',NULL,''),
		('p1','Ivy Kent',NULL,'07700 900300','+447700900300'),
		('p2','Ivy Kent',NULL,'07700 900300','+447700900300'),
		('e1','Jo Lane','jo@example.test',NULL,''),
		('f1','Kai Moss',NULL,'07700 900400','+447700900400')`)
	before := count(t, d, `SELECT COUNT(*) FROM customers`)
	res := apply(t, d,
		customerimport.Row{Row: 1, Name: "Hal Jay", Email: "shared@example.test", Phone: "07700 900999"},
		customerimport.Row{Row: 2, Name: "Ivy Kent", Phone: "07700 900300", Address: "9 Elm Row"},
		customerimport.Row{Row: 3, Name: "Jo Lane", Email: "jo@example.test", Phone: "07700 900400"},
	)
	want(t, res, customerimport.AmbiguousMatch, customerimport.AmbiguousMatch, customerimport.ConflictingMatch)
	if n := count(t, d, `SELECT COUNT(*) FROM customers`); n != before {
		t.Fatalf("an issue row created or removed customers: %d -> %d", before, n)
	}
	for _, id := range []string{"a1", "a2", "p1", "p2", "e1", "f1"} {
		if field(t, d, id, "address") != "" || field(t, d, id, "phone") == "07700 900999" {
			t.Fatalf("customer %s was written by an unresolved row", id)
		}
	}
	if got := field(t, d, "e1", "phone"); got != "" {
		t.Fatalf("e1 phone = %q: conflicting rows must not fill", got)
	}
}

func TestApply_DifferingFieldIsKeptAndOnlyNamedByFieldName(t *testing.T) {
	d := open(t)
	exec(t, d, `INSERT INTO customers(id,name,email,phone,phone_e164,address) VALUES
		('c1','Lou Nash','lou@example.test','07700 900500','+447700900500','5 Real Street')`)
	res := apply(t, d, customerimport.Row{Row: 7, Name: "Lou Nash", Email: "lou@example.test",
		Phone: "07700 900501", Address: "99 File Road", Postcode: "ZZ4 4ZZ"})
	want(t, res, customerimport.Updated)
	r := res.Rows[0]
	if r.Row != 7 || strings.Join(r.Fields, ",") != "phone,address" {
		t.Fatalf("row = %+v, want kept fields phone,address on row 7", r)
	}
	if field(t, d, "c1", "phone") != "07700 900500" || field(t, d, "c1", "address") != "5 Real Street" {
		t.Fatal("a differing field must keep the till's value")
	}
	if field(t, d, "c1", "postcode") != "ZZ4 4ZZ" {
		t.Fatal("an empty field must be filled")
	}
	b, _ := json.Marshal(res)
	for _, secret := range []string{"07700 900500", "07700 900501", "5 Real Street", "99 File Road", "lou@example.test", "Lou Nash"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("the result carries a customer value %q: %s", secret, b)
		}
	}
}

func TestApply_ForceFieldsOverwriteExactlyThose(t *testing.T) {
	d := open(t)
	exec(t, d, `INSERT INTO customers(id,name,email,phone,phone_e164,address) VALUES
		('c1','Mia Oak','mia@example.test','07700 900600','+447700900600','5 Real Street')`)
	file := customerimport.Row{Row: 4, Name: "Mia Oak", Email: "mia@example.test", Phone: "07700 900601", Address: "99 File Road"}
	apply(t, d, file) // everything differs: kept
	file.ForceFields = []string{"phone"}
	res := apply(t, d, file)
	want(t, res, customerimport.Updated)
	if field(t, d, "c1", "phone") != "07700 900601" || field(t, d, "c1", "phone_e164") != "+447700900601" {
		t.Fatalf("phone not overwritten: %q / %q", field(t, d, "c1", "phone"), field(t, d, "c1", "phone_e164"))
	}
	if field(t, d, "c1", "address") != "5 Real Street" {
		t.Fatal("a field not named in force_fields must keep the till's value")
	}
	if len(res.Rows[0].Fields) != 1 || res.Rows[0].Fields[0] != "address" {
		t.Fatalf("still-kept fields = %v, want [address]", res.Rows[0].Fields)
	}
	res = apply(t, d, customerimport.Row{Row: 5, Name: "Mia Oak", Email: "mia@example.test", ForceFields: []string{"nickname"}})
	want(t, res, customerimport.Invalid)
}

func TestApply_ReapplyChangesNothing(t *testing.T) {
	d := open(t)
	rows := []customerimport.Row{
		{Row: 1, ExternalID: "ext-1", Name: "Nell Pike", Email: "nell@example.test", Phone: "07700 900700"},
		{Row: 2, Name: "Otto Rowe", Postcode: "ZZ5 5ZZ"},
		{Row: 3, Name: "Pia Stone"},
	}
	apply(t, d, rows...)
	dump := func() string {
		var b strings.Builder
		for _, q := range []string{
			`SELECT id||name||COALESCE(email,'')||COALESCE(phone,'')||COALESCE(postcode,'') FROM customers ORDER BY id`,
			`SELECT source||entity||external_id||local_id||COALESCE(baseline_hash,'') FROM import_source_links ORDER BY external_id`,
		} {
			rs, err := d.Query(q)
			if err != nil {
				t.Fatal(err)
			}
			for rs.Next() {
				var s string
				_ = rs.Scan(&s)
				b.WriteString(s + "\n")
			}
			rs.Close()
		}
		return b.String()
	}
	audits := count(t, d, `SELECT COUNT(*) FROM audit_log`)
	before := dump()
	res := apply(t, d, rows...)
	want(t, res, customerimport.Unchanged, customerimport.Unchanged, customerimport.Unchanged)
	if dump() != before {
		t.Fatalf("re-apply changed data:\n%s\n---\n%s", before, dump())
	}
	if n := count(t, d, `SELECT COUNT(*) FROM audit_log`); n != audits {
		t.Fatalf("re-apply wrote %d audit rows", n-audits)
	}
	// A changed file value later still fills an empty field.
	rows[1].Address = "2 Test Mews"
	res = apply(t, d, rows...)
	if res.Rows[1].Outcome != customerimport.Updated {
		t.Fatalf("changed row outcome = %q", res.Rows[1].Outcome)
	}
}

func TestApply_LinkSurvivesAChangedIdentifier(t *testing.T) {
	d := open(t)
	apply(t, d, customerimport.Row{Row: 1, ExternalID: "ext-9", Name: "Quin Reed", Email: "quin@example.test"})
	res := apply(t, d, customerimport.Row{Row: 1, ExternalID: "ext-9", Name: "Quin Reed", Email: "quin.new@example.test", Phone: "07700 900800"})
	if res.Rows[0].Outcome != customerimport.Updated || strings.Join(res.Rows[0].Fields, ",") != "email" {
		t.Fatalf("row = %+v, want updated with email kept", res.Rows[0])
	}
	if n := count(t, d, `SELECT COUNT(*) FROM customers`); n != 1 {
		t.Fatalf("customers = %d, want the link to find the same customer", n)
	}
}

func TestApply_ErasedCustomersAreNeverRecreated(t *testing.T) {
	d := open(t)
	apply(t, d,
		customerimport.Row{Row: 1, ExternalID: "ext-1", Name: "Rae Frost", Email: "rae@example.test", Phone: "07700 900900"},
		customerimport.Row{Row: 2, Name: "Sol Vale", Email: "sol@example.test"},
	)
	var raeID, solID string
	_ = d.QueryRow(`SELECT id FROM customers WHERE name='Rae Frost'`).Scan(&raeID)
	_ = d.QueryRow(`SELECT id FROM customers WHERE name='Sol Vale'`).Scan(&solID)
	pos := data.NewPOSRepo(d)
	for _, id := range []string{raeID, solID} {
		if ok, err := pos.EraseCustomer(context.Background(), id, "", ""); err != nil || !ok {
			t.Fatalf("erase %s: %v %v", id, ok, err)
		}
	}
	if n := count(t, d, `SELECT COUNT(*) FROM customers`); n != 0 {
		t.Fatalf("customers after erase = %d", n)
	}
	res := apply(t, d,
		// same link
		customerimport.Row{Row: 1, ExternalID: "ext-1", Name: "Rae Frost", Email: "rae@example.test"},
		// no link any more, same email, other spelling
		customerimport.Row{Row: 2, ExternalID: "ext-other", Name: "S. Vale", Email: "SOL@example.test"},
		// same phone as Rae, a new id
		customerimport.Row{Row: 3, ExternalID: "ext-3", Name: "Rae F", Phone: "+44 7700 900900"},
		// a stranger
		customerimport.Row{Row: 4, Name: "Tess Wynn", Email: "tess@example.test"},
	)
	want(t, res, customerimport.ErasedOnTill, customerimport.ErasedOnTill, customerimport.ErasedOnTill, customerimport.Created)
	if res.Counts[customerimport.ErasedOnTill] != 3 {
		t.Fatalf("counts = %v", res.Counts)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM customers`); n != 1 {
		t.Fatalf("customers = %d, only the stranger may exist", n)
	}
	// The suppression rows are hashes, not the address or number.
	rs, _ := d.Query(`SELECT hash FROM customer_erasure_hashes`)
	defer rs.Close()
	for rs.Next() {
		var h string
		_ = rs.Scan(&h)
		if strings.Contains(h, "example") || strings.Contains(h, "7700") {
			t.Fatalf("a plain value in the suppression list: %q", h)
		}
	}
}

func TestApply_NoCustomerDataInLogsOrAudit(t *testing.T) {
	d := open(t)
	var buf bytes.Buffer
	var mu sync.Mutex
	restore := logging.CaptureForTest(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}))
	defer restore()
	exec(t, d, `INSERT INTO customers(id,name,email,phone,phone_e164,address) VALUES ('c1','Uma Birch','uma@example.test','07700 901000','+447700901000','4 Real Close')`)
	rows := []customerimport.Row{
		{Row: 1, ExternalID: "ext-1", Name: "Uma Birch", Email: "uma@example.test", Phone: "07700 901001", Address: "77 File Lane", Postcode: "ZZ6 6ZZ"},
		{Row: 2, Name: "Vic Hale", Email: "vic@example.test", Phone: "07700 901002", Address: "8 Test Way"},
		{Row: 3, Name: "", Email: "bad"},
	}
	apply(t, d, rows...)
	apply(t, d, rows...)
	secrets := []string{"Uma Birch", "uma@example.test", "07700 901000", "07700 901001", "4 Real Close", "77 File Lane", "ZZ6 6ZZ", "Vic Hale", "vic@example.test", "07700 901002", "8 Test Way"}
	mu.Lock()
	logs := buf.String()
	mu.Unlock()
	for _, s := range secrets {
		if strings.Contains(logs, s) {
			t.Fatalf("log carries customer data %q:\n%s", s, logs)
		}
	}
	rs, err := d.Query(`SELECT COALESCE(data_json,'')||entity_id||action FROM audit_log WHERE entity_type IN ('customer','customer_import')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	n := 0
	for rs.Next() {
		var s string
		_ = rs.Scan(&s)
		n++
		for _, secret := range secrets {
			if strings.Contains(s, secret) {
				t.Fatalf("audit row carries customer data %q: %s", secret, s)
			}
		}
	}
	if n == 0 {
		t.Fatal("the import wrote no audit rows")
	}
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestApply_InvalidRowsAndBounds(t *testing.T) {
	d := open(t)
	res := apply(t, d,
		customerimport.Row{Row: 1, Name: ""},
		customerimport.Row{Row: 2, Name: strings.Repeat("x", 300)},
		customerimport.Row{Row: 3, Name: "Wes Dale", Email: "not-an-email"},
		customerimport.Row{Row: 4, Name: "Xia Pond", Postcode: strings.Repeat("9", 40)},
	)
	want(t, res, customerimport.Invalid, customerimport.Invalid, customerimport.Invalid, customerimport.Invalid)
	if strings.Join(res.Rows[0].Fields, ",") != "name" || strings.Join(res.Rows[2].Fields, ",") != "email" {
		t.Fatalf("invalid rows must name the field: %+v", res.Rows)
	}
	if n := count(t, d, `SELECT COUNT(*) FROM customers`); n != 0 {
		t.Fatalf("customers = %d", n)
	}
	many := make([]customerimport.Row, customerimport.MaxRows+1)
	for i := range many {
		many[i] = customerimport.Row{Row: i + 1, Name: "Yan Reed"}
	}
	if _, err := customerimport.Apply(context.Background(), d, customerimport.Request{Source: src, Rows: many}, "system"); err == nil {
		t.Fatal("more than MaxRows rows must be refused")
	}
	if _, err := customerimport.Apply(context.Background(), d, customerimport.Request{Rows: many[:1]}, "system"); err == nil {
		t.Fatal("a request without a source must be refused")
	}
}

// Review findings (independent review, ut-docs#4748).

func eraseByName(t *testing.T, d *sql.DB, name string) {
	t.Helper()
	var id string
	if err := d.QueryRow(`SELECT id FROM customers WHERE name = ?`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if ok, err := data.NewPOSRepo(d).EraseCustomer(context.Background(), id, "", ""); err != nil || !ok {
		t.Fatalf("erase %s: %v %v", name, ok, err)
	}
}

// An erased person's email or phone is never written onto someone else.
func TestApply_ErasedIdentifiersAreNeverFilledOntoOthers(t *testing.T) {
	d := open(t)
	apply(t, d, customerimport.Row{Row: 1, Name: "Zed Pike", Email: "zed@example.test", Phone: "07700 902000", Postcode: "ZZ7 7ZZ"})
	eraseByName(t, d, "Zed Pike")
	// Another customer with the same name and postcode, no contact details.
	exec(t, d, `INSERT INTO customers(id,name,postcode) VALUES ('q','Zed Pike','ZZ7 7ZZ')`)
	// The row is the erased person: name + postcode would match q, but its
	// email names the erased one.
	res := apply(t, d, customerimport.Row{Row: 1, Name: "Zed Pike", Postcode: "ZZ7 7ZZ", Email: "ZED@example.test"})
	want(t, res, customerimport.ErasedOnTill)
	if field(t, d, "q", "email") != "" {
		t.Fatal("the erased email was written onto another customer")
	}
	// A live customer matched by email must not receive an erased phone.
	exec(t, d, `INSERT INTO customers(id,name,email) VALUES ('a','Amy Cole','amy@example.test')`)
	res = apply(t, d, customerimport.Row{Row: 2, Name: "Amy Cole", Email: "amy@example.test", Phone: "07700 902000"})
	want(t, res, customerimport.Unchanged)
	if field(t, d, "a", "phone") != "" || strings.Join(res.Rows[0].Fields, ",") != "phone" {
		t.Fatalf("phone = %q fields = %v: an erased number must stay out and be named", field(t, d, "a", "phone"), res.Rows[0].Fields)
	}
	// Forcing it does not help.
	res = apply(t, d, customerimport.Row{Row: 2, Name: "Amy Cole", Email: "amy@example.test", Phone: "07700 902000", ForceFields: []string{"phone"}})
	if field(t, d, "a", "phone") != "" {
		t.Fatal("force_fields wrote an erased number")
	}
}

// Rows with no id, no email and no phone are told apart by what they say; a
// name alone never merges two people.
func TestApply_IdlessRowsAreNotMergedByName(t *testing.T) {
	d := open(t)
	res := apply(t, d,
		customerimport.Row{Row: 1, Name: "Jo Smith", Address: "1 Test Row"},
		customerimport.Row{Row: 2, Name: "Jo Smith", Address: "2 Test Row"},
		customerimport.Row{Row: 3, Name: "Jo Smith"},
	)
	want(t, res, customerimport.Created, customerimport.Created, customerimport.Created)
	// The same file again changes nothing and creates nobody.
	res = apply(t, d,
		customerimport.Row{Row: 1, Name: "Jo Smith", Address: "1 Test Row"},
		customerimport.Row{Row: 2, Name: "Jo Smith", Address: "2 Test Row"},
		customerimport.Row{Row: 3, Name: "Jo Smith"},
	)
	want(t, res, customerimport.Unchanged, customerimport.Unchanged, customerimport.Unchanged)
	if n := count(t, d, `SELECT COUNT(*) FROM customers`); n != 3 {
		t.Fatalf("customers = %d, want 3", n)
	}
}

// An id-less row keyed by email still goes through the conflict check.
func TestApply_IdlessEmailRowStillChecksThePhone(t *testing.T) {
	d := open(t)
	apply(t, d, customerimport.Row{Row: 1, Name: "Kit Bell", Email: "kit@example.test"})
	exec(t, d, `INSERT INTO customers(id,name,phone,phone_e164) VALUES ('other','Lee Moss','07700 903000','+447700903000')`)
	res := apply(t, d, customerimport.Row{Row: 1, Name: "Kit Bell", Email: "kit@example.test", Phone: "07700 903000"})
	want(t, res, customerimport.ConflictingMatch)
}

// Re-delivering a chunk that holds issue rows leaves no new audit rows.
func TestApply_RedeliveryWritesNoAuditRows(t *testing.T) {
	d := open(t)
	exec(t, d, `INSERT INTO customers(id,name,email) VALUES ('a1','Mo Dunn','dup@example.test'),('a2','Mo Dunn','dup@example.test')`)
	rows := []customerimport.Row{{Row: 1, Name: "Mo Dunn", Email: "dup@example.test"}, {Row: 2, Name: ""}}
	apply(t, d, rows...)
	n := count(t, d, `SELECT COUNT(*) FROM audit_log`)
	apply(t, d, rows...)
	if got := count(t, d, `SELECT COUNT(*) FROM audit_log`); got != n {
		t.Fatalf("re-delivery wrote %d audit rows", got-n)
	}
}

// A forced field can correct case or spacing the fold treats as equal.
func TestApply_ForceCanCorrectCase(t *testing.T) {
	d := open(t)
	exec(t, d, `INSERT INTO customers(id,name,email) VALUES ('n','nan ross','nan@example.test')`)
	res := apply(t, d, customerimport.Row{Row: 1, Name: "Nan Ross", Email: "nan@example.test", ForceFields: []string{"name"}})
	want(t, res, customerimport.Updated)
	if field(t, d, "n", "name") != "Nan Ross" {
		t.Fatalf("name = %q", field(t, d, "n", "name"))
	}
}

// A number erased before the shop's country was set is still recognised
// after it is set.
func TestApply_ErasedPhoneSurvivesACountryChange(t *testing.T) {
	d := open(t)
	exec(t, d, `DELETE FROM settings WHERE key = 'store.country'`)
	exec(t, d, `INSERT INTO customers(id,name,phone) VALUES ('p','Ola Reed','07700 904000')`)
	if ok, err := data.NewPOSRepo(d).EraseCustomer(context.Background(), "p", "", ""); err != nil || !ok {
		t.Fatal(err)
	}
	exec(t, d, `INSERT OR REPLACE INTO settings(key,value) VALUES('store.country','GB')`)
	res := apply(t, d, customerimport.Row{Row: 1, Name: "Ola Reed", Phone: "07700 904000"})
	want(t, res, customerimport.ErasedOnTill)
}

// Rows the phone back-fill has not reached are still matched.
func TestApply_MatchesPhonesNotYetBackfilled(t *testing.T) {
	d := open(t)
	exec(t, d, `INSERT INTO customers(id,name,phone,phone_e164) VALUES ('b','Pat Lowe','07700 905000',NULL)`)
	res := apply(t, d, customerimport.Row{Row: 1, Name: "Pat Lowe", Phone: "+44 7700 905000", Address: "3 Test Mews"})
	want(t, res, customerimport.Updated)
	if n := count(t, d, `SELECT COUNT(*) FROM customers`); n != 1 {
		t.Fatalf("customers = %d", n)
	}
}
