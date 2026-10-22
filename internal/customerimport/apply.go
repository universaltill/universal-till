// Package customerimport applies a chunk of imported customer rows on the
// main till (ADR-0168 §5, ut-docs#4748). The rows arrive sealed from the
// cloud, which matches nothing and stores no customer; the till decides, by
// these rules, in this order:
//
//  1. a tombstoned link (the person was erased here) is skipped and counted;
//  2. the link (source, customer, external_id), then the folded email, then
//     the phone, then name + postcode only when exactly one customer fits;
//  3. two candidates, or an email and a phone naming different people, are
//     issues (ambiguous_match, conflicting_match), never a pick;
//  4. no match creates the customer, unless the email or phone is on the
//     erasure suppression list;
//  5. on a match the till fills empty fields and keeps every field that
//     differs; a kept field is reported as row number + field name only,
//     never a value; force_fields overwrites exactly the fields it names.
//
// Each link stores a salted hash of the fields last imported, so the same
// rows applied again change nothing. No customer value is logged or audited:
// the audit rows and the result carry ids, counts and field names.
package customerimport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// MaxRows is the most rows one chunk carries (ADR-0168 §5: 200 rows).
const MaxRows = 200

// Row outcomes.
const (
	Created          = "created"
	Updated          = "updated"
	Unchanged        = "unchanged"
	AmbiguousMatch   = "ambiguous_match"
	ConflictingMatch = "conflicting_match"
	ErasedOnTill     = "erased_on_till"
	Invalid          = "invalid"
)

// Field length bounds (runes).
const (
	maxName     = 200
	maxEmail    = 254
	maxPhone    = 40
	maxAddress  = 500
	maxPostcode = 16
	maxExtID    = 200
	maxSource   = 100
)

// Row is one imported customer. Row is the 1-based row number in the file,
// the only thing a result says about it besides an outcome and field names.
type Row struct {
	Row        int    `json:"row"`
	ExternalID string `json:"external_id,omitempty"`
	Name       string `json:"name"`
	Email      string `json:"email,omitempty"`
	Phone      string `json:"phone,omitempty"`
	Address    string `json:"address,omitempty"`
	Postcode   string `json:"postcode,omitempty"`
	// ForceFields names fields whose differing till value the owner chose to
	// replace with this row's value ("Use the file's value").
	ForceFields []string `json:"force_fields,omitempty"`
}

// Request is one chunk. Source is stamped by the cloud (never read from a
// row); JobID only labels the audit entry.
type Request struct {
	Source string `json:"source"`
	JobID  string `json:"job_id,omitempty"`
	Rows   []Row  `json:"rows"`
}

// RowResult is the outcome of one row. Fields means: for created, none; for
// updated and unchanged, the fields kept as the till has them; for invalid,
// the fields refused.
type RowResult struct {
	Row     int      `json:"row"`
	Outcome string   `json:"outcome"`
	Fields  []string `json:"fields,omitempty"`
}

// Result is what goes back to the cloud: outcomes and field names only.
type Result struct {
	Rows   []RowResult    `json:"rows"`
	Counts map[string]int `json:"counts"`
}

