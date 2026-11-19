package com.universaltill.pos.tap

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class DevicepayCodecTest {
    private fun req(extra: String = "", amount: String = "1250", currency: String = "\"GBP\"", key: String = "\"sale-1\"", provider: String = "\"stripe\"") =
        """{"provider":$provider,"amount_minor":$amount,"currency":$currency,"idempotency_key":$key,"deadline_unix_ms":1700000000000$extra}"""

    @Test fun parsesTheRequestExactlyAsGoSendsIt() {
        val r = DevicepayCodec.parseRequest(req(""","description":"Sale 1""""))!!
        assertEquals(CollectRequest(1250, "gbp", "sale-1", 1_700_000_000_000L), r)
    }

    @Test fun refusesAnythingGoWouldNeverSend() {
        assertNull(DevicepayCodec.parseRequest(null))
        assertNull(DevicepayCodec.parseRequest("not json"))
        assertNull(DevicepayCodec.parseRequest(req(provider = "\"sumup\"")))
        assertNull(DevicepayCodec.parseRequest(req(amount = "0")))
        assertNull(DevicepayCodec.parseRequest(req(amount = "-5")))
        assertNull(DevicepayCodec.parseRequest(req(amount = "10000000")))
        assertNull(DevicepayCodec.parseRequest(req(amount = "\"1250\"")))
        assertNull(DevicepayCodec.parseRequest(req(amount = "12.5")))
        assertNull(DevicepayCodec.parseRequest(req(currency = "\"pounds\"")))
        assertNull(DevicepayCodec.parseRequest(req(key = "\"\"")))
        assertNull(DevicepayCodec.parseRequest(req(key = "\"a b\"")))
        assertNull(DevicepayCodec.parseRequest(req(""","card_number":"4242""""))) // unknown field
        assertNull(DevicepayCodec.parseRequest(req(" ".repeat(DevicepayCodec.MAX_BYTES))))
        assertNull(DevicepayCodec.parseRequest("""{"provider":"stripe","amount_minor":5,"currency":"gbp","idempotency_key":"k"}""")) // no deadline
    }

    private fun result(status: TapStatus, reason: String = "ok", pi: String? = "pi_1", charge: String? = null, decline: String? = null, brand: String? = null, last4: String? = null) =
        JSONObject(DevicepayCodec.result(TapResult("r1", status, reason, pi, charge, decline, brand, last4, false)))

    @Test fun succeededIsApprovedAndNamesItsProviderTransaction() {
        val o = result(TapStatus.SUCCEEDED, charge = "ch_1", brand = "visa", last4 = "4242")
        assertEquals("approved", o.getString("status")); assertEquals("pi_1", o.getString("provider_txn_id"))
        assertEquals("ch_1", o.getString("charge_id")); assertEquals("visa", o.getString("card_brand")); assertEquals("4242", o.getString("last4"))
    }

    @Test fun anApprovalWithoutAProviderIdIsPendingNeverApproved() {
        val o = result(TapStatus.SUCCEEDED, pi = null)
        assertEquals("pending", o.getString("status")); assertEquals("outcome_unknown", o.getString("reason"))
        assertFalse(o.has("provider_txn_id"))
    }

    @Test fun unknownIsPendingNeverDeclined() {
        val o = result(TapStatus.UNKNOWN, reason = "timeout")
        assertEquals("pending", o.getString("status")); assertEquals("timeout", o.getString("reason")); assertEquals("pi_1", o.getString("provider_txn_id"))
    }

    @Test fun declinedAndCancelledKeepTheirMeaningWithSharedReasons() {
        val d = result(TapStatus.DECLINED, reason = "declined_by_issuer", decline = "insufficient_funds")
        assertEquals("declined", d.getString("status")); assertEquals("insufficient_funds", d.getString("decline_code"))
        assertEquals("operator", result(TapStatus.CANCELLED, "operator_cancelled").getString("reason"))
        assertEquals("customer", result(TapStatus.CANCELLED, "customer_cancelled").getString("reason"))
        assertEquals("cancelled", result(TapStatus.CANCELLED, "timeout").getString("status"))
        assertEquals("read_failed", result(TapStatus.DECLINED, "reader_error").getString("reason"))
    }

    @Test fun onlyFieldsGoAcceptsAreEverProduced() {
        val allowed = setOf("status", "provider_txn_id", "charge_id", "card_brand", "last4", "decline_code", "reason")
        for (s in TapStatus.values()) {
            val o = result(s, charge = "c", decline = "d", brand = "b", last4 = "1234")
            assertTrue("${s}: ${o.keys().asSequence().toList()}", o.keys().asSequence().all { it in allowed })
        }
    }

    @Test fun refusedStartIsDeclinedExceptWhenThisKeyMayHaveBeenCharged() {
        val d = JSONObject(DevicepayCodec.refused("busy"))
        assertEquals("declined", d.getString("status")); assertEquals("busy", d.getString("reason"))
        assertEquals("not_onboarded", JSONObject(DevicepayCodec.refused("connect_incomplete")).getString("reason"))
        val p = JSONObject(DevicepayCodec.refused("outcome_unknown"))
        assertEquals("pending", p.getString("status")); assertEquals("outcome_unknown", p.getString("reason"))
    }

    @Test fun setupAnswers() {
        assertEquals("""{"ok":true}""", DevicepayCodec.setup(null))
        val o = JSONObject(DevicepayCodec.setup("location_denied"))
        assertFalse(o.getBoolean("ok")); assertEquals("location_denied", o.getString("reason"))
    }

    private fun cap(c: Capability, preparing: Boolean = false) =
        JSONObject(DevicepayCodec.capabilities(c, preparing)).getJSONObject("tap_to_pay").getJSONObject("stripe")

    @Test fun capabilitiesShape() {
        val ready = cap(TapToPayCapability.evaluate(readyFacts()))
        assertTrue(ready.getBoolean("ready")); assertEquals("ready", ready.getString("state")); assertEquals(100, ready.getInt("progress"))
        assertFalse(ready.has("reason"))
        val none = cap(TapToPayCapability.evaluate(readyFacts().copy(hasNfc = false)))
        assertFalse(none.getBoolean("ready")); assertEquals("unavailable", none.getString("state")); assertEquals("no_nfc", none.getString("reason"))
        val setup = cap(TapToPayCapability.evaluate(readyFacts().copy(locationPermission = false)))
        assertEquals("needs_setup", setup.getString("state")); assertEquals("location_permission", setup.getString("reason"))
        val edu = cap(TapToPayCapability.evaluate(readyFacts().copy(educationAccepted = false)))
        assertEquals("needs_setup", edu.getString("state")); assertEquals("education_required", edu.getString("reason"))
        val off = cap(TapToPayCapability.evaluate(readyFacts().copy(nfcEnabled = false)))
        assertEquals("unavailable", off.getString("state")); assertEquals("nfc_off", off.getString("reason"))
        val busy = cap(TapToPayCapability.evaluate(readyFacts().copy(locationPermission = false)), preparing = true)
        assertEquals("preparing", busy.getString("state")); assertFalse(busy.getBoolean("ready"))
        val flagOff = cap(TapToPayCapability.evaluate(readyFacts().copy(flagOn = false)))
        assertFalse(flagOff.getBoolean("ready")); assertEquals("flag_off", flagOff.getString("reason"))
    }
}
