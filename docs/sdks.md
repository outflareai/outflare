# SDKs for sending events

Five libraries send events from your servers and four from your apps ([below](#apps)).

## Servers

Five libraries send [product events](customers.md#product-events) from your servers. Each has the same six calls (`identify`, `track`, `page`, `screen`, `group`, `alias`), queues events in memory, sends them in batches to `POST /v1/brands/{brand}/events`, retries a server that is busy and gives every event a message id so a retry is stored once.

| Language | Where | Install |
| --- | --- | --- |
| Node, Bun, Deno | [`sdk/ts`](../sdk/ts/README.md) (`Tracker` in `@outflare/sdk`) | `npm install @outflare/sdk` |
| Python | [`sdk/python`](../sdk/python/README.md) (`Tracker` in `outflare`) | `pip install outflare` |
| Go | [`sdk/go`](../sdk/go/README.md) | `go get github.com/outflareai/outflare/sdk/go` |
| Ruby | [`sdk/ruby`](../sdk/ruby/README.md) | `gem "outflare"` |
| PHP | [`sdk/php`](../sdk/php/README.md) | `composer require outflare/outflare` |

Go installs from the repository as it is. The others are published with each release once their registry is switched on ([Releasing](releasing.md#libraries)); until a package is on its registry, install it from the repository.

- **The token.** An API token with `customers.manage`, made for one brand where you can. It can change customer records, so it stays on your servers; browsers use the [website script](website-tracking.md) and its public site key.
- **Defaults.** Send once 20 events wait or every 10 seconds; at most 500 events a batch; 10,000 waiting, after which the oldest are dropped; three more tries with a growing wait when the server gives no answer, a 408, a 429 or a 5xx. A 4xx refusal is reported and not retried.
- **Before exit.** Call `close` (Go: `Close`, PHP: nothing, it sends when the request ends) so events still waiting are sent.
- **Source.** Each library names itself as the events' `source` (`node`, `python`, `go`, `ruby`, `php`), which the Events page counts and filters by.

Any library that speaks Segment's HTTP format can send to the same route too.

## Apps

Four libraries send the screens people see and what they do in a mobile app. Each has `screen(name)`, `track(event, properties)`, `anonymousId`, `reset` and `flush`, and sends to `POST /v1/site/events`, the route the [website script](website-tracking.md) uses.

| App | Where | Made of |
| --- | --- | --- |
| iOS, iPadOS, macOS, tvOS, watchOS | [`sdk/swift`](../sdk/swift/README.md) | Swift, Foundation only |
| Android | [`sdk/android`](../sdk/android/README.md) | Kotlin, no dependencies |
| React Native, Expo | [`sdk/react-native`](../sdk/react-native/README.md) | TypeScript, no native code |
| Flutter | [`sdk/flutter`](../sdk/flutter/README.md) | Dart, no plugins |

The Swift package installs from the repository: in Xcode, File, Add Package Dependencies, then `https://github.com/outflareai/outflare`. The other three are published to Maven Central, npm and pub.dev with each release once their registry is switched on ([Releasing](releasing.md#libraries)); until then, take them from the repository.

- **The key.** An app can be taken apart, so it never holds an API token. It sends with a **site key that has no origins**: make one under Website tracking for each app and leave its origins empty. An app sends no `Origin` header, so a key that lists websites refuses it. Like every site key it only adds events and reads nothing.
- **Who the person is.** A site key can't identify anyone. After sign-in, the app gives its `anonymousId` to your own server, which calls `identify` with your user's id and that `anonymousId` from one of the server libraries. The person's earlier screens and events then join their record. Call `reset` on sign-out so the next person on the phone starts fresh.
- **What is stored.** A screen is a `screen.viewed` event with the screen's name; a track call is the event you named (plain names such as Order Started). Each carries `context.app` (name, version, build), `context.device` (maker, model), `context.os` and `context.library`, and joins a visit as a website's events do, so sessions, devices and countries work the same. The visit's browser is `app`.
- **Sending.** Events wait on the phone, in a file, and go 20 at a time: once 20 wait, every 30 seconds, and when the app goes to the background (iOS and React Native; call `flush` yourself on Android and Flutter). With no connection the newest 1,000 wait until there is one. Each event has a message id, so one sent twice is stored once. A site key's daily limits apply.
- **Time.** An event is dated when it reaches Outflare, never by the phone's clock, so events sent after a long time offline are dated late.
- **Trust.** Anyone can send with a public key, so an app's events never count as product usage or money, as with a website's. Send orders and payments from your server.

## Versions

Every library has the version of the Outflare release it came with, and says it in each event's `context.library`. A newer library works with an older server as long as the server has the routes above.
