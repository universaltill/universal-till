package com.universaltill.pos.tap

enum class CancelKind { CUSTOMER, OPERATOR, NONE }

/** Pure mapping from the Stripe SDK's error-code NAMES (strings) to our tokens, so it runs on the JVM. */
object StripeErrorMapper {
    fun reason(code: String): String =
        when (code) {
            "TAP_TO_PAY_INSECURE_ENVIRONMENT", "TAP_TO_PAY_DEVICE_TAMPERED", "TAP_TO_PAY_DEBUG_NOT_SUPPORTED" -> "insecure_environment"
            "STRIPE_API_CONNECTION_ERROR", "NETWORK_ERROR", "REQUEST_TIMED_OUT" -> "network"
            else -> "reader_error"
        }

    fun cancelKind(code: String): CancelKind =
        when (code) {
            "CANCELED_BY_READER" -> CancelKind.CUSTOMER
            "CANCELED" -> CancelKind.OPERATOR
            else -> CancelKind.NONE
        }

    fun isSessionExpired(code: String) = code == "SESSION_EXPIRED"

    /** A decline is only a definite decline when the intent went back to requires_payment_method WITH a decline code. */
    fun isDecline(intentStatus: String?, declineCode: String?) = intentStatus == "REQUIRES_PAYMENT_METHOD" && declineCode != null
}

object CloudErrors {
    private val known = setOf("offline", "connect_incomplete", "cloud_error", "not_enrolled")

    /**
     * Go's TapToPayCloud error text is a stable code. The cloud's own
     * not_onboarded is our connect_incomplete; anything unrecognised is cloud_error.
     */
    fun code(message: String?): String {
        val head = message?.substringBefore(':')?.trim()
        return when {
            head == "not_onboarded" -> "connect_incomplete"
            head != null && head in known -> head
            else -> "cloud_error"
        }
    }

    fun toStartError(code: String): String =
        when (code) {
            "offline" -> "offline"
            "connect_incomplete" -> "connect_incomplete"
            else -> "not_ready"
        }
}

/** Debug builds use Stripe's simulated reader; a release build never does (design section 9). */
object DiscoveryPolicy {
    fun simulated(debugBuild: Boolean) = debugBuild

    /** The simulated reader skips root/developer-options/debuggable gating, so we skip our mirror of it too. */
    fun enforceEnvironmentChecks(debugBuild: Boolean) = !debugBuild
}

/**
 * One reader connect (discovery, then connect) that can never hang (follow-up of #3048): discovery that finds
 * nothing and never ends, or a connect that never calls back, would leave the single-flight gate waiting
 * forever and every later payment `not_ready`. After [timeoutMs] [abort] stops discovery and the attempt
 * ends as `reader_error`. [done] runs once; a late SDK answer after that is ignored.
 */
class ConnectAttempt(scheduler: Scheduler, timeoutMs: Long, private val abort: () -> Unit, private val done: (String?) -> Unit) {
    companion object {
        const val TIMEOUT_MS = 45_000L
    }

    var finished = false
        private set

    /** A reader was chosen and connectReader is under way: an empty discovery end no longer means "nothing found". */
    var connecting = false

    private val timer =
        scheduler.postDelayed(timeoutMs) {
            if (!finished) {
                abort()
                end("reader_error")
            }
        }

    fun end(err: String?) {
        if (finished) return
        finished = true
        timer.cancel()
        done(err)
    }
}

/**
 * Cloud location lookup (kept in memory only) plus ONE reconnect on SESSION_EXPIRED.
 * [connectFn] is the SDK's connectReader; its error is an SDK code name or null.
 */
class ReaderConnector(private val cloud: CloudApi, private val connectFn: (String, (String?) -> Unit) -> Unit) {
    private var locationId: String? = null

    fun connect(cb: (String?) -> Unit) {
        val loc =
            locationId ?: try {
                parseLocationId(cloud.onboardingStatus()).also { locationId = it }
            } catch (e: CloudException) {
                cb(CloudErrors.toStartError(CloudErrors.code(e.code))); return
            }
        connectFn(loc) { err ->
            when {
                err == null -> cb(null)
                StripeErrorMapper.isSessionExpired(err) ->
                    connectFn(loc) { err2 -> cb(if (err2 == null) null else StripeErrorMapper.reason(err2)) }
                else -> cb(StripeErrorMapper.reason(err))
            }
        }
    }
}

/**
 * onboarding_status reply (same rule as the iOS shell): onboarded AND
 * charges_enabled AND a location_id, else the shop has not finished Connect
 * onboarding (connect_incomplete).
 */
fun parseLocationId(body: String): String {
    val j = try { org.json.JSONObject(body.trim()) } catch (_: Exception) { throw CloudException("cloud_error") }
    val loc = j.optString("location_id")
    if (!j.optBoolean("onboarded", false) || !j.optBoolean("charges_enabled", false) || loc.isEmpty()) {
        throw CloudException("connect_incomplete")
    }
    return loc
}
