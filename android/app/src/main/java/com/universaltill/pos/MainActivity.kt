package com.universaltill.pos

import android.app.Activity
import android.app.DownloadManager
import android.app.admin.DevicePolicyManager
import android.content.ActivityNotFoundException
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.ServiceConnection
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Environment
import android.os.IBinder
import android.view.View
import android.webkit.CookieManager
import android.webkit.JavascriptInterface
import android.webkit.URLUtil
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.TextView
import androidx.activity.OnBackPressedCallback
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.appcompat.app.AppCompatDelegate
import androidx.core.content.ContextCompat
import androidx.core.content.FileProvider
import androidx.core.os.LocaleListCompat
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import java.io.File

/**
 * Native shell (ADR-0023, spec 013 Phase 2): binds to [TillService] — which
 * owns the actual embedded-server lifecycle, independent of this Activity
 * — and points a WebView at whatever loopback address it reports. Same
 * "start, wait, then show a window" shape as
 * cmd/unitill-desktop/desktop.go, minus the child-process spawn (mobile
 * apps can't do that). The manifest's `android:configChanges` on this
 * Activity means rotation doesn't destroy/recreate it at all (the WebView
 * instance persists too) — combined with TillService surviving
 * independently of this Activity either way, a rotation is now a true
 * no-op for the running till, not just "restarts fast."
 */
class MainActivity : AppCompatActivity() {
    private lateinit var webView: WebView
    private lateinit var statusView: TextView

    private var till: TillService? = null

    // ut-docs#1254 (review should-fix 3): the loopback host:port
    // WebViewClient.shouldOverrideUrlLoading below allows navigation
    // within. Set once TillService reports its real address — before that,
    // shouldOverrideUrlLoading refuses everything, which is correct: no
    // page has loaded yet for anything to navigate away from.
    private var allowedHost: String? = null

    private val listener: (String?, String?) -> Unit = { address, error ->
        runOnUiThread {
            when {
                address != null -> {
                    allowedHost = address
                    // Restore onCreate's release-build GONE: TillService is
                    // START_STICKY, so a failed first start (which forces the
                    // bar VISIBLE below) can be followed by a successful
                    // restart, and the bar must not stay up showing the
                    // internal bind address to a shop worker (ut-docs#412).
                    statusView.visibility = if (BuildConfig.DEBUG) View.VISIBLE else View.GONE
                    statusView.text = getString(R.string.status_running, address)
                    // Avoid reloading a URL the WebView already has. This
                    // matters now that the manifest's configChanges keeps
                    // the WebView instance alive across rotation (it used
                    // to be recreated every time regardless, which made
                    // this check permanently a no-op — independent review,
                    // 2026-07-25) — it also absorbs the benign double-
                    // invoke TillService's listener mechanism can produce
                    // (see the comment on addListener).
                    if (webView.url != "http://$address/") {
                        webView.loadUrl("http://$address")
                    }
                }
                error != null -> {
                    // ut-docs#1412: the bar is GONE in release builds (see
                    // onCreate) — correct while the till runs, but a start
                    // failure then left a blank white WebView with the only
                    // explanation buried in the notification shade (real
                    // device, v0.9.0, migration error). A failure is the one
                    // moment the message must be on screen in every build.
                    statusView.visibility = View.VISIBLE
                    statusView.text = getString(R.string.status_failed, error)
                }
            }
        }
    }

    private val connection =
        object : ServiceConnection {
            override fun onServiceConnected(name: ComponentName?, binder: IBinder?) {
                val service = (binder as TillService.LocalBinder).service
                till = service
                service.addListener(listener)
            }

            override fun onServiceDisconnected(name: ComponentName?) {
                till = null
            }
        }

