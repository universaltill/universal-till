package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/logging"
)

// ut-docs#6566 (ADR-0201 amendment 2026-11-18): the per-till card reader
// registry. A reader is one physical (or device) card reader this till can
// take a card payment on, of any provider. Core never branches on a provider
// name: `reader_ref` is the provider's own id, opaque to core, and only a
// plugin that declares the `payment.<key>.readers` hook is sent it.
//
// Till-local by design: no repository method here feeds the LAN journal, a
// snapshot or the cloud roster (guarded by TestCardReaders_NotInSyncPaths).

const (
	// MaxActiveCardReaders is an engineering bound on the picker and the
	// state cache, not a commercial plan limit (BA B1).
	MaxActiveCardReaders = 32
	// MaxCardReaderNameLen is the display-name bound (characters).
	MaxCardReaderNameLen = 40

	CardReaderKindCloud    = "cloud"
	CardReaderKindSDK      = "sdk" // provider SDK in the native shell (ADR-0205 §3)
	CardReaderKindTapToPay = "tap_to_pay"
	CardReaderKindPhone    = "phone"
	CardReaderKindExternal = "external" // the shop's own unconnected terminal (ADR-0205 §7)

	CardReaderTransportInternet  = "internet"
	CardReaderTransportBluetooth = "bluetooth"
	CardReaderTransportUSB       = "usb"
	CardReaderTransportDeviceOwn = "device_own"
	CardReaderTransportNone      = "none"
	// CardReaderTransportSerial is reserved (ADR-0205 §1): named only so it is refused.
	CardReaderTransportSerial = "serial"

	CardReaderSourceManual   = "manual"
	CardReaderSourceMigrated = "migrated"
	CardReaderSourceDevice   = "device"
	// CardReaderSourcePaired is a phone reader enrolled through the pairing
	// flow (ut-docs#6538); its identity is in card_reader_credentials.
	CardReaderSourcePaired = "paired"
)

var (
	ErrCardReaderNotFound = errors.New("card reader not found")
	ErrCardReaderName     = errors.New("card reader name must be 1-40 characters and unused")
	ErrCardReaderRef      = errors.New("card reader id is invalid or already added")
	ErrCardReaderKind     = errors.New("card reader kind is not allowed here")
	ErrCardReaderLimit    = errors.New("card reader limit reached")
	// ErrCardReaderTransport wraps ErrCardReaderKind so the 422 mapping and
	// every errors.Is(err, ErrCardReaderKind) caller keep working.
	ErrCardReaderTransport = fmt.Errorf("card reader kind and connection do not match: %w", ErrCardReaderKind)
)

// cardReaderRefRe is what core accepts as a provider reader id: plugins put
// it straight into a provider URL path, so no slash, no whitespace.
var cardReaderRefRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

