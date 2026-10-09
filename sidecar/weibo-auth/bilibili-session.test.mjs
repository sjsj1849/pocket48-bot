import assert from 'node:assert/strict';
import { bilibiliSessionFromCookies } from './bilibili-session.mjs';

const now = 1_800_000_000;

function cookie(name, value, domain = '.bilibili.com', expires = -1) {
  return { name, value, domain, expires, path: '/', secure: true };
}

assert.equal(bilibiliSessionFromCookies([], now), null, '空 cookie 应返回 null');
assert.equal(
  bilibiliSessionFromCookies([cookie('SESSDATA', '')], now),
  null,
  '空值 SESSDATA 不算登录态',
);
assert.equal(
  bilibiliSessionFromCookies([cookie('SESSDATA', 'abc; injected=1')], now),
  null,
  '含分号的值必须拒绝，防止 cookie 注入',
);
assert.equal(
  bilibiliSessionFromCookies([cookie('SESSDATA', 'abc', '.example.com')], now),
  null,
  '非 bilibili 域名的 cookie 不认',
);
assert.equal(
  bilibiliSessionFromCookies([cookie('SESSDATA', 'abc', '.bilibili.com', now - 10)], now),
  null,
  '已过期的 SESSDATA 不认',
);

const session = bilibiliSessionFromCookies([
  cookie('SESSDATA', 'main-sess'),
  cookie('bili_jct', 'csrf-token', '.bilibili.com'),
  cookie('DedeUserID', '12345'),
  cookie('buvid3', 'device-only'),
  cookie('SESSDATA', 'sub-sess', 'www.bilibili.com'),
], now);

assert.ok(session, '合法 cookie 应解析出登录态');
assert.equal(session.cookies[0].value, 'main-sess', '优先取带点域名的 SESSDATA');
const names = session.cookies.map(c => c.name);
assert.ok(names.includes('bili_jct'), 'bili_jct 应一并保留');
assert.ok(names.includes('DedeUserID'), 'DedeUserID 应一并保留');
assert.ok(!names.includes('buvid3'), 'buvid3 是设备指纹，不必持久化');

console.log('bilibili-session 测试全部通过');
