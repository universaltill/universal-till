package com.universaltill.pos.tap

import android.content.Context
import android.os.Handler
import android.os.Looper
import com.stripe.stripeterminal.Terminal
import com.stripe.stripeterminal.external.callable.Callback
import com.stripe.stripeterminal.external.callable.ConnectionTokenCallback
import com.stripe.stripeterminal.external.callable.ConnectionTokenProvider
import com.stripe.stripeterminal.external.callable.DiscoveryListener
import com.stripe.stripeterminal.external.callable.PaymentIntentCallback
import com.stripe.stripeterminal.external.callable.ReaderCallback
import com.stripe.stripeterminal.external.callable.TapToPayReaderListener
import com.stripe.stripeterminal.external.callable.TerminalListener
import com.stripe.stripeterminal.external.callable.OfflineListener
import com.stripe.stripeterminal.external.models.ConnectionConfiguration
import com.stripe.stripeterminal.external.models.ConnectionTokenException
import com.stripe.stripeterminal.external.models.DiscoveryConfiguration
import com.stripe.stripeterminal.external.models.LocaleConfig
import com.stripe.stripeterminal.external.models.OfflineStatus
import com.stripe.stripeterminal.external.models.PaymentIntent
import com.stripe.stripeterminal.external.models.PaymentIntentStatus
import com.stripe.stripeterminal.external.models.TapUseCase
import com.stripe.stripeterminal.external.models.TerminalException
import com.stripe.stripeterminal.log.LogLevel
import com.universaltill.pos.BuildConfig
import java.util.concurrent.Executors

/**
 * Thin Stripe adapter: no decisions live here (all of them are in
 * TapToPayFlow/TapToPayEngine). Every callback is re-posted to the main
 * thread so the flow stays single-threaded. Not JVM-tested: the device
 * checklist covers it. No card data is read: only ids, status, decline code.
 */
class StripeTerminalReader(private val appContext: Context, private val cloud: CloudApi) : Reader {
    private val main = Handler(Looper.getMainLooper())
    private val scheduler = MainScheduler()
    private val io = Executors.newSingleThreadExecutor()
    private val intents = HashMap<String, PaymentIntent>()
    private val simulated = DiscoveryPolicy.simulated(BuildConfig.DEBUG)
    private val connector = ReaderConnector(cloud) { loc, cb -> sdkConnect(loc, cb) }

    /** All state below is touched on the main thread only: SDK callbacks hop here first. */
    private fun onMain(block: () -> Unit) {
        if (Looper.myLooper() == Looper.getMainLooper()) block() else main.post { block() }
    }

    private val gate = FlightGate()

    private fun terminal(): Terminal {
        if (!Terminal.isInitialized()) {
            Terminal.init(
                appContext,
                if (BuildConfig.DEBUG) LogLevel.WARNING else LogLevel.ERROR,
                object : ConnectionTokenProvider {
                    override fun fetchConnectionToken(callback: ConnectionTokenCallback) {
                        // Fresh per request, never cached, never logged; off the SDK thread. The SDK callback is
                        // ALWAYS answered, whatever goes wrong (an unanswered one hangs the SDK).
                        io.execute {
                            try {
                                callback.onSuccess(parseConnectionToken(cloud.connectionToken()))
                            } catch (e: Throwable) {
                                callback.onFailure(ConnectionTokenException(if (e is CloudException) e.code else "cloud_error"))
                            }
                        }
                    }
                },
                object : TerminalListener {},
                object : OfflineListener {
                    override fun onOfflineStatusChange(offlineStatus: OfflineStatus) {}

                    override fun onPaymentIntentForwarded(paymentIntent: PaymentIntent, e: TerminalException?) {}

                    override fun onForwardingFailure(e: TerminalException) {}
                },
                LocaleConfig.CardLanguagePreferenceIfAvailable,
            )
        }
        return Terminal.getInstance()
    }

    override fun ensureConnected(cb: (String?) -> Unit) {
        onMain {
            val t = try { terminal() } catch (e: Throwable) { return@onMain cb("reader_error") }
            if (t.connectedReader != null) return@onMain cb(null)
            // prepare and start may both ask: ONE connect runs, every caller gets its answer.
            if (gate.join(cb)) io.execute { connector.connect { err -> onMain { gate.finish(err) } } }
        }
    }

