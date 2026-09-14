export function liveDiscoveryInterval(now = Date.now()) {
  const hour = new Date(now + 8 * 60 * 60_000).getUTCHours();
  return hour >= 20 && hour < 23 ? 30_000 : 300_000;
}

export class DouyinLiveCadence {
  constructor() { this.lastProbes = new Map(); }
  due(key, now = Date.now()) {
    const last = this.lastProbes.get(key);
    return last === undefined || now - last >= liveDiscoveryInterval(now);
  }
  mark(key, now = Date.now()) { this.lastProbes.set(key, now); }
  clear(key) { this.lastProbes.delete(key); }
}
