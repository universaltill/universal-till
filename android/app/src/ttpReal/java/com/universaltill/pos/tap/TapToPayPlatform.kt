package com.universaltill.pos.tap

import android.os.Build
import android.content.Context

/** The real side (#3048): arm64 built with -PtapToPay=true only. */
object TapToPayPlatform {
    const val sdkCompiledIn = true

    fun create(appContext: Context, probe: DeviceProbe, ui: UiActions, emit: (String) -> Unit): TapToPayEngine? {
        // The SDK is Android 13+; below that no SDK class is ever touched.
        if (Build.VERSION.SDK_INT < 33) return null
        val prefs = TapPrefs(appContext)
        val reader = StripeTerminalReader(appContext, GoCloudApi())
        return TapToPayEngine(probe, reader, MainScheduler(), PrefsPendingStore(prefs), PrefsDoneStore(prefs), ui, emit)
    }
}
