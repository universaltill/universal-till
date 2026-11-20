package com.universaltill.pos.reader

import com.universaltill.pos.tap.Cancelable
import com.universaltill.pos.tap.Capability
import com.universaltill.pos.tap.CollectEnd
import com.universaltill.pos.tap.Scheduler
import com.universaltill.pos.tap.TapStatus

/** The link, as the session sees it (OkHttp in the app, a fake in tests). */
interface LinkTransport {
    /** false = it could not be written (closed). */
    fun send(text: String): Boolean

    fun close()
}

/** The Tap to Pay engine, as reader mode uses it (see [EngineReaderPayments]). */
interface ReaderPayments {
    fun capability(): Capability

    /** The one-off preparation (permission, NFC, connect), null = ready else a token. */
    fun setup(cb: (String?) -> Unit)

    fun collect(c: ReaderCollect, remainingMs: Long, end: (CollectEnd) -> Unit)

    fun cancel(key: String): Boolean

    /** The app is on screen (the engine refuses every collect until it is told so). */
    fun onForeground()

    fun onBackground()

    /** Drops the Stripe SDK's reader connection (ADR-0198 6.4). */
    fun disconnect()
}

/** What the main's upgrade answer means for the phone (review A-6). */
object LinkClose {
    /** Only a 401 says "the main removed this phone"; 403, 429, 503 and the rest are "try again later". */
    fun isRevoked(code: Int?) = code == 401

    /** Back-off 1..15 s, or the main's Retry-After (bounded to 2 min). */
    fun retryDelayMs(attempt: Int, retryAfterSec: Int?): Long {
        if (retryAfterSec != null && retryAfterSec > 0) return minOf(120_000L, retryAfterSec * 1_000L)
        return minOf(15_000L, 1_000L shl minOf(attempt, 4))
    }
}

enum class ReaderResultKind { DONE, CANCELLED, CARD_REFUSED, EXPIRED, LINK_LOST }

/** What the phone shows (UX C3-C5). Never an amount outside Pay, never card data, never charged / not charged. */
sealed class ReaderScreen {
    object Connecting : ReaderScreen()
    object Idle : ReaderScreen()
    object Offline : ReaderScreen()
    object NeedsSetup : ReaderScreen()
    object Paused : ReaderScreen()
    object Revoked : ReaderScreen()
    data class Pay(val amountMinor: Long, val currency: String, val timeoutMs: Long) : ReaderScreen()
    data class Result(val kind: ReaderResultKind) : ReaderScreen()
}

interface ReaderUi {
    fun show(s: ReaderScreen)

    /** The vibration that says a payment is requested. */
    fun incoming()

    /** The main removed this phone: the app clears its credentials and returns to enrolment. */
    fun revoked()
}

sealed class TokenResult {
    data class Ok(val secret: String, val locationId: String) : TokenResult() {
        override fun toString() = "Ok(location=$locationId)"
    }

    data class Err(val code: String) : TokenResult()
}

/**
 * The phone's side of the reader link (ADR-0198 §6): single-threaded (everything runs on the main thread; the
 * link posts there). It speaks only after the main's hello, takes ONE collect at a time (a second is answered
 * `busy`, nothing started), reports `reader.status` on every change, relays the SDK's connection-token request over
 * the link, and answers each collect with exactly one `reader.result` on the SAME link the collect arrived on.
 *
 * The result is ADVICE: the main re-derives every fact from the payment service, so anything unsure is sent
 * `pending`, never `declined`. If the link is gone when the verdict arrives it is dropped; the main already holds
 * the payment pending (`link_lost`). A collect whose link stays down for [LINK_LOSS_DROP_MS] is cancelled.
 */
