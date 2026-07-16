function firstURL(value) {
  if (!value) return '';
  if (typeof value === 'string') return value;
  const list = value.url_list || value.urlList;
  return Array.isArray(list) ? String(list[0] || '') : '';
}

export function normalizeAweme(item, secUserId = '') {
  if (!item || typeof item !== 'object') return null;
  const id = String(item.aweme_id || item.awemeId || item.id || '').trim();
  if (!id) return null;
  const images = Array.isArray(item.images) ? item.images.map(firstURL).filter(Boolean) : [];
  const cover = firstURL(item.video?.cover) || firstURL(item.video?.origin_cover)
    || firstURL(item.video?.dynamic_cover) || images[0] || '';
  const author = item.author || {};
  const type = images.length > 0 || Number(item.aweme_type) === 68 ? 'note' : 'video';
  return {
    id,
    secUserId: String(author.sec_uid || author.secUserId || secUserId || ''),
    nickname: String(author.nickname || ''),
    desc: String(item.desc || item.title || '').trim(),
    createTime: Number(item.create_time || item.createTime || 0),
    type,
    url: String(item.share_url || `https://www.douyin.com/${type}/${id}`),
    cover,
    images,
  };
}

export function normalizeAwemeList(body, secUserId = '') {
  const list = body?.aweme_list || body?.awemeList || [];
  if (!Array.isArray(list)) return [];
  const seen = new Set();
  return list.map((item) => normalizeAweme(item, secUserId)).filter((item) => {
    if (!item || seen.has(item.id)) return false;
    seen.add(item.id);
    return true;
  });
}

export function findLiveID(value, depth = 0) {
  if (!value || depth > 12) return '';
  if (Array.isArray(value)) {
    for (const item of value) {
      const found = findLiveID(item, depth + 1);
      if (found) return found;
    }
    return '';
  }
  if (typeof value !== 'object') return '';
  for (const [key, child] of Object.entries(value)) {
    if (/^(web_rid|webRid)$/i.test(key) && (typeof child === 'string' || typeof child === 'number')) {
      const id = String(child).trim();
      if (/^\d{5,}$/.test(id)) return id;
    }
  }
  for (const child of Object.values(value)) {
    const found = findLiveID(child, depth + 1);
    if (found) return found;
  }
  return '';
}

export function extractProfileLive(body) {
  const user = body?.user || body?.data?.user;
  if (!user || typeof user !== 'object') return { active: false, liveId: '', nickname: '' };
  const active = Number(user.live_status ?? user.liveStatus ?? 0) !== 0;
  const nickname = String(user.nickname || '').trim();
  if (!active) return { active: false, liveId: '', nickname };

  let roomData = user.room_data ?? user.roomData;
  if (typeof roomData === 'string') {
    try { roomData = JSON.parse(roomData); } catch { roomData = null; }
  }
  const liveId = findLiveID({
    web_rid: user.web_rid ?? user.webRid,
    roomData,
  });
  return { active: true, liveId, nickname };
}
