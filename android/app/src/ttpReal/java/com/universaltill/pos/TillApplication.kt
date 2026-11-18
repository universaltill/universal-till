package com.universaltill.pos

import android.app.Application
import android.os.Build
import com.stripe.stripeterminal.TerminalApplicationDelegate
import com.stripe.stripeterminal.taptopay.TapToPay

/**
 * #3048, declared ONLY by the ttpReal manifest overlay. The Stripe SDK runs
 * Tap to Pay in a dedicated process and starts a second Application there:
 * init must be skipped in that process. The SDK_INT guard comes first so an
 * API 24-32 arm64 phone never loads an SDK class at launch.
 */
class TillApplication : Application() {
    override fun onCreate() {
        super.onCreate()
        if (Build.VERSION.SDK_INT >= 33) {
            if (TapToPay.isInTapToPayProcess()) return
            TerminalApplicationDelegate.onCreate(this)
        }
    }
}
