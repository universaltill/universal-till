package com.universaltill.pos.tap

import android.content.Context

/**
 * The no-op side (#3048): compiled into armv7, universal and arm64 without
 * -PtapToPay. No Stripe class exists in these builds; there is no engine, so
 * the devicepay bridge is never registered with Go.
 */
object TapToPayPlatform {
    const val sdkCompiledIn = false

    fun create(appContext: Context, probe: DeviceProbe, ui: UiActions): TapToPayEngine? = null
}
