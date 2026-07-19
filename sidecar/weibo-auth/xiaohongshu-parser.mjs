function unwrap(value, depth = 0) {
  if (depth > 8 || value == null || typeof value !== 'object') return value;
  if ('_value' in value && 'dep' in value) return unwrap(value._value, depth + 1);
  if ('value' in value && 'dep' in value) return unwrap(value.value, depth + 1);
  if (Array.isArray(value)) return value.map((item) => unwrap(item, depth + 1));
  const result = {};
  for (const [key, child] of Object.entries(value)) {
    if (key === 'dep' || key.startsWith('__')) continue;
    try { result[key] = unwrap(child, depth + 1); } catch {}
  }
  return result;
}

function firstURL(value) {
  if (!value) return '';
  if (typeof value === 'string') return value;
  if (Array.isArray(value)) return firstURL(value[0]);
  return String(value.urlDefault || value.url_default || value.url || value.urlPre || value.url_pre || value.infoList?.[0]?.url || '');
}

export function noteCreateTime(id) {
  if (!/^[0-9a-f]{8}/i.test(String(id || ''))) return 0;
  const seconds = Number.parseInt(String(id).slice(0, 8), 16);
  return seconds >= 1_450_000_000 ? seconds : 0;
}

export function normalizeXiaohongshuNotes(raw, userId = '', nickname = '') {
  const unwrapped = unwrap(raw);
  let list = Array.isArray(unwrapped) ? unwrapped : [];
  if (!list.length && unwrapped && typeof unwrapped === 'object') {
    for (const key of ['notes', 'data', 'list', 'value', '_value']) {
      if (Array.isArray(unwrapped[key])) { list = unwrapped[key]; break; }
    }
  }
  list = list.flatMap((item) => Array.isArray(item) ? item : [item]);
  const seen = new Set();
  return list.map((entry) => {
    const card = entry?.noteCard || entry?.note_card || entry || {};
    const id = String(entry?.id || entry?.noteId || entry?.note_id || card.noteId || card.note_id || '').trim();
    if (!id || seen.has(id)) return null;
    seen.add(id);
    const author = card.user || card.author || {};
    const imageList = card.imageList || card.image_list || card.images || [];
    const images = Array.isArray(imageList) ? imageList.map(firstURL).filter(Boolean) : [];
    const cover = firstURL(card.cover) || firstURL(card.video?.media?.stream?.h264?.[0]?.masterUrl) || images[0] || '';
    const token = String(entry?.xsecToken || entry?.xsec_token || '').trim();
    const query = token ? `?xsec_token=${encodeURIComponent(token)}&xsec_source=pc_user` : '';
    return {
      id, userId: String(author.userId || author.user_id || userId || ''),
      nickname: String(author.nickname || author.nickName || nickname || ''),
      title: String(card.displayTitle || card.display_title || card.title || '').trim(),
      desc: String(card.desc || card.description || '').trim(),
      type: String(card.type || '').toLowerCase() === 'video' ? 'video' : 'normal',
      url: `https://www.xiaohongshu.com/explore/${id}${query}`,
      cover, images, createTime: Number(card.time || card.createTime || card.create_time || noteCreateTime(id)),
    };
  }).filter(Boolean);
}

/** Walk nested state for note-card arrays (XHS sometimes nests under tabs / note queries). */
export function collectNoteCandidates(root, depth = 0, out = []) {
  if (depth > 6 || root == null) return out;
  if (Array.isArray(root)) {
    if (root.length && root.some((item) => item && typeof item === 'object' && (item.noteCard || item.note_card || item.displayTitle || item.id || item.noteId))) {
      out.push(root);
    }
    for (const item of root) collectNoteCandidates(item, depth + 1, out);
    return out;
  }
  if (typeof root !== 'object') return out;
  for (const [key, value] of Object.entries(root)) {
    if (key === 'dep' || key.startsWith('__')) continue;
    if (/note/i.test(key) && (Array.isArray(value) || (value && typeof value === 'object'))) {
      collectNoteCandidates(value, depth + 1, out);
    } else if (depth < 5) {
      collectNoteCandidates(value, depth + 1, out);
    }
  }
  return out;
}

export function extractXiaohongshuProfile(state, fallbackUserId = '') {
  const user = unwrap(state?.user || state || {});
  const page = user.userPageData || user.userInfo || {};
  const basic = page.basicInfo || page.basic_info || page;
  const nickname = String(basic.nickname || basic.nickName || basic.name || '').trim();
  let notes = normalizeXiaohongshuNotes(user.notes || page.notes || [], fallbackUserId, nickname);
  if (notes.length === 0) {
    // Fallback: deep-scan state for note arrays (SSR shape changes / tab-grouped lists).
    const bags = collectNoteCandidates(user);
    for (const bag of bags) {
      const extra = normalizeXiaohongshuNotes(bag, fallbackUserId, nickname);
      if (extra.length > notes.length) notes = extra;
    }
  }
  return {
    userId: String(basic.userId || basic.user_id || fallbackUserId || ''),
    nickname,
    notes,
  };
}
