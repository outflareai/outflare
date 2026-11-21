#!/usr/bin/env node
// Copies rrweb's record bundle into the engine, where the website script
// loads it for visits that are recorded (engine/internal/api/replay.go).
// Run after changing the @rrweb/record version in web/package.json:
//   pnpm install && node scripts/vendor-rrweb.mjs
import { readFileSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const require = createRequire(join(root, "web", "package.json"));
const dir = dirname(dirname(require.resolve("@rrweb/record")));
const { version } = JSON.parse(readFileSync(join(dir, "package.json"), "utf8"));
const bundle = readFileSync(join(dir, "dist", "record.umd.min.cjs"), "utf8").replace(/\n\/\/# sourceMappingURL=record\.umd\.min\.cjs\.map\s*$/, "\n");
const header = `/*! rrweb record ${version} (https://github.com/rrweb-io/rrweb), MIT licence.
 * Copyright (c) 2018 Contributors (https://github.com/rrweb-io/rrweb/graphs/contributors).
 * The recorder Outflare's website script loads for visits that are recorded
 * (replay.go). Unchanged from the package's dist/record.umd.min.cjs; to
 * update it, run node scripts/vendor-rrweb.mjs after bumping rrweb in web/. */
`;
writeFileSync(join(root, "engine", "internal", "api", "replay_record.js"), header + bundle);
console.log(`vendored @rrweb/record ${version}`);
