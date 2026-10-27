#!/usr/bin/env node
// Sends website events to a server the way the snippet does, as fast as it
// can for a while, and prints how many requests it answered, how fast, and
// the errors. Use it on your own test install to find where collection
// slows down (docs/scaling.md), never on someone else's server.
//
//   node scripts/load-site-events.mjs <base URL> <site key> [seconds] [concurrency] [origin]
//   node scripts/load-site-events.mjs http://localhost:8080 ofk_... 30 50 https://acme.example
//
// The key's daily limit (250,000 events) applies, so a long run on one key
// starts getting 429s; that is the limit working, not the server failing.

const [base, key, seconds = '30', concurrency = '20', origin = 'https://acme.example'] = process.argv.slice(2);
if (!base || !key) {
  console.error('usage: node scripts/load-site-events.mjs <base URL> <site key> [seconds] [concurrency] [origin]');
  process.exit(2);
}

const url = base.replace(/\/$/, '') + '/v1/site/events';
const until = Date.now() + Number(seconds) * 1000;
const codes = new Map();
const times = [];
let failed = 0;
let events = 0;

function batch() {
  const anon = 'load-' + Math.random().toString(36).slice(2, 12);
  const page = { url: origin + '/pricing?utm_source=load', path: '/pricing', referrer: '' };
  return [
    { type: 'page', anonymousId: anon, context: { page } },
    { type: 'track', event: 'Load Test', anonymousId: anon, properties: { step: 1 }, context: { page } },
  ];
}

async function worker() {
  while (Date.now() < until) {
    const body = JSON.stringify({ key, batch: batch() });
    const start = performance.now();
    try {
      const res = await fetch(url, { method: 'POST', body, headers: { 'Content-Type': 'text/plain;charset=UTF-8', Origin: origin } });
      await res.arrayBuffer();
      codes.set(res.status, (codes.get(res.status) || 0) + 1);
      if (res.status === 200) events += 2;
    } catch {
      failed++;
    }
    times.push(performance.now() - start);
  }
}

await Promise.all(Array.from({ length: Number(concurrency) }, worker));
times.sort((a, b) => a - b);
const pct = (p) => (times.length ? times[Math.min(times.length - 1, Math.floor((p / 100) * times.length))].toFixed(1) : '-');
console.log(`requests: ${times.length} (${(times.length / Number(seconds)).toFixed(0)}/s), events taken: ${events}`);
console.log(`status: ${[...codes].map(([c, n]) => `${c}×${n}`).join(' ')}${failed ? `, network errors: ${failed}` : ''}`);
console.log(`latency ms: p50 ${pct(50)}, p95 ${pct(95)}, p99 ${pct(99)}`);
