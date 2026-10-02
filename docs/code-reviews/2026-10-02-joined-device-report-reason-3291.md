# Review: joined device's bug report no longer says "finish enrolling" (ut-docs#3291)

- Date: 2026-10-02 · Lane: lane:cloud-24 · Author: Opus 5.5 · Reviewer: Fable (independent)
- Branch: `fix/3291-joined-device-report-reason`

## Change

A phone or iPad joined to a shop as a LAN replica (`sync.primary_url` set)
used to show *"Couldn't send — This till isn't fully set up yet — finish
enrolling it"* on `/my-reports`. That was wrong for a joined device. Since
universal-till#1648 (ADR-0116 D3), a joined device redeems its own cloud
credential through its main till's vouch, and the queued report then
uploads by itself.

- `internal/pages/my_reports_page.go`: a `not_registered` bundle on a
  joined device shows **Saved here, waiting to send** with the new reason
  `issuereport.status.pending_reason.joined_awaiting_credential`. It stays
  that way while the main till has vouched (`enroll.CurrentStatus().ViaMainTill`),
  or below `issuereport.UploadFailingThreshold` failures. After that it is
  **Couldn't send** with `issuereport.status.failing_reason.joined_no_access`,
  which tells the user to check the main till: on, online, registered, up
  to date. Non-joined tills keep the old behaviour.
- New keys in en/ar/fa/tr (`web/locales` ships only these four).
- The help topic `my-reports.md` has both bullets updated in en/de/fa/tr/ar.
- The de/es packs need their own follow-up PRs (see the close-out on #3291).

## Tests (TDD)

- `TestMyReportsPage_JoinedDeviceNotRegisteredStaysPending` was red before
  the handler change: it got "Couldn't send" and "finish enrolling".
- `TestMyReportsPage_JoinedDeviceNotRegisteredFailsPastThreshold` was
  added from the review.
- The existing `TestMyReportsPage_NotRegisteredFailsImmediately` still
  passes, so the non-joined path is unchanged.
- Each test renders the real `my_reports.html` template through the
  handler.

## Review findings (Fable)

1. **Minor, fixed.** The first version keyed the joined case on
   `sync.primary_url` alone, ahead of the failing threshold. So a joined
   device whose credential will never come showed "waiting" forever with no
   warning, and the advice was wrong. Examples: an unregistered or pre-#2730
   main till, a permanently refused redeem, or a device off the LAN. This
   regressed ut-docs#637's "on its way vs. will never arrive" promise. The
   fix applies the threshold unless the main till has vouched, and adds the
   joined-specific failing reason plus a test.
2. **Minor, fixed.** There was no past-threshold test. Added.
3. **Nit, accepted.** The Arabic help topic says «الكاشير الرئيسي» and
   `ar.json` says «الصندوق الرئيسي». Each matches the terminology already
   used in its own file.

Checked with no issue: the template renders `FailReasonKey` regardless of
`Failing`, so the chip stays neutral while waiting. `d.Settings` has a
nil/error fallback. `primary_url` follows the same convention as
`cloud_link.go`. A promoted replica clears `primary_url`, so it takes the
old path.

## Not verifiable here

On-device check (iPhone/iPad): a report from a paired device appears in
the admin list with platform iOS once the device runs a release that
includes #1648 and this change. That needs a human with the device; the
card's close-out records it.
