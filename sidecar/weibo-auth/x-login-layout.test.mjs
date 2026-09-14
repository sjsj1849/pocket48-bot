import test from 'node:test'
import assert from 'node:assert/strict'
import { chromium } from 'playwright'
import { handleXLogin } from './x-login.mjs'

test('password autofill helper hidden in a login form is not a real password challenge',async()=>{
 const browser=await chromium.launch({headless:true,args:['--no-sandbox']})
 try {
  const page=await browser.newPage()
  await page.setContent('<div role="dialog"><input autocomplete="username" name="username_or_email"><input type="password" style="width:1px;height:1px;opacity:0"><button>Continue</button></div>')
  let submitted=false
  await page.exposeFunction('submitted',()=>{submitted=true})
  await page.getByRole('button').evaluate(e=>e.addEventListener('click',()=>window.submitted()))
  const wrapped={url:()=> 'https://x.com/i/jf/onboarding/web',bringToFront:()=>page.bringToFront(),locator:page.locator.bind(page),getByText:page.getByText.bind(page),getByRole:page.getByRole.bind(page)}
  const context={pages:()=>[wrapped],cookies:async()=>[]}
  assert.equal((await handleXLogin(context,'resume',{password:'fixture'})).stage,'browser_verification_required')
  assert.equal(await page.locator('input[type=password]').inputValue(),'')
  assert.equal(submitted,false)
 }finally{await browser.close()}
})
