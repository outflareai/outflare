# Outflare for Android

Send the screens people see and what they do in your Android app to [Outflare](https://outflare.ai). It is plain Kotlin with no dependencies, so it also runs on any JVM.

```kotlin
dependencies {
    implementation("ai.outflare:outflare-android:0.110.0")
}
```

```kotlin
import ai.outflare.App
import ai.outflare.Config
import ai.outflare.Outflare

// In Application.onCreate: make one and share it.
val outflare = Outflare(
    Config(
        key = "ofk_test_not_a_real_key",
        directory = filesDir,
        app = App(name = "Northpeak", version = BuildConfig.VERSION_NAME, build = BuildConfig.VERSION_CODE.toString()),
    ),
)

outflare.screen("Checkout")
outflare.track("Order Started", mapOf("items" to 2))
```

The app needs the `android.permission.INTERNET` permission. The key is a **site key with no origins**, made under Website tracking in Outflare. It is public: it can only add screens and events, never read anything, so it is safe inside an app. Set `host` for a self-hosted server.

- **Who the person is.** An app can't say who someone is: anyone can read its key. After sign-in, send `outflare.anonymousId()` to your own server (call it off the main thread) and have it call `identify` with your user's id and that `anonymousId` (any of the [server libraries](../../docs/sdks.md)). The person's earlier screens and events then join their record. Call `outflare.reset()` when someone signs out.
- **Sending.** Events wait in a file under `directory`, so they survive the app being closed, and go 20 at a time: once 20 wait, or every 30 seconds, on a background thread. With no connection they wait (the newest 1,000) and go when there is one. `flush()` sends now (call it in `onStop`); `onError` hears about events that were given up on.
- **What is sent with each event.** The app's name, version and build as you gave them, the phone's maker and model, Android's version, the language and the time zone. Never an advertising id.
- **Time.** An event is dated when it reaches Outflare, not by the phone's clock.

## Develop

```sh
gradle test
```

## License

Apache-2.0.
