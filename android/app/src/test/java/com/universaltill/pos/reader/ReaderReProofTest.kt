package com.universaltill.pos.reader

import java.io.File
import javax.net.ssl.SSLHandshakeException
import javax.crypto.Mac
import javax.crypto.spec.SecretKeySpec
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** Reader re-proof after a main LAN key change (ADR-0198 §6.2), against the Go fixture reader_proof.json. */
class ReaderReProofTest {
    private val fx: JSONObject = Goldens.file("proof")
    private val bearer = fx.getString("bearer")
    private val hostPort = fx.getString("host_port")
    private val pinSeen = fx.getString("pin_seen")
    private val readerId = fx.getJSONObject("request").getString("till_id")
    private val nonce = fx.getJSONObject("request").getString("nonce")
    private val oldPin = "a".repeat(64)
    private val creds = ReaderCredentials(hostPort, readerId, bearer, oldPin)

    private fun http(answer: PairHttp.Answer = PairHttp.Answer(200, fx.getJSONObject("response").toString(), pinSeen)) =
        FakePairHttp().also { it.postAnswer = answer }

    private fun proof(h: FakePairHttp, now: () -> Long = { 0L }) = PinReProof(h, rand = { nonce }, nowMs = now)

    private fun hmacHex(key: String, msg: String): String {
        val m = Mac.getInstance("HmacSHA256").apply { init(SecretKeySpec(key.toByteArray(Charsets.US_ASCII), "HmacSHA256")) }
        return m.doFinal(msg.toByteArray(Charsets.US_ASCII)).joinToString("") { "%02x".format(it) }
    }

    @Test fun `the fixture proof matches, the seen pin is adopted and nothing else changes`() {
        val r = proof(http()).attempt(creds)
        r as ReProof.Adopted
        assertEquals(pinSeen, r.credentials.pin)
        assertEquals(bearer, r.credentials.bearer)
        assertEquals(readerId, r.credentials.readerId)
        assertEquals(hostPort, r.credentials.address)
    }

    @Test fun `the request is TLS, names the reader id and a fresh nonce, learns the key and carries no bearer`() {
        val h = http()
        proof(h).attempt(creds)
        val (url, body, expectPin) = h.posts.single()
        assertEquals("https://$hostPort/api/sync/primary-proof", url)
        assertNull("the unverified connection learns the pin, it does not enforce one", expectPin)
        val j = JSONObject(body)
        assertEquals(setOf("till_id", "nonce"), j.keys().asSequence().toSet())
        assertEquals(readerId, j.getString("till_id"))
        assertEquals(nonce, j.getString("nonce"))
        assertFalse("bearer never goes on the unverified connection", body.contains(bearer))
    }

    @Test fun `the message layout is label, primary id, reader id, nonce, host and pin, each ending in a line feed`() {
        val primary = "primary-x"
        val key = fx.getString("bearer_hash_hex")
        val good = hmacHex(key, "ut-primary-proof-v1\n$primary\n$readerId\n$nonce\n$hostPort\n$pinSeen\n")
        assertEquals(fx.getJSONObject("response").getJSONObject("data").getString("proof"), hmacHex(key, "ut-primary-proof-v1\nprimary-fixture\n$readerId\n$nonce\n$hostPort\n$pinSeen\n"))
        val body = """{"data":{"primary_till_id":"$primary","proof":"$good"},"error":null}"""
        assertTrue(proof(http(PairHttp.Answer(200, body, pinSeen))).attempt(creds) is ReProof.Adopted)
    }

    @Test fun `a wrong proof keeps the old pin and fails`() {
        val body = """{"data":{"primary_till_id":"primary-fixture","proof":"${"0".repeat(64)}"},"error":null}"""
        assertEquals(ReProof.Mismatch, proof(http(PairHttp.Answer(200, body, pinSeen))).attempt(creds))
    }

    @Test fun `a proof made over another pin than the one the handshake saw fails`() {
        val other = PairHttp.Answer(200, fx.getJSONObject("response").toString(), "c".repeat(64))
        assertEquals(ReProof.Mismatch, proof(http(other)).attempt(creds))
    }