// Apply applies one chunk in a single transaction (a failure rolls the whole
// chunk back, so a re-delivery starts clean). actor is the audit actor.
func Apply(ctx context.Context, db *sql.DB, req Request, actor string) (Result, error) {
	source := strings.TrimSpace(req.Source)
	if source == "" || utf8.RuneCountInString(source) > maxSource {
		return Result{}, errors.New("customer import: a source of at most 100 characters is required")
	}
	if len(req.Rows) > MaxRows {
		return Result{}, fmt.Errorf("customer import: at most %d rows a chunk", MaxRows)
	}
	repo := data.NewCustomerImportRepo()
	pos := data.NewPOSRepo(db)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, fmt.Errorf("customer import: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	region, err := repo.Region(ctx, tx)
	if err != nil {
		return Result{}, err
	}
	salt, err := repo.Salt(ctx, tx)
	if err != nil {
		return Result{}, err
	}
	a := &applier{ctx: ctx, tx: tx, repo: repo, pos: pos, source: source, region: region, salt: salt, actor: actor}
	res := Result{Rows: make([]RowResult, 0, len(req.Rows)), Counts: map[string]int{}}
	for _, row := range req.Rows {
		rr, err := a.row(row)
		if err != nil {
			return Result{}, err
		}
		res.Rows = append(res.Rows, rr)
		res.Counts[rr.Outcome]++
	}
	summary := map[string]any{"source": source, "rows": len(req.Rows), "counts": res.Counts}
	if req.JobID != "" {
		summary["job_id"] = req.JobID
	}
	// A chunk that created and updated nothing (the same rows again) leaves
	// no trace at all: re-applying is zero changes.
	if res.Counts[Created]+res.Counts[Updated] > 0 {
		if err := pos.InsertAudit(ctx, tx, actor, "customer_import", source, "customers_imported", summary, nowUTC(), ""); err != nil {
			return Result{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Result{}, fmt.Errorf("customer import: commit: %w", err)
	}
	// Counts only: a customer value never reaches the log.
	logging.L().Infof("customer import: %d rows applied: %v", len(req.Rows), res.Counts)
	return res, nil
}

type applier struct {
	ctx    context.Context
	tx     *sql.Tx
	repo   *data.CustomerImportRepo
	pos    *data.POSRepo
	source string
	region string
	salt   []byte
	actor  string

	pendingPhones map[string][]string
	pendingErr    error
}

func (a *applier) row(in Row) (RowResult, error) {
	f := data.CustomerFields{
		Name: strings.TrimSpace(in.Name), Email: strings.TrimSpace(in.Email), Phone: strings.TrimSpace(in.Phone),
		Address: strings.TrimSpace(in.Address), Postcode: strings.TrimSpace(in.Postcode),
	}
	ext := strings.TrimSpace(in.ExternalID)
	if bad := invalidFields(f, ext, in.ForceFields); len(bad) > 0 {
		return RowResult{Row: in.Row, Outcome: Invalid, Fields: bad}, nil
	}
	emailKey := data.FoldEmail(f.Email)
	phoneKey := data.PhoneKey(f.Phone, a.region)
	synthetic := ext == ""
	if synthetic {
		ext = a.syntheticID(f, emailKey, phoneKey)
	}
	baseline := a.baseline(f, emailKey, phoneKey)

	link, linked, err := a.repo.LinkGet(a.ctx, a.tx, a.source, ext)
	if err != nil {
		return RowResult{}, err
	}
	if linked && link.Erased {
		return RowResult{Row: in.Row, Outcome: ErasedOnTill}, nil
	}

	// A source-given id is trusted: its link names the customer. A row with
	// no id gets its link from a hash of what it says, which must never skip
	// the match rules, so it is matched every time and the link only carries
	// the baseline (and the tombstone).
	var matched string
	byIdentifier := ""
	if linked && !synthetic {
		matched = link.LocalID
	} else {
		var outcome string
		matched, byIdentifier, outcome, err = a.match(f, emailKey, phoneKey)
		if err != nil {
			return RowResult{}, err
		}
		if outcome != "" {
			return RowResult{Row: in.Row, Outcome: outcome}, nil
		}
		if matched == "" && linked {
			// The same id-less row seen before and not matchable by an
			// identifier (a name alone never matches): its link finds it.
			matched = link.LocalID
		}
	}
	if matched != "" {
		if _, ok, err := a.repo.Get(a.ctx, a.tx, matched); err != nil {
			return RowResult{}, err
		} else if !ok {
			// The customer behind a live link is gone: erased (here or on
			// the primary). Never recreate them.
			if err := a.repo.LinkTombstone(a.ctx, a.tx, a.source, ext); err != nil {
				return RowResult{}, err
			}
			return RowResult{Row: in.Row, Outcome: ErasedOnTill}, nil
		}
		if linked && link.LocalID == matched && link.Baseline == baseline && len(in.ForceFields) == 0 {
			return RowResult{Row: in.Row, Outcome: Unchanged}, nil
		}
		// A row that matched on name + postcode alone but carries an email
		// or phone of an erased person is that person: never write it onto
		// someone else.
		if byIdentifier == "namepostcode" {
			gone, err := a.suppressed(f, emailKey, phoneKey)
			if err != nil {
				return RowResult{}, err
			}
			if gone {
				if err := a.repo.LinkTombstone(a.ctx, a.tx, a.source, ext); err != nil {
					return RowResult{}, err
				}
				return RowResult{Row: in.Row, Outcome: ErasedOnTill}, nil
			}
		}
	}

	if matched == "" {
		suppressed, err := a.suppressed(f, emailKey, phoneKey)
		if err != nil {
			return RowResult{}, err
		}
		if suppressed {
			if err := a.repo.LinkTombstone(a.ctx, a.tx, a.source, ext); err != nil {
				return RowResult{}, err
			}
			return RowResult{Row: in.Row, Outcome: ErasedOnTill}, nil
		}
		id, err := a.repo.Create(a.ctx, a.tx, f, a.region)
		if err != nil {
			return RowResult{}, err
		}
		if err := a.repo.LinkPut(a.ctx, a.tx, a.source, ext, id, baseline); err != nil {
			return RowResult{}, err
		}
		if err := a.audit(id, "customer_imported", map[string]any{"source": a.source, "created": true}); err != nil {
			return RowResult{}, err
		}
		return RowResult{Row: in.Row, Outcome: Created}, nil
	}

	have, _, err := a.repo.Get(a.ctx, a.tx, matched)
	if err != nil {
		return RowResult{}, err
	}
	set, filled, kept, err := a.diff(have, f, in.ForceFields)
	if err != nil {
		return RowResult{}, err
	}
	outcome := Unchanged
	if len(set) > 0 {
		if err := a.repo.SetFields(a.ctx, a.tx, matched, set, a.region); err != nil {
			return RowResult{}, err
		}
		outcome = Updated
		if err := a.audit(matched, "customer_imported", map[string]any{"source": a.source, "filled": filled, "kept": kept}); err != nil {
			return RowResult{}, err
		}
	}
	if err := a.repo.LinkPut(a.ctx, a.tx, a.source, ext, matched, baseline); err != nil {
		return RowResult{}, err
	}
	return RowResult{Row: in.Row, Outcome: outcome, Fields: kept}, nil
}

// match applies the identifier order for a row not matched by a link. It
// returns the matched customer id and what matched it, or an issue outcome,
// or nothing (create).
func (a *applier) match(f data.CustomerFields, emailKey, phoneKey string) (id, by, issue string, err error) {
	var byEmail, byPhone []string
	if emailKey != "" {
		if byEmail, err = a.repo.FindByEmail(a.ctx, a.tx, f.Email); err != nil {
			return "", "", "", err
		}
	}
	if phoneKey != "" {
		pending := a.pending()
		if a.pendingErr != nil {
			return "", "", "", a.pendingErr
		}
		if byPhone, err = a.repo.FindByPhone(a.ctx, a.tx, phoneKey, pending); err != nil {
			return "", "", "", err
		}
	}
	if len(byEmail) > 1 || len(byPhone) > 1 {
		return "", "", AmbiguousMatch, nil
	}
	if len(byEmail) == 1 && len(byPhone) == 1 && byEmail[0] != byPhone[0] {
		return "", "", ConflictingMatch, nil
	}
	switch {
	case len(byEmail) == 1:
		return byEmail[0], "email", "", nil
	case len(byPhone) == 1:
		return byPhone[0], "phone", "", nil
	}
	np, err := a.repo.FindByNamePostcode(a.ctx, a.tx, f.Name, f.Postcode)
	if err != nil {
		return "", "", "", err
	}
	if len(np) == 1 {
		return np[0], "namepostcode", "", nil
	}
	return "", "", "", nil
}

// pending is the phone index of rows the phone_e164 back-fill has not
// reached, built once per chunk (it parses every such row).
func (a *applier) pending() map[string][]string {
	if a.pendingPhones == nil {
		idx, err := a.repo.PendingPhoneIndex(a.ctx, a.tx, a.region)
		if err != nil {
			a.pendingErr = err
			idx = map[string][]string{}
		}
		a.pendingPhones = idx
	}
	return a.pendingPhones
}

// suppressed reports whether the row names an erased person: the salted hash
// of its email, or of its phone in either key form, is on the list.
func (a *applier) suppressed(f data.CustomerFields, emailKey, phoneKey string) (bool, error) {
	for _, k := range a.suppressionKeys(f, emailKey, phoneKey) {
		ok, err := a.repo.Suppressed(a.ctx, a.tx, data.Hash(a.salt, k.kind, k.v))
		if err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}

type keyOf struct{ kind, v string }

// suppressionKeys are the values a row can be recognised by on the erasure
// list. A phone has two forms (E.164 and bare digits) because the shop's
// country may have been set between the erasure and now.
func (a *applier) suppressionKeys(f data.CustomerFields, emailKey, phoneKey string) []keyOf {
	var ks []keyOf
	if emailKey != "" {
		ks = append(ks, keyOf{"email", emailKey})
	}
	for _, v := range data.PhoneKeyForms(f.Phone, phoneKey) {
		ks = append(ks, keyOf{"phone", v})
	}
	return ks
}

// diff compares the till's customer with the row. It returns the fields to
// write (empty ones are filled; forced ones overwritten), the names filled
// or forced, and the names kept: those that differ, and an email or phone
// that is on the erasure list (never written, forced or not).
func (a *applier) diff(have, file data.CustomerFields, force []string) (set map[string]string, filled, kept []string, err error) {
	forced := map[string]bool{}
	for _, n := range force {
		forced[n] = true
	}
	set = map[string]string{}
	for _, name := range data.CustomerImportFieldNames {
		fv := file.Get(name)
		if fv == "" {
			continue
		}
		hv := have.Get(name)
		write := hv == "" || (forced[name] && strings.TrimSpace(hv) != fv)
		if !write {
			if !a.same(name, hv, fv) {
				kept = append(kept, name)
			}
			continue
		}
		if name == "email" || name == "phone" {
			gone, err := a.valueSuppressed(name, fv)
			if err != nil {
				return nil, nil, nil, err
			}
			if gone {
				kept = append(kept, name)
				continue
			}
		}
		set[name] = fv
		filled = append(filled, name)
	}
	return set, filled, kept, nil
}

// valueSuppressed reports whether one email or phone value is on the
// erasure list.
func (a *applier) valueSuppressed(field, value string) (bool, error) {
	f := data.CustomerFields{}
	emailKey, phoneKey := "", ""
	if field == "email" {
		f.Email, emailKey = value, data.FoldEmail(value)
	} else {
		f.Phone, phoneKey = value, data.PhoneKey(value, a.region)
	}
	return a.suppressed(f, emailKey, phoneKey)
}

// same compares two values of a field the way matching does: names and
// addresses by search fold, email by its fold, phone by its E.164 key,
// postcode without spaces or case.
func (a *applier) same(field, x, y string) bool {
	switch field {
	case "email":
		return data.FoldEmail(x) == data.FoldEmail(y)
	case "phone":
		kx, ky := data.PhoneKey(x, a.region), data.PhoneKey(y, a.region)
		if kx != "" && ky != "" {
			return kx == ky
		}
		return strings.TrimSpace(x) == strings.TrimSpace(y)
	case "postcode":
		return data.FoldPostcode(x) == data.FoldPostcode(y)
	}
	return data.FoldText(x) == data.FoldText(y)
}

// syntheticID is the link key of a row the source gave no id: a salted hash
// of its email, else its phone, else of everything it says, so re-importing
// the same file finds the same link instead of creating the customer again.
// A row with no email and no phone is only ever told apart by its whole
// content: two identical rows are one person here.
func (a *applier) syntheticID(f data.CustomerFields, emailKey, phoneKey string) string {
	switch {
	case emailKey != "":
		return "k:" + data.Hash(a.salt, "key-email", emailKey)
	case phoneKey != "":
		return "k:" + data.Hash(a.salt, "key-phone", phoneKey)
	}
	return "k:" + data.Hash(a.salt, "key-all", strings.Join([]string{
		data.FoldText(f.Name), data.FoldText(f.Address), data.FoldPostcode(f.Postcode)}, "\x1f"))
}

func (a *applier) baseline(f data.CustomerFields, emailKey, phoneKey string) string {
	return data.Hash(a.salt, "baseline", strings.Join([]string{
		data.FoldText(f.Name), emailKey, phoneKey, data.FoldText(f.Address), data.FoldPostcode(f.Postcode),
	}, "\x1f"))
}

// audit records a customer change by id and field names, never by value.
func (a *applier) audit(customerID, action string, payload map[string]any) error {
	return a.pos.InsertAudit(a.ctx, a.tx, a.actor, "customer", customerID, action, payload, nowUTC(), "")
}

// invalidFields names the fields a row may not carry.
func invalidFields(f data.CustomerFields, ext string, force []string) []string {
	var bad []string
	add := func(n string, cond bool) {
		if cond {
			bad = append(bad, n)
		}
	}
	long := func(s string, n int) bool { return utf8.RuneCountInString(s) > n }
	add("name", f.Name == "" || long(f.Name, maxName) || hasControl(f.Name))
	at := strings.Index(f.Email, "@")
	add("email", f.Email != "" && (at < 1 || at == len(f.Email)-1 || strings.ContainsAny(f.Email, " \t\r\n") || long(f.Email, maxEmail) || hasControl(f.Email)))
	add("phone", f.Phone != "" && (long(f.Phone, maxPhone) || hasControl(f.Phone) || !hasDigit(f.Phone)))
	add("address", long(f.Address, maxAddress) || hasControl(f.Address))
	add("postcode", long(f.Postcode, maxPostcode) || hasControl(f.Postcode))
	add("external_id", long(ext, maxExtID) || hasControl(ext))
	for _, n := range force {
		known := false
		for _, k := range data.CustomerImportFieldNames {
			known = known || k == n
		}
		if !known {
			bad = append(bad, "force_fields")
			break
		}
	}
	return bad
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < ' ' && r != '\n' && r != '\t' || r == 0x7f {
			return true
		}
	}
	return false
}

func hasDigit(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}