// ValidCardReaderRef reports whether ref is an acceptable provider reader id.
// On top of the character set, the id must start with a letter or digit and
// hold no `..`: a dot segment (`.`, `..`) in a URL path would climb out of the
// provider's readers/ resource (review, ut-docs#6566).
func ValidCardReaderRef(ref string) bool {
	if !cardReaderRefRe.MatchString(ref) || strings.Contains(ref, "..") {
		return false
	}
	c := ref[0]
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// ValidCardReaderPair reports whether (kind, transport) is allowed
// (ADR-0205 §1). The reserved `serial` transport is never allowed.
func ValidCardReaderPair(kind, transport string) bool {
	switch kind {
	case CardReaderKindCloud, CardReaderKindPhone:
		return transport == CardReaderTransportInternet
	case CardReaderKindSDK:
		return transport == CardReaderTransportBluetooth || transport == CardReaderTransportUSB || transport == CardReaderTransportInternet
	case CardReaderKindTapToPay:
		return transport == CardReaderTransportDeviceOwn
	case CardReaderKindExternal:
		return transport == CardReaderTransportNone
	}
	return false
}

// defaultCardReaderTransport is the transport an empty value means; "" for
// sdk (it must be explicit) and for unknown kinds.
func defaultCardReaderTransport(kind string) string {
	switch kind {
	case CardReaderKindCloud, CardReaderKindPhone:
		return CardReaderTransportInternet
	case CardReaderKindTapToPay:
		return CardReaderTransportDeviceOwn
	case CardReaderKindExternal:
		return CardReaderTransportNone
	}
	return ""
}

// knownCardReaderKind reports whether kind is one of the ADR-0205 §1 kinds.
func knownCardReaderKind(kind string) bool {
	switch kind {
	case CardReaderKindCloud, CardReaderKindSDK, CardReaderKindTapToPay, CardReaderKindPhone, CardReaderKindExternal:
		return true
	}
	return false
}

// CardReaderUnattendedAllowed reports whether a reader of this kind may be
// the self-order kiosk's default. tap_to_pay is the staff device's own NFC;
// sdk and external readers cannot take a kiosk payment yet (ADR-0205 §3,
// #6755), so SetUnattendedDefault refuses them and the Settings list hides
// the checkbox for them (it still shows it ticked so it can be cleared).
func CardReaderUnattendedAllowed(kind string) bool {
	switch kind {
	case CardReaderKindCloud, CardReaderKindPhone:
		return true
	}
	return false
}

// CardReader is one registry row.
type CardReader struct {
	ID                string
	Name              string
	MethodKey         string
	PluginID          string
	ReaderRef         string
	Kind              string
	Transport         string // how the reader reaches the device that drives it (ADR-0205 §1)
	DeviceSerial      string // provider-reported serial after a bind; "" = never bound (ADR-0205 §5)
	BoundAt           string // UTC time of the bind; "" = never bound
	Source            string
	SortOrder         int
	UnattendedDefault bool
	LastUsedAt        string // "" = never used on this till
	RemovedAt         string // "" = active
}

// CardReaderRepo is the data access for card_readers. All SQL for the
// registry lives here (repository pattern).
type CardReaderRepo struct {
	db *sql.DB
	// onSettingsChanged fires after SoftRemove cleared a legacy plugin
	// setting, so the caller can bump the plugin-settings generation
	// (same contract as PluginRepo.OnSettingsChanged).
	onSettingsChanged func()
}

var cardReaderObs = newRepoObservability("card_reader")

// NewCardReaderRepo returns the repository.
func NewCardReaderRepo(db *sql.DB) *CardReaderRepo { return &CardReaderRepo{db: db} }

// OnSettingsChanged attaches the plugin-settings generation bump.
func (r *CardReaderRepo) OnSettingsChanged(fn func()) *CardReaderRepo {
	r.onSettingsChanged = fn
	return r
}

const cardReaderCols = `id, name, method_key, plugin_id, reader_ref, kind, source, sort_order, unattended_default, COALESCE(last_used_at,''), COALESCE(removed_at,''), transport, COALESCE(device_serial,''), COALESCE(bound_at,'')`

func scanCardReader(s rowScanner) (CardReader, error) {
	var c CardReader
	var un int
	if err := s.Scan(&c.ID, &c.Name, &c.MethodKey, &c.PluginID, &c.ReaderRef, &c.Kind, &c.Source, &c.SortOrder, &un, &c.LastUsedAt, &c.RemovedAt, &c.Transport, &c.DeviceSerial, &c.BoundAt); err != nil {
		return CardReader{}, err
	}
	c.UnattendedDefault = un == 1
	return c, nil
}

func (r *CardReaderRepo) query(ctx context.Context, where string, args ...any) ([]CardReader, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+cardReaderCols+` FROM card_readers WHERE removed_at IS NULL `+where+` ORDER BY sort_order, created_at, id`, args...)
	if err != nil {
		return nil, cardReaderObs.wrap("list", err)
	}
	defer rows.Close()
	var out []CardReader
	for rows.Next() {
		c, err := scanCardReader(rows)
		if err != nil {
			return nil, cardReaderObs.wrap("scan", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListActive returns the active readers in picker order.
func (r *CardReaderRepo) ListActive(ctx context.Context) ([]CardReader, error) {
	return r.query(ctx, "")
}

// ListActiveByMethod returns the active readers of one payment method key.
func (r *CardReaderRepo) ListActiveByMethod(ctx context.Context, method string) ([]CardReader, error) {
	return r.query(ctx, "AND method_key = ?", method)
}

// Get returns one reader (active or removed: history and refunds need it).
func (r *CardReaderRepo) Get(ctx context.Context, id string) (CardReader, error) {
	c, err := scanCardReader(r.db.QueryRowContext(ctx, `SELECT `+cardReaderCols+` FROM card_readers WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return CardReader{}, ErrCardReaderNotFound
	}
	if err != nil {
		return CardReader{}, cardReaderObs.wrap("get", err)
	}
	return c, nil
}

// GetByRef returns the reader (active or removed) a provider ref maps to.
func (r *CardReaderRepo) GetByRef(ctx context.Context, method, ref string) (CardReader, error) {
	c, err := scanCardReader(r.db.QueryRowContext(ctx, `SELECT `+cardReaderCols+` FROM card_readers WHERE method_key = ? AND reader_ref = ?`, method, ref))
	if errors.Is(err, sql.ErrNoRows) {
		return CardReader{}, ErrCardReaderNotFound
	}
	if err != nil {
		return CardReader{}, cardReaderObs.wrap("get_by_ref", err)
	}
	return c, nil
}

func cleanReaderName(n string) (string, bool) {
	n = strings.TrimSpace(n)
	if n == "" || utf8.RuneCountInString(n) > MaxCardReaderNameLen {
		return "", false
	}
	for _, c := range n {
		if c < 0x20 || c == 0x7f {
			return "", false
		}
	}
	return n, true
}

type cardReaderExec interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}

func nameTaken(ctx context.Context, x cardReaderExec, name, exceptID string) (bool, error) {
	var n int
	err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM card_readers WHERE removed_at IS NULL AND LOWER(name) = LOWER(?) AND id <> ?`, name, exceptID).Scan(&n)
	return n > 0, err
}

// Create adds a reader (or revives a removed row with the same method+ref).
// It enforces the 32-active cap, name 1-40 chars unique among active rows,
// and the strict reader id format.
func (r *CardReaderRepo) Create(ctx context.Context, in CardReader) (CardReader, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return CardReader{}, cardReaderObs.wrap("create", err)
	}
	defer tx.Rollback()
	id, err := r.createTx(ctx, tx, in)
	if err != nil {
		return CardReader{}, err
	}
	if err := tx.Commit(); err != nil {
		return CardReader{}, cardReaderObs.wrap("create", err)
	}
	return r.Get(ctx, id)
}

// createTx is Create inside the caller's transaction: it validates, applies
// the cap and name rules, inserts (or revives) the row and returns its id.
func (r *CardReaderRepo) createTx(ctx context.Context, tx *sql.Tx, in CardReader) (string, error) {
	name, ok := cleanReaderName(in.Name)
	if !ok {
		return "", ErrCardReaderName
	}
	if !ValidCardReaderRef(in.ReaderRef) || strings.TrimSpace(in.MethodKey) == "" || strings.TrimSpace(in.PluginID) == "" {
		return "", ErrCardReaderRef
	}
	if in.Kind == "" {
		in.Kind = CardReaderKindCloud
	}
	if in.Transport == "" {
		in.Transport = defaultCardReaderTransport(in.Kind)
	}
	if !knownCardReaderKind(in.Kind) {
		return "", ErrCardReaderKind
	}
	if !ValidCardReaderPair(in.Kind, in.Transport) {
		return "", ErrCardReaderTransport
	}
	if in.Source == "" {
		in.Source = CardReaderSourceManual
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM card_readers WHERE removed_at IS NULL`).Scan(&active); err != nil {
		return "", cardReaderObs.wrap("create", err)
	}
	if active >= MaxActiveCardReaders {
		return "", ErrCardReaderLimit
	}
	var existingID, existingRemoved sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id, removed_at FROM card_readers WHERE method_key = ? AND reader_ref = ?`, in.MethodKey, in.ReaderRef).Scan(&existingID, &existingRemoved)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", cardReaderObs.wrap("create", err)
	}
	if err == nil && !existingRemoved.Valid {
		return "", ErrCardReaderRef // already an active reader
	}
	id := existingID.String
	if taken, err := nameTaken(ctx, tx, name, id); err != nil {
		return "", cardReaderObs.wrap("create", err)
	} else if taken {
		return "", ErrCardReaderName
	}
	var maxSort sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(sort_order) FROM card_readers WHERE removed_at IS NULL`).Scan(&maxSort); err != nil {
		return "", cardReaderObs.wrap("create", err)
	}
	sort := 0
	if maxSort.Valid {
		sort = int(maxSort.Int64) + 1
	}
	if id != "" {
		_, err = tx.ExecContext(ctx, `UPDATE card_readers SET name=?, plugin_id=?, kind=?, transport=?, device_serial=NULL, bound_at=NULL, source=?, sort_order=?, unattended_default=0, removed_at=NULL, updated_at=datetime('now') WHERE id=?`,
			name, in.PluginID, in.Kind, in.Transport, in.Source, sort, id)
	} else {
		id = uuid.NewString()
		_, err = tx.ExecContext(ctx, `INSERT INTO card_readers(id,name,method_key,plugin_id,reader_ref,kind,transport,source,sort_order) VALUES(?,?,?,?,?,?,?,?,?)`,
			id, name, in.MethodKey, in.PluginID, in.ReaderRef, in.Kind, in.Transport, in.Source, sort)
	}
	if err != nil {
		return "", cardReaderObs.wrap("create", err)
	}
	return id, nil
}

// Rename changes the display name.
func (r *CardReaderRepo) Rename(ctx context.Context, id, name string) error {
	name, ok := cleanReaderName(name)
	if !ok {
		return ErrCardReaderName
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return cardReaderObs.wrap("rename", err)
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM card_readers WHERE id = ? AND removed_at IS NULL`, id).Scan(&n); err != nil {
		return cardReaderObs.wrap("rename", err)
	}
	if n == 0 {
		return ErrCardReaderNotFound
	}
	if taken, err := nameTaken(ctx, tx, name, id); err != nil {
		return cardReaderObs.wrap("rename", err)
	} else if taken {
		return ErrCardReaderName
	}
	if _, err := tx.ExecContext(ctx, `UPDATE card_readers SET name=?, updated_at=datetime('now') WHERE id=?`, name, id); err != nil {
		return cardReaderObs.wrap("rename", err)
	}
	return tx.Commit()
}

// Move shifts a reader one place up (-1) or down (+1) in picker order.
func (r *CardReaderRepo) Move(ctx context.Context, id string, dir int) error {
	if dir != -1 && dir != 1 {
		return fmt.Errorf("card reader move: direction must be -1 or 1")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return cardReaderObs.wrap("move", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM card_readers WHERE removed_at IS NULL ORDER BY sort_order, created_at, id`)
	if err != nil {
		return cardReaderObs.wrap("move", err)
	}
	var ids []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			return cardReaderObs.wrap("move", err)
		}
		ids = append(ids, s)
	}
	rows.Close()
	at := -1
	for i, s := range ids {
		if s == id {
			at = i
		}
	}
	if at < 0 {
		return ErrCardReaderNotFound
	}
	to := at + dir
	if to < 0 || to >= len(ids) {
		return nil
	}
	ids[at], ids[to] = ids[to], ids[at]
	for i, s := range ids { // renumber densely: stable order after any history of edits
		if _, err := tx.ExecContext(ctx, `UPDATE card_readers SET sort_order=? WHERE id=?`, i, s); err != nil {
			return cardReaderObs.wrap("move", err)
		}
	}
	return tx.Commit()
}

// SetUnattendedDefault marks (on=true) the one reader a self-order/kiosk sale
// uses, moving it off any other row, or clears it. A tap_to_pay reader is
// never eligible (the self-order kiosk never shows it), nor is an sdk reader
// (no kiosk row in v1, ADR-0205 §3) or an external one (needs a cashier).
func (r *CardReaderRepo) SetUnattendedDefault(ctx context.Context, id string, on bool) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return cardReaderObs.wrap("unattended", err)
	}
	defer tx.Rollback()
	var kind string
	err = tx.QueryRowContext(ctx, `SELECT kind FROM card_readers WHERE id=? AND removed_at IS NULL`, id).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrCardReaderNotFound
	}
	if err != nil {
		return cardReaderObs.wrap("unattended", err)
	}
	if on {
		if !CardReaderUnattendedAllowed(kind) {
			return ErrCardReaderKind
		}
		if _, err := tx.ExecContext(ctx, `UPDATE card_readers SET unattended_default=0 WHERE unattended_default=1`); err != nil {
			return cardReaderObs.wrap("unattended", err)
		}
	}
	v := 0
	if on {
		v = 1
	}
	if _, err := tx.ExecContext(ctx, `UPDATE card_readers SET unattended_default=?, updated_at=datetime('now') WHERE id=?`, v, id); err != nil {
		return cardReaderObs.wrap("unattended", err)
	}
	return tx.Commit()
}

// TouchLastUsed stamps the reader as the newest used on this till (picker
// default). Called on an approved authorize only.
func (r *CardReaderRepo) TouchLastUsed(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE card_readers SET last_used_at = strftime('%Y-%m-%dT%H:%M:%f','now') WHERE id = ?`, id)
	if err != nil {
		return cardReaderObs.wrap("touch_last_used", err)
	}
	return nil
}

// SoftRemove removes a reader. When its reader_ref equals the current
// register-scoped legacy `*_reader_id` setting of its plugin, the same
// transaction clears that setting (otherwise a till with no registry rows
// would fall back to the setting and keep charging the reader the manager
// just removed); cleared reports whether it did.
func (r *CardReaderRepo) SoftRemove(ctx context.Context, id string) (cleared bool, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, cardReaderObs.wrap("remove", err)
	}
	defer tx.Rollback()
	c, err := scanCardReader(tx.QueryRowContext(ctx, `SELECT `+cardReaderCols+` FROM card_readers WHERE id = ? AND removed_at IS NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrCardReaderNotFound
	}
	if err != nil {
		return false, cardReaderObs.wrap("remove", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE card_readers SET removed_at = datetime('now'), unattended_default = 0, updated_at = datetime('now') WHERE id = ?`, id); err != nil {
		return false, cardReaderObs.wrap("remove", err)
	}
	// ut-docs#6538: a removed reader must stop authenticating in the same step.
	if err := revokeReaderCredentialTx(ctx, tx, id); err != nil {
		return false, cardReaderObs.wrap("remove", err)
	}
	srows, err := tx.QueryContext(ctx, `SELECT id, key, value_json FROM plugin_settings WHERE plugin_id = ? AND scope = 'register' AND key LIKE '%\_reader\_id' ESCAPE '\'`, c.PluginID)
	if err != nil {
		return false, cardReaderObs.wrap("remove", err)
	}
	type legacy struct{ id, key, val string }
	var ls []legacy
	for srows.Next() {
		var l legacy
		if err := srows.Scan(&l.id, &l.key, &l.val); err != nil {
			srows.Close()
			return false, cardReaderObs.wrap("remove", err)
		}
		ls = append(ls, l)
	}
	srows.Close()
	for _, l := range ls {
		if legacyReaderValue(openLegacyValue(ctx, c.PluginID, l.key, l.val)) != c.ReaderRef {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_settings WHERE id = ?`, l.id); err != nil {
			return false, cardReaderObs.wrap("remove", err)
		}
		cleared = true
	}
	if err := tx.Commit(); err != nil {
		return false, cardReaderObs.wrap("remove", err)
	}
	if cleared && r.onSettingsChanged != nil {
		r.onSettingsChanged()
	}
	return cleared, nil
}

// LegacySettingKey returns the register-scoped `*_reader_id` plugin setting
// whose current value equals the reader's ref ("" when none), so the manager
// page can word the removal confirm honestly.
func (r *CardReaderRepo) LegacySettingKey(ctx context.Context, c CardReader) (string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT key, value_json FROM plugin_settings WHERE plugin_id = ? AND scope = 'register' AND key LIKE '%\_reader\_id' ESCAPE '\'`, c.PluginID)
	if err != nil {
		return "", cardReaderObs.wrap("legacy_key", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return "", cardReaderObs.wrap("legacy_key", err)
		}
		if legacyReaderValue(openLegacyValue(ctx, c.PluginID, k, v)) == c.ReaderRef {
			return k, nil
		}
	}
	return "", rows.Err()
}

// legacyReaderValue decodes a plugin_settings.value_json holding a reader id
// (a JSON string; a bare value is tolerated).
func legacyReaderValue(valueJSON string) string {
	var s string
	if err := json.Unmarshal([]byte(valueJSON), &s); err == nil {
		return strings.TrimSpace(s)
	}
	return strings.Trim(strings.TrimSpace(valueJSON), `"`)
}

// MigrateLegacyReaderSettings imports each active payment plugin's non-empty
// register-scoped `*_reader_id` setting as a registry row (source 'migrated').
// Idempotent: the unique (method_key, reader_ref) index is the marker, and a
// soft-removed row blocks re-creation. The setting itself is never touched.
// Run after every plugin sync, so install order does not matter. Returns the
// number of rows created.
func (r *CardReaderRepo) MigrateLegacyReaderSettings(ctx context.Context) (int, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT s.plugin_id, s.key, s.value_json, p.name
FROM plugin_settings s
JOIN plugins p ON p.id = s.plugin_id AND p.is_active = 1
WHERE s.scope = 'register' AND s.key LIKE '%\_reader\_id' ESCAPE '\'
ORDER BY s.plugin_id, s.key`)
	if err != nil {
		return 0, cardReaderObs.wrap("migrate_legacy", err)
	}
	type cand struct{ pluginID, key, val, pname string }
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.pluginID, &c.key, &c.val, &c.pname); err != nil {
			rows.Close()
			return 0, cardReaderObs.wrap("migrate_legacy", err)
		}
		cands = append(cands, c)
	}
	rows.Close()
	created := 0
	for _, c := range cands {
		ref := legacyReaderValue(openLegacyValue(ctx, c.pluginID, c.key, c.val))
		if ref == "" {
			continue
		}
		if !ValidCardReaderRef(ref) {
			logging.L().Warnf("card readers: not importing %s/%s: the saved reader id is not a valid reader id", c.pluginID, c.key)
			continue
		}
		method, ok, err := r.methodForLegacy(ctx, c.pluginID, c.key)
		if err != nil {
			return created, err
		}
		if !ok {
			continue
		}
		n, err := r.insertMigrated(ctx, method, c.pluginID, ref, c.pname)
		if err != nil {
			return created, err
		}
		created += n
	}
	return created, nil
}

func openLegacyValue(ctx context.Context, pluginID, key, stored string) string {
	v, _ := openStoredSettingValue(ctx, pluginID, key, stored)
	return v
}

// methodForLegacy picks the plugin's payment entry for a `<prefix>_reader_id`
// setting: the active entry keyed <prefix>, else the plugin's first active one.
func (r *CardReaderRepo) methodForLegacy(ctx context.Context, pluginID, key string) (string, bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT key FROM plugin_entries WHERE plugin_id = ? AND type = 'payment' AND is_active = 1 ORDER BY sort_order, key`, pluginID)
	if err != nil {
		return "", false, cardReaderObs.wrap("migrate_legacy", err)
	}
	defer rows.Close()
	prefix := strings.TrimSuffix(key, "_reader_id")
	first := ""
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return "", false, cardReaderObs.wrap("migrate_legacy", err)
		}
		if k == prefix {
			return k, true, nil
		}
		if first == "" {
			first = k
		}
	}
	return first, first != "", rows.Err()
}

func (r *CardReaderRepo) insertMigrated(ctx context.Context, method, pluginID, ref, pluginName string) (int, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, cardReaderObs.wrap("migrate_legacy", err)
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM card_readers WHERE method_key = ? AND reader_ref = ?`, method, ref).Scan(&exists); err != nil {
		return 0, cardReaderObs.wrap("migrate_legacy", err)
	}
	if exists > 0 {
		return 0, nil // already imported (or removed on purpose)
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM card_readers WHERE removed_at IS NULL`).Scan(&active); err != nil {
		return 0, cardReaderObs.wrap("migrate_legacy", err)
	}
	if active >= MaxActiveCardReaders {
		return 0, nil
	}
	base, ok := cleanReaderName(pluginName)
	if !ok {
		base = method
		if base, ok = cleanReaderName(base); !ok {
			base = "Reader"
		}
	}
	name := base
	for i := 2; ; i++ {
		taken, err := nameTaken(ctx, tx, name, "")
		if err != nil {
			return 0, cardReaderObs.wrap("migrate_legacy", err)
		}
		if !taken {
			break
		}
		suffix := fmt.Sprintf(" %d", i)
		rs := []rune(base)
		if len(rs)+len(suffix) > MaxCardReaderNameLen {
			rs = rs[:MaxCardReaderNameLen-len(suffix)]
		}
		name = string(rs) + suffix
	}
	var maxSort sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(sort_order) FROM card_readers WHERE removed_at IS NULL`).Scan(&maxSort); err != nil {
		return 0, cardReaderObs.wrap("migrate_legacy", err)
	}
	sort := 0
	if maxSort.Valid {
		sort = int(maxSort.Int64) + 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO card_readers(id,name,method_key,plugin_id,reader_ref,kind,transport,source,sort_order) VALUES(?,?,?,?,?,?,?,?,?)`,
		uuid.NewString(), name, method, pluginID, ref, CardReaderKindCloud, CardReaderTransportInternet, CardReaderSourceMigrated, sort); err != nil {
		return 0, cardReaderObs.wrap("migrate_legacy", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, cardReaderObs.wrap("migrate_legacy", err)
	}
	return 1, nil
}
