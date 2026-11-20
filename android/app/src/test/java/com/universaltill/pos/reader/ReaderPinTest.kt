package com.universaltill.pos.reader

import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import okhttp3.tls.HeldCertificate
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

class ReaderPinTest {
    private val main = HeldCertificate.Builder().commonName("main").build()
    private val other = HeldCertificate.Builder().commonName("evil").build()

    private fun chain(c: HeldCertificate) = arrayOf<X509Certificate>(c.certificate)

    @Test fun `the pin is the hex sha256 of the SPKI, the same as lantls PinOf`() {
        val pin = SpkiPin.of(main.certificate)
        assertEquals(64, pin.length)
        assertTrue(pin.all { it in "0123456789abcdef" })
        val want = java.security.MessageDigest.getInstance("SHA-256").digest(main.certificate.publicKey.encoded)
        assertEquals(want.joinToString("") { "%02x".format(it) }, pin)
    }

    @Test fun `the certificate with the pinned key is accepted, no CA is consulted`() {
        SpkiPin.enforcing(SpkiPin.of(main.certificate)).checkServerTrusted(chain(main), "ECDHE_ECDSA")
    }

    @Test fun `any other certificate is refused, so the bearer is never sent to it`() {
        try {
            SpkiPin.enforcing(SpkiPin.of(main.certificate)).checkServerTrusted(chain(other), "ECDHE_ECDSA")
            fail("accepted a certificate that is not the pinned one")
        } catch (_: CertificateException) {
        }
    }

    @Test fun `an empty chain and a blank pin are refused`() {
        try { SpkiPin.enforcing(SpkiPin.of(main.certificate)).checkServerTrusted(emptyArray(), "x"); fail() } catch (_: CertificateException) {}
        try { SpkiPin.enforcing("").checkServerTrusted(chain(main), "x"); fail() } catch (_: CertificateException) {}
    }

    @Test fun `a replayed pin of another certificate with the same subject does not match`() {
        val twin = HeldCertificate.Builder().commonName("main").build()
        try { SpkiPin.enforcing(SpkiPin.of(main.certificate)).checkServerTrusted(chain(twin), "x"); fail() } catch (_: CertificateException) {}
    }

    @Test fun `the learning trust manager records the leaf pin for pairing and nothing else`() {
        val l = SpkiPin.learning()
        assertFalse(l.hasLearned)
        l.checkServerTrusted(chain(main), "x")
        assertEquals(SpkiPin.of(main.certificate), l.pin)
    }
}
