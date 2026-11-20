package com.universaltill.pos.reader

import com.universaltill.pos.tap.CloudException
import com.universaltill.pos.tap.CollectEnd
import com.universaltill.pos.tap.FakeReader
import com.universaltill.pos.tap.FakeScheduler
import com.universaltill.pos.tap.FakeProbe
import com.universaltill.pos.tap.FakeUi as TapFakeUi
import com.universaltill.pos.tap.MainExecutor
import com.universaltill.pos.tap.MemoryDoneStore
import com.universaltill.pos.tap.MemoryPendingStore
import com.universaltill.pos.tap.TapStatus
import com.universaltill.pos.tap.TapToPayEngine
import com.universaltill.pos.tap.readyFacts
import java.util.Locale
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

class ReaderPaymentsTest {
    private val inline = MainExecutor { it() }
    private val collect = ReaderFrames.parseCollect(Goldens.payload("collect"))!!

    private fun session(): Pair<ReaderSession, FakeTransport> {
        val s = ReaderSession(FakeScheduler(), FakePayments(), FakeUi(), "t")
        val t = object : LinkTransport {
            val inner = FakeTransport()
            var g = 0
            override fun send(text: String): Boolean {
                inner.send(text)
                val j = JSONObject(text)
                if (j.getString("type") == "reader.token") s.onText(g, Goldens.text("token_reply").replace("p1-4", j.getString("id")))
                return true
            }
            override fun close() {}
        }
        val g = s.attach(t); t.g = g
        s.onForeground()
        s.onText(g, Goldens.text("hello_main"))
        return s to t.inner
    }

    @Test fun `the session answer comes from the armed collect, never from the cloud`() {
        val api = ReaderCloudApi(inline) { session().first }
        api.arm(collect)
        val j = JSONObject(api.sessionCreate(1250, "gbp", "att_7:1:1"))
        assertEquals("pi_example_secret_example", j.getString("client_secret"))
        assertEquals(1250L, j.getLong("amount_minor"))
        val st = JSONObject(api.onboardingStatus())
        assertEquals("tml_example", st.getString("location_id"))
        assertTrue(st.getBoolean("onboarded") && st.getBoolean("charges_enabled"))
    }

    @Test fun `another key, amount or currency than the armed collect is refused, and a disarmed one too`() {
        val api = ReaderCloudApi(inline) { session().first }
        api.arm(collect)
        for ((a, c, k) in listOf(Triple(1251L, "gbp", "att_7:1:1"), Triple(1250L, "eur", "att_7:1:1"), Triple(1250L, "gbp", "other"))) {
            try { api.sessionCreate(a, c, k); fail("accepted $a $c $k") } catch (_: CloudException) {}
        }
        api.disarm()
        try { api.sessionCreate(1250, "gbp", "att_7:1:1"); fail() } catch (_: CloudException) {}
    }

    @Test fun `the SDK token provider is answered over the link`() {
        val (s, _) = session()
        val api = ReaderCloudApi(inline) { s }
        assertEquals("pst_test_example_secret", JSONObject(api.connectionToken()).getString("secret"))
        assertEquals("tml_example", JSONObject(api.onboardingStatus()).getString("location_id"))
    }

    @Test fun `no location known means not enrolled, not a guess`() {
        try { ReaderCloudApi(inline) { session().first }.onboardingStatus(); fail() } catch (e: CloudException) { assertEquals("not_enrolled", e.code) }
    }

    @Test fun `a collect runs on the shared engine and the client secret is released afterwards`() {
        val sched = FakeScheduler()
        val engine = TapToPayEngine(FakeProbe(readyFacts()), FakeReader(sched), sched, MemoryPendingStore(), MemoryDoneStore(), TapFakeUi())
        engine.onForeground()
        val api = ReaderCloudApi(inline) { session().first }
        val p = EnginePayments(engine, api)
        var end: CollectEnd? = null
        p.collect(collect, 110_000) { end = it }
        assertEquals(TapStatus.SUCCEEDED, ((end as CollectEnd.Verdict).result).status)
        try { api.sessionCreate(1250, "gbp", "att_7:1:1"); fail("still armed") } catch (_: CloudException) {}
    }

    @Test fun `the amount on the screen is the customer's currency in minor units`() {
        assertEquals("£12.50", ReaderAmount.format(1250, "gbp", Locale.UK))
        assertEquals("¥500", ReaderAmount.format(500, "jpy", Locale.JAPAN).replace('￥', '¥'))
        assertEquals("£0.05", ReaderAmount.format(5, "gbp", Locale.UK))
        assertTrue(ReaderAmount.format(1, "zzz", Locale.UK).isNotEmpty())
    }
}
