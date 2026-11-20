package com.universaltill.pos.reader

import org.json.JSONObject

/**
 * The reader link's frames (ut-docs#6538, ADR-0198 §6, dev-r1b "Frame shapes"). Mirror of
 * internal/fleetlink/reader_frames.go; the shared fixtures internal/fleetlink/testdata/reader_*.json pin
 * both sides. Strict on the way in (the main is the only sender, anything odd is dropped), and on the way out only
 * the fields the main's strict decoder accepts: there is no field a card number, brand or last four could go in
 * (ADR-0127).
 */
enum class ReaderStatus(val wire: String) { READY("ready"), NEEDS_SETUP("needs_setup"), BUSY("busy"), UNAVAILABLE("unavailable") }

sealed class ReaderResult {
    object Approved : ReaderResult()

    data class Declined(val declineCode: String?) : ReaderResult()

    object Cancelled : ReaderResult()

    data class Pending(val reason: String?) : ReaderResult()
}

/** One payment the main asks for. [clientSecret] belongs in this object only: never logged, persisted or shown. */
class ReaderCollect(
    val amountMinor: Long,
    val currency: String,
    val key: String,
    val timeoutMs: Long,
    val clientSecret: String,
    val locationId: String,
) {
    override fun toString() = "ReaderCollect(amountMinor=$amountMinor, currency=$currency, key=$key, timeoutMs=$timeoutMs)"
}

data class Token(val secret: String, val locationId: String) {
    override fun toString() = "Token(location=$locationId)"
}

class Envelope(val id: String, val type: String, val replyTo: String?, val payload: JSONObject)

object ReaderFrames {
    const val MAX_FRAME_CHARS = 16 * 1024
    const val PROTOCOL = 1
    const val MAX_AMOUNT_MINOR = 9_999_999L
    const val MAX_TIMEOUT_MS = 600_000L
    private val keyRe = Regex("^[A-Za-z0-9_.:-]{1,128}$")
    private val tokenRe = Regex("^[a-z0-9_]{1,64}$")
    private val currencyRe = Regex("^[A-Za-z]{3}$")
    private val keptReasons = setOf("reader_busy", "reader_unreachable", "reader_needs_setup")

    fun parse(text: String): Envelope? {
        if (text.length > MAX_FRAME_CHARS) return null
        val j = try { JSONObject(text) } catch (_: Exception) { return null }
        if (j.opt("v") != 1) return null
        val id = j.opt("id") as? String ?: return null
        val type = j.opt("type") as? String ?: return null
        if (id.isEmpty() || type.isEmpty()) return null
        val p = if (j.has("payload")) (j.opt("payload") as? JSONObject ?: return null) else JSONObject()
        val reply = j.opt("reply_to") as? String
        return Envelope(id, type, reply?.ifEmpty { null }, p)
    }

    fun encode(id: String, type: String, payload: JSONObject, replyTo: String? = null): String {
        val j = JSONObject().put("v", 1).put("id", id).put("type", type)
        if (replyTo != null) j.put("reply_to", replyTo)
        return j.put("payload", payload).toString()
    }

    fun helloPayload(appVersion: String): JSONObject =
        JSONObject().put("role", "card_reader").put("reader_protocol", PROTOCOL).put("app_version", appVersion).put("platform", "android")

    fun statusPayload(state: ReaderStatus, reason: String?): JSONObject {
        val p = JSONObject().put("state", state.wire)
        if (reason != null && reason in keptReasons) p.put("reason", reason)
        return p
    }

    fun tokenRequestPayload() = JSONObject()

    fun ackPayload() = JSONObject().put("accepted", true)

    fun errorPayload(code: String, retryable: Boolean) = JSONObject().put("code", code).put("retryable", retryable)

    fun resultPayload(key: String, r: ReaderResult): JSONObject {
        val p = JSONObject().put("idempotency_key", key)
        when (r) {
            ReaderResult.Approved -> p.put("status", "approved")
            is ReaderResult.Declined -> {
                p.put("status", "declined")
                r.declineCode?.takeIf { tokenRe.matches(it) }?.let { p.put("decline_code", it) }
            }
            ReaderResult.Cancelled -> p.put("status", "cancelled")
            is ReaderResult.Pending -> {
                p.put("status", "pending")
                r.reason?.takeIf { tokenRe.matches(it) }?.let { p.put("reason", it) }
            }
        }
        return p
    }

    fun parseToken(p: JSONObject): Token? {
        val s = p.opt("secret") as? String ?: return null
        val l = p.opt("location_id") as? String ?: return null
        if (s.isEmpty() || l.isEmpty() || s.length > 512 || l.length > 128) return null
        return Token(s, l)
    }

    fun parseCollect(p: JSONObject): ReaderCollect? {
        if (p.opt("provider") != "stripe") return null
        val amount = (p.opt("amount_minor") as? Number)?.takeIf { it is Int || it is Long }?.toLong() ?: return null
        if (amount < 1 || amount > MAX_AMOUNT_MINOR) return null
        val cur = p.opt("currency") as? String ?: return null
        if (!currencyRe.matches(cur)) return null
        val key = p.opt("idempotency_key") as? String ?: return null
        if (!keyRe.matches(key)) return null
        val timeout = (p.opt("timeout_ms") as? Number)?.takeIf { it is Int || it is Long }?.toLong() ?: return null
        if (timeout < 1 || timeout > MAX_TIMEOUT_MS) return null
        val secret = p.opt("client_secret") as? String ?: return null
        val loc = p.opt("location_id") as? String ?: return null
        if (secret.isEmpty() || secret.length > 512 || loc.isEmpty() || loc.length > 128) return null
        return ReaderCollect(amount, cur.lowercase(), key, timeout, secret, loc)
    }

    fun parseCancelKey(p: JSONObject): String? = (p.opt("idempotency_key") as? String)?.takeIf { keyRe.matches(it) }
}