    @Test fun `a proof under another bearer fails`() {
        val c = creds.copy(bearer = "someone-elses-bearer")
        assertEquals(ReProof.Mismatch, proof(http()).attempt(c))
    }

    @Test fun `a proof for another reader id or nonce fails`() {
        val c = creds.copy(readerId = "rd_other")
        assertEquals(ReProof.Mismatch, proof(http()).attempt(c))
        val h = http()
        assertEquals(ReProof.Mismatch, PinReProof(h, rand = { "f".repeat(64) }, nowMs = { 0L }).attempt(creds))
    }

    @Test fun `a prev_proof is accepted for a bearer rotated just now`() {
        val rotatedOld = hmacHex(fx.getString("bearer_hash_hex"), "ut-primary-proof-v1\nprimary-fixture\n$readerId\n$nonce\n$hostPort\n$pinSeen\n")
        val body = """{"data":{"primary_till_id":"primary-fixture","proof":"${"1".repeat(64)}","prev_proof":"$rotatedOld"},"error":null}"""
        assertTrue(proof(http(PairHttp.Answer(200, body, pinSeen))).attempt(creds) is ReProof.Adopted)
    }

    @Test fun `no pin seen, no proof field, garbage body and non-hex proofs are mismatches`() {
        assertEquals(ReProof.Mismatch, proof(http(PairHttp.Answer(200, fx.getJSONObject("response").toString(), null))).attempt(creds))
        assertEquals(ReProof.Mismatch, proof(http(PairHttp.Answer(200, """{"data":{"primary_till_id":"p"}}""", pinSeen))).attempt(creds))
        assertEquals(ReProof.Mismatch, proof(http(PairHttp.Answer(200, "not json", pinSeen))).attempt(creds))
        assertEquals(ReProof.Mismatch, proof(http(PairHttp.Answer(200, """{"data":{"primary_till_id":"p","proof":""}}""", pinSeen))).attempt(creds))
    }

    @Test fun `only a 404 means revoked, no answer and server errors mean unreachable`() {
        assertEquals(ReProof.Revoked, proof(http(PairHttp.Answer(404, "", pinSeen))).attempt(creds))
        assertEquals(ReProof.Unreachable, proof(http(PairHttp.Answer(-1, "", null))).attempt(creds))
        assertEquals(ReProof.Unreachable, proof(http(PairHttp.Answer(500, "", pinSeen))).attempt(creds))
        assertEquals(ReProof.Unreachable, proof(http(PairHttp.Answer(429, "", pinSeen))).attempt(creds))
    }

    @Test fun `at most one attempt per 30 seconds, whatever the outcome`() {
        var t = 1_000L
        val h = http(PairHttp.Answer(200, """{"data":{"primary_till_id":"p","proof":"${"0".repeat(64)}"}}""", pinSeen))
        val p = proof(h) { t }
        assertEquals(ReProof.Mismatch, p.attempt(creds))
        t += 29_999
        assertEquals(ReProof.TooSoon, p.attempt(creds))
        assertEquals(1, h.posts.size)
        t += 1
        assertEquals(ReProof.Mismatch, p.attempt(creds))
        assertEquals(2, h.posts.size)
    }

    @Test fun `a pin mismatch at the handshake is told from other failures`() {
        assertTrue(LinkClose.isPinMismatch(SSLHandshakeException("x").apply { initCause(java.security.cert.CertificateException("not the pinned main till")) }))
        assertFalse(LinkClose.isPinMismatch(java.io.IOException("timeout")))
        assertFalse(LinkClose.isPinMismatch(null))
    }

    @Test fun `the fixture file really is the Go one`() {
        assertTrue(File("../../internal/fleetlink/testdata/reader_proof.json").exists() || File("../internal/fleetlink/testdata/reader_proof.json").exists() || File("internal/fleetlink/testdata/reader_proof.json").exists())
    }
}
