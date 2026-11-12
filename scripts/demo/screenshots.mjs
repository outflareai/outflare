#!/usr/bin/env node
// Takes the outflare.ai homepage screenshots from the local Northpeak demo
// workspace (README.md), in light and dark, at 2x: 2880x1520 PNGs.
//
//   node scripts/demo/screenshots.mjs SEQUENCE_ID REPLY_ID [OUT_DIR]
//
// Needs Playwright with Chromium. The website stores them as WebP in
// public/app/<id>-<theme>.webp.

import { chromium } from 'playwright';

const [seq, reply, out = 'screenshots'] = process.argv.slice(2);
if (!seq || !reply) {
	console.error('usage: node scripts/demo/screenshots.mjs SEQUENCE_ID REPLY_ID [OUT_DIR]');
	process.exit(2);
}
const base = process.env.OUTFLARE_URL ?? 'http://localhost:8080';
const email = 'maya@northpeak.example';
// The local demo owner's password from README.md: made up, and only ever on localhost.
const password = 'demo-password-not-real';

// Pages are /{workspace}/{brand}/{page}; the demo's workspace and its first
// brand are both "northpeak".
const at = '/northpeak/northpeak';
const pages = {
	overview: `${at}/overview`,
	'inbox-item': `${at}/inbox/${reply}`,
	'sequences-item': `${at}/sequences/${seq}`,
	mailboxes: `${at}/mailboxes`,
	domains: `${at}/domains`,
	agents: `${at}/agents`,
};

const browser = await chromium.launch();
for (const theme of ['light', 'dark']) {
	const ctx = await browser.newContext({ viewport: { width: 1440, height: 760 }, deviceScaleFactor: 2, colorScheme: theme });
	await ctx.addInitScript((t) => {
		try {
			localStorage.setItem('theme', t);
		} catch {}
	}, theme);
	const page = await ctx.newPage();
	await page.goto(base + '/');
	const res = await page.request.post(base + '/v1/auth/login', { data: { email, password } });
	if (!res.ok()) throw new Error(`sign-in failed: ${res.status()}`);
	for (const [id, path] of Object.entries(pages)) {
		await page.goto(base + path, { waitUntil: 'networkidle' });
		await page.waitForTimeout(1500);
		await page.screenshot({ path: `${out}/${id}-${theme}.png` });
	}
	await ctx.close();
}
await browser.close();
