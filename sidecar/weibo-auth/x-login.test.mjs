import test from 'node:test'
import assert from 'node:assert/strict'
import { handleXLogin } from './x-login.mjs'

test('existing login avoids password entry and a new verification request', async () => {
 const result=await handleXLogin({cookies:async()=>[{domain:'.x.com',name:'auth_token',value:'auth'},{domain:'.x.com',name:'ct0',value:'csrf'}]},'start',{})
 assert.deepEqual(result,{stage:'authenticated'})
})

test('login uses foreground form; valid OTP is submitted once', async () => {
 let stage='email', lastFields=0, submits=0
 const page={url:()=> 'https://x.com/i/jf/onboarding/web',bringToFront:async()=>{},goto:async()=>{},waitForTimeout:async()=>{},
  locator:selector=>({
   count:async()=>selector.includes('password') || selector.includes('role=')?0:1,
   innerText:async()=>stage==='code'?'Enter your verification code':'Email or username',
   last:()=>({fill:async value=>{lastFields++;assert.equal(value,stage==='email'?'test@example.com':'123456')}}),
  }),
  getByRole:()=>({last:()=>({click:async()=>{submits++;stage=stage==='email'?'code':'done'}})}),
 }
 const context={pages:()=>[page],cookies:async()=>stage==='done'?[{domain:'.x.com',name:'auth_token',value:'auth'},{domain:'.x.com',name:'ct0',value:'csrf'}]:[]}
 assert.equal((await handleXLogin(context,'start',{email:'test@example.com',password:'fixture'})).stage,'awaiting_code')
 assert.equal((await handleXLogin(context,'verify',{code:'123456',password:'fixture'})).stage,'authenticated')
 assert.equal(lastFields,2)
 assert.equal(submits,2)
})
