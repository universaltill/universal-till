# Review record: plugin download resume + install race (ut-docs#6975, root causes 2 and 3)

Scope: `internal/plugins` only. Root cause 1 (Shopify manifest `image`) is
the plugin repo's PR; item (d) (report the directive result to my.) is
separate.

## Change
- `DownloadManager.Download` re-reads the `.part` size on every attempt (a
  stale offset let a retry append to a file the failed attempt had already
  grown).
- Resume verifies the server honoured Range: 206 with `Content-Range` start
  equal to the requested byte, else the partial is deleted and the file is
  re-fetched from zero (200 to a Range request: body used from byte zero,
  partial truncated; 416 and a wrong `Content-Range`: restart).
- Checksum mismatch after a RESUMED download: partial deleted, ONE retry from
  scratch with no Range; a from-zero mismatch stays permanent (no loop) and
  leaves no corrupt `.part`.
- `Install`, `DownloadToStore`, `InstallFromStore` take a per-listing mutex
  (tmp dir + listing id) around download, verify and install, so a cloud
  directive and a manual install no longer share and delete `tmp/<listing>.part`.
- A second install of the same listing at the same version, still installed
  (and active unless installing inactive), returns the first result without a
  download. In-process memory, always verified against the DB; requests with a
  PreInstall gate or payment-key adoption always run in full.

## Tests (written first, watched fail on the old code)
- Wrong Content-Range, ignored Range (200 full body), checksum mismatch after
  resume (retry once, no Range), mismatch from zero (no retry, no leftover),
  retry resumes from the current part size.
- 6 concurrent installs of one listing across two installer instances: all
  succeed, exactly one download. `go test -race`.
- Existing resume fixture now sends `Content-Range` like a real 206.

## Findings (independent Opus review: no blocking bugs, no deadlock/race)
- Medium, FIXED: waiting for the listing lock ignored ctx cancellation; now a
  capacity-1 channel lock that selects on `ctx.Done()`.
- Medium, ACCEPTED (deferred): the already-installed shortcut runs after the
  download token is issued (token unacknowledged) and can't repair damaged
  files within one process; it only fires for a same-version, DB-verified,
  non-revoked, active install. Follow-up: skip before issuing the token.
- Low, ACCEPTED: a 200-to-Range then from-zero mismatch costs one extra
  download; `Bytes` unit matched case-sensitively; 416 on a complete part
  re-downloads; rejected resume on the last attempt is final.
- Missing tests noted: 416 restart, shortcut negatives beyond revoked.

Pre-flight: build, vet, gofmt clean; no SQL, strings, money or UI touched;
tmp dir MkdirAll'd, paths via paths.Plugins; manifest verifier unchanged.
Verdict: approve.
