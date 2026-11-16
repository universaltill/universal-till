package com.universaltill.pos.tap

import android.content.Context
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import com.universaltill.pos.BuildConfig
import mobile.Mobile
import org.json.JSONObject

/** Main-thread timers (design section 7). */
class MainScheduler : Scheduler {
    private val handler = Handler(Looper.getMainLooper())

    override fun nowMs(): Long = SystemClock.elapsedRealtime()

    override fun postDelayed(delayMs: Long, task: () -> Unit): Cancelable {
        val r = Runnable { task() }
        handler.postDelayed(r, delayMs)
        return object : Cancelable {
            override fun cancel() = handler.removeCallbacks(r)
        }
    }
}

/**
 * The only persisted state of the whole feature: whether the operator saw the
 * education text, and the write-before-tap record (ids only: no client
 * secret, no card data). allowBackup is false for the app.
 */
class TapPrefs(context: Context) {
    private val p = context.applicationContext.getSharedPreferences("tap_to_pay", Context.MODE_PRIVATE)

    var educationAccepted: Boolean
        get() = p.getBoolean("education_accepted", false)
        set(v) {
            p.edit().putBoolean("education_accepted", v).apply()
        }

    fun putPending(r: PendingRecord) {
        // commit(): the record must be on disk before the customer taps.
        p.edit().putString("p_request", r.requestId).putString("p_key", r.key).putString("p_intent", r.intentId).putString("p_state", r.state).commit()
    }

    fun clearPending() {
        p.edit().remove("p_request").remove("p_key").remove("p_intent").remove("p_state").commit()
    }

    fun loadPending(): PendingRecord? {
        val r = p.getString("p_request", null) ?: return null
        val k = p.getString("p_key", null) ?: return null
        val i = p.getString("p_intent", null) ?: return null
        return PendingRecord(r, k, i, p.getString("p_state", "created") ?: "created")
    }
}

class PrefsPendingStore(private val prefs: TapPrefs) : PendingStore {
    override fun put(record: PendingRecord) = prefs.putPending(record)

    override fun clear() = prefs.clearPending()

    override fun load(): PendingRecord? = prefs.loadPending()
}

/**
 * The cloud calls go through Go (Mobile.tapToPayCloud): the till's device
 * credential never reaches Kotlin or the page (design section 6). Blocking:
 * call from a worker thread only.
 */
class GoCloudApi : CloudApi {
    private fun call(op: String, body: String): String =
        try {
            Mobile.tapToPayCloud(op, body)
        } catch (e: Exception) {
            throw CloudException(CloudErrors.code(e.message))
        }

    override fun connectionToken(): String = call("connection_token", "{}")

    override fun location(): String = call("location", "{}")

    override fun status(): String = call("connect_status", "{}")

    override fun onboardingLink(): String = call("onboarding_link", "{}")
}

/** The cloud's token reply field name is still open (design Q2): accept "secret" or "token". */
fun parseConnectionToken(body: String): String {
    val j = try { JSONObject(body.trim()) } catch (_: Exception) { throw CloudException("cloud_error") }
    val t = j.optString("secret").ifEmpty { j.optString("token") }
    if (t.isEmpty()) throw CloudException("cloud_error")
    return t
}

/** Where a flavor/property says the feature stands, for the shell. */
object TapBuild {
    val enabled: Boolean get() = BuildConfig.TAP_TO_PAY_ENABLED
}
