# Review: bulk-import item photos by SKU (ut-docs#6527)

Date: 2026-11-16. Lane: lane:sdk. Authority: card ut-docs#6527, this repo's
`CLAUDE.md`, #3079 (cashier is sale-only), the netaccess/demo rules.
Security sensitive: reads local files named in an uploaded CSV and fetches
URLs from it (file-read and SSRF primitives).

Reviewed independently of the Dev and the Tester, on the uncommitted diff in
the cycle worktree.

## What shipped

- `internal/photoimport` (new): `ParseCSV` (sku + image column, aliases incl.
  German, delimiter sniffing, BOM, Windows-1252), `ResolveInFolder` /
  `ReadRegular` (containment after symlink resolution, regular files only,
  10 MB cap), `OpenZip` / `Read` (in-memory, base-name index, declared and
  actual size caps), `Fetcher` (https only, port 443, no proxy, address check
  in the dialer's `Control` hook, 3 redirects, time and size limits),
  `Run` (one code path for preview and apply, 1 GB per-run byte cap),
  `WriteFailedCSV` (formula-prefix guard).
- `internal/pages/import_photos.go`: `POST /api/import/photos` (catalog
  management only, main till only, one run at a time, audit row on apply,
  linked tills nudged) and `GET /api/import/photos/failed.csv` (owner of the
  run only, 1 h, 8 kept); Import page card; demo-denied; unbounded-route entry.
- `CatalogRepo.ItemsBySKU`; English keys + `en.meta.json`; help topic
  `catalog`; README.

## Pre-flight

Pre-flight: help topic `web/help/en/catalog.md` updated; docs-shots NOT
regenerated (`guard-docs-shots` fails: app surface + en/catalog changed;
must run `make docs-shots` in the Linux container before the PR, R7);
`reference/` n/a (no reference page covers import); `README.md` updated;
new `en.json` keys have `en.meta.json` entries (English only, ADR-0160);
no plugin type, host function, manifest field, ADR, plan or price change;
follow-ups listed below for cards.

## Method

Read every non-test line against the card's security list. Ran
`go build ./...`, `go vet`, `golangci-lint` (0 issues), `go test ./internal/...`
(all green), guards: data-access, i18n, i18n-meta, help-topics, rtl,
compliance, competitor, mail, core-neutral, fixed-dates, native-confirm,
showmodal, help-terms, help-breadcrumbs, netaccess, demo-env (all pass) and
docs-shots (fails, R7). Re-verified the Dev's SSRF claim by disabling the
`Control` address check: `TestFetch_DialGuardRefusesPrivateAddress` fails
("dial to a loopback address was allowed"); restored, passes. Every fix
below was test-first: the new test failed before the fix, passes after.

## Findings

| # | Finding | Severity | Status |
|---|---|---|---|
| R1 | A relative Photos folder (`.`, `photos`) was accepted and resolved against the process's working directory (the repo's recurring cwd-relative-path bug), so `.` read files next to the binary | should-fix (security/correctness) | fixed: folder must be absolute (`filepath.IsAbs`), else `photoimport.error.folder`, whose text now asks for the full path; help says so. `TestPhotoImport_RelativeFolderRefused` |
| R2 | SSRF: IPv6 forms that tunnel to IPv4 passed `BlockedIP`: NAT64 `64:ff9b::7f00:1` (loopback), `64:ff9b::a9fe:a9fe` (metadata), 6to4 `2002:c0a8:101::1`, Teredo | should-fix (security) | fixed: NAT64 / 6to4 judged by the embedded IPv4, local-use NAT64 and Teredo refused. `TestBlockedIP_EmbeddedIPv4` |
| R3 | ZIP + CSV ref: two entries with the same base name in different folders (`x/a.png`, `y/a.png`) silently resolved to the last one indexed, so the wrong photo could land on an item | should-fix (correctness) | fixed: duplicate base names tracked, row refused as `duplicate_sku`. `TestZip_SameBaseNameRefIsAmbiguous` |
| R4 | ZIP-only runs numbered rows in Go map order, so "line 3" differed between preview, apply and the failed-rows CSV | low (correctness) | fixed: stems sorted. `TestRowsFromZip_Deterministic` |
| R5 | A CSV ref missing from the uploaded ZIP with no folder reported `no_source` ("no file or ZIP given") although a ZIP was given | low (wrong message) | fixed: `not_found`. `TestRun_ZipRefMissingIsNotFound` |
| R6 | The folder is free text: any catalog manager can point it at any absolute folder the till process can read (e.g. the owner's home Pictures) and import those images as item photos, and the per-row status (`not_found` / `not_image` / `not_regular`) is a file-existence oracle over that folder. Contents of non-images never leak (decode failure only), no listing, nothing outside the folder | medium (privacy, by design of the card) | accepted for v1, follow-up: restrict folder mode to admin or to a folder set once in Settings by an admin; collapse the three statuses into one for folder rows |
| R7 | `guard-docs-shots` fails (surface hash + en/catalog topic changed) | blocker (CI) | open: run `make docs-shots` alone in the Linux container before the PR (memory: run it last and alone) |
| R8 | `ReadRegular` re-opens the resolved path, so a symlink swapped in between resolve and open by a local user could escape the folder | low (needs local write access to the folder) | accepted, noted: the open handle is still checked as a regular file and decoded as an image only |
| R9 | `ItemsBySKU` uses `sku = ? COLLATE NOCASE`, which cannot use a binary index: one table scan per row | low (perf) | accepted: 5000 rows max; fine at pilot sizes (hundreds of items) |
| R10 | Preview of URL rows downloads every image, then apply downloads them again | low | accepted: keeps preview/apply parity (card AC); see URL concurrency follow-up |
| R11 | Misleading `netaccess:allow` comment ("demo mode is checked in DialContext") | nit | fixed: names `NewClientWithTransport` as the demo gate |

## What was checked and held

1. Path escape: `..` components (either slash), UNC `//`/`\\`, NUL refused
   before any filesystem call; symlinks resolved (`EvalSymlinks`) on both
   root and candidate before the strict-containment check; the root itself
   is not "inside"; absolute paths inside the folder accepted; devices/pipes/
   dirs refused on the open handle. Case tricks on case-insensitive disks only
   produce false refusals.
2. SSRF: check runs in `net.Dialer.Control` on the address really dialled
   (covers redirects and DNS rebinding); `Proxy: nil`; https + port 443 only;
   no userinfo; redirect chain re-checked; 64 KB headers; 20 s overall.
   Client built through `netaccess.NewClientWithTransport` (demo deny).
3. Zip bombs/memory: entries never extracted; declared `UncompressedSize64`
   and actual bytes both capped at 10 MB; 10 000 entries; 5000 rows; one image
   in memory at a time; 1 GB per run; multipart spools to disk beyond 8 MB and
   is removed; 600 MB body cap.
4. Role gating: both routes `canPerform(catalog_management)` server-side,
   card hidden otherwise (#3079); joined till refused (409) and told to use
   the main till; demo-denied; failed CSV owner-bound and short-lived.
5. CSV formula injection: failed CSV writes only line number, SKU (prefixed
   `'` when it starts with `= + - @ TAB CR`) and a fixed status code.
6. Error messages: only status codes / locale keys reach the client; paths,
   URLs and OS errors only go to the server log. Test asserts the folder path
   is absent from the response.
7. Repo rules: SQL only in `internal/data`; no migration; English keys +
   meta only; no hard-coded strings; logical CSS; no modal; writes go through
   the item editor's `StoreItemPhoto` (which creates its directories); no new
   cwd-relative path after R1.

## Verdict

PASS after fixes R1-R5, R11, on the condition that R7 (docs-shots) is
regenerated before the PR. R6 is a real but card-accepted privacy edge;
file it as a follow-up.

## Follow-ups (cards to file, never silent)

- XLSX input for the photo CSV (card asks CSV/XLSX; v1 reads CSV only).
- Progress, cancel and resume for long runs (v1 is one synchronous request
  with a one-run lock; no status chip).
- Variant photos: a variant SKU is refused (`variant_sku`) until the editor
  supports per-variant photos.
- URL downloads: bounded concurrency (v1 is sequential) and no double
  download between preview and apply (R10).
- e2e spec for the Import page card and docs-shots of it (R7 covers only the
  regeneration).
- Align the image rules with #4573 (image contract for import plugins / cloud
  import): one shared size/type/pixel rule set.
- R6: folder mode admin-only or Settings-pinned folder; single status for
  unreadable folder rows.

## Fable security pass (2026-11-17, same card) and playwright root cause

Playwright root cause: the photos card put two more `input[type=file]`
elements on the default Import page; ~20 existing e2e specs address
`input[type=file]` unscoped (strict-mode violation, 70 failures incl. their
retries). Fix: the card now renders only in `/import?mode=photos`; the default
page shows a link and keeps exactly one file input
(`TestImportPage_PhotosCardOnlyInPhotosMode`).

Fable (read-only) found, all fixed with tests:

1. Medium-High: the folder was any caller-supplied path (`/`, UNC share =
   SMB auth leak, existence oracle). `ValidateFolder` refuses UNC/device
   roots, filesystem/drive roots and system trees.
2. Medium: opening a FIFO blocked forever holding the run lock. `Lstat`
   before open, `O_NONBLOCK`, Windows device names refused
   (`fifo_unix_test.go`).
3. Medium: ZIP entry cap ran after the whole central directory was parsed.
   `centralDirOK` checks the EOCD record first.
4. Low-Medium: no same-origin check on the POST. `sameOriginRequest` added.
5. Low: run slot taken before the upload is spooled; 30-minute whole-run
   deadline.
6. Low: resolve/open symlink TOCTOU. Reads go through `os.OpenRoot`
   (`ReadInFolder`); `ReadRegular` removed.
7. Low: previews are audited too (`photo_import_preview`), with folder.
8. Low: `::a.b.c.d` and `fec0::/10` blocked; redirect limit is now 3.

Not changed: a per-run URL-row cap (5000 rows and the 30-minute deadline
bound it).
