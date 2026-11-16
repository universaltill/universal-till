package com.universaltill.pos.tap

interface Cancelable {
    /** [done] runs once the cancel has completed (or at once when there was nothing to cancel). */
    fun cancel(done: () -> Unit = {})
}

/** Time source + timers. Real: main-thread Handler. Tests: FakeScheduler. */
interface Scheduler {
    fun nowMs(): Long

    fun postDelayed(delayMs: Long, task: () -> Unit): Cancelable
}

sealed class IntentResult {
    data class Created(val id: String) : IntentResult()

    /**
     * The cloud returned an intent that already succeeded (a retry of a paid key): never collected again.
     * The flow reports it as succeeded.
     */
    data class Settled(val id: String, val chargeId: String?, val cardBrand: String?, val last4: String?) : IntentResult()

    /**
     * An intent exists but may have moved money (processing, requires_capture/confirmation, unknown status, an
     * amount/currency mismatch on a moved intent, or it could not be read back). Reported as unknown, never
     * declined, never collected or recreated. [reason] is a result reason token.
     */
    data class Unresolved(val id: String, val reason: String) : IntentResult()

    /**
     * [code] is a start-error token: offline, connect_incomplete, not_ready, or intent_canceled (the cloud's
     * intent for this key was cancelled: the page must use a NEW key for a new attempt).
     */
    data class Failed(val code: String) : IntentResult()
}

sealed class CollectOutcome {
    object Collected : CollectOutcome()

    data class Cancelled(val byCustomer: Boolean) : CollectOutcome()

    /** [reason] is a result reason token (reader_error, insecure_environment, network). */
    data class Failed(val reason: String) : CollectOutcome()
}

sealed class ConfirmOutcome {
    data class Succeeded(val chargeId: String?, val cardBrand: String?, val last4: String?) : ConfirmOutcome()

    data class Declined(val declineCode: String?, val reason: String = "declined_by_issuer") : ConfirmOutcome()

    /** Anything we cannot call a definite decline: the payment may or may not exist. */
    data class Failed(val reason: String) : ConfirmOutcome()
}

/**
 * The card reader, hidden behind plain callbacks. All callbacks are delivered
 * on ONE thread (the adapter posts to main), so the flow has no locks.
 */
interface Reader {
    /** null = connected, else a start-error or reason token. */
    fun ensureConnected(cb: (String?) -> Unit)

    fun createIntent(amountMinor: Long, currency: String, key: String, cb: (IntentResult) -> Unit)

    fun collect(intentId: String, cb: (CollectOutcome) -> Unit): Cancelable

    fun confirm(intentId: String, cb: (ConfirmOutcome) -> Unit): Cancelable

    /** true = the PaymentIntent is confirmed cancelled. */
    fun cancelIntent(intentId: String, cb: (Boolean) -> Unit)

    fun disconnect()
}

class CloudException(val code: String) : Exception(code)

/**
 * The cloud calls, as ut-cloud's payment broker allows them (paybroker ops
 * connection_token, onboarding_status, session_create; same ones the iOS
 * shell uses). Blocking; implementations run off the main thread.
 */
interface CloudApi {
    /** connection_token reply JSON: {"secret": "..."}. */
    fun connectionToken(): String

    /** onboarding_status reply JSON: {"onboarded", "charges_enabled", "location_id"}. */
    fun onboardingStatus(): String

    /** session_create reply JSON: {"client_secret", "amount_minor", "currency", ...}; the cloud creates the PaymentIntent. */
    fun sessionCreate(amountMinor: Long, currency: String, key: String): String
}

/** Lifecycle of a [PendingRecord]. Only after confirm began can the customer have been charged. */
object PendingState {
    const val CREATED = "created"
    const val CONFIRMING = "confirming"
    const val UNKNOWN = "unknown"

    fun unresolved(state: String) = state == CONFIRMING || state == UNKNOWN
}

/** Bound for the persisted pending list: resolved-state records go first; an unresolved one only when nothing else is left. */
object PendingBound {
    fun keep(list: List<PendingRecord>, max: Int): List<PendingRecord> {
        var drop = list.size - max
        if (drop <= 0) return list
        val dropKeys = HashSet<Int>()
        // oldest first: records that cannot mean a charge
        for ((i, r) in list.withIndex()) {
            if (drop == 0) break
            if (!PendingState.unresolved(r.state)) { dropKeys += i; drop-- }
        }
        for (i in list.indices) {
            if (drop == 0) break
            if (i !in dropKeys) { dropKeys += i; drop-- }
        }
        return list.filterIndexed { i, _ -> i !in dropKeys }
    }
}

/** One record per idempotency key. [amountMinor]/[currency] pin what the intent was created for. */
data class PendingRecord(
    val requestId: String,
    val key: String,
    val intentId: String,
    val state: String,
    val amountMinor: Long,
    val currency: String,
)

/** The write-before-tap records: ids only, never secrets. Keyed, so one payment never touches another's. */
interface PendingStore {
    fun put(record: PendingRecord)

    fun remove(key: String)

    fun load(key: String): PendingRecord?

    fun all(): List<PendingRecord>
}

/** A payment that succeeded, kept (bounded) so a repeat of its key is answered, never charged again. */
data class DoneRecord(
    val key: String,
    val requestId: String,
    val intentId: String,
    val amountMinor: Long,
    val currency: String,
    val chargeId: String?,
)

interface DoneStore {
    fun put(r: DoneRecord)

    fun load(key: String): DoneRecord?

    fun byRequest(requestId: String): DoneRecord?
}

interface DeviceProbe {
    fun facts(): DeviceFacts
}

/** Everything that needs an Activity or the OS, kept out of the engine. */
interface UiActions {
    fun showEducation(cb: (Boolean) -> Unit)

    /** Shows OUR rationale first, then the system prompt. */
    fun requestLocationPermission(cb: (Boolean) -> Unit)

    /** Fixed allowlist: location, nfc, app. */
    fun openSettings(target: String): Boolean
}

interface Sink {
    fun progress(requestId: String, state: String)

    fun result(r: TapResult)
}

data class StartRequest(val requestId: String, val amountMinor: Long, val currency: String, val key: String)
