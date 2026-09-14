import test from 'node:test';
import assert from 'node:assert/strict';
import { DouyinLiveCadence, liveDiscoveryInterval } from './douyin-live-cadence.mjs';
const at = (clock) => Date.parse(`2026-09-14T${clock}+08:00`);
test('Beijing 20:00 inclusive and 23:00 exclusive, independent of host timezone', () => {
  for (const [clock, interval] of [['00:00:00',300000],['19:59:59',300000],['20:00:00',30000],['22:59:59',30000],['23:00:00',300000]]) {
    assert.equal(liveDiscoveryInterval(at(clock)), interval);
  }
});
test('switching to evening shortens an existing daytime wait; accounts are independent', () => {
  const cadence = new DouyinLiveCadence();
  cadence.mark('a', at('19:59:00'));
  assert.equal(cadence.due('a', at('19:59:59')), false);
  assert.equal(cadence.due('a', at('20:00:00')), true);
  cadence.mark('a', at('20:00:00'));
  assert.equal(cadence.due('a', at('20:00:29')), false);
  assert.equal(cadence.due('a', at('20:00:30')), true);
  assert.equal(cadence.due('b', at('20:00:01')), true);
  cadence.mark('a', at('22:59:50'));
  assert.equal(cadence.due('a', at('23:00:30')), false);
  assert.equal(cadence.due('a', at('23:04:50')), true);
  cadence.clear('a');
  assert.equal(cadence.due('a', at('23:04:51')), true);
});
