import test from 'node:test';
import assert from 'node:assert/strict';
import { extractXiaohongshuProfile, normalizeXiaohongshuNotes, noteCreateTime } from './xiaohongshu-parser.mjs';

test('normalizes reactive user notes and preserves xsec token', () => {
  const id = '65f00000abcdef1234567890';
  const state = { user: { userPageData: { _value: { basicInfo: { nickname: '测试用户' } }, dep: {} }, notes: { _value: [{ id, xsecToken: 'token', noteCard: { displayTitle: '新笔记', type: 'normal', cover: { urlDefault: 'https://img/1.jpg' } } }], dep: {} } } };
  const profile = extractXiaohongshuProfile(state, 'user123456789012');
  assert.equal(profile.nickname, '测试用户');
  assert.equal(profile.notes[0].title, '新笔记');
  assert.match(profile.notes[0].url, /xsec_token=token/);
  assert.equal(profile.notes[0].cover, 'https://img/1.jpg');
});

test('deduplicates notes and derives ObjectId timestamp', () => {
  const id = '65f00000abcdef1234567890';
  const notes = normalizeXiaohongshuNotes([{ id }, { id }]);
  assert.equal(notes.length, 1);
  assert.equal(notes[0].createTime, noteCreateTime(id));
});

test('flattens tab-grouped profile note arrays', () => {
  const notes = normalizeXiaohongshuNotes([[{ id: '65f00000abcdef1234567890' }], [{ id: '65f00001abcdef1234567891' }]]);
  assert.equal(notes.length, 2);
});
