package com.universaltill.pos.reader

import android.content.Context
import com.universaltill.pos.BuildConfig
import com.universaltill.pos.tap.AndroidDeviceProbe
import com.universaltill.pos.tap.MainLooperExecutor
import com.universaltill.pos.tap.MainScheduler
import com.universaltill.pos.tap.SwitchableUi
import com.universaltill.pos.tap.TapPrefs
import com.universaltill.pos.tap.TapToPayGate
import com.universaltill.pos.tap.TapToPayPlatform

/**
 * The reader app's process (ReaderActivity runs in `:reader`: no Go server, no shop database, its own Stripe Terminal
 * singleton). Session and engine outlive the Activity, like the till's engine does, so a payment in flight survives a
 * recreated screen.
 */
object ReaderProcess {
    class Parts(val session: ReaderSession, val ui: SwitchableReaderUi, val tapUi: SwitchableUi)

    private var parts: Parts? = null

    /** The one link of this process (main thread only). */
    var link: ReaderLink? = null

    fun gate(context: Context): ReaderGate {
        val app = context.applicationContext
        return ReaderGate.evaluate(BuildConfig.READER_MODE_ENABLED, AndroidDeviceProbe(app, TapPrefs(app)).let { com.universaltill.pos.tap.TapToPayCapability.evaluate(it.facts()) })
    }

    fun partsFor(context: Context): Parts? {
        parts?.let { return it }
        val app = context.applicationContext
        val tapUi = SwitchableUi()
        val ui = SwitchableReaderUi()
        lateinit var session: ReaderSession
        val cloud = ReaderCloudApi(MainLooperExecutor) { session }
        val engine =
            TapToPayGate.engineOrNull(BuildConfig.TAP_TO_PAY_ENABLED, TapToPayPlatform.sdkCompiledIn) {
                TapToPayPlatform.create(app, AndroidDeviceProbe(app, TapPrefs(app)), tapUi, cloud)
            } ?: return null
        session = ReaderSession(MainScheduler(), EnginePayments(engine, cloud), ui, BuildConfig.VERSION_NAME)
        return Parts(session, ui, tapUi).also { parts = it }
    }
}

/** The screen of the CURRENT activity; with none, nothing is drawn (a payment still completes headless). */
class SwitchableReaderUi : ReaderUi {
    var target: ReaderUi? = null
    var last: ReaderScreen = ReaderScreen.Connecting
        private set

    override fun show(s: ReaderScreen) {
        last = s
        target?.show(s)
    }

    override fun incoming() {
        target?.incoming()
    }

    override fun revoked() {
        target?.revoked()
    }
}
