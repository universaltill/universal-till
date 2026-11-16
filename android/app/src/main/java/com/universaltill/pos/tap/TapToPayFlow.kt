package com.universaltill.pos.tap

/**
 * One payment (design section 7). Single-threaded: every Reader/Scheduler
 * callback arrives on the same thread. Rules that must never regress:
 *  - collect never authorises, so cancelling it on a timer is safe;
 *  - from the moment confirm starts, NOTHING is reported declined/cancelled
 *    on a timer, only `unknown`;
 *  - a late Stripe answer after `unknown` is delivered (late=true), never dropped;
 *  - an intent whose cancel is not confirmed is `unknown`, not `cancelled`.
 */
class TapToPayFlow(
    private val req: StartRequest,
    private val reader: Reader,
    private val scheduler: Scheduler,
    private val sink: Sink,
    private val store: PendingStore,
    reuseIntentId: String?,
) {
    companion object {
        const val COLLECT_WINDOW_MS = 18_000L
        const val HARD_DEADLINE_MS = 24_000L
        const val MIN_CONFIRM_BUDGET_MS = 4_000L
    }

    private enum class S { STARTING, COLLECTING, CANCELLING, CONFIRMING, ENDED }

    private var s = S.STARTING
    private val t0 = scheduler.nowMs()
    private var intentId: String? = reuseIntentId
    private var collectHandle: Cancelable? = null
    private var onAccepted: ((String?) -> Unit)? = null
    private var unknownDelivered = false
    private var collectTimer: Cancelable? = null
    private val timers = mutableListOf<Cancelable>()

    /** [accepted] gets null once the PaymentIntent exists and the tap is armed, or a start-error token (nothing exists). */
    fun start(accepted: (String?) -> Unit) {
        onAccepted = accepted
        timers += scheduler.postDelayed(HARD_DEADLINE_MS) { onHardDeadline() }
        sink.progress(req.requestId, "connecting")
        reader.ensureConnected { err ->
            if (s != S.STARTING) return@ensureConnected
            if (err != null) return@ensureConnected refuse(err)
            val reuse = intentId
            if (reuse != null) {
                intentReady(reuse)
            } else {
                reader.createIntent(req.amountMinor, req.currency, req.key) { r ->
                    when (r) {
                        is IntentResult.Failed -> if (s == S.STARTING) refuse(r.code)
                        is IntentResult.Created ->
                            if (s == S.STARTING) intentReady(r.id) else reader.cancelIntent(r.id) {} // arrived after we gave up
                    }
                }
            }
        }
    }

    /** Operator cancel: only while collecting. */
    fun cancel(): Boolean {
        if (s != S.COLLECTING) return false
        abortCollect("operator_cancelled")
        return true
    }

    /** Activity finishing for good. */
    fun destroy() {
        when (s) {
            S.COLLECTING -> abortCollect("app_closed")
            S.CONFIRMING -> finish(TapStatus.UNKNOWN, "app_closed")
            S.STARTING -> refuse("not_ready")
            else -> {}
        }
    }

    val isLive: Boolean get() = s != S.ENDED

    val requestId: String get() = req.requestId

    private fun refuse(code: String) {
        s = S.ENDED
        cancelTimers()
        answer(code)
    }

    private fun answer(v: String?) {
        val cb = onAccepted ?: return
        onAccepted = null
        cb(v)
    }

    private fun cancelTimers() {
        timers.forEach { it.cancel() }
        timers.clear()
    }

    private fun intentReady(id: String) {
        intentId = id
        store.put(PendingRecord(req.requestId, req.key, id, "created"))
        answer(null)
        s = S.COLLECTING
        sink.progress(req.requestId, "waiting_for_tap")
        collectTimer = scheduler.postDelayed(COLLECT_WINDOW_MS) { abortCollect("timeout") }
        timers += collectTimer!!
        collectHandle = reader.collect(id) { onCollect(it) }
    }

    private fun onCollect(o: CollectOutcome) {
        if (s != S.COLLECTING) return
        when (o) {
            is CollectOutcome.Collected -> {
                val remaining = t0 + HARD_DEADLINE_MS - scheduler.nowMs()
                if (remaining < MIN_CONFIRM_BUDGET_MS) abortCollect("timeout") else confirm()
            }
            is CollectOutcome.Cancelled -> abortCollect(if (o.byCustomer) "customer_cancelled" else "operator_cancelled")
            is CollectOutcome.Failed -> abortCollect(o.reason)
        }
    }

    private fun abortCollect(reason: String) {
        if (s != S.COLLECTING) return
        s = S.CANCELLING
        cancelTimers()
        collectHandle?.cancel()
        val id = intentId!!
        reader.cancelIntent(id) { ok ->
            if (ok) finish(TapStatus.CANCELLED, reason) else finish(TapStatus.UNKNOWN, reason)
        }
    }

    private fun confirm() {
        s = S.CONFIRMING
        // the collect window no longer applies; the hard deadline timer stays
        collectTimer?.cancel()
        store.put(PendingRecord(req.requestId, req.key, intentId!!, "confirming"))
        sink.progress(req.requestId, "processing")
        reader.confirm(intentId!!) { onConfirm(it) }
    }

    private fun onConfirm(o: ConfirmOutcome) {
        val late = s == S.ENDED && unknownDelivered
        if (s != S.CONFIRMING && !late) return
        when (o) {
            is ConfirmOutcome.Succeeded -> finish(TapStatus.SUCCEEDED, "ok", o.chargeId, null, o.cardBrand, o.last4)
            is ConfirmOutcome.Declined -> finish(TapStatus.DECLINED, o.reason, null, o.declineCode)
            is ConfirmOutcome.Failed -> if (!late) finish(TapStatus.UNKNOWN, o.reason)
        }
    }

    private fun onHardDeadline() {
        when (s) {
            S.STARTING -> refuse("offline")
            S.COLLECTING -> abortCollect("timeout")
            S.CONFIRMING -> finish(TapStatus.UNKNOWN, "timeout")
            else -> {}
        }
    }

    private fun finish(
        status: TapStatus,
        reason: String,
        chargeId: String? = null,
        declineCode: String? = null,
        brand: String? = null,
        last4: String? = null,
    ) {
        val late = s == S.ENDED && unknownDelivered
        if (s == S.ENDED && !late) return
        s = S.ENDED
        cancelTimers()
        unknownDelivered = status == TapStatus.UNKNOWN
        // unknown keeps the record: the PaymentIntent id is what reconciliation needs
        if (status != TapStatus.UNKNOWN) store.clear()
        sink.result(TapResult(req.requestId, status, reason, intentId, chargeId, declineCode, brand, last4, late))
    }
}
