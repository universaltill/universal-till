package com.universaltill.pos

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * ut-docs#5271: the in-app update source is the Gradle property
 * updateApkUrlBase. Without it the BuildConfig URLs are exactly today's
 * (the literal below is duplicated on purpose: it is the pin). android-ci
 * reruns this with -PupdateApkUrlBase and UT_EXPECT_UPDATE_APK_URL_BASE set
 * to the same value, proving only that base reaches the app.
 */
class UpdateSourceBuildConfigTest {
    private val base =
        System.getenv("UT_EXPECT_UPDATE_APK_URL_BASE")
            ?: "https://github.com/universaltill/universal-till/releases/latest/download/"

    @Test
    fun perAbiTableUsesTheBase() {
        assertEquals(base + "unitill-pos-android-arm64-latest.apk", BuildConfig.UPDATE_APK_URL_ARM64)
        assertEquals(base + "unitill-pos-android-armv7-latest.apk", BuildConfig.UPDATE_APK_URL_ARMV7)
    }

    @Test
    fun flavorUrlUsesTheBase() {
        val file =
            when (BuildConfig.FLAVOR) {
                "arm64" -> "unitill-pos-android-arm64-latest.apk"
                "armv7" -> "unitill-pos-android-armv7-latest.apk"
                else -> "unitill-pos-android-latest.apk"
            }
        assertEquals(base + file, BuildConfig.UPDATE_APK_URL)
        assertEquals(BuildConfig.FLAVOR == "universal", BuildConfig.UPDATE_UNIVERSAL)
    }
}
