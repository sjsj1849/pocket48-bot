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

test('X custom country dropdown selects +86 and submits the national number once',async()=>{
 const browser=await chromium.launch({headless:true,args:['--no-sandbox']})
 try {
  const page=await browser.newPage()
  await page.setContent(`<div role="dialog" style="width:600px"><button id="prefix" onclick="document.querySelector('#countries').hidden=false">+1</button><input type="tel" id="phone"><div hidden id="countries"><input placeholder="Search"><button onclick="document.querySelector('#prefix').textContent='+86';document.querySelector('#countries').hidden=true">China</button></div><button id="submit">Continue</button></div>`)
  await page.locator('#submit').evaluate(e=>e.addEventListener('click',()=>{window.submittedNumber=document.querySelector('#phone').value;document.body.innerHTML='Enter verification code'}))
  const wrapped={url:()=> 'https://x.com/i/jf/onboarding/web',bringToFront:()=>page.bringToFront(),locator:page.locator.bind(page),getByText:page.getByText.bind(page),getByRole:page.getByRole.bind(page),getByPlaceholder:page.getByPlaceholder.bind(page),waitForTimeout:()=>page.waitForTimeout(20)}
  const context={pages:()=>[wrapped],cookies:async()=>[]}
  assert.equal((await handleXLogin(context,'resume',{phone:'+8613800138000'})).stage,'awaiting_sms_code')
  assert.equal(await page.evaluate(()=>window.submittedNumber),'13800138000')
 }finally{await browser.close()}
})
