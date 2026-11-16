package com.universaltill.pos.tap

import android.app.AlertDialog
import android.content.Intent
import android.net.Uri
import android.provider.Settings
import android.webkit.WebView
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.webkit.JavaScriptReplyProxy
import androidx.webkit.WebMessageCompat
import androidx.webkit.WebViewCompat
import androidx.webkit.WebViewFeature
import com.universaltill.pos.BuildConfig
import com.universaltill.pos.R

/** Education + location rationale + settings hand-off (all English strings in strings.xml). */
class AndroidUi(private val activity: AppCompatActivity, private val prefs: TapPrefs) : UiActions {
    private var permissionCb: ((Boolean) -> Unit)? = null
    private val permissionLauncher =
        activity.registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
            permissionCb?.invoke(granted)
            permissionCb = null
        }

    override fun showEducation(cb: (Boolean) -> Unit) {
        AlertDialog.Builder(activity)
            .setTitle(R.string.tap_edu_title)
            .setMessage(R.string.tap_edu_body)
            .setPositiveButton(R.string.tap_edu_accept) { _, _ ->
                prefs.educationAccepted = true
                cb(true)
            }
            .setNegativeButton(R.string.tap_edu_decline) { _, _ -> cb(false) }
            .setOnCancelListener { cb(false) }
            .show()
    }

    override fun requestLocationPermission(cb: (Boolean) -> Unit) {
        // Our rationale first; the system prompt only after the operator agrees. Never at boot.
        AlertDialog.Builder(activity)
            .setTitle(R.string.tap_loc_rationale_title)
            .setMessage(R.string.tap_loc_rationale_body)
            .setPositiveButton(R.string.tap_loc_rationale_ok) { _, _ ->
                permissionCb = cb
                permissionLauncher.launch(android.Manifest.permission.ACCESS_FINE_LOCATION)
            }
            .setNegativeButton(R.string.tap_loc_rationale_cancel) { _, _ -> cb(false) }
            .setOnCancelListener { cb(false) }
            .show()
    }

    /** Fixed allowlist of system screens; nothing from the page picks an action or a URL. */
    override fun openSettings(target: String): Boolean {
        val intent =
            when (target) {
                "location" -> Intent(Settings.ACTION_LOCATION_SOURCE_SETTINGS)
                "nfc" -> Intent(Settings.ACTION_NFC_SETTINGS)
                "app" -> Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.fromParts("package", activity.packageName, null))
                else -> return false
            }
        return try {
            activity.startActivity(intent)
            true
        } catch (_: Exception) {
            false
        }
    }
}

/**
 * Glue between MainActivity and the engine (design section 5). Created only
 * when BuildConfig.TAP_TO_PAY_ENABLED and an engine exists; otherwise
 * MainActivity holds null and nothing here ever runs.
 */
class TapToPayShell private constructor(private val engine: TapToPayEngine, private val holder: Holder) {
    class Holder {
        var proxy: JavaScriptReplyProxy? = null

        fun emit(json: String) {
            // A reloaded page invalidates the proxy; results stay pollable via op "result".
            try { proxy?.postMessage(json) } catch (_: Exception) {}
        }
    }

    private var attachedHost: String? = null

    /** Registers window.AndroidTapToPay for exactly this origin, main frame only. Never addJavascriptInterface. */
    fun attach(webView: WebView, host: String) {
        if (attachedHost == host) return
        if (!WebViewFeature.isFeatureSupported(WebViewFeature.WEB_MESSAGE_LISTENER)) return
        attachedHost = host
        WebViewCompat.addWebMessageListener(webView, "AndroidTapToPay", setOf("http://$host")) { _, message: WebMessageCompat, origin: Uri, isMainFrame: Boolean, reply: JavaScriptReplyProxy ->
            // The rule set above already restricts origins; re-check per message (defence in depth).
            if (!OriginPolicy.allows(origin.toString(), isMainFrame, host)) return@addWebMessageListener
            val raw = message.data ?: return@addWebMessageListener
            holder.proxy = reply
            engine.handle(raw) { json -> reply.postMessage(json) }
        }
    }

    fun onStart() = engine.onForeground()

    /** Not onPause: the Stripe screen pauses our Activity mid-payment. */
    fun onStop() = engine.onBackground()

    fun onDestroy(isFinishing: Boolean) = engine.onDestroy(isFinishing)

    companion object {
        fun create(activity: AppCompatActivity): TapToPayShell? {
            if (!BuildConfig.TAP_TO_PAY_ENABLED) return null
            val prefs = TapPrefs(activity)
            val holder = Holder()
            val engine = TapToPayPlatform.create(activity, AndroidDeviceProbe(activity, prefs), holder::emit) ?: return null
            return TapToPayShell(engine, holder)
        }
    }
}
