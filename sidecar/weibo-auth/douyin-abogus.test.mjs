import test from 'node:test';
import assert from 'node:assert/strict';
import douyinABogus from './vendor/mediacrawler-douyin/sign.cjs';

test('generates a deterministic a_bogus value for a frozen browser clock', () => {
  const originalRandom = Math.random;
  const originalNow = Date.now;
  Math.random = () => 0.5;
  Date.now = () => 1718323200000;
  try {
    assert.equal(
      douyinABogus.sign_datail('aid=6383&sec_user_id=abc', 'Mozilla/5.0 test'),
      'xfmZ/RLDDi2sDDWv54dLfY3q65B3YBvb0trEMD2fkdvCzL39HMYD9exoE7kvlY8jNs/DIeYjy4hbT3ohrQ2y8qwf9W0L/25gsDSkKl12so0j53inCLf/E0iE5hsAtFH8svr4iKi8owICSYyhldAJ5kIlO62-zo0/9fW=',
    );
  } finally {
    Math.random = originalRandom;
    Date.now = originalNow;
  }
});
