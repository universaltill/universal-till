package com.universaltill.pos.reader

import com.universaltill.pos.tap.Capability
import com.universaltill.pos.tap.CloudApi
import com.universaltill.pos.tap.CloudException
import com.universaltill.pos.tap.CollectEnd
import com.universaltill.pos.tap.MainExecutor
import com.universaltill.pos.tap.ResultCell
import com.universaltill.pos.tap.StartRequest
import com.universaltill.pos.tap.TapToPayEngine
import org.json.JSONObject

/**
 * What the Stripe Terminal SDK would ask ut-cloud's broker for, answered from the reader link instead (ADR-0198 §6.4):
 * the connection token comes from the MAIN (`reader.token`), the PaymentIntent is the one the main already created
 * (its client secret travels in the `reader.collect` frame and lives here only while that collect is armed). Nothing
 * here can reach the cloud: a reader phone holds no cloud credential.
 */
class ReaderCloudApi(private val main: MainExecutor, private val session: () -> ReaderSession) : CloudApi {
    @Volatile private var armed: ReaderCollect? = null
    @Volatile private var lastLocation: String? = null

    /** Called on the main thread when a collect is accepted. */
    fun arm(c: ReaderCollect) {
        armed = c
        lastLocation = c.locationId
    }

    fun disarm() {
        armed = null
    }

    /** Blocking, called from the SDK's own thread. */
    override fun connectionToken(): String {
        val cell = ResultCell<TokenResult>()
        main.post { session().requestToken { cell.set(it) } }
        return when (val r = cell.await(ReaderSession.TOKEN_TIMEOUT_MS + 2_000) ?: TokenResult.Err("timeout")) {
            is TokenResult.Ok -> {
                lastLocation = r.locationId
                JSONObject().put("secret", r.secret).toString()
            }
            is TokenResult.Err -> throw CloudException(if (r.code == "offline" || r.code == "timeout") "offline" else "cloud_error")
        }
    }

    override fun onboardingStatus(): String {
        val loc = armed?.locationId ?: lastLocation ?: throw CloudException("not_enrolled")
        return JSONObject().put("onboarded", true).put("charges_enabled", true).put("location_id", loc).toString()
    }

    override fun sessionCreate(amountMinor: Long, currency: String, key: String): String {
        val c = armed
        if (c == null || c.key != key || c.amountMinor != amountMinor || c.currency != currency.lowercase()) throw CloudException("cloud_error")
        return JSONObject().put("client_secret", c.clientSecret).put("amount_minor", c.amountMinor).put("currency", c.currency).toString()
    }
}

/** [ReaderPayments] on the shared Tap to Pay engine (#6533): a `reader.collect` is the same payment a till's devicepay request is. */
class EnginePayments(private val engine: TapToPayEngine, private val cloud: ReaderCloudApi) : ReaderPayments {
    private var n = 0

    override fun capability(): Capability = engine.capability()

    override fun setup(cb: (String?) -> Unit) = engine.setup(cb)

    override fun collect(c: ReaderCollect, remainingMs: Long, end: (CollectEnd) -> Unit) {
        cloud.arm(c)
        engine.collect(StartRequest("rd-${++n}", c.amountMinor, c.currency, c.key), remainingMs) { e ->
            cloud.disarm()
            end(e)
        }
    }

    override fun cancel(key: String): Boolean = engine.cancel(key)
}
