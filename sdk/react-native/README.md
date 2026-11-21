# Outflare for React Native

Send the screens people see and what they do in your React Native app to [Outflare](https://outflare.ai). It is plain TypeScript with no native code, so it needs no linking and works with Expo. It needs React Native 0.72 or later.

```sh
npm install @outflare/react-native @react-native-async-storage/async-storage
```

```ts
import AsyncStorage from "@react-native-async-storage/async-storage";
import { createOutflare } from "@outflare/react-native";

export const outflare = createOutflare({
  key: "ofk_test_not_a_real_key",
  storage: AsyncStorage,
  app: { name: "Northpeak", version: "2.4.0" },
});

outflare.screen("Checkout");
outflare.track("Order Started", { items: 2 });
```

The key is a **site key with no origins**, made under Website tracking in Outflare. It is public: it can only add screens and events, never read anything, so it is safe inside an app. Set `host` for a self-hosted server.

- **Who the person is.** An app can't say who someone is: anyone can read its key. After sign-in, send `await outflare.anonymousId()` to your own server and have it call `identify` with your user's id and that `anonymousId` (any of the [server libraries](../../docs/sdks.md)). The person's earlier screens and events then join their record. Call `outflare.reset()` when someone signs out.
- **Sending.** Events wait in `storage`, so they survive the app being closed, and go 20 at a time: once 20 wait, every 30 seconds, and when the app goes to the background. With no connection they wait (the newest 1,000) and go when there is one. `flush()` sends now; `onError` hears about events that were given up on. Without `storage`, the visitor id and waiting events last only until the app closes.
- **Screens with React Navigation.** Call `outflare.screen(name)` from the navigation container's `onStateChange` with the current route's name.
- **What is sent with each event.** The app's name, version and build as you gave them, the phone's model and system, the language and the time zone. Never an advertising id.
- **Time.** An event is dated when it reaches Outflare, not by the phone's clock.

`@outflare/react-native/core` is the same client without React Native, for tests or another JavaScript runtime.

## Develop

```sh
pnpm --filter @outflare/react-native build && pnpm --filter @outflare/react-native test
```

## License

Apache-2.0.
