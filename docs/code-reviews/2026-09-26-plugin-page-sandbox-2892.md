# Review — plugin page isolation (ut-docs#2892)

**Why (p1):** a plugin's page `content/index.html` was injected as raw
`template.HTML` into the till's own page — same origin, no CSP — so a signed
plugin's `<script>` ran with a manager's session (could move its own
setting-bound grant, #2899, or self-grant `net:*`).

**Change:** the static page now renders inside `<iframe sandbox srcdoc>` with
an **empty sandbox token list** (no scripts, opaque origin, no forms, no
top navigation); srcdoc is attribute-escaped (passed as a plain string — the
type is load-bearing, pinned by a test); the frame carries the till
stylesheet, theme, `lang`, `--ui-scale`, and `dir="auto"` on its body. Every
plugin page route sends `Content-Security-Policy: object-src 'none';
frame-src 'self' about:`. **`/plugin-icons`** (auth-exempt) now serves
image types only (`.png .jpg .jpeg .webp .gif .ico .svg`) with an explicit
Content-Type, `nosniff` and `default-src 'none'; style-src 'unsafe-inline';
sandbox`, via `ServeContent` (no directory redirects), id/version validated
with `plugins.ValidatePluginID/Version`. ut-cloud: marketplace lint warns
reviewers about `<script`, `on*=` and `javascript:` in `content/index.html`
(advisory, never fails the gate). ut-docs: "Page content is sandboxed".
Global CSP for the till's own UI split to #2913.
Author: Opus 5.5. Reviewer: Fable.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | `/plugin-icons` served ANY bundle file (auth-exempt): a plugin's `docs/page.html` or scripted `.svg` ran on the till origin when opened | fixed (image allowlist, nosniff, sandbox CSP, validated ids); tests for .html/.js 404, scripted SVG sandboxed, traversal/odd ids 404 |
| 2 | minor | CSP only on the index.html branch | hoisted to every plugin page branch; test |
| 3 | minor | `frame-src` without `about:` (WebKitGTK) | added |
| 4 | minor | inside the frame, links/meta refresh navigate within the (still sandboxed) frame — an external page shown in till chrome | accepted, documented for plugin authors |
| 5 | minor | lint misses `<iframe`, `<meta http-equiv`, `<form`, `<object>` | accepted (advisory) |
| 6 | UX | an fa till mirrored an English docs page | `dir="auto"` on the frame body |

**Checked, no issue (reviewer):** srcdoc can't break out (`&quot;`,
`</iframe>`, NUL); no other plugin-provided `template.HTML` path (bundle
fields escaped, labels via T, icons from a closed set, store descriptions
escaped, helpLink validated); `/plugin/` routes stay session-gated; ut-cloud
lint bounded (1 MiB, zip-bomb caps), never enters Problems.

**Verification:** build, vet, gofmt, `go test -run 'Plugin|Icon'
./internal/pages/`, ut-cloud `go test ./internal/downloads/`, guards i18n,
core-neutral, data-access; screenshots of the tax-uk docs page at 1024×600
and 360 px, en + fa, looked at (renders in theme, frame scrolls; touch
scrolling not verified on hardware); `make docs-shots` (no manual screenshot
covers /plugin/*; surface hash refreshed).

**Verdict:** safe to merge.
