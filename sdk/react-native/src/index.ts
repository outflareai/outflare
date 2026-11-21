// Outflare for React Native: screens and events from an app, sent with the
// app's public site key. See README.md.

import { AppState, Platform } from "react-native";
import { Outflare, type Options } from "./core.js";

export { Outflare, userAgent, version } from "./core.js";
export type { Options, Properties, Storage } from "./core.js";

/**
 * Make the app's connection to Outflare: it fills in the phone's system
 * and model, and sends what waits whenever the app goes to the background.
 */
export function createOutflare(options: Options): Outflare {
  const ios = Platform.OS === "ios";
  const c = Platform.constants ?? {};
  let timezone: string | undefined;
  let locale: string | undefined;
  try {
    const o = Intl.DateTimeFormat().resolvedOptions();
    timezone = o.timeZone;
    locale = o.locale;
  } catch {
    // Some engines ship without Intl.
  }
  const outflare = new Outflare({
    os: { name: ios ? (Platform.isPad ? "iPadOS" : "iOS") : Platform.OS === "android" ? "Android" : Platform.OS, version: String(Platform.Version) },
    device: { manufacturer: ios ? "Apple" : (c.Manufacturer ?? c.Brand), model: ios ? (Platform.isPad ? "iPad" : "iPhone") : c.Model, type: Platform.OS },
    timezone,
    locale,
    ...options,
  });
  AppState.addEventListener("change", (state) => {
    if (state !== "active") void outflare.flush();
  });
  return outflare;
}
