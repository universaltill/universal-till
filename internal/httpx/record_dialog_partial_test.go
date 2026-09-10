package httpx

import (
	"html/template"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/web"
)

// parsePartialWithPage parses one REAL shared partial (from the embedded
// web FS, so the test exercises the shipped file, not a copy) together with
// a stub "page" that supplies the {{ define }} slots the partial owes.
func parsePartialWithPage(t *testing.T, partial, page string) *template.Template {
	t.Helper()
	src, err := web.FS.ReadFile(partial)
	if err != nil {
		t.Fatalf("read %s: %v", partial, err)
	}
	tpl := template.New("t").Funcs(FuncsFor("en"))
	if _, err := tpl.Parse(string(src)); err != nil {
		t.Fatalf("parse %s: %v", partial, err)
	}
	if page != "" {
		if _, err := tpl.Parse(page); err != nil {
			t.Fatalf("parse page stub: %v", err)
		}
	}
	return tpl
}

const recordDialogSlots = `{{ define "record_dialog_fields" }}<form id="thing-form" method="post"></form>{{ end }}` +
	`{{ define "record_dialog_destructive" }}{{ end }}`

func without(m map[string]any, key string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if k != key {
			out[k] = v
		}
	}
	return out
}

// ut-docs#2010 review (B2): html/template renders a MISSING map key as the
// empty string, so a misspelled dict key (`"formId"` for `"formID"`) used
// to ship a Save button with form="" — document.getElementById("") is null,
// Save does nothing and the discard guard silently disables — at HTTP 200.
// The `req` guard on the partial's first line turns that into an execute
// error that names the key. Pinned against the REAL partial, per key.
func TestRecordDialog_MissingRequiredKeyFailsAtExecute(t *testing.T) {
	tpl := parsePartialWithPage(t, "ui/partials/record_dialog.html", recordDialogSlots)
	full := map[string]any{
		"id":             "thing-dialog",
		"formID":         "thing-form",
		"createTitleKey": "categories.create",
		"editTitleKey":   "categories.edit",
		"createAction":   "/api/things",
	}
	var ok strings.Builder
	if err := tpl.ExecuteTemplate(&ok, "record_dialog", full); err != nil {
		t.Fatalf("complete dict must render: %v", err)
	}
	if !strings.Contains(ok.String(), `form="thing-form"`) {
		t.Fatalf("complete dict did not bind Save to the form:\n%s", ok.String())
	}
	for _, missing := range []string{"id", "formID", "createTitleKey", "editTitleKey", "createAction"} {
		var out strings.Builder
		err := tpl.ExecuteTemplate(&out, "record_dialog", without(full, missing))
		if err == nil {
			t.Errorf("without %q the dialog rendered %d bytes at execute time instead of failing", missing, out.Len())
			continue
		}
		if !strings.Contains(err.Error(), missing) {
			t.Errorf("error for missing %q does not name it: %v", missing, err)
		}
	}
	// A key that is present but EMPTY is the same authoring bug.
	empty := make(map[string]any, len(full))
	for k, v := range full {
		empty[k] = v
	}
	empty["createAction"] = ""
	var out strings.Builder
	if err := tpl.ExecuteTemplate(&out, "record_dialog", empty); err == nil || !strings.Contains(err.Error(), "createAction") {
		t.Errorf(`createAction "" must fail naming the key, got err=%v`, err)
	}
}

// Same guard on the list header: the New button targets `dialogID`, and a
// missing one silently rendered data-record-dialog-open="" — a New button
// that does nothing. emptyTarget stays optional (the no-results row is).
func TestListHeader_MissingRequiredKeyFailsAtExecute(t *testing.T) {
	tpl := parsePartialWithPage(t, "ui/partials/list_header.html", "")
	full := map[string]any{
		"searchID":       "things-search",
		"searchLabelKey": "categories.search_placeholder",
		"filterTarget":   "#things-table .thing-row",
		"newID":          "things-new",
		"newLabelKey":    "categories.new",
		"dialogID":       "thing-dialog",
	}
	var ok strings.Builder
	if err := tpl.ExecuteTemplate(&ok, "list_header", full); err != nil {
		t.Fatalf("complete dict (no emptyTarget) must render: %v", err)
	}
	if !strings.Contains(ok.String(), `data-record-dialog-open="thing-dialog"`) {
		t.Fatalf("New button is not wired to the dialog:\n%s", ok.String())
	}
	for _, missing := range []string{"searchID", "searchLabelKey", "filterTarget", "newID", "newLabelKey", "dialogID"} {
		var out strings.Builder
		err := tpl.ExecuteTemplate(&out, "list_header", without(full, missing))
		if err == nil {
			t.Errorf("without %q the header rendered %d bytes instead of failing", missing, out.Len())
			continue
		}
		if !strings.Contains(err.Error(), missing) {
			t.Errorf("error for missing %q does not name it: %v", missing, err)
		}
	}
}

// ut-docs#2010 review (S5): {{ template "record_dialog_fields" . }} hands
// the slot the DICT, and `$` inside a define binds to that same argument,
// so a slot has no route to the page's own data unless the call site puts
// it in the dict as "root". /users (roles), /registers and /tables
// (location select) all need that; without it they would render an empty
// <select> at HTTP 200. Pins that the idiom works through the real partial.
func TestRecordDialog_SlotReachesPageDataThroughRoot(t *testing.T) {
	slots := `{{ define "record_dialog_fields" }}<form id="thing-form"><select name="location">` +
		`{{ range .root.locations }}<option>{{ . }}</option>{{ end }}</select></form>{{ end }}` +
		`{{ define "record_dialog_destructive" }}{{ end }}`
	tpl := parsePartialWithPage(t, "ui/partials/record_dialog.html", slots)
	d := map[string]any{
		"id":             "thing-dialog",
		"formID":         "thing-form",
		"createTitleKey": "categories.create",
		"editTitleKey":   "categories.edit",
		"createAction":   "/api/things",
		"root":           map[string]any{"locations": []string{"Front", "Back"}},
	}
	var out strings.Builder
	if err := tpl.ExecuteTemplate(&out, "record_dialog", d); err != nil {
		t.Fatalf("execute: %v", err)
	}
	for _, want := range []string{"<option>Front</option>", "<option>Back</option>"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("slot did not see the page data through .root — missing %s:\n%s", want, out.String())
		}
	}
}
