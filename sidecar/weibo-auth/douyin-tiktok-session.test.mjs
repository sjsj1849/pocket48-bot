// 抖音 / TikTok 登录态判定的单测。
//
// 这些函数决定面板上那个状态灯亮不亮 —— 判据写错，灯就永远绿或者永远红，
// 而这两种都不会报错，只会让人以为「功能坏了」。
//
// 运行：node --test sidecar/weibo-auth/douyin-tiktok-session.test.mjs

import test from 'node:test';
import assert from 'node:assert/strict';
import { douyinSessionFromCookies, tiktokSessionFromCookies } from './douyin-tiktok-session.mjs';

const NOW = 1_700_000_000;
const past = NOW - 3600;
const future = NOW + 3600;

test('抖音：有 sessionid 即视为已登录', () => {
  const session = douyinSessionFromCookies(
    [{ name: 'sessionid', value: 'v', domain: '.douyin.com', expires: -1 }], NOW);
  assert.ok(session);
  assert.equal(session.cookies[0].name, 'sessionid');
});

test('抖音：sessionid_ss 也算', () => {
  const session = douyinSessionFromCookies(
    [{ name: 'sessionid_ss', value: 'v', domain: '.douyin.com', expires: -1 }], NOW);
  assert.ok(session);
});

test('抖音：iesdouyin.com 域也算（实测 sessionid 常落在这里）', () => {
  const session = douyinSessionFromCookies(
    [{ name: 'sessionid', value: 'v', domain: '.iesdouyin.com', expires: -1 }], NOW);
  assert.ok(session);
});

test('抖音：只有游客 cookie 不算登录', () => {
  // ttwid / odin_tt 游客也会拿到，拿它们当判据会让状态灯永远绿。
  assert.equal(douyinSessionFromCookies([
    { name: 'ttwid', value: 'anon', domain: '.douyin.com', expires: -1 },
    { name: 'odin_tt', value: 'x', domain: '.douyin.com', expires: -1 },
  ], NOW), null);
});

test('抖音：过期的 sessionid 不算登录', () => {
  assert.equal(douyinSessionFromCookies(
    [{ name: 'sessionid', value: 'v', domain: '.douyin.com', expires: past }], NOW), null);
});

test('抖音：未过期的 sessionid 算登录', () => {
  assert.ok(douyinSessionFromCookies(
    [{ name: 'sessionid', value: 'v', domain: '.douyin.com', expires: future }], NOW));
});

test('抖音：别的站点的 sessionid 不算', () => {
  assert.equal(douyinSessionFromCookies(
    [{ name: 'sessionid', value: 'v', domain: '.example.com', expires: -1 }], NOW), null);
});

test('抖音：过滤掉会破坏请求头的 cookie 值', () => {
  const session = douyinSessionFromCookies([
    { name: 'sessionid', value: 'good', domain: '.douyin.com', expires: -1 },
    { name: 'evil', value: 'a; b', domain: '.douyin.com', expires: -1 },
    { name: 'evil2', value: 'a\nb', domain: '.douyin.com', expires: -1 },
  ], NOW);
  assert.ok(session);
  assert.deepEqual(session.cookies.map(c => c.name), ['sessionid']);
});

test('抖音：空输入返回 null', () => {
  assert.equal(douyinSessionFromCookies([], NOW), null);
  assert.equal(douyinSessionFromCookies(undefined, NOW), null);
});

test('TikTok：有 sessionid 才算登录', () => {
  assert.ok(tiktokSessionFromCookies(
    [{ name: 'sessionid', value: 'v', domain: '.tiktok.com', expires: -1 }], NOW));
});

test('TikTok：只有 ttwid / msToken 不算（游客态）', () => {
  assert.equal(tiktokSessionFromCookies([
    { name: 'ttwid', value: 'anon', domain: '.tiktok.com', expires: -1 },
    { name: 'msToken', value: 't', domain: '.tiktok.com', expires: -1 },
  ], NOW), null);
});

test('TikTok：抖音的 sessionid 不算', () => {
  assert.equal(tiktokSessionFromCookies(
    [{ name: 'sessionid', value: 'v', domain: '.douyin.com', expires: -1 }], NOW), null);
});

test('TikTok：过期的 sessionid 不算', () => {
  assert.equal(tiktokSessionFromCookies(
    [{ name: 'sessionid', value: 'v', domain: '.tiktok.com', expires: past }], NOW), null);
});

test('TikTok：子域也算', () => {
  assert.ok(tiktokSessionFromCookies(
    [{ name: 'sessionid', value: 'v', domain: 'www.tiktok.com', expires: -1 }], NOW));
});

test('TikTok：空输入返回 null', () => {
  assert.equal(tiktokSessionFromCookies([], NOW), null);
});
