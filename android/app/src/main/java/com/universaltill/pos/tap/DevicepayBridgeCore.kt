package com.universaltill.pos.tap

import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger

/** Runs a task on the thread the engine lives on (the main looper in the app). */
fun interface MainExecutor {
    fun post(task: () -> Unit)
}

/** One value handed from the main thread to a blocked Go thread; the first set wins. */
class ResultCell<T> {
    private val latch = CountDownLatch(1)
    private var value: T? = null

    @Synchronized
    fun set(v: T) {
        if (value != null) return
        value = v
        latch.countDown()
    }

    fun await(timeoutMs: Long): T? {
        if (!latch.await(maxOf(0L, timeoutMs), TimeUnit.MILLISECONDS)) return null
        return synchronized(this) { value }
    }
}

/**
 * The Kotlin side of internal/devicepay.Bridge (and Resolver), without any Go types so it runs on the JVM:
 * mobile.TapToPayBridge's adapter (TapAndroid.kt) only forwards strings. Every method is called on a Go
 * goroutine, never the main thread, and answers JSON in the shapes internal/devicepay decodes strictly. The
 * web page is not involved anywhere: amount, currency and key come from Go's own request.
 */
class DevicepayBridgeCore(
    private val engine: TapToPayEngine,
    private val main: MainExecutor,
    private val wallClockMs: () -> Long,
    /** Waited past the request's deadline for the verdict (Go cancels at the deadline and answers pending itself). */
    private val backstopMs: Long = 5_000L,
    /** Waited for a person (permission prompt, the education text). */
    private val setupBackstopMs: Long = 600_000L,
) {
    private val seq = AtomicInteger()

    /** Never blocks, never touches the engine's main-thread state. */
    fun capabilities(): String = DevicepayCodec.capabilities(engine.capability(), engine.isPreparing)

    fun collect(requestJson: String?): String {
        val req = DevicepayCodec.parseRequest(requestJson) ?: return DevicepayCodec.declined("bad_request")
        val remaining = req.deadlineUnixMs - wallClockMs()
        // Already out of time: starting would let the tap sheet open for a payment Go has given up on.
        if (remaining <= 0) return DevicepayCodec.pending("timeout")
        val cell = ResultCell<String>()
        val start = StartRequest("pay-${seq.incrementAndGet()}", req.amountMinor, req.currency, req.idempotencyKey)
        main.post {
            engine.collect(start, remaining) { end ->
                cell.set(
                    when (end) {
                        is CollectEnd.Refused -> DevicepayCodec.refused(end.token)
                        is CollectEnd.Verdict -> DevicepayCodec.result(end.result)
                    },
                )
            }
        }
        val answer = cell.await(remaining + backstopMs)
        if (answer != null) return answer
        main.post { engine.cancel(req.idempotencyKey) }
        return DevicepayCodec.pending("timeout")
    }

    fun cancel(idempotencyKey: String?) {
        if (idempotencyKey.isNullOrEmpty()) return
        main.post { engine.cancel(idempotencyKey) }
    }

    fun setup(provider: String?): String {
        if (provider != DevicepayCodec.PROVIDER) return DevicepayCodec.setup("unsupported_provider")
        val cell = ResultCell<String>()
        main.post { engine.setup { cell.set(DevicepayCodec.setup(it)) } }
        return cell.await(setupBackstopMs) ?: DevicepayCodec.setup("timeout")
    }

    fun showEducation(provider: String?): String {
        if (provider != DevicepayCodec.PROVIDER) return DevicepayCodec.setup("unsupported_provider")
        val cell = ResultCell<String>()
        main.post { engine.showEducation { cell.set(DevicepayCodec.setup(if (it) null else "education_declined")) } }
        return cell.await(setupBackstopMs) ?: DevicepayCodec.setup("timeout")
    }

    /** Role-checked in Go (devicepay.ResolveUnknown) before this is reached. */
    fun resolveUnknown(idempotencyKey: String?): String {
        if (idempotencyKey.isNullOrEmpty()) return DevicepayCodec.setup("bad_request")
        val cell = ResultCell<String>()
        main.post { cell.set(DevicepayCodec.setup(engine.resolveUnknown(idempotencyKey))) }
        return cell.await(backstopMs) ?: DevicepayCodec.setup("timeout")
    }
}
