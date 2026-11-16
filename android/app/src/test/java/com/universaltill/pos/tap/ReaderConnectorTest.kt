package com.universaltill.pos.tap

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class ReaderConnectorTest {
    private class FakeCloud : CloudApi {
        var locationCalls = 0
        var locationError: String? = null
        override fun connectionToken(): String = "tok"
        override fun location(): String {
            locationCalls++
            locationError?.let { throw CloudException(it) }
            return "tml_1"
        }
        override fun status(): String = "{}"
        override fun onboardingLink(): String = "{}"
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
        assertEquals("s2", parseConnectionToken("""{"token":"s2"}"""))
    }

    @Test(expected = CloudException::class) fun emptyTokenIsAnError() {
        parseConnectionToken("""{"secret":""}""")
    }

    @Test(expected = CloudException::class) fun nonJsonTokenIsAnError() {
        parseConnectionToken("garbage")
    }

    @Test fun locationFields() {
        assertEquals("tml_1", parseLocationId("""{"location_id":"tml_1"}"""))
        assertEquals("tml_2", parseLocationId("""{"id":"tml_2"}"""))
        assertEquals("tml_3", parseLocationId(" tml_3 "))
    }

    @Test(expected = CloudException::class) fun emptyLocationIsAnError() {
        parseLocationId("""{}""")
    }
}
