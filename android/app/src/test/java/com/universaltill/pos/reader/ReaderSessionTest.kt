package com.universaltill.pos.reader

import com.universaltill.pos.tap.Capability
import com.universaltill.pos.tap.CollectEnd
import com.universaltill.pos.tap.FakeScheduler
import com.universaltill.pos.tap.TapResult
import com.universaltill.pos.tap.TapStatus
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

class FakeTransport : LinkTransport {
    val sent = mutableListOf<JSONObject>()
    var closed = false

    override fun send(text: String): Boolean {
        sent += JSONObject(text)
        return !closed
    }

    override fun close() {
        closed = true
    }

    fun types() = sent.map { it.getString("type") }

    fun last(type: String) = sent.last { it.getString("type") == type }

    fun all(type: String) = sent.filter { it.getString("type") == type }
}

class FakePayments : ReaderPayments {
    var cap = Capability(supported = true, ready = true, reason = null, needs = emptyList())
    val collected = mutableListOf<ReaderCollect>()
    val remaining = mutableListOf<Long>()
    val cancelled = mutableListOf<String>()
    private var end: ((CollectEnd) -> Unit)? = null
    var setups = 0
    var foreground = false
    var disconnects = 0

    override fun onForeground() { foreground = true }

    override fun onBackground() { foreground = false }

    override fun disconnect() { disconnects++ }

    override fun capability() = cap

    override fun setup(cb: (String?) -> Unit) {
        setups++
        cb(null)
    }

    override fun collect(c: ReaderCollect, remainingMs: Long, end: (CollectEnd) -> Unit) {
        collected += c
        remaining += remainingMs
        this.end = end
    }

    override fun cancel(key: String): Boolean {
        cancelled += key
        return true
    }

    fun finish(e: CollectEnd) = end!!(e)

    fun verdict(status: TapStatus, reason: String = "ok", decline: String? = null, brand: String? = null, last4: String? = null) =
        finish(CollectEnd.Verdict(TapResult("r", status, reason, "pi_1", "ch_1", decline, brand, last4, false)))
}

class FakeUi : ReaderUi {
    val states = mutableListOf<ReaderScreen>()
    var vibrations = 0
    var revoked = 0

    override fun show(s: ReaderScreen) {
        states += s
    }

    override fun incoming() {
        vibrations++
    }

    override fun revoked() {
        revoked++
    }

    fun last() = states.last()
}

class ReaderSessionTest {
    private lateinit var sched: FakeScheduler
    private lateinit var pay: FakePayments
    private lateinit var ui: FakeUi
    private lateinit var s: ReaderSession
    private lateinit var t: FakeTransport
    private var gen = 0

    @Before fun setUp() {
        sched = FakeScheduler()
        pay = FakePayments()
        ui = FakeUi()
        s = ReaderSession(sched, pay, ui, appVersion = "0.36.2")
        s.onForeground()
        connect()
    }

    private fun connect(hello: Boolean = true) {
        t = FakeTransport()
        gen = s.attach(t)
        if (hello) s.onText(gen, Goldens.text("hello_main"))
    }

    private fun collect() = s.onText(gen, Goldens.text("collect"))

    private fun same(want: JSONObject, got: JSONObject) =
        assertTrue("$got != $want", want.keys().asSequence().toSet() == got.keys().asSequence().toSet() && want.keys().asSequence().all { want.get(it).toString() == got.get(it).toString() })

    @Test fun `hello then ready status, nothing before the main spoke`() {
        val fresh = FakeTransport()
        val g = s.attach(fresh)
        assertTrue("phone spoke first", fresh.sent.isEmpty())
        s.onText(g, Goldens.text("hello_main"))
        assertEquals(listOf("hello", "reader.status"), fresh.types())
        same(Goldens.payload("hello_phone"), fresh.last("hello").getJSONObject("payload"))
        same(Goldens.payload("status_ready"), fresh.last("reader.status").getJSONObject("payload"))
        assertEquals(ReaderScreen.Idle, ui.last())
    }

