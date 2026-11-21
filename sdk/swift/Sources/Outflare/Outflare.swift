// Outflare for iOS and the other Apple systems: screens and events from an
// app, sent with the app's public site key to POST /v1/site/events. The
// visitor's id and the events still waiting are kept in files, so they
// survive the app being closed. It uses only Foundation. See README.md.

import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif
#if canImport(UIKit) && !os(watchOS)
import UIKit
#endif

/// One app's connection to Outflare. Make one when the app starts and share it.
public final class Outflare: @unchecked Sendable {
    public static let version = "0.110.0"
    static let library = "outflare-swift"
    static let maxBatch = 20

    /// The app, for reports by version. Left out, they are read from the app's Info.plist.
    public struct App {
        public var name: String?
        public var version: String?
        public var build: String?

        public init(name: String? = nil, version: String? = nil, build: String? = nil) {
            self.name = name
            self.version = version
            self.build = build
        }
    }

    public struct Config {
        /// The app's public site key (ofk_...), from a site key made with no origins.
        public var key: String
        /// Your Outflare server.
        public var host = "https://app.outflare.ai"
        /// Where the visitor id and waiting events are kept. Default: the app's Application Support folder.
        public var directory: URL?
        public var app: App?
        /// Send once this many events wait: at most 20, the most one request takes.
        public var flushAt = Outflare.maxBatch
        /// Send at least this often, in seconds.
        public var flushInterval: TimeInterval = 30
        /// The most events kept waiting; the oldest are dropped past it.
        public var maxQueue = 1000
        /// Called when events are given up on, with how many.
        public var onError: ((String, Int) -> Void)?
        /// How a request is sent: replaced in tests. It returns the HTTP status, or 0 for no answer.
        public var transport: ((URL, String, Data) -> Int)?

        public init(key: String) {
            self.key = key
        }
    }

    private let config: Config
    private let url: URL
    private let idFile: URL
    private let queueFile: URL
    private let flushAt: Int
    private let context: [String: Any]
    private let work = DispatchQueue(label: "ai.outflare.events")

    /// The User-Agent requests carry: the library and the device, the way Outflare reads them.
    public let userAgent: String

    // Only the work queue touches these.
    private var id = ""
    private var queue: [[String: Any]] = []
    private var timer: DispatchWorkItem?

    public init(_ config: Config) {
        precondition(!config.key.isEmpty, "Outflare: key is required")
        self.config = config
        var host = config.host
        while host.hasSuffix("/") {
            host.removeLast()
        }
        url = URL(string: host + "/v1/site/events")!
        let directory = config.directory
            ?? (FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first ?? FileManager.default.temporaryDirectory)
                .appendingPathComponent("outflare", isDirectory: true)
        idFile = directory.appendingPathComponent("outflare-id")
        queueFile = directory.appendingPathComponent("outflare-queue.json")
        flushAt = min(max(config.flushAt, 1), Outflare.maxBatch)

        let device = Device.read()
        userAgent = device.userAgent
        let info = Bundle.main.infoDictionary ?? [:]
        let app = App(
            name: config.app?.name ?? info["CFBundleName"] as? String,
            version: config.app?.version ?? info["CFBundleShortVersionString"] as? String,
            build: config.app?.build ?? info["CFBundleVersion"] as? String
        )
        var context: [String: Any] = [
            "library": ["name": Outflare.library, "version": Outflare.version],
            "locale": Locale.current.identifier.replacingOccurrences(of: "_", with: "-"),
            "timezone": TimeZone.current.identifier,
        ]
        func some(_ fields: [String: String?]) -> [String: String]? {
            let set = fields.compactMapValues { $0 }.filter { !$0.value.isEmpty }
            return set.isEmpty ? nil : set
        }
        if let a = some(["name": app.name, "version": app.version, "build": app.build]) {
            context["app"] = a
        }
        if let d = some(["manufacturer": device.manufacturer, "model": device.model, "type": device.type]) {
            context["device"] = d
        }
        if let o = some(["name": device.osName, "version": device.osVersion]) {
            context["os"] = o
        }
        self.context = context

        work.async { self.load(directory) }
        #if canImport(UIKit) && !os(watchOS)
        // Send what waits when the app goes to the background.
        NotificationCenter.default.addObserver(forName: UIApplication.didEnterBackgroundNotification, object: nil, queue: nil) { [weak self] _ in
            self?.flush()
        }
        #endif
    }

    /// The visitor's id on this device. Send it to your server with your user's id to join the two.
    /// It waits for storage, so call it off the main thread.
    public var anonymousId: String {
        work.sync { id }
    }

    /// A screen the person saw.
    public func screen(_ name: String, properties: [String: Any] = [:]) {
        precondition(!name.isEmpty, "Outflare: screen needs a name")
        add(["type": "screen", "name": name], properties)
    }

    /// Something the person did, such as Order Started.
    public func track(_ event: String, properties: [String: Any] = [:]) {
        precondition(!event.isEmpty, "Outflare: track needs an event name")
        add(["type": "track", "event": event], properties)
    }

    /// Forget the visitor: call it when someone signs out, so the next person starts fresh.
    public func reset(completion: (() -> Void)? = nil) {
        work.async {
            self.drain()
            self.id = UUID().uuidString.lowercased()
            try? self.id.write(to: self.idFile, atomically: true, encoding: .utf8)
            completion?()
        }
    }

    /// Send everything waiting now. The completion runs once it has been sent or kept for later.
    public func flush(completion: (() -> Void)? = nil) {
        work.async {
            self.drain()
            completion?()
        }
    }

