# 2026-11-18 installer public URL (ut-docs#6407)

Shipped: dedicated `unitillinstallers` blob account + OIDC `unitill-installer-publisher` (container-scoped Blob Data Contributor) in ut-infra; `publish-installer.yml` + `scripts/ci/publish-installer.sh` (+ self-test, release.yml job, verify-only signing setup) in universal-till. Pre-flight: n/a (infra/CI, no UI, no new strings).

Review: one combined Fable review + test (isolated mktemp mutations: 11/13 killed). Findings:
- HIGH platform provider would 403 on queue-property reads with shared key off: FIXED (`storage { data_plane_available = false }`).
- MEDIUM re-run after a successful upload could never succeed: FIXED (BlobAlreadyExists then identical public sha256 passes; different bytes fail; tests added).
- MEDIUM role can delete/overwrite (append-only is a convention): ACCEPTED, documented (same trust as signer; versioning + 30d soft delete); create-only custom role deferred.
- LOW prevent_destroy / local_user_enabled false: FIXED. Status-check test gap, CR strip, checkout comment: FIXED.
- INFO release-digest is same-source; Authenticode + build sha256 are the gates.
Verdict: approve after fixes. terraform fmt/validate clean; publish-installer_test.sh 19 ok.
