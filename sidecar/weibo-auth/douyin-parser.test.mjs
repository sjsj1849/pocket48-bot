import test from 'node:test';
import assert from 'node:assert/strict';
import {
  awemeCreateTime,
  DouyinAccountBackoff,
  extractProfileLive,
  findLiveID,
  normalizeAwemeList,
  resolveDouyinProfilePosts,
} from './douyin-parser.mjs';

test('normalizes Douyin video and note posts', () => {
  const posts = normalizeAwemeList({ aweme_list: [
    { aweme_id: '1', desc: 'video', create_time: 10, author: { sec_uid: 'sec', nickname: 'A' }, video: { cover: { url_list: ['c'] } } },
    { aweme_id: '2', desc: 'note', aweme_type: 68, author: { sec_uid: 'sec' }, images: [{ url_list: ['i'] }] },
  ] }, 'sec');
  assert.deepEqual(posts.map((post) => post.type), ['video', 'note']);
  assert.equal(posts[0].cover, 'c');
  assert.deepEqual(posts[1].images, ['i']);
});

test('rejects recommended and advertising posts from other authors', () => {
  const posts = normalizeAwemeList({ aweme_list: [
    { aweme_id: 'target', author: { sec_uid: 'target-sec' } },
    { aweme_id: 'ad', author: { sec_uid: 'advertiser-sec' } },
    { aweme_id: 'unknown' },
  ] }, 'target-sec');
  assert.deepEqual(posts.map((post) => post.id), ['target']);
});

test('derives the publish time from a Douyin aweme id', () => {
  assert.equal(awemeCreateTime('7280485100071046440'), 1695120031);
  assert.equal(awemeCreateTime('invalid'), 0);
});

test('uses signed browser profile posts when the direct HTTP response is blocked', () => {
  const cards = [{ id: 'post-from-page', createTime: 123 }];
  assert.deepEqual(resolveDouyinProfilePosts(null, { cards }, 'sec'), cards);
});

test('backs off failed creators independently', () => {
  const backoff = new DouyinAccountBackoff();
  backoff.fail('creator-a', 30_000, 1_000);
  assert.equal(backoff.active('creator-a', 2_000), true);
  assert.equal(backoff.active('creator-b', 2_000), false);
  assert.equal(backoff.active('creator-a', 31_001), false);
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
