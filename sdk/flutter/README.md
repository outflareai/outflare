# Outflare for Flutter

Send the screens people see and what they do in your Flutter app to [Outflare](https://outflare.ai). It is plain Dart (`dart:io`) with no plugins, for iOS, Android and desktop apps.

```yaml
dependencies:
  outflare: ^0.110.0
```

```dart
import 'package:outflare/outflare.dart';
import 'package:path_provider/path_provider.dart';

final outflare = Outflare(
  key: 'ofk_test_not_a_real_key',
  directory: (await getApplicationSupportDirectory()).path,
  appName: 'Northpeak',
  appVersion: '2.4.0',
);

outflare.screen('Checkout');
outflare.track('Order Started', {'items': 2});
```

The key is a **site key with no origins**, made under Website tracking in Outflare. It is public: it can only add screens and events, never read anything, so it is safe inside an app. Set `host` for a self-hosted server.

- **Who the person is.** An app can't say who someone is: anyone can read its key. After sign-in, send `await outflare.anonymousId` to your own server and have it call `identify` with your user's id and that `anonymousId` (any of the [server libraries](../../docs/sdks.md)). The person's earlier screens and events then join their record. Call `outflare.reset()` when someone signs out.
- **Sending.** Events wait in a file under `directory`, so they survive the app being closed, and go 20 at a time: once 20 wait, or every 30 seconds. With no connection they wait (the newest 1,000) and go when there is one. Call `outflare.flush()` when the app goes to the background (an `AppLifecycleListener`'s `onPause`); `onError` hears about events that were given up on.
- **What is sent with each event.** The app's name, version and build as you gave them, the system and the language. Dart can't read Android's version or the phone's model without a plugin: pass `osVersion`, `deviceModel` and `deviceManufacturer` (from `device_info_plus`, say) to have them in reports. Never an advertising id.
- **Time.** An event is dated when it reaches Outflare, not by the phone's clock.

## Develop

```sh
dart pub get && dart analyze && dart test
```

## License

Apache-2.0.