    @Test fun `a hello from anything but the main closes the link`() {
        val x = FakeTransport()
        val g = s.attach(x)
        s.onText(g, """{"v":1,"id":"1","type":"hello","payload":{"role":"till","reader_protocol":1}}""")
        assertTrue(x.closed)
        assertTrue(x.sent.isEmpty())
        val y = FakeTransport()
        val g2 = s.attach(y)
        s.onText(g2, """{"v":1,"id":"1","type":"hello","payload":{"role":"main","reader_protocol":2}}""")
        assertTrue(y.closed)
    }

    @Test fun `a phone that needs setup reports needs_setup with the golden shape`() {
        pay.cap = Capability(true, false, null, listOf("nfc_off"))
        connect()
        same(Goldens.payload("status_needs_setup"), t.last("reader.status").getJSONObject("payload"))
        assertEquals(ReaderScreen.NeedsSetup, ui.last())
    }

    @Test fun `collect is acked at once, busy is reported, the screen shows the amount`() {
        collect()
        val ack = t.last("reader.collect")
        assertEquals("m1-3", ack.getString("reply_to"))
        same(Goldens.payload("collect_ack"), ack.getJSONObject("payload"))
        assertEquals("busy", t.last("reader.status").getJSONObject("payload").getString("state"))
        assertEquals(ReaderScreen.Pay(1250, "gbp", 110000), ui.last())
        assertEquals(1, ui.vibrations)
        assertEquals(1, pay.collected.size)
        assertEquals("att_7:1:1", pay.collected[0].key)
        assertEquals(110000L, pay.remaining[0])
    }

    @Test fun `an approved verdict sends one approved result, then ready again, and the screen says Done`() {
        collect()
        pay.verdict(TapStatus.SUCCEEDED)
        same(Goldens.payload("result_approved"), t.last("reader.result").getJSONObject("payload"))
        assertEquals(1, t.all("reader.result").size)
        assertEquals("ready", t.last("reader.status").getJSONObject("payload").getString("state"))
        assertEquals(ReaderScreen.Result(ReaderResultKind.DONE), ui.last())
        sched.advance(3_000)
        assertEquals(ReaderScreen.Idle, ui.last())
    }

    @Test fun `a decline is declined with its code and the card may be retried inside the window`() {
        collect()
        pay.verdict(TapStatus.DECLINED, "declined_by_issuer", decline = "card_declined")
        same(Goldens.payload("result_declined"), t.last("reader.result").getJSONObject("payload"))
        assertEquals(ReaderScreen.Result(ReaderResultKind.CARD_REFUSED), ui.last())
    }

    @Test fun `unknown is pending, never declined, and an amount mismatch keeps its reason`() {
        collect()
        pay.verdict(TapStatus.UNKNOWN, "amount_mismatch")
        same(Goldens.payload("result_pending"), t.last("reader.result").getJSONObject("payload"))
        assertEquals(ReaderScreen.Result(ReaderResultKind.EXPIRED), ui.last())
    }

    @Test fun `cancelled and a refusal before the tap are cancelled, an unresolved earlier attempt is pending`() {
        collect()
        pay.verdict(TapStatus.CANCELLED, "customer")
        assertEquals("cancelled", t.last("reader.result").getJSONObject("payload").getString("status"))
        connect(); collect()
        pay.finish(CollectEnd.Refused("offline"))
        assertEquals("cancelled", t.last("reader.result").getJSONObject("payload").getString("status"))
        connect(); collect()
        pay.finish(CollectEnd.Refused("outcome_unknown"))
        val p = t.last("reader.result").getJSONObject("payload")
        assertEquals("pending", p.getString("status"))
        assertEquals("outcome_unknown", p.getString("reason"))
    }

