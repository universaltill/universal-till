package com.universaltill.pos.reader

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ReaderFramesTest {
    private fun sortedEq(a: JSONObject, b: JSONObject): Boolean =
        a.keys().asSequence().toSet() == b.keys().asSequence().toSet() && a.keys().asSequence().all { a.get(it).toString() == b.get(it).toString() }

    private fun assertPayload(golden: String, got: JSONObject) {
        val want = Goldens.payload(golden)
        assertTrue("payload $got != golden $want", sortedEq(want, got))
    }

    @Test fun `the main hello golden parses`() {
        val e = ReaderFrames.parse(Goldens.text("hello_main"))!!
        assertEquals("hello", e.type)
        assertEquals("main", e.payload.getString("role"))
        assertEquals(1, e.payload.getInt("reader_protocol"))
    }

    @Test fun `the main hello golden during a rotation still parses and carries next_pin (ignored by the phone, ut-docs#6796)`() {
        val e = ReaderFrames.parse(Goldens.text("hello_main_rotating"))!!
        assertEquals("main", e.payload.getString("role"))
        assertEquals(1, e.payload.getInt("reader_protocol"))
        assertTrue(e.payload.getString("next_pin").isNotEmpty())
    }

    @Test fun `the phone hello is exactly the golden`() {
        assertPayload("hello_phone", ReaderFrames.helloPayload("0.36.2"))
    }

    @Test fun `status frames equal the goldens`() {
        assertPayload("status_ready", ReaderFrames.statusPayload(ReaderStatus.READY, null))
        assertPayload("status_needs_setup", ReaderFrames.statusPayload(ReaderStatus.NEEDS_SETUP, "reader_needs_setup"))
    }

    @Test fun `a status reason outside the kept set is never sent`() {
        val p = ReaderFrames.statusPayload(ReaderStatus.UNAVAILABLE, "my secret reason")
        assertFalse(p.has("reason"))
    }

    private fun assertFalse(b: Boolean) = assertTrue(!b)

    @Test fun `the token request is an empty payload`() {
        assertPayload("token_request", ReaderFrames.tokenRequestPayload())
    }

    @Test fun `the token reply golden parses`() {
        val e = ReaderFrames.parse(Goldens.text("token_reply"))!!
        assertEquals("reader.token", e.type)
        assertEquals("p1-4", e.replyTo)
        val t = ReaderFrames.parseToken(e.payload)!!
        assertEquals("pst_test_example_secret", t.secret)
        assertEquals("tml_example", t.locationId)
    }

    @Test fun `a token reply without a secret or a location is refused`() {
        assertNull(ReaderFrames.parseToken(JSONObject("""{"secret":"x"}""")))
        assertNull(ReaderFrames.parseToken(JSONObject("""{"location_id":"tml_1"}""")))
    }

    @Test fun `the collect golden parses`() {
        val e = ReaderFrames.parse(Goldens.text("collect"))!!
        val c = ReaderFrames.parseCollect(e.payload)!!
        assertEquals(1250L, c.amountMinor)
        assertEquals("gbp", c.currency)
        assertEquals("att_7:1:1", c.key)
        assertEquals(110000L, c.timeoutMs)
        assertEquals("tml_example", c.locationId)
        assertEquals("pi_example_secret_example", c.clientSecret)
    }

    @Test fun `a collect with a bad amount, currency, key, provider or timeout is refused`() {
        fun with(k: String, v: Any?) = JSONObject(Goldens.payload("collect").toString()).put(k, v)
        for (bad in listOf(
            with("amount_minor", 0), with("amount_minor", -5), with("amount_minor", 10_000_000), with("amount_minor", "12"),
            with("currency", "pounds"), with("currency", 5), with("idempotency_key", ""), with("idempotency_key", "a b"),
            with("provider", "sumup"), with("timeout_ms", 0), with("timeout_ms", -1), with("timeout_ms", 3_600_000),
            with("client_secret", ""), with("location_id", ""),
        )) assertNull("accepted $bad", ReaderFrames.parseCollect(bad))
    }

    @Test fun `the ack golden`() {
        assertPayload("collect_ack", ReaderFrames.ackPayload())
    }

    @Test fun `the busy error golden`() {
        val g = Goldens.json("collect_busy")
        val text = ReaderFrames.encode("p1-6", "error", ReaderFrames.errorPayload("busy", true), replyTo = "m1-3")
        assertTrue(sortedEq(g, JSONObject(text)))
    }

    @Test fun `the cancel golden parses to its key`() {
        val e = ReaderFrames.parse(Goldens.text("cancel"))!!
        assertEquals("att_7:1:1", ReaderFrames.parseCancelKey(e.payload))
    }

    @Test fun `result frames equal the goldens and carry no other field`() {
        assertPayload("result_approved", ReaderFrames.resultPayload("att_7:1:1", ReaderResult.Approved))
        assertPayload("result_declined", ReaderFrames.resultPayload("att_7:1:1", ReaderResult.Declined("card_declined")))
        assertPayload("result_pending", ReaderFrames.resultPayload("att_7:1:1", ReaderResult.Pending("amount_mismatch")))
        val cancelled = ReaderFrames.resultPayload("k", ReaderResult.Cancelled)
        assertEquals(setOf("idempotency_key", "status"), cancelled.keys().asSequence().toSet())
    }

    @Test fun `a decline code or reason outside the allowed shape is dropped`() {
        assertFalse(ReaderFrames.resultPayload("k", ReaderResult.Declined("Card Number 4242")).has("decline_code"))
        assertFalse(ReaderFrames.resultPayload("k", ReaderResult.Pending("a".repeat(65))).has("reason"))
    }

    @Test fun `parse is strict about the envelope`() {
        assertNull(ReaderFrames.parse("not json"))
        assertNull(ReaderFrames.parse("""{"v":2,"id":"1","type":"hello","payload":{}}"""))
        assertNull(ReaderFrames.parse("""{"v":1,"id":"","type":"hello","payload":{}}"""))
        assertNull(ReaderFrames.parse("""{"v":1,"id":"1","type":"","payload":{}}"""))
        assertNull(ReaderFrames.parse("""{"v":1,"id":"1","type":"hello","payload":[]}"""))
        assertNull(ReaderFrames.parse("""{"v":1,"id":"1","type":"x","payload":{"a":"${"z".repeat(20_000)}"}}"""))
        assertNotNull(ReaderFrames.parse("""{"v":1,"id":"1","type":"ping"}"""))
    }
}
