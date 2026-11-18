package com.universaltill.pos

import android.app.Activity
import android.app.DownloadManager
import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Environment
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import android.provider.Settings
import android.util.Log
import android.webkit.WebView
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.FileProvider
import androidx.core.content.pm.PackageInfoCompat
import java.io.File
import mobile.Mobile

/**
 * Sideload / website channel ONLY (ADR-0194 §2.4, ut-docs#6409): the in-app
 * APK update — download, signer-pin verification, hand-off to the system
 * package installer. This file lives in src/sideload, which the `play` flavor
 * does not compile, so a Google Play build carries none of it (no
 * REQUEST_INSTALL_PACKAGES, no updates FileProvider, no installer intent);
 * PlayBuildHasNoSelfUpdateTest enforces that. The play source set has a
 * same-named, inert [SelfUpdateActivity].
 *
 * Moved verbatim from MainActivity (ut-docs#1246, #4312, #4495, #5436, #6551).
 */
abstract class SelfUpdateActivity : AppCompatActivity() {
    /** Runs [js] in the till's WebView, if it exists yet. */
    protected abstract fun evaluatePageScript(js: String)

    protected abstract fun releaseKioskLock()

    protected abstract fun clearImmersiveMode()

    fun selfUpdateInstall() = runOnUiThread { downloadAndInstallUpdate() }

    fun selfUpdateCanInstall(): Boolean =
        UpdateLogic.installPreflight(Build.VERSION.SDK_INT) { packageManager.canRequestPackageInstalls() } ==
            InstallPreflight.PROCEED

    fun selfUpdateOpenPermission() = runOnUiThread { openInstallPermissionScreen() }

    protected fun selfUpdateOnCreate() = pruneStaleUpdateCopy()

    protected fun selfUpdateOnResume() {
        resumeUpdateWatch()
        // ut-docs#6551: back from the "install unknown apps" screen. Never
        // continues the install on its own: the page shows the install
        // button again and the operator taps it (one explicit tap before the
        // till restarts).
        UpdateLogic.permissionResume(updateAwaitingPermission, Build.VERSION.SDK_INT) { packageManager.canRequestPackageInstalls() }?.let {
            when (it) {
                PermissionResume.GRANTED -> {
                    updateAwaitingPermission = false
                    reportUpdate(UpdateLogic.STATE_PERMISSION_GRANTED, null, null)
                }
                PermissionResume.STILL_DENIED -> {
                    // Report once per visit to the system screen, so a later
                    // unrelated resume does not re-open the page's panel.
                    updateAwaitingPermission = false
                    reportUpdate(UpdateLogic.STATE_NEEDS_PERMISSION, null, UpdateLogic.ERROR_STILL_DENIED)
                }
            }
        }
        // ut-docs#4495: back from Android's installer without an install.
        if (updateInstallerLaunched) {
            updateInstallerLaunched = false
            Log.i(TAG, "update: back from the package installer without an install")
            reportUpdate("closed", null, null)
        }
    }

    protected fun selfUpdateOnDestroy() = stopUpdatePoll()

    // ut-docs#4312: the in-app update's state (see downloadAndInstallUpdate).
    private val updateHandler = Handler(Looper.getMainLooper())
    private var updatePollRunnable: Runnable? = null
    private var updateCheckingReuse = false

    // ut-docs#5436: set while [verifyThenInstall] reads the APK's signer off
    // the UI thread (v2 verification reads the whole file).
    private var updateVerifyingApk = false
    private var updateLoggedPercent = -1
    private var lastUpdateState: String? = null

    // ut-docs#4495: set when [launchPackageInstaller] hands the APK over.
    // Android's installer is a see-through dialog activity, so the page
    // behind it never goes hidden; the next [onResume] in this process means
    // it went away without replacing the app (an install kills this process),
    // and the page is told so it can offer to open it again.
    private var updateInstallerLaunched = false

