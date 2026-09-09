package settings

import (
	"context"
	"strings"

	"github.com/universaltill/universal-till/internal/print"
)

const (
	keyPrinterCharset = "printer.charset"
	// keyPrinterCharsetAdopted records that the adoption pass below has
	// already run FOR THIS VERSION, so it never second-guesses the operator
	// twice at the version it already ran at. Versioned in the value, not
	// the key, exactly as designed: currentAdoptionVersion bumped from "v1"
	// to "v2" in ut-docs#1733, which is the "future adoption pass" that
	// comment always meant — DefaultCharset started resolving 7 more
	// locales to a real code page (win1250/win1257/win1253) that used to
	// resolve to "utf8", and every till in those markets that had already
	// completed a v1 pass (and is therefore still sitting on the "utf8" a
	// v1 pass left it on, since v1 never had anything better to offer them)
	// would otherwise never be re-evaluated and would keep printing mojibake
	// forever — precisely the "#1243 closed while the defect was still on
	// real receipts" failure this mechanism exists to prevent (independent
	// review finding, ut-docs#1733).
	//
	// Bumped again "v2" -> "v3" in ut-docs#1775: DefaultCharset now resolves
	// win1254 for a Turkish-language EUR/GBP store, which used to fall
	// through to "utf8" under v2 for the exact same reason the v1->v2 bump
	// exists above — a till that already spent its v2 pass with nothing
	// better to offer a Turkish locale is sitting on "utf8" and would never
	// be re-evaluated without this bump.
	keyPrinterCharsetAdopted = "printer.charset.adopted"
	currentAdoptionVersion   = "v3"
)

// AdoptDefaultPrinterCharset switches a till that is still carrying the
// never-chosen "utf8" printer charset onto the code page its currency and
// locale actually need — once per currentAdoptionVersion, at boot.
//
// ut-docs#1728. Resolving the default at READ time (see pages.printerConfig)
// only helps a till with nothing stored, and almost no real till is in that
// state: POST /api/settings/printer writes all seven printer keys on every
// save, defaulting an absent charset field to "utf8", so any shop that ever
// opened that page to enter its printer's address — which is the only way to
// get printing working at all — has an explicit "utf8" row from then on.
// Without this pass, the fix would ship and every till in the field would
// keep printing "âÎ¬2.50", which is precisely how ut-docs#1243 came to be
// closed while the defect it was raised for was still on real receipts.
//
// Deliberately conservative:
//
//   - It runs once PER VERSION. The marker is written whatever the
//     outcome, so within one version an operator who genuinely wants
//     UTF-8 sets it after this and is not overridden again at that same
//     version. A version bump (currentAdoptionVersion, e.g. ut-docs#1733
//     teaching DefaultCharset seven more locales) deliberately re-opens
//     the question for any till still sitting on "utf8" — there is no way
//     to distinguish "never touched" from "operator deliberately chose
//     utf8" (both are the same stored value), so a till whose owner
//     re-picked utf8 after an earlier version's pass can see one more
//     automatic move when a version bump ships. The alternative — a till
//     in a newly-covered market staying on mojibake forever because an
//     earlier version's pass already spent the marker before that
//     market's code page existed — is worse, and is exactly what
//     ut-docs#1733 fixed here (see that card for the concrete case: every
//     already-set-up Greek/Croatian/Slovenian/Slovak/Estonian/Latvian/
//     Lithuanian till was frozen on "utf8" by a v1 pass that had nothing
//     better to offer it, and this file's own versioning mechanism —
//     documented but unused until now — is what makes catching that up
//     possible without a second dead key).
//   - It only ever moves "utf8" (or nothing) — an explicit "ascii" or
//     "cp858" is a real choice about real hardware and is left untouched,
//     per the project rule that installing defaults must not clobber an
//     explicit override.
//   - It does nothing until the setup wizard has actually completed. This
//     gate is on "setup.completed", NOT on the currency being empty: the
//     schema SEEDS store.currency = 'GBP' (001_init.sql) and boot writes
//     store.locale = "en-US" from the config default via SaveRuntimeConfig
//     immediately before this runs, so on a brand-new till anywhere in the
//     world the currency is never empty and an emptiness check can never
//     fire. An earlier draft used one; the independent review showed it
//     would burn the single-shot marker at first boot on placeholder data,
//     so a shop that set up as US/USD, configured its printer, then changed
//     country to Germany would keep printing mojibake forever with its one
//     repair already spent. Returning early WITHOUT the marker lets the
//     next boot after setup do the real work instead.
//
// Returns the old and new values when it changes something, so the caller can
// write the audit row: every other write to printer.charset goes through
// settingsAudit's "printer_settings_changed" entry, and a silent boot-time
// mutation of a printing setting is a traceability gap in a product carrying
// DSFinV-K/TSE obligations (independent review, ut-docs#1728 finding 5).
//
// Best-effort by design: a settings read/write failure here must never stop
// the till booting, so errors are returned for logging and nothing else.
func (s *Store) AdoptDefaultPrinterCharset(ctx context.Context) (from, to string, changed bool, err error) {
	if v, _, gErr := s.Get(ctx, keyPrinterCharsetAdopted); gErr != nil {
		return "", "", false, gErr
	} else if strings.TrimSpace(v) == currentAdoptionVersion {
		return "", "", false, nil
	}

	// Not set up yet — no marker, so this is retried on the next boot, once
	// store.currency/store.locale describe a real shop rather than the
	// schema's seeded placeholders.
	setupDone, _, err := s.Get(ctx, "setup.completed")
	if err != nil {
		return "", "", false, err
	}
	if strings.TrimSpace(setupDone) != "true" {
		return "", "", false, nil
	}

	currency, _, err := s.Get(ctx, "store.currency")
	if err != nil {
		return "", "", false, err
	}
	locale, _, err := s.Get(ctx, "store.locale")
	if err != nil {
		return "", "", false, err
	}

	current, _, err := s.Get(ctx, keyPrinterCharset)
	if err != nil {
		return "", "", false, err
	}
	current = strings.TrimSpace(current)

	want := print.DefaultCharset(currency, locale)
	// Only the never-chosen value moves. "" is left alone too: nothing is
	// stored, so printerConfig's read-time default already resolves it, and
	// writing a row here would only turn a live default into a frozen one.
	if current == "utf8" && want != current {
		if err := s.Set(ctx, keyPrinterCharset, want); err != nil {
			return "", "", false, err
		}
		from, to, changed = current, want, true
	}
	if err := s.Set(ctx, keyPrinterCharsetAdopted, currentAdoptionVersion); err != nil {
		return from, to, changed, err
	}
	return from, to, changed, nil
}
