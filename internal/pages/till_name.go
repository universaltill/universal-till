package pages

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// maxTillNameRunes is the longest till display name (ut-docs#396): the
// Settings field's maxlength="60", the Settings handler's server-side
// truncation, and the cloud rename_till hook's refusal (ut-docs#3272) all
// use this one limit.
const maxTillNameRunes = 60

// truncateTillName is the Settings form's rule: trimmed, cut to
// maxTillNameRunes (the field's maxlength already stops a browser there).
func truncateTillName(raw string) string {
	name := strings.TrimSpace(raw)
	if rs := []rune(name); len(rs) > maxTillNameRunes {
		name = string(rs[:maxTillNameRunes])
	}
	return name
}

// validateTillName is the strict rule for a name that arrives without a
// form (the cloud's rename_till, ut-docs#3272): trimmed, then refused —
// never truncated — when empty, longer than maxTillNameRunes, or holding
// any control character, so the name the till stores and reports back is
// exactly the one it was sent.
func validateTillName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", errors.New("till name is empty")
	}
	if !utf8.ValidString(name) {
		return "", errors.New("till name is not valid UTF-8")
	}
	if n := utf8.RuneCountInString(name); n > maxTillNameRunes {
		return "", fmt.Errorf("till name is %d characters; the limit is %d", n, maxTillNameRunes)
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", errors.New("till name contains a control character")
	}
	return name, nil
}

// deviceNameOrDefault is the Settings till-name field's value: this till's
// own name as it reports it to the cloud (enroll.DeviceName) — till.name on
// the main till, sync.till_name on a joined till, never the main till's
// name there (ut-docs#3292) — or the translated default when it has none.
func deviceNameOrDefault(ctx context.Context, d *common.Deps, locale string) string {
	if name := enroll.DeviceName(ctx, d.Settings); name != "" {
		return name
	}
	return httpx.T(locale, "setup.till_name.default")
}

// errTillNameTaken is tillNameTaken's refusal, shown to the operator as
// sync.error.name_taken — the pairing-time message for the same rule.
var errTillNameTaken = errors.New("another till in this shop already uses this name")

// tillNameTaken reports whether renaming THIS till to name would give it a
// name another till in the shop already uses (ut-docs#3308), with the
// pairing-time fold (TillsRepo.NameTaken: trimmed, case-insensitive,
// ut-docs#1264). The other tills are, on the main till, its enrolled
// tills; on a joined till, its synced tills roster minus its own row
// (sync.till_id) plus the main till's name (till.name there, or the
// default the main till reports, read in the default locale as enrolment
// does). Local data only, so it works offline.
//
// A case change of this till's own current name is never "taken": the
// name is still its own, even if a duplicate already exists from before
// this rule.
func tillNameTaken(ctx context.Context, d *common.Deps, name string) (bool, error) {
	if strings.EqualFold(strings.TrimSpace(enroll.DeviceName(ctx, d.Settings)), strings.TrimSpace(name)) {
		return false, nil
	}
	exceptID := ""
	if tillFollowsMain(ctx, d) {
		if strings.EqualFold(tillNameOrDefault(ctx, d, httpx.DefaultLocale()), strings.TrimSpace(name)) {
			return true, nil
		}
		exceptID, _, _ = d.Settings.Get(ctx, "sync.till_id")
		exceptID = strings.TrimSpace(exceptID)
	}
	return data.NewTillsRepo(d.Db).NameTakenExcept(ctx, name, exceptID)
}
