package com.universaltill.pos

/**
 * ut-docs#5619: the pure decisions behind saving a backup through the system
 * file picker. Free of Android types so they run on the JVM.
 */
object BackupSaveLogic {
    // Same rule as the Go side's db.ValidBackupName: a plain file name.
    private val NAME = Regex("^unitill-pos-[A-Za-z0-9._-]*\\.db$")

    /** Result codes the page maps to translated text (settings.html). */
    val RESULT_CODES = setOf("saved", "cancelled", "failed", "no_space")

    fun isValidName(name: String): Boolean = NAME.matches(name) && !name.contains("..")

    fun downloadUrl(host: String, name: String): String = "http://$host/api/backup/download/$name?via=picker"

    /** Why a write failed, as one of [RESULT_CODES]. */
    fun reasonFor(e: Throwable?): String =
        if (e is java.io.IOException && (e.message ?: "").contains("ENOSPC")) "no_space" else "failed"
}
