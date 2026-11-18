package com.universaltill.pos.tap

import org.json.JSONObject

/** One payment's outcome inside the app. No card data field exists on purpose (ADR-0125 3f). */
enum class TapStatus(val wire: String) {
    SUCCEEDED("succeeded"),
    DECLINED("declined"),
    CANCELLED("cancelled"),
    UNKNOWN("unknown"),
}

data class TapResult(
    val requestId: String,
    val status: TapStatus,
    val reason: String,
    val paymentIntentId: String?,
    val chargeId: String?,
    val declineCode: String?,
    val cardBrand: String?,
    val last4: String?,
    val late: Boolean,
)

/** A payment as internal/devicepay.Request carries it (golden: internal/devicepay/testdata/request_golden.json). */
data class CollectRequest(
    val amountMinor: Long,
    val currency: String,
    val idempotencyKey: String,
    val deadlineUnixMs: Long,
)

/**
 * The JSON shapes that cross the gomobile boundary (mirror of internal/devicepay and
 * ios/.../TapToPayModels.swift). Strict on the way in (Go is the only caller, so anything else is a bug or an
 * attack); on the way out only the fields Go's strict decoder accepts, and an unreadable or approval-without-id
 * answer is never produced: anything unsure is `pending` (#6439).
 */
object DevicepayCodec {
    const val PROVIDER = "stripe"
    const val MAX_BYTES = 4096
    const val MAX_AMOUNT_MINOR = 9_999_999L
    private val fields = setOf("provider", "amount_minor", "currency", "idempotency_key", "description", "deadline_unix_ms")
    private val currencyRe = Regex("^[A-Za-z]{3}$")
    private val keyRe = Regex("^[A-Za-z0-9:_.-]{1,128}$")

    /** null = not a request this app will take (the caller answers declined/bad_request). */
    fun parseRequest(raw: String?): CollectRequest? {
        if (raw == null || raw.toByteArray(Charsets.UTF_8).size > MAX_BYTES) return null
        val j = try { JSONObject(raw) } catch (_: Exception) { return null }
        if (j.keys().asSequence().any { it !in fields }) return null
        if (j.opt("provider") != PROVIDER) return null
        val amount =
            when (val a = j.opt("amount_minor")) {
                is Int -> a.toLong()
                is Long -> a
                else -> return null
            }
        if (amount < 1 || amount > MAX_AMOUNT_MINOR) return null
        val currency = j.opt("currency") as? String ?: return null
        if (!currencyRe.matches(currency)) return null
        val key = j.opt("idempotency_key") as? String ?: return null
        if (!keyRe.matches(key)) return null
        val deadline =
            when (val d = j.opt("deadline_unix_ms")) {
                is Int -> d.toLong()
                is Long -> d
                else -> return null
            }
        return CollectRequest(amount, currency.lowercase(), key, deadline)
    }

    /** Internal tokens -> the reason vocabulary shared with Go and iOS (TapToPayReason). */
    fun reason(token: String): String =
        when (token) {
            "connect_incomplete" -> "not_onboarded"
            "operator_cancelled" -> "operator"
            "customer_cancelled" -> "customer"
            "reader_error" -> "read_failed"
            "network" -> "offline"
            else -> token
        }

    private fun verdict(status: String, reason: String?): JSONObject =
        JSONObject().put("status", status).also { if (reason != null) it.put("reason", reason) }

    fun declined(reason: String): String = verdict("declined", reason(reason)).toString()

    fun pending(reason: String): String = verdict("pending", reason(reason)).toString()

    fun cancelled(reason: String): String = verdict("cancelled", reason(reason)).toString()

    /** A start that was refused: nothing was charged, except that this key's earlier payment may have been. */
    fun refused(token: String): String = if (token == "outcome_unknown") pending(token) else declined(token)

    fun result(r: TapResult): String {
        val o =
            when (r.status) {
                // An approval names its provider transaction or it is not an approval.
                TapStatus.SUCCEEDED ->
                    if (r.paymentIntentId.isNullOrEmpty()) verdict("pending", "outcome_unknown") else verdict("approved", null)
                TapStatus.DECLINED -> verdict("declined", reason(r.reason))
                TapStatus.CANCELLED -> verdict("cancelled", reason(r.reason))
                TapStatus.UNKNOWN -> verdict("pending", reason(r.reason))
            }
        r.paymentIntentId?.let { o.put("provider_txn_id", it) }
        r.chargeId?.let { o.put("charge_id", it) }
        r.cardBrand?.let { o.put("card_brand", it) }
        r.last4?.let { o.put("last4", it) }
        r.declineCode?.let { o.put("decline_code", it) }
        return o.toString()
    }

    fun setup(reason: String?): String = JSONObject().put("ok", reason == null).also { if (reason != null) it.put("reason", reason(reason)) }.toString()

    private val setupNeeds = setOf("location_permission", "education_required")

    /** {"tap_to_pay":{"stripe":{ready,state,reason,progress}}} (devicepay.Capabilities). */
    fun capabilities(c: Capability, preparing: Boolean): String {
        val (state, reason) =
            when {
                !c.supported -> "unavailable" to c.reason
                c.ready -> "ready" to null
                preparing -> "preparing" to null
                c.needs.any { it in setupNeeds } -> "needs_setup" to c.needs.first { it in setupNeeds }
                else -> "unavailable" to c.needs.firstOrNull()
            }
        val cap =
            JSONObject().put("ready", c.supported && c.ready).put("state", state)
                .put("progress", if (state == "ready") 100 else 0)
        reason?.let { cap.put("reason", it) }
        return JSONObject().put("tap_to_pay", JSONObject().put(PROVIDER, cap)).toString()
    }
}
