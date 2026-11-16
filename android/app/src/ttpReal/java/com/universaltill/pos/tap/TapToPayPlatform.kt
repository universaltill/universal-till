package com.universaltill.pos.tap

import android.os.Build
import androidx.appcompat.app.AppCompatActivity

/** The real side (#3048): arm64 built with -PtapToPay=true only. */
object TapToPayPlatform {
    const val sdkCompiledIn = true

    fun create(activity: AppCompatActivity, probe: DeviceProbe, emit: (String) -> Unit): TapToPayEngine? {
        // The SDK is Android 13+; below that no SDK class is ever touched.
        if (Build.VERSION.SDK_INT < 33) return null
        val prefs = TapPrefs(activity)
        val scheduler = MainScheduler()
        val reader = StripeTerminalReader(activity.applicationContext, GoCloudApi())
        return TapToPayEngine(probe, reader, scheduler, PrefsPendingStore(prefs), AndroidUi(activity, prefs), emit)
    }
}
