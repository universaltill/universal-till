# Review: Stripe SDK "absent" check reads AABs and fails on unreadable input (#3048, Fable-early F5 on #2565)

Shipped: `scripts/ci/check-android-stripe-sdk.sh` now lists the archive, fails when it is not a readable zip, fails its integrity test, or holds no `classes*.dex`, and reads dex under any path (APK `classes.dex`, AAB `base/dex/classes.dex`). The old script printed `absent OK` for all of those.

Review (Fable, combined with ut-cloud#1057): found a CRC-damaged or encrypted dex still read as absent; fixed with `unzip -tq` first, test case added. Mutation: the original script fails 4 of the new cases.

Deferred: Fable-early F6 (an unresolved phone tap payment is only cleared by a manager "not charged"): needs the #6455 host function and a Go-to-native resolve driven by the broker's `session_status`; Kotlin not buildable here. Tracked on #6533. Pre-flight: script test 13/13.
