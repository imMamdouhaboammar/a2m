import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';

const source = fs.readFileSync(new URL('../UI/a2m-desktop-v2/app.js', import.meta.url), 'utf8');
const match = source.match(/const isHealthy = \/\^(.+?)\$\/([a-z]*)\.test\(status/);
assert.ok(match, 'dashboard health predicate is discoverable');
const healthPattern = new RegExp('^' + match[1] + '$', match[2]);

for (const [status, expected] of [
  ['running', true],
  ['healthy', true],
  ['reachable', true],
  ['ready (on-demand)', true],
  ['installed (on-demand)', true],
  ['ready', true],
  ['unavailable', false],
  ['not monitored', false],
]) {
  test('dashboard recognizes status: ' + status, () => {
    assert.equal(healthPattern.test(status), expected);
  });
}
