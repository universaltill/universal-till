package data

// Per-country settings (ut-docs#635 → ut-docs#659): the per-jurisdiction
// defaults a shop's country points at — currency, tax, and the archive
// retention window — held as admin-manageable data rather than the
// compile-time slice in internal/pages/setup_page.go.
//
// Not to be confused with SettingsRepo, which owns THIS shop's own key/value
// settings (including which country it is in). One shop, many countries
// available; the shop's choice lives over there.
//
// Two rules here exist because a UI cannot be trusted to hold them:
//
//   - The retention floor is enforced on every write, so no API caller,
//     plugin or future code path can set a shop below it.
//   - Deleting a BUILTIN country restores its shipped defaults instead of
//     removing the row. "Delete" must always land on a state the wizard and
//     the purge gate can still read a legal retention value out of; a missing
//     row for a real jurisdiction is not such a state.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// GlobalArchiveMinDays is ADR-0040's single global retention floor (10 years),
// applied uniformly to every country.
//
// It is deliberately NOT per-jurisdiction. ADR-0040 states it "does not encode
// per-jurisdiction retention minimums (they differ, e.g. DE vs UK)" and that no
// jurisdiction-specific compliance claim is made in code or UI copy — that is
// the product owner's and legal's call. Shipping different statutory values per
// country needs an ADR-0040 amendment first (ut-docs#659).
//
// Admins may RAISE a country's archive_min_days; nothing may lower it below
// this floor.
const GlobalArchiveMinDays int64 = 3650

const (
	// maxCountryCodeLen bounds the code, which doubles as an HTML element id
	// on the admin page. "OTHER" (5) is the longest one we ship.
	maxCountryCodeLen = 8
	// maxTaxRateBP caps a rate at 100%. Nothing legitimate exceeds it, and an
	// unbounded rate silently corrupts every price calculation downstream.
	maxTaxRateBP int64 = 10000
)

// CountrySetting is one jurisdiction's defaults.
type CountrySetting struct {
	Code           string // ISO-3166-1 alpha-2, upper-case; "OTHER" is the wizard's catch-all
	NameKey        string // locale key the UI renders the name by (setup.country.*)
	Currency       string
	CurrencySymbol string // display only; blank falls back to the currency code
	TaxRateBP      int64  // basis points, not percent
	TaxInclusive   bool
	ArchiveMinDays int64
	IsBuiltin      bool
	UpdatedAt      string
}

// builtinCountryDefaults is what Delete restores a builtin country to. It
// mirrors migration 041's seed exactly; TestBuiltinDefaultsMatchMigrationSeed
// asserts the two cannot drift, so this stays a restore source rather than a
// second source of truth.
var builtinCountryDefaults = []CountrySetting{
	{Code: "GB", NameKey: "setup.country.gb", Currency: "GBP", CurrencySymbol: "£", TaxRateBP: 2000, TaxInclusive: true},
	{Code: "IR", NameKey: "setup.country.ir", Currency: "IRT", TaxRateBP: 1000, TaxInclusive: true},
	{Code: "US", NameKey: "setup.country.us", Currency: "USD", CurrencySymbol: "$", TaxRateBP: 0, TaxInclusive: false},
	{Code: "DE", NameKey: "setup.country.de", Currency: "EUR", CurrencySymbol: "€", TaxRateBP: 1900, TaxInclusive: true},
	{Code: "FR", NameKey: "setup.country.fr", Currency: "EUR", CurrencySymbol: "€", TaxRateBP: 2000, TaxInclusive: true},
	{Code: "ES", NameKey: "setup.country.es", Currency: "EUR", CurrencySymbol: "€", TaxRateBP: 2100, TaxInclusive: true},
	{Code: "IT", NameKey: "setup.country.it", Currency: "EUR", CurrencySymbol: "€", TaxRateBP: 2200, TaxInclusive: true},
	{Code: "NL", NameKey: "setup.country.nl", Currency: "EUR", CurrencySymbol: "€", TaxRateBP: 2100, TaxInclusive: true},
	{Code: "TR", NameKey: "setup.country.tr", Currency: "TRY", CurrencySymbol: "₺", TaxRateBP: 2000, TaxInclusive: true},
	{Code: "AE", NameKey: "setup.country.ae", Currency: "AED", TaxRateBP: 500, TaxInclusive: true},
	{Code: "SA", NameKey: "setup.country.sa", Currency: "SAR", TaxRateBP: 1500, TaxInclusive: true},
	{Code: "IN", NameKey: "setup.country.in", Currency: "INR", CurrencySymbol: "₹", TaxRateBP: 1800, TaxInclusive: true},
	{Code: "PK", NameKey: "setup.country.pk", Currency: "PKR", TaxRateBP: 1800, TaxInclusive: true},
	{Code: "OTHER", NameKey: "setup.country.other", Currency: "", TaxRateBP: 0, TaxInclusive: true},
}

