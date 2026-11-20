package com.universaltill.pos.reader

import com.universaltill.pos.tap.Capability

/**
 * Whether this install offers reader mode at all. Dark unless the build carries -PreaderMode=true (it also needs the
 * Stripe SDK, -PtapToPay=true, since the collect runs on the same engine); hidden on a phone with no NFC, no Google
 * Play services, Android older than 13, and so on (the same capability probe the Tap to Pay seam uses). A phone that
 * only has NFC or a permission switched off is Available: the setup button handles those.
 */
sealed class ReaderGate {
    object Off : ReaderGate()

    object Available : ReaderGate()

    data class Unsupported(val reason: String?) : ReaderGate()

    companion object {
        fun evaluate(readerModeFlag: Boolean, cap: Capability): ReaderGate =
            when {
                !readerModeFlag -> Off
                !cap.supported -> Unsupported(cap.reason)
                else -> Available
            }
    }
}
