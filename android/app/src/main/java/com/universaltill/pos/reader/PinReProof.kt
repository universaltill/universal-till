package com.universaltill.pos.reader

import java.security.MessageDigest
import java.security.SecureRandom
import javax.crypto.Mac
import javax.crypto.spec.SecretKeySpec
import org.json.JSONObject

sealed class ReProof {
    /** The main proved it holds this phone's bearer over [credentials].pin: the only thing adopted is that pin. */
    data class Adopted(val credentials: ReaderCredentials) : ReProof()

    /** Wrong or missing proof: keep the old pin. */
    object Mismatch : ReProof()

    /** 404: the main removed this phone. The only case that offers "re-pair". */
    object Revoked : ReProof()

    object Unreachable : ReProof()

    /** Less than 30 s since the last attempt. */
    object TooSoon : ReProof()
}

/**
 * Pin-mismatch recovery (ADR-0198 §6.2 "as for tills"). When the main's LAN key changed, the pinned handshake fails
 * and the bearer was never sent. This asks the main to prove it knows the bearer: POST /api/sync/primary-proof over a
 * connection that records the leaf pin it saw (never carrying the bearer), then checks
 * HMAC-SHA256(key = ascii(sha256hex(bearer)), "ut-primary-proof-v1\n" primary_id "\n" reader_id "\n" nonce "\n"
 * host:port "\n" pin_seen "\n"). Only a match adopts pin_seen; at most one attempt per 30 s.
 */
class PinReProof(
    private val http: PairHttp,
    private val rand: () -> String = { ByteArray(32).also { SecureRandom().nextBytes(it) }.joinToString("") { "%02x".format(it) } },
    private val nowMs: () -> Long,
) {
    companion object {
        const val MIN_GAP_MS = 30_000L
        private const val LABEL = "ut-primary-proof-v1"

        private fun hex(b: ByteArray) = b.joinToString("") { "%02x".format(it) }

        private fun sha256Hex(s: String) = hex(MessageDigest.getInstance("SHA-256").digest(s.toByteArray(Charsets.UTF_8)))

        private fun hmacHex(key: String, msg: String): String {
            val m = Mac.getInstance("HmacSHA256").apply { init(SecretKeySpec(key.toByteArray(Charsets.US_ASCII), "HmacSHA256")) }
            return hex(m.doFinal(msg.toByteArray(Charsets.UTF_8)))
        }
    }

    private var lastAt: Long? = null

    fun attempt(c: ReaderCredentials, address: String = c.address): ReProof {
        val now = nowMs()
        lastAt?.let { if (now - it < MIN_GAP_MS) return ReProof.TooSoon }
        lastAt = now
        val hostPort = address.trim().removePrefix("http://").removePrefix("https://").trimEnd('/')
        val nonce = rand()
        val body = JSONObject().put("till_id", c.readerId).put("nonce", nonce).toString()
        val a = http.post("https://$hostPort/api/sync/primary-proof", body, null)
        if (a.status == 404) return ReProof.Revoked
        if (a.status != 200) return ReProof.Unreachable
        val seen = a.pin
        if (seen.isNullOrEmpty()) return ReProof.Mismatch
        val d = try { JSONObject(a.body).getJSONObject("data") } catch (_: Exception) { return ReProof.Mismatch }
        val primary = d.optString("primary_till_id")
        if (primary.isEmpty()) return ReProof.Mismatch
        val want = hmacHex(sha256Hex(c.bearer), "$LABEL\n$primary\n${c.readerId}\n$nonce\n$hostPort\n$seen\n")
        // both, so a rotated bearer's grace proof counts; constant-time compare
        val ok = listOf(d.optString("proof"), d.optString("prev_proof")).fold(false) { acc, got ->
            MessageDigest.isEqual(want.toByteArray(), got.toByteArray()) || acc
        }
        return if (ok) ReProof.Adopted(c.copy(address = hostPort, pin = seen)) else ReProof.Mismatch
    }
}