    private fun sdkConnect(locationId: String, done: (String?) -> Unit) {
        onMain {
            var discovery: com.stripe.stripeterminal.external.callable.Cancelable? = null
            val attempt =
                ConnectAttempt(
                    scheduler,
                    ConnectAttempt.TIMEOUT_MS,
                    abort = { discovery?.takeIf { !it.isCompleted }?.cancel(object : Callback {
                        override fun onSuccess() {}

                        override fun onFailure(e: TerminalException) {}
                    }) },
                    done,
                )
            val t = try { terminal() } catch (e: Throwable) { return@onMain attempt.end("reader_error") }
            discovery = t.discoverReaders(
                DiscoveryConfiguration.TapToPayDiscoveryConfiguration(isSimulated = simulated),
                object : DiscoveryListener {
                    override fun onUpdateDiscoveredReaders(readers: List<com.stripe.stripeterminal.external.models.Reader>) {
                        val r = readers.firstOrNull()
                        if (r == null || attempt.connecting || attempt.finished) return
                        attempt.connecting = true
                        t.connectReader(
                            r,
                            ConnectionConfiguration.TapToPayConnectionConfiguration(
                                TapUseCase.Pay(locationId),
                                true,
                                object : TapToPayReaderListener {},
                            ),
                            object : ReaderCallback {
                                override fun onSuccess(reader: com.stripe.stripeterminal.external.models.Reader) = onMain { attempt.end(null) }

                                override fun onFailure(e: TerminalException) = onMain { attempt.end(e.errorCode.name) }
                            },
                        )
                    }
                },
                object : Callback {
                    // Discovery ended. With no reader found and no connect started there is nothing left to wait for.
                    override fun onSuccess() = onMain { if (!attempt.connecting) attempt.end("reader_error") }

                    override fun onFailure(e: TerminalException) = onMain { if (!attempt.connecting) attempt.end(e.errorCode.name) }
                },
            )
        }
    }

    /**
     * The cloud creates the PaymentIntent (session_create: direct charge, metadata, no fee are
     * the cloud's); the shell only retrieves it by client secret to collect on it. Same flow as iOS.
     */
    override fun createIntent(amountMinor: Long, currency: String, key: String, cb: (IntentResult) -> Unit) {
        io.execute {
            val session =
                try {
                    parseSession(cloud.sessionCreate(amountMinor, currency, key))
                } catch (e: Throwable) {
                    val code = if (e is CloudException) e.code else "cloud_error"
                    return@execute onMain { cb(IntentResult.Failed(CloudErrors.toStartError(code))) }
                }
            onMain { retrieveOnMain(session, amountMinor, currency, cb) }
        }
    }

    private fun retrieveOnMain(session: CloudSession, amountMinor: Long, currency: String, cb: (IntentResult) -> Unit) {
        // The intent id is the client secret's prefix: known even when the read-back fails.
        val knownId = session.clientSecret.substringBefore("_secret_")
        val t = try { terminal() } catch (e: Throwable) { return cb(IntentResult.Unresolved(knownId, "outcome_unknown")) }
        t.retrievePaymentIntent(
            session.clientSecret,
            object : PaymentIntentCallback {
                override fun onSuccess(paymentIntent: PaymentIntent) = onMain {
                    val id = paymentIntent.id ?: knownId
                    val same =
                        paymentIntent.amount == amountMinor &&
                            paymentIntent.currency.equals(currency, ignoreCase = true) &&
                            (session.amountMinor == null || session.amountMinor == amountMinor) &&
                            (session.currency == null || session.currency.equals(currency, ignoreCase = true))
                    // Same decision table as the iOS coordinator: money that moved on this key is never collected
                    // again, never reported declined, and a mismatch on an untouched intent is a plain refusal.
                    when (paymentIntent.status) {
                        PaymentIntentStatus.REQUIRES_PAYMENT_METHOD ->
                            if (same) {
                                intents[id] = paymentIntent
                                cb(IntentResult.Created(id))
                            } else {
                                cb(IntentResult.Failed("not_ready"))
                            }
                        PaymentIntentStatus.CANCELED -> cb(IntentResult.Failed("intent_canceled"))
                        PaymentIntentStatus.SUCCEEDED ->
                            if (same) cb(IntentResult.Settled(id, paymentIntent.latestChargeId, null, null))
                            else cb(IntentResult.Unresolved(id, "amount_mismatch"))
                        // processing, requires_capture, requires_confirmation, anything unrecognised
                        else -> cb(IntentResult.Unresolved(id, if (same) "outcome_unknown" else "amount_mismatch"))
                    }
                }

                override fun onFailure(e: TerminalException) = onMain { cb(IntentResult.Unresolved(knownId, "outcome_unknown")) }
            },
        )
    }