    @Test fun `no card data ever leaves the phone, even if the SDK result carried a brand and last four`() {
        collect()
        pay.verdict(TapStatus.SUCCEEDED, brand = "visa", last4 = "4242")
        val text = t.last("reader.result").toString()
        assertFalse(text.contains("visa")); assertFalse(text.contains("4242")); assertFalse(text.contains("brand")); assertFalse(text.contains("last4"))
        assertFalse(t.sent.joinToString().contains("pi_example_secret_example"))
    }

    @Test fun `the client secret is never in any frame the phone sends or on the screen state`() {
        collect()
        pay.verdict(TapStatus.SUCCEEDED)
        assertFalse(t.sent.joinToString().contains("secret"))
        assertFalse(ui.states.joinToString().contains("secret"))
    }

    @Test fun `a second collect while one runs is answered busy and nothing starts`() {
        collect()
        val before = pay.collected.size
        s.onText(gen, Goldens.text("collect").replace("m1-3", "m1-9"))
        assertEquals(before, pay.collected.size)
        val err = t.last("error")
        assertEquals("m1-9", err.getString("reply_to"))
        same(Goldens.payload("collect_busy"), err.getJSONObject("payload"))
    }

    @Test fun `collect before the main's hello, or with a bad frame, or a bad timeout starts nothing`() {
        val x = FakeTransport()
        val g = s.attach(x)
        s.onText(g, Goldens.text("collect"))
        assertTrue(pay.collected.isEmpty())
        connect()
        s.onText(gen, Goldens.text("collect").replace("\"amount_minor\":1250", "\"amount_minor\":0").replace("m1-3", "m1-4"))
        assertEquals("bad_message", t.last("error").getJSONObject("payload").getString("code"))
        s.onText(gen, Goldens.text("collect").replace("110000", "0").replace("m1-3", "m1-5"))
        assertEquals("bad_message", t.last("error").getJSONObject("payload").getString("code"))
        assertTrue(pay.collected.isEmpty())
    }

    @Test fun `a collect while the app is in the background is refused busy and nothing starts`() {
        s.onBackground()
        collect()
        assertTrue(pay.collected.isEmpty())
        assertEquals("busy", t.last("error").getJSONObject("payload").getString("code"))
    }

    @Test fun `backgrounding while idle reports unavailable, coming back reports ready`() {
        s.onBackground()
        assertEquals("unavailable", t.last("reader.status").getJSONObject("payload").getString("state"))
        assertEquals(ReaderScreen.Paused, ui.last())
        s.onForeground()
        assertEquals("ready", t.last("reader.status").getJSONObject("payload").getString("state"))
    }

    @Test fun `backgrounding mid-collect does not cancel the payment`() {
        collect()
        s.onBackground()
        assertTrue(pay.cancelled.isEmpty())
    }

    @Test fun `a cancel for the live key stops it, another key is ignored`() {
        collect()
        s.onText(gen, Goldens.text("cancel").replace("att_7:1:1", "other"))
        assertTrue(pay.cancelled.isEmpty())
        s.onText(gen, Goldens.text("cancel"))
        assertEquals(listOf("att_7:1:1"), pay.cancelled)
    }

    @Test fun `a cancel with nothing live is a no-op`() {
        s.onText(gen, Goldens.text("cancel"))
        assertTrue(pay.cancelled.isEmpty())
    }

    @Test fun `a verdict after the link was replaced is dropped, never sent on the new link`() {
        collect()
        val old = t
        connect()
        pay.verdict(TapStatus.SUCCEEDED)
        assertTrue(old.all("reader.result").isEmpty())
        assertTrue(t.all("reader.result").isEmpty())
    }

    @Test fun `link lost mid-collect cancels after 30 s, not before, and says lost contact`() {
        collect()
        s.onClosed(gen, authFailed = false)
        sched.advance(29_999)
        assertTrue(pay.cancelled.isEmpty())
        sched.advance(2)
        assertEquals(listOf("att_7:1:1"), pay.cancelled)
        assertEquals(ReaderScreen.Result(ReaderResultKind.LINK_LOST), ui.last())
    }

