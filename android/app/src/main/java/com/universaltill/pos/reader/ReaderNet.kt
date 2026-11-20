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
            client(tm, 15).newCall(req).execute().use { r -> PairHttp.Answer(r.code, r.body?.string().orEmpty().take(8192), learn?.pin ?: expectPin) }
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
    private val creds: ReaderCredentials,
    private val session: ReaderSession,
    private val post: (() -> Unit) -> Unit,
    private val postDelayed: (Long, () -> Unit) -> Cancelable,
) {
    private var ws: WebSocket? = null
    private var retry: Cancelable? = null
    private var attempts = 0
    private var wanted = false
    private val http = client(SpkiPin.enforcing(creds.pin), 20).newBuilder().pingInterval(0, TimeUnit.SECONDS).build()

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
                    post { ended(webSocket, gen, false) }
                }

                override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                    val auth = response?.code == 401 || response?.code == 403
                    post { ended(webSocket, gen, auth) }
                }
            },
        )
        ws = sock
    }

    private fun ended(sock: WebSocket, gen: Int, authFailed: Boolean) {
        if (sock !== ws) return
        ws = null
        session.onClosed(gen, authFailed)
        if (!wanted || authFailed) return
        val delay = minOf(15_000L, 1_000L shl minOf(attempts++, 4))
        retry = postDelayed(delay) { retry = null; connect() }
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
