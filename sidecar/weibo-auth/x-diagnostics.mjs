const tracked = new WeakSet()
const events = []
const pathFor = raw => {
  try { const url=new URL(raw);return /(^|\.)(x|twitter)\.com$/.test(url.hostname) && /onboarding|login|auth|verification|knowledge/i.test(url.pathname) ? url.pathname : null } catch { return null }
}
export function trackXLogin(page) {
  if (tracked.has(page)) return
  tracked.add(page)
  const remember = value => {events.push({time:Date.now(),...value});if(events.length>30)events.shift()}
  page.on('response', async response => {
    const path=pathFor(response.url());if(!path)return
    const event={path,status:response.status()};remember(event)
    if (response.status()>=400) {
      try {
        const data=await response.json()
        event.errorCodes=(data.errors||[]).map(e=>e.code).filter(Number.isInteger).slice(0,5)
      }catch{}
    }
  })
  page.on('requestfailed', request => {const path=pathFor(request.url());if(path)remember({path,failed:true})})
}
export async function diagnoseXLogin(context) {
  const page=context.pages().filter(p=>{try{return /(^|\.)(x|twitter)\.com$/.test(new URL(p.url()).hostname)}catch{return false}}).at(-1)
  if(!page) return {stage:'no_x_page'}
  const dom=await page.evaluate(()=>{
    const visible=e=>e.getBoundingClientRect().height>0 && getComputedStyle(e).visibility!=='hidden'
    const dialogs=[...document.querySelectorAll('[role="dialog"],[aria-modal="true"]')].filter(visible)
    const root=dialogs.at(-1)||document.body
    const text=root.innerText
    const fields=[...root.querySelectorAll('input')].filter(visible).map(e=>({type:e.type,name:e.name,autocomplete:e.autocomplete,empty:!e.value}))
    const buttons=[...root.querySelectorAll('button')].filter(visible).filter(e=>/^(Continue|Next|Verify|Log in|Sign in|Use password|继续|下一步|验证|登录)$/i.test(e.innerText.trim())).map(e=>{const rect=e.getBoundingClientRect();return {name:e.innerText.trim(),disabled:e.disabled,withinViewport:rect.bottom<=innerHeight&&rect.top>=0}})
    return {height:innerHeight,width:innerWidth,fields,buttons,errors:{wrongInformation:/incorrect|doesn.t match|not match|couldn.t find|not found|不正确|不匹配/i.test(text),retry:/something went wrong|try again|出错|重试/i.test(text)}}
  })
  return {stage:'diagnosed',...dom,network:events.filter(e=>Date.now()-e.time<10*60_000)}
}
