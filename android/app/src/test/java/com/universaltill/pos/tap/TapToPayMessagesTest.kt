package com.universaltill.pos.tap

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class TapToPayMessagesTest {
    private fun start(amount: String = "1250", cur: String = "\"gbp\"", key: String = "\"ut:sale-1\"") =
        """{"v":1,"id":"r1","op":"start","amount_minor":$amount,"currency":$cur,"idempotency_key":$key}"""

    private fun ok(raw: String) = TapToPayMessages.parse(raw) as ParsedRequest.Ok
    private fun bad(raw: String) = TapToPayMessages.parse(raw) as ParsedRequest.Bad

    @Test fun validStart() {
        val r = ok(start()).req
        assertEquals("start", r.op); assertEquals(1250L, r.amountMinor); assertEquals("gbp", r.currency)
        assertEquals("ut:sale-1", r.idempotencyKey); assertEquals("r1", r.id)
    }

    @Test fun sizeCapIs2048Bytes() {
        val pad = "x".repeat(2100)
        assertEquals("bad_request", bad("""{"v":1,"id":"r1","op":"capabilities","pad":"$pad"}""").error)
    }

    @Test fun unknownOpAndBadVersionAndNotJson() {
        assertEquals("bad_request", bad("""{"v":1,"id":"r1","op":"format_disk"}""").error)
        assertEquals("bad_request", bad("""{"v":2,"id":"r1","op":"capabilities"}""").error)
        assertEquals("bad_request", bad("nope").error)
        assertEquals("bad_request", bad("""{"id":"r1","op":"capabilities"}""").error)
    }

    @Test fun amountBounds() {
        assertEquals("bad_request", bad(start(amount = "0")).error)
        assertEquals("bad_request", bad(start(amount = "-1")).error)
        assertEquals("bad_request", bad(start(amount = "10000000")).error)
        assertEquals("bad_request", bad(start(amount = "12.5")).error)
        assertEquals("bad_request", bad(start(amount = "\"1250\"")).error)
        assertEquals(9_999_999L, ok(start(amount = "9999999")).req.amountMinor)
        assertEquals(1L, ok(start(amount = "1")).req.amountMinor)
    }

    @Test fun currencyAllowlist() {
        assertEquals("bad_request", bad(start(cur = "\"usd\"")).error)
        assertEquals("bad_request", bad(start(cur = "\"GBP\"")).error)
        assertEquals("eur", ok(start(cur = "\"eur\"")).req.currency)
    }

    @Test fun keyCharsetAndLength() {
        assertEquals("bad_request", bad(start(key = "\"\"")).error)
        assertEquals("bad_request", bad(start(key = "\"a b\"")).error)
        assertEquals("bad_request", bad(start(key = "\"a;b\"")).error)
        assertEquals("bad_request", bad(start(key = "\"" + "k".repeat(129) + "\"")).error)
        assertEquals("k".repeat(128), ok(start(key = "\"" + "k".repeat(128) + "\"")).req.idempotencyKey)
    }

    @Test fun requestIdIsValidated() {
        assertEquals("bad_request", bad("""{"v":1,"id":"","op":"capabilities"}""").error)
        assertEquals("bad_request", bad("""{"v":1,"id":"<script>","op":"capabilities"}""").error)
    }

    @Test fun extraKeysAreIgnored() {
        val r = ok("""{"v":1,"id":"r1","op":"capabilities","card_number":"4242","url":"http://x"}""").req
        assertEquals("capabilities", r.op)
    }

    @Test fun cancelAndResultNeedRequestId() {
        assertEquals("bad_request", bad("""{"v":1,"id":"r1","op":"cancel"}""").error)
        assertEquals("rq-9", ok("""{"v":1,"id":"r1","op":"result","request_id":"rq-9"}""").req.requestId)
    }

    @Test fun openSettingsTargetAllowlist() {
        assertEquals("nfc", ok("""{"v":1,"id":"r1","op":"open_settings","target":"nfc"}""").req.target)
        assertEquals("bad_request", bad("""{"v":1,"id":"r1","op":"open_settings","target":"http://evil"}""").error)
        assertEquals("bad_request", bad("""{"v":1,"id":"r1","op":"open_settings"}""").error)
    }

    @Test fun badReplyStillCarriesTheIdWhenKnown() {
        assertEquals("r1", bad("""{"v":1,"id":"r1","op":"nope"}""").id)
    }

    private val allowed = setOf("type", "request_id", "status", "reason", "payment_intent_id", "charge_id", "decline_code", "card_brand", "last4", "late")

    @Test fun resultJsonHasOnlyAllowedKeysAndNoCardData() {
        val r = TapResult("r1", TapStatus.SUCCEEDED, "ok", "pi_1", "ch_1", null, "visa", "4242", false)
        val j = JSONObject(TapToPayMessages.resultJson(r))
        assertTrue(allowed.containsAll(j.keys().asSequence().toSet()))
        assertEquals("result", j.getString("type")); assertEquals("succeeded", j.getString("status"))
        assertFalse(j.has("late"))
        for (banned in listOf("pan", "number", "expiry", "cvc", "track", "pin", "client_secret", "secret")) assertFalse(j.has(banned))
    }

    @Test fun unknownNeverSerialisesAsDeclined() {
        val j = JSONObject(TapToPayMessages.resultJson(TapResult("r1", TapStatus.UNKNOWN, "timeout", "pi_1", null, null, null, null, false)))
        assertEquals("unknown", j.getString("status"))
        assertEquals(setOf("succeeded", "declined", "cancelled", "unknown"), TapStatus.values().map { it.wire }.toSet())
    }

    @Test fun lateFlagSerialises() {
        val j = JSONObject(TapToPayMessages.resultJson(TapResult("r1", TapStatus.SUCCEEDED, "ok", "pi_1", null, null, null, null, true)))
        assertTrue(j.getBoolean("late"))
    }

    @Test fun replyErrorAndOk() {
        val e = JSONObject(TapToPayMessages.replyError("r1", "busy"))
        assertEquals("reply", e.getString("type")); assertFalse(e.getBoolean("ok")); assertEquals("busy", e.getString("error"))
        assertTrue(JSONObject(TapToPayMessages.replyOk("r1")).getBoolean("ok"))
    }
}
