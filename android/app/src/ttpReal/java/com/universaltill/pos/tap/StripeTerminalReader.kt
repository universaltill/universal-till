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
import com.stripe.stripeterminal.external.models.CaptureMethod
import com.stripe.stripeterminal.external.models.ConnectionConfiguration
import com.stripe.stripeterminal.external.models.ConnectionTokenException
import com.stripe.stripeterminal.external.models.CreateConfiguration
import com.stripe.stripeterminal.external.models.DiscoveryConfiguration
import com.stripe.stripeterminal.external.models.LocaleConfig
import com.stripe.stripeterminal.external.models.OfflineBehavior
import com.stripe.stripeterminal.external.models.OfflineStatus
import com.stripe.stripeterminal.external.models.PaymentIntent
import com.stripe.stripeterminal.external.models.PaymentIntentParameters
import com.stripe.stripeterminal.external.models.PaymentIntentStatus
import com.stripe.stripeterminal.external.models.PaymentMethodType
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
    private val io = Executors.newSingleThreadExecutor()
    private val intents = HashMap<String, PaymentIntent>()
    private val simulated = DiscoveryPolicy.simulated(BuildConfig.DEBUG)
    private val connector = ReaderConnector(cloud) { loc, cb -> sdkConnect(loc, cb) }

    private fun <T> onMain(block: () -> T) {
        main.post { block() }
    }

    private fun terminal(): Terminal {
        if (!Terminal.isInitialized()) {
            Terminal.init(
                appContext,
                if (BuildConfig.DEBUG) LogLevel.WARNING else LogLevel.ERROR,
                object : ConnectionTokenProvider {
                    override fun fetchConnectionToken(callback: ConnectionTokenCallback) {
                        // Fresh per request, never cached, never logged; runs off the SDK thread.
                        io.execute {
                            try {
                                callback.onSuccess(parseConnectionToken(cloud.connectionToken()))
                            } catch (e: CloudException) {
                                callback.onFailure(ConnectionTokenException(e.code))
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
        val t = try { terminal() } catch (e: Exception) { return cb("reader_error") }
        if (t.connectedReader != null) return cb(null)
        io.execute { connector.connect { err -> onMain { cb(err) } } }
    }

    private var connecting = false

    private fun sdkConnect(locationId: String, done: (String?) -> Unit) {
        onMain {
            val t = terminal()
            connecting = false
            t.discoverReaders(
                DiscoveryConfiguration.TapToPayDiscoveryConfiguration(isSimulated = simulated),
                object : DiscoveryListener {
                    override fun onUpdateDiscoveredReaders(readers: List<com.stripe.stripeterminal.external.models.Reader>) {
                    val r = readers.firstOrNull()
                    if (r != null && !connecting) {
                        connecting = true
                        t.connectReader(
                            r,
                            ConnectionConfiguration.TapToPayConnectionConfiguration(
                                TapUseCase.Pay(locationId),
                                true,
                                object : TapToPayReaderListener {},
                            ),
                            object : ReaderCallback {
                                override fun onSuccess(reader: com.stripe.stripeterminal.external.models.Reader) = done(null)

                                override fun onFailure(e: TerminalException) = done(e.errorCode.name)
                            },
                        )
                    }
                    }
                },
                object : Callback {
                    override fun onSuccess() {}

                    override fun onFailure(e: TerminalException) {
                        if (!connecting) done(e.errorCode.name)
                    }
                },
            )
        }
    }

    override fun createIntent(amountMinor: Long, currency: String, key: String, cb: (IntentResult) -> Unit) {
        val params =
            PaymentIntentParameters.Builder(amountMinor, currency, CaptureMethod.Automatic, listOf(PaymentMethodType.CARD_PRESENT))
                .setMetadata(mapOf("ut_key" to key)) // no PII, no application fee
                .build()
        terminal().createPaymentIntent(
            params,
            object : PaymentIntentCallback {
                override fun onSuccess(paymentIntent: PaymentIntent) {
                    val id = paymentIntent.id
                    if (id == null) {
                        onMain { cb(IntentResult.Failed("not_ready")) }
                        return
                    }
                    intents[id] = paymentIntent
                    onMain { cb(IntentResult.Created(id)) }
                }

                override fun onFailure(e: TerminalException) = onMain { cb(IntentResult.Failed("not_ready")) }
            },
            CreateConfiguration(OfflineBehavior.REQUIRE_ONLINE),
        )
    }

    private fun wrap(c: com.stripe.stripeterminal.external.callable.Cancelable) =
        object : Cancelable {
            override fun cancel() {
                if (!c.isCompleted) {
                    c.cancel(
                        object : Callback {
                            override fun onSuccess() {}

                            override fun onFailure(e: TerminalException) {}
                        },
                    )
                }
            }
        }

    override fun collect(intentId: String, cb: (CollectOutcome) -> Unit): Cancelable {
        val pi = intents[intentId] ?: run {
            onMain { cb(CollectOutcome.Failed("reader_error")) }
            return object : Cancelable { override fun cancel() {} }
        }
        return wrap(
            terminal().collectPaymentMethod(
                pi,
                object : PaymentIntentCallback {
                    override fun onSuccess(paymentIntent: PaymentIntent) {
                        intents[intentId] = paymentIntent
                        onMain { cb(CollectOutcome.Collected) }
                    }

                    override fun onFailure(e: TerminalException) {
                        e.paymentIntent?.let { intents[intentId] = it }
                        val name = e.errorCode.name
                        onMain {
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
            return object : Cancelable { override fun cancel() {} }
        }
        return wrap(
            terminal().confirmPaymentIntent(
                pi,
                object : PaymentIntentCallback {
                    override fun onSuccess(paymentIntent: PaymentIntent) {
                        intents[intentId] = paymentIntent
                        onMain {
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
                        e.paymentIntent?.let { intents[intentId] = it }
                        val decline = e.apiError?.declineCode
                        val status = e.paymentIntent?.status?.name
                        onMain {
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
        val pi = intents[intentId] ?: return onMain { cb(false) }
        terminal().cancelPaymentIntent(
            pi,
            object : PaymentIntentCallback {
                override fun onSuccess(paymentIntent: PaymentIntent) {
                    intents.remove(intentId)
                    onMain { cb(true) }
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
