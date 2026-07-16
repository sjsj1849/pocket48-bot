export function parseCookieHeader(header) {
  const result = new Map();
  for (const part of String(header || '').split(';')) {
    const index = part.indexOf('=');
    if (index <= 0) continue;
    const name = part.slice(0, index).trim();
    const value = part.slice(index + 1).trim();
    if (name && value) result.set(name, value);
  }
  return result;
}

export function formatCookies(cookies) {
  const values = new Map();
  for (const cookie of cookies || []) {
    if (cookie?.name && cookie?.value) values.set(cookie.name, cookie.value);
  }
  return [...values.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([name, value]) => `${name}=${value}`)
    .join('; ');
}
