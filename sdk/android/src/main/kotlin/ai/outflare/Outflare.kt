// Outflare for Android: screens and events from an app, sent with the
// app's public site key to POST /v1/site/events. The visitor's id and the
// events still waiting are kept in files, so they survive the app being
// closed. See README.md.
package ai.outflare

import java.io.File
import java.net.HttpURLConnection
import java.net.URI
import java.util.Locale
import java.util.TimeZone
import java.util.UUID
import java.util.concurrent.Executors
import java.util.concurrent.Future
import java.util.concurrent.ScheduledExecutorService
import java.util.concurrent.ScheduledFuture
import java.util.concurrent.TimeUnit

/** The app, for reports by version. */
data class App(val name: String? = null, val version: String? = null, val build: String? = null)

/** How a request is sent: replaced in tests. It returns the HTTP status, or 0 for no answer. */
fun interface Transport {
    fun post(url: String, userAgent: String, body: String): Int
}

class Config(
    /** The app's public site key (ofk_...), from a site key made with no origins. */
    val key: String,
    /** Where the visitor id and waiting events are kept: pass context.filesDir. */
    val directory: File,
    /** Your Outflare server. */
    val host: String = "https://app.outflare.ai",
    val app: App? = null,
    /** Send once this many events wait: at most 20, the most one request takes. */
    val flushAt: Int = 20,
    /** Send at least this often. */
    val flushIntervalMillis: Long = 30_000,
    /** The most events kept waiting; the oldest are dropped past it. */
    val maxQueue: Int = 1000,
    /** Called when events are given up on, with how many. */
    val onError: ((String, Int) -> Unit)? = null,
    val transport: Transport? = null,
)

/** One app's connection to Outflare. Make one, in Application.onCreate, and share it. */
class Outflare(private val config: Config) {
    private val worker: ScheduledExecutorService = Executors.newSingleThreadScheduledExecutor { r ->
        Thread(r, "outflare").apply { isDaemon = true }
    }
    private val url = config.host.trimEnd('/') + "/v1/site/events"
    private val idFile = File(config.directory, "outflare-id")
    private val queueFile = File(config.directory, "outflare-queue.jsonl")
    private val transport = config.transport ?: Transport(::httpPost)
    private val flushAt = config.flushAt.coerceIn(1, MAX_BATCH)
    private val phone = Phone.read()
    private val context: String

    // Only the worker thread touches these.
    private var id = ""
    private val queue = ArrayDeque<String>()
    private var timer: ScheduledFuture<*>? = null

    init {
        require(config.key.isNotEmpty()) { "Outflare: key is required" }
        val parts = mutableListOf("\"library\":{\"name\":\"$LIBRARY\",\"version\":\"$VERSION\"}")
        config.app?.let { a -> obj("name" to a.name, "version" to a.version, "build" to a.build)?.let { parts += "\"app\":$it" } }
        obj("manufacturer" to phone.manufacturer, "model" to phone.model, "type" to "android")?.let { parts += "\"device\":$it" }
        obj("name" to phone.osName, "version" to phone.osVersion)?.let { parts += "\"os\":$it" }
        parts += "\"locale\":${Json.string(Locale.getDefault().toLanguageTag())}"
        parts += "\"timezone\":${Json.string(TimeZone.getDefault().id)}"
        context = "{" + parts.joinToString(",") + "}"
        worker.execute(::load)
    }

    /** The visitor's id on this phone. Send it to your server with your user's id to join the two. It waits for storage, so call it off the main thread. */
    fun anonymousId(): String = worker.submit<String> { id }.get()

    /** A screen the person saw. */
    fun screen(name: String, properties: Map<String, Any?> = emptyMap()) {
        require(name.isNotBlank()) { "Outflare: screen needs a name" }
        add("screen", "name", name, properties)
    }

    /** Something the person did, such as Order Started. */
    fun track(event: String, properties: Map<String, Any?> = emptyMap()) {
        require(event.isNotBlank()) { "Outflare: track needs an event name" }
        add("track", "event", event, properties)
    }

    /** Forget the visitor: call it when someone signs out, so the next person starts fresh. */
    fun reset(): Future<*> = worker.submit {
        drain()
        id = UUID.randomUUID().toString()
        runCatching { idFile.writeText(id) }
    }

    /** Send everything waiting now. The Future is done once it has been sent or kept for later. */
    fun flush(): Future<*> = worker.submit(::drain)

    private fun add(type: String, nameKey: String, name: String, properties: Map<String, Any?>) {
        val props = Json.encode(properties)
        val messageId = UUID.randomUUID().toString()
        worker.execute {
            queue.addLast(
                "{\"type\":\"$type\",\"$nameKey\":${Json.string(name)},\"anonymousId\":${Json.string(id)}," +
                    "\"messageId\":\"$messageId\",\"properties\":$props,\"context\":$context}",
            )
            if (queue.size > config.maxQueue) {
                val lost = queue.size - config.maxQueue
                repeat(lost) { queue.removeFirst() }
                config.onError?.invoke("Outflare: too many events are waiting, so the oldest were dropped", lost)
            }
            save()
            if (queue.size >= flushAt) {
                drain()
            } else if (timer == null) {
                timer = worker.schedule(::drain, config.flushIntervalMillis, TimeUnit.MILLISECONDS)
            }
        }
    }

