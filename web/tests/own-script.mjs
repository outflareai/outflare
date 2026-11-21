import test from 'node:test';
import assert from 'node:assert/strict';
import { ownScript } from '../src/lib/own-script.ts';

const origin = 'https://app.example.com';

test('a crash in the app\'s own script is the app\'s', () => {
  assert.equal(ownScript(`TypeError: x is undefined\n    at render (${origin}/assets/index-abc.js:1:200)\n    at chrome-extension://abcdef/inject.js:2:1`, origin), true);
});

test('a crash with no stack, or no script named, could be the app\'s', () => {
  assert.equal(ownScript(undefined, origin), true);
  assert.equal(ownScript('TypeError: x is undefined\n    at <anonymous>:1:5', origin), true);
});

test('a crash only in an extension or another site\'s script is not', () => {
  assert.equal(ownScript("TypeError: Cannot read properties of undefined\n    at t (chrome-extension://abcdef/content.js:1:99)", origin), false);
  assert.equal(ownScript('f@moz-extension://abcdef/inject.js:3:7', origin), false);
  assert.equal(ownScript('Error: boom\n    at https://cdn.example.net/widget.js:1:1', origin), false);
  // Another site whose address starts with ours is still another site.
  assert.equal(ownScript('Error: boom\n    at https://app.example.com.evil.example/x.js:1:1', origin), false);
});
