/* Every module in the panel is reached from somewhere, and everything it
 * exports is used.
 *
 * This is here because of a real bug of exactly this shape: `nodeUpgradeAll`
 * was written, exported, documented and never called. Nothing failed, nothing
 * logged, nothing looked wrong — the button simply was not wired to it, and the
 * feature did not exist while appearing to. A bundler would have said so; the
 * panel has no build step, which is a deliberate choice and this is its cost.
 *
 * It is a static check rather than a behavioural one, so it is cheap and it
 * never flakes. It reads the source, not the running page.
 */

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { join, relative, dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));
const JS = resolve(HERE, '../panel/js');

function walk(dir) {
  const out = [];
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) out.push(...walk(p));
    else if (e.name.endsWith('.js')) out.push(p);
  }
  return out;
}

const files = walk(JS);
const sources = new Map(files.map(f => [f, readFileSync(f, 'utf8')]));

/* `export function name`, `export const name`, `export let name`,
 * `export async function name`, and `export { a, b }`. */
function exportsOf(src) {
  const names = new Set();
  for (const m of src.matchAll(/^export\s+(?:async\s+)?(?:function|const|let|var|class)\s+(\w+)/gm)) {
    names.add(m[1]);
  }
  for (const m of src.matchAll(/^export\s*\{([^}]*)\}/gm)) {
    for (const part of m[1].split(',')) {
      const name = part.trim().split(/\s+as\s+/).pop().trim();
      if (name) names.add(name);
    }
  }
  return names;
}

test('the panel has modules to check, so this test is looking in the right place', () => {
  assert.ok(files.length >= 15, `found ${files.length} modules under panel/js`);
});

test('every module is imported by another, except the entry point', () => {
  const entry = join(JS, 'main.js');
  for (const file of files) {
    if (file === entry) continue;
    const spec = relative(JS, file);
    const base = spec.split('/').pop();
    const used = [...sources].some(([other, src]) =>
      other !== file && new RegExp(`from\\s+['"][^'"]*${base.replace('.', '\\.')}['"]`).test(src));
    assert.ok(used, `${spec} is imported by nothing — it is either dead or the import was lost`);
  }
});

test('nothing is exported that nobody uses', () => {
  const problems = [];
  for (const [file, src] of sources) {
    const spec = relative(JS, file);
    for (const name of exportsOf(src)) {
      // Used by another module, by name, anywhere: an import list, a call, a
      // namespace member (api.tunnels), or a JSX-free reference.
      const used = [...sources].some(([other, otherSrc]) =>
        other !== file && new RegExp(`\\b${name}\\b`).test(otherSrc));
      if (!used) problems.push(`${spec}: ${name}`);
    }
  }
  assert.deepEqual(problems, [],
    'these are exported and referenced nowhere else — a feature that exists and ' +
    'is not reachable looks exactly like one that works');
});

/* An import that names a file which does not exist fails at load time in the
 * browser, with the whole screen blank and one line in the console. The panel
 * has no build step to catch it first. */
test('every relative import resolves to a file that exists', () => {
  for (const [file, src] of sources) {
    for (const m of src.matchAll(/from\s+['"](\.[^'"]+)['"]/g)) {
      const target = resolve(dirname(file), m[1]);
      assert.ok(sources.has(target),
        `${relative(JS, file)} imports ${m[1]}, which is not a module in this panel`);
    }
  }
});