// BuiltinCountryDefaults returns the shipped defaults, for callers that need
// the canonical list (tests, the migration-drift guard).
func BuiltinCountryDefaults() []CountrySetting {
	out := make([]CountrySetting, len(builtinCountryDefaults))
	for i, c := range builtinCountryDefaults {
		c.ArchiveMinDays = GlobalArchiveMinDays
		c.IsBuiltin = true
		out[i] = c
	}
	return out
}

func builtinCountryDefault(code string) (CountrySetting, bool) {
	for _, c := range BuiltinCountryDefaults() {
		if c.Code == code {
			return c, true
		}
	}
	return CountrySetting{}, false
}

// CountrySettingsRepo owns all SQL for the country_settings table.
type CountrySettingsRepo struct {
	db *sql.DB
}

func NewCountrySettingsRepo(db *sql.DB) *CountrySettingsRepo {
	return &CountrySettingsRepo{db: db}
}

var countrySettingsObs = newRepoObservability("country_settings")

// normaliseCountryCode makes the code a stable join key with the wizard and
// with settings' KeyCountry, rather than trusting whatever a caller typed.
func normaliseCountryCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

const countrySettingsCols = `code, name_key, currency, currency_symbol, tax_rate_bp, tax_inclusive, archive_min_days, is_builtin, updated_at`

func scanCountrySetting(sc interface{ Scan(...any) error }) (CountrySetting, error) {
	var c CountrySetting
	var inclusive, builtin int
	if err := sc.Scan(&c.Code, &c.NameKey, &c.Currency, &c.CurrencySymbol, &c.TaxRateBP, &inclusive, &c.ArchiveMinDays, &builtin, &c.UpdatedAt); err != nil {
		return CountrySetting{}, err
	}
	c.TaxInclusive = inclusive == 1
	c.IsBuiltin = builtin == 1
	return c, nil
}

// List returns every configured country, ordered by code.
func (r *CountrySettingsRepo) List(ctx context.Context) ([]CountrySetting, error) {
	var err error
	done := countrySettingsObs.trace("list")
	defer func() { done(err) }()

	rows, err := r.db.QueryContext(ctx, `SELECT `+countrySettingsCols+` FROM country_settings ORDER BY code`)
	if err != nil {
		return nil, countrySettingsObs.wrapf("list", "list country settings", err)
	}
	defer rows.Close()
	var out []CountrySetting
	for rows.Next() {
		c, serr := scanCountrySetting(rows)
		if serr != nil {
			err = serr
			return nil, countrySettingsObs.wrapf("list", "scan country setting", err)
		}
		out = append(out, c)
	}
	if err = rows.Err(); err != nil {
		return nil, countrySettingsObs.wrapf("list", "iterate country settings", err)
	}
	return out, nil
}

// Get returns one country by code. The code is normalised, so callers may pass
// whatever the wizard or a settings row gave them.
func (r *CountrySettingsRepo) Get(ctx context.Context, code string) (CountrySetting, bool, error) {
	var err error
	done := countrySettingsObs.trace("get")
	defer func() { done(err) }()

	row := r.db.QueryRowContext(ctx, `SELECT `+countrySettingsCols+` FROM country_settings WHERE code = ?`, normaliseCountryCode(code))
	c, err := scanCountrySetting(row)
	if err == sql.ErrNoRows {
		err = nil
		return CountrySetting{}, false, nil
	}
	if err != nil {
		return CountrySetting{}, false, countrySettingsObs.wrapf("get", "get country setting %s", err, code)
	}
	return c, true, nil
}

