import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { EventEmitter } from 'node:events';
import { createXSessionStore } from './x-session-store.mjs';

async function fixture(t) {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'x-session-sync-'));
  const context = new EventEmitter(); context.live = [];
  context.cookies = async () => context.live;
  context.addCookies = async cookies => { context.live = cookies; };
  const saved = { cookies: ['auth_token', 'ct0'].map(name => ({name, value: name+'-first', domain: '.x.com', expires: Date.now()/1000+86400})) };
  const file = path.join(dir, 'session.json');
  await fs.writeFile(file, JSON.stringify(saved), {mode:0o600});
  const store = createXSessionStore(context, dir, 15);
  t.after(async () => { store.stop(); await fs.rm(dir, {recursive:true,force:true}); });
  return {dir,context,saved,file,store};
}
test('restore browser session and automatically persist rotated cookies privately', async t => {
  const {context,file,store} = await fixture(t);
  assert.equal((await store.restore()).imported,true);
  assert.equal(context.live[0].httpOnly,true);
  assert.equal(context.live[0].secure,true);
  context.live = context.live.map(c=>({...c,value:c.value+'-rotated'}));
  await new Promise(r=>setTimeout(r,100));
  const saved = JSON.parse(await fs.readFile(file,'utf8'));
  assert(saved.cookies.every(c=>c.value.endsWith('-rotated')));
  assert.equal((await fs.stat(file)).mode & 0o777,0o600);
});
test('logged-out browser cannot erase saved cookies or silently restore them during sync', async t => {
  const {context,file,store} = await fixture(t);
  const before = await fs.readFile(file,'utf8');
  assert.equal((await store.sync()).synced,false);
  assert.equal(await fs.readFile(file,'utf8'),before);
  assert.equal(context.live.length,0);
});
test('boot keeps existing browser session; explicit import replaces it', async t => {
  const {context,store} = await fixture(t);
  context.live = ['auth_token','ct0'].map(name=>({name,value:'existing',domain:'.x.com',expires:-1}));
  assert.equal((await store.restore()).imported,false);
  assert.equal(context.live[0].value,'existing');
  assert.equal((await store.restore(true)).imported,true);
  assert.equal(context.live[0].value,'auth_token-first');
});
