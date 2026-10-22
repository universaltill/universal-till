# Review: customer write repository and sealed import apply (ut-docs#4748, ADR-0168 §5)

Scope: migration `087_customer_import.sql`, `internal/data/customer_import_repo.go`,
`EraseCustomer` (suppression hashes + link tombstones), `internal/customerimport`,
`directivekey.OpenSealed` / `CustomersAAD`, `internal/cloudsync/import_customers.go`,
`internal/pages/cloud_import_customers.go`, `docs/data-model.md`.
All tests use invented customers.

## What changed
- `customers.postcode` (+ expression indexes on `lower(email)` and the folded
  postcode), `import_source_links.baseline_hash` / `erased_at`, and the
  main-till-only tables `customer_import_salt` and `customer_erasure_hashes`.
  `postcode` syncs to replicas and is scrubbed on an erased shell; the new
  tables are classified not-synced.
- `customerimport.Apply` (one transaction per chunk, at most 200 rows): the
  ADR order tombstone, link, folded email, `phone_e164`, name + postcode only
  when exactly one fits, else create; ambiguous / conflicting rows are issues
  and write nothing; empty fields are filled, differing ones kept and returned
  as row number + field name; `force_fields` overwrites exactly those fields;
  a salted per-link baseline makes the same rows again a zero-change run.
- `EraseCustomer` records salted email / phone hashes and turns the customer's
  import links into tombstones in its own transaction; a later import counts
  `erased_on_till` and never recreates the person.
- Sealed entry point: `import_customers` (`job_id`, `chunk_index`, `sealed`) is
  main-till only (skipped on a satellite, refused again in the hook by
  `requirePrimaryDirective`); HPKE info `universaltill/import-customers/v1`,
  AAD store, directive id, type, job id, chunk index. The result carries
  counts and per-row outcomes with field names, nothing else.
- No customer value in logs, audit rows, errors or results (tests assert it
  over the real log capture and the audit table).

## Independent review (opus subagent) and triage
Fixed after it:
- B1 an erased email/phone could be written onto another customer by a name +
  postcode match or a fill/force: such a row is `erased_on_till`; a suppressed
  value is never written (reported by field name as kept).
- B2 the id-less synthetic link merged different people and skipped the match
  rules: id-less rows are matched every time; their link only carries the
  baseline; with no email and no phone the key is a hash of the whole row.
- Re-delivery wrote an audit row for issue-only chunks (audit now only when a
  customer was created or updated); force_fields could not correct case; the
  erasure hash missed a number erased before the shop country was set (both
  key forms are kept); the pending-phone scan ran per row (once per chunk
  now); erasure scanned every link (index on `(entity, local_id)`); a
  sealed-size check before decrypt.
Known limits, reported on the PR:
- Two identical id-less rows with no email and no phone are one person here.
- A customer erased with no email, no phone and no import link leaves nothing
  to recognise them by.
- A chunk whose result post fails is applied again on re-delivery and reports
  `unchanged` (the first run's kept-field list is not stored).
- No override for an erased person in v1 (ADR decision 5).

## Not in this PR
The cloud side of the customers path (sealing, jobs, differences list, the
shared seal test vector in `reference/contracts/`) is cards B/F; the plugin
host call of #4437 only adds a caller to the repository here. No user-facing
string was added (directive results are English like every other directive
result), so no locale keys or language-pack PRs.
