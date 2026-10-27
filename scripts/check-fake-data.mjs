#!/usr/bin/env node
// Fails when a pull request adds an email address at a real domain
// (AGENTS.md, "What may go in a public repository"). Fake people live at
// example.com or a .example, .test or .invalid domain, so nobody's real
// address, and no domain someone could register, ends up in public history.
// The few real addresses and domains allowed are in
// scripts/fake-data-allowed.txt. Geonode and Repocket are allowed as example
// brand names and websites, but never in an email address: those would be
// real people's inboxes.
//
// Checks the lines a pull request adds (git diff base...head) and its title
// and description (PR_TITLE, PR_BODY), which are as public as the code.
//
//   node scripts/check-fake-data.mjs <base> <head>
//   node scripts/check-fake-data.mjs origin/main HEAD   # before you push
//   node scripts/check-fake-data.mjs --tree <dir>       # every file under dir
//
// --tree checks a whole copy of the repository, as each release is checked
// before it is published (docs/releasing.md).

import { execFileSync } from 'node:child_process';
import { readFileSync, readdirSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const [base, head] = process.argv.slice(2);
if (!base || !head) {
	console.error('usage: node scripts/check-fake-data.mjs <base> <head> | --tree <dir>');
	process.exit(2);
}
const tree = base === '--tree' ? head : '';

const allowFile = join(dirname(fileURLToPath(import.meta.url)), 'fake-data-allowed.txt');
const allowed = readFileSync(allowFile, 'utf8')
	.split('\n')
	.map((l) => l.replace(/#.*/, '').trim().toLowerCase())
	.filter(Boolean);

// Reserved for examples and tests (RFC 2606, RFC 6761).
const reserved = /(^|\.)(example\.(com|org|net)|[a-z0-9-]+\.(example|test|invalid|localhost)|example|test|invalid|localhost)$/;
// "logo@2x.png" and "pkg@1.2.3" are file and version names, not addresses.
const notTLD = new Set(['png', 'jpg', 'jpeg', 'webp', 'gif', 'svg', 'ico', 'md', 'ts', 'tsx', 'js', 'mjs', 'cjs', 'go', 'json', 'yaml', 'yml', 'css', 'html', 'txt', 'astro', 'sql', 'sh', 'lock']);
const emailRe = /[A-Za-z0-9._%+-]+@((?:[A-Za-z0-9-]+\.)+([A-Za-z]{2,}))\b/g;

function addressOK(address, domain) {
	address = address.toLowerCase();
	domain = domain.toLowerCase();
	if (reserved.test(domain)) return true;
	return allowed.some((a) => (a.includes('@') ? address === a : domain === a || domain.endsWith('.' + a)));
}

function findings(text) {
	const out = [];
	for (const m of text.matchAll(emailRe)) {
		if (notTLD.has(m[2].toLowerCase())) continue;
		if (!addressOK(m[0], m[1])) out.push(m[0]);
	}
	return out;
}

const problems = [];

function walk(dir) {
	const out = [];
	for (const e of readdirSync(dir, { withFileTypes: true })) {
		if (e.name === '.git' || e.name === 'node_modules') continue;
		const p = join(dir, e.name);
		if (e.isDirectory()) out.push(...walk(p));
		else if (e.isFile()) out.push(p);
	}
	return out;
}

if (tree) {
	for (const path of walk(tree)) {
		const name = relative(tree, path);
		if (name.endsWith('fake-data-allowed.txt')) continue;
		const text = readFileSync(path);
		if (text.includes(0)) continue; // binary
		text.toString('utf8').split('\n').forEach((l, n) => {
			for (const f of findings(l)) problems.push(`${name}:${n + 1}: ${f}`);
		});
	}
} else {
	const diff = execFileSync('git', ['diff', '--unified=0', '--no-color', '--no-ext-diff', `${base}...${head}`], {
		encoding: 'utf8',
		maxBuffer: 256 * 1024 * 1024,
	});
	let file = '';
	let line = 0;
	for (const l of diff.split('\n')) {
		if (l.startsWith('+++ ')) {
			file = l.slice(4).replace(/^b\//, '');
		} else if (l.startsWith('@@')) {
			line = Number(/\+(\d+)/.exec(l)?.[1] ?? 0);
		} else if (l.startsWith('+')) {
			if (!file.endsWith('fake-data-allowed.txt')) {
				for (const f of findings(l.slice(1))) problems.push(`${file}:${line}: ${f}`);
			}
			line++;
		}
	}

}

for (const [name, text] of [['pull request title', process.env.PR_TITLE], ['pull request description', process.env.PR_BODY]]) {
	for (const f of findings(text ?? '')) problems.push(`${name}: ${f}`);
}

if (problems.length) {
	console.error('These look like real email addresses (AGENTS.md, "What may go in a public repository"):\n');
	for (const p of problems) console.error('  ' + p);
	console.error('\nUse a fake address at example.com or a .example, .test or .invalid domain, such as ana@acme.example.');
	console.error('A real address or domain that belongs here goes in scripts/fake-data-allowed.txt, with a reason, in a pull request a maintainer approves.');
	process.exit(1);
}
console.log(tree ? 'No real email addresses.' : 'No real email addresses added.');
