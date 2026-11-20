package com.universaltill.pos.reader

import android.app.KeyguardManager
import android.os.Build
import android.os.Bundle
import android.os.VibrationEffect
import android.os.Vibrator
import android.text.InputType
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.view.WindowManager
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import com.universaltill.pos.R
import com.universaltill.pos.tap.AndroidUi
import com.universaltill.pos.tap.Cancelable
import com.universaltill.pos.tap.TapPrefs
import java.util.Locale
import java.util.concurrent.Executors

/**
 * Reader mode's screens (UX C1-C5, 360 px, one thumb). Ships dark: the Activity is disabled in the manifest unless the
 * build says -PreaderMode=true, and it runs in its own process (`:reader`), so it never starts the till's Go server and
 * holds no shop data. It draws what [ReaderSession] says and nothing else: no basket, no customer, no card data, and
 * never the words charged / not charged.
 */
class ReaderActivity : AppCompatActivity(), ReaderUi {
    private val bg = Executors.newSingleThreadExecutor()
    private lateinit var root: LinearLayout
    private lateinit var store: ReaderStore
    private var parts: ReaderProcess.Parts? = null
    private var discovery: MainDiscovery? = null
    private var tick: Cancelable? = null
    private var pairing: Cancelable? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER_HORIZONTAL
            setPadding(dp(24), dp(32), dp(24), dp(24))
        }
        setContentView(root)
        store = KeystoreReaderStore(this)
        when {
            ReaderProcess.gate(this) != ReaderGate.Available -> return message(R.string.reader_unsupported)
            !(getSystemService(KEYGUARD_SERVICE) as KeyguardManager).isDeviceSecure -> return message(R.string.reader_lock_required)
        }
        val p = ReaderProcess.partsFor(this) ?: return message(R.string.reader_unsupported)
        parts = p
        // Registers an activity-result launcher (location prompt), so it must run in onCreate.
        p.tapUi.target = AndroidUi(this, TapPrefs(this))
        val creds = store.load()
        if (creds == null) enrol() else run(creds)
    }

    override fun onStart() {
        super.onStart()
        val p = parts ?: return
        p.session.onForeground()
        if (store.load() != null) ReaderProcess.link?.start()
    }

    override fun onStop() {
        super.onStop()
        val p = parts ?: return
        p.session.onBackground()
        // A payment in flight keeps its link: Stripe's own screen pauses this Activity mid-tap.
        if (!p.session.hasLive) ReaderProcess.link?.stop()
    }

    override fun onDestroy() {
        super.onDestroy()
        discovery?.stop()
        tick?.cancel(); pairing?.cancel()
        parts?.let { if (it.ui.target === this) it.ui.target = null }
    }

    private fun dp(v: Int) = (v * resources.displayMetrics.density).toInt()

    private fun text(@androidx.annotation.StringRes id: Int, size: Float = 20f, vararg args: Any, live: Boolean = true): TextView =
        TextView(this).apply {
            text = getString(id, *args)
            textSize = size
            gravity = Gravity.CENTER
            if (live) accessibilityLiveRegion = View.ACCESSIBILITY_LIVE_REGION_ASSERTIVE
            layoutParams = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { topMargin = dp(12) }
        }

    private fun button(@androidx.annotation.StringRes id: Int, onClick: () -> Unit): Button =
        Button(this).apply {
            setText(id)
            minHeight = dp(56)
            layoutParams = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { topMargin = dp(16) }
            setOnClickListener { onClick() }
        }

    private fun fresh(keepAwake: Boolean = false) {
        tick?.cancel(); tick = null
        root.removeAllViews()
        if (keepAwake) window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON) else window.clearFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
    }

    private fun message(@androidx.annotation.StringRes id: Int) {
        fresh()
        root.addView(text(id))
    }

    // ---- C1 / C2 enrolment ----

    private fun enrol(error: Int? = null) {
        fresh()
        root.addView(text(R.string.reader_offer_title, 24f))
        root.addView(text(R.string.reader_offer_body, 16f, live = false))
        error?.let { root.addView(text(it, 16f)) }
        val name = EditText(this).apply {
            setText(Build.MODEL ?: "Phone")
            inputType = InputType.TYPE_CLASS_TEXT
            hint = getString(R.string.reader_name_label)
            filters = arrayOf(android.text.InputFilter.LengthFilter(40))
        }
        root.addView(name)
        val list = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        val status = text(R.string.reader_finding_main, 16f)
        root.addView(status)
        root.addView(list)
        discovery?.stop()
        discovery = MainDiscovery(this) { m ->
            status.setText(R.string.reader_pick_main)
            list.addView(button(R.string.reader_request_btn) { startPairing(m, name.text.toString().trim().ifEmpty { "Phone" }) }.apply { text = "${getString(R.string.reader_request_btn)}: ${m.shop}" })
        }.also { it.start() }
    }

    private fun startPairing(m: FoundMain, deviceName: String) {
        discovery?.stop()
        val p = ReaderPairing(OkPairHttp(), nowMs = System::currentTimeMillis)
        bg.execute {
            val r = p.start(m.address, m.tillId, deviceName)
            MainThread.post {
                when (r) {
                    is PairStart.Failed -> enrol(
                        when (r.reason) {
                            "needs_tls" -> R.string.reader_err_needs_tls
                            "not_found" -> R.string.reader_err_not_found
                            else -> R.string.reader_err_refused
                        },
                    )
                    is PairStart.Waiting -> waiting(r.code, p)
                }
            }
        }
    }

    private fun waiting(code: String, p: ReaderPairing) {
        fresh(keepAwake = true)
        root.addView(text(R.string.reader_waiting, 24f))
        root.addView(text(R.string.reader_code_label, 14f, live = false))
        root.addView(TextView(this).apply { text = code; textSize = 48f; gravity = Gravity.CENTER; layoutDirection = View.LAYOUT_DIRECTION_LTR })
        root.addView(text(R.string.reader_waiting_hint, 16f, live = false))
        var cancelled = false
        root.addView(button(R.string.reader_cancel) { cancelled = true; pairing?.cancel(); enrol() })
        fun poll() {
            if (cancelled) return
            bg.execute {
                val r = p.poll()
                MainThread.post {
                    if (cancelled) return@post
                    when (r) {
                        PairPoll.Waiting -> pairing = MainThread.postDelayed(2_000) { poll() }
                        PairPoll.Expired -> failed(R.string.reader_expired_request)
                        is PairPoll.Failed -> failed(R.string.reader_err_refused)
                        is PairPoll.Approved -> {
                            store.save(r.credentials)
                            run(r.credentials)
                            ReaderProcess.link?.start()
                        }
                    }
                }
            }
        }
        poll()
    }

    private fun failed(msg: Int) {
        fresh()
        root.addView(text(msg))
        root.addView(button(R.string.reader_try_again) { enrol() })
    }

    // ---- paired: C3-C5 ----

    private fun run(c: ReaderCredentials) {
        val p = parts ?: return
        p.ui.target = this
        if (ReaderProcess.link == null) ReaderProcess.link = ReaderLink(c, p.session, MainThread::post, MainThread::postDelayed)
        ReaderProcess.link?.start()
        show(p.ui.last)
    }

    override fun incoming() {
        val v = getSystemService(VIBRATOR_SERVICE) as? Vibrator ?: return
        v.vibrate(VibrationEffect.createOneShot(300, VibrationEffect.DEFAULT_AMPLITUDE))
    }

    override fun revoked() {
        store.clear()
        ReaderProcess.link?.stop()
        ReaderProcess.link = null
    }

    override fun show(s: ReaderScreen) {
        runOnUiThread { draw(s) }
    }

    private fun draw(s: ReaderScreen) {
        when (s) {
            ReaderScreen.Connecting, ReaderScreen.Offline -> { fresh(); root.addView(text(R.string.reader_offline)) }
            ReaderScreen.Idle -> { fresh(keepAwake = true); root.addView(text(R.string.reader_idle, 24f)) }
            ReaderScreen.Paused -> { fresh(); root.addView(text(R.string.reader_paused)) }
            ReaderScreen.NeedsSetup -> {
                fresh()
                root.addView(text(R.string.reader_needs_setup))
                root.addView(button(R.string.reader_setup_btn) { parts?.session?.setup() })
            }
            ReaderScreen.Revoked -> {
                fresh()
                root.addView(text(R.string.reader_revoked))
                root.addView(button(R.string.reader_try_again) { enrol() })
            }
            is ReaderScreen.Pay -> pay(s)
            is ReaderScreen.Result ->
                { fresh(keepAwake = true); root.addView(text(resultText(s.kind), 28f)) }
        }
    }

    private fun resultText(k: ReaderResultKind) =
        when (k) {
            ReaderResultKind.DONE -> R.string.reader_done
            ReaderResultKind.CANCELLED -> R.string.reader_cancelled
            ReaderResultKind.CARD_REFUSED -> R.string.reader_card_refused
            ReaderResultKind.EXPIRED -> R.string.reader_time_up
            ReaderResultKind.LINK_LOST -> R.string.reader_link_lost
        }

    private fun pay(s: ReaderScreen.Pay) {
        fresh(keepAwake = true)
        val amount = ReaderAmount.format(s.amountMinor, s.currency, Locale.getDefault())
        root.addView(text(R.string.reader_incoming, 16f, amount))
        root.addView(TextView(this).apply {
            text = amount
            gravity = Gravity.CENTER
            maxLines = 1
            setAutoSizeTextTypeUniformWithConfiguration(24, 72, 2, android.util.TypedValue.COMPLEX_UNIT_SP)
            layoutParams = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, dp(96)).apply { topMargin = dp(16) }
        })
        root.addView(text(R.string.reader_hold, 20f))
        // The phone's own window; the Stripe sheet takes over the card read, this wraps amount, time and Cancel.
        val left = text(R.string.reader_time_left, 14f, 0, live = false)
        root.addView(left)
        val ends = System.currentTimeMillis() + s.timeoutMs
        fun update() {
            val sec = maxOf(0L, (ends - System.currentTimeMillis()) / 1000)
            left.text = getString(R.string.reader_time_left, sec)
            tick = MainThread.postDelayed(1_000) { update() }
        }
        update()
        root.addView(View(this).apply { layoutParams = LinearLayout.LayoutParams(1, 0, 1f) })
        root.addView(button(R.string.reader_cancel) { parts?.session?.cancelLive() })
    }
}
