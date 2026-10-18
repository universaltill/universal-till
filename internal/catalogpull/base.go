package catalogpull

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// Every uploadable row keeps a base (cloud_links.base, migration 084): the
// values of its uploadable fields the till last adopted from the cloud, in
// the cloud's own representation (cloud_ids for references, minor units,
// basis points). A field whose local value differs from its base is a till
// edit not answered by the cloud yet: the upload sends it with the base as
// its before value (the conflict test), and the pull leaves it alone
// (ADR-0162 §4: field-wise apply).
//
// The base is set from what the till APPLIED — a pulled row's fields, or
// an upload answer's current row — never from a row it refused, so a
// refused cloud value never looks like a till edit.

// uploadFields are each kind's uploadable fields (contract
// cloud-catalog-changes.md 1.0.0), in upload order.
var uploadFields = map[string][]string{
	kindItem:     {"name", "description", "sku", "price_minor", "category_cloud_id", "tax_code_cloud_id", "active", "barcodes"},
	kindCategory: {"name", "parent_cloud_id", "sort_order", "active"},
	kindTaxCode:  {"name", "rate_basis_points", "takeaway_rate_basis_points", "active"},
}

// uploadable reports whether kind's rows upload.
func uploadable(kind string) bool { _, ok := uploadFields[kind]; return ok }

// baseBarcode is one entry of an item base's barcodes: the code as the
// cloud holds it and the till's stored key for it (they differ for an
// embedded-data code, whose key is its zeroed template).
type baseBarcode struct {
	Code string `json:"code"`
	Key  string `json:"key"`
}

// values is a row's uploadable fields, field → JSON value.
type values map[string]json.RawMessage

func parseBase(raw string) values {
	if raw == "" {
		return nil
	}
	var v values
	if json.Unmarshal([]byte(raw), &v) != nil {
		return nil // a damaged base: treated as none
	}
	return v
}

func (v values) encode() string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func rawJSON(x any) json.RawMessage {
	raw, _ := json.Marshal(x)
	return raw
}

// norm is a JSON value in one canonical spelling (numbers kept exact), so
// equal values compare equal.
func norm(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "null"
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var x any
	if dec.Decode(&x) != nil {
		return string(raw)
	}
	out, _ := json.Marshal(x)
	return string(out)
}

