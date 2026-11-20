package com.universaltill.pos.reader

import android.os.Handler
import android.os.Looper
import com.universaltill.pos.tap.Cancelable
import java.security.SecureRandom
import java.util.concurrent.TimeUnit
import javax.net.ssl.HostnameVerifier
import javax.net.ssl.SSLContext
import javax.net.ssl.X509TrustManager
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okhttp3.MediaType.Companion.toMediaType

private fun client(tm: X509TrustManager, timeoutSec: Long): OkHttpClient {
    val ctx = SSLContext.getInstance("TLS").apply { init(null, arrayOf(tm), SecureRandom()) }
    // The main is dialled by IP on the LAN: its identity is the pinned key, not a name.
    val anyHost = HostnameVerifier { _, _ -> true }
    return OkHttpClient.Builder()
        .sslSocketFactory(ctx.socketFactory, tm)
        .hostnameVerifier(anyHost)
        .connectTimeout(timeoutSec, TimeUnit.SECONDS)
        .readTimeout(timeoutSec, TimeUnit.SECONDS)
        .retryOnConnectionFailure(false)
        .followRedirects(false)
        .build()
}

/** [PairHttp] over OkHttp: first contact learns the key, every later call enforces it. */
class OkPairHttp : PairHttp {
    private fun run(req: Request, expectPin: String?): PairHttp.Answer {
        val learn = if (expectPin == null) SpkiPin.learning() else null
        val tm = learn ?: SpkiPin.enforcing(expectPin!!)
        return try {
            client(tm, 15).newCall(req).execute().use { r -> PairHttp.Answer(r.code, r.body?.source()?.let { src -> src.request(PairHttp.MAX_BODY.toLong()); src.buffer.readUtf8(minOf(src.buffer.size, PairHttp.MAX_BODY.toLong())) }.orEmpty(), learn?.pin ?: expectPin, r.header("Retry-After")?.trim()?.toIntOrNull() ?: 0) }
        } catch (e: javax.net.ssl.SSLException) {
            PairHttp.Answer(if (expectPin != null) -2 else -1, "", null)
        } catch (_: Exception) {
            PairHttp.Answer(-1, "", null)
        }
    }

    override fun post(url: String, body: String, expectPin: String?) =
        run(Request.Builder().url(url).post(body.toRequestBody("application/json".toMediaType())).build(), expectPin)

    override fun get(url: String, headers: Map<String, String>, expectPin: String?) =
        run(Request.Builder().url(url).apply { headers.forEach { (k, v) -> header(k, v) } }.build(), expectPin)
}

/**
 * The WebSocket to wss://<main>/api/sync/link (ADR-0114 / ADR-0198 §6.3). The TLS trust manager accepts exactly the
 * pinned key, so a handshake with any other certificate fails before the upgrade request is written: the bearer is
 * never sent to a non-pinned peer. Reconnects with a short back-off while the app wants a link; a 401/403 on the
 * upgrade means the main removed this phone (revoked) and ends the loop.
 */
class ReaderLink(
    private var creds: ReaderCredentials,
    private val session: ReaderSession,
    private val post: (() -> Unit) -> Unit,
    private val postDelayed: (Long, () -> Unit) -> Cancelable,
    /** Pin-mismatch recovery (runs off the main thread); null = a mismatch is just a failed attempt. */
    private val reProof: PinReProof? = null,
    /** The adopted credentials (new pin) to persist; main thread. */
    private val onAdopted: (ReaderCredentials) -> Unit = {},
) {
    private var ws: WebSocket? = null
    private var retry: Cancelable? = null
    private var attempts = 0
    private var wanted = false
    private var http = pinnedClient(creds.pin)

    private fun pinnedClient(pin: String) = client(SpkiPin.enforcing(pin), 20).newBuilder().pingInterval(0, TimeUnit.SECONDS).build()

    /** Main thread. */
    fun start() {
        if (wanted) return
        wanted = true
        connect()
    }

    /** Main thread. */
    fun stop() {
        wanted = false
        retry?.cancel(); retry = null
        ws?.close(1000, null); ws = null
    }

    private fun connect() {
        if (!wanted || session.isRevoked) return
        val req = Request.Builder().url("https://${creds.address}/api/sync/link").header("Authorization", "Bearer ${creds.bearer}").build()
        var gen = 0
        val transport = object : LinkTransport {
            override fun send(text: String): Boolean = ws?.send(text) ?: false

            override fun close() {
                ws?.close(1000, null)
            }
        }
        gen = session.attach(transport)
        val sock = http.newWebSocket(
            req,
            object : WebSocketListener() {
                override fun onOpen(webSocket: WebSocket, response: Response) {
                    post { attempts = 0 }
                }

                override fun onMessage(webSocket: WebSocket, text: String) {
                    post { if (webSocket === ws) session.onText(gen, text) }
                }

                override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
                    post { ended(webSocket, gen, false, null, false) }
                }

                override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                    // Only a 401 means "removed" (review A-6); 429 / 503 / the rest are retried, 429 by Retry-After.
                    val auth = LinkClose.isRevoked(response?.code)
                    val after = response?.header("Retry-After")?.trim()?.toIntOrNull()
                    val mismatch = reProof != null && LinkClose.isPinMismatch(t)
                    post { ended(webSocket, gen, auth, after, mismatch) }
                }
            },
        )
        ws = sock
    }

    private fun ended(sock: WebSocket, gen: Int, authFailed: Boolean, retryAfterSec: Int?, pinMismatch: Boolean) {
        if (sock !== ws) return
        ws = null
        session.onClosed(gen, authFailed)
        if (!wanted || authFailed) return
        if (pinMismatch) { reProve(); return }
        val delay = LinkClose.retryDelayMs(attempts++, retryAfterSec)
        retry = postDelayed(delay) { retry = null; connect() }
    }

    /** The main's key changed: never wipe, never send the bearer there; ask for the proof (one try per 30 s). */
    private fun reProve() {
        val rp = reProof ?: return
        val c = creds
        Thread {
            val r = rp.attempt(c)
            post {
                if (!wanted) return@post
                when (r) {
                    is ReProof.Adopted -> {
                        creds = r.credentials
                        http = pinnedClient(r.credentials.pin)
                        onAdopted(r.credentials)
                        attempts = 0
                        connect()
                    }
                    // anything but a verified proof (a 404 included) is unverified: keep the pairing, back off (A-12)
                    else -> retry = postDelayed(LinkClose.retryDelayMs(attempts++, 30)) { retry = null; connect() }
                }
            }
        }.start()
    }
}

/** Main-thread helpers shared by the activity. */
object MainThread {
    private val h = Handler(Looper.getMainLooper())

    fun post(task: () -> Unit) {
        h.post { task() }
    }

    fun postDelayed(ms: Long, task: () -> Unit): Cancelable {
        val r = Runnable { task() }
        h.postDelayed(r, ms)
        return object : Cancelable {
            override fun cancel(done: () -> Unit) {
                h.removeCallbacks(r)
                done()
            }
        }
    }
}