    private fun wrap(c: com.stripe.stripeterminal.external.callable.Cancelable) =
        object : Cancelable {
            override fun cancel(done: () -> Unit) {
                if (c.isCompleted) return done()
                c.cancel(
                    object : Callback {
                        override fun onSuccess() = onMain { done() }

                        override fun onFailure(e: TerminalException) = onMain { done() }
                    },
                )
            }
        }

    override fun collect(intentId: String, cb: (CollectOutcome) -> Unit): Cancelable {
        val pi = intents[intentId] ?: run {
            onMain { cb(CollectOutcome.Failed("reader_error")) }
            return object : Cancelable { override fun cancel(done: () -> Unit) = done() }
        }
        return wrap(
            terminal().collectPaymentMethod(
                pi,
                object : PaymentIntentCallback {
                    override fun onSuccess(paymentIntent: PaymentIntent) = onMain {
                        intents[intentId] = paymentIntent
                        cb(CollectOutcome.Collected)
                    }

                    override fun onFailure(e: TerminalException) {
                        val name = e.errorCode.name
                        onMain {
                            e.paymentIntent?.let { intents[intentId] = it }
                            cb(
                                when (StripeErrorMapper.cancelKind(name)) {
                                    CancelKind.CUSTOMER -> CollectOutcome.Cancelled(true)
                                    CancelKind.OPERATOR -> CollectOutcome.Cancelled(false)
                                    CancelKind.NONE -> CollectOutcome.Failed(StripeErrorMapper.reason(name))
                                },
                            )
                        }
                    }
                },
            ),
        )
    }

    override fun confirm(intentId: String, cb: (ConfirmOutcome) -> Unit): Cancelable {
        val pi = intents[intentId] ?: run {
            onMain { cb(ConfirmOutcome.Failed("reader_error")) }
            return object : Cancelable { override fun cancel(done: () -> Unit) = done() }
        }
        return wrap(
            terminal().confirmPaymentIntent(
                pi,
                object : PaymentIntentCallback {
                    override fun onSuccess(paymentIntent: PaymentIntent) {
                        onMain {
                            intents[intentId] = paymentIntent
                            cb(
                                if (paymentIntent.status == PaymentIntentStatus.SUCCEEDED) {
                                    // Only the charge id; no brand/last4 are read (no card data in this layer).
                                    ConfirmOutcome.Succeeded(paymentIntent.latestChargeId, null, null)
                                } else {
                                    ConfirmOutcome.Failed("reader_error")
                                },
                            )
                        }
                    }

                    override fun onFailure(e: TerminalException) {
                        val decline = e.apiError?.declineCode
                        val status = e.paymentIntent?.status?.name
                        onMain {
                            e.paymentIntent?.let { intents[intentId] = it }
                            cb(
                                if (StripeErrorMapper.isDecline(status, decline)) {
                                    ConfirmOutcome.Declined(decline)
                                } else {
                                    ConfirmOutcome.Failed(StripeErrorMapper.reason(e.errorCode.name))
                                },
                            )
                        }
                    }
                },
            ),
        )
    }

    override fun cancelIntent(intentId: String, cb: (Boolean) -> Unit) {
        onMain { cancelIntentOnMain(intentId, cb) }
    }

    private fun cancelIntentOnMain(intentId: String, cb: (Boolean) -> Unit) {
        val pi = intents[intentId] ?: return cb(false)
        terminal().cancelPaymentIntent(
            pi,
            object : PaymentIntentCallback {
                override fun onSuccess(paymentIntent: PaymentIntent) = onMain {
                    intents.remove(intentId)
                    cb(true)
                }

                override fun onFailure(e: TerminalException) = onMain { cb(false) }
            },
        )
    }

    override fun disconnect() {
        if (Terminal.isInitialized() && Terminal.getInstance().connectedReader != null) {
            Terminal.getInstance().disconnectReader(
                object : Callback {
                    override fun onSuccess() {}

                    override fun onFailure(e: TerminalException) {}
                },
            )
        }
    }
}
