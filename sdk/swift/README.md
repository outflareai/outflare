# Outflare for iOS

Send the screens people see and what they do in your iPhone, iPad, Mac, Apple TV or Apple Watch app to [Outflare](https://outflare.ai). It uses only Foundation.

In Xcode, choose File, Add Package Dependencies, enter `https://github.com/outflareai/outflare` and pick a version. In a `Package.swift`:

```swift
.package(url: "https://github.com/outflareai/outflare", from: "0.110.0")
```

The package's manifest is [`Package.swift`](../../Package.swift) at the top of the repository; its code is this folder. To run its tests, run `swift test` there.

```swift
import Outflare

let outflare = Outflare(Outflare.Config(key: "ofk_test_not_a_real_key"))

outflare.screen("Checkout")
outflare.track("Order Started", properties: ["items": 2])
```

The key is a **site key with no origins**, made under Website tracking in Outflare. It is public: it can only add screens and events, never read anything, so it is safe inside an app. Set `config.host` for a self-hosted server.

- **Who the person is.** An app can't say who someone is: anyone can read its key. After sign-in, send `outflare.anonymousId` to your own server and have it call `identify` with your user's id and that `anonymousId` (any of the [server libraries](../../docs/sdks.md)). The person's earlier screens and events then join their record. Call `outflare.reset()` when someone signs out.
- **Sending.** Events wait in a file, so they survive the app being closed, and go 20 at a time: once 20 wait, every 30 seconds, and when the app goes to the background. With no connection they wait (the newest 1,000) and go when there is one. `flush()` sends now; `config.onError` hears about events that were given up on.
- **What is sent with each event.** The app's name, version and build (from Info.plist), the device's model, the system and its version, the language and the time zone. Never the device's name or an advertising id.
- **Time.** An event is dated when it reaches Outflare, not by the phone's clock.

## Develop

```sh
swift test
```

## License

Apache-2.0.