    // Best-effort: a foreground service runs regardless of this permission
    // (API 33+) — it just wouldn't show its notification without it. Never
    // block starting the till on the user's answer here, same "never block
    // the core function on a secondary permission" posture as the rest of
    // this codebase.
    private val requestNotificationPermission =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) {}

    // Backs <input type="file"> in the WebView (e.g. the Plugins page's
    // "Import from file" side-load form, spec 001-plugin-marketplace). A
    // plain WebView has NO file-chooser UI without this: without a
    // WebChromeClient overriding onShowFileChooser, tapping a file input is
    // a silent no-op — no dialog, no error, nothing (a well-documented
    // WebView gotcha, confirmed live 2026-07-27: the button did nothing at
    // all on a real device/emulator until this was wired up).
    private var fileChooserCallback: ValueCallback<Array<Uri>>? = null
    private val fileChooserLauncher =
        registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { result ->
            val callback = fileChooserCallback ?: return@registerForActivityResult
            fileChooserCallback = null
            val data = result.data
            val uris =
                if (result.resultCode == Activity.RESULT_OK && data != null) {
                    val clipData = data.clipData
                    if (clipData != null) {
                        Array(clipData.itemCount) { i -> clipData.getItemAt(i).uri }
                    } else {
                        data.data?.let { arrayOf(it) } ?: emptyArray()
                    }
                } else {
                    emptyArray()
                }
            callback.onReceiveValue(uris)
        }

    // ut-docs#1254: whether a manager has deliberately left kiosk mode.
    // Set true ONLY by [KioskBridge.exitLockdown] (the server's own
    // purpose-built "exit to OS" escape hatch, internal/pages/
    // settings_page.go's /api/settings/exit-to-os — see the bridge's own
    // KDoc for why that, and not simply reaching /settings, is the right
    // signal). Reset to false defensively on every onResume and whenever
    // WebView navigation leaves the manager-facing /login or /settings
    // pages, so an unlock never silently outlives the moment it was
    // granted for.
    private var kioskUnlocked = false

    /**
     * The native side of the server's existing "exit to OS" escape hatch
     * (ut-docs#1254, ut-docs#1099). `/api/settings/exit-to-os` already does
     * its OWN live manager-PIN check server-side (internal/pages/
     * settings_page.go) — reachable from BOTH `/settings` (a signed-in
     * operator) and `/login` (ut-docs#1099: a fully anonymous, un-signed-in
     * kiosk screen, exactly the self-order case this ticket is most
     * concerned about) — but it's issued as a plain `fetch()` from the
     * page's own JS, not a top-level navigation, so nothing about it is
     * observable via WebViewClient.onPageFinished/shouldInterceptRequest.
     * `login.html`/`settings.html`'s success branch calls
     * `window.AndroidKiosk.exitLockdown()` directly instead, so this class
     * only has to trust that the WEB layer already did the real
     * authorization — which it did, via the exact same endpoint every
     * other platform's "exit to OS" button already relies on.
     *
     * Safe to expose via addJavascriptInterface — but ONLY because of
     * webViewClient's shouldOverrideUrlLoading override below, not because
     * of anything about this WebView alone (review finding, ut-docs#1254):
     * network_security_config.xml restricts *cleartext* only, and this
     * app's WebViewClient has no navigation restriction of its own by
     * default — an ordinary in-page link (found reachable in practice:
     * my_reports.html's ungated GitHub issue link) would otherwise
     * navigate this SAME WebView instance to an untrusted https:// origin,
     * where this object stays reachable (addJavascriptInterface's scope is
     * the WebView instance, not any one page it happens to be showing).
     * shouldOverrideUrlLoading confining navigation to the till's own
     * loopback host is what actually closes that gap; without it, this
     * class would be a real bypass of the very lock it exists to release.
     */
    private inner class KioskBridge {
        @JavascriptInterface
        fun exitLockdown() {
            runOnUiThread {
                kioskUnlocked = true
                releaseKioskLock()
                clearImmersiveMode()
            }
        }

        /**
         * ut-docs#1246: the Android half of in-app update. The Go core can
         * never self-swap here — it ships as a native library inside the APK
         * and only the package installer may replace an app's own code — so
         * `selfupdate.Supported()` is false on android by design and the
         * Settings page had no actionable control at all. Every build meant
         * telling the operator to go and download an APK by hand, which is
         * what made the pilot's feedback loop unworkable.
         *
         * **Takes no URL, deliberately.** The release APK is resolved here
         * from [UPDATE_APK_URL], a compile-time constant, so nothing the page
         * renders can steer what gets installed. That matters more than usual
         * for this particular method: an argument would turn a JS-reachable
         * bridge into "install arbitrary package", which is the one capability
         * this app must never expose. shouldOverrideUrlLoading already
         * confines the WebView to loopback, but defence in depth is cheap here
         * and the alternative is not recoverable if it ever fails.
         *
         * Android enforces that the downloaded APK is signed with the same key
         * as the installed app, so a substituted package cannot install over
         * this one even if the download were tampered with in transit — the
         * platform provides the integrity guarantee, not this code.
         */
        @JavascriptInterface
        fun installUpdate() {
            runOnUiThread { downloadAndInstallUpdate() }
        }
    }

    /**
     * Enqueues the release APK and hands the finished file to the system
     * package installer. Downloads into the app's own external files dir
     * rather than the shared Downloads collection: that directory needs no
     * storage permission, is what [FileProvider] is configured to expose (see
     * res/xml/file_paths.xml), and keeps a half-written APK out of the
     * operator's Files app.
     *
     * The completion receiver is registered per download and unregistered as
     * soon as it fires, so a till left running for weeks doesn't accumulate
     * receivers. Same never-crash-the-till posture as startDownload: any
     * failure leaves the operator exactly where they were, still able to
     * install by hand.
     */
    private fun downloadAndInstallUpdate() {
        try {
            val dest = File(getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS), UPDATE_APK_NAME)
            if (dest.exists()) {
                dest.delete() // a stale partial from an interrupted attempt must never be installed
            }
            val request =
                DownloadManager.Request(Uri.parse(UPDATE_APK_URL)).apply {
                    setMimeType(APK_MIME)
                    setTitle(getString(R.string.app_name))
                    setNotificationVisibility(DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED)
                    setDestinationInExternalFilesDir(
                        this@MainActivity,
                        Environment.DIRECTORY_DOWNLOADS,
                        UPDATE_APK_NAME,
                    )
                }
            val manager = getSystemService(Context.DOWNLOAD_SERVICE) as DownloadManager
            val id = manager.enqueue(request)
            // POLLED, not a completion broadcast. DownloadManager's
            // ACTION_DOWNLOAD_COMPLETE comes from the system, so a receiver
            // must be registered RECEIVER_EXPORTED to hear it at all —
            // verified the hard way on a real tablet, where a
            // RECEIVER_NOT_EXPORTED registration silently never fired and the
            // 142MB APK just sat on disk. Exporting it would let any app on
            // the device spoof "your download finished", so poll our own
            // query instead: no receiver to leak, no broadcast to spoof, and
            // the terminal states are explicit.
            pollDownload(manager, id, dest)
        } catch (e: Exception) {
            statusView.visibility = View.VISIBLE
            statusView.text = getString(R.string.status_failed, e.message ?: "update download failed")
        }
    }

    /**
     * Watches [id] to a terminal state, then hands the file to the installer.
     * Re-posts itself on the main looper rather than blocking, and gives up
     * after [UPDATE_POLL_LIMIT] ticks so a stalled download can never leave a
     * callback running for the life of the till.
     */
    private fun pollDownload(
        manager: DownloadManager,
        id: Long,
        dest: File,
        tick: Int = 0,
    ) {
        if (tick > UPDATE_POLL_LIMIT) {
            statusView.visibility = View.VISIBLE
            statusView.text = getString(R.string.status_failed, "update download timed out")
            return
        }
        val cursor = manager.query(DownloadManager.Query().setFilterById(id))
        val status =
            cursor.use { c ->
                if (!c.moveToFirst()) return@use DownloadManager.STATUS_FAILED
                c.getInt(c.getColumnIndexOrThrow(DownloadManager.COLUMN_STATUS))
            }
        when (status) {
            DownloadManager.STATUS_SUCCESSFUL -> launchPackageInstaller(dest)
            DownloadManager.STATUS_FAILED -> {
                statusView.visibility = View.VISIBLE
                statusView.text = getString(R.string.status_failed, "update download failed")
            }
            else ->
                webView.postDelayed({ pollDownload(manager, id, dest, tick + 1) }, UPDATE_POLL_MS)
        }
    }

    /**
     * Hands [apk] to the system installer via [FileProvider]. A `file://` URI
     * is rejected outright from API 24 (FileUriExposedException), which is
     * this app's own minSdk — so the content:// grant is the only route, not a
     * modern-Android nicety.
     */
    private fun launchPackageInstaller(apk: File) {
        try {
            // ut-docs#1246: the till runs pinned in lock-task (kiosk) mode, and
            // Android silently REFUSES to start any activity outside the
            // allowlist from a pinned app — "Attempted Lock Task Mode
            // violation r=...packageinstaller/...InstallStart" in logcat, no
            // dialog, no exception, nothing on screen. That is exactly what
            // "I pressed Update and nothing happened" looked like on a real
            // tablet. Release the pin first, the same way KioskBridge
            // .exitLockdown does for the manager's own exit-to-OS path; the
            // installer then appears normally. kioskUnlocked is set so
            // onResume does not immediately re-pin and re-block it.
            kioskUnlocked = true
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
        } catch (e: Exception) {
            // Most likely ActivityNotFoundException, or the user has not yet
            // granted "install unknown apps" for this app. Surface it rather
            // than failing silently — the operator's fallback is a manual
            // install, and they can only choose that if they know this failed.
            statusView.visibility = View.VISIBLE
            statusView.text = getString(R.string.status_failed, e.message ?: "install failed")
        }
    }

    /**
     * ut-docs#1258: a plain WebView has no built-in reaction to a response
     * carrying `Content-Disposition: attachment` (e.g. GET
     * /api/catalog/export) -- without this listener it just tries to render
     * the CSV bytes in place. Registering DownloadListener is Android's own
     * documented mechanism for exactly this gap, invoked automatically by
     * the WebView's rendering engine on a same-origin navigation that turns
     * out to be a download (see catalog.html's window.AndroidKiosk branch,
     * which turns the export button's click into that navigation instead of
     * an htmx POST). No JS-callable bridge method needed -- registering this
     * once on the WebView instance is the whole mechanism.
     *
     * Delegates to Android's own DownloadManager rather than a raw file
     * write: it is the OS-supported way to land a file in the shared
     * Downloads collection (the exact gap the ticket reports -- a raw
     * os.Create in the Go server has nowhere meaningful to write on this
     * OS), and it surfaces the OS's own download-progress/completion
     * notification for free.
     *
     * The download is a fetch of this same till's own loopback origin under
     * its normal manager/admin gate (canPerform in import_page.go), so the
     * WebView's session cookie has to ride along explicitly -- DownloadManager
     * is a separate OS service with no access to the WebView's CookieManager
     * on its own.
     *
     * Independent review (2026-08-29) caught a real gap in the first draft:
     * setDestinationInExternalPublicDir's DESTINATION_FILE_URI needs
     * WRITE_EXTERNAL_STORAGE below API 29 (Q) -- this app's minSdk is 24 --
     * and this app declares no such permission (deliberately: it's a
     * dangerous permission needing its own runtime-grant UI, and every
     * device this pipeline actually targets ships well past Android 10).
     * Without the branch below, enqueue() throws SecurityException on API
     * 24-28 and the old catch below swallowed it -- silently reproducing
     * the exact "button does nothing" bug this ticket reports, just on an
     * older OS range. setDestinationInExternalFilesDir needs no permission
     * at any API level (app-scoped external storage): a real, working save
     * on those older devices, just nested under Android/data/<pkg>/files/
     * rather than the shared top-level Downloads a file manager shows by
     * default, since MediaStore-backed shared Downloads is itself the
     * post-Q model.
     */
    private fun startDownload(
        url: String,
        contentDisposition: String?,
        mimeType: String?,
    ) {
        try {
            val filename = URLUtil.guessFileName(url, contentDisposition, mimeType)
            val request =
                DownloadManager.Request(Uri.parse(url)).apply {
                    val cookie = CookieManager.getInstance().getCookie(url)
                    if (!cookie.isNullOrEmpty()) {
                        addRequestHeader("Cookie", cookie)
                    }
                    // setMimeType's Android SDK signature is not reliably
                    // annotated nullable across API levels this app spans
                    // (minSdk 24 - compileSdk 36) -- default to a real MIME
                    // type rather than pass through a possibly-null one.
                    setMimeType(mimeType ?: "text/csv")
                    setNotificationVisibility(DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED)
                    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                        setDestinationInExternalPublicDir(Environment.DIRECTORY_DOWNLOADS, filename)
                    } else {
                        setDestinationInExternalFilesDir(this@MainActivity, Environment.DIRECTORY_DOWNLOADS, filename)
                    }
                }
            (getSystemService(Context.DOWNLOAD_SERVICE) as DownloadManager).enqueue(request)
        } catch (e: Exception) {
            // Same never-crash-the-till posture as engageKioskLock/
            // releaseKioskLock above: a failed download must not take down
            // a live till. Unlike those two, though, an exception here means
            // NOTHING was ever enqueued -- DownloadManager itself has
            // nothing to show a failure notification about, so the OS gives
            // no signal at all (independent review, 2026-08-29: the original
            // comment here claimed otherwise, incorrectly). catalog.html's
            // in-page "download started" notice (shown optimistically,
            // before this call, since DownloadManager itself is
            // fire-and-forget from here) is deliberately NOT a success
            // guarantee for the same reason -- see its own comment.
        }
    }

    /**
     * Hides the status and navigation bars (immersive full-screen). Purely
     * cosmetic defense-in-depth on top of [engageKioskLock] — Lock Task is
     * what actually prevents leaving the app; this just keeps the OS chrome
     * (clock, notification shade handle, nav buttons) out of a shop
     * customer's reach-by-glance. BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
     * means a swipe reveals the bars briefly and they auto-hide again,
     * which is the standard kiosk-friendly behavior.
     */
    private fun applyImmersiveMode() {
        WindowCompat.setDecorFitsSystemWindows(window, false)
        val controller = WindowCompat.getInsetsController(window, window.decorView)
        controller.systemBarsBehavior =
            WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
        controller.hide(WindowInsetsCompat.Type.systemBars())
    }

    /** The inverse of [applyImmersiveMode], for the unlocked-via-exitLockdown state. */
    private fun clearImmersiveMode() {
        WindowCompat.setDecorFitsSystemWindows(window, true)
        WindowCompat.getInsetsController(window, window.decorView)
            .show(WindowInsetsCompat.Type.systemBars())
    }

    /**
     * Engages Android's Lock Task mode (ut-docs#1254) so the till can't be
     * left like an ordinary app. Two strengths, decided at runtime:
     *
     *  - If this app has been provisioned as Device Owner (a manual,
     *    physical, one-time step — `adb shell dpm set-device-owner ...` or
     *    QR provisioning at factory reset; never attempted from code, see
     *    [TillDeviceAdminReceiver]), setLockTaskPackages whitelists this
     *    package first, which makes startLockTask() enter FULL lock-task
     *    mode: no unpin gesture, no user exit at all.
     *  - Otherwise (every unprovisioned device — the realistic case today)
     *    startLockTask() alone engages standard screen-pinning: Home and
     *    Recents are blocked, but Android's documented back+overview
     *    long-press unpin gesture still works. A real, known-weaker mode —
     *    documented as such in android/README.md, not silently claimed
     *    secure.
     *
     * Idempotent when already locked, so re-asserting it (onResume, page
     * navigation) is safe.
     */
    private fun engageKioskLock() {
        try {
            val dpm = getSystemService(Context.DEVICE_POLICY_SERVICE) as? DevicePolicyManager
            if (dpm != null && dpm.isDeviceOwnerApp(packageName)) {
                dpm.setLockTaskPackages(
                    ComponentName(this, TillDeviceAdminReceiver::class.java),
                    arrayOf(packageName),
                )
            }
            startLockTask()
        } catch (e: Exception) {
            // Deliberately broad: kiosk bookkeeping must never take down a
            // live till, and the exact exception types thrown here vary by
            // OS version/OEM (SecurityException, IllegalStateException,
            // undocumented others) — can't be pinned down without a device
            // matrix to verify against. Failing to pin leaves the app in
            // its pre-#1254 (unpinned but fully working) state.
        }
    }

    /** Releases Lock Task / screen-pinning. Same never-crash posture as [engageKioskLock]. */
    private fun releaseKioskLock() {
        try {
            stopLockTask()
        } catch (e: Exception) {
            // See engageKioskLock — never let unlock bookkeeping crash the till.
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)

        webView = findViewById(R.id.webview)
        statusView = findViewById(R.id.status)
        // ut-docs#412: this bar (including the loopback address it shows)
        // is a debug convenience for developers running the wrapper off a
        // USB-attached device, not something a shop worker has any use
        // for — it ate scarce vertical space on a phone screen and leaked
        // an internal bind address to end users. Real users never see it;
        // BuildConfig.DEBUG is false in the release build shipped via
        // release.yml's android-app job. The foreground-service
        // notification (TillService.buildNotification) still surfaces
        // "till is running" without the address, in every build — that
        // one's a genuine OS requirement, not a diagnostic.
        statusView.visibility = if (BuildConfig.DEBUG) View.VISIBLE else View.GONE
        webView.settings.javaScriptEnabled = true
        webView.settings.domStorageEnabled = true
        // ut-docs#1254: see KioskBridge's own KDoc for why this is safe to
        // expose — it depends on webViewClient's shouldOverrideUrlLoading
        // below, registered before that WebViewClient is (next statement),
        // so there is no window where this interface is live without that
        // navigation restriction also being in place.
        webView.addJavascriptInterface(KioskBridge(), "AndroidKiosk")
        // ut-docs#1258: see startDownload's KDoc above for why this exists.
        webView.setDownloadListener { url, _, contentDisposition, mimeType, _ ->
            startDownload(url, contentDisposition, mimeType)
        }
        webView.webViewClient =
            object : WebViewClient() {
                // ut-docs#1254 (review should-fix 3): confine this WebView
                // to the till's own loopback origin. Without this, an
                // in-page link this app doesn't control the far end of —
                // e.g. my_reports.html's "open GitHub issue" link, which
                // carries no platform gating at all — would navigate this
                // SAME WebView instance to an untrusted page, where
                // window.AndroidKiosk (injected into every page this
                // WebView ever shows, per addJavascriptInterface's own
                // documented scope) would still be reachable from content
                // this app never authored. Refuses everything until
                // allowedHost is known (nothing has legitimately loaded
                // yet at that point either). This is the actual
                // enforcement the KioskBridge/addJavascriptInterface
                // comments below describe — declaring it in prose without
                // this override would be describing a guarantee that
                // doesn't exist.
                override fun shouldOverrideUrlLoading(
                    view: WebView?,
                    request: WebResourceRequest?,
                ): Boolean {
                    val target = request?.url ?: return true
                    if (target.authority != allowedHost) {
                        return true // block: refuse to navigate off-origin
                    }
                    return false // same-origin: let the WebView load it normally
                }

                // ut-docs#414: the loaded page already carries the till's
                // configured locale on every render (base.html's
                // <html lang="..."> — the same value web/locales/*.json is
                // fully translated against), independent of the phone's own
                // device locale. Reading it back here and applying it via
                // AppCompatDelegate is what actually makes the NATIVE
                // wrapper chrome (the foreground-service notification;
                // TillService.refreshLocalizedNotification below) follow
                // the till's own language instead of staying stuck on
                // English/the device locale — that mismatch, not missing
                // translated strings by themselves, was the actual gap
                // this ticket reports.
                //
                // Independent review (2026-08-07) found and fixed two real
                // bugs in the first draft of this mechanism, both load-
                // bearing:
                //  1. `?lang=` reaches <html lang="..."> UNVALIDATED
                //     (internal/httpx.ResolveLocale) -- a value like "EN",
                //     "fa_IR", or garbage would either never match the
                //     comparison below (permanently re-firing on every
                //     navigation) or reach LocaleListCompat.forLanguageTags
                //     as garbage. Gated on KNOWN_LOCALES (the same four
                //     this app actually ships translations for) before
                //     touching AppCompatDelegate at all, and the
                //     comparison itself now normalizes both sides through
                //     the same LocaleListCompat round-trip instead of
                //     comparing a canonical tag against raw DOM text.
                //  2. AppCompatDelegate.setApplicationLocales() recreates
                //     every resumed AppCompatActivity when the locale
                //     actually changes (AndroidX's documented, OS-native
                //     per-app-language mechanism) -- this Activity reloads
                //     the WebView at TillService's root address afterward,
                //     same as a normal cold start. Accepted deliberately: a
                //     language switch is a rare, deliberate settings-type
                //     action (done from /menu), and landing back on the
                //     sale screen, fully localized, is standard per-app-
                //     language UX, not a regression -- BUT ONLY if this
                //     doesn't also fire on every ordinary cold launch. The
                //     original comment claimed the applied locale persists
                //     across process restarts "automatically" -- false:
                //     AndroidX's auto-storage is opt-in via the
                //     AppLocalesMetadataHolderService declaration
                //     (AndroidManifest.xml), which the first draft never
                //     added, so sRequestedAppLocales was null at every
                //     process start and this recreated the Activity on
                //     EVERY cold launch, not just on a genuine language
                //     change. Fixed by adding that manifest declaration.
                override fun onPageFinished(view: WebView?, url: String?) {
                    super.onPageFinished(view, url)
                    // ut-docs#1254: unlocking is [KioskBridge.exitLockdown]'s
                    // job, not this method's — see its KDoc for why. This
                    // side only ever RE-LOCKS: /login and /settings are the
                    // two manager-facing screens exitLockdown can be called
                    // from, so navigation landing anywhere else (the sale
                    // screen, /self-order, ...) means the manager is done
                    // and the till is handed back — re-engage defensively
                    // even if onResume's own reset (below) never fires
                    // because the Activity was never actually backgrounded.
                    url?.let {
                        val path = Uri.parse(it).path ?: ""
                        val managerFacing = path == "/login" || path == "/settings" || path.startsWith("/settings/")
                        if (!managerFacing && kioskUnlocked) {
                            kioskUnlocked = false
                            engageKioskLock()
                            applyImmersiveMode()
                        }
                    }
                    view?.evaluateJavascript("document.documentElement.lang") { result ->
                        // evaluateJavascript's callback value is always
                        // JSON-encoded ("\"en\"", or the literal string
                        // "null" if the attribute is absent/empty) — never
                        // raw text.
                        val raw = result?.trim('"')?.takeIf { it.isNotBlank() && it != "null" }
                        val lang = raw?.lowercase()?.takeIf { it in KNOWN_LOCALES }
                        if (lang != null) {
                            val requested = LocaleListCompat.forLanguageTags(lang)
                            if (AppCompatDelegate.getApplicationLocales().toLanguageTags() != requested.toLanguageTags()) {
                                AppCompatDelegate.setApplicationLocales(requested)
                                // Same-process call: AppCompatDelegate's
                                // static locale state (what str()/
                                // ContextCompat.getString resolve against)
                                // is already updated at this point, even
                                // though the visual Activity recreate this
                                // triggers hasn't happened yet -- so the
                                // service's notification can catch up
                                // immediately instead of waiting for a
                                // restart it may never get.
                                till?.refreshLocalizedNotification()
                            }
                        }
                    }
                }
            }
        webView.webChromeClient =
            object : WebChromeClient() {
                override fun onShowFileChooser(
                    webView: WebView?,
                    filePathCallback: ValueCallback<Array<Uri>>?,
                    fileChooserParams: FileChooserParams?,
                ): Boolean {
                    // A second chooser opening while one is already pending
                    // (shouldn't happen from a single WebView, but the
                    // contract requires resolving any outstanding callback)
                    // must not be leaked.
                    fileChooserCallback?.onReceiveValue(null)
                    fileChooserCallback = filePathCallback
                    val intent =
                        fileChooserParams?.createIntent()
                            ?: Intent(Intent.ACTION_GET_CONTENT).apply { type = "*/*" }
                    return try {
                        fileChooserLauncher.launch(intent)
                        true
                    } catch (e: ActivityNotFoundException) {
                        fileChooserCallback = null
                        false
                    }
                }
            }

        statusView.text = getString(R.string.status_starting)

        onBackPressedDispatcher.addCallback(
            this,
            object : OnBackPressedCallback(true) {
                override fun handleOnBackPressed() {
                    if (webView.canGoBack()) {
                        webView.goBack()
                    } else {
                        isEnabled = false
                        onBackPressedDispatcher.onBackPressed()
                    }
                }
            },
        )

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            requestNotificationPermission.launch(android.Manifest.permission.POST_NOTIFICATIONS)
        }

        val intent = Intent(this, TillService::class.java)
        ContextCompat.startForegroundService(this, intent)
        bindService(intent, connection, Context.BIND_AUTO_CREATE)

        // ut-docs#1254: no lock/immersive call here — startLockTask() acts
        // on the RESUMED task, so calling it this early (review finding:
        // "probably inert" before the Activity has actually resumed) would
        // just be a documented no-op the broad catch in engageKioskLock
        // silently swallows. onResume below always runs one lifecycle step
        // after onCreate, on every cold launch as much as every real
        // resume — a till boots straight into kiosk mode with no separate
        // "wait for the first page load" step, exactly as before, just
        // without the redundant call.
    }

    override fun onResume() {
        super.onResume()
        // ut-docs#1254: unconditionally re-lock on every resume — the
        // FIRST call on a cold launch (a till boots straight into kiosk
        // mode) and every later one, even if exitLockdown() granted an
        // unlock earlier: resuming after a real background (Home/Recents/
        // another app) is exactly the point a kiosk must default back to
        // secure, not trust that whatever the manager was doing out there
        // is still what's wanted. Both calls are idempotent when already
        // applied.
        kioskUnlocked = false
        applyImmersiveMode()
        engageKioskLock()
    }

    override fun onDestroy() {
        super.onDestroy()
        // Unbind (not stop) TillService — the till keeps running
        // regardless of whether this Activity exists (a real POS terminal
        // isn't "done" just because its screen isn't currently shown).
        // till may already be null if the service connection never
        // completed.
        till?.removeListener(listener)
        unbindService(connection)
        // WebView instances are a well-known Android leak if not
        // explicitly destroyed — detach from its parent first (destroying
        // an attached WebView is documented as unsafe) then release it.
        (webView.parent as? android.view.ViewGroup)?.removeView(webView)
        webView.destroy()
    }

    companion object {
        // ut-docs#1246: the release APK's STABLE url — release.yml republishes
        // this same filename on every release (see its
        // unitill-pos-android-latest.apk step), so the shell never has to
        // resolve a version or talk to the GitHub API to find the newest
        // build. Compile-time constant on purpose: installUpdate() takes no
        // argument, so page content can never redirect an install.
        private const val UPDATE_APK_URL =
            "https://github.com/universaltill/universal-till/releases/latest/download/unitill-pos-android-latest.apk"
        private const val UPDATE_APK_NAME = "unitill-pos-update.apk"
        private const val APK_MIME = "application/vnd.android.package-archive"

        // ~5 minutes at 750ms: long enough for a ~140MB APK on shop WiFi,
        // bounded so a stalled download can't poll forever.
        private const val UPDATE_POLL_MS = 750L
        private const val UPDATE_POLL_LIMIT = 400

        // ut-docs#414: the exact locale codes this app ships native string
        // resources for (values-fa/, values-tr/, values-ar/, plus the
        // default values/ for "en") — matches
        // universal-till/internal/httpx.AvailableLocales()'s set. A gate,
        // not just a formatting nicety: internal/httpx.ResolveLocale
        // accepts any `?lang=` value unvalidated and writes it straight
        // into <html lang="...">, so without this a malformed or
        // unsupported value would either permanently mismatch the
        // comparison above (re-firing setApplicationLocales on every
        // single page load) or reach LocaleListCompat.forLanguageTags as
        // outright garbage (independent review, 2026-08-07).
        private val KNOWN_LOCALES = setOf("en", "fa", "tr", "ar")
    }
}
