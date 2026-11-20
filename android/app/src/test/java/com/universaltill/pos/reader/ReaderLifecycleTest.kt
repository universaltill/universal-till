package com.universaltill.pos.reader

import com.universaltill.pos.tap.FakeProbe
import com.universaltill.pos.tap.FakeReader
import com.universaltill.pos.tap.FakeScheduler
import com.universaltill.pos.tap.FakeUi as TapFakeUi
import com.universaltill.pos.tap.MainExecutor
import com.universaltill.pos.tap.MemoryDoneStore
import com.universaltill.pos.tap.MemoryPendingStore
import com.universaltill.pos.tap.PendingRecord
import com.universaltill.pos.tap.PendingState
import com.universaltill.pos.tap.TapToPayEngine
import com.universaltill.pos.tap.readyFacts
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The session wired to the REAL Tap to Pay engine the way ReaderProcess wires it. Nothing here calls
 * engine.onForeground() by hand: the foreground state arrives through the session's own lifecycle
 * (ReaderActivity.onStart -> session.onForeground), which is what review A-1 found missing.
 */
class ReaderLifecycleTest {
    private val inline = MainExecutor { it() }
    private val sched = FakeScheduler()
    private val reader = FakeReader(sched)
    private val pending = MemoryPendingStore()
    private val done = MemoryDoneStore()
    private val engine = TapToPayEngine(FakeProbe(readyFacts()), reader, sched, pending, done, TapFakeUi())
    private var wall = 1_000_000L
    private lateinit var session: ReaderSession
    private val ui = FakeUi()
    private val link = FakeTransport()
    private var gen = 0

    private fun boot() {
        val cloud = ReaderCloudApi(inline) { session }
        session = ReaderSession(sched, EnginePayments(engine, cloud, nowMs = { wall }), ui, "t")
        gen = session.attach(link)
        session.onText(gen, Goldens.text("hello_main"))
    }

    private fun collectFrame(key: String = "att_7:1:1") = Goldens.text("collect").replace("att_7:1:1", key)

    @Test fun `a collect after the activity started is taken by the engine, not refused as not_foreground`() {
        boot()
        session.onForeground() // ReaderActivity.onStart
        session.onText(gen, collectFrame())
        assertEquals("approved", link.last("reader.result").getJSONObject("payload").getString("status"))
    }

    @Test fun `backgrounded, the engine refuses and the phone says it is unavailable`() {
        boot()
        session.onForeground()
        session.onBackground()
        session.onText(gen, collectFrame())
        assertTrue(link.all("reader.result").isEmpty())
        assertEquals("busy", link.last("error").getJSONObject("payload").getString("code"))
        assertTrue(reader.createCalls.isEmpty())
    }

    // ---- review A-2: one unknown outcome must not block the phone for good ----

    private fun unknown(key: String) = pending.put(PendingRecord("r0", key, "pi_old", PendingState.UNKNOWN, 500, "gbp"))

    @Test fun `an earlier unknown attempt is released when the main sends the next payment, the phone never says charged`() {
        boot()
        session.onForeground()
        unknown("att_old:1:1")
        session.onText(gen, collectFrame("att_9:1:1"))
        assertEquals("approved", link.last("reader.result").getJSONObject("payload").getString("status"))
        assertTrue(pending.all().none { it.key == "att_old:1:1" })
        val words = ui.states.map { it.toString().lowercase() }
        assertTrue(words.none { "charged" in it })
    }

    @Test fun `the same unknown key is not released at once, it stays pending for the main`() {
        boot()
        session.onForeground()
        unknown("att_7:1:1")
        session.onText(gen, collectFrame("att_7:1:1"))
        val r = link.last("reader.result").getJSONObject("payload")
        assertEquals("pending", r.getString("status"))
        assertEquals("outcome_unknown", r.getString("reason"))
        assertTrue(reader.createCalls.isEmpty())
    }

    @Test fun `the same unknown key is released after the timeout`() {
        boot()
        session.onForeground()
        unknown("att_7:1:1")
        session.onText(gen, collectFrame("att_7:1:1"))
        wall += EnginePayments.UNKNOWN_TTL_MS - 1
        session.onText(gen, collectFrame("att_7:1:1"))
        assertEquals(listOf("pending", "pending"), link.all("reader.result").map { it.getJSONObject("payload").getString("status") })
        assertTrue(reader.createCalls.isEmpty())
        wall += 2
        session.onText(gen, collectFrame("att_7:1:1"))
        assertEquals(1, reader.createCalls.size)
    }

    @Test fun `EnginePayments drops the SDK connection on request`() {
        var drops = 0
        val r = object : com.universaltill.pos.tap.Reader by reader {
            override fun disconnect() { drops++ }
        }
        val e = TapToPayEngine(FakeProbe(readyFacts()), r, sched, pending, done, TapFakeUi())
        EnginePayments(e, ReaderCloudApi(inline) { session }, nowMs = { wall }).disconnect()
        assertEquals(1, drops)
    }
}
