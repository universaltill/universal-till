package com.universaltill.pos.tap

interface Cancelable {
    fun cancel()
}

/** Time source + timers. Real: main-thread Handler. Tests: FakeScheduler. */
interface Scheduler {
    fun nowMs(): Long

    fun postDelayed(delayMs: Long, task: () -> Unit): Cancelable
}

sealed class IntentResult {
    data class Created(val id: String) : IntentResult()

    /** [code] is a start-error token: offline, connect_incomplete, not_ready. */
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

/** The four cloud calls (design section 6). Blocking; implementations run off the main thread. */
interface CloudApi {
    fun connectionToken(): String

    fun location(): String

    fun status(): String

    fun onboardingLink(): String
}

data class PendingRecord(val requestId: String, val key: String, val intentId: String, val state: String)

/** The write-before-tap record: ids only, never secrets. */
interface PendingStore {
    fun put(record: PendingRecord)

    fun clear()

    fun load(): PendingRecord?
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
