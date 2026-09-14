import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { EventEmitter } from 'node:events';
import { instagramSessionFromCookies, createInstagramSessionStore } from './instagram-session-store.mjs';

const cookie=(name,value)=>({name,value,domain:'.instagram.com',expires:-1});
test('Instagram selection requires auth and correct domain, rejects expired cookies',()=>{
 assert.equal(instagramSessionFromCookies([{...cookie('sessionid','a'),domain:'.evil.test'},cookie('csrftoken','b')]),null);
 assert.equal(instagramSessionFromCookies([{...cookie('sessionid','a'),expires:1},cookie('csrftoken','b')]),null);
 assert.deepEqual(instagramSessionFromCookies([cookie('sessionid','a'),cookie('csrftoken','b')]),{sessionid:'a',csrftoken:'b'});
});
test('restore imports existing session; changed browser cookie becomes candidate only; logout preserves good session',async()=>{
 const dir=await fs.mkdtemp(path.join(os.tmpdir(),'ig-session-'));
 let cookies=[];const context=new EventEmitter();context.cookies=async()=>cookies;context.addCookies=async(next)=>{cookies=next};
 const saved={username:'me',cookies:{sessionid:'good',csrftoken:'csrf'}};
 await fs.writeFile(path.join(dir,'session.json'),JSON.stringify(saved),{mode:0o600});
 const store=createInstagramSessionStore(context,dir,3600000);
 try{
  assert((await store.restore()).imported);assert.equal(cookies.find(c=>c.name==='sessionid').value,'good');
  cookies=cookies.map(c=>c.name==='csrftoken'?{...c,value:'new-csrf'}:c);assert((await store.sync()).pending);
  assert.deepEqual(JSON.parse(await fs.readFile(path.join(dir,'session.json'),'utf8')),saved);
  assert.equal(JSON.parse(await fs.readFile(path.join(dir,'browser-candidate.json'),'utf8')).cookies.csrftoken,'new-csrf');
  assert.equal((await fs.stat(path.join(dir,'browser-candidate.json'))).mode&0o777,0o600);
  cookies=[];assert.equal((await store.sync()).synced,false);assert.deepEqual(JSON.parse(await fs.readFile(path.join(dir,'session.json'),'utf8')),saved);
 }finally{store.stop();await fs.rm(dir,{recursive:true,force:true})}
});
test('automatic timer publishes changed cookies without network requests',async()=>{
 const dir=await fs.mkdtemp(path.join(os.tmpdir(),'ig-auto-'));
 let cookies=[cookie('sessionid','good'),cookie('csrftoken','csrf')];const context=new EventEmitter();context.cookies=async()=>cookies;context.addCookies=async(next)=>{cookies=next};
 const saved={username:'me',cookies:{sessionid:'good',csrftoken:'csrf'}};
 await fs.writeFile(path.join(dir,'session.json'),JSON.stringify(saved),{mode:0o600});
 const store=createInstagramSessionStore(context,dir,10);
 try{
  await store.restore();cookies=cookies.map(c=>c.name==='csrftoken'?{...c,value:'rotated'}:c);
  for(let i=0;i<50;i++){try{await fs.access(path.join(dir,'browser-candidate.json'));break}catch{await new Promise(r=>setTimeout(r,10))}}
  assert.equal(JSON.parse(await fs.readFile(path.join(dir,'browser-candidate.json'),'utf8')).cookies.csrftoken,'rotated');
  assert.deepEqual(JSON.parse(await fs.readFile(path.join(dir,'session.json'),'utf8')),saved);
 }finally{store.stop();await fs.rm(dir,{recursive:true,force:true})}
});
