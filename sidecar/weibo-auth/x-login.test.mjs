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
   count:async()=>selector.includes('password') || selector.includes('role=') || selector.includes('type="tel"')?0:1,
   innerText:async()=>stage==='code'?'Enter your verification code':'Email or username',
   last:()=>({waitFor:async()=>{},fill:async value=>{lastFields++;assert.equal(value,stage==='email'?'test@example.com':'123456')}}),
  }),
  getByRole:()=>({last:()=>({click:async()=>{submits++;stage=stage==='email'?'code':'done'}})}),
 }
 const context={pages:()=>[page],cookies:async()=>stage==='done'?[{domain:'.x.com',name:'auth_token',value:'auth'},{domain:'.x.com',name:'ct0',value:'csrf'}]:[]}
 assert.equal((await handleXLogin(context,'start',{email:'test@example.com',password:'fixture'})).stage,'awaiting_code')
 assert.equal((await handleXLogin(context,'verify',{code:'123456',password:'fixture'})).stage,'authenticated')
 assert.equal(lastFields,2)
 assert.equal(submits,2)
})

test('email verification never submits an email code to SMS verification', async () => {
 const page={url:()=> 'https://x.com/i/jf/onboarding/web',bringToFront:async()=>{},locator:()=>({innerText:async()=> 'Enter verification code sent by SMS text message'})}
 const context={pages:()=>[page],cookies:async()=>[]}
 assert.equal((await handleXLogin(context,'verify',{code:'123456'})).stage,'awaiting_sms_code')
})

test('phone verification selects China before submitting the national number', async () => {
 let selected=false, submitted=false
 const field={count:async()=>1,fill:async value=>{assert(selected);assert.equal(value,'13800138000')}}
 const country={count:async()=>1,last:()=>({locator:()=>({evaluateAll:async()=>[{value:'US',text:'United States +1'},{value:'CN',text:'China +86'}]}),selectOption:async value=>{assert.equal(value,'CN');selected=true}})}
 const page={url:()=> 'https://x.com/i/jf/onboarding/web',bringToFront:async()=>{},waitForTimeout:async()=>{},
 locator:selector=>selector==='body'?{innerText:async()=> submitted?'Enter verification code sent by SMS':'Enter phone number +1'}:selector.includes('role=')?{count:async()=>0}:selector==='select:visible'?country:selector.includes('password')?{count:async()=>0}:{count:async()=>1,last:()=>field},
 getByRole:()=>({last:()=>({click:async()=>{submitted=true}})}),
 }
 const context={pages:()=>[page],cookies:async()=>[]}
 assert.equal((await handleXLogin(context,'resume',{phone:'+8613800138000'})).stage,'awaiting_sms_code')
 assert(submitted)
})

test('account confirmation can switch to password without submitting a phone number', async()=> {
 let switched=false,authenticated=false
 const page={url:()=> 'https://x.com/i/jf/onboarding/web',bringToFront:async()=>{},waitForTimeout:async()=>{},
 locator:selector=>selector==='body'?{innerText:async()=> 'Confirm your account'}:selector.includes('role=')?{count:async()=>0}:{count:async()=>selector.includes('type="tel"')?0:switched?1:0,nth:()=>({evaluate:async()=>true,fill:async value=>assert.equal(value,'fixture-password')}),last:()=>({fill:async value=>assert.equal(value,'fixture-password')})},
 getByText:()=>({count:async()=>1,last:()=>({click:async()=>{switched=true}})}),
 getByRole:(_role,options)=>options.name.source==='^Use password$'?({count:async()=>1,last:()=>({click:async()=>{switched=true}})}):({last:()=>({click:async()=>{authenticated=true}})}),
 }
 const context={pages:()=>[page],cookies:async()=>authenticated?[{domain:'.x.com',name:'auth_token',value:'auth'},{domain:'.x.com',name:'ct0',value:'csrf'}]:[]}
 assert.equal((await handleXLogin(context,'resume',{password:'fixture-password'})).stage,'authenticated')
 assert(switched)
})
