package com.universaltill.pos.tap

/**
 * Front-door-agnostic engine (design section 1): the origin-locked WebMessage
 * bridge feeds it raw JSON, a later mobile.TapToPayBridge adapter can call the
 * same entry points. Pure Kotlin, no Android types. Everything runs on the
 * main thread. The result it emits is ADVISORY: the sale is recorded only
 * when the stripe_tap plugin's server-side verification approves.
 */
class TapToPayEngine(
    private val probe: DeviceProbe,
    private val reader: Reader,
    private val scheduler: Scheduler,
    private val store: PendingStore,
    private val ui: UiActions,
    private val emit: (String) -> Unit,
) {
    private companion object {
        const val RESULT_KEEP = 8
    }

    private var foreground = false
    private var flow: TapToPayFlow? = null
    private var preparing = false
    private val results = LinkedHashMap<String, TapResult>()

    /** key -> PaymentIntent id that is still usable (declined, or unknown) in this process. */
    private val liveIntents = HashMap<String, String>()
    private val keyOf = HashMap<String, String>()

    private val sink =
        object : Sink {
            override fun progress(requestId: String, state: String) = emit(TapToPayMessages.progressJson(requestId, state))

            override fun result(r: TapResult) {
                results.remove(r.requestId)
                results[r.requestId] = r
                while (results.size > RESULT_KEEP) results.remove(results.keys.first())
                val key = keyOf[r.requestId]
                if (key != null) {
                    when (r.status) {
                        TapStatus.DECLINED, TapStatus.UNKNOWN -> r.paymentIntentId?.let { liveIntents[key] = it }
                        TapStatus.SUCCEEDED, TapStatus.CANCELLED -> liveIntents.remove(key)
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

    private fun dispatch(r: TapRequest, reply: (String) -> Unit) {
        when (r.op) {
            "capabilities" -> {
                val f = probe.facts()
                reply(TapToPayMessages.replyCapability(r.id, TapToPayCapability.evaluate(f), f.educationAccepted, store.load()))
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
            "result" -> {
                val stored = results[r.requestId]
                val pending = store.load()
                val res =
                    stored ?: pending?.takeIf { it.requestId == r.requestId }?.let {
                        TapResult(it.requestId, TapStatus.UNKNOWN, "app_closed", it.intentId, null, null, null, null, false)
                    }
                reply(TapToPayMessages.replyResult(r.id, res))
            }
            "open_settings" ->
                reply(if (ui.openSettings(r.target!!)) TapToPayMessages.replyOk(r.id) else TapToPayMessages.replyError(r.id, "not_available"))
            else -> reply(TapToPayMessages.replyError(r.id, "bad_request"))
        }
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
                emit(if (err == null) TapToPayMessages.prepareEvent("ready", null) else TapToPayMessages.prepareEvent("failed", err))
            }
        }
        if ("location_permission" in cap.needs) {
            emit(TapToPayMessages.prepareEvent("permission", null))
            ui.requestLocationPermission { connect() }
        } else {
            connect()
        }
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
        val live = liveIntents[key]
        if (live == null && store.load()?.key == key) return refuse("unknown_pending")
        val f = TapToPayFlow(StartRequest(r.id, r.amountMinor!!, r.currency!!, key), reader, scheduler, sink, store, live)
        if (keyOf.size > 64) keyOf.clear()
        keyOf[r.id] = key
        flow = f
        f.start { err ->
            if (err == null) {
                reply(TapToPayMessages.replyOk(r.id))
            } else {
                if (flow === f) flow = null
                refuse(err)
            }
        }
    }
}
