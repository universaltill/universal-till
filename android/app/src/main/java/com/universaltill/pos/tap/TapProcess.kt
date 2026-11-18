package com.universaltill.pos.tap

import android.content.Context
import com.universaltill.pos.BuildConfig

/**
 * The ONE place the feature flag turns into an engine (#3048). Pure so a test
 * can prove that with the flag off (or no SDK compiled in) no engine is built
 * and the factory does not even run.
 */
object TapToPayGate {
    fun engineOrNull(flagOn: Boolean, sdkCompiledIn: Boolean, build: () -> TapToPayEngine?): TapToPayEngine? =
        if (flagOn && sdkCompiledIn) build() else null
}

/** UiActions bound to the CURRENT Activity; with none, every prompt is refused (nothing is shown, nothing leaks). */
class SwitchableUi : UiActions {
    var target: UiActions? = null

    override fun showEducation(cb: (Boolean) -> Unit) = target?.showEducation(cb) ?: cb(false)

    override fun requestLocationPermission(cb: (Boolean) -> Unit) = target?.requestLocationPermission(cb) ?: cb(false)

    override fun openSettings(target: String): Boolean = this.target?.openSettings(target) ?: false
}

/** Runs one connect for any number of concurrent callers (prepare + start must not race two discoveries). */
class FlightGate {
    private val waiters = ArrayList<(String?) -> Unit>()

    val inFlight: Boolean get() = waiters.isNotEmpty()

    /** true = this caller must start the work. */
    fun join(cb: (String?) -> Unit): Boolean {
        val first = waiters.isEmpty()
        waiters += cb
        return first
    }

    fun finish(err: String?) {
        val w = ArrayList(waiters)
        waiters.clear()
        w.forEach { it(err) }
    }
}

/**
 * Engine, reader, bridge and result stores are PROCESS-scoped: an Activity that is
 * recreated or finished must not forget a payment in flight or a payment that
 * succeeded (a forgotten success reads back as "none" and gets charged again).
 */
object TapToPayProcess {
    private var engine: TapToPayEngine? = null
    private var bridge: GoDevicepayBridge? = null
    val ui = SwitchableUi()

    fun engineFor(context: Context): TapToPayEngine? {
        engine?.let { return it }
        val app = context.applicationContext
        return TapToPayGate.engineOrNull(BuildConfig.TAP_TO_PAY_ENABLED, TapToPayPlatform.sdkCompiledIn) {
            TapToPayPlatform.create(app, AndroidDeviceProbe(app, TapPrefs(app)), ui)
        }?.also { engine = it }
    }

    /** The one Go-facing object per process (it holds no Activity). */
    fun bridgeFor(engine: TapToPayEngine): GoDevicepayBridge =
        bridge ?: GoDevicepayBridge(DevicepayBridgeCore(engine, MainLooperExecutor, System::currentTimeMillis)).also { bridge = it }
}
