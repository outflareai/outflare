// The note a tracking domain shows about its registration (Domains page):
// nothing without a date, quiet while it is far off, amber within 30 days,
// red within 7 days and once it has run out.
import test from 'node:test';
import assert from 'node:assert/strict';
import { registrationNote } from '../src/lib/registration.ts';

const now = Date.parse('2026-11-15T12:00:00Z');
const inDays = (n) => new Date(now + n * 86_400_000).toISOString();

test('no date, no note', () => {
  assert.equal(registrationNote(null, now), null);
  assert.equal(registrationNote(undefined, now), null);
  assert.equal(registrationNote('not a date', now), null);
});
test('far off is quiet and says the day', () => {
  const note = registrationNote('2027-05-09T14:00:00Z', now);
  assert.equal(note.tone, 'grey');
  assert.match(note.label, /^Registered until .*2027/);
  assert.equal(registrationNote(inDays(31), now).tone, 'grey');
});
test('within 30 days is amber, within 7 red, and counts whole days', () => {
  assert.deepEqual(registrationNote(inDays(30), now), { label: 'Registration expires in 30 days', tone: 'amber' });
  assert.deepEqual(registrationNote(inDays(7.5), now), { label: 'Registration expires in 8 days', tone: 'amber' });
  assert.deepEqual(registrationNote(inDays(7), now), { label: 'Registration expires in 7 days', tone: 'red' });
  assert.deepEqual(registrationNote(inDays(0.5), now), { label: 'Registration expires tomorrow', tone: 'red' });
});
test('run out is red and says when', () => {
  const note = registrationNote('2026-11-14T00:00:00Z', now);
  assert.equal(note.tone, 'red');
  assert.match(note.label, /^Registration expired .*2026/);
});
