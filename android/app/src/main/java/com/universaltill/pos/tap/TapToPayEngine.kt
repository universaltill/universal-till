package com.universaltill.pos.tap

/** How one [TapToPayEngine.collect] ended. The callback runs exactly once. */
sealed class CollectEnd {
    /** Nothing was charged and nothing is in flight: [token] says why (start-error vocabulary). */
    data class Refused(val token: String) : CollectEnd()

    /** The payment's verdict (it may be `unknown`: a late answer is persisted, never reported twice). */
    data class Verdict(val result: TapResult) : CollectEnd()
}

/** The two outcomes a manager can give an unresolved payment; mirrors devicepay.OutcomeNotCharged / OutcomeCharged. */
object Outcome {
    const val NOT_CHARGED = "not_charged"
    const val CHARGED = "charged"
}

/**
 * The payment engine behind the devicepay bridge (design section 1). Pure
 * Kotlin, no Android types. Everything runs on the main thread (the bridge
 * hops onto it). PROCESS-scoped (see TapToPayProcess): it outlives any
 * Activity, and everything that must survive the process lives in
 * [store]/[done].
 *
 * Amount, currency and idempotency key come from the till's own sale
 * (plugin -> internal/devicepay -> bridge); there is no page-facing entry.
 * The result it reports is ADVISORY: the sale is recorded only when the
 * payment plugin's server-side verification approves.
 */
