package com.universaltill.pos.tap

import android.app.AlertDialog
import android.content.Intent
import android.provider.Settings
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import com.universaltill.pos.R
import mobile.Mobile

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

    /**
     * Explains, then opens one of a fixed allowlist of system screens; nothing outside this app picks an action
     * or a URL. true = the prompt was shown (the operator may still decline).
     */
    override fun openSettings(target: String): Boolean {
        val (title, body, action) =
            when (target) {
                "location" -> Triple(R.string.tap_enable_location_title, R.string.tap_enable_location_body, Settings.ACTION_LOCATION_SOURCE_SETTINGS)
                "nfc" -> Triple(R.string.tap_enable_nfc_title, R.string.tap_enable_nfc_body, Settings.ACTION_NFC_SETTINGS)
                else -> return false
            }
        AlertDialog.Builder(activity)
            .setTitle(title)
            .setMessage(body)
            .setPositiveButton(R.string.tap_settings_open) { _, _ ->
                try {
                    activity.startActivity(Intent(action))
                } catch (_: Exception) {
                    // No such settings screen on this device: the setup result already names what is off.
                }
            }
            .setNegativeButton(R.string.tap_loc_rationale_cancel, null)
            .show()
        return true
    }
}

/**
 * Per-Activity glue (design section 5): points the process-scoped engine at this Activity's UI and registers
 * the devicepay bridge with Go while the Activity is started (ADR-0125 3e: registered in the foreground, nil
 * in the background). Created only when the flag is on and an engine exists; otherwise MainActivity holds null
 * and nothing here runs. There is no page API: no WebView listener, no JavaScript interface.
 */
class TapToPayShell private constructor(
    private val engine: TapToPayEngine,
    private val ui: AndroidUi,
) {
    private val bridge = TapToPayProcess.bridgeFor(engine)

    /** Not onResume: the Stripe tap screen pauses our Activity mid-payment. */
    fun onStart() {
        engine.onForeground()
        Mobile.setTapToPayBridge(bridge)
        Mobile.setTapToPayResolver(bridge)
    }

    /** Not onPause, for the same reason. */
    fun onStop() {
        engine.onBackground()
        Mobile.setTapToPayBridge(null)
        Mobile.setTapToPayResolver(null)
    }

    fun onDestroy(isFinishing: Boolean) {
        // Do not keep a destroyed Activity reachable from the process-scoped engine.
        if (TapToPayProcess.ui.target === ui) TapToPayProcess.ui.target = null
        engine.onDestroy(isFinishing)
    }

    companion object {
        fun create(activity: AppCompatActivity): TapToPayShell? {
            val engine = TapToPayProcess.engineFor(activity) ?: return null
            // Registers an activity-result launcher, so it must run in onCreate.
            val ui = AndroidUi(activity, TapPrefs(activity))
            TapToPayProcess.ui.target = ui
            return TapToPayShell(engine, ui)
        }
    }
}