    private func add(_ event: [String: Any], _ properties: [String: Any]) {
        var event = event
        // Properties JSON can't hold are left out rather than losing the event.
        event["properties"] = JSONSerialization.isValidJSONObject(properties) ? properties : [String: Any]()
        event["messageId"] = UUID().uuidString.lowercased()
        event["context"] = context
        work.async {
            self.queue.append(event)
            if self.queue.count > self.config.maxQueue {
                let lost = self.queue.count - self.config.maxQueue
                self.queue.removeFirst(lost)
                self.config.onError?("Outflare: too many events are waiting, so the oldest were dropped", lost)
            }
            self.save()
            if self.queue.count >= self.flushAt {
                self.drain()
            } else if self.timer == nil {
                self.schedule()
            }
        }
    }

    private func schedule() {
        let item = DispatchWorkItem { [weak self] in
            self?.timer = nil
            self?.drain()
        }
        timer = item
        work.asyncAfter(deadline: .now() + config.flushInterval, execute: item)
    }

    private func load(_ directory: URL) {
        try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        if let kept = try? String(contentsOf: idFile, encoding: .utf8) {
            id = kept.trimmingCharacters(in: .whitespacesAndNewlines)
        }
        if let data = try? Data(contentsOf: queueFile), let kept = (try? JSONSerialization.jsonObject(with: data)) as? [[String: Any]] {
            // Events from the last launch go first.
            queue = kept + queue
        }
        let allowed = CharacterSet(charactersIn: "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-")
        if id.count < 8 || id.count > 64 || id.unicodeScalars.contains(where: { !allowed.contains($0) }) {
            id = UUID().uuidString.lowercased()
            try? id.write(to: idFile, atomically: true, encoding: .utf8)
        }
    }

    private func save() {
        if queue.isEmpty {
            try? FileManager.default.removeItem(at: queueFile)
        } else if let data = try? JSONSerialization.data(withJSONObject: queue) {
            try? data.write(to: queueFile, options: .atomic)
        }
    }

    private func drain() {
        timer?.cancel()
        timer = nil
        while !queue.isEmpty {
            let batch = Array(queue.prefix(Outflare.maxBatch))
            let events: [[String: Any]] = batch.map { event in
                var event = event
                event["anonymousId"] = id
                return event
            }
            guard let body = try? JSONSerialization.data(withJSONObject: ["key": config.key, "batch": events] as [String: Any]) else {
                queue.removeFirst(batch.count)
                continue
            }
            let status = (config.transport ?? Outflare.post)(url, userAgent, body)
            // No answer, or a busy server: keep the events for the next try.
            if status == 0 || status == 408 || status == 429 || status >= 500 {
                break
            }
            queue.removeFirst(batch.count)
            if status < 200 || status > 299 {
                config.onError?("Outflare: the server refused the events (\(status)); check the site key", batch.count)
            }
        }
        save()
        if !queue.isEmpty && timer == nil {
            schedule()
        }
    }

    private static func post(_ url: URL, _ userAgent: String, _ body: Data) -> Int {
        var request = URLRequest(url: url, timeoutInterval: 15)
        request.httpMethod = "POST"
        request.setValue("text/plain;charset=UTF-8", forHTTPHeaderField: "Content-Type")
        request.setValue(userAgent, forHTTPHeaderField: "User-Agent")
        request.httpBody = body
        let done = DispatchSemaphore(value: 0)
        let status = StatusBox()
        URLSession.shared.dataTask(with: request) { _, response, _ in
            status.value = (response as? HTTPURLResponse)?.statusCode ?? 0
            done.signal()
        }.resume()
        done.wait()
        return status.value
    }
}

private final class StatusBox: @unchecked Sendable {
    var value = 0
}

/// What the device says about itself.
struct Device {
    var manufacturer: String?
    var model: String?
    var type: String
    var osName: String
    var osVersion: String

    var userAgent: String {
        let base = "\(Outflare.library)/\(Outflare.version)"
        let v = osVersion.replacingOccurrences(of: ".", with: "_")
        switch osName {
        case "iOS":
            return "\(base) (iPhone; CPU iPhone OS \(v) like Mac OS X)"
        case "iPadOS":
            return "\(base) (iPad; CPU OS \(v) like Mac OS X)"
        case "macOS":
            return "\(base) (Macintosh; Intel Mac OS X \(v))"
        default:
            return base
        }
    }

    static func read() -> Device {
        var system = utsname()
        uname(&system)
        let machine = withUnsafePointer(to: &system.machine) {
            $0.withMemoryRebound(to: CChar.self, capacity: 1) { String(cString: $0) }
        }
        let v = ProcessInfo.processInfo.operatingSystemVersion
        let version = "\(v.majorVersion).\(v.minorVersion)"
        #if os(iOS)
        return Device(manufacturer: "Apple", model: machine, type: "ios", osName: machine.hasPrefix("iPad") ? "iPadOS" : "iOS", osVersion: version)
        #elseif os(tvOS)
        return Device(manufacturer: "Apple", model: machine, type: "tvos", osName: "tvOS", osVersion: version)
        #elseif os(watchOS)
        return Device(manufacturer: "Apple", model: machine, type: "watchos", osName: "watchOS", osVersion: version)
        #elseif os(macOS)
        return Device(manufacturer: "Apple", model: machine, type: "macos", osName: "macOS", osVersion: version)
        #else
        return Device(manufacturer: nil, model: machine, type: "other", osName: "Linux", osVersion: version)
        #endif
    }
}
