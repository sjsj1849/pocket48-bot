const authURLPattern = /(?:passport\.weibo\.(?:com|cn)|visitor\.passport\.weibo\.cn|\/login)/i;

export function classifyWeiboProbe(kind, { status = 0, url = '', body = null } = {}) {
  if (authURLPattern.test(String(url || '')) || status === 401 || status === 403) {
    return { state: 'invalid', detail: `http=${status || 0} url=${String(url || '').slice(0, 120)}` };
  }
  if (status < 200 || status >= 300 || !body || typeof body !== 'object') {
    return { state: 'unreachable', detail: `http=${status || 0}` };
  }
  if (kind === 'web') {
    return Number(body.ok) === 1
      ? { state: 'valid', detail: 'ok=1' }
      : { state: 'invalid', detail: `ok=${body.ok ?? 'missing'}` };
  }
  if (body?.data?.login === true) return { state: 'valid', detail: 'login=true' };
  if (body?.data?.login === false || Number(body?.ok) === -100 || Number(body?.code) === -100) {
    return { state: 'invalid', detail: `login=${body?.data?.login ?? 'missing'} code=${body?.code ?? body?.ok ?? 'missing'}` };
  }
  return { state: 'invalid', detail: 'login marker missing' };
}

export function summarizeWeiboProbes(web, mobile) {
  const valid = [web, mobile].filter((item) => item?.state === 'valid').length;
  const invalid = [web, mobile].filter((item) => item?.state === 'invalid').length;
  if (valid === 2) return 'healthy';
  if (valid === 1) return 'partial';
  if (invalid > 0) return 'login_required';
  return 'network_error';
}
