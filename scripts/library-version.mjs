#!/usr/bin/env node
// Gives every library in sdk/ the version of the Outflare release it ships
// in (docs/releasing.md, "Libraries").
//
//   node scripts/library-version.mjs 0.4.0           write the version everywhere
//   node scripts/library-version.mjs --check 0.4.0   fail unless it is everywhere
//
// A release is stamped once, as it is copied to the public repository, so
// the tag people install from (Go, PHP and Swift read the tag itself) and
// the packages built from it (npm, PyPI, RubyGems, Maven Central, pub.dev)
// all say the same version. Publish libraries checks it before it builds.
//
// Each entry is a file and the one place in it that holds the version. A
// file that is listed but missing, or whose place isn't found exactly once,
// is an error: a library that moved must be moved here too.
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const V = String.raw`\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?`;

// before and after are regular expressions around the version.
export const places = [
  ["sdk/ts/package.json", String.raw`^  "version": "`, `",$`],
  ["sdk/ts/src/index.ts", String.raw`^export const SDK_VERSION = "`, `";$`],
  ["sdk/ts/src/track.ts", String.raw`^export const trackerVersion = "`, `";$`],
  ["sdk/react-native/package.json", String.raw`^  "version": "`, `",$`],
  ["sdk/react-native/src/core.ts", String.raw`^export const version = "`, `";$`],
  ["sdk/python/src/outflare/_version.py", String.raw`^__version__ = "`, `"$`],
  ["sdk/ruby/lib/outflare/version.rb", String.raw`^  VERSION = "`, `"$`],
  ["sdk/php/src/Tracker.php", String.raw`^    public const VERSION = '`, `';$`],
  ["sdk/go/outflare.go", String.raw`^const Version = "`, `"$`],
  ["sdk/swift/Sources/Outflare/Outflare.swift", String.raw`^    public static let version = "`, `"$`],
  ["sdk/swift/README.md", String.raw`^\.package\(url: "https://github\.com/outflareai/outflare", from: "`, String.raw`"\)$`],
  ["sdk/android/build.gradle.kts", String.raw`^version = "`, `"$`],
  ["sdk/android/src/main/kotlin/ai/outflare/Outflare.kt", String.raw`^        const val VERSION = "`, `"$`],
  ["sdk/android/README.md", String.raw`implementation\("ai\.outflare:outflare-android:`, String.raw`"\)`],
  ["sdk/flutter/pubspec.yaml", String.raw`^version: `, `$`],
  ["sdk/flutter/lib/outflare.dart", String.raw`^const outflareVersion = '`, `';$`],
  ["sdk/flutter/README.md", String.raw`^  outflare: \^`, `$`],
];

// stamp returns the text with the version written at the place, or throws
// when the place isn't there exactly once.
export function stamp(text, before, after, version) {
  const re = new RegExp(`(${before})${V}(${after})`, "gm");
  const found = text.match(re);
  if (!found || found.length !== 1) {
    throw new Error(`expected its version once, found it ${found ? found.length : 0} times`);
  }
  return text.replace(re, (_, a, b) => a + version + b);
}

export function run(root, version, { check = false } = {}) {
  if (!new RegExp(`^${V}$`).test(version)) {
    throw new Error(`"${version}" is not a version like 0.4.0 (leave the v off).`);
  }
  const wrong = [];
  for (const [file, before, after] of places) {
    const path = join(root, file);
    let text;
    try {
      text = readFileSync(path, "utf8");
    } catch {
      throw new Error(`${file} is missing. Update scripts/library-version.mjs if the library moved.`);
    }
    let next;
    try {
      next = stamp(text, before, after, version);
    } catch (err) {
      throw new Error(`${file}: ${err.message}.`);
    }
    if (next === text) continue;
    if (check) wrong.push(file);
    else writeFileSync(path, next);
  }
  return wrong;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const args = process.argv.slice(2);
  const check = args[0] === "--check";
  const version = (check ? args[1] : args[0]) ?? "";
  const root = join(fileURLToPath(import.meta.url), "..", "..");
  try {
    const wrong = run(root, version.replace(/^v/, ""), { check });
    if (wrong.length) {
      console.error(`These don't say ${version}:\n  ${wrong.join("\n  ")}`);
      process.exit(1);
    }
    console.log(check ? `Every library says ${version}.` : `Every library now says ${version}.`);
  } catch (err) {
    console.error(err.message);
    process.exit(1);
  }
}