class ReaderSession(
    private val scheduler: Scheduler,
    private val payments: ReaderPayments,
    private val ui: ReaderUi,
    private val appVersion: String,
) {
    companion object {
        const val PING_MS = 5_000L
        const val LINK_LOSS_DROP_MS = 30_000L
        const val RESULT_SHOWN_MS = 3_000L
        const val TOKEN_TIMEOUT_MS = 10_000L
    }

    private class Live(val key: String, val gen: Int)

    private class TokenWait(val cb: (TokenResult) -> Unit, var timer: Cancelable?)

    private var transport: LinkTransport? = null
    private var gen = 0
    private var helloed = false
    private var foreground = false
    private var live: Live? = null
    private var seq = 0
    private var pingTimer: Cancelable? = null
    private var lossTimer: Cancelable? = null
    private var resultTimer: Cancelable? = null
    private val tokens = HashMap<String, TokenWait>()

    var isRevoked = false
        private set

    /** A collect is in flight: the app must not drop the link when it goes to the background (the Stripe sheet pauses it). */
    val hasLive: Boolean get() = live != null

    private fun nextId() = "p-${++seq}"

    /** A new link (the old one, if any, is closed). Nothing is sent until the main speaks first. */
    fun attach(t: LinkTransport): Int {
        val old = transport
        transport = t
        gen++
        helloed = false
        pingTimer?.cancel(); pingTimer = null
        // The 30 s link-loss clock is NOT touched here: a reconnect attempt is not contact (review A-3).
        old?.close()
        failTokens("offline")
        if (live == null) ui.show(ReaderScreen.Connecting)
        return gen
    }

    /** The Activity came to the front: the engine must hear it too, or it refuses every collect (review A-1). */
    fun onForeground() {
        foreground = true
        payments.onForeground()
        if (helloed) reportStatus()
        if (live == null && resultTimer == null) showIdle()
    }

    fun onBackground() {
        foreground = false
        payments.onBackground()
        // Backgrounded and idle: no token-holding Terminal session stays open (ADR-0198 6.4).
        if (live == null) payments.disconnect()
        if (helloed) reportStatus()
        if (live == null && resultTimer == null) ui.show(ReaderScreen.Paused)
    }

    fun onClosed(g: Int, authFailed: Boolean) {
        if (g != gen) return
        helloed = false
        pingTimer?.cancel(); pingTimer = null
        transport = null
        failTokens("offline")
        if (authFailed) {
            isRevoked = true
            lossTimer?.cancel(); lossTimer = null
            live?.let { payments.cancel(it.key) }
            live = null
            payments.disconnect()
            ui.revoked()
            ui.show(ReaderScreen.Revoked)
            return
        }
        val l = live
        if (l != null) {
            // Anchored to the FIRST loss: back-off attempts that fail again do not restart it (review A-3).
            if (lossTimer == null) lossTimer = scheduler.postDelayed(LINK_LOSS_DROP_MS) {
                lossTimer = null
                if (live === l) {
                    payments.cancel(l.key)
                    payments.disconnect()
                    ui.show(ReaderScreen.Result(ReaderResultKind.LINK_LOST))
                }
            }
        } else if (resultTimer == null) {
            ui.show(ReaderScreen.Offline)
        }
    }

    fun onText(g: Int, text: String) {
        if (g != gen || isRevoked) return
        val e = ReaderFrames.parse(text) ?: return
        if (e.type == "hello") return onHello(e)
        if (!helloed) return
        when (e.type) {
            "reader.collect" -> onCollect(e)
            "reader.cancel" -> {
                val key = ReaderFrames.parseCancelKey(e.payload) ?: return
                if (live?.key == key) payments.cancel(key)
            }
            "reader.token", "error" -> onTokenAnswer(e)
        }
    }

    private fun onHello(e: Envelope) {
        if (helloed) return
        val p = e.payload
        if (p.opt("role") != "main" || p.opt("reader_protocol") != ReaderFrames.PROTOCOL) {
            transport?.close()
            return
        }
        helloed = true
        // Real contact with the main again: the loss clock ends.
        lossTimer?.cancel(); lossTimer = null
        send("hello", ReaderFrames.helloPayload(appVersion))
        reportStatus()
        schedulePing()
        if (live == null && resultTimer == null) showIdle()
    }

    private fun schedulePing() {
        pingTimer?.cancel()
        pingTimer = scheduler.postDelayed(PING_MS) {
            if (helloed) {
                send("ping", org.json.JSONObject())
                schedulePing()
            }
        }
    }

    private fun send(type: String, payload: org.json.JSONObject, replyTo: String? = null, id: String = nextId()): Boolean =
        transport?.send(ReaderFrames.encode(id, type, payload, replyTo)) ?: false

    private fun cap(): Capability = payments.capability()

    private fun reportStatus() {
        val c = cap()
        val s =
            when {
                live != null -> ReaderStatus.BUSY
                !foreground -> ReaderStatus.UNAVAILABLE
                !c.ready -> ReaderStatus.NEEDS_SETUP
                else -> ReaderStatus.READY
            }
        send("reader.status", ReaderFrames.statusPayload(s, if (s == ReaderStatus.NEEDS_SETUP) "reader_needs_setup" else null))
    }

    private fun showIdle() {
        ui.show(
            when {
                isRevoked -> ReaderScreen.Revoked
                !foreground -> ReaderScreen.Paused
                !helloed -> ReaderScreen.Offline
                !cap().ready -> ReaderScreen.NeedsSetup
                else -> ReaderScreen.Idle
            },
        )
    }

    /** A fresh enrolment succeeded: the revoked state (and its stale link) of the earlier enrolment is gone (review A-5). */
    fun enrolled() {
        isRevoked = false
        helloed = false
        pingTimer?.cancel(); pingTimer = null
        lossTimer?.cancel(); lossTimer = null
        transport?.close()
        transport = null
        gen++
    }

    /** The customer's or cashier's Cancel on the phone itself: stops the live collect (the verdict then goes out as cancelled). */
    fun cancelLive() {
        live?.let { payments.cancel(it.key) }
    }

    /** The "Set up this phone" button: permission, NFC, connect. Re-reports either way. */
    fun setup() {
        payments.setup {
            if (helloed) reportStatus()
            if (live == null && resultTimer == null) showIdle()
        }
    }

    private fun onCollect(e: Envelope) {
        val c = ReaderFrames.parseCollect(e.payload)
        if (c == null) {
            send("error", ReaderFrames.errorPayload("bad_message", false), replyTo = e.id)
            return
        }
        if (live != null || !foreground || !cap().ready) {
            send("error", ReaderFrames.errorPayload("busy", true), replyTo = e.id)
            return
        }
        val l = Live(c.key, gen)
        live = l
        resultTimer?.cancel(); resultTimer = null
        send("reader.collect", ReaderFrames.ackPayload(), replyTo = e.id)
        reportStatus()
        ui.show(ReaderScreen.Pay(c.amountMinor, c.currency, c.timeoutMs))
        ui.incoming()
        payments.collect(c, c.timeoutMs) { end -> onEnd(l, end) }
    }

    private fun onEnd(l: Live, end: CollectEnd) {
        if (live !== l) return
        live = null
        lossTimer?.cancel(); lossTimer = null
        val (result, kind) = map(end)
        val sameLink = l.gen == gen && helloed
        if (sameLink) {
            send("reader.result", ReaderFrames.resultPayload(l.key, result))
            reportStatus()
        }
        ui.show(ReaderScreen.Result(if (sameLink) kind else ReaderResultKind.LINK_LOST))
        resultTimer?.cancel()
        resultTimer = scheduler.postDelayed(RESULT_SHOWN_MS) {
            resultTimer = null
            if (live == null) showIdle()
        }
    }

    /** Anything unsure is pending; the outcome words never reach the screen. */
    private fun map(end: CollectEnd): Pair<ReaderResult, ReaderResultKind> =
        when (end) {
            is CollectEnd.Refused ->
                when (end.token) {
                    "outcome_unknown", "unknown_pending", "key_mismatch" -> ReaderResult.Pending(end.token) to ReaderResultKind.EXPIRED
                    else -> ReaderResult.Cancelled to ReaderResultKind.EXPIRED
                }
            is CollectEnd.Verdict ->
                when (end.result.status) {
                    TapStatus.SUCCEEDED -> ReaderResult.Approved to ReaderResultKind.DONE
                    TapStatus.DECLINED -> ReaderResult.Declined(end.result.declineCode) to ReaderResultKind.CARD_REFUSED
                    TapStatus.CANCELLED -> ReaderResult.Cancelled to ReaderResultKind.CANCELLED
                    TapStatus.UNKNOWN -> ReaderResult.Pending(end.result.reason) to ReaderResultKind.EXPIRED
                }
        }

    // ---- the SDK's connection token provider asks the main (reader.token) instead of the cloud ----

    /** [cb] runs once, on the main thread: the token, or a code (offline, timeout, not_ready, rate_limited, unavailable...). */
    fun requestToken(cb: (TokenResult) -> Unit) {
        if (!helloed || transport == null) return cb(TokenResult.Err("offline"))
        val id = nextId()
        val w = TokenWait(cb, null)
        tokens[id] = w
        w.timer = scheduler.postDelayed(TOKEN_TIMEOUT_MS) { finishToken(id, TokenResult.Err("timeout")) }
        if (!send("reader.token", ReaderFrames.tokenRequestPayload(), id = id)) finishToken(id, TokenResult.Err("offline"))
    }

    private fun finishToken(id: String, r: TokenResult) {
        val w = tokens.remove(id) ?: return
        w.timer?.cancel()
        w.cb(r)
    }

    private fun failTokens(code: String) {
        for (id in tokens.keys.toList()) finishToken(id, TokenResult.Err(code))
    }

    private fun onTokenAnswer(e: Envelope) {
        val id = e.replyTo ?: return
        if (id !in tokens) return
        if (e.type == "error") return finishToken(id, TokenResult.Err((e.payload.opt("code") as? String)?.takeIf { it.isNotEmpty() } ?: "unavailable"))
        val t = ReaderFrames.parseToken(e.payload) ?: return finishToken(id, TokenResult.Err("bad_reply"))
        finishToken(id, TokenResult.Ok(t.secret, t.locationId))
    }
}