    /**
     * The in-app update (ut-docs#1246, made reliable by ut-docs#4312).
     *
     * One download at a time, reported to the page as it goes:
     * - a repeat [KioskBridge.installUpdate] while a download runs does
     *   nothing but re-report its state (the chip and the Settings form used
     *   to restart a 150 MB download on every tap);
     * - the download id survives this Activity ([UPDATE_PREFS]), so a
     *   recreated Activity, or a later tap, picks the same download up;
     * - a complete APK already on disk is installed instead of downloaded
     *   again, but only when it is provably the current release
     *   ([UpdateLogic.canReuseDownloadedApk]: same length as the server's
     *   Content-Length — or, when HEAD fails, as the finished saved
     *   download's total ([UpdateLogic.expectedApkLength]) — parses, newer
     *   than the running build);
     * - it fails on a stall (no bytes for [UPDATE_STALL_MS]) or after
     *   [UPDATE_HARD_CAP_MS], never on a fixed clock;
     * - every transition is a `Log.i(TAG, "update: …")` line and a
     *   `window.utAndroidUpdate({state, percent, error})` call
     *   (web/public/android-update.js), so the chip shows a % and a failure.
     *
     * Downloads into the app's own external files dir: no storage permission,
     * and a half-written APK never appears in the operator's Files app. That
     * dir is readable by other apps on API 29 and below, so the installer is
     * never given this file: [verifyThenInstall] copies it into app-private
     * storage first and installs the verified copy (ut-docs#5436). Never
     * crashes the till — a failure leaves the operator able to retry.
     */
    internal fun downloadAndInstallUpdate() {
        if (updatePollRunnable != null || updateCheckingReuse || updateVerifyingApk) {
            Log.i(TAG, "update: already in progress, re-reporting state")
            lastUpdateState?.let { pushUpdateState(it) }
            return
        }
        // ut-docs#6551: before spending a ~40 MB download, make sure Android
        // will let this app hand an APK to the installer. If not, tell the
        // page (it explains and offers the settings screen) instead of a
        // silent no-op later.
        if (UpdateLogic.installPreflight(Build.VERSION.SDK_INT) { packageManager.canRequestPackageInstalls() } == InstallPreflight.NEEDS_PERMISSION) {
            Log.i(TAG, "update: install-unknown-apps not allowed for this app, asking the operator")
            reportUpdate(UpdateLogic.STATE_NEEDS_PERMISSION, null, null)
            return
        }
        // ut-docs#4495: a new attempt. A launch Android refused silently
        // never paused this Activity, so its flag must not survive into this
        // attempt and report "closed" mid-download on the next resume.
        updateInstallerLaunched = false
        try {
            val manager = getSystemService(Context.DOWNLOAD_SERVICE) as DownloadManager
            val dest = File(getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS), UPDATE_APK_NAME)
            val savedId = updatePrefs().getLong(UPDATE_PREF_ID, -1L)
            if (savedId >= 0 && isDownloadInFlight(manager, savedId)) {
                Log.i(TAG, "update: download $savedId is still running, watching it")
                watchUpdateDownload(manager, savedId, dest)
                return
            }
            val url = updateApkUrl()
            updateCheckingReuse = true
            reportUpdate("downloading", null, null)
            // The size check is a network call (HEAD), so off the UI thread.
            Thread {
                val (savedSuccessful, savedTotal) = savedDownloadOutcome(manager, savedId)
                val expected = UpdateLogic.expectedApkLength(headContentLength(url), savedSuccessful, savedTotal)
                val fileLength = if (dest.isFile) dest.length() else 0L
                val archiveVersion =
                    if (fileLength > 0) inspectArchive(dest, false)?.takeIf { it.packageName == packageName }?.versionCode else null
                runOnUiThread {
                    updateCheckingReuse = false
                    if (isDestroyed) return@runOnUiThread
                    if (UpdateLogic.canReuseDownloadedApk(fileLength, expected, archiveVersion, installedVersionCode())) {
                        Log.i(TAG, "update: reusing the complete APK already downloaded ($fileLength bytes, versionCode $archiveVersion)")
                        verifyThenInstall(dest, savedId)
                    } else {
                        enqueueUpdateDownload(manager, dest, url, savedId)
                    }
                }
            }.apply {
                name = "update-size-check"
                isDaemon = true
            }.start()
        } catch (e: Exception) {
            updateCheckingReuse = false
            failUpdate(e.message ?: "update download failed")
        }
    }

    /** ut-docs#1541: this build's own package, or — on the universal build — the device ABI's. */
    private fun updateApkUrl(): String =
        UpdateLogic.updateApkUrl(
            BuildConfig.UPDATE_UNIVERSAL,
            BuildConfig.UPDATE_APK_URL,
            mapOf(
                UpdateLogic.ABI_ARM64 to BuildConfig.UPDATE_APK_URL_ARM64,
                UpdateLogic.ABI_ARMV7 to BuildConfig.UPDATE_APK_URL_ARMV7,
            ),
            Build.SUPPORTED_ABIS.toList(),
        )

    private fun enqueueUpdateDownload(
        manager: DownloadManager,
        dest: File,
        url: String,
        staleId: Long,
    ) {
        try {
            // Only now is the old file worthless (partial, or an older
            // release): remove the old record (DownloadManager deletes its
            // file) and anything else left at the destination, so a
            // half-written APK is never installed.
            if (staleId >= 0) manager.remove(staleId)
            if (dest.exists()) dest.delete()
            val request =
                DownloadManager.Request(Uri.parse(url)).apply {
                    setMimeType(APK_MIME)
                    setTitle(getString(R.string.app_name))
                    setNotificationVisibility(DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED)
                    setDestinationInExternalFilesDir(
                        this@SelfUpdateActivity,
                        Environment.DIRECTORY_DOWNLOADS,
                        UPDATE_APK_NAME,
                    )
                }
            val id = manager.enqueue(request)
            // The enqueue time (wall clock) goes with the id, so the hard cap
            // counts from here in every later Activity (UpdateLogic.detectorStartMs).
            updatePrefs()
                .edit()
                .putLong(UPDATE_PREF_ID, id)
                .putLong(UPDATE_PREF_ENQUEUED_AT, System.currentTimeMillis())
                .apply()
            Log.i(TAG, "update: enqueued download $id from $url")
            watchUpdateDownload(manager, id, dest)
        } catch (e: Exception) {
            failUpdate(e.message ?: "update download failed")
        }
    }

    /**
     * Whether download [id] finished (STATUS_SUCCESSFUL) and its
     * COLUMN_TOTAL_SIZE_BYTES — the reuse check's fallback when HEAD fails.
     */
    private fun savedDownloadOutcome(
        manager: DownloadManager,
        id: Long,
    ): Pair<Boolean, Long?> {
        if (id < 0) return false to null
        return runCatching {
            manager.query(DownloadManager.Query().setFilterById(id)).use { c ->
                if (!c.moveToFirst()) return@use false to null
                val status = c.getInt(c.getColumnIndexOrThrow(DownloadManager.COLUMN_STATUS))
                val total = c.getLong(c.getColumnIndexOrThrow(DownloadManager.COLUMN_TOTAL_SIZE_BYTES))
                (status == DownloadManager.STATUS_SUCCESSFUL) to total
            }
        }.getOrDefault(false to null)
    }

    private fun isDownloadInFlight(
        manager: DownloadManager,
        id: Long,
    ): Boolean =
        manager.query(DownloadManager.Query().setFilterById(id)).use { c ->
            c.moveToFirst() &&
                c.getInt(c.getColumnIndexOrThrow(DownloadManager.COLUMN_STATUS)) in IN_FLIGHT_STATUSES
        }

    /**
     * Polls [id] to a terminal state, then hands the file to the installer.
     *
     * POLLED, not a completion broadcast: DownloadManager's
     * ACTION_DOWNLOAD_COMPLETE comes from the system, so a receiver must be
     * registered RECEIVER_EXPORTED to hear it at all — verified the hard way
     * on a real tablet, where a RECEIVER_NOT_EXPORTED registration silently
     * never fired and the 142MB APK just sat on disk. Exporting it would let
     * any app on the device spoof "your download finished"; polling has no
     * receiver to leak and explicit terminal states. Bounded by
     * [UpdateLogic.StallDetector], not by a fixed number of polls.
     */
    private fun watchUpdateDownload(
        manager: DownloadManager,
        id: Long,
        dest: File,
    ) {
        stopUpdatePoll()
        val enqueuedAt = updatePrefs().getLong(UPDATE_PREF_ENQUEUED_AT, -1L).takeIf { it > 0 }
        val start = UpdateLogic.detectorStartMs(SystemClock.elapsedRealtime(), System.currentTimeMillis(), enqueuedAt)
        val stall = UpdateLogic.StallDetector(UPDATE_STALL_MS, UPDATE_HARD_CAP_MS, start)
        updateLoggedPercent = -1
        val poll =
            object : Runnable {
                override fun run() {
                    if (pollUpdateDownload(manager, id, dest, stall)) {
                        updateHandler.postDelayed(this, UPDATE_POLL_MS)
                    } else {
                        updatePollRunnable = null
                    }
                }
            }
        updatePollRunnable = poll
        poll.run()
    }

    /** One poll. Returns true to keep polling. */
    private fun pollUpdateDownload(
        manager: DownloadManager,
        id: Long,
        dest: File,
        stall: UpdateLogic.StallDetector,
    ): Boolean {
        try {
            manager.query(DownloadManager.Query().setFilterById(id)).use { c ->
                if (!c.moveToFirst()) {
                    // Cancelled from the notification, or cleared by the system.
                    clearUpdateId()
                    failUpdate("download cancelled")
                    return false
                }
                val status = c.getInt(c.getColumnIndexOrThrow(DownloadManager.COLUMN_STATUS))
                val done = c.getLong(c.getColumnIndexOrThrow(DownloadManager.COLUMN_BYTES_DOWNLOADED_SO_FAR))
                val total = c.getLong(c.getColumnIndexOrThrow(DownloadManager.COLUMN_TOTAL_SIZE_BYTES))
                when (status) {
                    DownloadManager.STATUS_SUCCESSFUL -> {
                        val localUri = c.getString(c.getColumnIndexOrThrow(DownloadManager.COLUMN_LOCAL_URI))
                        val file = localUri?.let { Uri.parse(it) }?.takeIf { it.scheme == "file" }?.path?.let { File(it) } ?: dest
                        Log.i(TAG, "update: download $id complete ($done bytes)")
                        // Finished: KEEP the id. A later tap reuses this file
                        // only through the size + version check, never blindly;
                        // when HEAD fails, this record's total is the size
                        // (UpdateLogic.expectedApkLength). The next enqueue
                        // removes it.
                        reportUpdate("ready", 100, null)
                        verifyThenInstall(file, id)
                        return false
                    }
                    DownloadManager.STATUS_FAILED -> {
                        val reason = c.getInt(c.getColumnIndexOrThrow(DownloadManager.COLUMN_REASON))
                        manager.remove(id)
                        clearUpdateId()
                        failUpdate("download failed (reason $reason)")
                        return false
                    }
                }
                val pct = UpdateLogic.percent(done, total)
                if (pct != null && UpdateLogic.crossedLogStep(updateLoggedPercent, pct)) {
                    Log.i(TAG, "update: download $id at $pct% ($done/$total bytes)")
                    updateLoggedPercent = pct
                }
                when (stall.observe(done, SystemClock.elapsedRealtime())) {
                    UpdateLogic.Verdict.OK -> {
                        reportUpdate("downloading", pct, null)
                        return true
                    }
                    UpdateLogic.Verdict.STALLED -> {
                        manager.remove(id)
                        clearUpdateId()
                        failUpdate("download stalled: no progress for ${UPDATE_STALL_MS / 60_000} minutes at $done bytes (status $status)")
                        return false
                    }
                    UpdateLogic.Verdict.TIMED_OUT -> {
                        manager.remove(id)
                        clearUpdateId()
                        failUpdate("download timed out after ${UPDATE_HARD_CAP_MS / 60_000} minutes at $done/$total bytes")
                        return false
                    }
                }
            }
        } catch (e: Exception) {
            failUpdate(e.message ?: "update download failed")
            return false
        }
    }

    /**
     * ut-docs#4312: a download started before this Activity was (re)created
     * keeps reporting to the page. Called from [onResume]; a finished one is
     * left for the operator's next tap (the reuse check), never installed
     * unasked.
     */
    internal fun resumeUpdateWatch() {
        if (updatePollRunnable != null || updateCheckingReuse || updateVerifyingApk) return
        val id = updatePrefs().getLong(UPDATE_PREF_ID, -1L)
        if (id < 0) return
        runCatching {
            val manager = getSystemService(Context.DOWNLOAD_SERVICE) as DownloadManager
            if (isDownloadInFlight(manager, id)) {
                Log.i(TAG, "update: resuming the watch on download $id")
                watchUpdateDownload(manager, id, File(getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS), UPDATE_APK_NAME))
            }
        }.onFailure { Log.w(TAG, "update: could not resume the download watch", it) }
    }

    internal fun stopUpdatePoll() {
        updatePollRunnable?.let { updateHandler.removeCallbacks(it) }
        updatePollRunnable = null
    }

    private fun updatePrefs() = getSharedPreferences(UPDATE_PREFS, Context.MODE_PRIVATE)

    private fun clearUpdateId() =
        updatePrefs()
            .edit()
            .remove(UPDATE_PREF_ID)
            .remove(UPDATE_PREF_ENQUEUED_AT)
            .apply()

    private fun failUpdate(error: String) {
        Log.w(TAG, "update: failed: $error")
        reportUpdate("failed", null, error)
    }

    /** Tells the page (web/public/android-update.js) where the update is. */
    private fun reportUpdate(
        state: String,
        percent: Int?,
        error: String?,
    ) {
        val json = UpdateLogic.stateJson(state, percent, error)
        lastUpdateState = json
        pushUpdateState(json)
    }

    private fun pushUpdateState(json: String) {
        // json is UpdateLogic.stateJson output: a JSON object literal with
        // every string escaped, so it is a valid JS expression as it stands.
        evaluatePageScript("window.utAndroidUpdate && window.utAndroidUpdate($json)")
    }

    /** The server's Content-Length for [url] (redirects followed), or null. */
    private fun headContentLength(url: String): Long? =
        runCatching {
            val conn = java.net.URL(url).openConnection() as java.net.HttpURLConnection
            try {
                conn.requestMethod = "HEAD"
                conn.instanceFollowRedirects = true
                conn.connectTimeout = 15_000
                conn.readTimeout = 15_000
                if (conn.responseCode == java.net.HttpURLConnection.HTTP_OK) conn.contentLengthLong.takeIf { it > 0 } else null
            } finally {
                conn.disconnect()
            }
        }.onFailure { Log.w(TAG, "update: size check failed: ${it.message}") }.getOrNull()

    /** What [inspectArchive] read from an APK file (ut-docs#5436). */
    private data class ArchiveFacts(
        val packageName: String,
        val versionCode: Long,
        val signerDigests: List<String>?,
        /** API 28+: digests in signingInfo.signingCertificateHistory (rotation lineage). */
        val signerHistory: List<String>? = null,
    )

    /**
     * Parses the APK at [apk]: package, versionCode and, when [withSigners],
     * the SHA-256 of each signing certificate (null when none could be read;
     * [UpdateLogic.checkUpdateApk] then fails closed). Null when the file does
     * not parse. [withSigners] reads and verifies the whole file, so call it
     * off the UI thread; the reuse size check wants only the versionCode.
     */
    @Suppress("DEPRECATION")
    private fun inspectArchive(
        apk: File,
        withSigners: Boolean,
    ): ArchiveFacts? =
        runCatching {
            val pm = packageManager
            val flags =
                when {
                    !withSigners -> 0
                    Build.VERSION.SDK_INT >= Build.VERSION_CODES.P ->
                        PackageManager.GET_SIGNING_CERTIFICATES or PackageManager.GET_SIGNATURES
                    else -> PackageManager.GET_SIGNATURES
                }
            val info = pm.getPackageArchiveInfo(apk.path, flags) ?: return@runCatching null
            val digests =
                if (!withSigners) {
                    null
                } else {
                    // API 28+: the CURRENT signer (apkContentsSigners), which is
                    // what the pin must contain under a v3 rotation. API 24-27,
                    // or an API 28 archive without signingInfo: legacy signatures.
                    val sigs =
                        (if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) info.signingInfo?.apkContentsSigners else null)
                            ?: info.signatures
                    sigs?.takeIf { it.isNotEmpty() }?.map { UpdateLogic.sha256Hex(it.toByteArray()) }
                }
            val history =
                if (withSigners && Build.VERSION.SDK_INT >= Build.VERSION_CODES.P && info.signingInfo?.hasMultipleSigners() == false) {
                    info.signingInfo?.signingCertificateHistory?.map { UpdateLogic.sha256Hex(it.toByteArray()) }
                } else {
                    null
                }
            ArchiveFacts(info.packageName, PackageInfoCompat.getLongVersionCode(info), digests, history)
        }.getOrNull()

    /**
     * ut-docs#5436 / ADR-0179: the only way to the installer. Checks off the
     * UI thread that the downloaded [apk] is this app's package, newer, and
     * signed by a pinned certificate ([UpdateLogic.checkUpdateApk]). On a
     * refusal the file and the download record are removed and the update
     * fails as `failed`; the kiosk lock, immersive mode and installer are
     * never touched ([launchPackageInstaller] is what releases them).
     */
    private fun verifyThenInstall(
        downloaded: File,
        downloadId: Long,
    ) {
        updateVerifyingApk = true
        Thread {
            var facts: ArchiveFacts? = null
            // TOCTOU (M1): other apps can write the external Download/ dir on
            // API 29 and below, so verify and install a private COPY.
            val apk = File(File(filesDir, UPDATE_COPY_DIR).apply { mkdirs() }, UPDATE_APK_NAME)
            val pins = UpdateLogic.parsePinSet(BuildConfig.APK_SIGNER_SHA256S)
            val verdict =
                try {
                    apk.delete()
                    downloaded.copyTo(apk, overwrite = true)
                    facts = inspectArchive(apk, true)
                    UpdateLogic.checkUpdateApk(
                        facts?.packageName,
                        packageName,
                        facts?.versionCode,
                        installedVersionCode(),
                        facts?.signerDigests,
                        pins,
                        facts?.signerHistory,
                    )
                } catch (e: Exception) {
                    ApkCheck.Refuse(RefuseClass.CHECK_ERROR, "signer check failed: ${e.message}")
                }
            runOnUiThread {
                updateVerifyingApk = false
                if (verdict is ApkCheck.Refuse) {
                    // Cleanup runs even if the Activity is gone (L2).
                    Log.w(
                        TAG,
                        "update: refused: ${verdict.reason}; signers ${facts?.signerDigests}, history ${facts?.signerHistory}, pinned $pins, package ${facts?.packageName}",
                    )
                    runCatching {
                        if (downloadId >= 0) (getSystemService(Context.DOWNLOAD_SERVICE) as DownloadManager).remove(downloadId)
                    }
                    apk.delete()
                    downloaded.delete()
                    clearUpdateId()
                    // ut-docs#5446: tell the fleet roster, class only (never
                    // verdict.reason: digests and foreign package names). Runs
                    // even if the Activity is gone, and a missing Go library
                    // must never break the refusal.
                    runCatching { Mobile.reportAndroidUpdateRefused(verdict.cls.wire) }
                        .onFailure { Log.w(TAG, "update: could not report refusal", it) }
                    if (!isDestroyed) failUpdate("refused: ${verdict.reason}")
                    return@runOnUiThread
                }
                // The check passed (pinned or dormant): resolve an earlier
                // refusal on the fleet roster, whether or not the Activity lives.
                runCatching { Mobile.reportAndroidUpdateAccepted() }
                    .onFailure { Log.w(TAG, "update: could not report acceptance", it) }
                if (isDestroyed) return@runOnUiThread
                when (verdict) {
                    is ApkCheck.Accept -> {
                        Log.i(TAG, "update: APK signer verified (${verdict.digest.take(12)}...)")
                        launchPackageInstaller(apk)
                    }
                    ApkCheck.AcceptUnpinned -> {
                        Log.i(TAG, "update: APK signer not pinned (no certificate digest compiled in)")
                        launchPackageInstaller(apk)
                    }
                    is ApkCheck.Refuse -> Unit // handled above
                }
            }
        }.apply {
            name = "update-signer-check"
            isDaemon = true
        }.start()
    }

    /**
     * ut-docs#5436: the verified copy is only needed while the system
     * installer reads it. A copy older than a day (an abandoned or finished
     * install) is removed at start; a fresh one is kept because an Activity
     * recreated mid-install must not pull the file from under the installer.
     */
    internal fun pruneStaleUpdateCopy() {
        runCatching {
            val f = File(File(filesDir, UPDATE_COPY_DIR), UPDATE_APK_NAME)
            if (f.exists() && System.currentTimeMillis() - f.lastModified() > 24L * 3600 * 1000) f.delete()
        }
    }

    private fun installedVersionCode(): Long =
        runCatching {
            @Suppress("DEPRECATION")
            PackageInfoCompat.getLongVersionCode(packageManager.getPackageInfo(packageName, 0))
        }.getOrDefault(Long.MAX_VALUE) // unknown: never reuse a file blindly

    /** ut-docs#6551: set while the system permission screen is open, read by [onResume]. */
    private var updateAwaitingPermission = false

    internal fun openInstallPermissionScreen() {
        try {
            // Same reason as launchPackageInstaller: a pinned (lock-task) app
            // cannot start another app's activity.
            releaseKioskLock()
            clearImmersiveMode()
            val intent =
                Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:$packageName")).apply {
                    addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                }
            updateAwaitingPermission = true
            startActivity(intent)
            Log.i(TAG, "update: opened the install-unknown-apps screen")
        } catch (e: Exception) {
            updateAwaitingPermission = false
            failUpdate("permission screen: ${e.message ?: "could not open"}")
        }
    }

    /**
     * Hands [apk] to the system installer via [FileProvider]. A `file://` URI
     * is rejected from API 24 (FileUriExposedException) — this app's own
     * minSdk — so the content:// grant is the only route.
     *
     * Only called after [verifyThenInstall] accepted the file (ut-docs#5436).
     */
    private fun launchPackageInstaller(apk: File) {
        try {
            // If the till is pinned in lock-task (kiosk) mode — after
            // ut-docs#1508 that means a genuine self-order pin the manager
            // carried into /settings, which onPageFinished deliberately
            // leaves engaged there (only the PIN-gated exitLockdown()
            // releases it); ordinary till use never pins at all — Android
            // silently REFUSES to start any non-allowlisted activity from a
            // pinned app: "Attempted Lock Task Mode violation
            // r=...packageinstaller/...InstallStart" in logcat, no dialog,
            // no exception. That is exactly what "I pressed Update and
            // nothing happened" looked like on a real tablet. This is only
            // reachable after POST /api/update/android-install authorised the
            // caller (manager session or PIN). Release defensively, the same
            // way KioskBridge.exitLockdown does for exit-to-OS — a no-op if
            // the pin was already off, which it is in the ordinary case.
            releaseKioskLock()
            clearImmersiveMode()
            val uri = FileProvider.getUriForFile(this, "$packageName.updates", apk)
            val intent =
                Intent(Intent.ACTION_VIEW).apply {
                    setDataAndType(uri, APK_MIME)
                    addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
                    addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                }
            startActivity(intent)
            updateInstallerLaunched = true
            Log.i(TAG, "update: package installer launched for ${apk.name} (${apk.length()} bytes)")
            reportUpdate("installing", 100, null)
        } catch (e: Exception) {
            // Most likely ActivityNotFoundException, or "install unknown apps"
            // not yet granted. Surface it — the fallback is a retry or a
            // manual install, which the operator can only choose if they know
            // this failed.
            failUpdate("installer: ${e.message ?: "install failed"}")
        }
    }

    companion object {
        private const val TAG = "MainActivity"

        // ut-docs#1246/#1541: the release APK's STABLE url is
        // BuildConfig.UPDATE_APK_URL, one per ABI flavor (app/build.gradle.kts)
        // — release.yml republishes those filenames on every release, so the
        // shell never resolves a version or calls the GitHub API. Compile-time
        // on purpose: installUpdate() takes no argument, so page content can
        // never redirect an install.
        private const val UPDATE_APK_NAME = "unitill-pos-update.apk"

        /** App-private (filesDir) dir holding the verified copy handed to the installer (res/xml/file_paths.xml). */
        private const val UPDATE_COPY_DIR = "updates"
        private const val APK_MIME = "application/vnd.android.package-archive"

        // ut-docs#4312: the old fixed limit (~5 minutes at 750ms) failed a
        // 150 MB download on ordinary shop Wi-Fi. Now: poll once a second,
        // fail only when no byte has arrived for 3 minutes, and give up after
        // an hour whatever happens.
        private const val UPDATE_POLL_MS = 1000L
        private const val UPDATE_STALL_MS = 3L * 60L * 1000L
        private const val UPDATE_HARD_CAP_MS = 60L * 60L * 1000L

        private const val UPDATE_PREFS = "in_app_update"
        private const val UPDATE_PREF_ID = "download_id"
        private const val UPDATE_PREF_ENQUEUED_AT = "download_enqueued_at_wall_ms"
        private val IN_FLIGHT_STATUSES =
            setOf(DownloadManager.STATUS_PENDING, DownloadManager.STATUS_RUNNING, DownloadManager.STATUS_PAUSED)
    }
}
