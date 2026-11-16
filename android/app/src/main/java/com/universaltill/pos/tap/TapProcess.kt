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

/** Reply channel to the page: the latest valid message's proxy. A stale (reloaded) page's proxy is swallowed. */
class SwitchableOut {
    var proxy: ((String) -> Unit)? = null

    fun emit(json: String) {
        try { proxy?.invoke(json) } catch (_: Exception) {}
    }
}

/** UiActions bound to the CURRENT Activity; with none, every prompt is refused (nothing is shown, nothing leaks). */
class SwitchableUi : UiActions {
    var target: UiActions? = null

    override fun showEducation(cb: (Boolean) -> Unit) = target?.showEducation(cb) ?: cb(false)

    override fun requestLocationPermission(cb: (Boolean) -> Unit) = target?.requestLocationPermission(cb) ?: cb(false)

    override fun openSettings(target: String): Boolean = this.target?.openSettings(target) ?: false
}

/** One call-in point for the WebMessage listener: re-checks the origin on EVERY message (design section 9). */
class BridgeCore(private val engine: TapToPayEngine, private val out: SwitchableOut, private val host: String) {
    fun onMessage(raw: String?, origin: String, isMainFrame: Boolean, reply: (String) -> Unit) {
        if (!OriginPolicy.allows(origin, isMainFrame, host)) return
        val m = raw ?: return
        out.proxy = reply
        engine.handle(m, reply)
    }

    companion object {
        /** The listener's origin rule set: exactly the till's own origin, never a wildcard. */
        fun originRules(host: String): Set<String> = setOf("http://$host")
    }
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
 * Engine, reader and result stores are PROCESS-scoped: an Activity that is
 * recreated or finished must not forget a payment in flight or a payment that
 * succeeded (a forgotten success reads back as "none" and gets charged again).
 */
object TapToPayProcess {
    private var engine: TapToPayEngine? = null
    val ui = SwitchableUi()
    val out = SwitchableOut()

    fun engineFor(context: Context): TapToPayEngine? {
        engine?.let { return it }
        val app = context.applicationContext
        return TapToPayGate.engineOrNull(BuildConfig.TAP_TO_PAY_ENABLED, TapToPayPlatform.sdkCompiledIn) {
            TapToPayPlatform.create(app, AndroidDeviceProbe(app, TapPrefs(app)), ui, out::emit)
        }?.also { engine = it }
    }
}
