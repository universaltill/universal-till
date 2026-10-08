# Review — plugin-blob thumbnails on suggestion candidates (ut-docs#3957)

Date: 2026-10-08 · Lane: `lane:cloud-24` · Built by Opus 5.5, reviewed by Fable (independent subagent).

## What shipped

- `pluginview`: a suggestion candidate may carry `thumbnail`, the name of a
  blob in the answering plugin's own store. The name must be a legal blob
  name (`plugins.ValidBlobName`, `[a-z0-9._-]{1,128}`, no Windows aliases).
  An explicit `""`, a path, a URL or a non-string refuses the document.
- `plugins.OpenBlobImage`: opens one committed blob of one plugin for core
  to serve. It validates the id and name as the `blob_*` host functions do,
  keeps the path directly inside the store, refuses symlinks and
  non-regular files (Lstat, then `SameFile` after Open), caps the size and
  sniffs the type (JPEG, PNG or WebP only; never SVG or HTML).
- Identify seam (`internal/pages/plugin_identify.go`): when the plugin is
  active, holds `blob:own` and the blob checks out, the button shows the
  thumbnail through a core-signed URL
  (`GET /api/pos/identify/plugin/thumb?p=&n=&s=`, HMAC-SHA256 with a
  per-process key). Otherwise it falls back to the catalog photo, or shows
  no image. The route re-checks the signature, active state, `blob:own`,
  type and size on every hit, and every refusal is a 404. Responses carry
  `nosniff`, `default-src 'none'; sandbox` and `private, no-store`. The
  route is behind the session and denied in demo mode.
- Docs: ut-docs `reference/plugin-views.md`.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The test counted thumbnails but didn't check which blob it was | Fixed: asserts `n=oat.png` and the plugin id. The FOREIGN case now has a comment |
| 2 | minor | Thumbnail loads extend the session (unlike timer polls) | Accepted: `backgroundPollPaths` is for timer GETs, and a thumbnail load follows directly from the operator's capture |
| 3 | minor | The blob is sniffed twice (at render and at serve) | Accepted: bounded by 20 candidates, and it avoids broken-image icons |
| 4 | nit | A signed URL has no expiry until restart | Accepted: the route re-checks everything, and the docs state it |
| 5 | nit | Manual copy instead of `http.ServeContent` | Accepted: the inode is pinned and blobs are only replaced by rename |
| 6 | nit | Operator help not touched | Accepted: no operator step changes (the image is just a different picture) |

## Verification

- TDD/mutation, run by both author and reviewer, then restored:
  - Dropping the HMAC check fails "forged/no signature".
  - Forcing `blob:own` allowed fails the revocation and foreign-plugin cases.
  - Removing the template branch fails the thumbnail count.
  - Widening the type allow-list fails the svg/html/text refusals.
- `go build ./...`, `go vet`, full `go test ./...`. `internal/plugins` needs
  `-timeout 25m` (575 s, as in CI).
- Every ci.yml build guard I could run passed. `deadcode-baseline` reports 2
  env-only findings that are identical on `main`, because there are no GTK
  headers here. golangci-lint can't run in this container (its binary is
  built with go1.25), so CI covers it.
- UX: the image reuses `.ai-identify-results .ai-match img` (2.5rem,
  `object-fit: cover`, logical properties). There is no new string and no
  new modal. I didn't drive it on a device: the template is rendered by the
  seam tests.

Verdict: safe to merge.
