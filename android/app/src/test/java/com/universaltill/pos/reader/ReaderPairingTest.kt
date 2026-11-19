package com.universaltill.pos.reader

import java.security.MessageDigest
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class FakePairHttp : PairHttp {
    var learnedPin: String? = "b".repeat(64)
    val posts = mutableListOf<Triple<String, String, String?>>()
    val gets = mutableListOf<Triple<String, Map<String, String>, String?>>()
    var postAnswer = PairHttp.Answer(200, """{"data":{"id":"req-1","pending_link":true},"error":null}""", "b".repeat(64))
    val getAnswers = ArrayDeque<PairHttp.Answer>()

    override fun post(url: String, body: String, expectPin: String?): PairHttp.Answer {
        posts += Triple(url, body, expectPin)
        return postAnswer
    }

    override fun get(url: String, headers: Map<String, String>, expectPin: String?): PairHttp.Answer {
        gets += Triple(url, headers, expectPin)
        return if (getAnswers.isEmpty()) PairHttp.Answer(404, "", expectPin) else getAnswers.removeFirst()
    }
}

class ReaderPairingTest {
    private val sha = { s: String -> MessageDigest.getInstance("SHA-256").digest(s.toByteArray()).joinToString("") { "%02x".format(it) } }
    private fun newPairing(h: FakePairHttp, now: () -> Long = { 0L }) =
        ReaderPairing(h, rand = { "s".repeat(64) }, nowMs = now)

    @Test fun `the verification code is the Go one, six digits over commitment, till id and pin`() {
        // python: sha256("a"*64 + "till-1" + "b"*64)[:4] big-endian % 1_000_000
        assertEquals("834893", ReaderPairing.code("a".repeat(64), "till-1", "b".repeat(64)))
    }

    @Test fun `the request is TLS, names the card_reader role and sends only the commitment of its secret`() {
        val h = FakePairHttp()
        val st = newPairing(h).start("192.168.1.50:8080", "till-9", "Pat's phone")
        assertTrue(st is PairStart.Waiting)
        val (url, body, pinExpected) = h.posts.single()
        assertEquals("https://192.168.1.50:8080/api/sync/pair-request", url)
        assertEquals(null, pinExpected)
        val j = JSONObject(body)
        assertEquals("card_reader", j.getString("role"))
        assertEquals("Pat's phone", j.getString("device_name"))
        assertEquals(sha("s".repeat(64)), j.getString("commitment"))
        assertTrue(!body.contains("s".repeat(64)))
        val w = st as PairStart.Waiting
        assertEquals(ReaderPairing.code(sha("s".repeat(64)), "till-9", "b".repeat(64)), w.code)
    }

    @Test fun `plain http addresses are upgraded, a pair request never goes in the clear`() {
        val h = FakePairHttp()
        newPairing(h).start("http://192.168.1.50:8080", "t", "n")
        assertTrue(h.posts.single().first.startsWith("https://"))
    }

    @Test fun `no pin seen means no code, and the pair is refused`() {
        val h = FakePairHttp().also { it.postAnswer = PairHttp.Answer(200, """{"data":{"id":"r"},"error":null}""", null) }
        assertEquals(PairStart.Failed("needs_tls"), newPairing(h).start("1.2.3.4:8080", "t", "n"))
    }

    @Test fun `server refusals map to the screens words`() {
        val h = FakePairHttp()
        h.postAnswer = PairHttp.Answer(403, """{"data":null,"error":"reader_pairing_off"}""", "b".repeat(64))
        assertEquals(PairStart.Failed("refused"), newPairing(h).start("1.2.3.4:8080", "t", "n"))
        h.postAnswer = PairHttp.Answer(400, """{"data":null,"error":"tls_required"}""", null)
        assertEquals(PairStart.Failed("needs_tls"), newPairing(h).start("1.2.3.4:8080", "t", "n"))
        h.postAnswer = PairHttp.Answer(-1, "", null)
        assertEquals(PairStart.Failed("not_found"), newPairing(h).start("1.2.3.4:8080", "t", "n"))
        h.postAnswer = PairHttp.Answer(429, "", "b".repeat(64))
        assertEquals(PairStart.Failed("refused"), newPairing(h).start("1.2.3.4:8080", "t", "n"))
    }

    @Test fun `polling carries the secret in the header, enforces the pin seen, and waits on 404`() {
        val h = FakePairHttp()
        val p = newPairing(h)
        p.start("1.2.3.4:8080", "t", "n")
        assertEquals(PairPoll.Waiting, p.poll())
        val (url, headers, pin) = h.gets.single()
        assertEquals("https://1.2.3.4:8080/api/sync/pair-requests/req-1", url)
        assertEquals("s".repeat(64), headers["X-UT-Pair-Request-Secret"])
        assertEquals("b".repeat(64), pin)
        assertTrue(!url.contains("s".repeat(64)))
    }

    @Test fun `approval returns the credentials, bound to the pin seen at the request`() {
        val h = FakePairHttp()
        val p = newPairing(h)
        p.start("1.2.3.4:8080", "t", "n")
        h.getAnswers += PairHttp.Answer(200, """{"data":{"reader_id":"rid-1","bearer":"bb","pin":"${"b".repeat(64)}"},"error":null}""", "b".repeat(64))
        assertEquals(PairPoll.Approved(ReaderCredentials("1.2.3.4:8080", "rid-1", "bb", "b".repeat(64))), p.poll())
    }

    @Test fun `a pin in the answer that differs from the one the code was made on is refused`() {
        val h = FakePairHttp()
        val p = newPairing(h)
        p.start("1.2.3.4:8080", "t", "n")
        h.getAnswers += PairHttp.Answer(200, """{"data":{"reader_id":"r","bearer":"bb","pin":"${"c".repeat(64)}"},"error":null}""", "b".repeat(64))
        assertEquals(PairPoll.Failed("refused"), p.poll())
    }

    @Test fun `no answer for 10 minutes is expired, and a poll through another certificate is not a success`() {
        var now = 0L
        val h = FakePairHttp()
        val p = newPairing(h) { now }
        p.start("1.2.3.4:8080", "t", "n")
        now = 10 * 60_000L + 1
        assertEquals(PairPoll.Expired, p.poll())
        val q = newPairing(h) { 0L }
        q.start("1.2.3.4:8080", "t", "n")
        h.getAnswers += PairHttp.Answer(-2, "", null) // pin mismatch from the transport
        assertEquals(PairPoll.Failed("refused"), q.poll())
    }

    @Test fun `no reader limit or reader method on the main is a clear refusal`() {
        val h = FakePairHttp()
        val p = newPairing(h)
        p.start("1.2.3.4:8080", "t", "n")
        h.getAnswers += PairHttp.Answer(409, """{"data":null,"error":"reader_limit"}""", "b".repeat(64))
        assertEquals(PairPoll.Failed("refused"), p.poll())
    }
}
