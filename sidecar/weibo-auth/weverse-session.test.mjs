import test from 'node:test'
import assert from 'node:assert/strict'
import { weverseSessionFromCookies, handleWeversePanel } from './weverse-session.mjs'
test('only Weverse session cookies are imported', () => {
 const session=weverseSessionFromCookies([
  {domain:'evilweverse.io',name:'we2_access_token',value:'wrong'},
  {domain:'.weverse.io',name:'we2_access_token',value:'access'},
  {domain:'weverse.io',name:'we2_refresh_token',value:'refresh'},
  {domain:'weibo.com',name:'we2_device_id',value:'wrong'},
  {domain:'weverse.io',name:'we2_device_id',value:'device'}
 ])
 assert.deepEqual(session,{accessToken:'access',refreshToken:'refresh',deviceId:'device'})
})
test('missing login is actionable and opens no extra page', async () => {
 const result=await handleWeversePanel({cookies:async()=>[]},'sync')
 assert.match(result.error,/登录/)
 assert.equal(result.session,undefined)
})
