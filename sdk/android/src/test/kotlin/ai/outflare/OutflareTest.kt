package ai.outflare

import java.io.File
import java.nio.file.Files
import kotlin.test.Test
import kotlin.test.assertContains
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNotEquals
import kotlin.test.assertTrue

class OutflareTest {
    private class Fake(vararg answers: Int) : Transport {
        private val answers = answers.toMutableList()
        val calls = mutableListOf<Triple<String, String, String>>()

        override fun post(url: String, userAgent: String, body: String): Int {
            calls += Triple(url, userAgent, body)
            return if (answers.isEmpty()) 200 else answers.removeAt(0)
        }
    }

    private fun dir(): File = Files.createTempDirectory("outflare-test").toFile()

    private fun make(fake: Fake, directory: File = dir(), onError: ((String, Int) -> Unit)? = null) = Outflare(
        Config(
            key = "ofk_test_not_a_real_key", directory = directory, host = "https://outflare.example/",
            app = App("Northpeak", "2.4.0", "311"), flushIntervalMillis = 600_000, transport = fake, onError = onError,
        ),
    )

    private fun count(body: String) = Regex("\"messageId\"").findAll(body).count()

    @Test
    fun `screens and events go to the site route with the key and one visitor id`() {
        val fake = Fake()
        val o = make(fake)
        o.screen("Checkout")
        o.track("Order \"Started\"", mapOf("items" to 2, "gift" to true, "tags" to listOf("a", "b"), "note" to null))
        o.flush().get()
        assertEquals(1, fake.calls.size)
        val (url, ua, body) = fake.calls[0]
        assertEquals("https://outflare.example/v1/site/events", url)
        assertTrue(ua.startsWith("outflare-android/${Outflare.VERSION}"))
        assertContains(body, "\"key\":\"ofk_test_not_a_real_key\"")
        assertContains(body, "\"type\":\"screen\",\"name\":\"Checkout\"")
        assertContains(body, "\"type\":\"track\",\"event\":\"Order \\\"Started\\\"\"")
        assertContains(body, "\"properties\":{\"items\":2,\"gift\":true,\"tags\":[\"a\",\"b\"],\"note\":null}")
        assertContains(body, "\"app\":{\"name\":\"Northpeak\",\"version\":\"2.4.0\",\"build\":\"311\"}")
        assertContains(body, "\"library\":{\"name\":\"outflare-android\"")
        assertEquals(2, Regex("\"anonymousId\":\"${o.anonymousId()}\"").findAll(body).count())
    }

    @Test
    fun `at most 20 events go in one request`() {
        val fake = Fake()
        val o = make(fake)
        repeat(45) { o.track("Tapped") }
        o.flush().get()
        assertEquals(listOf(20, 20, 5), fake.calls.map { count(it.third) })
    }

    @Test
    fun `events wait through no answer and a busy server and are dropped on a refusal`() {
        val fake = Fake(0, 503, 200, 401)
        val lost = mutableListOf<Int>()
        val o = make(fake) { _, n -> lost += n }
        o.track("Kept")
        o.flush().get()
        o.flush().get()
        assertEquals(2, fake.calls.size)
        o.flush().get()
        assertEquals(3, fake.calls.size)
        // The same body each time, so a retry is stored once.
        assertEquals(fake.calls[0].third, fake.calls[2].third)
        o.flush().get()
        assertEquals(3, fake.calls.size)
        o.track("Refused")
        o.flush().get()
        assertEquals(listOf(1), lost)
        o.flush().get()
        assertEquals(4, fake.calls.size)
    }

    @Test
    fun `the visitor id and waiting events survive a restart and reset starts a new visitor`() {
        val directory = dir()
        val first = make(Fake(0), directory)
        val id = first.anonymousId()
        first.screen("Home")
        first.flush().get()
        assertEquals(1, File(directory, "outflare-queue.jsonl").readLines().size)

        val up = Fake()
        val second = make(up, directory)
        assertEquals(id, second.anonymousId())
        second.flush().get()
        assertContains(up.calls[0].third, "\"name\":\"Home\"")
        assertFalse(File(directory, "outflare-queue.jsonl").exists())

        second.reset().get()
        assertNotEquals(id, second.anonymousId())
        assertEquals(second.anonymousId(), File(directory, "outflare-id").readText())
    }

    @Test
    fun `strings are escaped`() {
        assertEquals("\"a\\\"b\\\\c\\n\\u0001\"", Json.string("a\"b\\c\n\u0001"))
        assertEquals("{\"n\":1.5,\"x\":null}", Json.encode(mapOf("n" to 1.5, "x" to Double.NaN)))
    }
}
