import fs from 'node:fs/promises';
import path from 'node:path';

const allowed = ['sessionid', 'csrftoken', 'ds_user_id', 'mid', 'ig_did', 'rur', 'datr'];
export function instagramSessionFromCookies(cookies, now = Date.now() / 1000) {
  const eligible = cookies.filter(c => ['instagram.com', 'www.instagram.com'].includes(String(c.domain || '').replace(/^\./, '')) && (c.expires == null || c.expires <= 0 || c.expires > now));
  const selected = {};
  for (const key of allowed) {
    const c = eligible.find(c => c.name === key && c.domain === '.instagram.com') || eligible.find(c => c.name === key);
    if (c?.value && c.value.length <= 8192 && !/[\r\n]/.test(c.value)) selected[key] = c.value;
  }
  return selected.sessionid && selected.csrftoken ? selected : null;
}
async function privateWrite(dir, name, data) {
  await fs.mkdir(dir, { recursive: true, mode: 0o700 });
  const target = path.join(dir, name), temp = `${target}.${process.pid}.tmp`;
  await fs.writeFile(temp, JSON.stringify(data), { mode: 0o600 });
  await fs.chmod(temp, 0o600);
  await fs.rename(temp, target);
}

// Browsers publish candidates, never overwrite the collector's known-good session.
// Validation and activation happen under the Python account lock and rate budget.
export function createInstagramSessionStore(context, dir, intervalMs = 60_000) {
  let queue = Promise.resolve(), closed = false, timer, debounce, lastLive;
  const serialize = fn => { const next = queue.then(fn); queue = next.catch(() => {}); return next; };
  const read = async name => { try { const file=path.join(dir,name); if ((await fs.stat(file)).mode & 0o077) return null; return JSON.parse(await fs.readFile(file,'utf8')); } catch { return null; } };
  async function syncNow(force=false) {
    const cookies = instagramSessionFromCookies(await context.cookies('https://www.instagram.com/'));
    if (!cookies) return { synced:false, reason:'browser_session_missing' };
    const fingerprint=JSON.stringify([cookies.sessionid,cookies.csrftoken]);
    if (!force && lastLive===fingerprint) return {synced:true,changed:false};
    lastLive=fingerprint;
    const saved=await read('session.json');
    if (saved?.cookies?.sessionid===cookies.sessionid && saved?.cookies?.csrftoken===cookies.csrftoken) return {synced:true,changed:false};
    const pending=await read('browser-candidate.json');
    if (pending?.cookies?.sessionid===cookies.sessionid && pending?.cookies?.csrftoken===cookies.csrftoken) return {synced:true,changed:false,pending:!pending.rejected};
    await privateWrite(dir,'browser-candidate.json',{cookies,updatedAt:Date.now()});
    await privateWrite(dir,'browser-status.json',{configured:true,pending:true,updatedAt:Date.now()});
    return {synced:true,changed:true,pending:true};
  }
  const sync = (force=false) => serialize(()=>syncNow(force));
  const restore = (force=false) => serialize(async()=>{
    const live=instagramSessionFromCookies(await context.cookies('https://www.instagram.com/'));
    if (live && !force) return {imported:false,reason:'browser_session_exists'};
    const saved=await read('session.json');
    if (!saved?.cookies?.sessionid || !saved?.cookies?.csrftoken) return {imported:false,reason:'saved_session_missing'};
    await context.addCookies(Object.entries(saved.cookies).filter(([k,v])=>allowed.includes(k)&&typeof v==='string').map(([name,value])=>({name,value,domain:'.instagram.com',path:'/',secure:true,httpOnly:['sessionid','datr','ig_did'].includes(name),sameSite:name==='csrftoken'?'Lax':'None',expires:-1})));
    lastLive=JSON.stringify([saved.cookies.sessionid,saved.cookies.csrftoken]);
    if(force)await fs.unlink(path.join(dir,'browser-candidate.json')).catch(()=>{});
    await privateWrite(dir,'browser-status.json',{configured:true,pending:false,importedAt:Date.now(),updatedAt:Date.now()});
    return {imported:true};
  });
  const onResponse = response => {let host;try{host=new URL(response.url()).hostname}catch{return}if((host==='instagram.com'||host.endsWith('.instagram.com'))&&!closed){clearTimeout(debounce);debounce=setTimeout(()=>void sync().catch(()=>{}),1500);debounce.unref?.()}};
  context.on('response',onResponse);timer=setInterval(()=>{if(!closed)void sync().catch(()=>{})},intervalMs);timer.unref?.();
  function stop(){closed=true;clearTimeout(debounce);clearInterval(timer);context.off('response',onResponse)}
  context.once('close',stop);
  async function open(){let p=context.pages().find(p=>{try{return ['instagram.com','www.instagram.com'].includes(new URL(p.url()).hostname)}catch{return false}});if(!p)p=await context.newPage();await p.bringToFront();await p.goto('https://www.instagram.com/',{waitUntil:'domcontentloaded',timeout:30000});await p.bringToFront();return {opened:true}}
  return {sync,restore,stop,open};
}
