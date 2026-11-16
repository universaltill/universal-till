package com.universaltill.pos.tap

/** Deterministic clock + scheduler: tests advance time, nothing sleeps. */
class FakeScheduler : Scheduler {
    var now = 0L
    private class Task(val at: Long, val seq: Int, val fn: () -> Unit) {
        var cancelled = false
    }
    private val tasks = mutableListOf<Task>()
    private var seq = 0

    override fun nowMs(): Long = now

    override fun postDelayed(delayMs: Long, task: () -> Unit): Cancelable {
        val t = Task(now + delayMs, seq++, task)
        tasks += t
        return object : Cancelable {
            override fun cancel(done: () -> Unit) {
                t.cancelled = true
                done()
            }
        }
    }

    fun advance(ms: Long) {
        val target = now + ms
        while (true) {
            val next = tasks.filter { !it.cancelled && it.at <= target }.minWithOrNull(compareBy({ it.at }, { it.seq })) ?: break
            tasks.remove(next)
            now = maxOf(now, next.at)
            next.fn()
        }
        now = target
    }
}

class MemoryPendingStore : PendingStore {
    val map = LinkedHashMap<String, PendingRecord>()
    val history = mutableListOf<String>()

    /** The single record, for tests that run one payment. */
    val only: PendingRecord? get() = map.values.singleOrNull()

    override fun put(record: PendingRecord) {
        map[record.key] = record
        history += "put:${record.state}"
    }

    override fun remove(key: String) {
        map.remove(key)
        history += "remove"
    }

    override fun load(key: String): PendingRecord? = map[key]

    override fun all(): List<PendingRecord> = map.values.toList()
}

class MemoryDoneStore : DoneStore {
    val map = LinkedHashMap<String, DoneRecord>()

    override fun put(r: DoneRecord) {
        map[r.key] = r
    }

    override fun load(key: String): DoneRecord? = map[key]

    override fun byRequest(requestId: String): DoneRecord? = map.values.firstOrNull { it.requestId == requestId }
}

class RecordingSink : Sink {
    val results = mutableListOf<TapResult>()
    val progress = mutableListOf<String>()

    override fun progress(requestId: String, state: String) {
        progress += state
    }

    override fun result(r: TapResult) {
        results += r
    }
}

/**
 * Scripted reader. Every async step either completes at once (default) or is
 * parked so a test can complete it later (or never).
 */
class FakeReader(private val sched: FakeScheduler) : Reader {
    var connectError: String? = null
    var connectDelayMs = 0L
    var intentError: String? = null
    var autoCollect: CollectOutcome? = CollectOutcome.Collected
    var autoConfirm: ConfirmOutcome? = ConfirmOutcome.Succeeded(null, null, null)
    var cancelIntentOk = true
    var cancelIntentHang = false
    var collectCancelNever = false
    val order = mutableListOf<String>()

    val createCalls = mutableListOf<List<Any>>()
    val cancelledIntents = mutableListOf<String>()
    val collectIds = mutableListOf<String>()
    var collectCb: ((CollectOutcome) -> Unit)? = null
    var confirmCb: ((ConfirmOutcome) -> Unit)? = null
    var collectCancels = 0
    private var n = 0

    override fun ensureConnected(cb: (String?) -> Unit) {
        if (connectDelayMs == 0L) cb(connectError) else sched.postDelayed(connectDelayMs) { cb(connectError) }
    }

    override fun createIntent(amountMinor: Long, currency: String, key: String, cb: (IntentResult) -> Unit) {
        createCalls += listOf(amountMinor, currency, key)
        val e = intentError
        cb(if (e != null) IntentResult.Failed(e) else IntentResult.Created("pi_${++n}"))
    }

    override fun collect(intentId: String, cb: (CollectOutcome) -> Unit): Cancelable {
        collectIds += intentId
        collectCb = cb
        autoCollect?.let { cb(it) }
        return object : Cancelable {
            override fun cancel(done: () -> Unit) {
                collectCancels++
                order += "collect-cancel"
                if (!collectCancelNever) done()
            }
        }
    }

    override fun confirm(intentId: String, cb: (ConfirmOutcome) -> Unit): Cancelable {
        confirmCb = cb
        autoConfirm?.let { cb(it) }
        return object : Cancelable {
            override fun cancel(done: () -> Unit) = done()
        }
    }

    override fun cancelIntent(intentId: String, cb: (Boolean) -> Unit) {
        cancelledIntents += intentId
        order += "intent-cancel"
        if (!cancelIntentHang) cb(cancelIntentOk)
    }

    override fun disconnect() {}
}

class FakeUi : UiActions {
    var educationAnswer = true
    var permissionAnswer = true
    val opened = mutableListOf<String>()
    var permissionAsked = 0

    override fun showEducation(cb: (Boolean) -> Unit) = cb(educationAnswer)

    override fun requestLocationPermission(cb: (Boolean) -> Unit) {
        permissionAsked++
        cb(permissionAnswer)
    }

    override fun openSettings(target: String): Boolean {
        opened += target
        return true
    }
}

class FakeProbe(var facts: DeviceFacts) : DeviceProbe {
    override fun facts(): DeviceFacts = facts
}

/** A device that passes every check. */
fun readyFacts(sched: Long = 20_000L) =
    DeviceFacts(
        flagOn = true, sdkPresent = true, sdkInt = 34, abiOk = true, hasNfc = true, nfcEnabled = true,
        hasGms = true, hasPlayStore = true, keystoreVersion = 200, securityPatchEpochDay = sched - 30,
        nowEpochDay = sched, devOptionsOn = false, debuggable = false, locationPermission = true,
        locationServicesOn = true, online = true, educationAccepted = true,
    )
