// Give the hand-built a_bogus endpoint a moderate recovery window. Profile
// fallbacks continue during this cooldown, one creator per poll, so throttling
// this endpoint does not pause works monitoring.
export const DOUYIN_WORKS_API_RETRY_MS = 5 * 60_000;

// Keep the scheduler fast while spreading requests across creators. With six
// creators and a 10-second tick, two per batch checks each creator every 30s.
export const DOUYIN_WORKS_BATCH_SIZE = 2;

export class DouyinWorksAccountRotation {
  constructor() { this.cursor = 0; }

  take(accounts = [], size = DOUYIN_WORKS_BATCH_SIZE) {
    const valid = accounts.filter((account) => String(account?.secUserId || '').trim());
    if (valid.length === 0) return [];
    const count = Math.min(valid.length, Math.max(1, Number(size) || 1));
    const selected = [];
    for (let index = 0; index < count; index += 1) {
      selected.push(valid[(this.cursor + index) % valid.length]);
    }
    this.cursor = (this.cursor + count) % valid.length;
    return selected;
  }
}

// When the hand-built a_bogus signature is rejected, keep the page-native
// creator feed as the source of truth. Rotate one creator per normal poll so
// recovery traffic is spread out instead of opening every profile at once.
export class DouyinWorksFallbackRotation {
  constructor() { this.cursor = 0; }

  next(accounts = []) {
    const ids = accounts
      .map((account) => String(account?.secUserId || '').trim())
      .filter(Boolean);
    if (ids.length === 0) return '';
    const selected = ids[this.cursor % ids.length];
    this.cursor = (this.cursor + 1) % ids.length;
    return selected;
  }
}
