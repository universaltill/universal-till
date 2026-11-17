package com.universaltill.pos.tap

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ReaderConnectorTest {
    private class FakeCloud : CloudApi {
        var locationCalls = 0
        var locationError: String? = null
        override fun connectionToken(): String = "tok"
        override fun onboardingStatus(): String {
            locationCalls++
            locationError?.let { throw CloudException(it) }
            return """{"onboarded":true,"charges_enabled":true,"location_id":"tml_1"}"""
        }
        override fun sessionCreate(amountMinor: Long, currency: String, key: String): String = "{}"
    }

    @Test fun locationIsFetchedOnceThenKeptInMemory() {
        val cloud = FakeCloud()
        val seen = mutableListOf<String>()
        val c = ReaderConnector(cloud) { loc, cb -> seen += loc; cb(null) }
        var r: String? = "x"
        c.connect { r = it }; c.connect { r = it }
        assertNull(r); assertEquals(1, cloud.locationCalls); assertEquals(listOf("tml_1", "tml_1"), seen)
    }

    @Test fun cloudErrorsBecomeCodes() {
        val cloud = FakeCloud().also { it.locationError = "connect_incomplete" }
        var r: String? = null
        ReaderConnector(cloud) { _, cb -> cb(null) }.connect { r = it }
        assertEquals("connect_incomplete", r)
    }

    @Test fun unknownCloudFailureBecomesTheDocumentedNotReady() {
        val cloud = FakeCloud().also { it.locationError = "cloud_error" }
        var r: String? = null
        ReaderConnector(cloud) { _, cb -> cb(null) }.connect { r = it }
        assertEquals("not_ready", r)
        val off = FakeCloud().also { it.locationError = "offline: cloud not reached" }
        ReaderConnector(off) { _, cb -> cb(null) }.connect { r = it }
        assertEquals("offline", r)
    }

    @Test fun sessionExpiredReconnectsExactlyOnce() {
        var attempts = 0
        val c = ReaderConnector(FakeCloud()) { _, cb -> attempts++; cb("SESSION_EXPIRED") }
        var r: String? = null
        c.connect { r = it }
        assertEquals(2, attempts); assertEquals("reader_error", r)
        attempts = 0
        val ok = ReaderConnector(FakeCloud()) { _, cb -> attempts++; cb(if (attempts == 1) "SESSION_EXPIRED" else null) }
        ok.connect { r = it }
        assertEquals(2, attempts); assertNull(r)
    }

    @Test fun sdkErrorsAreMappedToReasons() {
        var r: String? = null
        ReaderConnector(FakeCloud()) { _, cb -> cb("TAP_TO_PAY_INSECURE_ENVIRONMENT") }.connect { r = it }
        assertEquals("insecure_environment", r)
    }
}

class CloudParsingTest {
    @Test fun connectionTokenFields() {
        assertEquals("s1", parseConnectionToken("""{"secret":"s1"}"""))
    }

    @Test(expected = CloudException::class) fun oldTokenFieldIsNotAccepted() {
        parseConnectionToken("""{"token":"s2"}""")
    }

    @Test(expected = CloudException::class) fun emptyTokenIsAnError() {
        parseConnectionToken("""{"secret":""}""")
    }

    @Test(expected = CloudException::class) fun nonJsonTokenIsAnError() {
        parseConnectionToken("garbage")
    }

    @Test fun locationNeedsOnboardedChargesEnabledAndLocation() {
        assertEquals("tml_1", parseLocationId("""{"onboarded":true,"charges_enabled":true,"location_id":"tml_1"}"""))
        for (bad in listOf(
            """{"onboarded":true,"charges_enabled":true}""",
            """{"onboarded":true,"charges_enabled":true,"location_id":""}""",
            """{"onboarded":false,"charges_enabled":true,"location_id":"tml_1"}""",
            """{"onboarded":true,"charges_enabled":false,"location_id":"tml_1"}""",
            """{"location_id":"tml_1"}""",
        )) {
            val e = try { parseLocationId(bad); null } catch (e: CloudException) { e }
            assertEquals(bad, "connect_incomplete", e?.code)
        }
    }

    @Test(expected = CloudException::class) fun nonJsonLocationIsAnError() {
        parseLocationId("tml_3")
    }

    @Test fun sessionReply() {
        val s = parseSession("""{"client_secret":"pi_1_secret_x","amount_minor":250,"currency":"gbp","status":"requires_payment_method"}""")
        assertEquals(CloudSession("pi_1_secret_x", 250L, "gbp"), s)
        assertEquals(CloudSession("pi_2_secret_y", null, null), parseSession("""{"client_secret":"pi_2_secret_y"}"""))
    }

    @Test(expected = CloudException::class) fun sessionWithoutSecretIsAnError() {
        parseSession("""{"amount_minor":250}""")
    }
}

class ConnectAttemptTest {
    private fun attempt(sched: FakeScheduler, log: MutableList<String?>, aborts: IntArray) =
        ConnectAttempt(sched, ConnectAttempt.TIMEOUT_MS, { aborts[0]++ }) { log += it ?: "ok" }

    @Test fun aDiscoveryThatNeverEndsIsAbortedAndFailsAsReaderError() {
        val sched = FakeScheduler(); val log = mutableListOf<String?>(); val aborts = intArrayOf(0)
        val a = attempt(sched, log, aborts)
        sched.advance(ConnectAttempt.TIMEOUT_MS - 1)
        assertTrue(log.isEmpty())
        sched.advance(1)
        assertEquals(listOf<String?>("reader_error"), log)
        assertEquals(1, aborts[0])
        assertTrue(a.finished)
    }

    @Test fun aLateSdkAnswerAfterTheTimeoutIsIgnored() {
        val sched = FakeScheduler(); val log = mutableListOf<String?>(); val aborts = intArrayOf(0)
        val a = attempt(sched, log, aborts)
        sched.advance(ConnectAttempt.TIMEOUT_MS)
        a.end(null)
        assertEquals(listOf<String?>("reader_error"), log)
    }

    @Test fun anAnswerInTimeCancelsTheTimerAndNeverAborts() {
        val sched = FakeScheduler(); val log = mutableListOf<String?>(); val aborts = intArrayOf(0)
        val a = attempt(sched, log, aborts)
        a.end(null)
        a.end("later")
        sched.advance(ConnectAttempt.TIMEOUT_MS * 2)
        assertEquals(listOf<String?>("ok"), log)
        assertEquals(0, aborts[0])
    }
}
