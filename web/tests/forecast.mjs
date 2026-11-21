import test from 'node:test';
import assert from 'node:assert/strict';
import { forecast, horizonFor } from '../src/lib/forecast.ts';

test('a steady rise keeps rising, and levels off rather than shooting up', () => {
  const values = Array.from({ length: 20 }, (_, i) => 10 + 2 * i);
  const f = forecast(values, 5);
  assert.ok(f);
  assert.equal(f.mean.length, 5);
  assert.ok(f.mean[0] > values[values.length - 1]);
  assert.ok(f.mean[4] > f.mean[0]);
  assert.ok(f.mean[4] - f.mean[3] < f.mean[1] - f.mean[0], 'damped');
});

test('the band holds the forecast and widens with each step', () => {
  const values = [5, 7, 4, 6, 8, 5, 7, 6, 9, 5, 7, 8, 6, 7];
  const f = forecast(values, 4);
  for (let i = 0; i < 4; i++) assert.ok(f.lo[i] <= f.mean[i] && f.mean[i] <= f.hi[i]);
  assert.ok(f.hi[3] - f.lo[3] >= f.hi[0] - f.lo[0]);
});

test('counts never go below zero', () => {
  const values = [9, 8, 7, 6, 5, 4, 3, 2, 1, 1];
  const f = forecast(values, 6);
  assert.ok(f.mean.every((v) => v >= 0) && f.lo.every((v) => v >= 0));
});

test('a weekly pattern carries on', () => {
  const week = [2, 10, 10, 10, 10, 10, 2];
  const values = [...week, ...week, ...week, ...week];
  const f = forecast(values, 7, 7);
  assert.ok(f.mean[0] < f.mean[2], 'the quiet day stays quiet');
});

test('too few numbers give no forecast', () => {
  assert.equal(forecast([1, 2, 3], 3), null);
  assert.equal(horizonFor('day', 30), 8);
  assert.equal(horizonFor('month', 3), 2);
});
