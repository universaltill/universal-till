package com.universaltill.pos.reader

import java.security.MessageDigest
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import javax.net.ssl.X509TrustManager

/**
 * The main till's certificate identity is its public key: the hex SHA-256 of the SubjectPublicKeyInfo, the same
 * value internal/lantls.PinOf makes (ADR-0114 §7). There is NO CA validation: the main's certificate is
 * self-signed and the phone trusts exactly the key it saw at pairing. A handshake with any other key fails
 * before an HTTP/WebSocket request exists, so the reader bearer is never sent to a non-pinned peer.
 */
object SpkiPin {
    fun of(cert: X509Certificate): String =
        MessageDigest.getInstance("SHA-256").digest(cert.publicKey.encoded).joinToString("") { "%02x".format(it) }

    fun enforcing(pin: String): X509TrustManager = Enforcing(pin)

    fun learning(): Learning = Learning()

    abstract class Base : X509TrustManager {
        override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) = throw CertificateException("no client auth")

        override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
    }

    private class Enforcing(private val pin: String) : Base() {
        override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {
            val leaf = chain?.firstOrNull() ?: throw CertificateException("no certificate")
            if (pin.isEmpty()) throw CertificateException("no pin")
            // constant time: the pin is public, but there is no reason to be clever
            if (!MessageDigest.isEqual(of(leaf).toByteArray(), pin.toByteArray())) throw CertificateException("not the pinned main till")
        }
    }

    /** Pairing's first contact only: records the leaf's pin so the manager can compare the code made over it. */
    class Learning : Base() {
        @Volatile var pin: String = ""
            private set
        val hasLearned: Boolean get() = pin.isNotEmpty()

        override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {
            val leaf = chain?.firstOrNull() ?: throw CertificateException("no certificate")
            pin = of(leaf)
        }
    }
}
