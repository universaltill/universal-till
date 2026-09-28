# Review: iOS TestFlight build stamps the release version (ut-docs#3221)

**Shipped:** `.github/workflows/ios-testflight.yml` binds the Go server with
`-ldflags "-X …/internal/buildinfo.Version=${MARKETING_VERSION}"`, so the iOS
app shows "Universal Till v0.30.x" instead of "vdev".
`scripts/ci/ios-testflight-workflow_test.sh` fails if the flag is missing or
the version is resolved after the bind (two new self-test fixture checks).

**Reviewer:** independent Fable subagent (the code was written by Opus 5.5).

**TDD:** the new check failed against the old workflow ("gomobile bind does
not stamp buildinfo.Version"), and passes after the fix. The reviewer mutated
a scratch copy six ways (flag removed, wrong variable, step order swapped,
flag moved to another step, step renamed, single-quoted flag): every one failed.

## Findings

| Severity | Finding | Outcome |
|---|---|---|
| minor | The guard is text-only. Unlike Android's `go version -m` gate, nothing checks that the built xcframework embeds the version, and `go version -m` can't read a static `.a`. | Accepted. The real check is the footer of the branch TestFlight build on the owner's iPad (#3221 AC). |
| nit | The FAIL message didn't say the exact spelling is intentional. | Fixed. |
| nit | The workflow comment said "updater"; on iOS only the update check runs. | Fixed. |

Also confirmed: `buildinfo.Version` is a `var`; `MARKETING_VERSION` is plain
`N.N.N` (the footer adds the `v`); `ios-ci.yml`'s unstamped bind is correct
("dev" for a simulator-only build). Side effect: the iOS app stops showing a
spurious "update available" (updates.go treats "dev" as older than every release).

**Verdict:** safe to merge.
