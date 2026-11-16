package com.universaltill.pos.tap

import org.json.JSONArray
import org.json.JSONObject

/** One payment's outcome. No card data field exists on purpose (ADR-0125 3f). */
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

data class TapRequest(
    val id: String,
    val op: String,
    val amountMinor: Long? = null,
    val currency: String? = null,
    val idempotencyKey: String? = null,
    val requestId: String? = null,
    val target: String? = null,
)

sealed class ParsedRequest {
    data class Ok(val req: TapRequest) : ParsedRequest()

    data class Bad(val id: String?, val error: String) : ParsedRequest()
}

/** Strict parsing and encoding of the v1 bridge messages (design section 5). */
object TapToPayMessages {
    const val MAX_BYTES = 2048
    const val MAX_AMOUNT_MINOR = 9_999_999L
    private val ops = setOf("capabilities", "education", "prepare", "start", "cancel", "result", "open_settings")
    private val currencies = setOf("gbp", "eur")
    private val targets = setOf("location", "nfc", "app")
    private val keyRe = Regex("^[A-Za-z0-9:_.-]{1,128}$")
    private val idRe = Regex("^[A-Za-z0-9:_.-]{1,64}$")

    private fun bad(id: String?) = ParsedRequest.Bad(id, "bad_request")

    fun parse(raw: String): ParsedRequest {
        if (raw.toByteArray(Charsets.UTF_8).size > MAX_BYTES) return bad(null)
        val j = try { JSONObject(raw) } catch (_: Exception) { return bad(null) }
        val id = (j.opt("id") as? String)?.takeIf { idRe.matches(it) } ?: return bad(null)
        if (j.opt("v") != 1) return bad(id)
        val op = j.opt("op") as? String ?: return bad(id)
        if (op !in ops) return bad(id)
        when (op) {
            "start" -> {
                val amount = j.opt("amount_minor")
                val a = when (amount) {
                    is Int -> amount.toLong()
                    is Long -> amount
                    else -> return bad(id)
                }
                if (a < 1 || a > MAX_AMOUNT_MINOR) return bad(id)
                val cur = j.opt("currency") as? String ?: return bad(id)
                if (cur !in currencies) return bad(id)
                val key = j.opt("idempotency_key") as? String ?: return bad(id)
                if (!keyRe.matches(key)) return bad(id)
                return ParsedRequest.Ok(TapRequest(id, op, a, cur, key))
            }
            "cancel", "result" -> {
                val rid = j.opt("request_id") as? String ?: return bad(id)
                if (!idRe.matches(rid)) return bad(id)
                return ParsedRequest.Ok(TapRequest(id, op, requestId = rid))
            }
            "open_settings" -> {
                val t = j.opt("target") as? String ?: return bad(id)
                if (t !in targets) return bad(id)
                return ParsedRequest.Ok(TapRequest(id, op, target = t))
            }
        }
        return ParsedRequest.Ok(TapRequest(id, op))
    }

    fun replyOk(id: String): String = JSONObject().put("type", "reply").put("id", id).put("ok", true).toString()

    fun replyError(id: String?, error: String): String =
        JSONObject().put("type", "reply").put("id", id ?: JSONObject.NULL).put("ok", false).put("error", error).toString()

    fun replyCapability(id: String, c: Capability, educationAccepted: Boolean, pending: PendingRecord?): String {
        val cap =
            JSONObject()
                .put("supported", c.supported)
                .put("ready", c.ready)
                .put("reason", c.reason ?: JSONObject.NULL)
                .put("needs", JSONArray(c.needs))
                .put("provider", "stripe")
                .put("education_accepted", educationAccepted)
        if (pending != null) {
            cap.put("pending_unknown", JSONObject().put("request_id", pending.requestId).put("payment_intent_id", pending.intentId))
        }
        return JSONObject().put("type", "reply").put("id", id).put("ok", true).put("capability", cap).toString()
    }

    private fun resultObject(r: TapResult): JSONObject {
        val o = JSONObject().put("type", "result").put("request_id", r.requestId).put("status", r.status.wire).put("reason", r.reason)
        o.put("payment_intent_id", r.paymentIntentId ?: JSONObject.NULL)
        r.chargeId?.let { o.put("charge_id", it) }
        r.declineCode?.let { o.put("decline_code", it) }
        r.cardBrand?.let { o.put("card_brand", it) }
        r.last4?.let { o.put("last4", it) }
        if (r.late) o.put("late", true)
        return o
    }

    fun resultJson(r: TapResult): String = resultObject(r).toString()

    fun replyResult(id: String, r: TapResult?): String {
        val body = if (r == null) JSONObject().put("status", "none") else resultObject(r).also { it.remove("type") }
        return JSONObject().put("type", "reply").put("id", id).put("ok", true).put("result", body).toString()
    }

    fun progressJson(requestId: String, state: String): String =
        JSONObject().put("type", "progress").put("request_id", requestId).put("state", state).toString()

    fun educationEvent(accepted: Boolean): String = JSONObject().put("type", "education").put("accepted", accepted).toString()

    fun prepareEvent(state: String, reason: String?): String =
        JSONObject().put("type", "prepare").put("state", state).also { if (reason != null) it.put("reason", reason) }.toString()
}
