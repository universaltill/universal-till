package com.universaltill.pos.tap

/**
 * Front-door-agnostic engine (design section 1): the origin-locked WebMessage
 * bridge feeds it raw JSON, a later mobile.TapToPayBridge adapter can call the
 * same entry points. Pure Kotlin, no Android types. Everything runs on the
 * main thread. PROCESS-scoped (see TapToPayProcess): it outlives any Activity,
 * and everything that must survive the process lives in [store]/[done].
 * The result it emits is ADVISORY: the sale is recorded only when the
 * stripe_tap plugin's server-side verification approves.
 */
class TapToPayEngine(
    private val probe: DeviceProbe,
    private val reader: Reader,
    private val scheduler: Scheduler,
    private val store: PendingStore,
    private val done: DoneStore,
    private val ui: UiActions,
    private val emit: (String) -> Unit,
) {
    private companion object {
        const val RESULT_KEEP = 8
        val START_ERRORS = setOf("offline", "connect_incomplete", "not_ready", "insecure_environment")
    }

    private class Info(val key: String, val amountMinor: Long, val currency: String)

    private class Live(val intentId: String, val amountMinor: Long, val currency: String)

    private var foreground = false
    private var flow: TapToPayFlow? = null
    private var preparing = false
    private val results = LinkedHashMap<String, TapResult>()

    /** key -> a PaymentIntent still usable in this process (declined, or unknown). */
    private val liveIntents = HashMap<String, Live>()
    private val infoOf = HashMap<String, Info>()

    private val sink =
        object : Sink {
            override fun progress(requestId: String, state: String) = emit(TapToPayMessages.progressJson(requestId, state))

            override fun result(r: TapResult) {
                results.remove(r.requestId)
                results[r.requestId] = r
                while (results.size > RESULT_KEEP) results.remove(results.keys.first())
                val info = infoOf[r.requestId]
                val pi = r.paymentIntentId
                if (info != null && pi != null) {
                    when (r.status) {
                        // a success is persisted FIRST: repeating its key must never create a second charge
                        TapStatus.SUCCEEDED -> {
                            done.put(DoneRecord(info.key, r.requestId, pi, info.amountMinor, info.currency, r.chargeId))
                            liveIntents.remove(info.key)
                        }
                        TapStatus.DECLINED, TapStatus.UNKNOWN -> liveIntents[info.key] = Live(pi, info.amountMinor, info.currency)
                        TapStatus.CANCELLED -> liveIntents.remove(info.key)
                    }
                }
                if (!r.late && flow?.requestId == r.requestId) flow = null
                emit(TapToPayMessages.resultJson(r))
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

    fun handle(raw: String, reply: (String) -> Unit) {
        when (val p = TapToPayMessages.parse(raw)) {
            is ParsedRequest.Bad -> reply(TapToPayMessages.replyError(p.id, p.error))
            is ParsedRequest.Ok -> dispatch(p.req, reply)
        }
    }

    private fun capability() = TapToPayCapability.evaluate(probe.facts())

    private fun unresolved() = store.all().filter { PendingState.unresolved(it.state) }

    private fun dispatch(r: TapRequest, reply: (String) -> Unit) {
        when (r.op) {
            "capabilities" -> {
                val f = probe.facts()
                reply(TapToPayMessages.replyCapability(r.id, TapToPayCapability.evaluate(f), f.educationAccepted, unresolved().firstOrNull()))
            }
            "education" -> {
                reply(TapToPayMessages.replyOk(r.id))
                ui.showEducation { accepted -> emit(TapToPayMessages.educationEvent(accepted)) }
            }
            "prepare" -> prepare(r, reply)
            "start" -> start(r, reply)
            "cancel" -> {
                val f = flow
                when {
                    f == null || f.requestId != r.requestId -> reply(TapToPayMessages.replyError(r.id, "no_such_payment"))
                    f.cancel() -> reply(TapToPayMessages.replyOk(r.id))
                    else -> reply(TapToPayMessages.replyError(r.id, "too_late"))
                }
            }
            "result" -> reply(TapToPayMessages.replyResult(r.id, lookup(r.requestId!!)))
            "resolve_unknown" -> resolveUnknown(r, reply)
            "open_settings" ->
                reply(if (ui.openSettings(r.target!!)) TapToPayMessages.replyOk(r.id) else TapToPayMessages.replyError(r.id, "not_available"))
            else -> reply(TapToPayMessages.replyError(r.id, "bad_request"))
        }
    }

    /** In-memory result, else what survived the process: a success, or an unresolved payment (unknown). */
    private fun lookup(requestId: String): TapResult? {
        results[requestId]?.let { return it }
        done.byRequest(requestId)?.let {
            return TapResult(it.requestId, TapStatus.SUCCEEDED, "ok", it.intentId, it.chargeId, null, null, null, false)
        }
        val p = store.all().firstOrNull { it.requestId == requestId && PendingState.unresolved(it.state) } ?: return null
        return TapResult(p.requestId, TapStatus.UNKNOWN, "app_closed", p.intentId, null, null, null, null, false)
    }

    /** The operator states the earlier payment was NOT charged; only then may another sale start. */
    private fun resolveUnknown(r: TapRequest, reply: (String) -> Unit) {
        val p = unresolved().firstOrNull { it.requestId == r.requestId }
        if (p == null) return reply(TapToPayMessages.replyError(r.id, "no_such_payment"))
        store.remove(p.key)
        liveIntents.remove(p.key)
        reply(TapToPayMessages.replyOk(r.id))
    }

    private fun prepare(r: TapRequest, reply: (String) -> Unit) {
        val cap = capability()
        if (!cap.supported) return reply(TapToPayMessages.replyError(r.id, "not_ready"))
        reply(TapToPayMessages.replyOk(r.id))
        if (preparing) return
        preparing = true
        fun fail(reason: String) {
            preparing = false
            emit(TapToPayMessages.prepareEvent("failed", reason))
        }
        fun connect() {
            val c = capability()
            if (c.needs.isNotEmpty()) return fail(c.needs.first())
            emit(TapToPayMessages.prepareEvent("connecting", null))
            reader.ensureConnected { err ->
                preparing = false
                emit(if (err == null) TapToPayMessages.prepareEvent("ready", null) else TapToPayMessages.prepareEvent("failed", startError(err)))
            }
        }
        if ("location_permission" in cap.needs) {
            emit(TapToPayMessages.prepareEvent("permission", null))
            ui.requestLocationPermission { connect() }
        } else {
            connect()
        }
    }

    /** Only documented start-error tokens leave the engine. */
    private fun startError(err: String): String =
        when {
            err in START_ERRORS -> err
            err == "network" -> "offline"
            else -> "not_ready"
        }

    private fun start(r: TapRequest, reply: (String) -> Unit) {
        fun refuse(e: String) = reply(TapToPayMessages.replyError(r.id, e))
        if (flow?.isLive == true) return refuse("busy")
        if (!foreground) return refuse("not_foreground")
        val cap = capability()
        if (!cap.ready) {
            return refuse(
                when {
                    !cap.supported -> "not_ready"
                    "education_required" in cap.needs -> "needs_education"
                    "offline" in cap.needs -> "offline"
                    else -> "not_ready"
                },
            )
        }
        val key = r.idempotencyKey!!
        val amount = r.amountMinor!!
        val currency = r.currency!!

        // 1. Idempotency after success: a repeat of a paid key is answered with the stored result, never charged again.
        val paid = done.load(key)
        if (paid != null) {
            if (paid.amountMinor != amount || paid.currency != currency) return refuse("key_mismatch")
            val res = TapResult(r.id, TapStatus.SUCCEEDED, "ok", paid.intentId, paid.chargeId, null, null, null, false)
            results.remove(r.id)
            results[r.id] = res
            reply(TapToPayMessages.replyOk(r.id))
            emit(TapToPayMessages.resultJson(res))
            return
        }
        // 2. An earlier outcome nobody resolved blocks every other basket (a possible double charge).
        if (unresolved().any { it.key != key }) return refuse("unknown_pending")
        // 3. Same key again: reuse the live intent only for the SAME amount and currency.
        val live = liveIntents[key]
        if (live != null && (live.amountMinor != amount || live.currency != currency)) return refuse("key_mismatch")
        if (live == null) {
            val mine = store.load(key)
            if (mine != null && PendingState.unresolved(mine.state)) return refuse("unknown_pending")
            // Process died before confirm: collect never authorises, so nothing can have been charged; start afresh.
            if (mine != null) store.remove(key)
        }
        val f = TapToPayFlow(StartRequest(r.id, amount, currency, key), reader, scheduler, sink, store, live?.intentId)
        if (infoOf.size > 64) infoOf.clear()
        infoOf[r.id] = Info(key, amount, currency)
        flow = f
        f.start { err ->
            if (err == null) {
                reply(TapToPayMessages.replyOk(r.id))
            } else {
                if (flow === f) flow = null
                refuse(startError(err))
            }
        }
    }
}
