#!/usr/bin/env node
// Change notes: one small file per pull request in changes/ (changes/README.md).
// The daily changelog on outflare.ai and the notes for each self-hosted release
// are both built from them, so no two pull requests edit the same lines.
//
//   node scripts/changes.mjs check [<base>] [--dir <repo>] [--product <path regex>]
//       Every note in changes/ is well formed. With <base>, the pull request
//       (<base>...HEAD) also adds a note when it changes what Outflare does,
//       unless PR_LABELS (comma-separated) has no-note.
//   node scripts/changes.mjs list <from> <to> [--edition open-source|cloud] [--dir <repo>]
//       The notes added between two commits, as JSON, each with the pull request
//       that added it. Lines added under Unreleased in CHANGELOG.md count too,
//       until every open pull request has moved to notes. The daily changelog on
//       outflare.ai is built from it.
//   node scripts/changes.mjs fold
//       Adds the notes not released yet to CHANGELOG.md's Unreleased section.
//       The automatic release cut runs it first (docs/releasing.md).
import { execFileSync } from 'node:child_process';
import { existsSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

export const kinds = { launch: 'Added', improvement: 'Changed', fix: 'Fixed', security: 'Security' };
export const areas = [
	'agents', 'crm', 'outreach', 'deliverability', 'inbox', 'lifecycle', 'seo', 'social', 'paid-ads',
	'partners', 'analytics', 'records', 'connectors', 'api', 'workspace', 'self-hosting', 'general',
];

const args = process.argv.slice(2);
const opt = (name, fallback) => {
	const i = args.indexOf(`--${name}`);
	if (i < 0) return fallback;
	const [, v] = args.splice(i, 2);
	return v;
};
const dir = opt('dir', new URL('..', import.meta.url).pathname);
const edition = opt('edition', 'open-source');
// Paths whose changes a person using Outflare could notice (Cloud passes its own).
const product = new RegExp(opt('product', '^(engine|web|api|mcp|sdk|cli|deploy|Dockerfile)'));
const git = (...a) => execFileSync('git', ['-C', dir, ...a], { encoding: 'utf8', maxBuffer: 64 << 20 }).trim();

// A note: front matter with kind, area and, in Cloud only, public; then the text.
// An optional "Upgrade:" paragraph goes only into the self-hosted release notes.
export function parse(text, name) {
	const m = text.match(/^---\n([\s\S]*?)\n---\n([\s\S]*)$/);
	if (!m) return { name, problems: ['must start with front matter between --- lines'] };
	const meta = Object.fromEntries(
		m[1].split('\n').filter(Boolean).map((l) => {
			const [k, ...v] = l.split(':');
			return [k.trim(), v.join(':').trim().replace(/^["']|["']$/g, '')];
		}),
	);
	const [body, upgrade] = m[2].trim().split(/\n\s*\nUpgrade:\s*/);
	const note = { name, kind: meta.kind, area: meta.area, public: meta.public === 'true', text: body.replace(/\s+/g, ' ').trim(), upgrade: upgrade?.replace(/\s+/g, ' ').trim(), problems: [] };
	if (!kinds[note.kind]) note.problems.push(`kind must be one of ${Object.keys(kinds).join(', ')}`);
	if (!areas.includes(note.area)) note.problems.push(`area must be one of ${areas.join(', ')}`);
	if (note.text.length < 10 || note.text.length > 400) note.problems.push('the text must be 10 to 400 characters: one or two sentences for the people using Outflare');
	if (note.kind === 'security' && !/^Security improvements( to [a-z ,-]+)?\.$/.test(note.text))
		note.problems.push('a security note says only "Security improvements to <area>." until the fix is live and any advisory is out (SECURITY.md)');
	return note;
}

const notesDir = join(dir, 'changes');
const isNote = (f) => /^changes\/[^/]+\.md$/.test(f) && f !== 'changes/README.md';

function check(base) {
	const problems = [];
	if (existsSync(notesDir))
		for (const f of readdirSync(notesDir).filter((f) => f.endsWith('.md') && f !== 'README.md'))
			for (const p of parse(readFileSync(join(notesDir, f), 'utf8'), f).problems) problems.push(`changes/${f}: ${p}`);
	if (base) {
		const files = git('diff', '--name-only', '--diff-filter=AM', `${base}...HEAD`).split('\n').filter(Boolean);
		const labels = (process.env.PR_LABELS ?? '').split(',').map((l) => l.trim());
		const noted = files.some(isNote) || git('diff', `${base}...HEAD`, '--', 'CHANGELOG.md').split('\n').some((l) => /^\+- /.test(l));
		if (files.some((f) => product.test(f)) && !noted && !labels.includes('no-note'))
			problems.push('This pull request changes Outflare but adds no note. Add changes/<name>.md (changes/README.md), or the no-note label if nobody using Outflare would notice.');
	}
	if (problems.length) {
		console.error(problems.join('\n'));
		process.exit(1);
	}
	console.log('Change notes are in order.');
}

// The pull request that brought a commit into <to>: the first merge on the way.
function prFor(commit, to) {
	const subjects = git('log', '--ancestry-path', '--merges', '--format=%s', `${commit}..${to}`).split('\n').reverse();
	for (const s of subjects) {
		const m = s.match(/^Merge pull request #(\d+)/);
		if (m) return Number(m[1]);
	}
	return undefined;
}

// Lines added under the Unreleased heading of CHANGELOG.md, the way notes were
// written before changes/. Kept so nothing merged during the switch is lost.
function legacy(from, to) {
	if (!existsSync(join(dir, 'CHANGELOG.md'))) return [];
	const out = [];
	for (const commit of git('log', '--format=%H', '--first-parent', `${from}..${to}`, '--', 'CHANGELOG.md').split('\n').filter(Boolean)) {
		let heading = '';
		let unreleased = false;
		for (const line of git('show', '--format=', '-U100000', '--first-parent', '-m', commit, '--', 'CHANGELOG.md').split('\n')) {
			const t = line.slice(1);
			if (/^## /.test(t)) unreleased = t.startsWith('## [Unreleased]');
			if (/^### /.test(t)) heading = t.slice(4).trim();
			if (unreleased && line.startsWith('+- ')) {
				const kind = { Added: 'launch', Changed: 'improvement', Removed: 'improvement', Fixed: 'fix', Security: 'security' }[heading] ?? 'improvement';
				let text = t.slice(2).replace(/\[([^\]]+)\]\([^)]+\)/g, '$1').replace(/`/g, '');
				// The first sentence: changelog lines were written for self-hosters and run long.
				text = text.match(/^.{10,380}?[.!?](?=\s|$)/)?.[0] ?? text.slice(0, 380);
				out.push({ kind, area: 'general', text: kind === 'security' ? 'Security improvements.' : text, pr: prOfMerge(commit) ?? prFor(commit, to), legacy: true });
			}
		}
	}
	return out;
}
const prOfMerge = (commit) => Number(git('log', '-1', '--format=%s', commit).match(/^Merge pull request #(\d+)/)?.[1]) || undefined;

function list(from, to) {
	const added = git('log', '--diff-filter=A', '--no-renames', '--format=%H', '--name-only', `${from}..${to}`, '--', 'changes');
	const notes = [];
	let commit = '';
	for (const line of added.split('\n')) {
		if (/^[0-9a-f]{40}$/.test(line)) commit = line;
		else if (isNote(line)) {
			let text;
			try {
				text = git('show', `${to}:${line}`);
			} catch {
				continue; // added and removed again within the range
			}
			const n = parse(text + '\n', line.slice(8));
			if (n.problems.length) continue; // CI stops malformed notes; never publish one
			if (edition === 'cloud' && !n.public) continue; // Cloud notes are private unless they say otherwise
			notes.push({ kind: n.kind, area: n.area, text: n.text, upgrade: n.upgrade, pr: prFor(commit, to) });
		}
	}
	// One line per text: a security fix across several pull requests reads once.
	const byText = new Map();
	for (const n of edition === 'cloud' ? notes : [...notes, ...legacy(from, to)]) {
		const key = `${n.kind}\n${n.text}`;
		const seen = byText.get(key);
		if (seen) seen.prs = [...new Set([...seen.prs, n.pr].filter(Boolean))];
		else byText.set(key, { kind: n.kind, area: n.area, edition, text: n.text, upgrade: n.upgrade, prs: n.pr ? [n.pr] : [] });
	}
	console.log(JSON.stringify({ edition, from, to, notes: [...byText.values()] }, null, 2));
}

// The commit that released the newest version: the first-parent commit that
// added CHANGELOG.md's newest "## [x.y.z]" heading. Notes added after it are
// not released yet.
function lastRelease() {
	const log = join(dir, 'CHANGELOG.md');
	const v = existsSync(log) && readFileSync(log, 'utf8').match(/^## \[(\d+\.\d+\.\d+)\]/m)?.[1];
	if (!v) return '';
	return git('log', '-1', '--first-parent', '--format=%H', `-G^## \\[${v.replace(/\./g, '\\.')}\\]`, '--', 'CHANGELOG.md');
}

// Adds the notes not released yet to CHANGELOG.md's Unreleased section, under
// Added, Changed, Fixed and Security, with upgrade steps last. The release cut
// runs this before it turns Unreleased into the next version (docs/releasing.md).
function fold() {
	const since = lastRelease();
	const range = since ? [`${since}..HEAD`] : ['HEAD'];
	const notes = [];
	const added = git('log', '--diff-filter=A', '--no-renames', '--format=', '--name-only', ...range, '--', 'changes');
	for (const f of [...new Set(added.split('\n').filter(isNote))]) {
		if (!existsSync(join(dir, f))) continue;
		const n = parse(readFileSync(join(dir, f), 'utf8'), f);
		if (!n.problems.length) notes.push(n);
	}
	if (!notes.length) return;
	const path = join(dir, 'CHANGELOG.md');
	const lines = readFileSync(path, 'utf8').split('\n');
	const start = lines.findIndex((l) => l.startsWith('## [Unreleased]'));
	if (start < 0) throw new Error('CHANGELOG.md has no ## [Unreleased] section');
	let end = lines.findIndex((l, i) => i > start && l.startsWith('## ['));
	if (end < 0) end = lines.length;
	// The section as headings with their entries, keeping what is already there.
	const sections = new Map();
	let heading = '';
	for (const l of lines.slice(start + 1, end)) {
		if (l.startsWith('### ')) heading = l.slice(4).trim();
		else if (l.trim() && heading) sections.set(heading, [...(sections.get(heading) ?? []), l]);
	}
	const add = (h, entry) => {
		const have = sections.get(h) ?? [];
		if (!have.includes(entry)) sections.set(h, [...have, entry]);
	};
	for (const n of notes) add(kinds[n.kind], `- ${n.text}`);
	for (const n of notes.filter((n) => n.upgrade)) add('Upgrading', `- ${n.upgrade}`);
	const order = ['Added', 'Changed', 'Removed', 'Fixed', 'Security', 'Upgrading'];
	const names = [...order.filter((h) => sections.has(h)), ...[...sections.keys()].filter((h) => !order.includes(h))];
	const body = names.flatMap((h) => ['', `### ${h}`, '', ...sections.get(h)]);
	lines.splice(start + 1, end - start - 1, ...body, '');
	writeFileSync(path, lines.join('\n'));
	console.log(`Added ${notes.length} change note${notes.length === 1 ? '' : 's'} to Unreleased.`);
}

const [cmd, a, b] = args;
if (cmd === 'check') check(a);
else if (cmd === 'list' && a && b) list(a, b);
else if (cmd === 'fold') fold();
else {
	console.error('usage: node scripts/changes.mjs check [<base>] | list <from> <to> [--edition cloud] [--dir <repo>] | fold');
	process.exit(2);
}