    @Test fun `the link back inside 30 s keeps the collect`() {
        collect()
        s.onClosed(gen, authFailed = false)
        sched.advance(10_000)
        connect()
        sched.advance(60_000)
        assertTrue(pay.cancelled.isEmpty())
    }

    // ---- review A-3: the 30 s is anchored to the FIRST loss ----

    @Test fun `reconnect attempts that never reach the main do not restart the 30 s`() {
        collect()
        s.onClosed(gen, authFailed = false)
        sched.advance(10_000)
        for (i in 1..4) { // back-off attempts: attach, fail, attach, fail (no hello)
            gen = s.attach(FakeTransport())
            s.onClosed(gen, authFailed = false)
            sched.advance(4_000)
        }
        // 26 s since the first loss
        assertTrue(pay.cancelled.isEmpty())
        sched.advance(4_100)
        assertEquals(listOf("att_7:1:1"), pay.cancelled)
        assertEquals(ReaderScreen.Result(ReaderResultKind.LINK_LOST), ui.last())
    }

    @Test fun `a loss after a real hello starts a new 30 s`() {
        collect()
        s.onClosed(gen, authFailed = false)
        sched.advance(20_000)
        connect() // hello: contact is back
        sched.advance(5_000)
        s.onClosed(gen, authFailed = false)
        sched.advance(29_000)
        assertTrue(pay.cancelled.isEmpty())
        sched.advance(1_500)
        assertEquals(1, pay.cancelled.size)
    }

    // ---- review A-1: the engine's foreground state follows the session's lifecycle ----

    @Test fun `foreground and background reach the payments`() {
        assertTrue(pay.foreground)
        s.onBackground()
        assertFalse(pay.foreground)
        s.onForeground()
        assertTrue(pay.foreground)
    }

    // ---- review A-4: the Stripe connection is dropped (ADR-0198 6.4) ----

    @Test fun `backgrounded while idle drops the SDK connection, mid-collect it does not`() {
        s.onBackground()
        assertEquals(1, pay.disconnects)
        s.onForeground()
        collect()
        s.onBackground()
        assertEquals(1, pay.disconnects)
    }

    @Test fun `a 30 s link loss cancels the collect and drops the SDK connection`() {
        collect()
        s.onClosed(gen, authFailed = false)
        sched.advance(30_100)
        assertEquals(1, pay.cancelled.size)
        assertEquals(1, pay.disconnects)
    }

    @Test fun `revoke drops the SDK connection at once`() {
        collect()
        s.onClosed(gen, authFailed = true)
        assertEquals(listOf("att_7:1:1"), pay.cancelled)
        assertEquals(1, pay.disconnects)
    }

    @Test fun `revoke while idle drops the SDK connection too`() {
        s.onClosed(gen, authFailed = true)
        assertEquals(1, pay.disconnects)
    }

    // ---- review A-5: re-pairing after a revoke ----

    @Test fun `after a revoke a fresh enrolment works without a restart`() {
        s.onClosed(gen, authFailed = true)
        assertTrue(s.isRevoked)
        s.enrolled()
        assertFalse(s.isRevoked)
        connect()
        assertEquals(ReaderScreen.Idle, ui.last())
        assertEquals("reader.status", t.types().last())
        collect()
        assertEquals(1, pay.collected.size)
    }

    // ---- review A-6: which close means revoked ----

    @Test fun `only a 401 on the upgrade is a revoke`() {
        assertTrue(LinkClose.isRevoked(401))
        for (c in listOf(403, 429, 503, 500, 502, 404, 200, null)) assertFalse("$c", LinkClose.isRevoked(c))
    }