// barcodesOf decodes a base's barcodes value ([] when absent).
func barcodesOf(raw json.RawMessage) []baseBarcode {
	var out []baseBarcode
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

// keyList is the canonical comparison of a barcode set: its keys, the
// primary first and the rest sorted (the contract's barcodes comparison).
func keyList(keys []string) string {
	if len(keys) == 0 {
		return "[]"
	}
	rest := append([]string(nil), keys[1:]...)
	sort.Strings(rest)
	return strings.Join(append([]string{keys[0]}, rest...), "\x00")
}

func baseKeys(raw json.RawMessage) string {
	bcs := barcodesOf(raw)
	keys := make([]string, len(bcs))
	for i, b := range bcs {
		keys[i] = b.Key
	}
	return keyList(keys)
}

// local is a row's current local state for the upload: its values in the
// cloud's representation and, for an item, its barcode keys.
type local struct {
	vals   values
	keys   []string // item: item-level barcode keys, primary first
	types  map[string]string
	exists bool
	// unlinked are referenced local rows with no cloud_id yet (a category
	// created offline, say): they upload first.
	unlinked []ref
}

type ref struct{ kind, localID string }

// readLocal reads (kind, localID)'s uploadable values. base supplies the
// cloud's code of a barcode key the till already shares with the cloud.
func (r *runner) readLocal(ctx context.Context, q data.SyncQuerier, kind, localID string, base values) (local, error) {
	var l local
	refID := func(refKind, id string) (json.RawMessage, error) {
		if id == "" {
			return json.RawMessage("null"), nil
		}
		st, ok, err := r.links.StateByLocal(ctx, q, refKind, id)
		if err != nil {
			return nil, err
		}
		if !ok {
			l.unlinked = append(l.unlinked, ref{refKind, id})
			return json.RawMessage("null"), nil
		}
		return rawJSON(st.CloudID), nil
	}
	switch kind {
	case kindItem:
		it, bcs, ok, err := r.outbox.OutboxItemValues(ctx, q, localID)
		if err != nil || !ok {
			return l, err
		}
		l.exists = true
		cat, err := refID(kindCategory, it.CategoryID)
		if err != nil {
			return l, err
		}
		tax, err := refID(kindTaxCode, it.TaxCodeID)
		if err != nil {
			return l, err
		}
		codes := map[string]string{}
		for _, b := range barcodesOf(base["barcodes"]) {
			codes[b.Key] = b.Code
		}
		type wireBarcode struct {
			Code string `json:"code"`
			Type string `json:"type"`
		}
		list := make([]wireBarcode, 0, len(bcs))
		l.types = map[string]string{}
		for _, b := range bcs {
			code := b.Key
			if c, ok := codes[b.Key]; ok {
				code = c
			}
			list = append(list, wireBarcode{Code: code, Type: b.Type})
			l.keys = append(l.keys, b.Key)
			l.types[b.Key] = b.Type
		}
		l.vals = values{
			"name": rawJSON(it.Name), "description": rawJSON(it.Description), "sku": rawJSON(it.SKU),
			"price_minor": rawJSON(it.PriceMinor), "category_cloud_id": cat, "tax_code_cloud_id": tax,
			"active": rawJSON(it.Active), "barcodes": rawJSON(list),
		}
	case kindCategory:
		c, ok, err := r.outbox.OutboxCategoryValues(ctx, q, localID)
		if err != nil || !ok {
			return l, err
		}
		l.exists = true
		parent, err := refID(kindCategory, c.ParentID)
		if err != nil {
			return l, err
		}
		l.vals = values{"name": rawJSON(c.Name), "parent_cloud_id": parent, "sort_order": rawJSON(c.Sort), "active": rawJSON(c.Active)}
	case kindTaxCode:
		t, ok, err := r.outbox.OutboxTaxCodeValues(ctx, q, localID)
		if err != nil || !ok {
			return l, err
		}
		l.exists = true
		l.vals = values{
			"name": rawJSON(t.Name), "rate_basis_points": rawJSON(t.RateBP),
			"takeaway_rate_basis_points": rawJSON(t.TakeawayBP), "active": rawJSON(t.Active),
		}
	}
	return l, nil
}

// changedFields are the uploadable fields whose local value differs from
// base, in upload order. A field the base lacks counts as changed only if
// the base lacks every field (no base at all is the caller's case). An
// item's sku compares with the SKU the till kept for a blank cloud one
// (keptSKUField) when the base holds one.
func changedFields(kind string, l local, base values) []string {
	var out []string
	for _, f := range uploadFields[kind] {
		b, ok := base[f]
		if !ok {
			continue
		}
		if f == "barcodes" {
			if keyList(l.keys) != baseKeys(b) {
				out = append(out, f)
			}
			continue
		}
		if kept, ok := base[keptSKUField]; ok && f == "sku" {
			b = kept
		}
		if norm(l.vals[f]) != norm(b) {
			out = append(out, f)
		}
	}
	return out
}

// baseFromRow is a pulled (or answered) row's base: the old base with
// every uploadable field the row carried overwritten by its value (a blank
// sku and a null description as the till applies them), the item's
// barcodes kept (they arrive as their own rows).
func baseFromRow(kind string, f fields, old values) values {
	out := values{}
	for k, v := range old {
		out[k] = v
	}
	for _, name := range uploadFields[kind] {
		if name == "barcodes" {
			continue
		}
		raw, present, null := f.get(name)
		if !present {
			continue
		}
		switch {
		case name == "description" && null:
			out[name] = rawJSON("")
		case name == "sku" && (null || norm(raw) == `""`):
			// The till keeps its own SKU for a blank one (applyItem):
			// before stays the cloud's "", keptSKUBase records the kept one.
			out[name] = rawJSON("")
		case name == "sku":
			out[name] = append(json.RawMessage(nil), raw...)
			delete(out, keptSKUField)
		default:
			out[name] = append(json.RawMessage(nil), raw...)
		}
	}
	if kind == kindItem {
		if _, ok := out["barcodes"]; !ok {
			out["barcodes"] = json.RawMessage("[]")
		}
	}
	return out
}

// keptSKUField is the base entry holding the SKU the till kept for a blank
// cloud one; not an upload field, never sent.
const keptSKUField = "sku_kept"

// keptSKUBase records in an item's base the SKU the till kept for a blank
// cloud one (applyItem keeps or generates its own: never an item without
// SKU, ut-docs#1459). The base's sku stays the cloud's "" (the change's
// before, which the cloud tests); changedFields compares the local SKU
// with the kept one, so the kept SKU never uploads by itself, and an
// operator's later SKU edit does (ut-docs#4464).
func (r *runner) keptSKUBase(ctx context.Context, tx *sql.Tx, kind, localID string, f fields, base values) error {
	if kind != kindItem {
		return nil
	}
	if raw, present, null := f.get("sku"); !present || (!null && norm(raw) != `""`) {
		return nil
	}
	it, _, ok, err := r.outbox.OutboxItemValues(ctx, tx, localID)
	if err != nil || !ok {
		return err
	}
	base[keptSKUField] = rawJSON(it.SKU)
	return nil
}

// setBaseBarcode records an item-level barcode the till applied in its
// item's base: code is the cloud's, key the till's; primary moves it first.
func (r *runner) setBaseBarcode(ctx context.Context, tx *sql.Tx, itemID, code, key string, primary bool) error {
	st, ok, err := r.links.StateByLocal(ctx, tx, kindItem, itemID)
	if err != nil || !ok {
		return err
	}
	base := parseBase(st.Base)
	if base == nil {
		base = values{}
	}
	list := barcodesOf(base["barcodes"])
	kept := make([]baseBarcode, 0, len(list)+1)
	for _, b := range list {
		if b.Key != key {
			kept = append(kept, b)
		}
	}
	entry := baseBarcode{Code: code, Key: key}
	if primary {
		kept = append([]baseBarcode{entry}, kept...)
	} else {
		kept = append(kept, entry)
	}
	base["barcodes"] = rawJSON(kept)
	return r.links.SetBase(ctx, tx, kindItem, st.CloudID, base.encode())
}

// dropBaseBarcode removes a barcode key from its item's base.
func (r *runner) dropBaseBarcode(ctx context.Context, tx *sql.Tx, itemID, key string) error {
	st, ok, err := r.links.StateByLocal(ctx, tx, kindItem, itemID)
	if err != nil || !ok || st.Base == "" {
		return err
	}
	base := parseBase(st.Base)
	list := barcodesOf(base["barcodes"])
	kept := make([]baseBarcode, 0, len(list))
	for _, b := range list {
		if b.Key != key {
			kept = append(kept, b)
		}
	}
	if len(kept) == len(list) {
		return nil
	}
	base["barcodes"] = rawJSON(kept)
	return r.links.SetBase(ctx, tx, kindItem, st.CloudID, base.encode())
}

// pendingFields are the fields of a linked row with an unanswered local
// edit that the pull must leave alone: those whose local value differs
// from base. None without an outbox mark, or without a base (nothing to
// compare against: the cloud wins).
func (r *runner) pendingFields(ctx context.Context, tx *sql.Tx, kind, localID string, st data.CloudLinkState) (map[string]bool, bool, error) {
	if !uploadable(kind) {
		return nil, false, nil
	}
	marked, err := r.outbox.HasMark(ctx, tx, kind, localID)
	if err != nil || !marked {
		return nil, false, err
	}
	base := parseBase(st.Base)
	l, err := r.readLocal(ctx, tx, kind, localID, base)
	if err != nil {
		return nil, true, err
	}
	if !l.exists {
		return nil, true, nil
	}
	out := map[string]bool{}
	if base != nil {
		for _, f := range changedFields(kind, l, base) {
			out[f] = true
		}
	}
	return out, true, nil
}

// seedChunk is how many links one seeding transaction carries.
const seedChunk = 500

// seedBasesIfUpgrading seeds the bases of an applier-3 till (seedBases)
// before its first upload or re-pull; a no-op once the stored applier
// version is current, and on a till that never pulled.
func seedBasesIfUpgrading(ctx context.Context, db *sql.DB, settings *data.SettingsRepo) error {
	v, ok, err := settings.Get(ctx, KeyApplierVersion)
	if err != nil {
		return fmt.Errorf("catalogpull: read %s: %w", KeyApplierVersion, err)
	}
	n, perr := strconv.ParseInt(v, 10, 64)
	if !ok || perr != nil || n <= 0 || n >= ApplierVersion {
		return nil
	}
	return seedBases(ctx, db)
}

// seedBases gives every pulled uploadable link with no base (an applier-3
// build linked it before migration 084 added the column) a base seeded
// from its local row (ut-docs#4464 review). Such a row's local values are
// what the till last pulled, so they are the right before for a till edit
// made from now on: the re-pull leaves the edited field alone and the
// upload's conflict test compares it to the cloud's value — a cloud edit
// made since still wins as a conflict. (Treating every field of a marked
// base-less row as pending instead would make the re-pulled cloud row the
// base, so the upload would carry every field the till holds differently,
// reverting cloud edits the till never saw.) A row already marked when its
// base is seeded — edited in the moments between the upgrade and the first
// cloud tick — cannot tell its edit from what it pulled: the cloud's row
// wins it, and the till logs how many there were.
func seedBases(ctx context.Context, db *sql.DB) error {
	r := newRunner(db, Options{})
	seeded, marked := 0, 0
	for _, kind := range []string{kindTaxCode, kindCategory, kindItem} {
		after := ""
		for {
			n, last, m, err := r.seedChunk(ctx, kind, after)
			if err != nil {
				return err
			}
			seeded, marked = seeded+n, marked+m
			if last == "" {
				break
			}
			after = last
		}
	}
	if seeded > 0 {
		logging.L().Infof("catalogpull: seeded the upload base of %d rows pulled by an older build", seeded)
	}
	if marked > 0 {
		logging.L().Warnf("catalogpull: %d rows were edited on this till before their upload base existed; the cloud's values replace those edits", marked)
	}
	return nil
}

// seedChunk seeds one chunk of kind's base-less links after the cloud_id
// after; last is the chunk's last cloud_id ("" when there were none).
func (r *runner) seedChunk(ctx context.Context, kind, after string) (seeded int, last string, marked int, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, "", 0, fmt.Errorf("catalogpull: seed bases: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	links, err := r.links.LinksWithoutBase(ctx, tx, kind, after, seedChunk)
	if err != nil || len(links) == 0 {
		return 0, "", 0, err
	}
	for _, l := range links {
		last = l.CloudID
		loc, err := r.readLocal(ctx, tx, kind, l.LocalID, nil)
		if err != nil {
			return 0, "", 0, err
		}
		if !loc.exists {
			continue // gone locally: the pull or the upload settles it
		}
		base := values{}
		for _, f := range uploadFields[kind] {
			if f == "barcodes" {
				list := make([]baseBarcode, len(loc.keys))
				for i, k := range loc.keys {
					list[i] = baseBarcode{Code: k, Key: k}
				}
				base[f] = rawJSON(list)
				continue
			}
			base[f] = loc.vals[f]
		}
		if err := r.links.SetBase(ctx, tx, kind, l.CloudID, base.encode()); err != nil {
			return 0, "", 0, err
		}
		seeded++
		if m, err := r.outbox.HasMark(ctx, tx, kind, l.LocalID); err != nil {
			return 0, "", 0, err
		} else if m {
			marked++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, "", 0, fmt.Errorf("catalogpull: seed bases: commit: %w", err)
	}
	return seeded, last, marked, nil
}
