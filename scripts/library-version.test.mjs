// node --test scripts/library-version.test.mjs
import assert from "node:assert/strict";
import { cpSync, mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import { places, run, stamp } from "./library-version.mjs";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");

// A copy of only the files the script touches, so the test never changes
// the working tree.
function copy() {
  const dir = mkdtempSync(join(tmpdir(), "library-version-"));
  for (const [file] of places) cpSync(join(root, file), join(dir, file), { recursive: true });
  return dir;
}

test("every listed place is in the repository, once", () => {
  const dir = copy();
  assert.deepEqual(run(dir, "9.8.7"), []);
});

test("a stamped tree passes the check, and only for that version", () => {
  const dir = copy();
  run(dir, "9.8.7");
  assert.deepEqual(run(dir, "9.8.7", { check: true }), []);
  assert.equal(run(dir, "9.8.8", { check: true }).length, places.length);
  for (const [file] of places) {
    assert.ok(readFileSync(join(dir, file), "utf8").includes("9.8.7"), file);
  }
});

test("check changes nothing", () => {
  const dir = copy();
  const before = places.map(([file]) => readFileSync(join(dir, file), "utf8"));
  run(dir, "9.8.7", { check: true });
  assert.deepEqual(places.map(([file]) => readFileSync(join(dir, file), "utf8")), before);
});

test("a place that is missing or doubled is an error", () => {
  assert.throws(() => stamp("nothing here", "^version = \"", "\"$", "1.2.3"), /found it 0 times/);
  assert.throws(() => stamp('version = "1.0.0"\nversion = "1.0.0"', "^version = \"", "\"$", "1.2.3"), /found it 2 times/);
});

test("only versions are accepted", () => {
  assert.throws(() => run(root, "v1.2.3", { check: true }), /not a version/);
  assert.throws(() => run(root, "latest", { check: true }), /not a version/);
});

test("the libraries in the repository agree with each other", () => {
  const version = JSON.parse(readFileSync(join(root, "sdk/ts/package.json"), "utf8")).version;
  assert.deepEqual(run(root, version, { check: true }), []);
});
