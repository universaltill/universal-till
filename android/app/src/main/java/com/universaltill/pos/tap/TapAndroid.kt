package com.universaltill.pos.tap

import android.content.Context
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import com.universaltill.pos.BuildConfig
import mobile.Mobile
import org.json.JSONArray
import org.json.JSONObject

/** Main-thread timers (design section 7). */
class MainScheduler : Scheduler {
    private val handler = Handler(Looper.getMainLooper())

    override fun nowMs(): Long = SystemClock.elapsedRealtime()

    override fun postDelayed(delayMs: Long, task: () -> Unit): Cancelable {
        val r = Runnable { task() }
        handler.postDelayed(r, delayMs)
        return object : Cancelable {
            override fun cancel(done: () -> Unit) {
                handler.removeCallbacks(r)
                done()
            }
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

    // Keyed JSON arrays (bounded). commit(): a pending record must be on disk before the customer taps.
    private fun readArray(name: String): JSONArray =
        try { JSONArray(p.getString(name, "[]")) } catch (_: Exception) { JSONArray() }

    private fun writeArray(name: String, a: JSONArray) {
        p.edit().putString(name, a.toString()).commit()
    }

    fun pendingAll(): List<PendingRecord> {
        val a = readArray("pending")
        return (0 until a.length()).mapNotNull {
            val o = a.optJSONObject(it) ?: return@mapNotNull null
            PendingRecord(
                o.optString("r"), o.optString("k"), o.optString("i"), o.optString("s"),
                o.optLong("a"), o.optString("c"),
            ).takeIf { r -> r.key.isNotEmpty() && r.intentId.isNotEmpty() }
        }
    }

    fun pendingWrite(list: List<PendingRecord>) {
        val a = JSONArray()
        // Unresolved records are never evicted by the bound; the oldest resolved-state ones go first.
        val keep = PendingBound.keep(list, MAX_PENDING)
        for (r in keep) a.put(JSONObject().put("r", r.requestId).put("k", r.key).put("i", r.intentId).put("s", r.state).put("a", r.amountMinor).put("c", r.currency))
        writeArray("pending", a)
    }

    fun doneAll(): List<DoneRecord> {
        val a = readArray("done")
        return (0 until a.length()).mapNotNull {
            val o = a.optJSONObject(it) ?: return@mapNotNull null
            DoneRecord(o.optString("k"), o.optString("r"), o.optString("i"), o.optLong("a"), o.optString("c"), o.optString("h").ifEmpty { null })
                .takeIf { d -> d.key.isNotEmpty() && d.intentId.isNotEmpty() }
        }
    }

    fun doneWrite(list: List<DoneRecord>) {
        val a = JSONArray()
        for (d in list.takeLast(MAX_DONE)) {
            val o = JSONObject().put("k", d.key).put("r", d.requestId).put("i", d.intentId).put("a", d.amountMinor).put("c", d.currency)
            d.chargeId?.let { o.put("h", it) }
            a.put(o)
        }
        writeArray("done", a)
    }

    private companion object {
        const val MAX_PENDING = 16
        const val MAX_DONE = 32
    }
}

class PrefsPendingStore(private val prefs: TapPrefs) : PendingStore {
    override fun put(record: PendingRecord) = prefs.pendingWrite(prefs.pendingAll().filter { it.key != record.key } + record)

    override fun remove(key: String) = prefs.pendingWrite(prefs.pendingAll().filter { it.key != key })

    override fun load(key: String): PendingRecord? = prefs.pendingAll().firstOrNull { it.key == key }

    override fun all(): List<PendingRecord> = prefs.pendingAll()
}

class PrefsDoneStore(private val prefs: TapPrefs) : DoneStore {
    override fun put(r: DoneRecord) = prefs.doneWrite(prefs.doneAll().filter { it.key != r.key } + r)

    override fun load(key: String): DoneRecord? = prefs.doneAll().firstOrNull { it.key == key }

    override fun byRequest(requestId: String): DoneRecord? = prefs.doneAll().firstOrNull { it.requestId == requestId }
}

/**
 * The cloud calls go through Go (Mobile.tapToPayCloud -> internal/paybroker.Call):
 * the till's device credential and the op allow-list stay in Go, never in
 * Kotlin or the page. Blocking: call from a worker thread only. Go's error
 * text is a stable code (not_onboarded, offline, not_enrolled, http_<n>, ...).
 */
class GoCloudApi : CloudApi {
    private fun call(op: String, body: String): String =
        try {
            Mobile.tapToPayCloud(PROVIDER, op, body)
        } catch (e: Exception) {
            throw CloudException(CloudErrors.code(e.message))
        }

    override fun connectionToken(): String = call("connection_token", "{}")

    override fun onboardingStatus(): String = call("onboarding_status", "{}")

    override fun sessionCreate(amountMinor: Long, currency: String, key: String): String =
        call(
            "session_create",
            JSONObject().put("amount_minor", amountMinor).put("currency", currency).put("idempotency_key", key).toString(),
        )

    private companion object {
        const val PROVIDER = "stripe"
    }
}

/** connection_token reply: {"secret": "..."} (the iOS shell reads the same field). */
fun parseConnectionToken(body: String): String {
    val j = try { JSONObject(body.trim()) } catch (_: Exception) { throw CloudException("cloud_error") }
    val t = j.optString("secret")
    if (t.isEmpty()) throw CloudException("cloud_error")
    return t
}

/** session_create reply: the cloud-created PaymentIntent's client secret plus the amount/currency it holds. */
class CloudSession(val clientSecret: String, val amountMinor: Long?, val currency: String?) {
    // plain class: the client secret must never reach a log or a failed-assertion message
    override fun toString() = "CloudSession(amountMinor=$amountMinor, currency=$currency)"

    override fun equals(other: Any?) =
        other is CloudSession && other.clientSecret == clientSecret && other.amountMinor == amountMinor && other.currency == currency

    override fun hashCode() = clientSecret.hashCode()
}

fun parseSession(body: String): CloudSession {
    val j = try { JSONObject(body.trim()) } catch (_: Exception) { throw CloudException("cloud_error") }
    val secret = j.optString("client_secret")
    if (secret.isEmpty()) throw CloudException("cloud_error")
    return CloudSession(
        secret,
        if (j.has("amount_minor")) j.optLong("amount_minor") else null,
        j.optString("currency").ifEmpty { null },
    )
}

/** Where a flavor/property says the feature stands, for the shell. */
object TapBuild {
    val enabled: Boolean get() = BuildConfig.TAP_TO_PAY_ENABLED
}
