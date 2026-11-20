package com.universaltill.pos.reader

import java.security.MessageDigest
import java.security.SecureRandom
import org.json.JSONObject

/** What the phone keeps after the main approved it (bearer + pin go to Keystore-backed storage, see [ReaderStore]). */
data class ReaderCredentials(val address: String, val readerId: String, val bearer: String, val pin: String) {
    override fun toString() = "ReaderCredentials(address=$address, readerId=$readerId)"
}

sealed class PairStart {
    /** [code] is what the manager compares with the main's screen. */
    data class Waiting(val code: String) : PairStart()

    /** [reason]: needs_tls, refused, not_found (UX C1 error strings). */
    data class Failed(val reason: String) : PairStart()
}

sealed class PairPoll {
    object Waiting : PairPoll()
    object Expired : PairPoll()
    data class Approved(val credentials: ReaderCredentials) : PairPoll()
    data class Failed(val reason: String) : PairPoll()
}

/** HTTPS only. status -1 = no answer (not found), -2 = the pin did not match; [pin] = the leaf key pin the handshake saw. */
interface PairHttp {
    class Answer(val status: Int, val body: String, val pin: String?)

    fun post(url: String, body: String, expectPin: String?): Answer

    fun get(url: String, headers: Map<String, String>, expectPin: String?): Answer
}

/**
 * Enrolment (C1/C2, ADR-0198 §6.2, the same protocol a joining till uses): a random secret stays on the phone, only
 * its sha256 commitment goes out with role `card_reader`; TLS only, the certificate pin seen on the first contact is
 * what makes the verification code, and every poll must meet that same pin. The manager approves on the main
 * after comparing the code; the answer carries the reader's bearer and the pin it must keep dialling.
 */
class ReaderPairing(
    private val http: PairHttp,
    private val rand: () -> String = { randomSecret() },
    private val nowMs: () -> Long,
) {
    companion object {
        const val REQUEST_TTL_MS = 10 * 60_000L
        const val SECRET_HEADER = "X-UT-Pair-Request-Secret"

        private fun randomSecret(): String = ByteArray(32).also { SecureRandom().nextBytes(it) }.joinToString("") { "%02x".format(it) }

        private fun sha256Hex(s: String) = MessageDigest.getInstance("SHA-256").digest(s.toByteArray()).joinToString("") { "%02x".format(it) }

        /** derivedVerificationCode in internal/pages/pairing_api.go: six decimal digits of sha256(commitment + till id + pin). */
        fun code(commitment: String, tillId: String, pin: String): String {
            val h = MessageDigest.getInstance("SHA-256").digest((commitment + tillId + pin).toByteArray())
            val v = ((h[0].toLong() and 0xff) shl 24) or ((h[1].toLong() and 0xff) shl 16) or ((h[2].toLong() and 0xff) shl 8) or (h[3].toLong() and 0xff)
            return "%06d".format(v % 1_000_000L)
        }

        private fun errorOf(body: String): String? = try { JSONObject(body).opt("error") as? String } catch (_: Exception) { null }
    }

    private var base = ""
    private var address = ""
    private var secret = ""
    private var requestId = ""
    private var pin = ""
    private var startedAt = 0L

    /** [tillId] is the main's id from its mDNS record ("id="); the code cannot be made without it. */
    fun start(addr: String, tillId: String, deviceName: String): PairStart {
        address = addr.trim().removePrefix("http://").removePrefix("https://").trimEnd('/')
        base = "https://$address"
        secret = rand()
        val commitment = sha256Hex(secret)
        val body = JSONObject().put("device_name", deviceName).put("commitment", commitment).put("role", "card_reader").toString()
        val a = http.post("$base/api/sync/pair-request", body, null)
        if (a.status == -1) return PairStart.Failed("not_found")
        if (errorOf(a.body) == "tls_required") return PairStart.Failed("needs_tls")
        if (a.status != 200) return PairStart.Failed("refused")
        val seen = a.pin
        if (seen.isNullOrEmpty()) return PairStart.Failed("needs_tls")
        val id = try { JSONObject(a.body).getJSONObject("data").optString("id") } catch (_: Exception) { "" }
        if (id.isEmpty()) return PairStart.Failed("refused")
        requestId = id
        pin = seen
        startedAt = nowMs()
        return PairStart.Waiting(code(commitment, tillId, seen))
    }

    fun poll(): PairPoll {
        if (nowMs() - startedAt > REQUEST_TTL_MS) return PairPoll.Expired
        val a = http.get("$base/api/sync/pair-requests/$requestId", mapOf(SECRET_HEADER to secret), pin)
        return when {
            a.status == 404 || a.status == -1 -> PairPoll.Waiting
            a.status != 200 -> PairPoll.Failed("refused") // -2 (another certificate), a limit, no reader method
            else -> {
                val d = try { JSONObject(a.body).getJSONObject("data") } catch (_: Exception) { return PairPoll.Failed("refused") }
                val rid = d.optString("reader_id")
                val bearer = d.optString("bearer")
                // the key the code was made over, and nothing else, is what this phone will ever trust
                if (rid.isEmpty() || bearer.isEmpty() || d.optString("pin") != pin) PairPoll.Failed("refused")
                else PairPoll.Approved(ReaderCredentials(address, rid, bearer, pin))
            }
        }
    }
}
