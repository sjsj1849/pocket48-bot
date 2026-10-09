import { createHash } from 'node:crypto';

// Ported from Evil0ctal/Douyin_TikTok_Download_API's Apache-2.0
// src/dtk/signing/native/websign.py (captured from @byted/secsdk-strategy
// v1.0.40, project-id 34). This is the second signature layer required by
// protected Douyin endpoints such as /aweme/v1/web/aweme/post/.
export const DOUYIN_WEBSIGN_SALT = 'A96D855A08C0A9707F8BEF0D9A527E4E';

const UIFID_COOKIE_NAMES = [
  'uifid', 'uifid_temp', 'uifidtemp', 'UIFID', 'UIFID_TEMP', 'UIFIDTEMP',
];

function encodeComponent(value) {
  return encodeURIComponent(String(value)).replace(/[!'()~]/g, (char) =>
    `%${char.charCodeAt(0).toString(16).toUpperCase()}`);
}

function encodePairs(pairs) {
  return pairs.map(([key, value]) => `${encodeComponent(key)}=${encodeComponent(value)}`).join('&');
}

export function signDouyinProtectedParams(params, cookies = {}, timestamp = Math.floor(Date.now() / 1000)) {
  const uifid = UIFID_COOKIE_NAMES.map((name) => cookies[name]).find(Boolean) || '';
  if (!uifid) return { query: params.toString(), headers: {}, signature: '' };

  const pairs = [...params.entries()];
  const verifyFp = String(cookies.s_v_web_id || '').trim();
  if (verifyFp) {
    pairs.push(['verifyFp', verifyFp], ['fp', verifyFp]);
  }
  if (!pairs.some(([name]) => name === 'uifid')) pairs.push(['uifid', uifid]);
  const stamp = String(Math.trunc(Number(timestamp)));
  pairs.push(['timestamp', stamp]);
  const coveredQuery = encodePairs(pairs);
  const signature = createHash('md5')
    .update(`${uifid}_${stamp}_${DOUYIN_WEBSIGN_SALT}_${coveredQuery}`)
    .digest('hex');
  return {
    query: `${coveredQuery}&x-secsdk-web-signature=${signature}`,
    signature,
    headers: {
      uifid,
      'x-secsdk-web-signature': signature,
      'x-secsdk-web-expire': stamp,
    },
  };
}