    @Test fun `429 and 503 back off by Retry-After, bounded`() {
        assertEquals(1_000L, LinkClose.retryDelayMs(attempt = 0, retryAfterSec = null))
        assertEquals(15_000L, LinkClose.retryDelayMs(attempt = 9, retryAfterSec = null))
        assertEquals(42_000L, LinkClose.retryDelayMs(attempt = 0, retryAfterSec = 42))
        assertEquals(120_000L, LinkClose.retryDelayMs(attempt = 0, retryAfterSec = 99_999))
        assertEquals(1_000L, LinkClose.retryDelayMs(attempt = 0, retryAfterSec = 0))
    }

    @Test fun `a closed link while idle shows offline, not an error`() {
        s.onClosed(gen, authFailed = false)
        assertEquals(ReaderScreen.Offline, ui.last())
    }

    @Test fun `an auth failure means revoked, says so once and goes quiet`() {
        s.onClosed(gen, authFailed = true)
        assertEquals(1, ui.revoked)
        assertEquals(ReaderScreen.Revoked, ui.last())
        assertTrue(s.isRevoked)
    }

    @Test fun `the phone pings every 5 seconds after the hello`() {
        val before = t.all("ping").size
        sched.advance(5_000)
        sched.advance(5_000)
        assertEquals(before + 2, t.all("ping").size)
        s.onClosed(gen, false)
        val n = t.all("ping").size
        sched.advance(20_000)
        assertEquals(n, t.all("ping").size)
    }

    @Test fun `unknown frames from the main are ignored`() {
        s.onText(gen, """{"v":1,"id":"z","type":"sat.sync","payload":{}}""")
        s.onText(gen, "garbage")
        assertEquals(ReaderScreen.Idle, ui.last())
    }

    // ---- the SDK's connection token provider rides the link ----

    @Test fun `a token request goes out and the golden reply is delivered`() {
        var got: TokenResult? = null
        s.requestToken { got = it }
        val req = t.last("reader.token")
        same(Goldens.payload("token_request"), req.getJSONObject("payload"))
        val reply = Goldens.text("token_reply").replace("p1-4", req.getString("id"))
        s.onText(gen, reply)
        assertEquals(TokenResult.Ok("pst_test_example_secret", "tml_example"), got)
    }

    @Test fun `a token error code is passed on and a silent main times out`() {
        var got: TokenResult? = null
        s.requestToken { got = it }
        val id = t.last("reader.token").getString("id")
        s.onText(gen, """{"v":1,"id":"m","type":"error","reply_to":"$id","payload":{"code":"rate_limited","retryable":true}}""")
        assertEquals(TokenResult.Err("rate_limited"), got)
        got = null
        s.requestToken { got = it }
        sched.advance(ReaderSession.TOKEN_TIMEOUT_MS + 1)
        assertEquals(TokenResult.Err("timeout"), got)
    }

    @Test fun `a token reply to another request id is ignored and a late one does not call back twice`() {
        var calls = 0
        s.requestToken { calls++ }
        s.onText(gen, Goldens.text("token_reply").replace("p1-4", "someone-else"))
        assertEquals(0, calls)
        sched.advance(ReaderSession.TOKEN_TIMEOUT_MS + 1)
        assertEquals(1, calls)
        s.onText(gen, Goldens.text("token_reply").replace("p1-4", t.last("reader.token").getString("id")))
        assertEquals(1, calls)
    }

    @Test fun `a token request with no link answers offline at once`() {
        s.onClosed(gen, false)
        var got: TokenResult? = null
        s.requestToken { got = it }
        assertEquals(TokenResult.Err("offline"), got)
    }

    @Test fun `the setup button runs the engine setup then re-reports`() {
        pay.cap = Capability(true, false, null, listOf("nfc_off"))
        connect()
        pay.cap = Capability(true, true, null, emptyList())
        s.setup()
        assertEquals(1, pay.setups)
        assertEquals("ready", t.last("reader.status").getJSONObject("payload").getString("state"))
    }
}
