package com.universaltill.pos

import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

/**
 * ADR-0194 §2.4 (ut-docs#6409): the Google Play flavor carries no self-update
 * or install-bridge code. Three independent proofs:
 *  1. the update classes are not on the play classpath;
 *  2. the sources the play flavor compiles (src/main + src/play) never name an
 *     installer API;
 *  3. the build is stamped as the play channel with no update URL.
 */
class PlayBuildHasNoSelfUpdateTest {
    private fun absent(name: String) {
        try {
            Class.forName(name)
            fail("$name must not exist in the play flavor")
        } catch (_: ClassNotFoundException) {
        }
    }

    @Test
    fun updateClassesAreNotInThePlayBuild() {
        absent("com.universaltill.pos.UpdateLogic")
        absent("com.universaltill.pos.InstallPreflight")
        absent("com.universaltill.pos.PermissionResume")
        // The inert stand-in has no update members.
        val names = SelfUpdateActivity::class.java.declaredMethods.map { it.name }
        assertFalse(names.any { it.contains("Download", true) || it.contains("Installer", true) || it.contains("Apk", true) })
    }

    @Test
    fun playSourcesNameNoInstallerApi() {
        val forbidden =
            listOf(
                "REQUEST_INSTALL_PACKAGES",
                "canRequestPackageInstalls",
                "ACTION_MANAGE_UNKNOWN_APP_SOURCES",
                "application/vnd.android.package-archive",
                "PackageInstaller",
                "UpdateLogic",
                "launchPackageInstaller",
                "downloadAndInstallUpdate",
                "UPDATE_APK_URL",
                "APK_SIGNER_SHA256S",
            )
        val roots = listOf(File("src/main"), File("src/play"))
        var scanned = 0
        for (root in roots) {
            assertTrue("${root.absolutePath} missing (run from android/app)", root.isDirectory)
            root.walkTopDown().filter { it.isFile && (it.extension == "kt" || it.extension == "xml") }.forEach { f ->
                scanned++
                // comments may explain the absence; code and manifest entries may not use it
                val code = f.readText().replace(Regex("(?s)/\\*.*?\\*/|<!--.*?-->"), "").lines().filterNot { it.trim().startsWith("//") }
                for (tok in forbidden) {
                    assertFalse("${f.path} names $tok", code.any { it.contains(tok) })
                }
            }
        }
        assertTrue("scanned nothing", scanned > 10)
    }

    @Test
    fun buildIsThePlayChannelWithNoUpdateUrl() {
        assertEquals("play", BuildConfig.FLAVOR)
        assertEquals("play", BuildConfig.INSTALL_CHANNEL)
        assertEquals("", BuildConfig.UPDATE_APK_URL)
        assertFalse(BuildConfig.UPDATE_UNIVERSAL)
    }
}
