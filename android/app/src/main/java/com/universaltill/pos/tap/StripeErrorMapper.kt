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
    private val known = setOf("offline", "connect_incomplete", "cloud_error", "bad_request")

    /** Go's TapToPayCloud error text starts with a closed code ("offline: cloud not reached"). */
    fun code(message: String?): String {
        val head = message?.substringBefore(':')?.trim()
        return if (head != null && head in known) head else "cloud_error"
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
 * Cloud location lookup (kept in memory only) plus ONE reconnect on SESSION_EXPIRED.
 * [connectFn] is the SDK's connectReader; its error is an SDK code name or null.
 */
class ReaderConnector(private val cloud: CloudApi, private val connectFn: (String, (String?) -> Unit) -> Unit) {
    private var locationId: String? = null

    fun connect(cb: (String?) -> Unit) {
        val loc =
            locationId ?: try {
                parseLocationId(cloud.location()).also { locationId = it }
            } catch (e: CloudException) {
                cb(CloudErrors.code(e.code)); return
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
 * The cloud's location reply field names are still open (design Q2): accept
 * {"location_id":..} or {"id":..}, or a bare id. Only GoCloudApi's JSON reaches this.
 */
fun parseLocationId(body: String): String {
    val t = body.trim()
    if (t.startsWith("{")) {
        val j = try { org.json.JSONObject(t) } catch (_: Exception) { throw CloudException("cloud_error") }
        val id = j.optString("location_id").ifEmpty { j.optString("id") }
        if (id.isEmpty()) throw CloudException("cloud_error")
        return id
    }
    if (t.isEmpty()) throw CloudException("cloud_error")
    return t
}