    private fun load() {
        runCatching {
            config.directory.mkdirs()
            if (idFile.exists()) id = idFile.readText().trim()
            // Events from the last launch go first.
            if (queueFile.exists()) queueFile.readLines().filter { it.startsWith("{") }.forEach(queue::addLast)
        }
        if (!ID.matches(id)) {
            id = UUID.randomUUID().toString()
            runCatching { idFile.writeText(id) }
        }
    }

    private fun save() {
        runCatching {
            if (queue.isEmpty()) queueFile.delete() else queueFile.writeText(queue.joinToString("\n"))
        }
    }

    private fun drain() {
        timer?.cancel(false)
        timer = null
        while (queue.isNotEmpty()) {
            val batch = queue.take(MAX_BATCH)
            val body = "{\"key\":${Json.string(config.key)},\"batch\":[${batch.joinToString(",")}]}"
            val status = runCatching { transport.post(url, phone.userAgent, body) }.getOrDefault(0)
            // No answer, or a busy server: keep the events for the next try.
            if (status == 0 || status == 408 || status == 429 || status >= 500) break
            repeat(batch.size) { queue.removeFirst() }
            if (status !in 200..299) {
                config.onError?.invoke("Outflare: the server refused the events ($status); check the site key", batch.size)
            }
        }
        save()
        if (queue.isNotEmpty() && timer == null) {
            timer = worker.schedule(::drain, config.flushIntervalMillis, TimeUnit.MILLISECONDS)
        }
    }

    private fun obj(vararg fields: Pair<String, String?>): String? {
        val set = fields.filter { !it.second.isNullOrBlank() }
        if (set.isEmpty()) return null
        return set.joinToString(",", "{", "}") { "${Json.string(it.first)}:${Json.string(it.second!!)}" }
    }

    private fun httpPost(url: String, userAgent: String, body: String): Int {
        val c = URI(url).toURL().openConnection() as HttpURLConnection
        return try {
            c.requestMethod = "POST"
            c.connectTimeout = 10_000
            c.readTimeout = 15_000
            c.doOutput = true
            c.setRequestProperty("Content-Type", "text/plain;charset=UTF-8")
            c.setRequestProperty("User-Agent", userAgent)
            c.outputStream.use { it.write(body.toByteArray(Charsets.UTF_8)) }
            c.responseCode
        } finally {
            c.disconnect()
        }
    }

    companion object {
        const val VERSION = "0.110.0"
        const val LIBRARY = "outflare-android"
        private const val MAX_BATCH = 20
        private val ID = Regex("^[A-Za-z0-9-]{8,64}$")
    }
}

/** What the phone says about itself, read by reflection so the library needs no Android SDK to build. */
internal class Phone(val manufacturer: String?, val model: String?, val osName: String, val osVersion: String?) {
    val userAgent: String
        get() = if (osName == "Android") {
            "${Outflare.LIBRARY}/${Outflare.VERSION} (Linux; Android ${osVersion ?: ""}; ${model ?: "Phone"}) Mobile"
        } else {
            "${Outflare.LIBRARY}/${Outflare.VERSION}"
        }

    companion object {
        fun read(): Phone = try {
            val build = Class.forName("android.os.Build")
            val release = Class.forName("android.os.Build\$VERSION").getField("RELEASE").get(null) as? String
            Phone(build.getField("MANUFACTURER").get(null) as? String, build.getField("MODEL").get(null) as? String, "Android", release)
        } catch (e: Throwable) {
            Phone(null, null, System.getProperty("os.name") ?: "JVM", System.getProperty("os.version"))
        }
    }
}

/** A small JSON writer, so the library depends on nothing. */
internal object Json {
    fun string(s: String): String {
        val b = StringBuilder(s.length + 2).append('"')
        for (ch in s) {
            when {
                ch == '"' -> b.append("\\\"")
                ch == '\\' -> b.append("\\\\")
                ch == '\n' -> b.append("\\n")
                ch == '\r' -> b.append("\\r")
                ch == '\t' -> b.append("\\t")
                ch < ' ' || ch == ' ' || ch == ' ' -> b.append(String.format("\\u%04x", ch.code))
                else -> b.append(ch)
            }
        }
        return b.append('"').toString()
    }

    fun encode(v: Any?): String = when (v) {
        null -> "null"
        is String -> string(v)
        is Boolean -> v.toString()
        is Double -> if (v.isFinite()) v.toString() else "null"
        is Float -> if (v.isFinite()) v.toString() else "null"
        is Number -> v.toString()
        is Map<*, *> -> v.entries.joinToString(",", "{", "}") { "${string(it.key.toString())}:${encode(it.value)}" }
        is Iterable<*> -> v.joinToString(",", "[", "]") { encode(it) }
        is Array<*> -> v.joinToString(",", "[", "]") { encode(it) }
        else -> string(v.toString())
    }
}
