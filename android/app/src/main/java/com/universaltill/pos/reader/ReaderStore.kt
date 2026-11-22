package com.universaltill.pos.reader

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec
import org.json.JSONObject

/** Where the phone keeps its reader credentials. The bearer and the pin never touch disk in the clear. */
interface ReaderStore {
    fun load(): ReaderCredentials?

    fun save(c: ReaderCredentials)

    fun clear()
}

/**
 * AES-GCM under a key that lives in the Android Keystore (not exportable, so it never leaves the device). allowBackup is false, which keeps the file out of cloud backup;
 * on Android 12+ a phone-to-phone transfer may still copy the ciphertext (only secrets/ and tls/ are excluded,
 * ut-docs#6915), which is useless without the key. Only the ciphertext is in the private preferences file. No shop data is ever stored in reader mode.
 */
class KeystoreReaderStore(context: Context) : ReaderStore {
    private val prefs = context.applicationContext.getSharedPreferences("reader_mode", Context.MODE_PRIVATE)

    private fun key(): SecretKey {
        val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (ks.getKey(ALIAS, null) as? SecretKey)?.let { return it }
        val g = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
        g.init(
            KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .build(),
        )
        return g.generateKey()
    }

    override fun load(): ReaderCredentials? =
        try {
            val raw = Base64.decode(prefs.getString("c", null) ?: return null, Base64.NO_WRAP)
            val c = Cipher.getInstance("AES/GCM/NoPadding")
            c.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(128, raw.copyOfRange(0, 12)))
            val j = JSONObject(String(c.doFinal(raw, 12, raw.size - 12), Charsets.UTF_8))
            ReaderCredentials(j.getString("address"), j.getString("reader_id"), j.getString("bearer"), j.getString("pin"))
        } catch (_: Exception) {
            null
        }

    override fun save(c: ReaderCredentials) {
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, key())
        val plain = JSONObject().put("address", c.address).put("reader_id", c.readerId).put("bearer", c.bearer).put("pin", c.pin).toString()
        val out = cipher.iv + cipher.doFinal(plain.toByteArray(Charsets.UTF_8))
        prefs.edit().putString("c", Base64.encodeToString(out, Base64.NO_WRAP)).commit()
    }

    override fun clear() {
        prefs.edit().clear().commit()
    }

    private companion object {
        const val ALIAS = "ut_reader_credentials"
    }
}