// validateCountrySetting holds the rules that must survive any caller. The
// retention floor is the load-bearing one: enforcing it only in the settings
// form would leave every other write path free to drop a shop below it.
func validateCountrySetting(c *CountrySetting) error {
	c.Code = normaliseCountryCode(c.Code)
	if c.Code == "" {
		return fmt.Errorf("country code is required")
	}
	// Codes are A-Z0-9 only. Besides being what a country code is, the admin
	// page uses the code as an HTML element id to bind each row's inputs to
	// its form — a code carrying whitespace or punctuation would silently
	// break that association rather than fail visibly.
	if len(c.Code) > maxCountryCodeLen {
		return fmt.Errorf("country code %q is longer than %d characters", c.Code, maxCountryCodeLen)
	}
	for _, r := range c.Code {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return fmt.Errorf("country code %q must contain only letters and digits", c.Code)
		}
	}
	if c.TaxRateBP < 0 {
		return fmt.Errorf("tax rate must not be negative (got %d bp)", c.TaxRateBP)
	}
	if c.TaxRateBP > maxTaxRateBP {
		return fmt.Errorf("tax rate %d bp is above the %d bp (%d%%) maximum", c.TaxRateBP, maxTaxRateBP, maxTaxRateBP/100)
	}
	if c.ArchiveMinDays < GlobalArchiveMinDays {
		return fmt.Errorf(
			"archive retention for %s must be at least %d days (ADR-0040's global floor); raising it is allowed, lowering it is not",
			c.Code, GlobalArchiveMinDays)
	}
	if c.NameKey == "" {
		if def, ok := builtinCountryDefault(c.Code); ok {
			c.NameKey = def.NameKey
		} else {
			// Operator-created country: render it by its own code rather than
			// inventing a locale key that no locale file would carry.
			c.NameKey = ""
		}
	}
	return nil
}

// Upsert creates or updates a country. is_builtin is NOT caller-controlled: a
// code we ship defaults for stays builtin, and anything else is an
// operator-created country, so a caller cannot mark its own row builtin (which
// would change what Delete does to it).
func (r *CountrySettingsRepo) Upsert(ctx context.Context, c CountrySetting) error {
	var err error
	done := countrySettingsObs.trace("upsert")
	defer func() { done(err) }()

	if err = validateCountrySetting(&c); err != nil {
		return err
	}
	_, isBuiltin := builtinCountryDefault(c.Code)

	inclusive, builtin := 0, 0
	if c.TaxInclusive {
		inclusive = 1
	}
	if isBuiltin {
		builtin = 1
	}
	_, err = r.db.ExecContext(ctx, `
INSERT INTO country_settings (`+countrySettingsCols+`)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(code) DO UPDATE SET
	name_key         = excluded.name_key,
	currency         = excluded.currency,
	currency_symbol  = excluded.currency_symbol,
	tax_rate_bp      = excluded.tax_rate_bp,
	tax_inclusive    = excluded.tax_inclusive,
	archive_min_days = excluded.archive_min_days,
	is_builtin       = excluded.is_builtin,
	updated_at       = excluded.updated_at`,
		c.Code, c.NameKey, c.Currency, c.CurrencySymbol, c.TaxRateBP, inclusive,
		c.ArchiveMinDays, builtin, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return countrySettingsObs.wrapf("upsert", "upsert country setting %s", err, c.Code)
	}
	return nil
}

// Delete removes an operator-created country, but RESTORES a builtin one to
// its shipped defaults rather than removing it (ut-docs#659).
//
// The product owner asked for admin deletion of these configs; taken literally
// that would let a shop delete the row its retention window is read from and
// end up with no configured minimum at all for a real jurisdiction. Restoring
// keeps the capability (the customisation is gone) while guaranteeing the
// purge gate and the wizard always have a value to read.
func (r *CountrySettingsRepo) Delete(ctx context.Context, code string) error {
	var err error
	done := countrySettingsObs.trace("delete")
	defer func() { done(err) }()

	code = normaliseCountryCode(code)
	if def, ok := builtinCountryDefault(code); ok {
		return r.Upsert(ctx, def)
	}
	_, err = r.db.ExecContext(ctx, `DELETE FROM country_settings WHERE code = ?`, code)
	if err != nil {
		return countrySettingsObs.wrapf("delete", "delete country setting %s", err, code)
	}
	return nil
}
