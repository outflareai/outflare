// Tests for scripts/changes.mjs fold: node --test scripts/changes.test.mjs
// Each builds a small repository with the shapes main really has (release
// branches merged with merge commits, notes written in merges) and folds it.
import { execFileSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { after, test } from 'node:test';
import assert from 'node:assert/strict';

const script = new URL('changes.mjs', import.meta.url).pathname;
const dirs = [];
after(() => dirs.forEach((d) => rmSync(d, { recursive: true, force: true })));

function repo() {
	const dir = mkdtempSync(join(tmpdir(), 'changes-test-'));
	dirs.push(dir);
	const env = { ...process.env, GIT_AUTHOR_NAME: 'Test', GIT_AUTHOR_EMAIL: 'test@example.com', GIT_COMMITTER_NAME: 'Test', GIT_COMMITTER_EMAIL: 'test@example.com' };
	const git = (...a) => execFileSync('git', ['-C', dir, '-c', 'commit.gpgsign=false', ...a], { encoding: 'utf8', env }).trim();
	const write = (f, text) => writeFileSync(join(dir, f), text);
	const note = (name, text) => write(`changes/${name}.md`, `---\nkind: launch\narea: general\n---\n${text}\n`);
	const commit = (msg) => {
		git('add', '-A');
		git('commit', '-q', '-m', msg);
	};
	// A pull request: a branch off main with one commit, merged with a merge commit.
	const pr = (branch, fn) => {
		git('checkout', '-q', '-b', branch, 'main');
		fn();
		commit(branch);
		git('checkout', '-q', 'main');
		git('merge', '-q', '--no-ff', '-m', `Merge pull request from ${branch}`, branch);
	};
	// The release cut: fold on main, then a "Release vX" commit on release/next
	// that turns Unreleased into the version. It isn't merged yet.
	const cut = (v) => {
		const out = fold();
		git('checkout', '-q', '-B', 'release/next', 'main');
		write('CHANGELOG.md', changelog().replace('## [Unreleased]\n', `## [Unreleased]\n\n## [${v}] - 2026-10-29\n`));
		commit(`Release v${v}`);
		git('checkout', '-q', '-f', 'main');
		return out;
	};
	const land = () => git('merge', '-q', '--no-ff', '-m', 'Merge pull request from release/next', 'release/next');
	const fold = () => execFileSync('node', [script, 'fold', '--dir', dir], { encoding: 'utf8' }).trim();
	const changelog = () => readFileSync(join(dir, 'CHANGELOG.md'), 'utf8');
	const unreleased = () => changelog().split(/^## \[Unreleased\]\n/m)[1].split(/^## \[/m)[0];

	git('init', '-q', '-b', 'main');
	mkdirSync(join(dir, 'changes'));
	write('CHANGELOG.md', '# Changelog\n\n## [Unreleased]\n\n## [0.1.0] - 2026-10-28\n\n### Added\n\n- The first release.\n');
	write('changes/README.md', 'Change notes.\n');
	commit('Start');
	return { git, write, note, commit, pr, cut, land, fold, changelog, unreleased };
}

test('a pull request merged between the cut and the release is in the next release', () => {
	const r = repo();
	r.pr('a', () => r.note('a', 'Note A is in the first release.'));
	r.cut('0.2.0');
	r.pr('b', () => r.note('b', 'Note B merged after the cut.'));
	r.land();
	r.fold();
	assert.match(r.unreleased(), /- Note B merged after the cut\./);
	assert.doesNotMatch(r.unreleased(), /Note A/);
});

test('no note is folded into two releases', () => {
	const r = repo();
	r.pr('a', () => r.note('a', 'Note A is in the first release.'));
	r.cut('0.2.0');
	r.pr('b', () => r.note('b', 'Note B merged after the cut.'));
	r.land();
	r.cut('0.3.0');
	r.land();
	assert.equal(r.fold(), '');
	assert.equal(r.unreleased().trim(), '');
	const released = r.changelog();
	assert.equal(released.match(/Note A/g).length, 1);
	assert.equal(released.match(/Note B/g).length, 1);
	assert.match(released.split('## [0.2.0]')[0], /Note B/); // in 0.3.0, above 0.2.0
});

test('a release branch that never merged changes nothing', () => {
	const r = repo();
	r.pr('a', () => r.note('a', 'Note A waits for its release.'));
	r.cut('0.2.0');
	r.pr('b', () => r.note('b', 'Note B comes later.'));
	r.fold();
	assert.match(r.unreleased(), /Note A waits/);
	assert.match(r.unreleased(), /Note B comes later/);
});

test('a note written in a merge commit is folded', () => {
	const r = repo();
	r.git('checkout', '-q', '-b', 'c', 'main');
	r.write('c.txt', 'c\n');
	r.commit('Work without its note');
	r.git('checkout', '-q', 'main');
	r.git('merge', '-q', '--no-ff', '--no-commit', 'c');
	r.note('c', 'Note C written while merging.');
	r.commit('Merge pull request from c');
	r.fold();
	assert.match(r.unreleased(), /Note C written while merging/);
});

test('a branch that merges main after a release keeps its own notes', () => {
	const r = repo();
	r.git('checkout', '-q', '-b', 'batch', 'main');
	r.note('d', 'Note D on a long-lived branch.');
	r.commit('D');
	r.git('checkout', '-q', 'main');
	r.cut('0.2.0');
	r.land();
	r.git('checkout', '-q', 'batch');
	r.git('merge', '-q', '--no-ff', '-m', 'Merge main into the batch', 'main');
	r.git('checkout', '-q', 'main');
	r.git('merge', '-q', '--ff-only', 'batch');
	r.fold();
	assert.match(r.unreleased(), /Note D on a long-lived branch/);
});
