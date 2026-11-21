import Foundation
import XCTest
@testable import Outflare

final class Fake: @unchecked Sendable {
    var answers: [Int]
    var calls: [(url: URL, userAgent: String, body: [String: Any])] = []

    init(_ answers: [Int] = []) {
        self.answers = answers
    }

    func post(_ url: URL, _ userAgent: String, _ data: Data) -> Int {
        let body = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] ?? [:]
        calls.append((url, userAgent, body))
        return answers.isEmpty ? 200 : answers.removeFirst()
    }

    func batch(_ call: Int) -> [[String: Any]] {
        calls[call].body["batch"] as? [[String: Any]] ?? []
    }
}

final class OutflareTests: XCTestCase {
    private func directory() -> URL {
        FileManager.default.temporaryDirectory.appendingPathComponent("outflare-test-\(UUID().uuidString)", isDirectory: true)
    }

    private func make(_ fake: Fake, _ directory: URL, onError: ((String, Int) -> Void)? = nil) -> Outflare {
        var config = Outflare.Config(key: "ofk_test_not_a_real_key")
        config.host = "https://outflare.example/"
        config.directory = directory
        config.app = Outflare.App(name: "Northpeak", version: "2.4.0", build: "311")
        config.flushInterval = 600
        config.transport = fake.post
        config.onError = onError
        return Outflare(config)
    }

    // Everything runs in order on the library's own queue, so waiting for a
    // flush waits for all that was asked before it.
    private func flush(_ o: Outflare) {
        let done = DispatchSemaphore(value: 0)
        o.flush { done.signal() }
        done.wait()
    }

    func testScreensAndEventsGoToTheSiteRouteWithTheKeyAndOneVisitorId() {
        let fake = Fake()
        let o = make(fake, directory())
        o.screen("Checkout")
        o.track("Order Started", properties: ["items": 2, "gift": true])
        flush(o)
        XCTAssertEqual(fake.calls.count, 1)
        XCTAssertEqual(fake.calls[0].url.absoluteString, "https://outflare.example/v1/site/events")
        XCTAssertTrue(fake.calls[0].userAgent.hasPrefix("outflare-swift/\(Outflare.version)"))
        XCTAssertEqual(fake.calls[0].body["key"] as? String, "ofk_test_not_a_real_key")
        let batch = fake.batch(0)
        XCTAssertEqual(batch.map { $0["type"] as? String }, ["screen", "track"])
        XCTAssertEqual(batch[0]["name"] as? String, "Checkout")
        XCTAssertEqual(batch[1]["event"] as? String, "Order Started")
        XCTAssertEqual((batch[1]["properties"] as? [String: Any])?["items"] as? Int, 2)
        XCTAssertEqual(batch[0]["anonymousId"] as? String, o.anonymousId)
        XCTAssertEqual(batch[1]["anonymousId"] as? String, o.anonymousId)
        XCTAssertNotEqual(batch[0]["messageId"] as? String, batch[1]["messageId"] as? String)
        let context = batch[0]["context"] as? [String: Any]
        XCTAssertEqual((context?["app"] as? [String: String])?["version"], "2.4.0")
        XCTAssertEqual((context?["library"] as? [String: String])?["name"], "outflare-swift")
    }

    func testAtMost20EventsGoInOneRequest() {
        let fake = Fake()
        let o = make(fake, directory())
        for _ in 0..<45 {
            o.track("Tapped")
        }
        flush(o)
        XCTAssertEqual((0..<fake.calls.count).map { fake.batch($0).count }, [20, 20, 5])
    }

    func testEventsWaitThroughNoAnswerAndABusyServerAndAreDroppedOnARefusal() {
        let fake = Fake([0, 503, 200, 401])
        var lost: [Int] = []
        let o = make(fake, directory()) { _, n in lost.append(n) }
        o.track("Kept")
        flush(o)
        flush(o)
        XCTAssertEqual(fake.calls.count, 2)
        flush(o)
        XCTAssertEqual(fake.calls.count, 3)
        // The same message id each time, so a retry is stored once.
        XCTAssertEqual(fake.batch(0)[0]["messageId"] as? String, fake.batch(2)[0]["messageId"] as? String)
        flush(o)
        XCTAssertEqual(fake.calls.count, 3)
        o.track("Refused")
        flush(o)
        XCTAssertEqual(lost, [1])
        flush(o)
        XCTAssertEqual(fake.calls.count, 4)
    }

    func testTheVisitorIdAndWaitingEventsSurviveARestartAndResetStartsANewVisitor() throws {
        let dir = directory()
        let first = make(Fake([0]), dir)
        let id = first.anonymousId
        first.screen("Home")
        flush(first)
        let kept = try JSONSerialization.jsonObject(with: Data(contentsOf: dir.appendingPathComponent("outflare-queue.json"))) as? [Any]
        XCTAssertEqual(kept?.count, 1)

        let up = Fake()
        let second = make(up, dir)
        XCTAssertEqual(second.anonymousId, id)
        flush(second)
        XCTAssertEqual(up.batch(0)[0]["name"] as? String, "Home")
        XCTAssertFalse(FileManager.default.fileExists(atPath: dir.appendingPathComponent("outflare-queue.json").path))

        let done = DispatchSemaphore(value: 0)
        second.reset { done.signal() }
        done.wait()
        XCTAssertNotEqual(second.anonymousId, id)
        XCTAssertEqual(try String(contentsOf: dir.appendingPathComponent("outflare-id"), encoding: .utf8), second.anonymousId)
    }

    func testTheUserAgentNamesTheDeviceTheWayOutflareReadsIt() {
        let phone = Device(manufacturer: "Apple", model: "iPhone16,2", type: "ios", osName: "iOS", osVersion: "18.1")
        XCTAssertEqual(phone.userAgent, "outflare-swift/\(Outflare.version) (iPhone; CPU iPhone OS 18_1 like Mac OS X)")
        let pad = Device(manufacturer: "Apple", model: "iPad14,1", type: "ios", osName: "iPadOS", osVersion: "18.1")
        XCTAssertEqual(pad.userAgent, "outflare-swift/\(Outflare.version) (iPad; CPU OS 18_1 like Mac OS X)")
    }
}
