import test from 'node:test';
import assert from 'node:assert/strict';
import { extractProfileLive, findLiveID, normalizeAwemeList } from './douyin-parser.mjs';

test('normalizes Douyin video and note posts', () => {
  const posts = normalizeAwemeList({ aweme_list: [
    { aweme_id: '1', desc: 'video', create_time: 10, author: { nickname: 'A' }, video: { cover: { url_list: ['c'] } } },
    { aweme_id: '2', desc: 'note', aweme_type: 68, images: [{ url_list: ['i'] }] },
  ] }, 'sec');
  assert.deepEqual(posts.map((post) => post.type), ['video', 'note']);
  assert.equal(posts[0].cover, 'c');
  assert.deepEqual(posts[1].images, ['i']);
});

test('finds nested Douyin live id', () => {
  assert.equal(findLiveID({ roomInfo: { room: { web_rid: '386395296025' } } }), '386395296025');
});

test('does not take unrelated live ids from an offline profile', () => {
  assert.deepEqual(extractProfileLive({
    user: { nickname: 'A', live_status: 0, room_id_str: '0' },
    recommendation: { web_rid: '999999999999' },
  }), { active: false, liveId: '', nickname: 'A' });
});

test('extracts the active profile room web id from room_data', () => {
  assert.deepEqual(extractProfileLive({ user: {
    nickname: 'A',
    live_status: 1,
    room_data: JSON.stringify({ room: { web_rid: '386395296025' } }),
  } }), { active: true, liveId: '386395296025', nickname: 'A' });
});
