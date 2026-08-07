package com.universaltill.pos

import android.app.Service
import android.content.Intent
import android.os.Binder
import android.os.IBinder
import androidx.core.app.NotificationChannelCompat
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat
import mobile.Mobile

/**
 * Runs the embedded till server (mobile.Mobile) as a genuine Android
 * foreground service (ADR-0023 §1's target packaging shape), not tied to
 * MainActivity's own lifecycle. This is the fix for the first working
 * prototype's real limitation: with Start/Stop called directly from
 * onCreate/onDestroy, a screen rotation (which by default destroys and
 * recreates the Activity) would kill and restart the whole server —
 * losing in-memory basket state and forcing a visible reload for no
 * reason a cashier would understand. A foreground service outlives
 * Activity recreation entirely; MainActivity just binds to whichever
 * instance is already running (or starts a new one if none is).
 */
class TillService : Service() {
    private val binder = LocalBinder()

    /** [MainActivity] reads [address]/[startError] after binding, and can
     * register a listener to be notified once Start finishes (it may
     * still be starting up at bind time). */
    inner class LocalBinder : Binder() {
        val service: TillService get() = this@TillService
    }

    @Volatile
    var address: String? = null
        private set

    @Volatile
    var startError: String? = null
        private set

    private val listeners = mutableListOf<(String?, String?) -> Unit>()

    /** Registers a listener for the (address, error) Start eventually
     * resolves to — called immediately with the current state if Start
     * has already finished (success or failure) by the time this is
     * called, so a late-binding Activity (e.g. after rotation) doesn't
     * miss an already-resolved result.
     *
     * Correctness note (confirmed by independent review, 2026-07-25 —
     * do not reorder these two operations): this registers under the
     * [listeners] lock, THEN reads [address]/[startError] outside it.
     * [onCreate]'s worker thread does the reverse — writes address/
     * startError, THEN snapshots [listeners] under the lock. Because both
     * sides use the same monitor, the two critical sections can never
     * overlap, which guarantees every listener is called at least once
     * (either by this immediate replay, or by the worker's later
     * snapshot) — flipping the order here would open a real window where
     * a listener registered between the worker's write and its lock
     * acquisition is never called at all. A listener CAN be called twice
     * in the current, safe ordering (harmless here — MainActivity's own
     * reload-avoidance check absorbs it); don't rely on exactly-once if
     * this mechanism is ever reused for something that isn't idempotent. */
    fun addListener(listener: (address: String?, error: String?) -> Unit) {
        synchronized(listeners) { listeners.add(listener) }
        if (address != null || startError != null) {
            listener(address, startError)
        }
    }

    fun removeListener(listener: (String?, String?) -> Unit) {
        synchronized(listeners) { listeners.remove(listener) }
    }

    override fun onBind(intent: Intent?): IBinder = binder

    // ut-docs#414 (independent review, 2026-08-07): a plain Service's own
    // getString() does NOT follow AppCompatDelegate.setApplicationLocales()
    // on API 24-32 — that per-app-language mechanism only patches
    // AppCompatActivity contexts on those levels (AppCompatDelegate's own
    // javadoc says so explicitly). ContextCompat.getString/getContextForLanguage
    // is the documented fix: it resolves against the process-wide per-app
    // locale AppCompatDelegate maintains, regardless of what kind of
    // Context asks. Every string this Service surfaces to a real user (the
    // notification title/text, the channel name) MUST go through this, not
    // the bare getString() the rest of this file used before that review.
    private fun str(resId: Int) = ContextCompat.getString(this, resId)

    override fun onCreate() {
        super.onCreate()
        val channel =
            NotificationChannelCompat.Builder(CHANNEL_ID, NotificationManagerCompat.IMPORTANCE_LOW)
                .setName(str(R.string.notification_channel_name))
                .build()
        NotificationManagerCompat.from(this).createNotificationChannel(channel)
        startForeground(NOTIFICATION_ID, buildNotification(str(R.string.status_starting)))

        // Start() is idempotent (mobile/mobile.go) — a service restart
        // (e.g. START_STICKY after the OS killed it under memory
        // pressure) safely re-attaches rather than double-starting.
        Thread {
            try {
                val addr = Mobile.start(filesDir.absolutePath)
                address = addr
                // ut-docs#412: the notification text never carries the raw
                // loopback address (real Android requirement to convey
                // "still running" — but not a place to leak an internal
                // bind detail to the end user). The listener callback still
                // gets the real addr — MainActivity's debug-only status bar
                // and the WebView's loadUrl() both need it.
                updateNotification(str(R.string.notification_running))
                synchronized(listeners) { listeners.toList() }.forEach { it(addr, null) }
            } catch (e: Exception) {
                val message = e.message ?: e.toString()
                startError = message
                updateNotification(String.format(str(R.string.status_failed), message))
                synchronized(listeners) { listeners.toList() }.forEach { it(null, message) }
            }
        }.start()
    }

    // ut-docs#414: called by MainActivity right after it applies a newly-
    // detected till locale, so the persistent notification (and the
    // channel name, which Android only re-reads when the SAME channel ID
    // is recreated with a new name — safe/idempotent, not a new channel)
    // catch up immediately instead of staying in the previous language
    // until this Service is next restarted. str() above already resolves
    // against the just-updated per-app locale by the time this runs.
    fun refreshLocalizedNotification() {
        val channel =
            NotificationChannelCompat.Builder(CHANNEL_ID, NotificationManagerCompat.IMPORTANCE_LOW)
                .setName(str(R.string.notification_channel_name))
                .build()
        NotificationManagerCompat.from(this).createNotificationChannel(channel)
        val text =
            when {
                address != null -> str(R.string.notification_running)
                startError != null -> String.format(str(R.string.status_failed), startError)
                else -> str(R.string.status_starting)
            }
        updateNotification(text)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int = START_STICKY

    override fun onDestroy() {
        super.onDestroy()
        // Mobile.stop() blocks until the server has fully torn down
        // (internal/app.Run's deferred database.Close() included) —
        // internal/server.Start alone allows up to a 5s graceful-shutdown
        // drain. A fully detached, un-joined thread (the first version of
        // this method) would race that whole drain against however long
        // Android gives the process after onDestroy() returns before
        // reclaiming it — routinely discarding a graceful-shutdown window
        // the OS explicitly granted, for an ordinary user-driven stop
        // (independent review, 2026-07-25). Joining with a bounded
        // timeout here — briefly blocking the main thread during
        // shutdown, not during normal operation — captures most of that
        // benefit without risking an indefinite hang if something's
        // genuinely stuck: this is a one-time-per-stop cost, not a
        // per-frame one, so it's an acceptable tradeoff a plain ANR
        // budget (several seconds) easily covers.
        val stopper = Thread { Mobile.stop() }
        stopper.start()
        stopper.join(STOP_JOIN_TIMEOUT_MS)
    }

    private fun buildNotification(text: String) =
        NotificationCompat.Builder(this, CHANNEL_ID)
            .setContentTitle(str(R.string.app_name))
            .setContentText(text)
            .setSmallIcon(R.drawable.ic_stat_till)
            .setOngoing(true)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            .build()

    private fun updateNotification(text: String) {
        NotificationManagerCompat.from(this).notify(NOTIFICATION_ID, buildNotification(text))
    }

    companion object {
        private const val CHANNEL_ID = "till_service"
        private const val NOTIFICATION_ID = 1
        private const val STOP_JOIN_TIMEOUT_MS = 3000L
    }
}
