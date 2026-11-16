package com.universaltill.pos.tap

import org.junit.Assert.assertEquals
import org.junit.Test

class StripeErrorMapperTest {
    @Test fun connectErrors() {
        assertEquals("insecure_environment", StripeErrorMapper.reason("TAP_TO_PAY_INSECURE_ENVIRONMENT"))
        assertEquals("insecure_environment", StripeErrorMapper.reason("TAP_TO_PAY_DEVICE_TAMPERED"))
        assertEquals("network", StripeErrorMapper.reason("STRIPE_API_CONNECTION_ERROR"))
        assertEquals("network", StripeErrorMapper.reason("NETWORK_ERROR"))
        assertEquals("reader_error", StripeErrorMapper.reason("SOMETHING_NEW"))
    }

    @Test fun customerCancelIsNotOperatorCancel() {
        assertEquals(CancelKind.CUSTOMER, StripeErrorMapper.cancelKind("CANCELED_BY_READER"))
        assertEquals(CancelKind.OPERATOR, StripeErrorMapper.cancelKind("CANCELED"))
        assertEquals(CancelKind.NONE, StripeErrorMapper.cancelKind("READER_ERROR"))
    }

    @Test fun sessionExpiredIsRetryable() {
        assertEquals(true, StripeErrorMapper.isSessionExpired("SESSION_EXPIRED"))
        assertEquals(false, StripeErrorMapper.isSessionExpired("CANCELED"))
    }

    @Test fun confirmDeclineVsUnknown() {
        assertEquals(true, StripeErrorMapper.isDecline(intentStatus = "REQUIRES_PAYMENT_METHOD", declineCode = "insufficient_funds"))
        assertEquals(false, StripeErrorMapper.isDecline(intentStatus = "REQUIRES_PAYMENT_METHOD", declineCode = null))
        assertEquals(false, StripeErrorMapper.isDecline(intentStatus = "REQUIRES_CONFIRMATION", declineCode = "x"))
        assertEquals(false, StripeErrorMapper.isDecline(intentStatus = null, declineCode = "x"))
        assertEquals(false, StripeErrorMapper.isDecline(intentStatus = "PROCESSING", declineCode = "x"))
    }

    @Test fun cloudErrorCodes() {
        assertEquals("offline", CloudErrors.code("offline: cloud not reached"))
        assertEquals("connect_incomplete", CloudErrors.code("connect_incomplete"))
        assertEquals("cloud_error", CloudErrors.code("cloud_error: HTTP Internal Server Error"))
        assertEquals("cloud_error", CloudErrors.code("boom"))
        assertEquals("cloud_error", CloudErrors.code(null))
        assertEquals("not_ready", CloudErrors.toStartError("cloud_error"))
        assertEquals("offline", CloudErrors.toStartError("offline"))
        assertEquals("connect_incomplete", CloudErrors.toStartError("connect_incomplete"))
    }
}
