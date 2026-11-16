package com.universaltill.pos.tap

import androidx.appcompat.app.AppCompatActivity

/**
 * The no-op side (#3048): compiled into armv7, universal and arm64 without
 * -PtapToPay. No Stripe class exists in these builds; there is no engine, so
 * the bridge is never registered and the page sees no window.AndroidTapToPay.
 */
object TapToPayPlatform {
    const val sdkCompiledIn = false

    fun create(activity: AppCompatActivity, probe: DeviceProbe, emit: (String) -> Unit): TapToPayEngine? = null
}