class TapToPayEngine(
    private val probe: DeviceProbe,
    private val reader: Reader,
    private val scheduler: Scheduler,
    private val store: PendingStore,
    private val done: DoneStore,
    private val ui: UiActions,
) {
    companion object {
        val START_ERRORS = setOf("offline", "connect_incomplete", "not_ready", "insecure_environment", "intent_canceled")

        /** Go cancels at the request's deadline; the verdict is due this long before it. */
        const val DEADLINE_MARGIN_MS = 3_000L

        /** Declined intents kept for a same-key retry (#3048 R3). */
        const val MAX_LIVE_INTENTS = 64
    }

    private class Live(val intentId: String, val amountMinor: Long, val currency: String)

    private var foreground = false
    private var flow: TapToPayFlow? = null

    /** Read from Go's threads (capabilities); written on main. */
    @Volatile
    private var preparing = false

    /** key -> a PaymentIntent still usable in this process (declined, or unknown). */
    private val liveIntents = LinkedHashMap<String, Live>()

    /** Test hook: how many intents are held for a same-key retry. */
    internal fun liveIntentCount() = liveIntents.size

    /**
     * Bounded (#3048 R3 recheck): the oldest DECLINED intent goes first; one whose record is unresolved (unknown,
     * confirming) is never evicted, so a possibly charged payment is never forgotten. Dropping a declined entry only
     * means its retry creates a fresh intent; the paid write does not depend on this map (it lives in the sink).
     */
    private fun remember(key: String, live: Live) {
        liveIntents.remove(key)
        liveIntents[key] = live
        val it = liveIntents.entries.iterator()
        while (liveIntents.size > MAX_LIVE_INTENTS && it.hasNext()) {
            val k = it.next().key
            if (k != key && store.load(k)?.let { r -> PendingState.unresolved(r.state) } != true) it.remove()
        }
    }

    /**
     * One sink per payment: it knows ITS key, amount and currency, so a late success is persisted no matter
     * how many payments ran since (a shared requestId -> info map had to be bounded, and dropping an entry
     * lost a paid write).
     */
    private inner class PaymentSink(
        private val key: String,
        private val amountMinor: Long,
        private val currency: String,
        private val end: (CollectEnd) -> Unit,
    ) : Sink {
        var deadlineTimer: Cancelable? = null
        private var delivered = false

        fun deliver(e: CollectEnd) {
            if (delivered) return
            delivered = true
            deadlineTimer?.cancel()
            end(e)
        }

        override fun progress(requestId: String, state: String) {
            // Nothing is listening: the verdict is the only thing Go reads. A native status chip is #6457.
        }

        override fun result(r: TapResult) {
            val pi = r.paymentIntentId
            if (pi != null) {
                when (r.status) {
                    // a success is persisted FIRST: repeating its key must never create a second charge
                    TapStatus.SUCCEEDED -> {
                        done.put(DoneRecord(key, r.requestId, pi, amountMinor, currency, r.chargeId))
                        liveIntents.remove(key)
                    }
                    TapStatus.DECLINED, TapStatus.UNKNOWN -> remember(key, Live(pi, amountMinor, currency))
                    TapStatus.CANCELLED -> liveIntents.remove(key)
                }
            }
            if (!r.late && flow?.requestId == r.requestId) flow = null
            deadlineTimer?.cancel()
            deliver(CollectEnd.Verdict(r))
        }
    }

    fun onForeground() {
        foreground = true
    }

    /** Backgrounding never cancels an in-flight payment (the Stripe screen pauses our Activity). */
    fun onBackground() {
        foreground = false
    }

    fun onDestroy(isFinishing: Boolean) {
        if (isFinishing) flow?.destroy()
    }

    /** Safe from any thread: it only reads the OS. */
    fun capability(): Capability = TapToPayCapability.evaluate(probe.facts())

    val isPreparing: Boolean get() = preparing

    private fun unresolved() = store.all().filter { PendingState.unresolved(it.state) }

    /** Stops the in-flight payment with this idempotency key, only while it is still collecting. */
    fun cancel(key: String): Boolean {
        val f = flow ?: return false
        return f.key == key && f.cancel()
    }

    /**
     * The manager states what the provider shows for the earlier payment [key] (ut-docs#6533, Go's Resolve): [Outcome.NOT_CHARGED]
     * frees the till for another sale; [Outcome.CHARGED] keeps the key paid (a repeat of it is answered, never charged again)
     * and frees the till too, Go records the tender. null = resolved, else a token (bad_request, no_such_payment, busy).
     * WHO may say it, and that the provider was asked first, is decided in Go (devicepay.Resolve), before this is reached.
     */
    fun resolve(key: String, outcome: String): String? {
        if (outcome != Outcome.NOT_CHARGED && outcome != Outcome.CHARGED) return "bad_request"
        val p = unresolved().firstOrNull { it.key == key } ?: return "no_such_payment"
        if (flow?.isLive == true && flow?.key == key) return "busy"
        // paid first, then the record goes: a crash in between leaves the key blocked, never free-and-unpaid
        if (outcome == Outcome.CHARGED) done.put(DoneRecord(p.key, p.requestId, p.intentId, p.amountMinor, p.currency, null))
        store.remove(p.key)
        liveIntents.remove(p.key)
        return null
    }

    /** Keys of attempts whose outcome nobody resolved (reader mode releases them, see EnginePayments). */
    fun unresolvedKeys(): List<String> = unresolved().map { it.key }

    /** Drops the SDK's reader connection (ADR-0198 6.4: revoke, link loss, background). The next collect reconnects. */
    fun disconnectReader() = reader.disconnect()

    fun showEducation(cb: (Boolean) -> Unit) = ui.showEducation(cb)

    /**
     * The manager's one-off preparation: location permission, the education text, then connect the reader.
     * [cb] gets null when the device is ready, else a reason token. Settings screens are opened by the app
     * itself from a fixed allowlist, never at a caller's request.
     */
    fun setup(cb: (String?) -> Unit) {
        val cap = capability()
        if (!cap.supported) return cb(cap.reason ?: "not_ready")
        if (preparing || flow?.isLive == true) return cb("busy")
        preparing = true

        fun finish(reason: String?) {
            preparing = false
            cb(reason)
        }

        // A step the operator just completed is trusted without waiting for the OS to catch up.
        fun connect(trusted: Set<String>) {
            val needs = capability().needs.filter { it !in trusted }
            when {
                "location_permission" in needs -> finish("location_denied")
                "nfc_off" in needs -> {
                    ui.openSettings("nfc")
                    finish("nfc_off")
                }
                "location_off" in needs -> {
                    ui.openSettings("location")
                    finish("location_off")
                }
                "offline" in needs -> finish("offline")
                else -> reader.ensureConnected { err -> finish(err?.let { startError(it) }) }
            }
        }

        fun education(trusted: Set<String>) {
            if ("education_required" !in cap.needs) return connect(trusted)
            ui.showEducation { accepted ->
                if (accepted) connect(trusted + "education_required") else finish("education_declined")
            }
        }

        if ("location_permission" in cap.needs) {
            ui.requestLocationPermission { granted ->
                if (granted) education(setOf("location_permission")) else finish("location_denied")
            }
        } else {
            education(emptySet())
        }
    }

    /** Only documented start-error tokens leave the engine. */
    private fun startError(err: String): String =
        when {
            err in START_ERRORS -> err
            err == "network" -> "offline"
            else -> "not_ready"
        }

    /**
     * Takes one payment. [remainingMs] is how long the caller will wait (null = no deadline known): the
     * collect is cancelled [DEADLINE_MARGIN_MS] before it, so a verdict reaches Go before Go gives up.
     * [end] runs exactly once.
     */
    fun collect(r: StartRequest, remainingMs: Long?, end: (CollectEnd) -> Unit) {
        val sink = PaymentSink(r.key, r.amountMinor, r.currency, end)
        fun refuse(e: String) = sink.deliver(CollectEnd.Refused(e))
        val key = r.key
        val amount = r.amountMinor
        val currency = r.currency

        // 1. Idempotency after success FIRST (before busy/foreground/capability): a repeat of a paid key is
        // answered with the stored approval, never charged again and never declined for an environment reason.
        val paid = done.load(key)
        if (paid != null) {
            if (paid.amountMinor != amount || paid.currency != currency) return refuse("key_mismatch")
            return sink.deliver(CollectEnd.Verdict(TapResult(r.requestId, TapStatus.SUCCEEDED, "ok", paid.intentId, paid.chargeId, null, null, null, false)))
        }
        // 1b. The same key is still in flight: its first tap may be charged any moment, so this is pending (#6439).
        if (flow?.isLive == true && flow?.key == key) return refuse("outcome_unknown")
        // From here a refusal for THIS key must not read as "not charged" when an earlier attempt may have been.
        val maybeCharged = store.load(key)?.let { PendingState.unresolved(it.state) } == true
        fun gate(token: String) = refuse(if (maybeCharged) "outcome_unknown" else token)
        if (flow?.isLive == true) return gate("busy")
        if (!foreground) return gate("not_foreground")
        val cap = capability()
        if (!cap.ready) {
            return gate(
                when {
                    !cap.supported -> "not_ready"
                    "education_required" in cap.needs -> "needs_education"
                    "offline" in cap.needs -> "offline"
                    else -> "not_ready"
                },
            )
        }
        // 2. An earlier outcome nobody resolved blocks every other basket (a possible double charge).
        if (unresolved().any { it.key != key }) return refuse("unknown_pending")
        // 3. Same key again: reuse the live intent only for the SAME amount and currency.
        val live = liveIntents[key]
        if (live != null && (live.amountMinor != amount || live.currency != currency)) return refuse("key_mismatch")
        if (live == null) {
            val mine = store.load(key)
            // This very payment may have been charged: pending (the plugin settles it), never a plain refusal.
            if (mine != null && PendingState.unresolved(mine.state)) return refuse("outcome_unknown")
            // Process died before confirm: collect never authorises, so nothing can have been charged; start afresh.
            if (mine != null) store.remove(key)
        }
        val f = TapToPayFlow(r, reader, scheduler, sink, store, live?.intentId)
        flow = f
        // Go's deadline binds from the first moment: a slow connect must not open the tap sheet for a payment
        // Go already gave up on (cancel(key) aborts a STARTING flow too).
        if (remainingMs != null) {
            sink.deadlineTimer = scheduler.postDelayed(maxOf(0L, remainingMs - DEADLINE_MARGIN_MS)) { cancel(key) }
        }
        f.start { err ->
            if (err != null) {
                if (flow === f) flow = null
                refuse(startError(err))
            }
        }
    }
}
