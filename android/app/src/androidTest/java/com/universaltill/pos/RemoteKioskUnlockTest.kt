package com.universaltill.pos

import android.net.Uri
import android.os.SystemClock
import android.webkit.WebView
import androidx.test.core.app.ActivityScenario
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.util.concurrent.atomic.AtomicReference
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

/**
 * ut-docs#3466 (ADR-0142 Consequences, #1508 AC 3): with the WebView on a
 * deliberately broken self-order page, a remote `kiosk_unlock` still
 * releases the pin — because it never goes through the WebView.
 *
 * This drives the native half of the path: it calls the gomobile-bound
 * `mobile.KioskBridge` MainActivity registers, from a background thread,
 * exactly as Go's AndroidNativeWindowController.ReleaseKiosk does after the
 * till's own self-order check. The Go half (directive → handler → window
 * controller → registered bridge) is covered by the Go tests in
 * internal/pages/cloudsync_kiosk_unlock_test.go. An end-to-end run with a
 * real cloud directive is the card's on-device check.
 *
 * Asserts the INTENDED pin state ([MainActivity.kioskPinnedForTest]), not
 * the OS lock-task state: on an unprovisioned device startLockTask() only
 * raises a system confirmation dialog the test cannot answer. On a
 * Device-Owner-provisioned device the OS state follows the same intent.
 *
 * Run on a device or emulator: `./gradlew connectedDebugAndroidTest`.
 * Precondition: the till on that device has finished setup (an un-set-up
 * till redirects every page to the setup wizard, so `/self-order` never
 * loads).
 */
@RunWith(AndroidJUnit4::class)
class RemoteKioskUnlockTest {

    @Test
    fun remoteUnlockReleasesPinFromBrokenSelfOrderPage() {
        ActivityScenario.launch(MainActivity::class.java).use { scenario ->
            val host = awaitTillHost(scenario)

            // A deliberately broken self-order page: a route that does not
            // exist (no layout, no escape link), with its document wiped
            // for good measure. onPageFinished pins on any /self-order/*.
            loadAndWait(scenario, "http://$host/self-order/__broken_3466__")
            scenario.onActivity {
                it.findViewById<WebView>(R.id.webview)
                    .evaluateJavascript("document.documentElement.innerHTML = ''", null)
            }
            assertTrue("pinned on the broken self-order page", pinned(scenario))

            // The Go side's call: off the UI thread, through the bridge.
            val bridge = AtomicReference<mobile.KioskBridge>()
            scenario.onActivity { bridge.set(it.remoteUnlockBridge) }
            val failure = AtomicReference<Throwable?>(null)
            val goThread = Thread {
                try {
                    bridge.get().releaseKiosk()
                } catch (t: Throwable) {
                    failure.set(t)
                }
            }
            goThread.start()
            goThread.join(10_000)
            assertNull("releaseKiosk threw", failure.get())

            assertFalse("pin released", pinned(scenario))
            assertTrue("release window open", windowOpen(scenario))
            awaitPath(scenario, "/login")

            // Inside the window a self-order load does not re-pin.
            loadAndWait(scenario, "http://$host/self-order")
            assertFalse("self-order re-pinned inside the release window", pinned(scenario))

            // A page outside /login, /settings*, /self-order* closes the
            // window; the next self-order load pins again (self-heal).
            loadAndWait(scenario, "http://$host/healthz")
            assertFalse("release window still open after leaving", windowOpen(scenario))
            loadAndWait(scenario, "http://$host/self-order")
            assertTrue("self-order did not re-pin after the window closed", pinned(scenario))
        }
    }

    private fun pinned(s: ActivityScenario<MainActivity>): Boolean {
        val out = AtomicReference(false)
        s.onActivity { out.set(it.kioskPinnedForTest()) }
        return out.get()
    }

    private fun windowOpen(s: ActivityScenario<MainActivity>): Boolean {
        val out = AtomicReference(false)
        s.onActivity { out.set(it.remoteUnlockWindowOpenForTest()) }
        return out.get()
    }

    private fun currentUrl(s: ActivityScenario<MainActivity>): String? {
        val out = AtomicReference<String?>(null)
        s.onActivity { out.set(it.findViewById<WebView>(R.id.webview).url) }
        return out.get()
    }

    /** The till's loopback host:port, once TillService has loaded it. */
    private fun awaitTillHost(s: ActivityScenario<MainActivity>): String {
        val deadline = SystemClock.elapsedRealtime() + 60_000
        while (SystemClock.elapsedRealtime() < deadline) {
            currentUrl(s)?.let { Uri.parse(it) }?.takeIf { it.scheme == "http" }?.let {
                return "${it.host}:${it.port}"
            }
            Thread.sleep(250)
        }
        throw AssertionError("till server never loaded in the WebView")
    }

    private fun loadAndWait(s: ActivityScenario<MainActivity>, url: String) {
        s.onActivity { it.findViewById<WebView>(R.id.webview).loadUrl(url) }
        awaitPath(s, Uri.parse(url).path ?: "/")
        // onPageFinished runs right after the url changes; give it a beat.
        Thread.sleep(1_000)
    }

    private fun awaitPath(s: ActivityScenario<MainActivity>, path: String) {
        val deadline = SystemClock.elapsedRealtime() + 15_000
        while (SystemClock.elapsedRealtime() < deadline) {
            if (currentUrl(s)?.let { Uri.parse(it).path } == path) return
            Thread.sleep(200)
        }
        assertEquals("WebView path", path, currentUrl(s)?.let { Uri.parse(it).path })
    }
}
