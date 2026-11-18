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

    private val forbidden =
        listOf(
            "REQUEST_INSTALL_PACKAGES",
            "canRequestPackageInstalls",
            "ACTION_MANAGE_UNKNOWN_APP_SOURCES",
            "ACTION_INSTALL_PACKAGE",
            "ACTION_VIEW_PACKAGE",
            "application/vnd.android.package-archive",
            "PackageInstaller",
            "UpdateLogic",
            "launchPackageInstaller",
            "downloadAndInstallUpdate",
            "UPDATE_APK_URL",
            "APK_SIGNER_SHA256S",
            "unitill-pos-update",
            ".updates\"",
            "@xml/file_paths\"",
        )

    /** Strips comments, then returns "path: token" for every forbidden token in code/manifest/res. */
    private fun violations(root: File): List<String> {
        val out = mutableListOf<String>()
        root.walkTopDown().filter { it.isFile && it.extension in setOf("kt", "java", "xml") }.forEach { f ->
            val code = f.readText().replace(Regex("(?s)/\\*.*?\\*/|<!--.*?-->"), "")
                // the one allowed mention: the play manifest REMOVING the permission
                .replace(Regex("(?s)<uses-permission[^>]*REQUEST_INSTALL_PACKAGES[^>]*tools:node=\"remove\"[^>]*/>"), "").lines().filterNot { it.trim().startsWith("//") }
            for (tok in forbidden) if (code.any { it.contains(tok) }) out += "${f.path}: $tok"
            // DownloadManager is fine for backups/CSV exports, never as an APK fetcher.
            if (code.any { it.contains("DownloadManager") } && code.any { it.contains(".apk") || it.contains("package-archive") }) {
                out += "${f.path}: DownloadManager used with an apk"
            }
            // Only the captures FileProvider paths file may exist; the update file_paths.xml may not.
            if (f.extension == "xml" && f.parentFile.name.startsWith("xml") && f.name == "file_paths.xml") out += "${f.path}: update file_paths.xml"
        }
        return out
    }

    @Test
    fun playSourcesNameNoInstallerApi() {
        var scanned = 0
        for (root in listOf(File("src/main"), File("src/play"))) {
            assertTrue("${root.absolutePath} missing (run from android/app)", root.isDirectory)
            scanned += root.walkTopDown().count { it.isFile }
            assertEquals(emptyList<String>(), violations(root))
        }
        assertTrue("scanned nothing", scanned > 10)
    }

    /** Mutation proof: the scanner flags each forbidden pattern, in .kt and .java, and is not fooled by comments. */
    @Test
    fun scannerCatchesEachForbiddenPattern() {
        val dir = createTempDir("play-scan")
        try {
            val cases =
                forbidden + listOf("x DownloadManager y .apk z")
            for ((i, tok) in cases.withIndex()) {
                for (ext in listOf("kt", "java", "xml")) {
                    val f = File(dir, "c$i.$ext")
                    f.writeText("val a = \"$tok\"\n".replace("\"\"\"", "\""))
                    assertTrue("$tok in .$ext not flagged", violations(dir).any { it.startsWith(f.path) })
                    f.delete()
                }
            }
            File(dir, "ok.kt").writeText("// REQUEST_INSTALL_PACKAGES\n/* PackageInstaller */\nval a = 1\n")
            assertEquals(emptyList<String>(), violations(dir))
            File(dir, "xml").mkdirs()
            File(dir, "xml/file_paths.xml").writeText("<paths/>")
            assertTrue(violations(dir).any { it.contains("update file_paths.xml") })
        } finally {
            dir.deleteRecursively()
        }
    }

    @Test
    fun playManifestRemovesInstallPermissionAndStampsChannel() {
        val m = File("src/play/AndroidManifest.xml")
        assertTrue("src/play/AndroidManifest.xml missing", m.isFile)
        val t = m.readText()
        assertTrue(Regex("REQUEST_INSTALL_PACKAGES\"[^>]*tools:node=\"remove\"").containsMatchIn(t))
        assertTrue(Regex("install_channel\"[^>]*android:value=\"play\"").containsMatchIn(t))
        assertFalse(File("src/main/AndroidManifest.xml").readText().contains("install_channel"))
    }

    @Test
    fun buildIsThePlayChannelWithNoUpdateUrl() {
        assertEquals("play", BuildConfig.FLAVOR)
        assertEquals("play", BuildConfig.INSTALL_CHANNEL)
        assertEquals("", BuildConfig.UPDATE_APK_URL)
        assertFalse(BuildConfig.UPDATE_UNIVERSAL)
    }
}
