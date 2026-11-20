package com.universaltill.pos.tap

import android.os.Build
import android.content.Context

/** The real side (#3048): arm64 built with -PtapToPay=true only. */
object TapToPayPlatform {
    const val sdkCompiledIn = true

    fun create(appContext: Context, probe: DeviceProbe, ui: UiActions): TapToPayEngine? = create(appContext, probe, ui, GoCloudApi())

    /** [cloud] is where connection tokens and PaymentIntents come from: Go's broker for the till, the main's reader link in reader mode (ut-docs#6538). */
    fun create(appContext: Context, probe: DeviceProbe, ui: UiActions, cloud: CloudApi): TapToPayEngine? {
        // The SDK is Android 13+; below that no SDK class is ever touched.
        if (Build.VERSION.SDK_INT < 33) return null
        val prefs = TapPrefs(appContext)
        val reader = StripeTerminalReader(appContext, cloud)
        return TapToPayEngine(probe, reader, MainScheduler(), PrefsPendingStore(prefs), PrefsDoneStore(prefs), ui)
    }
}
